package serverd

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func shareRequest(method, query string) *http.Request {
	req := httptest.NewRequest(method, "/api/share?"+query, nil)
	req.SetBasicAuth("andrea", "segreta")
	req.Header.Set("X-NASBox-Web", "1")
	return req
}

func createShare(t *testing.T, ctx *Context, path, days string) shareView {
	t.Helper()
	rec := doWeb(ctx, shareRequest(http.MethodPost, "path="+path+"&days="+days))
	if rec.Code != http.StatusOK {
		t.Fatalf("creazione link: %d %s", rec.Code, rec.Body.String())
	}
	var view shareView
	if err := jsonDecode(rec.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	return view
}

func TestShareLinkDownloadsWithoutPasswordThenExpiresAndRevokes(t *testing.T) {
	ctx, share := newWebTestContext(t, "segreta")
	_ = os.MkdirAll(filepath.Join(share, "Doc"), 0o700)
	_ = os.WriteFile(filepath.Join(share, "Doc", "contratto.pdf"), []byte("contenuto"), 0o600)

	view := createShare(t, ctx, "Doc/contratto.pdf", "7")
	if !strings.HasPrefix(view.URL, "http://example.com/s/") || len(view.Token) < 30 {
		t.Fatalf("link inatteso: %+v", view)
	}
	// Chiunque lo scarica, senza password, sempre come allegato.
	rec := doWeb(ctx, httptest.NewRequest(http.MethodGet, "/s/"+view.Token, nil))
	if rec.Code != http.StatusOK || rec.Body.String() != "contenuto" {
		t.Fatalf("download pubblico: %d %q", rec.Code, rec.Body.String())
	}
	if !strings.HasPrefix(rec.Header().Get("Content-Disposition"), "attachment") || rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("il link deve servire un allegato: %v", rec.Header())
	}
	// Il contatore dei download sale e l'elenco lo mostra.
	list := doWeb(ctx, shareRequest(http.MethodGet, ""))
	if !strings.Contains(list.Body.String(), `"downloads":1`) {
		t.Fatalf("elenco: %s", list.Body.String())
	}
	// Revoca: poi 404.
	if rec := doWeb(ctx, shareRequest(http.MethodDelete, "token="+view.Token)); rec.Code != http.StatusOK {
		t.Fatalf("revoca: %d", rec.Code)
	}
	if rec := doWeb(ctx, httptest.NewRequest(http.MethodGet, "/s/"+view.Token, nil)); rec.Code != http.StatusNotFound {
		t.Fatalf("link revocato: %d", rec.Code)
	}
}

func TestShareLinkExpiryAndSafetyRules(t *testing.T) {
	ctx, share := newWebTestContext(t, "segreta")
	_ = os.WriteFile(filepath.Join(share, "a.txt"), []byte("a"), 0o600)
	_ = os.Mkdir(filepath.Join(share, "cartella"), 0o700)

	// Un link scaduto sparisce.
	store := ctx.shares()
	token, _, err := store.create("a.txt", 1, time.Now().Add(-48*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if rec := doWeb(ctx, httptest.NewRequest(http.MethodGet, "/s/"+token, nil)); rec.Code != http.StatusNotFound {
		t.Fatalf("link scaduto: %d", rec.Code)
	}
	// Solo file reali, mai cartelle, mai percorsi interni o fuori dalla share.
	for query, want := range map[string]int{
		"path=cartella&days=7":        http.StatusBadRequest,
		"path=../fuori&days=7":        http.StatusBadRequest,
		"path=.sync-trash/x&days=7":   http.StatusBadRequest,
		"path=inesistente.txt&days=7": http.StatusNotFound,
		"path=a.txt&days=999":         http.StatusBadRequest,
	} {
		if rec := doWeb(ctx, shareRequest(http.MethodPost, query)); rec.Code != want {
			t.Fatalf("%s: %d, atteso %d", query, rec.Code, want)
		}
	}
	// Senza password né creazione né elenco; senza header anti-CSRF niente scrittura.
	open, openShare := newWebTestContext(t, "")
	_ = os.WriteFile(filepath.Join(openShare, "a.txt"), []byte("a"), 0o600)
	for _, method := range []string{http.MethodPost, http.MethodGet} {
		req := httptest.NewRequest(method, "/api/share?path=a.txt&days=7", nil)
		if rec := doWeb(open, req); rec.Code != http.StatusForbidden {
			t.Fatalf("%s senza password: %d", method, rec.Code)
		}
	}
	noHeader := shareRequest(http.MethodPost, "path=a.txt&days=7")
	noHeader.Header.Del("X-NASBox-Web")
	if rec := doWeb(ctx, noHeader); rec.Code != http.StatusForbidden {
		t.Fatalf("senza header anti-CSRF: %d", rec.Code)
	}
}

func TestShareTokenGuessingIsRateLimitedAndSymlinkedFilesRefused(t *testing.T) {
	ctx, share := newWebTestContext(t, "segreta")
	outside := filepath.Join(t.TempDir(), "segreto.txt")
	_ = os.WriteFile(outside, []byte("segreto"), 0o600)
	_ = os.WriteFile(filepath.Join(share, "reale.txt"), []byte("x"), 0o600)
	view := createShare(t, ctx, "reale.txt", "7")
	// Il file viene poi sostituito da un symlink fuori dalla share: mai servito.
	_ = os.Remove(filepath.Join(share, "reale.txt"))
	_ = os.Symlink(outside, filepath.Join(share, "reale.txt"))
	if rec := doWeb(ctx, httptest.NewRequest(http.MethodGet, "/s/"+view.Token, nil)); rec.Code != http.StatusNotFound {
		t.Fatalf("symlink servito: %d %q", rec.Code, rec.Body.String())
	}
	handler := ctx.webHandler()
	for i := 0; i < webMaxFailures; i++ {
		req := httptest.NewRequest(http.MethodGet, "/s/"+strings.Repeat("x", 32), nil)
		req.RemoteAddr = "198.51.100.7:1234"
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("tentativo %d: %d", i, rec.Code)
		}
	}
	req := httptest.NewRequest(http.MethodGet, "/s/"+strings.Repeat("y", 32), nil)
	req.RemoteAddr = "198.51.100.7:1234"
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("dopo troppi tentativi: %d", rec.Code)
	}
}

func jsonDecode(data []byte, into any) error { return json.Unmarshal(data, into) }
