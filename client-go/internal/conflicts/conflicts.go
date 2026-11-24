// Package conflicts discovers, creates and safely resolves NASBox conflict
// copies. It has no UI or sync-engine dependencies.
package conflicts

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// ConflictTag is the marker shared with the Python client.
const ConflictTag = " (conflitto da "

var internalDirs = map[string]struct{}{
	".sync-partial": {},
	".sync-trash":   {},
	".nasbox-root":  {},
}

// Group contains the relative original path and its conflict copies. Scan
// leaves the metadata fields empty; List fills them without requiring a GUI or
// a sync-state database.
type Group struct {
	Original   string
	Candidates []string
	GroupID    string

	OriginalInfo FileMetadata
	Members      []FileMetadata
}

// FileMetadata describes one version in a conflict group. Digest is the
// SHA-256 of the file contents, not the short token embedded in its name.
// Missing files have Exists=false and zero-valued file attributes.
type FileMetadata struct {
	Path    string
	Origin  string
	Size    int64
	MTime   time.Time
	MTimeNS int64
	Digest  string
	Exists  bool
}

// Scan walks the local tree and groups regular conflict copies by original
// path. Internal sync directories and symbolic links are never followed.
func Scan(root string, excluded func(string) bool) ([]Group, error) {
	info, err := os.Stat(root)
	if os.IsNotExist(err) {
		return []Group{}, nil
	}
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("la cartella NASBox non e' una directory: %s", root)
	}

	walkRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil, err
	}
	groups := map[string][]string{}
	err = filepath.WalkDir(walkRoot, func(filePath string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			if filePath == walkRoot {
				return walkErr
			}
			return nil
		}
		if filePath == walkRoot {
			return nil
		}
		rel, err := filepath.Rel(walkRoot, filePath)
		if err != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if entry.IsDir() {
			if _, internal := internalDirs[entry.Name()]; internal {
				return filepath.SkipDir
			}
			return nil
		}
		if excluded != nil && excluded(rel) {
			return nil
		}
		entryInfo, err := entry.Info()
		if err != nil || !entryInfo.Mode().IsRegular() {
			return nil
		}
		original, ok := originalName(entry.Name())
		if !ok {
			return nil
		}
		originalRel := path.Join(path.Dir(rel), original)
		groups[originalRel] = append(groups[originalRel], rel)
		return nil
	})
	if err != nil {
		return nil, err
	}

	result := make([]Group, 0, len(groups))
	for original, candidates := range groups {
		sort.Strings(candidates)
		result = append(result, Group{
			Original:   original,
			Candidates: candidates,
			GroupID:    conflictGroupID(original),
		})
	}
	sort.Slice(result, func(i, j int) bool {
		left, right := strings.ToLower(result[i].Original), strings.ToLower(result[j].Original)
		if left == right {
			return result[i].Original < result[j].Original
		}
		return left < right
	})
	return result, nil
}

// List returns the same groups as Scan, enriched with content digests and
// filesystem metadata for both the original and every candidate. Individual
// unreadable or vanished files remain in the list with Exists=false or an empty
// Digest, so one transient filesystem error does not hide the whole group.
func List(root string, excluded func(string) bool) ([]Group, error) {
	groups, err := Scan(root, excluded)
	if err != nil {
		return nil, err
	}
	if len(groups) == 0 {
		return groups, nil
	}
	files, _, err := openRoot(root)
	if err != nil {
		return nil, err
	}
	defer files.Close()

	for index := range groups {
		group := &groups[index]
		group.OriginalInfo = readMetadata(files, group.Original, "")
		group.Members = make([]FileMetadata, 0, len(group.Candidates))
		for _, candidate := range group.Candidates {
			parts, ok := parseConflictName(path.Base(candidate))
			origin := ""
			if ok {
				origin = parts.origin
			}
			group.Members = append(group.Members, readMetadata(files, candidate, origin))
		}
	}
	return groups, nil
}

func conflictGroupID(original string) string {
	sum := sha256.Sum256([]byte(original))
	return hex.EncodeToString(sum[:12])
}

func readMetadata(files *os.Root, relative, origin string) FileMetadata {
	metadata := FileMetadata{Path: relative, Origin: origin}
	info, err := regularFileInfo(files, relative)
	if err != nil {
		return metadata
	}
	metadata.Exists = true
	metadata.Size = info.Size()
	metadata.MTime = info.ModTime()
	metadata.MTimeNS = info.ModTime().UnixNano()

	file, err := files.Open(relative)
	if err != nil {
		return metadata
	}
	defer file.Close()
	openedInfo, err := file.Stat()
	if err != nil || !openedInfo.Mode().IsRegular() || !os.SameFile(info, openedInfo) {
		return metadata
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return metadata
	}
	finalInfo, err := file.Stat()
	if err != nil || finalInfo.Size() != openedInfo.Size() || !finalInfo.ModTime().Equal(openedInfo.ModTime()) {
		return metadata
	}
	metadata.Digest = hex.EncodeToString(hash.Sum(nil))
	return metadata
}
