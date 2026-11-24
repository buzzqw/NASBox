// Package history manages local trash versions (History/Trash tab).
package history

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

var versionRE = regexp.MustCompile(`^(.*)-(\d{4}-\d{2}-\d{2}--\d{2}-\d{2}-\d{2}-\d{6}Z)$`)

type Version struct {
	TrashPath string
	Original  string
	Timestamp string
	Size      int64
}

func trashDir() (string, error) {
	base := os.Getenv("XDG_STATE_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		base = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(base, "sync-daemon", "trash"), nil
}

// List returns all restorable local versions, newest first.
func List() ([]Version, error) {
	dir, err := trashDir()
	if err != nil {
		return nil, err
	}
	var versions []Version
	err = filepath.WalkDir(dir, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil || entry.IsDir() || !entry.Type().IsRegular() {
			return nil
		}
		name := entry.Name()
		match := versionRE.FindStringSubmatch(name)
		if match == nil {
			return nil
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return nil
		}
		original := filepath.ToSlash(filepath.Join(filepath.Dir(rel), match[1]))
		versions = append(versions, Version{
			TrashPath: path, Original: original,
			Timestamp: match[2], Size: info.Size(),
		})
		return nil
	})
	if err != nil {
		return nil, err
	}
	return versions, nil
}

// Restore moves a version back to its original path. It refuses to overwrite.
func Restore(version Version, root string) error {
	target := filepath.Join(root, filepath.FromSlash(version.Original))
	if _, err := os.Stat(target); err == nil {
		return fmt.Errorf("il file originale esiste gia': %s", version.Original)
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return err
	}
	return os.Rename(version.TrashPath, target)
}

// Prune removes versions older than retentionDays. Returns the removed count.
func Prune(retentionDays int) (int, error) {
	if retentionDays <= 0 {
		retentionDays = 30
	}
	cutoff := time.Now().AddDate(0, 0, -retentionDays)
	versions, err := List()
	if err != nil {
		return 0, err
	}
	removed := 0
	for _, version := range versions {
		stamp, err := time.Parse("2006-01-02--15-04-05-000000Z", version.Timestamp)
		if err != nil || stamp.After(cutoff) {
			continue
		}
		if err := os.Remove(version.TrashPath); err == nil {
			removed++
		}
	}
	return removed, nil
}

// FindByOriginal returns versions for a given original relative path.
func FindByOriginal(original string) ([]Version, error) {
	all, err := List()
	if err != nil {
		return nil, err
	}
	var result []Version
	for _, version := range all {
		if version.Original == strings.TrimPrefix(original, "/") {
			result = append(result, version)
		}
	}
	return result, nil
}
