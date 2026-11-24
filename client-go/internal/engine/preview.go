// Package engine holds the decision logic shared by CLI and (later) UI.
// The NAS is never involved: preview is computed from a manifest snapshot and
// the local SQLite identity table.
package engine

import (
	"os"
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
	identities, err := store.IdentitySnapshot(cfg.RepositoryID())
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

func excluded(cfg *config.Config, path string) bool {
	patterns, ok := cfg.Get("exclude_patterns").([]any)
	if !ok {
		return false
	}
	for _, pattern := range patterns {
		text, ok := pattern.(string)
		if !ok {
			continue
		}
		text = strings.TrimSuffix(text, "/")
		if text != "" && (path == text || strings.HasPrefix(path, text+"/")) {
			return true
		}
	}
	return false
}
