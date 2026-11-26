package serverd

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Web UI: consultazione e caricamento.
//
// Regole di sicurezza (la UI può essere esposta fuori casa):
//   - senza WEB_PASSWORD la UI è di sola lettura e `--web` rifiuta di ascoltare
//     su un indirizzo che non sia loopback;
//   - ogni richiesta che modifica qualcosa richiede la password, l'header
//     X-NASBox-Web e, se presente, un Origin uguale all'host (anti-CSRF: le
//     credenziali Basic vengono inviate dal browser in automatico);
//   - dopo troppi tentativi falliti da uno stesso indirizzo si risponde 429;
//   - un caricamento non scrive mai nella share: passa dal publish transazionale
//     (chunk store, journal, manifest) sotto lo stesso lease dei client, quindi
//     gli altri PC lo ricevono come qualsiasi altro file.

const (
	webDevice          = "web"
	webMaxFailures     = 5
	webFailureWindow   = 10 * time.Minute
	webLeaseTimeout    = 90 * time.Second
	webMaxUploadFiles  = 200
	webDefaultMaxMB    = 4096
	webSearchMaxResult = 300
	webSearchBudget    = 4 * time.Second
)

type webGuard struct {
	mu    sync.Mutex
	fails map[string][]time.Time
}

func (g *webGuard) blocked(ip string, now time.Time) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.recent(ip, now)) >= webMaxFailures
}

func (g *webGuard) recent(ip string, now time.Time) []time.Time {
	kept := g.fails[ip][:0]
	for _, at := range g.fails[ip] {
		if now.Sub(at) < webFailureWindow {
			kept = append(kept, at)
		}
	}
	if len(kept) == 0 {
		delete(g.fails, ip)
	} else {
		g.fails[ip] = kept
	}
	return kept
}

func (g *webGuard) fail(ip string, now time.Time) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.fails == nil {
		g.fails = map[string][]time.Time{}
	}
	g.fails[ip] = append(g.recent(ip, now), now)
}

func (g *webGuard) reset(ip string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.fails, ip)
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func equalSecret(a, b string) bool {
	ha, hb := sha256.Sum256([]byte(a)), sha256.Sum256([]byte(b))
	return subtle.ConstantTimeCompare(ha[:], hb[:]) == 1
}

func safeMethod(method string) bool {
	return method == http.MethodGet || method == http.MethodHead || method == http.MethodOptions
}

// sameOrigin accepts a request without Origin (non-browser) or whose Origin
// host is the one the request was sent to.
func sameOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	parsed, err := url.Parse(origin)
	return err == nil && parsed.Host == r.Host
}

func (c *Context) webPassword() string { return c.Config["WEB_PASSWORD"] }

