package engine

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"nasbox-go/internal/classify"
)

func renamesOf(pairs ...[2]string) []classify.Rename {
	out := make([]classify.Rename, 0, len(pairs))
	for _, pair := range pairs {
		out = append(out, classify.Rename{Missing: pair[0], Local: pair[1]})
	}
	return out
}

func manifestOf(paths ...string) map[string]classify.RemoteState {
	out := map[string]classify.RemoteState{}
	for _, path := range paths {
		out[path] = classify.RemoteState{Kind: "FILE", Digest: "d"}
	}
	return out
}

func TestGroupDirRenamesMovesAWholeFolderInOneRequest(t *testing.T) {
	manifest := manifestOf("A/x/1.txt", "A/x/2.txt", "A/3.txt", "Keep/k.txt")
	renames := renamesOf(
		[2]string{"A/x/1.txt", "B/x/1.txt"}, [2]string{"A/x/2.txt", "B/x/2.txt"}, [2]string{"A/3.txt", "B/3.txt"})
	moves, rest := groupDirRenames(renames, manifest)
	if len(moves) != 1 || moves[0].Src != "A" || moves[0].Dst != "B" || len(moves[0].Pairs) != 3 || len(rest) != 0 {
		t.Fatalf("moves=%+v rest=%+v", moves, rest)
	}
}

func TestGroupDirRenamesNeedsEveryFileOfTheFolder(t *testing.T) {
	// A/ also holds 3.txt, which was NOT renamed: moving the folder would drag it along.
	manifest := manifestOf("A/x/1.txt", "A/x/2.txt", "A/3.txt")
	renames := renamesOf([2]string{"A/x/1.txt", "B/x/1.txt"}, [2]string{"A/x/2.txt", "B/x/2.txt"})
	moves, rest := groupDirRenames(renames, manifest)
	// The sub-folder A/x moved as a whole to B/x, which is a valid (smaller) folder move.
	if len(moves) != 1 || moves[0].Src != "A/x" || moves[0].Dst != "B/x" || len(rest) != 0 {
		t.Fatalf("moves=%+v rest=%+v", moves, rest)
	}
}

func TestGroupDirRenamesLeavesSingleFilesAndOccupiedTargets(t *testing.T) {
	manifest := manifestOf("A/1.txt", "A/2.txt", "B/other.txt", "solo/f.txt")
	renames := renamesOf(
		[2]string{"A/1.txt", "B/1.txt"}, [2]string{"A/2.txt", "B/2.txt"}, // B is occupied on the NAS
		[2]string{"solo/f.txt", "elsewhere/f.txt"}) // one file: not worth a folder move
	moves, rest := groupDirRenames(renames, manifest)
	if len(moves) != 0 || len(rest) != 3 {
		t.Fatalf("moves=%+v rest=%+v", moves, rest)
	}
}

func TestGroupDirRenamesRequiresTheSameRelativePath(t *testing.T) {
	manifest := manifestOf("A/1.txt", "A/2.txt")
	renames := renamesOf([2]string{"A/1.txt", "B/1.txt"}, [2]string{"A/2.txt", "C/2.txt"})
	if moves, rest := groupDirRenames(renames, manifest); len(moves) != 0 || len(rest) != 2 {
		t.Fatalf("moves=%+v rest=%+v", moves, rest)
	}
}

func TestRemoveEmptyParentsKeepsNonEmptyFoldersAndTheRoot(t *testing.T) {
	root := t.TempDir()
	mustMk := func(path string) {
		if err := os.MkdirAll(filepath.Join(root, path), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	mustMk("a/b/c")
	mustMk("keep/sub")
	if err := os.WriteFile(filepath.Join(root, "keep", "file.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	removeEmptyParents(root, "a/b/c/gone.txt")
	if _, err := os.Stat(filepath.Join(root, "a")); !os.IsNotExist(err) {
		t.Fatal("the chain of empty folders must disappear")
	}
	removeEmptyParents(root, "keep/sub/gone.txt")
	if _, err := os.Stat(filepath.Join(root, "keep")); err != nil {
		t.Fatal("a folder that still holds a file must stay")
	}
	if _, err := os.Stat(root); err != nil {
		t.Fatal("the root must never be removed")
	}
}

func TestDownloadedStateTrustsMatchingFilesAndReadsChangedOnes(t *testing.T) {
	root := t.TempDir()
	full := filepath.Join(root, "f.txt")
	if err := os.WriteFile(full, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	when := time.Unix(1_700_000_000, 0)
	_ = os.Chtimes(full, when, when)
	same := classify.RemoteState{Kind: "FILE", Digest: "manifest-digest", Size: 5, MTimeNS: when.UnixNano()}
	if digest, _, _ := downloadedState(root, "f.txt", same); digest != "manifest-digest" {
		t.Fatalf("a file matching the manifest keeps the manifest digest, got %q", digest)
	}
	// The NAS changed after the manifest: different size and mtime arrive.
	stale := classify.RemoteState{Kind: "FILE", Digest: "old-digest", Size: 3, MTimeNS: when.Add(-time.Hour).UnixNano()}
	digest, size, _ := downloadedState(root, "f.txt", stale)
	if digest == "old-digest" || size != 5 || len(digest) != 64 {
		t.Fatalf("a changed file must be recorded by its real digest: %q %d", digest, size)
	}
}
