package serverd

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveRelativeNoSymlinksRejectsInvalidComponents(t *testing.T) {
	root := t.TempDir()
	for _, relative := range []string{
		"", ".", "..", "../outside", "cartella/../outside",
		"cartella//file.txt", "cartella/./file.txt", "/assoluto",
	} {
		t.Run(relative, func(t *testing.T) {
			if _, err := resolveRelativeNoSymlinks(root, relative); !errors.Is(err, errInvalidPath) {
				t.Fatalf("percorso %q: atteso errInvalidPath, ottenuto %v", relative, err)
			}
		})
	}
}

func TestResolveRelativeNoSymlinksRejectsAncestorSymlink(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("segreto"), 0o600); err != nil {
		t.Fatal(err)
	}
	links := filepath.Join(root, "collegamenti")
	if err := os.Mkdir(links, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(links, "esterno")); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveRelativeNoSymlinks(root, "collegamenti/esterno/secret.txt"); !errors.Is(err, errInvalidPath) {
		t.Fatalf("symlink antenato non rifiutato: %v", err)
	}
}

func requestWebPath(ctx *Context, endpoint, relative string) *httptest.ResponseRecorder {
	query := url.Values{"path": []string{relative}}
	req := httptest.NewRequest(http.MethodGet, endpoint+"?"+query.Encode(), nil)
	recorder := httptest.NewRecorder()
	ctx.webHandler().ServeHTTP(recorder, req)
	return recorder
}

func TestWebBrowseAllowsRegularDirectory(t *testing.T) {
	ctx, share := newTestContext(t)
	dir := filepath.Join(share, "documenti")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "visibile.txt"), []byte("ok"), 0o600); err != nil {
		t.Fatal(err)
	}
	recorder := requestWebPath(ctx, "/api/browse", "documenti")
	if recorder.Code != http.StatusOK {
		t.Fatalf("browse cartella regolare: status %d, body %q", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "visibile.txt") {
		t.Fatalf("contenuto della cartella assente: %q", recorder.Body.String())
	}
}

func TestWebBrowseRejectsSymlinkAncestor(t *testing.T) {
	ctx, share := newTestContext(t)
	outside := t.TempDir()
	nested := filepath.Join(outside, "nested")
	if err := os.Mkdir(nested, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nested, "secret.txt"), []byte("segreto"), 0o600); err != nil {
		t.Fatal(err)
	}
	links := filepath.Join(share, "collegamenti")
	if err := os.Mkdir(links, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(links, "esterno")); err != nil {
		t.Fatal(err)
	}

	recorder := requestWebPath(ctx, "/api/browse", "collegamenti/esterno/nested")
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("browse tramite symlink: status %d, body %q", recorder.Code, recorder.Body.String())
	}
	if strings.Contains(recorder.Body.String(), "secret.txt") {
		t.Fatalf("il browse ha esposto la directory esterna: %q", recorder.Body.String())
	}
}

func TestWebDownloadRejectsSymlinkAncestor(t *testing.T) {
	ctx, share := newTestContext(t)
	outside := t.TempDir()
	const secret = "contenuto-esterno"
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte(secret), 0o600); err != nil {
		t.Fatal(err)
	}
	links := filepath.Join(share, "collegamenti")
	if err := os.Mkdir(links, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(links, "esterno")); err != nil {
		t.Fatal(err)
	}

	recorder := requestWebPath(ctx, "/api/download", "collegamenti/esterno/secret.txt")
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("download tramite symlink: status %d, body %q", recorder.Code, recorder.Body.String())
	}
	if strings.Contains(recorder.Body.String(), secret) {
		t.Fatalf("il download ha esposto il file esterno: %q", recorder.Body.String())
	}
}

func TestWebDownloadAllowsRegularAndTrashFiles(t *testing.T) {
	ctx, share := newTestContext(t)
	files := []struct {
		relative string
		content  string
	}{
		{relative: "documenti/file.txt", content: "file-regolare"},
		{relative: ".sync-trash/documenti/file.txt-2026-11-26--10-00-00-000000Z", content: "versione-trash"},
	}
	for _, file := range files {
		full := filepath.Join(share, filepath.FromSlash(file.relative))
		if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(file.content), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	for _, file := range files {
		t.Run(file.relative, func(t *testing.T) {
			recorder := requestWebPath(ctx, "/api/download", file.relative)
			if recorder.Code != http.StatusOK {
				t.Fatalf("download %q: status %d, body %q", file.relative, recorder.Code, recorder.Body.String())
			}
			if recorder.Body.String() != file.content {
				t.Fatalf("download %q: contenuto %q, atteso %q", file.relative, recorder.Body.String(), file.content)
			}
		})
	}
}

func TestWebDownloadRejectsSymlinkTrash(t *testing.T) {
	ctx, share := newTestContext(t)
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("segreto"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(share, ".sync-trash")); err != nil {
		t.Fatal(err)
	}

	recorder := requestWebPath(ctx, "/api/download", ".sync-trash/secret.txt")
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("download da .sync-trash symlink: status %d, body %q", recorder.Code, recorder.Body.String())
	}
}