func (c *Context) withWebAuth(next http.Handler) http.Handler {
	user := value(c.Config, "WEB_USER", "")
	password := c.webPassword()
	guard := &webGuard{}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Frame-Options", "SAMEORIGIN")
		if password == "" {
			if !safeMethod(r.Method) {
				http.Error(w, "caricamento disabilitato: imposta WEB_PASSWORD in server.conf", http.StatusForbidden)
				return
			}
			next.ServeHTTP(w, r)
			return
		}
		ip := clientIP(r)
		now := time.Now()
		if guard.blocked(ip, now) {
			w.Header().Set("Retry-After", strconv.Itoa(int(webFailureWindow.Seconds())))
			http.Error(w, "troppi tentativi falliti, riprova più tardi", http.StatusTooManyRequests)
			return
		}
		u, p, ok := r.BasicAuth()
		if !ok || !equalSecret(u, user) || !equalSecret(p, password) {
			if ok {
				guard.fail(ip, now)
			}
			w.Header().Set("WWW-Authenticate", `Basic realm="NASBox", charset="UTF-8"`)
			http.Error(w, "autenticazione richiesta", http.StatusUnauthorized)
			return
		}
		guard.reset(ip)
		if !safeMethod(r.Method) {
			if r.Header.Get("X-NASBox-Web") != "1" || !sameOrigin(r) {
				http.Error(w, "richiesta non consentita", http.StatusForbidden)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// webWritable reports whether uploads are enabled (a password is configured).
func (c *Context) webWritable() bool { return c.webPassword() != "" }

func (c *Context) webMaxUploadBytes() int64 {
	return parseConfigInt(c.Config["WEB_MAX_UPLOAD_MB"], webDefaultMaxMB) * 1024 * 1024
}

// withWebLease takes the global transfer lock for the web "device", the same
// way a client's lease does, so uploads never interleave with a sync.
func (c *Context) withWebLease(timeout time.Duration, fn func() error) error {
	p := c.paths()
	lock, err := os.OpenFile(p.Lock, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return err
	}
	defer lock.Close()
	deadline := time.Now().Add(timeout)
	for {
		err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			break
		}
		if err != syscall.EWOULDBLOCK && err != syscall.EINTR {
			return err
		}
		if time.Now().After(deadline) {
			return errWebBusy
		}
		time.Sleep(250 * time.Millisecond)
	}
	defer func() {
		_ = os.Remove(p.Owner)
		_ = syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	}()
	if err := c.writeOwner(webDevice, "web", "", 0, 0); err != nil {
		return err
	}
	return fn()
}

var errWebBusy = errors.New("NAS occupato da un'altra sincronizzazione, riprova tra poco")

func webRunTS() string {
	now := time.Now().UTC()
	return now.Format("2006-01-02--15-04-05-") + fmt.Sprintf("%06d", now.Nanosecond()/1000) + "Z"
}

func webTxID() string {
	buf := make([]byte, 8)
	_, _ = rand.Read(buf)
	return "web-" + hex.EncodeToString(buf)
}

// cleanUploadName returns a safe single path component or "".
func cleanUploadName(raw string) string {
	name := filepath.Base(strings.ReplaceAll(raw, "\\", "/"))
	if name == "" || name == "." || name == ".." || len(name) > 255 {
		return ""
	}
	for _, r := range name {
		if r < 0x20 || r == 0x7f {
			return ""
		}
	}
	if !validRelative(name) {
		return ""
	}
	return name
}

// uniqueName avoids overwriting: "foto.jpg" becomes "foto (1).jpg" when taken.
func uniqueName(dir, name string, taken map[string]bool) string {
	candidate := name
	ext := filepath.Ext(name)
	base := strings.TrimSuffix(name, ext)
	for n := 1; ; n++ {
		_, err := os.Lstat(filepath.Join(dir, candidate))
		if os.IsNotExist(err) && !taken[candidate] {
			return candidate
		}
		candidate = fmt.Sprintf("%s (%d)%s", base, n, ext)
	}
}

type uploadedFile struct {
	Name string `json:"name"`
	Path string `json:"path"`
	Size int64  `json:"size"`
}

func (c *Context) handleWebUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "metodo non consentito", http.StatusMethodNotAllowed)
		return
	}
	dirRel := strings.Trim(r.URL.Query().Get("path"), "/")
	dirAbs := c.ShareRoot
	if dirRel != "" {
		if !validRelative(dirRel) {
			http.Error(w, "percorso non valido", http.StatusBadRequest)
			return
		}
		var err error
		dirAbs, err = resolveRelativeNoSymlinks(c.ShareRoot, dirRel)
		if err != nil {
			http.Error(w, "cartella non trovata", http.StatusNotFound)
			return
		}
		if info, err := os.Lstat(dirAbs); err != nil || !info.IsDir() {
			http.Error(w, "cartella non trovata", http.StatusNotFound)
			return
		}
	}
	store, err := c.chunkStore()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, c.webMaxUploadBytes())
	reader, err := r.MultipartReader()
	if err != nil {
		http.Error(w, "richiesta multipart non valida", http.StatusBadRequest)
		return
	}
	type staged struct {
		name, tmp string
	}
	var files []staged
	defer func() {
		for _, item := range files {
			_ = os.Remove(item.tmp)
		}
	}()
	tmpDir := filepath.Join(c.StateDir, "chunks", "tmp")
	for {
		part, err := reader.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			status := http.StatusBadRequest
			var tooBig *http.MaxBytesError
			if errors.As(err, &tooBig) {
				status = http.StatusRequestEntityTooLarge
			}
			http.Error(w, "caricamento interrotto o troppo grande", status)
			return
		}
		if part.FileName() == "" {
			continue
		}
		name := cleanUploadName(part.FileName())
		if name == "" {
			http.Error(w, "nome file non valido", http.StatusBadRequest)
			return
		}
		if len(files) >= webMaxUploadFiles {
			http.Error(w, "troppi file in una richiesta", http.StatusBadRequest)
			return
		}
		tmp, err := os.CreateTemp(tmpDir, "web-upload-*")
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		files = append(files, staged{name: name, tmp: tmp.Name()})
		_, copyErr := io.Copy(tmp, part)
		closeErr := tmp.Close()
		if copyErr != nil || closeErr != nil {
			status := http.StatusBadRequest
			var tooBig *http.MaxBytesError
			if errors.As(copyErr, &tooBig) {
				status = http.StatusRequestEntityTooLarge
			}
			http.Error(w, "caricamento interrotto o troppo grande", status)
			return
		}
	}
	if len(files) == 0 {
		http.Error(w, "nessun file ricevuto", http.StatusBadRequest)
		return
	}

	type ingested struct {
		name, digest string
		size         int64
		chunks       []string
	}
	items := make([]ingested, 0, len(files))
	for _, file := range files {
		digest, size, chunks, err := store.StoreFile(file.tmp, c.ChunkSize())
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if err := store.SaveFileMeta(digest, chunks); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		items = append(items, ingested{name: file.name, digest: digest, size: size, chunks: chunks})
	}

	saved := make([]uploadedFile, 0, len(items))
	err = c.withWebLease(webLeaseTimeout, func() error {
		taken := map[string]bool{}
		records := make([]publishRecord, 0, len(items))
		saved = saved[:0]
		now := time.Now().UnixNano()
		for _, item := range items {
			name := uniqueName(dirAbs, item.name, taken)
			taken[name] = true
			rel := name
			if dirRel != "" {
				rel = dirRel + "/" + name
			}
			records = append(records, publishRecord{
				path: rel, digest: item.digest, size: strconv.FormatInt(item.size, 10),
				mtime: strconv.FormatInt(now, 10), causal: webDevice + ":1", chunks: item.chunks,
			})
			saved = append(saved, uploadedFile{Name: name, Path: rel, Size: item.size})
		}
		return c.publishChunkRecords(webDevice, webTxID(), webRunTS(), "create", records)
	})
	if errors.Is(err, errWebBusy) {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	if err != nil {
		http.Error(w, "pubblicazione non riuscita: "+err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"saved": saved})
}

