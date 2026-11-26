package transfer

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPruneEmptyParentsStopsAtRootAndKeepsNonEmptyDirs(t *testing.T) {
	root := t.TempDir()
	nested := filepath.Join(root, "a", "b")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	keep := filepath.Join(root, "a", "keep.txt")
	if err := os.WriteFile(keep, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	pruneEmptyParents(root, nested)
	if _, err := os.Stat(nested); !os.IsNotExist(err) {
		t.Fatalf("empty staged dir should be removed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "a")); err != nil {
		t.Fatalf("parent with content must survive: %v", err)
	}
	if _, err := os.Stat(keep); err != nil {
		t.Fatalf("unrelated file must survive: %v", err)
	}
}

func TestCopyFile(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	dst := filepath.Join(dir, "dst")
	if err := os.WriteFile(src, []byte("contenuto"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := copyFile(src, dst); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(dst)
	if err != nil || string(got) != "contenuto" {
		t.Fatalf("copy mismatch: %q, %v", got, err)
	}
}
