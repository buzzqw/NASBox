// Package storage implements the read-only "storage insights" scan of the
// local NASBox tree (largest folders, duplicate files, local trash usage) and
// the guided cleanup of duplicate copies. It is a direct port of the Python
// client's metrics_worker storage helpers so both clients show the same
// numbers and apply the same safety rules.
//
// Every scan is bounded: a pathological tree (huge folder, symlink farm,
// unreadable directory) can slow the analysis but never block it forever. No
// function in this package touches the NAS, SSH or the sync journal.
package storage

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"nasbox-go/internal/config"
	"nasbox-go/internal/history"
)

const (
	// TopFolders is how many folder rows the UI shows.
	TopFolders = 20
	// MaxDuplicateCandidates bounds how many files are hashed.
	MaxDuplicateCandidates = 2000
	// MaxFiles bounds the whole scan.
	MaxFiles = 50000
	// MaxFilesPerDir keeps one enormous folder from consuming the budget.
	MaxFilesPerDir = 10000
	// MaxDirs bounds the directory bookkeeping.
	MaxDirs = 20000
	// hashChunk is the read buffer used while hashing candidates.
	hashChunk = 1 << 20
	// progressEvery controls how often the progress callback fires.
	progressEvery = 250
)

// FolderSize is the subtree size of one directory below the NASBox root.
// Path is relative to the root and "." denotes the root itself.
type FolderSize struct {
	Path      string `json:"path"`
	SizeBytes int64  `json:"size_bytes"`
	FileCount int    `json:"file_count"`
}

// DuplicateGroup is a set of files with identical size and SHA-256 digest.
type DuplicateGroup struct {
	SizeBytes int64    `json:"size_bytes"`
	SHA256    string   `json:"sha256"`
	Paths     []string `json:"paths"`
}

// WastedBytes is the space reclaimable by keeping a single copy.
func (g DuplicateGroup) WastedBytes() int64 {
	if len(g.Paths) < 2 {
		return 0
	}
	return g.SizeBytes * int64(len(g.Paths)-1)
}

// TrashInsight is a read-only summary of the local version history.
type TrashInsight struct {
	VersionCount  int     `json:"version_count"`
	TotalBytes    int64   `json:"total_bytes"`
	ExpiredCount  int     `json:"expired_count"`
	ExpiredBytes  int64   `json:"expired_bytes"`
	RetentionDays float64 `json:"retention_days"`
}

// Insights is one complete storage snapshot.
type Insights struct {
	Root                   string           `json:"root"`
	CollectedAt            time.Time        `json:"collected_at"`
	ScannedFiles           int              `json:"scanned_files"`
	ScanErrors             int              `json:"scan_errors"`
	CandidatesLimitReached bool             `json:"candidates_limit_reached"`
	TopFolders             []FolderSize     `json:"top_folders"`
	Duplicates             []DuplicateGroup `json:"duplicates"`
	DuplicateCandidates    int              `json:"duplicate_candidates"`
	DuplicateWastedBytes   int64            `json:"duplicate_wasted_bytes"`
	Trash                  TrashInsight     `json:"trash"`
}

// Progress receives the number of files examined so far.
type Progress func(examined int)

type fileRecord struct {
	relative string
	absolute string
	size     int64
}

type dirStat struct {
	size  int64
	files int
}

// Collect analyses the configured local root.
func Collect(cfg *config.Config, progress Progress) Insights {
	return CollectRoot(cfg.LocalRoot(), retentionDays(cfg), progress)
}

// CollectRoot analyses an explicit root. Tests and the CLI use it to point at
// a temporary tree without mutating the real configuration.
func CollectRoot(root string, retention float64, progress Progress) Insights {
	out := Insights{
		Root:        root,
		CollectedAt: time.Now(),
		TopFolders:  []FolderSize{},
		Duplicates:  []DuplicateGroup{},
	}
	out.Trash = trashInsight(retention)
	if strings.TrimSpace(root) == "" {
		return out
	}
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		return out
	}
	folders, files, errors := scanFolders(root, progress)
	out.TopFolders = folders
	out.ScannedFiles = len(files)
	out.ScanErrors = errors
	candidates, groups, wasted, limited := findDuplicates(files, progress)
	out.DuplicateCandidates = candidates
	out.Duplicates = groups
	out.DuplicateWastedBytes = wasted
	out.CandidatesLimitReached = limited
	return out
}

