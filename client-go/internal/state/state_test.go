package state

import (
	"os"
	"path/filepath"
	"testing"

	"nasbox-go/internal/config"
)

func TestObservationClockIsStableThenIncrements(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	store, err := Open(config.New(""))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	first, err := store.Observe("repo1", "a.txt", "pc1", Observation{
		Digest: "one", Size: 3, MTimeNS: 10, Present: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	same, err := store.Observe("repo1", "a.txt", "pc1", Observation{
		Digest: "one", Size: 3, MTimeNS: 10, Present: true,
	})
	if err != nil || same != first {
		t.Fatalf("identical observation changed clock: %q -> %q (%v)", first, same, err)
	}
	second, err := store.Observe("repo1", "a.txt", "pc1", Observation{
		Digest: "two", Size: 3, MTimeNS: 20, Present: true,
	})
	if err != nil || second == first {
		t.Fatalf("changed observation did not increment: %q -> %q (%v)", first, second, err)
	}
}

func TestDeviceIDIsPersistentAndUnique(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	cfg := config.New("")
	store, err := Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	first := store.DeviceID()
	if first == "" || first == "go-client" {
		t.Fatalf("invalid device ID %q", first)
	}
	if second := store.DeviceID(); second != first {
		t.Fatalf("device ID changed: %q -> %q", first, second)
	}
}

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
