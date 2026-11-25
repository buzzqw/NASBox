package engine

import (
	"os"
	"path/filepath"
	"sort"
	"strconv"

	"nasbox-go/internal/causal"
	"nasbox-go/internal/classify"
	"nasbox-go/internal/config"
	"nasbox-go/internal/state"
)

// pullRename is a file another PC renamed on the NAS: the same bytes are
// already here, under the old name.
type pullRename struct {
	Src, Dst string
}

// planPullRenames pairs the downloads the NAS asks for with the local files it
// deleted, when they are the very same bytes (a rename made on another PC).
//
// A folder rename on one PC reaches every other PC as DELETE old paths + PUT new
// paths in the journal. The pull used to read that as "delete these files, then
// download those" and transferred every byte of the folder again, even though
// the files were right there under the old names. Only a digest and size match
// is a rename: anything else stays a real download and a real deletion.
func planPullRenames(
	downloads, localDeletes []string,
	local map[string]localFile,
	manifest map[string]classify.RemoteState,
) (renames []pullRename, keptDownloads, keptDeletes []string) {
	candidates := make(map[string][]string, len(localDeletes))
	for _, src := range localDeletes {
		current, ok := local[src]
		if !ok || current.Digest == "" {
			continue
		}
		key := pullKey(current.Digest, current.Size)
		candidates[key] = append(candidates[key], src)
	}
	for key := range candidates {
		sort.Strings(candidates[key])
	}

	sorted := append([]string(nil), downloads...)
	sort.Strings(sorted)
	used := make(map[string]bool, len(localDeletes))
	keptDownloads = make([]string, 0, len(downloads))
	for _, dst := range sorted {
		remoteState, ok := manifest[dst]
		if !ok || remoteState.Kind != "FILE" || remoteState.Digest == "" {
			keptDownloads = append(keptDownloads, dst)
			continue
		}
		key := pullKey(remoteState.Digest, remoteState.Size)
		picked := ""
		for _, src := range candidates[key] {
			if used[src] || src == dst {
				continue
			}
			picked = src
			break
		}
		if picked == "" {
			keptDownloads = append(keptDownloads, dst)
			continue
		}
		used[picked] = true
		renames = append(renames, pullRename{Src: picked, Dst: dst})
	}

	keptDeletes = make([]string, 0, len(localDeletes))
	for _, src := range localDeletes {
		if !used[src] {
			keptDeletes = append(keptDeletes, src)
		}
	}
	return renames, keptDownloads, keptDeletes
}

func pullKey(digest string, size int64) string {
	return digest + ":" + strconv.FormatInt(size, 10)
}

// applyPullRenames moves the local file onto its new name instead of
// downloading the same bytes again, and records both baselines (the new path
// with the NAS fingerprint, the old one as a tombstone). A file that changed
// since the scan, or a destination that appeared in the meantime, is left out
// and goes through the ordinary download and deletion paths, so a failure here
// can only cost a transfer, never a file.
func applyPullRenames(
	cfg *config.Config, store *state.Store, local map[string]localFile,
	manifest map[string]classify.RemoteState, renames []pullRename,
) (applied int, failed []pullRename, err error) {
	root := cfg.LocalRoot()
	for _, move := range renames {
		planned := local[move.Src]
		srcPath := filepath.Join(root, filepath.FromSlash(move.Src))
		dstPath := filepath.Join(root, filepath.FromSlash(move.Dst))
		info, statErr := os.Lstat(srcPath)
		if statErr != nil || !info.Mode().IsRegular() || info.Size() != planned.Size ||
			info.ModTime().UnixNano() != planned.MTimeNS {
			failed = append(failed, move)
			continue
		}
		if _, statErr := os.Lstat(dstPath); statErr == nil {
			failed = append(failed, move)
			continue
		}
		if mkErr := os.MkdirAll(filepath.Dir(dstPath), 0o777); mkErr != nil {
			failed = append(failed, move)
			continue
		}
		if renameErr := os.Rename(srcPath, dstPath); renameErr != nil {
			failed = append(failed, move)
			continue
		}
		remoteState := manifest[move.Dst]
		remoteCausal := remoteState.Causal
		if _, ok := causal.Parse(remoteCausal); !ok {
			remoteCausal = ""
		}
		digest, size, mtime := downloadedState(root, move.Dst, remoteState)
		if err := store.RecordEntry(cfg.LocalRepositoryID(), move.Dst, state.EntryState{
			Digest: digest, Size: size, MTimeNS: mtime, Causal: remoteCausal,
		}); err != nil {
			return applied, failed, err
		}
		// The old name is gone locally: record the tombstone the NAS journal
		// already carries, so the next pass does not see a local edit.
		if err := store.RecordEntry(cfg.LocalRepositoryID(), move.Src, state.EntryState{
			Causal: remoteCausal,
		}); err != nil {
			return applied, failed, err
		}
		removeEmptyParents(root, move.Src)
		applied++
	}
	return applied, failed, nil
}
