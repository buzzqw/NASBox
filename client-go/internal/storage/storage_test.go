package storage

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"nasbox-go/internal/config"
)

func writeFile(t *testing.T, root, relative string, content []byte) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestCollectRootFindsFoldersAndDuplicates(t *testing.T) {
	stateHome := t.TempDir()
	t.Setenv("XDG_STATE_HOME", stateHome)
	root := t.TempDir()
	contentA := []byte("unique-Aunique-Aunique-A")
	contentB := []byte("unique-Bunique-B")
	writeFile(t, root, "docs/a.txt", contentA)
	writeFile(t, root, "media/a_copy.txt", contentA)
	writeFile(t, root, "docs/b.txt", contentB)
	writeFile(t, root, "media/b_copy.txt", contentB)
	writeFile(t, root, "single.txt", []byte("only one"))

	trash := filepath.Join(stateHome, "sync-daemon", "trash", "old")
	if err := os.MkdirAll(trash, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(trash, "old.txt-2020-01-01--00-00-00-000000Z"), make([]byte, 500), 0o600); err != nil {
		t.Fatal(err)
	}

	var progress []int
	result := CollectRoot(root, 30, func(examined int) { progress = append(progress, examined) })

	if result.ScannedFiles != 5 {
		t.Fatalf("scanned files = %d, want 5", result.ScannedFiles)
	}
	if result.ScanErrors != 0 {
		t.Fatalf("scan errors = %d, want 0", result.ScanErrors)
	}
	folders := map[string]FolderSize{}
	for _, folder := range result.TopFolders {
		folders[folder.Path] = folder
	}
	if folders["docs"].SizeBytes != int64(len(contentA)+len(contentB)) || folders["docs"].FileCount != 2 {
		t.Fatalf("docs folder = %+v", folders["docs"])
	}
	if folders["media"].SizeBytes != folders["docs"].SizeBytes {
		t.Fatalf("media folder = %+v", folders["media"])
	}
	wantRoot := int64(len(contentA)*2 + len(contentB)*2 + len("only one"))
	if folders["."].SizeBytes != wantRoot {
		t.Fatalf("root size = %d, want %d", folders["."].SizeBytes, wantRoot)
	}
	if len(result.Duplicates) != 2 {
		t.Fatalf("duplicates = %d, want 2", len(result.Duplicates))
	}
	for _, group := range result.Duplicates {
		if len(group.Paths) != 2 || group.WastedBytes() != group.SizeBytes {
			t.Fatalf("unexpected group %+v", group)
		}
	}
	if result.DuplicateWastedBytes != int64(len(contentA)+len(contentB)) {
		t.Fatalf("wasted = %d", result.DuplicateWastedBytes)
	}
	if result.Trash.VersionCount != 1 || result.Trash.TotalBytes != 500 ||
		result.Trash.ExpiredCount != 1 || result.Trash.ExpiredBytes != 500 {
		t.Fatalf("trash = %+v", result.Trash)
	}
	if len(progress) == 0 || progress[len(progress)-1] != 5 {
		t.Fatalf("progress = %v", progress)
	}
}

func TestCollectRootMissingRootIsEmpty(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	result := CollectRoot(filepath.Join(t.TempDir(), "missing"), 0, nil)
	if result.ScannedFiles != 0 || len(result.TopFolders) != 0 || len(result.Duplicates) != 0 {
		t.Fatalf("unexpected insights: %+v", result)
	}
}

func TestDuplicateCandidatesAreBounded(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	root := t.TempDir()
	for index := 0; index <= MaxDuplicateCandidates; index++ {
		writeFile(t, root, fmt.Sprintf("files/file-%05d.bin", index), make([]byte, index+1))
	}
	result := CollectRoot(root, 0, nil)
	if result.DuplicateCandidates != MaxDuplicateCandidates {
		t.Fatalf("candidates = %d, want %d", result.DuplicateCandidates, MaxDuplicateCandidates)
	}
	if !result.CandidatesLimitReached {
		t.Fatal("limit flag not set")
	}
	if len(result.Duplicates) != 0 {
		t.Fatalf("duplicates = %d, want 0", len(result.Duplicates))
	}
}

func TestRemoveDuplicatesKeepsFirstPathAndMovesSelected(t *testing.T) {
	stateHome := t.TempDir()
	t.Setenv("XDG_STATE_HOME", stateHome)
	root := t.TempDir()
	content := []byte("same-bytes")
	writeFile(t, root, "keep.txt", content)
	writeFile(t, root, "copy.txt", content)
	writeFile(t, root, "other.txt", []byte("different"))

	group := DuplicateGroup{SizeBytes: int64(len(content)), SHA256: "x", Paths: []string{"keep.txt", "copy.txt"}}
	result := RemoveDuplicates(root, []DuplicateGroup{group}, []string{"keep.txt", "copy.txt", "missing.txt"})
	if len(result.Moved) != 1 || result.Moved[0] != "copy.txt" {
		t.Fatalf("moved = %v", result.Moved)
	}
	if len(result.Errors) != 2 {
		t.Fatalf("errors = %v", result.Errors)
	}
	if _, err := os.Stat(filepath.Join(root, "copy.txt")); !os.IsNotExist(err) {
		t.Fatal("copy.txt still exists in the tree")
	}
	if _, err := os.Stat(filepath.Join(root, "keep.txt")); err != nil {
		t.Fatal("keep.txt was removed")
	}
	trashDir := filepath.Join(stateHome, "sync-daemon", "trash")
	entries, err := os.ReadDir(trashDir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("trash entries = %v, err = %v", entries, err)
	}
	versionPath := filepath.Join(trashDir, entries[0].Name())
	if _, err := os.Stat(versionPath); err != nil {
		t.Fatalf("version file %s: %v", versionPath, err)
	}
	if captured, err := os.ReadFile(versionPath); err != nil || string(captured) != string(content) {
		t.Fatalf("version content = %q, err = %v", captured, err)
	}
}

func TestRemoveDuplicatesRejectsPathTraversal(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	root := t.TempDir()
	group := DuplicateGroup{SizeBytes: 1, Paths: []string{"keep.txt", "../outside.txt"}}
	result := RemoveDuplicates(root, []DuplicateGroup{group}, []string{"../outside.txt"})
	if len(result.Moved) != 0 || len(result.Errors) == 0 {
		t.Fatalf("result = %+v", result)
	}
}

func TestCollectUsesConfiguredRoot(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	root := t.TempDir()
	writeFile(t, root, "a.txt", []byte("x"))
	cfg := config.New("")
	cfg.Set("local_root", root, false)
	result := Collect(cfg, nil)
	if result.Root != root || result.ScannedFiles != 1 {
		t.Fatalf("result = %+v", result)
	}
}
