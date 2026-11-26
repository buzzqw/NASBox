package serverd

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const webPage = `<!doctype html>
<html lang="it"><head><meta charset="utf-8">
<title>NASBox server</title>
<style>
body{font-family:system-ui,sans-serif;background:#12161c;color:#e8edf3;margin:0;padding:2rem}
h1{font-size:1.4rem}a{color:#6db3ff}code{background:#222a34;padding:.1rem .35rem;border-radius:4px}
.card{background:#1b212a;border:1px solid #2b3542;border-radius:10px;padding:1rem;margin:.8rem 0;max-width:60rem}
table{border-collapse:collapse;width:100%}td,th{text-align:left;padding:.25rem .6rem;border-bottom:1px solid #2b3542}
</style></head><body>
<h1>NASBox — demone Go</h1>
<div class="card"><h2>Stato</h2><table id="status"></table></div>
<div class="card"><h2>API</h2>
<p><a href="/api/status">/api/status</a> · <a href="/api/metrics">/api/metrics</a> ·
<a href="/api/browse?path=">/api/browse</a> · <a href="/api/history">/api/history</a> ·
<a href="/api/chunks">/api/chunks</a></p>
<p>Per scaricare un file: <code>/api/download?path=cartella/file.txt</code>.
Per una versione: <code>/api/download?path=.sync-trash/cartella/file.txt-2026-11-26--10-00-00-000000Z</code>.</p>
</div>
<div class="card"><h2>Note</h2><p>Questa UI è servita dallo stesso binario del demone.
Di default ascolta solo su <code>127.0.0.1</code>: passa un indirizzo diverso
con <code>--web 0.0.0.0:8080</code> solo su una rete fidata.</p></div>
<script>
async function load(){const r=await fetch('/api/status');const d=await r.json();
const t=document.getElementById('status');for(const k of Object.keys(d).sort()){
const tr=document.createElement('tr');tr.innerHTML='<th>'+k+'</th><td>'+d[k]+'</td>';t.appendChild(tr);}}
load();
</script></body></html>`

func writeJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(value)
}

func (c *Context) withBasicAuth(next http.Handler) http.Handler {
	user := value(c.Config, "WEB_USER", "")
	password := c.Config["WEB_PASSWORD"]
	if password == "" {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, p, ok := r.BasicAuth()
		if !ok || u != user || p != password {
			w.Header().Set("WWW-Authenticate", `Basic realm="NASBox"`)
			http.Error(w, "autenticazione richiesta", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
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
		fmt.Fprint(w, webPage)
	})
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
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Length", strconv.FormatInt(info.Size(), 10))
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
	return ctx.withBasicAuth(mux)
}

func cmdWeb(ctx *Context, args []string) error {
	address := "127.0.0.1:8080"
	if len(args) > 0 && args[0] != "" {
		address = args[0]
	}
	server := &http.Server{Addr: address, Handler: ctx.webHandler(), ReadHeaderTimeout: 5 * time.Second}
	fmt.Printf("NASBox web UI in ascolto su http://%s\n", address)
	return server.ListenAndServe()
}
