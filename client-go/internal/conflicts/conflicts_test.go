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

func TestCreateUsesPythonNaming(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	name, err := Create(root, "a.txt", "pc2", "abcdef123456")
	if err != nil {
		t.Fatal(err)
	}
	if name != "a (conflitto da pc2 abcdef12).txt" {
		t.Fatalf("bad conflict name: %q", name)
	}
}
