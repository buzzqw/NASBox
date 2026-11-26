package serverd

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

func writeJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(value)
}

func keyValues(lines []string) map[string]string {
	out := map[string]string{}
	for _, line := range lines {
		key, value, ok := strings.Cut(line, "=")
		if ok {
			out[key] = value
		}
	}
	return out
}

func (c *Context) statusValues() map[string]string {
	p := c.paths()
	trashBytes, trashFiles, _ := "", "", ""
	if raw := readString(p.TrashUsage); raw != "" {
		fields := strings.Fields(raw)
		if len(fields) >= 3 {
			trashBytes, trashFiles = fields[0], fields[1]
		}
	}
	running := "false"
	if _, ok := c.daemonRunning(); ok {
		running = "true"
	}
	return map[string]string{
		"VERSION":                version,
		"RUNNING":                running,
		"SHARE_ROOT":             c.ShareRoot,
		"REPOSITORY_ID":          repositoryID(c.ShareRoot),
		"RETENTION_DAYS":         value(c.Config, "RETENTION_DAYS", "30"),
		"TRASH_MAX_MB":           value(c.Config, "TRASH_MAX_MB", "0"),
		"TRASH_USED_BYTES":       trashBytes,
		"TRASH_FILES":            trashFiles,
		"CHECK_INTERVAL_MINUTES": value(c.Config, "CHECK_INTERVAL_MINUTES", "60"),
		"JOURNAL_BYTES":          strconv.FormatInt(fileSize(p.Journal), 10),
		"MANIFEST_BYTES":         strconv.FormatInt(fileSize(p.Manifest), 10),
	}
}

