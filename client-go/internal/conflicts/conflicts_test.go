package conflicts

import (
	"os"
	"path/filepath"
	"testing"
)

func TestScanAndResolveConflictGroup(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "doc.txt"), []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	copyName := "doc (conflitto da pc2 abcdef12).txt"
	if err := os.WriteFile(filepath.Join(root, copyName), []byte("copy"), 0o600); err != nil {
		t.Fatal(err)
	}
	groups, err := Scan(root, nil)
	if err != nil || len(groups) != 1 || groups[0].Original != "doc.txt" {
		t.Fatalf("bad groups: %+v %v", groups, err)
	}
	if err := Resolve(root, groups[0], copyName); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(filepath.Join(root, "doc.txt"))
	if err != nil || string(content) != "copy" {
		t.Fatalf("promotion failed: %q %v", content, err)
	}
	if _, err := os.Stat(filepath.Join(root, copyName)); !os.IsNotExist(err) {
		t.Fatal("conflict copy should have been moved to trash")
	}
}

func TestKeepBothPreservesOriginal(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "doc.txt"), []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	candidate := "doc (conflitto da pc2 abcdef12).txt"
	if err := os.WriteFile(filepath.Join(root, candidate), []byte("copy"), 0o600); err != nil {
		t.Fatal(err)
	}
	group := Group{Original: "doc.txt", Candidates: []string{candidate}}
	kept, err := KeepBoth(root, group, candidate)
	if err != nil {
		t.Fatal(err)
	}
	if kept == "" {
		t.Fatal("no kept path returned")
	}
	original, _ := os.ReadFile(filepath.Join(root, "doc.txt"))
	copyBytes, _ := os.ReadFile(filepath.Join(root, kept))
	if string(original) != "original" || string(copyBytes) != "copy" {
		t.Fatal("keep-both did not preserve both versions")
	}
}

func TestCreateUsesPythonNaming(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	name, err := Create(root, "a.txt", "pc2", "abcdef123456")
	if err != nil {
		t.Fatal(err)
	}
	if name != "a (conflitto da pc2 c7148ce3).txt" {
		t.Fatalf("bad conflict name: %q", name)
	}
}

func writeTestFile(t *testing.T, root, relative, content string) {
	t.Helper()
	fullPath := filepath.Join(root, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(fullPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fullPath, []byte(content), 0o640); err != nil {
		t.Fatal(err)
	}
}

func requireTestContent(t *testing.T, root, relative, want string) {
	t.Helper()
	content, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(relative)))
	if err != nil || string(content) != want {
		t.Fatalf("content of %s = %q, %v; want %q", relative, content, err, want)
	}
}
