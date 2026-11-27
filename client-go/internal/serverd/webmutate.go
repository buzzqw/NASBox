package serverd

import (
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Web mutations: rename, delete and create-folder.
//
// They deliberately reuse the same primitives as the desktop commands
// (--browse-delete, --browse-rename): the global web lease, .sync-trash,
// appendTransactionLocked and pruneEmptyDirs. No second mutation path exists.
//
// The web UI is authenticated by withWebAuth; these handlers only accept POST
// and are reachable only when WEB_PASSWORD is configured.

var (
	errWebNotFound = errors.New("elemento non trovato")
	errWebConflict = errors.New("esiste gia'")
	errWebTooLarge = errors.New("troppi file per l'operazione web")
)

const (
	webEntryMaxFiles  = 5000
	webRenameMaxFiles = 5000
)

type webEntryInfo struct {
	Path      string `json:"path"`
	Name      string `json:"name"`
	Kind      string `json:"kind"`
	Size      int64  `json:"size"`
	Files     int    `json:"files"`
	Bytes     int64  `json:"bytes"`
	Truncated bool   `json:"truncated"`
}

type webDeleteResult struct {
	Status string `json:"status"`
	Files  int    `json:"files"`
	Bytes  int64  `json:"bytes"`
	Trash  string `json:"trash"`
}

type webRenameResult struct {
	Status string `json:"status"`
	From   string `json:"from"`
	To     string `json:"to"`
	Files  int    `json:"files"`
}

type webMkdirResult struct {
	Status string `json:"status"`
	Path   string `json:"path"`
}

// cleanEntryName validates a single path component supplied by the web UI.
// It never trims or normalises a name: names are opaque bytes to the sync
// engine, and silently changing them would move a different file.
func cleanEntryName(raw string) string {
	if raw == "" || raw == "." || raw == ".." || len(raw) > 255 {
		return ""
	}
	if strings.ContainsAny(raw, "/\\\x00\n\r\t") {
		return ""
	}
	for _, r := range raw {
		if r < 0x20 || r == 0x7f {
			return ""
		}
	}
	if !validRelative(raw) {
		return ""
	}
	return raw
}

func webRelativeParent(relative string) string {
	parent := filepath.ToSlash(filepath.Dir(filepath.FromSlash(relative)))
	if parent == "." {
		return ""
	}
	return parent
}

func webJoinRelative(parent, name string) string {
	if parent == "" {
		return name
	}
	return parent + "/" + name
}

// webEntryStats reports a file or a directory. The directory walk is bounded:
// it is used for the confirmation dialog, never as the authoritative list.
func webEntryStats(root, relative string) (webEntryInfo, error) {
	if !validRelative(relative) {
		return webEntryInfo{}, errInvalidPath
	}
	full, err := resolveRelativeNoSymlinks(root, relative)
	if err != nil {
		if errors.Is(err, errInvalidPath) {
			return webEntryInfo{}, errInvalidPath
		}
		return webEntryInfo{}, errWebNotFound
	}
	info, err := os.Lstat(full)
	if err != nil {
		if os.IsNotExist(err) {
			return webEntryInfo{}, errWebNotFound
		}
		return webEntryInfo{}, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return webEntryInfo{}, errInvalidPath
	}
	result := webEntryInfo{Path: relative, Name: filepath.Base(full)}
	if info.Mode().IsRegular() {
		result.Kind = "FILE"
		result.Size = info.Size()
		result.Files = 1
		result.Bytes = info.Size()
		return result, nil
	}
	if !info.IsDir() {
		return webEntryInfo{}, errInvalidPath
	}
	result.Kind = "DIR"
	deadline := time.Now().Add(2 * time.Second)
	stop := errors.New("stats limit")
	_ = filepath.WalkDir(full, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if path == full {
			return nil
		}
		if internalNames[entry.Name()] {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 || entry.IsDir() {
			return nil
		}
		item, err := entry.Info()
		if err != nil || !item.Mode().IsRegular() {
			return nil
		}
		result.Files++
		result.Bytes += item.Size()
		if result.Files >= webEntryMaxFiles || time.Now().After(deadline) {
			result.Truncated = true
			return stop
		}
		return nil
	})
	return result, nil
}

// deleteEntryLocked moves one file or directory to .sync-trash and journals
// every regular file in one transaction. The caller must already own the
// global lease (withWebLease or --browse-delete).
func (c *Context) deleteEntryLocked(device, runTS, relative string) (webDeleteResult, error) {
	result := webDeleteResult{Status: "OK"}
	if !validRelative(relative) {
		return result, errInvalidPath
	}
	full, err := resolveRelativeNoSymlinks(c.ShareRoot, relative)
	if err != nil {
		if errors.Is(err, errInvalidPath) {
			return result, errInvalidPath
		}
		return result, errWebNotFound
	}
	err = c.withJournalLock(func() error {
		// Re-resolve under the journal lock: an ancestor could have been
		// replaced by a symlink between the first check and the lease.
		full, err = resolveRelativeNoSymlinks(c.ShareRoot, relative)
		if err != nil {
			if errors.Is(err, errInvalidPath) {
				return errInvalidPath
			}
			return errWebNotFound
		}
		info, err := os.Lstat(full)
		if err != nil {
			if os.IsNotExist(err) {
				return errWebNotFound
			}
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return errInvalidPath
		}
		if !info.IsDir() && !info.Mode().IsRegular() {
			return errInvalidPath
		}
		paths := []string{relative}
		if info.IsDir() {
			files, err := listRegularFiles(full)
			if err != nil {
				return err
			}
			paths = make([]string, 0, len(files))
			for _, file := range files {
				rel := filepath.ToSlash(filepath.Join(relative, file))
				if !validRelative(rel) {
					// Internal paths (a nested .sync-trash, for example) move
					// with the directory but are never journaled.
					continue
				}
				paths = append(paths, rel)
				if item, statErr := os.Lstat(filepath.Join(full, file)); statErr == nil {
					result.Bytes += item.Size()
				}
			}
		} else {
			result.Bytes = info.Size()
		}
		result.Files = len(paths)
		backupRelative := filepath.ToSlash(filepath.Join(".sync-trash", relative+"-"+runTS))
		backup := filepath.Join(c.ShareRoot, filepath.FromSlash(backupRelative))
		if err := os.MkdirAll(filepath.Dir(backup), 0o755); err != nil {
			return err
		}
		if err := os.Rename(full, backup); err != nil {
			return err
		}
		txid := "web-delete-" + runTS + "-" + strconv.Itoa(os.Getpid()) + "-" + randomSuffix()
		if err := c.appendTransactionLocked(txid, device, deleteRows(paths, device, journalNow())); err != nil {
			_ = os.MkdirAll(filepath.Dir(full), 0o755)
			if rollbackErr := os.Rename(backup, full); rollbackErr != nil {
				return fmt.Errorf("delete journal: %w (rollback: %v)", err, rollbackErr)
			}
			return err
		}
		result.Trash = backupRelative
		pruneEmptyDirs(c.ShareRoot, relative)
		return nil
	})
	return result, err
}

// renameEntryLocked moves a file or directory inside its current parent and
// journals every affected path in one transaction. It refuses to overwrite.
func (c *Context) renameEntryLocked(device, runTS, src, dst string) (webRenameResult, error) {
	result := webRenameResult{Status: "OK", From: src, To: dst}
	if !validRelative(src) || !validRelative(dst) {
		return result, errInvalidPath
	}
	if filepath.ToSlash(filepath.Dir(filepath.FromSlash(src))) != filepath.ToSlash(filepath.Dir(filepath.FromSlash(dst))) {
		return result, errInvalidPath
	}
	if src == dst {
		return result, errWebConflict
	}
	srcFull, err := resolveRelativeNoSymlinks(c.ShareRoot, src)
	if err != nil {
		if errors.Is(err, errInvalidPath) {
			return result, errInvalidPath
		}
		return result, errWebNotFound
	}
	parent := webRelativeParent(dst)
	parentFull := c.ShareRoot
	if parent != "" {
		parentFull, err = resolveRelativeNoSymlinks(c.ShareRoot, parent)
		if err != nil {
			return result, errInvalidPath
		}
	}
	dstFull := filepath.Join(parentFull, filepath.Base(filepath.FromSlash(dst)))
	err = c.withJournalLock(func() error {
		// Re-resolve both paths while the journal lock is held, so an ancestor
		// swapped for a symlink cannot redirect the move.
		srcFull, err = resolveRelativeNoSymlinks(c.ShareRoot, src)
		if err != nil {
			if errors.Is(err, errInvalidPath) {
				return errInvalidPath
			}
			return errWebNotFound
		}
		parentFull = c.ShareRoot
		if parent != "" {
			parentFull, err = resolveRelativeNoSymlinks(c.ShareRoot, parent)
			if err != nil {
				if errors.Is(err, errInvalidPath) {
					return errInvalidPath
				}
				return errWebNotFound
			}
		}
		dstFull = filepath.Join(parentFull, filepath.Base(filepath.FromSlash(dst)))
		info, err := os.Lstat(srcFull)
		if err != nil {
			if os.IsNotExist(err) {
				return errWebNotFound
			}
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return errInvalidPath
		}
		if !info.IsDir() && !info.Mode().IsRegular() {
			return errInvalidPath
		}
		if _, err := os.Lstat(dstFull); err == nil {
			return errWebConflict
		} else if !os.IsNotExist(err) {
			return err
		}
		records := []renameRecord{}
		if info.IsDir() {
			files, err := listRegularFiles(srcFull)
			if err != nil {
				return err
			}
			if len(files) > webRenameMaxFiles {
				return errWebTooLarge
			}
			records = make([]renameRecord, 0, len(files))
			for _, file := range files {
				rel := filepath.ToSlash(file)
				if !validRelative(rel) {
					// Internal paths move with the directory but are never
					// written to the manifest.
					continue
				}
				fileFull := filepath.Join(srcFull, file)
				fileInfo, err := os.Lstat(fileFull)
				if err != nil {
					return err
				}
				digest, err := sha256File(fileFull)
				if err != nil {
					return err
				}
				records = append(records, renameRecord{
					rel:    rel,
					digest: digest,
					size:   strconv.FormatInt(fileInfo.Size(), 10),
					mtime:  fileMTimeNS(fileInfo),
				})
			}
		} else {
			digest, err := sha256File(srcFull)
			if err != nil {
				return err
			}
			records = append(records, renameRecord{
				rel: "", digest: digest,
				size: strconv.FormatInt(info.Size(), 10), mtime: fileMTimeNS(info),
			})
		}
		result.Files = len(records)
		if err := os.Rename(srcFull, dstFull); err != nil {
			return err
		}
		txid := "web-rename-" + runTS + "-" + strconv.Itoa(os.Getpid()) + "-" + randomSuffix()
		if err := c.appendTransactionLocked(txid, device, renameRows(src, dst, records, device, journalNow())); err != nil {
			if rollbackErr := os.Rename(dstFull, srcFull); rollbackErr != nil {
				return fmt.Errorf("rename journal: %w (rollback: %v)", err, rollbackErr)
			}
			return err
		}
		return nil
	})
	return result, err
}

func (c *Context) handleWebEntry(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "metodo non consentito", http.StatusMethodNotAllowed)
		return
	}
	relative := r.URL.Query().Get("path")
	if relative == "" {
		http.Error(w, "percorso non valido", http.StatusBadRequest)
		return
	}
	info, err := webEntryStats(c.ShareRoot, relative)
	if err != nil {
		writeWebMutationError(w, err)
		return
	}
	writeJSON(w, info)
}

