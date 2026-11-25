package history

import (
	"os"
	"path/filepath"
	"testing"
	"time"
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

// MoveToTrash and rsync --backup-dir must share one layout, so List reports
// the right original path for files in subfolders.
func TestMoveToTrashKeepsOriginalPathForSubfolders(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "Documenti", "Progetti"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "Documenti", "Progetti", "a.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := MoveToTrash(root, "Documenti/Progetti/a.txt"); err != nil {
		t.Fatal(err)
	}
	versions, err := List()
	if err != nil {
		t.Fatal(err)
	}
	if len(versions) != 1 || versions[0].Original != "Documenti/Progetti/a.txt" {
		t.Fatalf("original path = %+v", versions)
	}
}

func TestPruneBeforeAndSummary(t *testing.T) {
	base := t.TempDir()
	t.Setenv("XDG_STATE_HOME", base)
	trash := filepath.Join(base, "sync-daemon", "trash")
	if err := os.MkdirAll(trash, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"old.txt-2026-01-01--07-00-00-000001Z": "12345",
		"new.txt-2026-11-24--07-00-00-000001Z": "123",
	} {
		if err := os.WriteFile(filepath.Join(trash, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	count, size, older, err := Summary(30)
	if err != nil || count != 2 || size != 8 || older != 1 {
		t.Fatalf("summary: %d %d %d %v", count, size, older, err)
	}
	removed, freed, err := PruneBefore(time.Now().AddDate(0, 0, -30))
	if err != nil || removed != 1 || freed != 5 {
		t.Fatalf("prune old: %d %d %v", removed, freed, err)
	}
	removed, freed, err = PruneBefore(time.Now().Add(24 * time.Hour))
	if err != nil || removed != 1 || freed != 3 {
		t.Fatalf("prune all: %d %d %v", removed, freed, err)
	}
}
