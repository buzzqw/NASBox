package engine

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"nasbox-go/internal/classify"
	"nasbox-go/internal/config"
	"nasbox-go/internal/state"
)

// LocalSnapshot is the result of the previous local scan. With inotify
// healthy, the next pass rescans only the changed paths on top of it instead
// of walking the whole tree (2-3 s on 120k files, every pass).
type LocalSnapshot struct {
	files    map[string]localFile
	FullAt   time.Time // last complete walk
	Complete bool
	// Skipped lists the non-regular local entries (symlinks, fifos, sockets)
	// the scan ignored. Like the Python client they are never uploaded and
	// never reported as deletions; the Runner logs them once so a missing
	// file cannot disappear silently.
	Skipped []string
}

// ScanScope tells a pass how to obtain the local state.
type ScanScope struct {
	Previous *LocalSnapshot
	Dirty    []string // relative paths reported by the watcher
	Full     bool     // force a complete walk
}

func (s *LocalSnapshot) clone() map[string]localFile {
	files := make(map[string]localFile, len(s.files))
	for path, file := range s.files {
		files[path] = file
	}
	return files
}

// scanIncremental applies the dirty paths to the previous snapshot. Deleted
// paths drop their entry (and everything below them when they were folders),
// folders are rescanned as subtrees, files are fingerprinted again.
func scanIncremental(
	cfg *config.Config, identities map[string]state.Identity, previous *LocalSnapshot, dirty []string,
	skipped *[]string,
) (map[string]localFile, error) {
	root := cfg.LocalRoot()
	files := previous.clone()
	for _, raw := range dirty {
		rel := strings.Trim(path.Clean("/"+filepath.ToSlash(raw)), "/")
		if rel == "" || rel == "." {
			return nil, fmt.Errorf("rescan completo richiesto")
		}
		removeTree(files, rel)
		if skipLocalPath(cfg, rel) {
			continue
		}
		info, err := os.Lstat(filepath.Join(root, filepath.FromSlash(rel)))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("stat locale non riuscito per %s: %w", rel, err)
		}
		if info.IsDir() {
			if err := scanTree(cfg, identities, rel, files, skipped); err != nil {
				return nil, err
			}
			continue
		}
		if !info.Mode().IsRegular() {
			// Symlinks and special files are ignored, never fatal.
			if skipped != nil {
				*skipped = append(*skipped, rel)
			}
			continue
		}
		file, err := scanFile(filepath.Join(root, filepath.FromSlash(rel)), rel, info, identities)
		if err != nil {
			return nil, err
		}
		files[rel] = file
	}
	return files, nil
}

// carrySkipped keeps the previous pass's ignored entries that are not under a
// rescanned path, so an incremental pass reports the same set as a full walk.
func carrySkipped(previous *LocalSnapshot, dirty []string) []string {
	if previous == nil || len(previous.Skipped) == 0 {
		return nil
	}
	kept := make([]string, 0, len(previous.Skipped))
	for _, rel := range previous.Skipped {
		dropped := false
		for _, raw := range dirty {
			parent := strings.Trim(path.Clean("/"+filepath.ToSlash(raw)), "/")
			if parent == "" || rel == parent || strings.HasPrefix(rel, parent+"/") {
				dropped = true
				break
			}
		}
		if !dropped {
			kept = append(kept, rel)
		}
	}
	return kept
}

func removeTree(files map[string]localFile, rel string) {
	delete(files, rel)
	prefix := rel + "/"
	for key := range files {
		if strings.HasPrefix(key, prefix) {
			delete(files, key)
		}
	}
}

// incrementalConsistent double-checks the one decision an incremental scan
// could get dangerously wrong: a file the snapshot thinks is gone. Every
// known path missing from the result is verified on disk; if any exists the
// snapshot is stale and the caller falls back to a complete walk.
func incrementalConsistent(
	cfg *config.Config, files map[string]localFile,
	entries map[string]state.EntryState, manifest map[string]classify.RemoteState,
) bool {
	root := cfg.LocalRoot()
	check := func(rel string) bool {
		if _, ok := files[rel]; ok || skipLocalPath(cfg, rel) {
			return true
		}
		_, err := os.Lstat(filepath.Join(root, filepath.FromSlash(rel)))
		return os.IsNotExist(err)
	}
	for rel, entry := range entries {
		if entry.Digest != "" && !check(rel) {
			return false
		}
	}
	for rel, remote := range manifest {
		if remote.Kind == "FILE" && !check(rel) {
			return false
		}
	}
	return true
}

// localScan returns the local state for a pass and the snapshot to keep.
func localScan(
	cfg *config.Config, identities map[string]state.Identity, scope *ScanScope,
	entries map[string]state.EntryState, manifest map[string]classify.RemoteState,
) (map[string]localFile, *LocalSnapshot, string, error) {
	if scope != nil && !scope.Full && scope.Previous != nil && scope.Previous.Complete {
		skipped := carrySkipped(scope.Previous, scope.Dirty)
		files, err := scanIncremental(cfg, identities, scope.Previous, scope.Dirty, &skipped)
		if err == nil && incrementalConsistent(cfg, files, entries, manifest) {
			return files, &LocalSnapshot{
					files: files, FullAt: scope.Previous.FullAt, Complete: true, Skipped: skipped,
				},
				fmt.Sprintf("incrementale(%d)", len(scope.Dirty)), nil
		}
	}
	skipped := make([]string, 0)
	files, err := scanLocal(cfg, identities, &skipped)
	if err != nil {
		return nil, nil, "completa", err
	}
	return files, &LocalSnapshot{
		files: files, FullAt: time.Now(), Complete: true, Skipped: skipped,
	}, "completa", nil
}