func retentionDays(cfg *config.Config) float64 {
	switch value := cfg.Get("retention_days_local").(type) {
	case float64:
		return value
	case int:
		return float64(value)
	case int64:
		return float64(value)
	}
	return 0
}

func trashInsight(retention float64) TrashInsight {
	stats, err := history.Stats(retention)
	if err != nil {
		return TrashInsight{RetentionDays: retention}
	}
	return TrashInsight{
		VersionCount:  stats.VersionCount,
		TotalBytes:    stats.TotalBytes,
		ExpiredCount:  stats.ExpiredCount,
		ExpiredBytes:  stats.ExpiredBytes,
		RetentionDays: retention,
	}
}

// scanFolders walks root once, returning the largest folders, the file records
// needed for duplicate detection and the number of unreadable entries.
// Symlinks are never followed or counted, matching the Python scanner.
func scanFolders(root string, progress Progress) ([]FolderSize, []fileRecord, int) {
	root = filepath.Clean(root)
	sizes := map[string]*dirStat{root: {}}
	parent := map[string]string{root: ""}
	perDirFiles := map[string]int{}
	files := make([]fileRecord, 0)
	errors := 0
	stack := []string{root}

	for len(stack) > 0 {
		current := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		entries, err := os.ReadDir(current)
		if err != nil {
			errors++
			continue
		}
		subdirs := make([]string, 0)
		for _, entry := range entries {
			name := entry.Name()
			path := filepath.Join(current, name)
			if entry.Type()&os.ModeSymlink != 0 {
				continue
			}
			if entry.IsDir() {
				if len(sizes) >= MaxDirs {
					continue
				}
				sizes[path] = &dirStat{}
				parent[path] = current
				subdirs = append(subdirs, path)
				continue
			}
			if !entry.Type().IsRegular() {
				continue
			}
			if perDirFiles[current] >= MaxFilesPerDir || len(files) >= MaxFiles {
				continue
			}
			info, err := entry.Info()
			if err != nil {
				errors++
				continue
			}
			perDirFiles[current]++
			stat := sizes[current]
			stat.size += info.Size()
			stat.files++
			relative, err := filepath.Rel(root, path)
			if err != nil {
				relative = name
			}
			files = append(files, fileRecord{
				relative: filepath.ToSlash(relative),
				absolute: path,
				size:     info.Size(),
			})
			if progress != nil && len(files)%progressEvery == 0 {
				progress(len(files))
			}
		}
		// Push in reverse so the alphabetically first folder is processed
		// first, exactly like os.scandir + sorted in the Python client.
		for index := len(subdirs) - 1; index >= 0; index-- {
			stack = append(stack, subdirs[index])
		}
	}

	// Accumulate descendant sizes into ancestors, deepest directories first.
	ordered := make([]string, 0, len(sizes))
	for directory := range sizes {
		ordered = append(ordered, directory)
	}
	sort.Slice(ordered, func(i, j int) bool {
		return pathDepth(ordered[i]) > pathDepth(ordered[j])
	})
	for _, directory := range ordered {
		up, ok := parent[directory]
		if !ok || up == "" {
			continue
		}
		if target, ok := sizes[up]; ok {
			target.size += sizes[directory].size
			target.files += sizes[directory].files
		}
	}

	folders := make([]FolderSize, 0, len(sizes))
	for directory, stat := range sizes {
		if stat.files == 0 {
			continue
		}
		relative, err := filepath.Rel(root, directory)
		if err != nil {
			relative = directory
		}
		folders = append(folders, FolderSize{
			Path:      filepath.ToSlash(relative),
			SizeBytes: stat.size,
			FileCount: stat.files,
		})
	}
	sort.Slice(folders, func(i, j int) bool {
		if folders[i].SizeBytes != folders[j].SizeBytes {
			return folders[i].SizeBytes > folders[j].SizeBytes
		}
		return folders[i].Path < folders[j].Path
	})
	if len(folders) > TopFolders {
		folders = folders[:TopFolders]
	}
	if progress != nil {
		progress(len(files))
	}
	return folders, files, errors
}

