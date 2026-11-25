package engine

import (
	"sort"
	"strings"

	"nasbox-go/internal/classify"
)

// dirMove is a folder that was renamed as a whole: every file the NAS has under
// Src now exists locally under Dst with the same relative path.
type dirMove struct {
	Src, Dst string
	Pairs    []classify.Rename
}

// minDirMoveFiles: below this a folder move saves nothing over per-file renames
// and is not worth the (new) code path.
const minDirMoveFiles = 2

// groupDirRenames recognises folders renamed as a whole among file renames, so
// a folder of 2.000 files moves in one request instead of 2.000. A folder
// qualifies only when EVERY file the manifest knows under it is part of the
// renames, with the same relative path under the new name, and the manifest
// has nothing under the new name. Anything else stays a per-file rename.
func groupDirRenames(
	renames []classify.Rename, manifest map[string]classify.RemoteState,
) ([]dirMove, []classify.Rename) {
	target := make(map[string]string, len(renames)) // missing -> local
	for _, rename := range renames {
		target[rename.Missing] = rename.Local
	}
	// Files per folder in the manifest, for the "every file moved" check.
	filesUnder := map[string][]string{}
	for path, state := range manifest {
		if state.Kind != "FILE" {
			continue
		}
		parts := strings.Split(path, "/")
		for depth := 1; depth < len(parts); depth++ {
			prefix := strings.Join(parts[:depth], "/")
			filesUnder[prefix] = append(filesUnder[prefix], path)
		}
	}
	occupied := func(dir string) bool {
		prefix := dir + "/"
		for path := range manifest {
			if path == dir || strings.HasPrefix(path, prefix) {
				return true
			}
		}
		return false
	}

	var moves []dirMove
	consumed := map[string]bool{}
	sorted := append([]classify.Rename(nil), renames...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Missing < sorted[j].Missing })
	tried := map[string]bool{}
	for _, rename := range sorted {
		if consumed[rename.Missing] {
			continue
		}
		srcParts := strings.Split(rename.Missing, "/")
		dstParts := strings.Split(rename.Local, "/")
		common := 0
		for common < len(srcParts) && common < len(dstParts) &&
			srcParts[len(srcParts)-1-common] == dstParts[len(dstParts)-1-common] {
			common++
		}
		// Topmost candidate first: the folder that keeps the most files.
		for suffix := common; suffix >= 1; suffix-- {
			srcDirParts := srcParts[:len(srcParts)-suffix]
			dstDirParts := dstParts[:len(dstParts)-suffix]
			if len(srcDirParts) == 0 || len(dstDirParts) == 0 {
				continue
			}
			src, dst := strings.Join(srcDirParts, "/"), strings.Join(dstDirParts, "/")
			if src == dst || strings.HasPrefix(dst, src+"/") || strings.HasPrefix(src, dst+"/") {
				continue // moving a folder into itself/its parent: leave it to per-file
			}
			key := src + "\x00" + dst
			if tried[key] {
				continue
			}
			tried[key] = true
			members := filesUnder[src]
			if len(members) < minDirMoveFiles {
				continue
			}
			pairs := make([]classify.Rename, 0, len(members))
			valid := true
			for _, member := range members {
				local, moved := target[member]
				rel := strings.TrimPrefix(member, src+"/")
				if !moved || consumed[member] || local != dst+"/"+rel {
					valid = false
					break
				}
				pairs = append(pairs, classify.Rename{Missing: member, Local: local, Reason: "folder"})
			}
			if !valid || occupied(dst) {
				continue
			}
			for _, pair := range pairs {
				consumed[pair.Missing] = true
			}
			moves = append(moves, dirMove{Src: src, Dst: dst, Pairs: pairs})
			break
		}
	}
	rest := make([]classify.Rename, 0, len(renames))
	for _, rename := range renames {
		if !consumed[rename.Missing] {
			rest = append(rest, rename)
		}
	}
	return moves, rest
}
