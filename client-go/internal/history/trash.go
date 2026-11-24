// Package history manages local trash versions (History/Trash tab).
package history

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"
	"time"
)

// The suffix may carry microseconds and the UTC marker (current clients) or
// only a local timestamp (versions written before 2025); both are restorable.
var versionRE = regexp.MustCompile(`^(.*)-(\d{4}-\d{2}-\d{2}--\d{2}-\d{2}-\d{2}(?:-\d{6}Z)?)$`)

// TrashStats is a cheap read-only summary used by storage insights.
type TrashStats struct {
	VersionCount int
	TotalBytes   int64
	ExpiredCount int
	ExpiredBytes int64
}

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
	sort.Slice(versions, func(i, j int) bool {
		left, leftErr := versionTime(versions[i].Timestamp)
		right, rightErr := versionTime(versions[j].Timestamp)
		if leftErr != nil || rightErr != nil {
			return versions[i].Timestamp > versions[j].Timestamp
		}
		return left.After(right)
	})
	return versions, nil
}

func versionTime(timestamp string) (time.Time, error) {
	if strings.HasSuffix(timestamp, "Z") {
		return time.ParseInLocation("2006-01-02--15-04-05-000000Z", timestamp, time.UTC)
	}
	return time.ParseInLocation("2006-01-02--15-04-05", timestamp, time.Local)
}

// Stats summarises the local trash without modifying it. Expired counts the
// versions older than retentionDays when retention is positive.
func Stats(retentionDays float64) (TrashStats, error) {
	versions, err := List()
	if err != nil {
		return TrashStats{}, err
	}
	now := time.Now()
	stats := TrashStats{VersionCount: len(versions)}
	for _, version := range versions {
		stats.TotalBytes += version.Size
		stamp, err := versionTime(version.Timestamp)
		if err != nil {
			continue
		}
		if retentionDays > 0 && now.Sub(stamp).Hours()/24 > retentionDays {
			stats.ExpiredCount++
			stats.ExpiredBytes += version.Size
		}
	}
	return stats, nil
}

// MoveToTrash moves a file from the synced tree into restorable local history,
// preserving its relative folder. It never follows symlinks and refuses paths
// outside root. A cross-device move falls back to copy+remove so a NASBox
// folder on another partition still keeps the version.
func MoveToTrash(localRoot, relative string) error {
	root := filepath.Clean(localRoot)
	clean := filepath.Clean(filepath.FromSlash(relative))
	if clean == "." || filepath.IsAbs(clean) || strings.HasPrefix(clean, "..") {
		return fmt.Errorf("percorso non valido: %s", relative)
	}
	source := filepath.Join(root, clean)
	info, err := os.Lstat(source)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("non è un file regolare: %s", relative)
	}
	dir, err := trashDir()
	if err != nil {
		return err
	}
	stamp := time.Now().UTC().Format("2006-01-02--15-04-05.000000")
	stamp = strings.Replace(stamp, ".", "-", 1) + "Z"
	destination := filepath.Join(dir, filepath.Dir(clean), clean+"-"+stamp)
	if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
		return err
	}
	if err := os.Rename(source, destination); err == nil {
		return nil
	} else if !errors.Is(err, syscall.EXDEV) {
		return err
	}
	if err := copyFile(source, destination, info.Mode().Perm()); err != nil {
		_ = os.Remove(destination)
		return err
	}
	return os.Remove(source)
}

func copyFile(source, destination string, mode os.FileMode) error {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	output, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(output, input); err != nil {
		_ = output.Close()
		return err
	}
	if err := output.Sync(); err != nil {
		_ = output.Close()
		return err
	}
	return output.Close()
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
		stamp, err := versionTime(version.Timestamp)
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