func pathDepth(path string) int {
	return strings.Count(filepath.Clean(path), string(os.PathSeparator))
}

// findDuplicates groups a bounded sample of files by (size, sha256). Empty
// files are ignored: they are never meaningful reclaimable space.
func findDuplicates(files []fileRecord, progress Progress) (int, []DuplicateGroup, int64, bool) {
	nonEmpty := make([]fileRecord, 0, len(files))
	for _, record := range files {
		if record.size > 0 {
			nonEmpty = append(nonEmpty, record)
		}
	}
	candidates := nonEmpty
	limited := false
	if len(candidates) > MaxDuplicateCandidates {
		candidates = candidates[:MaxDuplicateCandidates]
		limited = true
	}
	bySize := map[int64][]fileRecord{}
	for _, record := range candidates {
		bySize[record.size] = append(bySize[record.size], record)
	}
	groups := make([]DuplicateGroup, 0)
	for size, records := range bySize {
		if len(records) < 2 {
			continue
		}
		byDigest := map[string][]string{}
		for _, record := range records {
			digest, err := hashFile(record.absolute)
			if err != nil {
				continue
			}
			byDigest[digest] = append(byDigest[digest], record.relative)
		}
		for digest, paths := range byDigest {
			if len(paths) < 2 {
				continue
			}
			sort.Strings(paths)
			groups = append(groups, DuplicateGroup{SizeBytes: size, SHA256: digest, Paths: paths})
		}
	}
	sort.Slice(groups, func(i, j int) bool {
		if groups[i].WastedBytes() != groups[j].WastedBytes() {
			return groups[i].WastedBytes() > groups[j].WastedBytes()
		}
		return firstPath(groups[i]) < firstPath(groups[j])
	})
	wasted := int64(0)
	for _, group := range groups {
		wasted += group.WastedBytes()
	}
	if len(groups) > TopFolders {
		groups = groups[:TopFolders]
	}
	if progress != nil {
		progress(len(candidates))
	}
	return len(candidates), groups, wasted, limited
}

func firstPath(group DuplicateGroup) string {
	if len(group.Paths) == 0 {
		return ""
	}
	return group.Paths[0]
}

func hashFile(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	digest := sha256.New()
	buffer := make([]byte, hashChunk)
	for {
		read, readErr := file.Read(buffer)
		if read > 0 {
			_, _ = digest.Write(buffer[:read])
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return "", readErr
		}
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

// CleanupResult reports the outcome of a duplicate cleanup.
type CleanupResult struct {
	Moved  []string `json:"moved"`
	Errors []string `json:"errors"`
}

// RemoveDuplicates moves the selected paths into the local trash. It never
// accepts the first ("keep") path of a duplicate group, a path outside root, a
// symlink or a directory. The caller is responsible for user confirmation.
func RemoveDuplicates(root string, groups []DuplicateGroup, selected []string) CleanupResult {
	result := CleanupResult{Moved: []string{}, Errors: []string{}}
	keep := map[string]bool{}
	allowed := map[string]bool{}
	for _, group := range groups {
		if len(group.Paths) == 0 {
			continue
		}
		keep[group.Paths[0]] = true
		for _, path := range group.Paths[1:] {
			allowed[path] = true
		}
	}
	seen := map[string]bool{}
	for _, relative := range selected {
		if seen[relative] {
			continue
		}
		seen[relative] = true
		if keep[relative] {
			result.Errors = append(result.Errors, fmt.Sprintf("il file da mantenere non può essere rimosso: %s", relative))
			continue
		}
		if !allowed[relative] {
			result.Errors = append(result.Errors, fmt.Sprintf("percorso non è un duplicato rimovibile: %s", relative))
			continue
		}
		if err := history.MoveToTrash(root, relative); err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("%s: %v", relative, err))
			continue
		}
		result.Moved = append(result.Moved, relative)
	}
	return result
}
