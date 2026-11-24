package integrity

import (
	"os"
	"path/filepath"
	"testing"

	"nasbox-go/internal/classify"
	"nasbox-go/internal/config"
)

func TestRunDetectsChecksumDifference(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := config.New("")
	cfg.Set("local_root", root, false)
	manifest := map[string]classify.RemoteState{
		"a.txt": {Kind: "FILE", Digest: "wrong", Size: 5},
	}
	report := Run(cfg, manifest, "0.2.0", nil)
	if report.Status != "differences" || len(report.Differences) != 1 || report.FilesChecked != 1 {
		t.Fatalf("bad report: %+v", report)
	}
	if len(Load()) != 1 {
		t.Fatal("report was not persisted")
	}
}
