package serverd

import (
	"bytes"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newWebTestContext(t *testing.T, password string) (*Context, string) {
	t.Helper()
	ctx, share := newTestContext(t)
	if err := ctx.ensureJournalFiles(); err != nil {
		t.Fatal(err)
	}
	if password != "" {
		ctx.Config["WEB_USER"] = "andrea"
		ctx.Config["WEB_PASSWORD"] = password
	}
	return ctx, share
}

func multipartBody(t *testing.T, files map[string]string) (*bytes.Buffer, string) {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	for name, content := range files {
		part, err := writer.CreateFormFile("file", name)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = part.Write([]byte(content))
	}
	_ = writer.Close()
	return &body, writer.FormDataContentType()
}

func doWeb(ctx *Context, req *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	ctx.webHandler().ServeHTTP(rec, req)
	return rec
}

func uploadRequest(t *testing.T, dir string, files map[string]string) *http.Request {
	t.Helper()
	body, contentType := multipartBody(t, files)
	req := httptest.NewRequest(http.MethodPost, "/api/upload?path="+dir, body)
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("X-NASBox-Web", "1")
	req.SetBasicAuth("andrea", "segreta")
	return req
}

func TestWebUploadPublishesThroughJournalAndNeverOverwrites(t *testing.T) {
	ctx, share := newWebTestContext(t, "segreta")
	if err := os.MkdirAll(filepath.Join(share, "Documenti"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(share, "Documenti", "nota.txt"), []byte("originale"), 0o600); err != nil {
		t.Fatal(err)
	}
	rec := doWeb(ctx, uploadRequest(t, "Documenti", map[string]string{"nota.txt": "dal web", "nuovo.txt": "ciao"}))
	if rec.Code != http.StatusOK {
		t.Fatalf("upload: %d %s", rec.Code, rec.Body.String())
	}
	if got, _ := os.ReadFile(filepath.Join(share, "Documenti", "nota.txt")); string(got) != "originale" {
		t.Fatalf("il file esistente è stato sovrascritto: %q", got)
	}
	if got, _ := os.ReadFile(filepath.Join(share, "Documenti", "nota (1).txt")); string(got) != "dal web" {
		t.Fatalf("nome univoco mancante: %q", got)
	}
	if got, _ := os.ReadFile(filepath.Join(share, "Documenti", "nuovo.txt")); string(got) != "ciao" {
		t.Fatalf("nuovo.txt: %q", got)
	}
	// Il file deve essere nel manifest, altrimenti gli altri PC non lo vedono.
	row, ok, err := ctx.lookupManifest("Documenti/nuovo.txt")
	if err != nil || !ok || !strings.Contains(row, "Documenti/nuovo.txt") {
		t.Fatalf("manca la riga del manifest: %q ok=%v err=%v", row, ok, err)
	}
	// Il lease del web è rilasciato: un altro device lo può prendere.
	if _, err := os.Stat(ctx.paths().Owner); err == nil {
		t.Fatal("owner del lease non rimosso")
	}
}

func TestWebUploadRejectsUnsafeNamesAndTargets(t *testing.T) {
	ctx, share := newWebTestContext(t, "segreta")
	for _, dir := range []string{"../fuori", ".sync-trash", "inesistente"} {
		rec := doWeb(ctx, uploadRequest(t, dir, map[string]string{"a.txt": "x"}))
		if rec.Code == http.StatusOK {
			t.Fatalf("cartella %q accettata", dir)
		}
	}
	for _, name := range []string{"..", ".", ".sync-trash", "@eaDir"} {
		rec := doWeb(ctx, uploadRequest(t, "", map[string]string{name: "x"}))
		if rec.Code == http.StatusOK {
			t.Fatalf("nome %q accettato", name)
		}
	}
	// Tab, newline e controlli romperebbero journal e manifest (TSV).
	for _, name := range []string{"a\nb.txt", "a\tb.txt", "a\x00b", "a\x7fb", strings.Repeat("x", 256)} {
		if got := cleanUploadName(name); got != "" {
			t.Fatalf("nome %q accettato come %q", name, got)
		}
	}
	if got := cleanUploadName(`C:\Utenti\io\foto.jpg`); got != "foto.jpg" {
		t.Fatalf("i percorsi Windows vanno ridotti al nome: %q", got)
	}
	// Un percorso con "/" viene ridotto al solo nome, mai fuori dalla cartella.
	rec := doWeb(ctx, uploadRequest(t, "", map[string]string{"../../evil.txt": "x"}))
	if rec.Code != http.StatusOK {
		t.Fatalf("il nome va ridotto, non rifiutato: %d %s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(filepath.Join(share, "evil.txt")); err != nil {
		t.Fatalf("file atteso nella radice: %v", err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(share), "evil.txt")); err == nil {
		t.Fatal("path traversal riuscito")
	}
}

func TestWebWriteRequiresPasswordHeaderAndSameOrigin(t *testing.T) {
	// Senza password: sola lettura.
	open, _ := newWebTestContext(t, "")
	req := uploadRequest(t, "", map[string]string{"a.txt": "x"})
	req.Header.Del("Authorization")
	if rec := doWeb(open, req); rec.Code != http.StatusForbidden {
		t.Fatalf("senza password la scrittura deve essere vietata: %d", rec.Code)
	}
	if rec := doWeb(open, httptest.NewRequest(http.MethodGet, "/api/info", nil)); rec.Code != http.StatusOK {
		t.Fatalf("la lettura senza password resta possibile: %d", rec.Code)
	}

	ctx, _ := newWebTestContext(t, "segreta")
	noAuth := uploadRequest(t, "", map[string]string{"a.txt": "x"})
	noAuth.Header.Del("Authorization")
	if rec := doWeb(ctx, noAuth); rec.Code != http.StatusUnauthorized {
		t.Fatalf("senza credenziali: %d", rec.Code)
	}
	noHeader := uploadRequest(t, "", map[string]string{"a.txt": "x"})
	noHeader.Header.Del("X-NASBox-Web")
	if rec := doWeb(ctx, noHeader); rec.Code != http.StatusForbidden {
		t.Fatalf("senza header anti-CSRF: %d", rec.Code)
	}
	cross := uploadRequest(t, "", map[string]string{"a.txt": "x"})
	cross.Header.Set("Origin", "https://sito-malevolo.example")
	if rec := doWeb(ctx, cross); rec.Code != http.StatusForbidden {
		t.Fatalf("Origin diverso: %d", rec.Code)
	}
	wrong := httptest.NewRequest(http.MethodGet, "/api/info", nil)
	wrong.SetBasicAuth("andrea", "sbagliata")
	if rec := doWeb(ctx, wrong); rec.Code != http.StatusUnauthorized {
		t.Fatalf("password sbagliata: %d", rec.Code)
	}
}

func TestWebRateLimitsFailedLogins(t *testing.T) {
	ctx, _ := newWebTestContext(t, "segreta")
	handler := ctx.webHandler()
	attempt := func(password string) int {
		req := httptest.NewRequest(http.MethodGet, "/api/info", nil)
		req.RemoteAddr = "203.0.113.9:5555"
		req.SetBasicAuth("andrea", password)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec.Code
	}
	for i := 0; i < webMaxFailures; i++ {
		if code := attempt("no"); code != http.StatusUnauthorized {
			t.Fatalf("tentativo %d: %d", i, code)
		}
	}
	if code := attempt("segreta"); code != http.StatusTooManyRequests {
		t.Fatalf("dopo troppi errori anche la password giusta va bloccata: %d", code)
	}
}

func TestWebDownloadNeverServesUserHTMLInline(t *testing.T) {
	ctx, share := newWebTestContext(t, "")
	for name, body := range map[string]string{"pagina.html": "<script>alert(1)</script>", "icona.svg": "<svg onload=alert(1)>", "foto.png": "png"} {
		if err := os.WriteFile(filepath.Join(share, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	get := func(name string) *httptest.ResponseRecorder {
		return doWeb(ctx, httptest.NewRequest(http.MethodGet, "/api/download?inline=1&path="+name, nil))
	}
	for _, name := range []string{"pagina.html", "icona.svg"} {
		rec := get(name)
		if !strings.HasPrefix(rec.Header().Get("Content-Disposition"), "attachment") || rec.Header().Get("Content-Type") != "application/octet-stream" {
			t.Fatalf("%s non deve essere mostrato inline: %v", name, rec.Header())
		}
		if rec.Header().Get("X-Content-Type-Options") != "nosniff" {
			t.Fatalf("manca nosniff su %s", name)
		}
	}
	if rec := get("foto.png"); !strings.HasPrefix(rec.Header().Get("Content-Disposition"), "inline") || rec.Header().Get("Content-Type") != "image/png" {
		t.Fatalf("le immagini si possono mostrare: %v", rec.Header())
	}
}

func TestWebSearchAndVersions(t *testing.T) {
	ctx, share := newWebTestContext(t, "")
	_ = os.MkdirAll(filepath.Join(share, "Foto", "2026"), 0o700)
	_ = os.WriteFile(filepath.Join(share, "Foto", "2026", "Mare-estate.jpg"), []byte("x"), 0o600)
	_ = os.MkdirAll(filepath.Join(share, ".sync-trash", "Foto"), 0o700)
	_ = os.WriteFile(filepath.Join(share, ".sync-trash", "Foto", "Mare-estate.jpg-2026-11-20--10-00-00-000001Z"), []byte("vecchia"), 0o600)
	_ = os.WriteFile(filepath.Join(share, ".sync-trash", "Foto", "mare-segreto.jpg"), []byte("x"), 0o600)

	rec := doWeb(ctx, httptest.NewRequest(http.MethodGet, "/api/search?q=MARE", nil))
	body := rec.Body.String()
	if !strings.Contains(body, "Foto/2026/Mare-estate.jpg") || strings.Contains(body, "sync-trash") {
		t.Fatalf("ricerca: %s", body)
	}
	rec = doWeb(ctx, httptest.NewRequest(http.MethodGet, "/api/versions?path=Foto/Mare-estate.jpg", nil))
	if !strings.Contains(rec.Body.String(), ".sync-trash/Foto/Mare-estate.jpg-2026-11-20--10-00-00-000001Z") {
		t.Fatalf("versioni: %s", rec.Body.String())
	}
	if rec := doWeb(ctx, httptest.NewRequest(http.MethodGet, "/api/versions?path=../x", nil)); rec.Code != http.StatusBadRequest {
		t.Fatalf("path traversal nelle versioni: %d", rec.Code)
	}
}

func TestWebRefusesPublicAddressWithoutPassword(t *testing.T) {
	ctx, _ := newWebTestContext(t, "")
	if err := cmdWeb(ctx, []string{"0.0.0.0:0"}); err == nil || !strings.Contains(err.Error(), "senza password") {
		t.Fatalf("atteso rifiuto, ottenuto %v", err)
	}
	for address, want := range map[string]bool{"127.0.0.1:8080": true, "[::1]:8080": true, "0.0.0.0:8080": false, "192.168.1.5:80": false} {
		if isLoopbackAddress(address) != want {
			t.Fatalf("isLoopbackAddress(%q) != %v", address, want)
		}
	}
}
