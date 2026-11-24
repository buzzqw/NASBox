// Package mirror implements external-folder mirrors: a folder that lives
// OUTSIDE NASBox is copied, one-way, into local_root/dest with a local rsync.
// The ordinary engine push then propagates the freshly written copy to the NAS
// (with journal and backup), exactly as if the user had dropped the files into
// NASBox by hand.
//
// This package never talks to the NAS: no remote lease, no SSH and no sleeping
// 3600s loop. Every pass is driven by a context, so the local Runner can cancel
// it together with the rest of the transfer.
package mirror

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"nasbox-go/internal/config"
	"nasbox-go/internal/paths"
)

const (
	// DefaultMinFiles is the empty-source guard: a source with fewer regular
	// files than this never gets to run rsync --delete-after.
	DefaultMinFiles = 20
	// MaxDestDepth / MaxSourceDepth cap the number of path components so a
	// malformed config cannot produce an unbounded walk or rsync tree.
	MaxDestDepth   = 32
	MaxSourceDepth = 64
	// DefaultSyncTimeout bounds a single rsync pass; it is not a poll interval.
	DefaultSyncTimeout = time.Hour
)

var (
	// MirrorExcludeDirs are NASBox internals that must never be mirrored.
	MirrorExcludeDirs = []string{".sync-trash", ".sync-partial", ".nasbox-staging"}
	// ConflictGlob and ArchivedGlob protect copies created inside the mirrored
	// destination by the sync engine: rsync --delete must not erase them.
	ConflictGlob = `*conflitto\ da\ *`
	ArchivedGlob = `*copia archiviata*`

	reservedDestNames = map[string]bool{
		".sync-trash": true, ".sync-partial": true, ".nasbox-staging": true,
		".nasbox-root": true, ".~tmp~": true,
	}
)

// Mirror is one configured external folder. LastSync is a Unix timestamp in
// seconds (the same field the Python client writes into client.json).
type Mirror struct {
	Source   string  `json:"source"`
	Dest     string  `json:"dest"`
	Enabled  bool    `json:"enabled"`
	LastSync float64 `json:"last_sync,omitempty"`
	Error    string  `json:"error,omitempty"`
}

// Configured reads the "mirrors" list from client.json. A missing "enabled"
// key means enabled, matching the Python client's entry.get("enabled", True).
func Configured(cfg *config.Config) []Mirror {
	raw, ok := cfg.Get("mirrors").([]any)
	if !ok {
		return nil
	}
	result := make([]Mirror, 0, len(raw))
	for _, item := range raw {
		entry, ok := item.(map[string]any)
		if !ok {
			continue
		}
		m := Mirror{Enabled: true}
		if value, ok := entry["source"].(string); ok {
			m.Source = strings.TrimSpace(value)
		}
		if value, ok := entry["dest"].(string); ok {
			m.Dest = strings.Trim(strings.TrimSpace(value), "/")
		}
		if value, ok := entry["enabled"].(bool); ok {
			m.Enabled = value
		}
		switch value := entry["last_sync"].(type) {
		case float64:
			m.LastSync = value
		case int:
			m.LastSync = float64(value)
		}
		if value, ok := entry["error"].(string); ok {
			m.Error = value
		}
		if m.Source == "" || m.Dest == "" {
			continue
		}
		result = append(result, m)
	}
	return result
}

