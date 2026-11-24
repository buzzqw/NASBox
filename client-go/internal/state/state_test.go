package state

import (
	"os"
	"path/filepath"
	"testing"

	"nasbox-go/internal/config"
)

func TestBackfillRecordsWholeTree(t *testing.T) {
	base := t.TempDir()
	t.Setenv("XDG_STATE_HOME", base)
	store, err := Open(config.New(""))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	root := t.TempDir()
	for _, name := range []string{"a.txt", "sub/b.txt", "sub/c.bin"} {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	count, err := store.BackfillIdentities("repo1", root, nil, nil)
	if err != nil || count != 3 {
		t.Fatalf("backfill failed: count=%d err=%v", count, err)
	}
	identities, err := store.IdentitySnapshot("repo1")
	if err != nil || len(identities) != 3 {
		t.Fatalf("snapshot failed: %+v %v", identities, err)
	}
}

func TestIdentityAndUndoRoundTrip(t *testing.T) {
	base := t.TempDir()
	t.Setenv("XDG_STATE_HOME", base)
	cfg := config.New("")
	store, err := Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	root := t.TempDir()
	file := filepath.Join(root, "a.txt")
	if err := os.WriteFile(file, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordIdentities("repo1", root, map[string]string{"a.txt": "deadbeef"}); err != nil {
		t.Fatal(err)
	}
	identities, err := store.IdentitySnapshot("repo1")
	if err != nil {
		t.Fatal(err)
	}
	if identities["a.txt"].Digest != "deadbeef" || identities["a.txt"].Ino == 0 {
		t.Fatalf("identity not stored: %+v", identities)
	}

	if err := store.RecordUndo("repo1", []string{"gone.txt"}, "2026-11-24--07-00-00-000001Z"); err != nil {
		t.Fatal(err)
	}
	if store.UndoCount("repo1", 24) != 1 {
		t.Fatal("undo count should be 1")
	}
	entries, err := store.RecentUndo("repo1", 24, 10)
	if err != nil || len(entries) != 1 || entries[0].Path != "gone.txt" {
		t.Fatalf("recent undo wrong: %+v %v", entries, err)
	}
	if err := store.MarkUndoApplied("repo1", "2026-11-24--07-00-00-000001Z", []string{"gone.txt"}); err != nil {
		t.Fatal(err)
	}
	if store.UndoCount("repo1", 24) != 0 {
		t.Fatal("undo should be consumed after apply")
	}
}
