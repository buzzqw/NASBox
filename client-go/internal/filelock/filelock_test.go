package filelock

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func open(t *testing.T, path string) *os.File {
	t.Helper()
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Close() })
	return f
}

func TestExclusiveLockBlocksOtherHolderAndReleases(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.lock")
	first, second := open(t, path), open(t, path)
	if err := Lock(first, true, true); err != nil {
		t.Fatal(err)
	}
	if err := Lock(second, true, true); !errors.Is(err, ErrLocked) {
		t.Fatalf("un secondo lock esclusivo deve dare ErrLocked, ottenuto %v", err)
	}
	Unlock(first)
	if err := Lock(second, true, true); err != nil {
		t.Fatalf("dopo Unlock il lock deve essere libero: %v", err)
	}
}

func TestSharedLocksCoexistButExcludeWriters(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.lock")
	a, b, w := open(t, path), open(t, path), open(t, path)
	if err := Lock(a, false, true); err != nil {
		t.Fatal(err)
	}
	if err := Lock(b, false, true); err != nil {
		t.Fatalf("due lock condivisi devono coesistere: %v", err)
	}
	if err := Lock(w, true, true); !errors.Is(err, ErrLocked) {
		t.Fatalf("lo scrittore deve attendere i lettori: %v", err)
	}
}