func (c *Context) handleWebDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "metodo non consentito", http.StatusMethodNotAllowed)
		return
	}
	if !c.webWritable() {
		http.Error(w, "operazione disabilitata: imposta WEB_PASSWORD in server.conf", http.StatusForbidden)
		return
	}
	relative := r.URL.Query().Get("path")
	if relative == "" || r.URL.Query().Get("confirm") != "1" {
		http.Error(w, "conferma richiesta", http.StatusBadRequest)
		return
	}
	var result webDeleteResult
	err := c.withWebLease(webLeaseTimeout, func() error {
		var err error
		result, err = c.deleteEntryLocked(webDevice, webRunTS(), relative)
		return err
	})
	if err != nil {
		writeWebMutationError(w, err)
		return
	}
	writeJSON(w, result)
}

func (c *Context) handleWebRename(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "metodo non consentito", http.StatusMethodNotAllowed)
		return
	}
	if !c.webWritable() {
		http.Error(w, "operazione disabilitata: imposta WEB_PASSWORD in server.conf", http.StatusForbidden)
		return
	}
	relative := r.URL.Query().Get("path")
	name := cleanEntryName(r.URL.Query().Get("name"))
	if relative == "" || name == "" {
		http.Error(w, "nome o percorso non valido", http.StatusBadRequest)
		return
	}
	parent := webRelativeParent(relative)
	dst := webJoinRelative(parent, name)
	var result webRenameResult
	err := c.withWebLease(webLeaseTimeout, func() error {
		var err error
		result, err = c.renameEntryLocked(webDevice, webRunTS(), relative, dst)
		return err
	})
	if err != nil {
		writeWebMutationError(w, err)
		return
	}
	writeJSON(w, result)
}

