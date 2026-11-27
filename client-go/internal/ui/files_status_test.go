package ui

import (
	"os"
	"path/filepath"
	"testing"

	"nasbox-go/internal/config"
	"nasbox-go/internal/conflicts"
	"nasbox-go/internal/remote"
	"nasbox-go/internal/transfer"
)

func TestBrowseFileStatus(t *testing.T) {
	root := t.TempDir()
	cfg := config.New("")
	if err := cfg.Set("local_root", root, false); err != nil {
		t.Fatal(err)
	}
	c := &clientApp{cfg: cfg}
	entry := remote.BrowseEntry{Name: "a.txt", Kind: "FILE", Size: 3}

	if got := c.browseFileStatus("a.txt", entry); got.Label != tr("ui.file.status_remote_only", nil) {
		t.Fatalf("missing local file: %+v", got)
	}
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("abc"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := c.browseFileStatus("a.txt", entry); got.Label != tr("ui.file.status_synced", nil) || got.Warning {
		t.Fatalf("same size: %+v", got)
	}
	entry.Size = 4
	if got := c.browseFileStatus("a.txt", entry); got.Label != tr("ui.file.status_not_synced", nil) || !got.Warning {
		t.Fatalf("different size: %+v", got)
	}

	c.ghost = true
	entry.Size = 3
	if got := c.browseFileStatus("a.txt", entry); got.Label != tr("ui.file.status_available", nil) {
		t.Fatalf("ghost mode: %+v", got)
	}
	c.ghost = false

	c.confGroups = []conflicts.Group{{Original: "a.txt"}}
	if got := c.browseFileStatus("a.txt", entry); got.Label != tr("ui.file.status_conflict", nil) || !got.Warning {
		t.Fatalf("conflict: %+v", got)
	}
	c.confGroups = nil

	c.progressMu.Lock()
	c.progress = transfer.Progress{File: "a.txt", Total: 1, Upload: true}
	c.progressMu.Unlock()
	if got := c.browseFileStatus("a.txt", entry); got.Label != tr("ui.file.status_uploading", nil) {
		t.Fatalf("uploading: %+v", got)
	}
	c.progressMu.Lock()
	c.progress = transfer.Progress{File: "a.txt", Total: 1, Upload: false}
	c.progressMu.Unlock()
	if got := c.browseFileStatus("a.txt", entry); got.Label != tr("ui.file.status_downloading", nil) {
		t.Fatalf("downloading: %+v", got)
	}
}

func TestBrowseFileStatusSkipsDirectoriesAndFinishedTransfers(t *testing.T) {
	c := &clientApp{cfg: config.New("")}
	if got := c.browseFileStatus("dir", remote.BrowseEntry{Kind: "DIR"}); got.Label != "" {
		t.Fatalf("directory status: %+v", got)
	}
	c.progressMu.Lock()
	c.progress = transfer.Progress{File: "a.txt", Total: 1, Finished: true}
	c.progressMu.Unlock()
	if got := c.browseTransferStatus("a.txt"); got != "" {
		t.Fatalf("finished transfer: %q", got)
	}
}
