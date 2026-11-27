package serverd

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Link di condivisione: un file scaricabile da chiunque abbia l'indirizzo,
// con scadenza e revoca. Il token è casuale (192 bit); l'elenco sta in
// state/share-links.json (0600). Il link serve sempre come allegato e mai il
// contenuto inline, per la stessa ragione per cui l'HTML dell'utente non viene
// mai mostrato dalla UI. Chi non conosce un token valido riceve 404 e, dopo
// troppi tentativi, 429.

const (
	shareTokenBytes = 24
	shareMaxLinks   = 1000
)

type shareLink struct {
	Path      string `json:"path"`
	Created   int64  `json:"created"`
	Expires   int64  `json:"expires"` // 0 = mai
	Downloads int    `json:"downloads"`
}

type shareStore struct {
	mu    sync.Mutex
	path  string
	links map[string]shareLink
}

var shareStores sync.Map // StateDir -> *shareStore

func (c *Context) shares() *shareStore {
	if existing, ok := shareStores.Load(c.StateDir); ok {
		return existing.(*shareStore)
	}
	store := &shareStore{path: filepath.Join(c.StateDir, "share-links.json"), links: map[string]shareLink{}}
	if data, err := os.ReadFile(store.path); err == nil {
		_ = json.Unmarshal(data, &store.links)
	}
	actual, _ := shareStores.LoadOrStore(c.StateDir, store)
	return actual.(*shareStore)
}