func (c *Context) handleWebMkdir(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "metodo non consentito", http.StatusMethodNotAllowed)
		return
	}
	if !c.webWritable() {
		http.Error(w, "operazione disabilitata: imposta WEB_PASSWORD in server.conf", http.StatusForbidden)
		return
	}
	parent := strings.Trim(r.URL.Query().Get("path"), "/")
	name := cleanEntryName(r.URL.Query().Get("name"))
	if name == "" {
		http.Error(w, "nome non valido", http.StatusBadRequest)
		return
	}
	if parent != "" && !validRelative(parent) {
		http.Error(w, "percorso non valido", http.StatusBadRequest)
		return
	}
	relative := webJoinRelative(parent, name)
	var result webMkdirResult
	err := c.withWebLease(webLeaseTimeout, func() error {
		parentFull := c.ShareRoot
		if parent != "" {
			var err error
			parentFull, err = resolveRelativeNoSymlinks(c.ShareRoot, parent)
			if err != nil {
				if errors.Is(err, errInvalidPath) {
					return errInvalidPath
				}
				return errWebNotFound
			}
		}
		if info, err := os.Lstat(parentFull); err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return errWebNotFound
		}
		target := filepath.Join(parentFull, filepath.FromSlash(name))
		if err := os.Mkdir(target, 0o755); err != nil {
			if os.IsExist(err) {
				return errWebConflict
			}
			return err
		}
		result = webMkdirResult{Status: "OK", Path: relative}
		return nil
	})
	if err != nil {
		writeWebMutationError(w, err)
		return
	}
	writeJSON(w, result)
}

func writeWebMutationError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errInvalidPath):
		http.Error(w, "percorso o nome non valido", http.StatusBadRequest)
	case errors.Is(err, errWebNotFound):
		http.Error(w, "elemento non trovato", http.StatusNotFound)
	case errors.Is(err, errWebConflict):
		http.Error(w, "esiste gia' un elemento con questo nome", http.StatusConflict)
	case errors.Is(err, errWebTooLarge):
		http.Error(w, "troppi file: usa il client desktop per questa cartella", http.StatusRequestEntityTooLarge)
	case errors.Is(err, errWebBusy):
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
	default:
		http.Error(w, "operazione non riuscita: "+err.Error(), http.StatusInternalServerError)
	}
}
