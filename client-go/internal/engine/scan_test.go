package engine

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"nasbox-go/internal/classify"
	"nasbox-go/internal/config"
	"nasbox-go/internal/state"
)

func writeLocal(t *testing.T, root, rel, content string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestIncrementalScanEqualsFullScan(t *testing.T) {
	root := t.TempDir()
	cfg := config.New("")
	cfg.Set("local_root", root, false)
	for _, rel := range []string{"a.txt", "doc/b.txt", "doc/sub/c.txt", "foto/d.jpg", "foto/e.jpg"} {
		writeLocal(t, root, rel, rel)
	}
	identities := map[string]state.Identity{}
	first, snapshot, mode, err := localScan(cfg, identities, nil, nil, nil)
	if err != nil || mode != "completa" || len(first) != 5 {
		t.Fatalf("first scan %v %q %d", err, mode, len(first))
	}
	// Changes the watcher would report.
	writeLocal(t, root, "a.txt", "modificato")
	writeLocal(t, root, "nuova/f.txt", "nuovo")
	if err := os.Remove(filepath.Join(root, "doc", "b.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(root, "foto")); err != nil {
		t.Fatal(err)
	}
	scope := &ScanScope{Previous: snapshot, Dirty: []string{"a.txt", "nuova", "doc/b.txt", "foto"}}
	incremental, _, mode, err := localScan(cfg, identities, scope, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if mode == "completa" {
		t.Fatal("expected an incremental scan")
	}
	full, err := scanLocal(cfg, identities)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(incremental, full) {
		t.Fatalf("incremental differs from full scan:\n%v\n%v", incremental, full)
	}
}

// A stale snapshot (a file the watcher never reported) must never make a
// present file look deleted: the pass falls back to a complete walk.
func TestIncrementalScanFallsBackWhenSnapshotIsStale(t *testing.T) {
	root := t.TempDir()
	cfg := config.New("")
	cfg.Set("local_root", root, false)
	writeLocal(t, root, "a.txt", "a")
	_, snapshot, _, err := localScan(cfg, map[string]state.Identity{}, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	writeLocal(t, root, "missed.txt", "created while the watcher was not looking")
	manifest := map[string]classify.RemoteState{"missed.txt": {Kind: "FILE", Digest: "x"}}
	files, _, mode, err := localScan(cfg, map[string]state.Identity{},
		&ScanScope{Previous: snapshot, Dirty: []string{"a.txt"}}, nil, manifest)
	if err != nil {
		t.Fatal(err)
	}
	if mode != "completa" {
		t.Fatalf("stale snapshot must force a complete scan, got %q", mode)
	}
	if _, ok := files["missed.txt"]; !ok {
		t.Fatal("the missed file must be found by the fallback scan")
	}
}