// saveLocked writes the list atomically. Callers hold s.mu.
func (s *shareStore) saveLocked() error {
	data, err := json.MarshalIndent(s.links, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

func (s *shareStore) pruneLocked(now time.Time) {
	for token, link := range s.links {
		if link.Expires > 0 && link.Expires <= now.Unix() {
			delete(s.links, token)
		}
	}
}

func newShareToken() (string, error) {
	buf := make([]byte, shareTokenBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

var errShareLimit = errors.New("troppi link attivi: revoca quelli che non servono")

func (s *shareStore) create(path string, days int, now time.Time) (string, shareLink, error) {
	token, err := newShareToken()
	if err != nil {
		return "", shareLink{}, err
	}
	link := shareLink{Path: path, Created: now.Unix()}
	if days > 0 {
		link.Expires = now.Add(time.Duration(days) * 24 * time.Hour).Unix()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked(now)
	if len(s.links) >= shareMaxLinks {
		return "", shareLink{}, errShareLimit
	}
	s.links[token] = link
	if err := s.saveLocked(); err != nil {
		delete(s.links, token)
		return "", shareLink{}, err
	}
	return token, link, nil
}

// lookup returns a live link and counts the download.
func (s *shareStore) lookup(token string, now time.Time) (shareLink, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	link, ok := s.links[token]
	if !ok {
		return shareLink{}, false
	}
	if link.Expires > 0 && link.Expires <= now.Unix() {
		delete(s.links, token)
		_ = s.saveLocked()
		return shareLink{}, false
	}
	link.Downloads++
	s.links[token] = link
	_ = s.saveLocked()
	return link, true
}

func (s *shareStore) revoke(token string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.links[token]; !ok {
		return false
	}
	delete(s.links, token)
	_ = s.saveLocked()
	return true
}

type shareView struct {
	Token     string `json:"token"`
	URL       string `json:"url"`
	Path      string `json:"path"`
	Created   int64  `json:"created"`
	Expires   int64  `json:"expires"`
	Downloads int    `json:"downloads"`
}

func (s *shareStore) list(now time.Time, base string) []shareView {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked(now)
	views := make([]shareView, 0, len(s.links))
	for token, link := range s.links {
		views = append(views, shareView{
			Token: token, URL: base + "/s/" + token, Path: link.Path,
			Created: link.Created, Expires: link.Expires, Downloads: link.Downloads,
		})
	}
	sort.Slice(views, func(i, j int) bool { return views[i].Created > views[j].Created })
	return views
}

// publicBase builds the address placed in links: WEB_PUBLIC_URL when the UI sits
// behind a proxy or a name, otherwise the address the request arrived at.
func (c *Context) publicBase(r *http.Request) string {
	if configured := strings.TrimRight(strings.TrimSpace(c.Config["WEB_PUBLIC_URL"]), "/"); configured != "" {
		return configured
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

// handleWebShare creates (POST) or revokes (DELETE) a link and lists them (GET).
// Auth and the anti-CSRF header are enforced by withWebAuth for non-GET methods.
func (c *Context) handleWebShare(w http.ResponseWriter, r *http.Request) {
	store := c.shares()
	switch r.Method {
	case http.MethodGet:
		if !c.webWritable() {
			// The list contains the tokens: only with a password configured.
			http.Error(w, "elenco link disponibile solo con WEB_PASSWORD", http.StatusForbidden)
			return
		}
		writeJSON(w, store.list(time.Now(), c.publicBase(r)))
	case http.MethodDelete:
		if !store.revoke(r.URL.Query().Get("token")) {
			http.Error(w, "link non trovato", http.StatusNotFound)
			return
		}
		writeJSON(w, map[string]bool{"revoked": true})
	case http.MethodPost:
		relative := r.URL.Query().Get("path")
		if !validRelative(relative) {
			http.Error(w, "percorso non valido", http.StatusBadRequest)
			return
		}
		full, err := resolveRelativeNoSymlinks(c.ShareRoot, relative)
		if err != nil {
			http.Error(w, "file non trovato", http.StatusNotFound)
			return
		}
		if info, err := os.Lstat(full); err != nil || !info.Mode().IsRegular() {
			http.Error(w, "si possono condividere solo file", http.StatusBadRequest)
			return
		}
		days := 7
		switch r.URL.Query().Get("days") {
		case "1":
			days = 1
		case "7", "":
			days = 7
		case "30":
			days = 30
		case "0":
			days = 0
		default:
			http.Error(w, "scadenza non valida", http.StatusBadRequest)
			return
		}
		token, link, err := store.create(relative, days, time.Now())
		if err != nil {
			status := http.StatusInternalServerError
			if errors.Is(err, errShareLimit) {
				status = http.StatusConflict
			}
			http.Error(w, err.Error(), status)
			return
		}
		writeJSON(w, shareView{
			Token: token, URL: c.publicBase(r) + "/s/" + token, Path: link.Path,
			Created: link.Created, Expires: link.Expires,
		})
	default:
		http.Error(w, "metodo non consentito", http.StatusMethodNotAllowed)
	}
}

// serveSharedFile answers /s/<token> without credentials.
func (c *Context) serveSharedFile(w http.ResponseWriter, r *http.Request, guard *webGuard) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Cache-Control", "no-store")
	if !safeMethod(r.Method) {
		http.Error(w, "metodo non consentito", http.StatusMethodNotAllowed)
		return
	}
	ip, now := clientIP(r), time.Now()
	if guard.blocked(ip, now) {
		w.Header().Set("Retry-After", "600")
		http.Error(w, "troppi tentativi, riprova più tardi", http.StatusTooManyRequests)
		return
	}
	token := strings.TrimPrefix(r.URL.Path, "/s/")
	link, ok := shareLink{}, false
	if len(token) >= 16 && len(token) <= 128 && !strings.ContainsAny(token, "/.") {
		link, ok = c.shares().lookup(token, now)
	}
	if !ok {
		guard.fail(ip, now)
		http.NotFound(w, r)
		return
	}
	full, err := resolveRelativeNoSymlinks(c.ShareRoot, link.Path)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	info, err := os.Lstat(full)
	if err != nil || !info.Mode().IsRegular() {
		http.NotFound(w, r)
		return
	}
	setDownloadHeaders(w, filepath.Base(full), false)
	http.ServeFile(w, r, full)
}