// ValidateMirror checks a mirror immediately before a destructive pass.
//
// The source must be an existing absolute directory outside the NASBox root;
// the destination must be a relative path inside it. Neither side may contain
// a symlink component, traversal or a reserved NASBox name.
func ValidateMirror(cfg *config.Config, source, dest string) error {
	source = strings.TrimSpace(source)
	rawDest := strings.TrimSpace(dest)
	if source == "" {
		return errors.New("sorgente mirror mancante")
	}
	if !filepath.IsAbs(source) {
		return fmt.Errorf("sorgente mirror assoluta richiesta: %s", source)
	}
	if rawDest == "" {
		return errors.New("destinazione mirror mancante")
	}
	if strings.Contains(rawDest, `\`) {
		return fmt.Errorf("destinazione mirror non valida (backslash): %s", rawDest)
	}
	if filepath.IsAbs(rawDest) || strings.HasPrefix(rawDest, "/") {
		return fmt.Errorf("destinazione mirror relativa richiesta: %s", rawDest)
	}
	dest = strings.Trim(rawDest, "/")
	if dest == "" || dest == "." {
		return errors.New("destinazione mirror non puo' essere la root NASBox")
	}
	parts := strings.Split(dest, "/")
	if len(parts) > MaxDestDepth {
		return fmt.Errorf("destinazione mirror troppo profonda (%d componenti)", len(parts))
	}
	for _, part := range parts {
		if part == "" || part == "." || part == ".." {
			return fmt.Errorf("componente di percorso non valido nella destinazione mirror: %s", dest)
		}
		if reservedDestNames[part] {
			return fmt.Errorf("nome riservato nella destinazione mirror: %s", part)
		}
	}
	root := strings.TrimRight(strings.TrimSpace(cfg.LocalRoot()), "/")
	if root == "" {
		return errors.New("root NASBox non configurata")
	}
	if !filepath.IsAbs(root) {
		return fmt.Errorf("root NASBox assoluta richiesta: %s", root)
	}
	if strings.Count(filepath.ToSlash(source), "/") > MaxSourceDepth {
		return fmt.Errorf("sorgente mirror troppo profonda: %s", source)
	}

	info, err := os.Stat(source)
	if err != nil {
		return fmt.Errorf("sorgente mirror non accessibile: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("sorgente mirror non e' una cartella: %s", source)
	}
	if err := rejectSymlinkComponents(source); err != nil {
		return err
	}
	// SafeDestination also rejects traversal, absolute paths and existing
	// symlink components anywhere below root, including the final target.
	if _, err := paths.SafeDestination(root, dest); err != nil {
		return fmt.Errorf("destinazione mirror non valida: %w", err)
	}

	canonicalRoot := canonicalPath(root)
	canonicalSource := canonicalPath(source)
	canonicalDest := filepath.Join(canonicalRoot, filepath.FromSlash(dest))
	if containedIn(canonicalRoot, canonicalSource) {
		return fmt.Errorf("sorgente mirror dentro la root NASBox: %s", source)
	}
	if !containedIn(canonicalRoot, canonicalDest) || canonicalDest == canonicalRoot {
		return fmt.Errorf("destinazione mirror fuori dalla root NASBox: %s", dest)
	}
	return nil
}

// RsyncCommand builds the one-way local copy used by every mirror pass.
// The trailing slashes make rsync copy the contents of source into dest.
func RsyncCommand(cfg *config.Config, source, destination string) []string {
	args := []string{
		"-a", "--delete-after", "--partial",
		"--partial-dir=.sync-partial", "--delay-updates", "--no-links",
	}
	for _, pattern := range configuredExcludes(cfg) {
		args = append(args, "--exclude", pattern)
	}
	for _, name := range MirrorExcludeDirs {
		args = append(args, "--exclude", name)
	}
	args = append(args, "--exclude", ConflictGlob, "--exclude", ArchivedGlob)
	if limit := cfg.Int("bandwidth_upload_kbps"); limit > 0 {
		args = append(args, "--bwlimit", strconv.Itoa(limit))
	}
	if !strings.HasSuffix(source, "/") {
		source += "/"
	}
	if !strings.HasSuffix(destination, string(filepath.Separator)) {
		destination += string(filepath.Separator)
	}
	return append(args, source, destination)
}

// SyncAll runs every enabled mirror into local_root/dest. It holds no NAS
// lease: the following normal engine push is what propagates the local copy.
// The returned error joins per-mirror failures; cancellation wins immediately.
func SyncAll(ctx context.Context, cfg *config.Config) (int, error) {
	mirrors := Configured(cfg)
	if len(mirrors) == 0 {
		return 0, nil
	}
	timeout := time.Duration(cfg.Int("mirror_sync_timeout_seconds")) * time.Second
	if timeout <= 0 {
		timeout = DefaultSyncTimeout
	}
	root := strings.TrimRight(strings.TrimSpace(cfg.LocalRoot()), "/")

	synced := 0
	var failures []error
	for _, item := range mirrors {
		if err := ctx.Err(); err != nil {
			return synced, err
		}
		if !item.Enabled {
			continue
		}
		source := strings.TrimRight(strings.TrimSpace(item.Source), "/")
		dest := strings.Trim(strings.TrimSpace(item.Dest), "/")

		if err := ValidateMirror(cfg, source, dest); err != nil {
			failures = append(failures, fmt.Errorf("mirror %s: %w", source, err))
			recordMirrorError(cfg, item.Source, err.Error())
			continue
		}
		if err := mirrorGuard(cfg, source); err != nil {
			failures = append(failures, fmt.Errorf("mirror %s: %w", source, err))
			recordMirrorError(cfg, item.Source, err.Error())
			continue
		}

		destination := filepath.Join(root, filepath.FromSlash(dest))
		passCtx, cancel := context.WithTimeout(ctx, timeout)
		cmd := exec.CommandContext(passCtx, "rsync", RsyncCommand(cfg, source, destination)...)
		var stdout, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		err := cmd.Run()
		cancel()
		if err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return synced, ctxErr
			}
			detail := strings.TrimSpace(stderr.String())
			if detail == "" {
				detail = err.Error()
			}
			failures = append(failures, fmt.Errorf("mirror %s: %s", source, detail))
			recordMirrorError(cfg, item.Source, detail)
			continue
		}

		stamp := float64(time.Now().UnixNano()) / 1e9
		if err := patchMirror(cfg, item.Source, map[string]any{
			"last_sync": stamp, "error": nil,
		}); err != nil {
			failures = append(failures, fmt.Errorf("mirror %s: salvataggio last_sync: %w", source, err))
			continue
		}
		synced++
	}
	return synced, errors.Join(failures...)
}

// SyncOnce is the UI-facing entry point. The optional argument is accepted for
// source compatibility with the old signature that took a *remote.Client and
// acquired a NAS lease; mirrors are local now, so it is ignored.
func SyncOnce(cfg *config.Config, _ ...any) (int, error) {
	return SyncAll(context.Background(), cfg)
}

// mirrorGuard refuses to delete the destination when the authoritative source
// looks suspiciously empty, or when a configured sentinel is missing.
func mirrorGuard(cfg *config.Config, source string) error {
	minimum := cfg.Int("mirror_min_files")
	if minimum <= 0 {
		minimum = cfg.Int("delete_dir_guard_min_files")
	}
	if minimum <= 0 {
		minimum = DefaultMinFiles
	}
	count, err := countSourceFiles(source)
	if err != nil {
		return fmt.Errorf("conteggio sorgente mirror fallito: %w", err)
	}
	if count < minimum {
		return fmt.Errorf(
			"sorgente mirror con %d file (minimo %d): replica annullata per sicurezza",
			count, minimum,
		)
	}
	for _, value := range strings.Split(cfg.String("delete_sentinels"), ",") {
		sentinel := strings.TrimSpace(value)
		if sentinel == "" {
			continue
		}
		if err := paths.ValidateRelative(sentinel); err != nil {
			return fmt.Errorf("sentinella mirror non valida: %w", err)
		}
		if _, err := os.Stat(filepath.Join(source, filepath.FromSlash(sentinel))); err != nil {
			return fmt.Errorf("sentinella mirror mancante: %s", sentinel)
		}
	}
	return nil
}

// countSourceFiles counts regular files in source, ignoring NASBox internals
// and conflict/archive copies. It fails closed on walk errors.
func countSourceFiles(source string) (int, error) {
	count := 0
	err := filepath.WalkDir(source, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if rel == "." {
			return nil
		}
		if excludedMirrorPath(rel) {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.IsDir() {
			return nil
		}
		if entry.Type().IsRegular() {
			count++
		}
		return nil
	})
	return count, err
}

func excludedMirrorPath(rel string) bool {
	for _, part := range strings.Split(rel, "/") {
		for _, name := range MirrorExcludeDirs {
			if part == name {
				return true
			}
		}
	}
	name := filepath.Base(rel)
	return strings.Contains(name, "conflitto da ") || strings.Contains(name, "copia archiviata")
}

func configuredExcludes(cfg *config.Config) []string {
	var out []string
	switch value := cfg.Get("exclude_patterns").(type) {
	case []any:
		for _, item := range value {
			if text, ok := item.(string); ok {
				if text = strings.TrimSpace(text); text != "" {
					out = append(out, text)
				}
			}
		}
	case []string:
		for _, text := range value {
			if text = strings.TrimSpace(text); text != "" {
				out = append(out, text)
			}
		}
	}
	return out
}

var patchMu sync.Mutex

// patchMirror changes only the given fields of one entry, keeping every other
// mirror, every extra field and every other client.json key intact.
func patchMirror(cfg *config.Config, source string, fields map[string]any) error {
	patchMu.Lock()
	defer patchMu.Unlock()
	values, _ := cfg.Get("mirrors").([]any)
	found := false
	for _, item := range values {
		entry, ok := item.(map[string]any)
		if !ok || strings.TrimSpace(fmt.Sprint(entry["source"])) != source {
			continue
		}
		for key, value := range fields {
			if value == nil {
				delete(entry, key)
			} else {
				entry[key] = value
			}
		}
		found = true
	}
	if !found {
		return fmt.Errorf("mirror %s non presente nella configurazione", source)
	}
	return cfg.Set("mirrors", values, true)
}

// recordMirrorError writes the error only when it changes: a persistent
// failure must not rewrite client.json on every scheduler tick.
func recordMirrorError(cfg *config.Config, source, message string) {
	patchMu.Lock()
	values, _ := cfg.Get("mirrors").([]any)
	changed := false
	for _, item := range values {
		entry, ok := item.(map[string]any)
		if !ok || strings.TrimSpace(fmt.Sprint(entry["source"])) != source {
			continue
		}
		current, _ := entry["error"].(string)
		if current != message {
			changed = true
		}
		break
	}
	patchMu.Unlock()
	if changed {
		_ = patchMirror(cfg, source, map[string]any{"error": message})
	}
}

func canonicalPath(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		return filepath.Clean(path)
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		return resolved
	}
	return abs
}

func containedIn(parent, child string) bool {
	rel, err := filepath.Rel(parent, child)
	if err != nil {
		return false
	}
	if rel == "." {
		return true
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func rejectSymlinkComponents(path string) error {
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	current := string(filepath.Separator)
	for _, part := range strings.Split(filepath.ToSlash(abs), "/") {
		if part == "" {
			continue
		}
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return fmt.Errorf("impossibile controllare %s: %w", current, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("symlink nei componenti della sorgente mirror: %s", current)
		}
	}
	return nil
}
