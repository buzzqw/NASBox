package history

import (
	"os"
	"path/filepath"
	"testing"
)

func TestListAndRestoreVersion(t *testing.T) {
	base := t.TempDir()
	t.Setenv("XDG_STATE_HOME", base)
	trash := filepath.Join(base, "sync-daemon", "trash", "folder")
	if err := os.MkdirAll(trash, 0o700); err != nil {
		t.Fatal(err)
	}
	name := "note.txt-2026-11-24--07-00-00-000001Z"
	if err := os.WriteFile(filepath.Join(trash, name), []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	versions, err := List()
	if err != nil || len(versions) != 1 || versions[0].Original != "folder/note.txt" {
		t.Fatalf("bad versions: %+v %v", versions, err)
	}
	root := t.TempDir()
	if err := Restore(versions[0], root); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(filepath.Join(root, "folder", "note.txt"))
	if err != nil || string(content) != "old" {
		t.Fatalf("restore failed: %q %v", content, err)
	}
}