func (ctx *Context) webHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Content-Security-Policy",
			"default-src 'self'; img-src 'self' data:; media-src 'self'; frame-src 'self'; "+
				"style-src 'self' 'unsafe-inline'; script-src 'self' 'unsafe-inline'; base-uri 'none'; form-action 'none'")
		w.Header().Set("Cache-Control", "no-store")
		fmt.Fprint(w, webPage)
	})
	mux.HandleFunc("/api/info", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{
			"version": version, "writable": ctx.webWritable(),
			"max_upload_bytes": ctx.webMaxUploadBytes(),
		})
	})
	mux.HandleFunc("/api/upload", ctx.handleWebUpload)
	mux.HandleFunc("/api/search", ctx.handleWebSearch)
	mux.HandleFunc("/api/versions", ctx.handleWebVersions)
	mux.HandleFunc("/api/status", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, ctx.statusValues())
	})
	mux.HandleFunc("/api/metrics", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, keyValues(ctx.metricsLines()))
	})
	mux.HandleFunc("/api/browse", func(w http.ResponseWriter, r *http.Request) {
		relative := r.URL.Query().Get("path")
		if relative != "" && !validRelative(relative) {
			http.Error(w, "percorso non valido", http.StatusBadRequest)
			return
		}
		target := ctx.ShareRoot
		if relative != "" {
			var err error
			target, err = resolveRelativeNoSymlinks(ctx.ShareRoot, relative)
			if errors.Is(err, errInvalidPath) {
				http.Error(w, "percorso non valido", http.StatusBadRequest)
				return
			}
			if err != nil {
				http.Error(w, "cartella non trovata", http.StatusNotFound)
				return
			}
		}
		info, err := os.Lstat(target)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			http.Error(w, "cartella non trovata", http.StatusNotFound)
			return
		}
		entries, err := os.ReadDir(target)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		items := []map[string]string{}
		for _, entry := range entries {
			if internalNames[entry.Name()] {
				continue
			}
			info, err := os.Lstat(filepath.Join(target, entry.Name()))
			if err != nil {
				continue
			}
			kind, size, mtime := "OTHER", "0", "0"
			if info.IsDir() {
				kind = "DIR"
				mtime = strconv.FormatInt(info.ModTime().Unix(), 10)
			} else if info.Mode().IsRegular() {
				kind = "FILE"
				size = strconv.FormatInt(info.Size(), 10)
				mtime = strconv.FormatInt(info.ModTime().Unix(), 10)
			}
			items = append(items, map[string]string{"name": entry.Name(), "kind": kind, "size": size, "mtime": mtime})
		}
		writeJSON(w, items)
	})
	mux.HandleFunc("/api/history", func(w http.ResponseWriter, _ *http.Request) {
		trash := filepath.Join(ctx.ShareRoot, ".sync-trash")
		type row struct {
			Original, Suffix, Path string
			Size                   int64
		}
		rows := []row{}
		_ = filepath.Walk(trash, func(path string, info os.FileInfo, err error) error {
			if err != nil || info == nil || !info.Mode().IsRegular() {
				return nil
			}
			match := historySuffix.FindStringSubmatch(info.Name())
			if match == nil {
				return nil
			}
			relative, err := filepath.Rel(trash, path)
			if err != nil {
				return nil
			}
			suffix := match[1] + match[2]
			rows = append(rows, row{
				Original: strings.TrimSuffix(filepath.ToSlash(relative), "-"+suffix),
				Suffix:   suffix, Path: filepath.ToSlash(relative), Size: info.Size(),
			})
			return nil
		})
		writeJSON(w, rows)
	})
	mux.HandleFunc("/api/download", func(w http.ResponseWriter, r *http.Request) {
		relative := r.URL.Query().Get("path")
		if relative == "" || filepath.IsAbs(relative) {
			http.Error(w, "percorso non valido", http.StatusBadRequest)
			return
		}
		for _, component := range strings.Split(relative, "/") {
			if component == ".nasbox-staging" || component == ".sync-partial" || component == "@eaDir" {
				http.Error(w, "percorso non valido", http.StatusBadRequest)
				return
			}
		}
		full, err := resolveRelativeNoSymlinks(ctx.ShareRoot, relative)
		if errors.Is(err, errInvalidPath) {
			http.Error(w, "percorso non valido", http.StatusBadRequest)
			return
		}
		if err != nil {
			http.Error(w, "file non trovato", http.StatusNotFound)
			return
		}
		info, err := os.Lstat(full)
		if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			http.Error(w, "file non trovato", http.StatusNotFound)
			return
		}
		setDownloadHeaders(w, filepath.Base(full), r.URL.Query().Get("inline") == "1")
		http.ServeFile(w, r, full)
	})
	mux.HandleFunc("/api/chunks", func(w http.ResponseWriter, _ *http.Request) {
		store, err := ctx.chunkStore()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		objects, bytes, err := store.Stats()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		snapshots, err := store.ListSnapshots()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, map[string]any{
			"objects": objects, "bytes": bytes, "snapshots": len(snapshots), "chunk_size": ctx.ChunkSize(),
		})
	})
	return ctx.withWebAuth(mux)
}

func isLoopbackAddress(address string) bool {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func cmdWeb(ctx *Context, args []string) error {
	address := value(ctx.Config, "WEB_LISTEN", "127.0.0.1:8080")
	if len(args) > 0 && args[0] != "" {
		address = args[0]
	}
	if !isLoopbackAddress(address) && !ctx.webWritable() {
		return fmt.Errorf("la UI web non può ascoltare su %s senza password: imposta WEB_USER e WEB_PASSWORD in server.conf", address)
	}
	server := &http.Server{
		Addr: address, Handler: ctx.webHandler(),
		ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 2 * time.Minute,
	}
	cert, key := ctx.Config["WEB_TLS_CERT"], ctx.Config["WEB_TLS_KEY"]
	if cert != "" && key != "" {
		fmt.Printf("NASBox web UI in ascolto su https://%s\n", address)
		return server.ListenAndServeTLS(cert, key)
	}
	fmt.Printf("NASBox web UI in ascolto su http://%s\n", address)
	return server.ListenAndServe()
}
