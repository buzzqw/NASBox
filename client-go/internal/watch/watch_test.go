package watch

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestWatcherEmitsChangedPath(t *testing.T) {
	root := t.TempDir()
	w := New(root, 50*time.Millisecond, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.Run(ctx)
	time.Sleep(100 * time.Millisecond)
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	select {
	case path := <-w.Events():
		if path != "a.txt" {
			t.Fatalf("unexpected path %q", path)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no event received")
	}
}
