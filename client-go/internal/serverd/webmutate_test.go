package serverd

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func webMutationRequest(t *testing.T, method, endpoint string, values url.Values) *http.Request {
	t.Helper()
	req := httptest.NewRequest(method, endpoint+"?"+values.Encode(), nil)
	req.Header.Set("X-NASBox-Web", "1")
	req.SetBasicAuth("andrea", "segreta")
	return req
}

func writeWebTestFile(t *testing.T, root, relative, content string) {
	t.Helper()
	full := filepath.Join(root, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestWebDeleteMovesToTrashAndJournals(t *testing.T) {
	ctx, share := newWebTestContext(t, "segreta")
	writeWebTestFile(t, share, "documenti/nota.txt", "da eliminare")

	req := webMutationRequest(t, http.MethodPost, "/api/delete", url.Values{
		"path": {"documenti/nota.txt"}, "confirm": {"1"},
	})
	rec := doWeb(ctx, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(filepath.Join(share, "documenti", "nota.txt")); !os.IsNotExist(err) {
		t.Fatalf("il file è ancora presente: %v", err)
	}
	trash, err := filepath.Glob(filepath.Join(share, ".sync-trash", "documenti", "nota.txt-*"))
	if err != nil || len(trash) != 1 {
		t.Fatalf("copia nel cestino mancante: %v %v", trash, err)
	}
	if data, err := os.ReadFile(trash[0]); err != nil || string(data) != "da eliminare" {
		t.Fatalf("contenuto nel cestino: %q %v", data, err)
	}
	if journal := readString(ctx.paths().Journal); !strings.Contains(journal, "DELETE\tdocumenti/nota.txt") {
		t.Fatalf("DELETE non journalata: %q", journal)
	}
	if _, err := os.Stat(ctx.paths().Owner); err == nil {
		t.Fatal("owner del lease non rimosso")
	}
}

func TestWebDeleteDirectoryJournalsEveryFile(t *testing.T) {
	ctx, share := newWebTestContext(t, "segreta")
	writeWebTestFile(t, share, "cartella/a.txt", "a")
	writeWebTestFile(t, share, "cartella/sotto/b.txt", "b")
	if err := os.MkdirAll(filepath.Join(share, "cartella", "vuota"), 0o700); err != nil {
		t.Fatal(err)
	}

	rec := doWeb(ctx, webMutationRequest(t, http.MethodPost, "/api/delete", url.Values{
		"path": {"cartella"}, "confirm": {"1"},
	}))
	if rec.Code != http.StatusOK {
		t.Fatalf("delete cartella: %d %s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(filepath.Join(share, "cartella")); !os.IsNotExist(err) {
		t.Fatalf("la cartella è ancora presente: %v", err)
	}
	trash, _ := filepath.Glob(filepath.Join(share, ".sync-trash", "cartella-*"))
	if len(trash) != 1 {
		t.Fatalf("cestino della cartella mancante: %v", trash)
	}
	for _, relative := range []string{"a.txt", "sotto/b.txt"} {
		if _, err := os.Stat(filepath.Join(trash[0], filepath.FromSlash(relative))); err != nil {
			t.Fatalf("%s non è nel cestino: %v", relative, err)
		}
	}
	journal := readString(ctx.paths().Journal)
	for _, path := range []string{"cartella/a.txt", "cartella/sotto/b.txt"} {
		if !strings.Contains(journal, "DELETE\t"+path) {
			t.Fatalf("DELETE mancante per %s: %q", path, journal)
		}
	}
}

func TestWebDeleteRejectsTraversalSymlinkAndInternalPaths(t *testing.T) {
	ctx, share := newWebTestContext(t, "segreta")
	outside := t.TempDir()
	outsideFile := filepath.Join(outside, "segreto.txt")
	if err := os.WriteFile(outsideFile, []byte("segreto"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(share, "collegamento.txt")
	if err := os.Symlink(outsideFile, link); err != nil {
		t.Fatal(err)
	}
	for _, relative := range []string{"../fuori", ".sync-trash", ".nasbox-staging", "@eaDir", "collegamento.txt"} {
		rec := doWeb(ctx, webMutationRequest(t, http.MethodPost, "/api/delete", url.Values{
			"path": {relative}, "confirm": {"1"},
		}))
		if rec.Code == http.StatusOK {
			t.Fatalf("percorso %q accettato", relative)
		}
	}
	if _, err := os.Stat(outsideFile); err != nil {
		t.Fatalf("il file esterno è stato toccato: %v", err)
	}
}

func TestWebDeleteRequiresConfirmation(t *testing.T) {
	ctx, share := newWebTestContext(t, "segreta")
	writeWebTestFile(t, share, "nota.txt", "x")
	rec := doWeb(ctx, webMutationRequest(t, http.MethodPost, "/api/delete", url.Values{
		"path": {"nota.txt"},
	}))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("delete senza conferma: %d %s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(filepath.Join(share, "nota.txt")); err != nil {
		t.Fatalf("il file è stato eliminato senza conferma: %v", err)
	}
}

func TestWebRenameFileMovesManifestAndKeepsContent(t *testing.T) {
	ctx, share := newWebTestContext(t, "segreta")
	writeWebTestFile(t, share, "documenti/uno.txt", "contenuto")

	rec := doWeb(ctx, webMutationRequest(t, http.MethodPost, "/api/rename", url.Values{
		"path": {"documenti/uno.txt"}, "name": {"due.txt"},
	}))
	if rec.Code != http.StatusOK {
		t.Fatalf("rename: %d %s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(filepath.Join(share, "documenti", "uno.txt")); !os.IsNotExist(err) {
		t.Fatalf("il vecchio nome è ancora presente: %v", err)
	}
	if data, err := os.ReadFile(filepath.Join(share, "documenti", "due.txt")); err != nil || string(data) != "contenuto" {
		t.Fatalf("contenuto dopo rename: %q %v", data, err)
	}
	row, ok, err := ctx.lookupManifest("documenti/due.txt")
	if err != nil || !ok || !strings.HasPrefix(row, "documenti/due.txt\t") {
		t.Fatalf("manifest dopo rename: %q ok=%v err=%v", row, ok, err)
	}
	// A deleted path stays as a tombstone until its TTL, but it must no longer
	// carry a digest: clients see it as REMOVED.
	if old, ok, _ := ctx.lookupManifest("documenti/uno.txt"); ok {
		fields := strings.Split(old, "\t")
		if len(fields) < 2 || fields[1] != "" {
			t.Fatalf("il vecchio percorso è ancora vivo nel manifest: %q", old)
		}
	}
}

func TestWebRenameRefusesOverwriteAndInvalidName(t *testing.T) {
	ctx, share := newWebTestContext(t, "segreta")
	writeWebTestFile(t, share, "uno.txt", "uno")
	writeWebTestFile(t, share, "due.txt", "due")

	rec := doWeb(ctx, webMutationRequest(t, http.MethodPost, "/api/rename", url.Values{
		"path": {"uno.txt"}, "name": {"due.txt"},
	}))
	if rec.Code != http.StatusConflict {
		t.Fatalf("collisione: %d %s", rec.Code, rec.Body.String())
	}
	if data, err := os.ReadFile(filepath.Join(share, "due.txt")); err != nil || string(data) != "due" {
		t.Fatalf("la collisione ha sovrascritto: %q %v", data, err)
	}
	for _, name := range []string{"../fuori", "a/b", ".sync-trash", "@eaDir", ""} {
		rec := doWeb(ctx, webMutationRequest(t, http.MethodPost, "/api/rename", url.Values{
			"path": {"uno.txt"}, "name": {name},
		}))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("nome %q: %d %s", name, rec.Code, rec.Body.String())
		}
	}
}

func TestWebRenameDirectory(t *testing.T) {
	ctx, share := newWebTestContext(t, "segreta")
	writeWebTestFile(t, share, "cartella/a.txt", "a")
	writeWebTestFile(t, share, "cartella/sotto/b.txt", "b")

	rec := doWeb(ctx, webMutationRequest(t, http.MethodPost, "/api/rename", url.Values{
		"path": {"cartella"}, "name": {"rinominata"},
	}))
	if rec.Code != http.StatusOK {
		t.Fatalf("rename cartella: %d %s", rec.Code, rec.Body.String())
	}
	for _, relative := range []string{"a.txt", "sotto/b.txt"} {
		if _, err := os.Stat(filepath.Join(share, "rinominata", filepath.FromSlash(relative))); err != nil {
			t.Fatalf("%s non è stato spostato: %v", relative, err)
		}
	}
	row, ok, err := ctx.lookupManifest("rinominata/sotto/b.txt")
	if err != nil || !ok || !strings.HasPrefix(row, "rinominata/sotto/b.txt\t") {
		t.Fatalf("manifest dopo rename cartella: %q ok=%v err=%v", row, ok, err)
	}
}

func TestWebMkdirCreatesFolderAndRefusesCollision(t *testing.T) {
	ctx, share := newWebTestContext(t, "segreta")
	rec := doWeb(ctx, webMutationRequest(t, http.MethodPost, "/api/mkdir", url.Values{
		"path": {""}, "name": {"Nuova"},
	}))
	if rec.Code != http.StatusOK {
		t.Fatalf("mkdir: %d %s", rec.Code, rec.Body.String())
	}
	if info, err := os.Stat(filepath.Join(share, "Nuova")); err != nil || !info.IsDir() {
		t.Fatalf("cartella non creata: %v", err)
	}
	rec = doWeb(ctx, webMutationRequest(t, http.MethodPost, "/api/mkdir", url.Values{
		"path": {""}, "name": {"Nuova"},
	}))
	if rec.Code != http.StatusConflict {
		t.Fatalf("collisione mkdir: %d %s", rec.Code, rec.Body.String())
	}
	for _, name := range []string{"../fuori", "a/b", ".sync-trash", "@eaDir", ""} {
		rec = doWeb(ctx, webMutationRequest(t, http.MethodPost, "/api/mkdir", url.Values{
			"path": {""}, "name": {name},
		}))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("nome %q: %d %s", name, rec.Code, rec.Body.String())
		}
	}
}

func TestWebMutationsRequirePasswordAndAntiCSRFHeader(t *testing.T) {
	open, share := newWebTestContext(t, "")
	writeWebTestFile(t, share, "nota.txt", "x")
	req := httptest.NewRequest(http.MethodPost, "/api/delete?path=nota.txt&confirm=1", nil)
	if rec := doWeb(open, req); rec.Code != http.StatusForbidden {
		t.Fatalf("delete senza password: %d %s", rec.Code, rec.Body.String())
	}

	ctx, _ := newWebTestContext(t, "segreta")
	req = webMutationRequest(t, http.MethodPost, "/api/delete", url.Values{
		"path": {"nota.txt"}, "confirm": {"1"},
	})
	req.Header.Del("X-NASBox-Web")
	if rec := doWeb(ctx, req); rec.Code != http.StatusForbidden {
		t.Fatalf("delete senza header anti-CSRF: %d %s", rec.Code, rec.Body.String())
	}
}

func TestWebEntryStatsReportsDirectory(t *testing.T) {
	ctx, share := newWebTestContext(t, "segreta")
	writeWebTestFile(t, share, "cartella/a.txt", "12345")
	writeWebTestFile(t, share, "cartella/b.txt", "123")
	rec := doWeb(ctx, webMutationRequest(t, http.MethodGet, "/api/entry", url.Values{
		"path": {"cartella"},
	}))
	if rec.Code != http.StatusOK {
		t.Fatalf("entry: %d %s", rec.Code, rec.Body.String())
	}
	var info webEntryInfo
	if err := json.Unmarshal(rec.Body.Bytes(), &info); err != nil {
		t.Fatal(err)
	}
	if info.Kind != "DIR" || info.Files != 2 || info.Bytes != 8 {
		t.Fatalf("statistiche: %+v", info)
	}
}
