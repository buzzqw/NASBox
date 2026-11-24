package engine

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRunnerSyncsAfterFilesystemEvent(t *testing.T) {
	cfg, store, root := setupTest(t)
	cfg.Set("debounce_seconds", float64(1), false)
	if err := os.MkdirAll(filepath.Join(root, "folder"), 0o700); err != nil {
		t.Fatal(err)
	}
	nas := &fakeNAS{config: nasConfig(), manifest: "NASBOX_MANIFEST_V1\n"}
	runner := NewRunner(cfg, store, nas, &fakeTransfer{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = runner.Run(ctx) }()
	time.Sleep(300 * time.Millisecond) // let the watcher install its inotify watch
	if err := os.WriteFile(filepath.Join(root, "folder", "new.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	deadline := time.After(8 * time.Second)
	for {
		select {
		case event := <-runner.Events():
			t.Logf("event: %s %s", event.Kind, event.Message)
			if event.Kind == "sync" {
				return
			}
		case <-deadline:
			t.Fatal("runner did not sync after a filesystem event")
		}
	}
}
