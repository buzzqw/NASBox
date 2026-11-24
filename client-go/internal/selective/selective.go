// Package selective implements selective sync (exclude whole directories).
package selective

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"nasbox-go/internal/config"
)

func ListDirectories(root string, limit int) []string {
	result := []string{}
	_ = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil || !entry.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil || rel == "." {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if strings.HasPrefix(rel, ".sync-trash") || strings.HasPrefix(rel, ".git") ||
			strings.HasPrefix(rel, ".venv") || strings.HasPrefix(rel, ".nasbox") {
			return filepath.SkipDir
		}
		result = append(result, rel)
		if limit > 0 && len(result) >= limit {
			return filepath.SkipAll
		}
		return nil
	})
	sort.Strings(result)
	return result
}

func ExcludedPaths(cfg *config.Config) []string {
	values, ok := cfg.Get("selective_sync_excludes").([]any)
	if !ok {
		return nil
	}
	result := make([]string, 0, len(values))
	for _, value := range values {
		if text, ok := value.(string); ok && text != "" {
			result = append(result, text)
		}
	}
	return result
}

func Excluded(cfg *config.Config, path string) bool {
	values, ok := cfg.Get("selective_sync_excludes").([]any)
	if !ok {
		return false
	}
	for _, value := range values {
		prefix, ok := value.(string)
		if !ok || prefix == "" {
			continue
		}
		if path == prefix || strings.HasPrefix(path, prefix+"/") {
			return true
		}
	}
	return false
}

func SetExcluded(cfg *config.Config, paths []string) {
	values := make([]any, 0, len(paths))
	for _, path := range paths {
		values = append(values, path)
	}
	cfg.Set("selective_sync_excludes", values, true)
}