type searchHit struct {
	Path  string `json:"path"`
	Name  string `json:"name"`
	Kind  string `json:"kind"`
	Size  int64  `json:"size"`
	MTime int64  `json:"mtime"`
}

func (c *Context) handleWebSearch(w http.ResponseWriter, r *http.Request) {
	needle := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q")))
	if len([]rune(needle)) < 2 {
		writeJSON(w, map[string]any{"hits": []searchHit{}, "truncated": false})
		return
	}
	deadline := time.Now().Add(webSearchBudget)
	hits := []searchHit{}
	truncated := false
	errStop := errors.New("stop")
	_ = filepath.WalkDir(c.ShareRoot, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if path == c.ShareRoot {
			return nil
		}
		name := entry.Name()
		if internalNames[name] || name == ".sync-trash" {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		if time.Now().After(deadline) || len(hits) >= webSearchMaxResult {
			truncated = true
			return errStop
		}
		if !strings.Contains(strings.ToLower(name), needle) {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return nil
		}
		rel, err := filepath.Rel(c.ShareRoot, path)
		if err != nil {
			return nil
		}
		hit := searchHit{Path: filepath.ToSlash(rel), Name: name, Kind: "FILE", MTime: info.ModTime().Unix()}
		if entry.IsDir() {
			hit.Kind = "DIR"
		} else if info.Mode().IsRegular() {
			hit.Size = info.Size()
		} else {
			return nil
		}
		hits = append(hits, hit)
		return nil
	})
	sort.Slice(hits, func(i, j int) bool {
		if hits[i].Kind != hits[j].Kind {
			return hits[i].Kind == "DIR"
		}
		return strings.ToLower(hits[i].Path) < strings.ToLower(hits[j].Path)
	})
	writeJSON(w, map[string]any{"hits": hits, "truncated": truncated})
}

type fileVersion struct {
	Path   string `json:"path"`
	Suffix string `json:"suffix"`
	Size   int64  `json:"size"`
	MTime  int64  `json:"mtime"`
}

// handleWebVersions lists the previous versions of one file kept in
// .sync-trash (history) so they can be downloaded.
func (c *Context) handleWebVersions(w http.ResponseWriter, r *http.Request) {
	relative := r.URL.Query().Get("path")
	if !validRelative(relative) {
		http.Error(w, "percorso non valido", http.StatusBadRequest)
		return
	}
	dir, base := filepath.Split(filepath.FromSlash(relative))
	trashDir := filepath.Join(c.ShareRoot, ".sync-trash", dir)
	entries, err := os.ReadDir(trashDir)
	versions := []fileVersion{}
	if err == nil {
		for _, entry := range entries {
			name := entry.Name()
			match := historySuffix.FindStringSubmatch(name)
			if match == nil || strings.TrimSuffix(name, "-"+match[1]+match[2]) != base {
				continue
			}
			info, err := entry.Info()
			if err != nil || !info.Mode().IsRegular() {
				continue
			}
			versions = append(versions, fileVersion{
				Path:   filepath.ToSlash(filepath.Join(".sync-trash", dir, name)),
				Suffix: match[1] + match[2], Size: info.Size(), MTime: info.ModTime().Unix(),
			})
		}
	}
	sort.Slice(versions, func(i, j int) bool { return versions[i].Suffix > versions[j].Suffix })
	writeJSON(w, versions)
}

// inlineTypes lists what the browser may display in the page. Everything else
// is always downloaded: serving user HTML/SVG inline would allow stored XSS.
var inlineTypes = map[string]string{
	".jpg": "image/jpeg", ".jpeg": "image/jpeg", ".png": "image/png", ".gif": "image/gif",
	".webp": "image/webp", ".pdf": "application/pdf", ".mp4": "video/mp4", ".webm": "video/webm",
	".mp3": "audio/mpeg", ".ogg": "audio/ogg", ".m4a": "audio/mp4", ".flac": "audio/flac",
	".wav": "audio/wav", ".txt": "text/plain; charset=utf-8", ".md": "text/plain; charset=utf-8",
}

// setDownloadHeaders chooses attachment or (for whitelisted types and only on
// request) inline display.
func setDownloadHeaders(w http.ResponseWriter, name string, inline bool) {
	disposition := "attachment"
	contentType := "application/octet-stream"
	if kind, ok := inlineTypes[strings.ToLower(filepath.Ext(name))]; ok && inline {
		disposition, contentType = "inline", kind
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Disposition", mime.FormatMediaType(disposition, map[string]string{"filename": name}))
	w.Header().Set("X-Content-Type-Options", "nosniff")
}
