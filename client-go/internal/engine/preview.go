// Package engine holds the decision logic shared by CLI and (later) UI.
// The NAS is never involved: preview is computed from a manifest snapshot and
// the local SQLite identity table.
package engine

import (
	"os"
	pathpkg "path"
	"path/filepath"
	"strings"
	"sync"

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
	return matcherFor(cfg).match(filepath.ToSlash(path))
}

// excludeMatcher is the compiled form of the exclusion rules. The previous
// implementation ran ~40 path.Match calls on the full path and on every
// component for each file: 5.2 s per 120k files, paid three times per pass.
// Same semantics, now plain map/prefix/suffix checks for the usual patterns.
type excludeMatcher struct {
	owner     *config.Config
	revision  uint64
	key       string
	selective []string
	names     map[string]bool // component equals
	suffixes  []string        // "*.pyc": component ends with
	prefixes  []string        // "~$*": component starts with
	compGlobs []string        // other slash-free globs, per component
	fullExact map[string]bool // pattern with "/" and no metacharacters
	fullGlobs []string        // pattern with "/" and metacharacters
}

var (
	matcherMu    sync.Mutex
	matcherCache *excludeMatcher
)

func stringValues(cfg *config.Config, key string) []string {
	values, ok := cfg.Get(key).([]any)
	if !ok {
		return nil
	}
	result := make([]string, 0, len(values))
	for _, value := range values {
		if text, ok := value.(string); ok {
			result = append(result, text)
		}
	}
	return result
}

// matcherFor returns the compiled matcher, rebuilt only when the
// configured patterns change (settings can be edited while running).
func matcherFor(cfg *config.Config) *excludeMatcher {
	revision := cfg.Revision()
	matcherMu.Lock()
	if cached := matcherCache; cached != nil && cached.owner == cfg && cached.revision == revision {
		matcherMu.Unlock()
		return cached
	}
	matcherMu.Unlock()
	selective := stringValues(cfg, "selective_sync_excludes")
	patterns := stringValues(cfg, "exclude_patterns")
	key := strings.Join(selective, "\x00") + "\x01" + strings.Join(patterns, "\x00")
	matcherMu.Lock()
	defer matcherMu.Unlock()
	if matcherCache != nil && matcherCache.key == key {
		matcherCache.owner, matcherCache.revision = cfg, revision
		return matcherCache
	}
	m := &excludeMatcher{owner: cfg, revision: revision, key: key, names: map[string]bool{}, fullExact: map[string]bool{}}
	for name := range internalDirNames {
		m.names[name] = true
	}
	for _, folder := range selective {
		if folder = strings.Trim(strings.TrimSpace(folder), "/"); folder != "" {
			m.selective = append(m.selective, folder)
		}
	}
	for _, raw := range append(append([]string{}, systemExcludes...), patterns...) {
		pattern := strings.TrimSuffix(strings.TrimSpace(strings.ReplaceAll(raw, `\`, "/")), "/")
		if pattern == "" {
			continue
		}
		meta := strings.ContainsAny(pattern, "*?[")
		switch {
		case strings.Contains(pattern, "/") && !meta:
			m.fullExact[pattern] = true
		case strings.Contains(pattern, "/"):
			m.fullGlobs = append(m.fullGlobs, pattern)
		case !meta:
			m.names[pattern] = true
		case strings.HasPrefix(pattern, "*") && !strings.ContainsAny(pattern[1:], "*?["):
			m.suffixes = append(m.suffixes, pattern[1:])
		case strings.HasSuffix(pattern, "*") && !strings.ContainsAny(pattern[:len(pattern)-1], "*?["):
			m.prefixes = append(m.prefixes, pattern[:len(pattern)-1])
		default:
			m.compGlobs = append(m.compGlobs, pattern)
		}
	}
	matcherCache = m
	return m
}

func (m *excludeMatcher) match(normalized string) bool {
	for _, folder := range m.selective {
		if normalized == folder || strings.HasPrefix(normalized, folder+"/") {
			return true
		}
	}
	if m.fullExact[normalized] {
		return true
	}
	for _, pattern := range m.fullGlobs {
		if matched, _ := pathpkg.Match(pattern, normalized); matched {
			return true
		}
	}
	rest := normalized
	for {
		part := rest
		slash := strings.IndexByte(rest, '/')
		if slash >= 0 {
			part = rest[:slash]
		}
		if m.names[part] {
			return true
		}
		for _, suffix := range m.suffixes {
			if strings.HasSuffix(part, suffix) {
				return true
			}
		}
		for _, prefix := range m.prefixes {
			if strings.HasPrefix(part, prefix) {
				return true
			}
		}
		for _, pattern := range m.compGlobs {
			if matched, _ := pathpkg.Match(pattern, part); matched {
				return true
			}
		}
		if slash < 0 {
			return false
		}
		rest = rest[slash+1:]
	}
}
