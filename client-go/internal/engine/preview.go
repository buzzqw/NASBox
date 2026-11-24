// Package engine holds the decision logic shared by CLI and (later) UI.
// The NAS is never involved: preview is computed from a manifest snapshot and
// the local SQLite identity table.
package engine

import (
	"os"
	pathpkg "path"
	"path/filepath"
	"strings"

	"nasbox-go/internal/classify"
	"nasbox-go/internal/config"
	"nasbox-go/internal/state"
)

// Preview classifies remote files that are missing from the local tree.
func Preview(
	cfg *config.Config,
	store *state.Store,
	manifest map[string]classify.RemoteState,
) (classify.Report, error) {
	root := cfg.LocalRoot()
	missing := make([]string, 0)
	for path, remote := range manifest {
		if remote.Kind != "FILE" || excluded(cfg, path) {
			continue
		}
		if _, err := os.Lstat(filepath.Join(root, filepath.FromSlash(path))); os.IsNotExist(err) {
			missing = append(missing, path)
		}
	}
	identities, err := store.IdentitySnapshot(cfg.LocalRepositoryID())
	if err != nil {
		return classify.Report{}, err
	}
	converted := make(map[string]classify.Identity, len(identities))
	for path, id := range identities {
		converted[path] = classify.Identity{
			Digest: id.Digest, Size: id.Size, CtimeNS: id.CtimeNS,
		}
	}
	ratio := 0.5
	minFiles := 20
	if value, ok := cfg.Get("delete_dir_guard_ratio").(float64); ok {
		ratio = value
	}
	if value, ok := cfg.Get("delete_dir_guard_min_files").(float64); ok {
		minFiles = int(value)
	}
	return classify.Classify(missing, manifest, converted, ratio, minFiles), nil
}

// internalDirNames are never user content: caches, VCS metadata and the two
// client-update channels live inside the NASBox tree but must never be synced
// or counted as deleted when absent locally.
var internalDirNames = map[string]bool{
	".sync-trash": true, ".sync-partial": true, ".nasbox-staging": true,
	".nasbox-root": true, ".nasbox-client-update": true,
	".nasbox-client-update-go": true, "@eaDir": true, ".git": true,
	".venv": true, ".hg": true, ".svn": true, "__pycache__": true,
	"node_modules": true,
}

// systemExcludes mirrors the Python client's DEFAULT_SYSTEM_EXCLUDES, so both
// implementations ignore the same generated files (tool caches, LaTeX
// intermediates, editor backups).
var systemExcludes = []string{
	".DS_Store", "Thumbs.db", "desktop.ini", ".~lock.*", "~$*", "*.tmp.*",
	"*.part", "*.crdownload", "__pycache__", "*.pyc", "*.pyo", ".venv",
	"venv", "node_modules", "*.swp", "*.swo", "*~", ".pytest_cache",
	".mypy_cache", ".ruff_cache", ".tox", ".coverage", "htmlcov", "*.egg-info",
	"*.aux", "*.fls", "*.fdb_latexmk", "*.synctex.gz", "*.toc", "*.lof",
	"*.lot", "*.bbl", "*.blg", "*.idx", "*.ind", "*.ilg", "*.xdv", "*.nav",
	"*.snm", "*.vrb", "*.bcf", "*.run.xml",
}

func excluded(cfg *config.Config, path string) bool {
	normalized := filepath.ToSlash(path)
	parts := strings.Split(normalized, "/")
	for _, part := range parts {
		if internalDirNames[part] {
			return true
		}
	}
	patterns := append([]string{}, systemExcludes...)
	for _, key := range []string{"exclude_patterns", "selective_sync_excludes"} {
		values, ok := cfg.Get(key).([]any)
		if !ok {
			continue
		}
		for _, value := range values {
			if text, ok := value.(string); ok {
				patterns = append(patterns, text)
			}
		}
	}
	for _, raw := range patterns {
		pattern := strings.TrimSuffix(strings.TrimSpace(strings.ReplaceAll(raw, `\`, "/")), "/")
		if pattern == "" {
			continue
		}
		if matched, _ := pathpkg.Match(pattern, normalized); matched {
			return true
		}
		// Slash-free patterns also match any single path component, so
		// "venv", "build" and "*.pyc" apply at any depth like the Python client.
		if !strings.Contains(pattern, "/") {
			for _, part := range parts {
				if matched, _ := pathpkg.Match(pattern, part); matched {
					return true
				}
			}
		}
	}
	return false
}
