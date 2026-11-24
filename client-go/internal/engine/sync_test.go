package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"nasbox-go/internal/config"
	"nasbox-go/internal/remote"
	"nasbox-go/internal/state"
)

type fakeNAS struct {
	config   map[string]string
	manifest string
	deleted  []remote.CheckedRequest
}

func (f *fakeNAS) PrintConfig() (map[string]string, error) { return f.config, nil }
func (f *fakeNAS) ManifestExport(string) ([]byte, error)   { return []byte(f.manifest), nil }
func (f *fakeNAS) CheckedDelete(reqs []remote.CheckedRequest, _ string, _ string) (map[string]string, error) {
	f.deleted = reqs
	out := map[string]string{}
	for _, req := range reqs {
		out[req.Path] = "DELETED"
	}
	return out, nil
}

func (f *fakeNAS) RetryPendingJournal(string) (bool, string, error) { return true, "", nil }
func (f *fakeNAS) JournalAppend(string, string, string, []remote.JournalPut) (bool, error) {
	return true, nil
}
func (f *fakeNAS) RenameV2(string, string, string, string, int64, int64, string) (bool, error) {
	return true, nil
}

type fakeTransfer struct {
	uploaded   []string
	downloaded []string
}

func (f *fakeTransfer) Upload(paths []string) error    { f.uploaded = paths; return nil }
func (f *fakeTransfer) Download(paths []string) error  { f.downloaded = paths; return nil }
func (f *fakeTransfer) SetContext(ctx context.Context) {}

func digest(data string) string {
	sum := sha256.Sum256([]byte(data))
	return hex.EncodeToString(sum[:])
}

func setupTest(t *testing.T) (*config.Config, *state.Store, string) {
	t.Helper()
	base := t.TempDir()
	t.Setenv("XDG_STATE_HOME", base)
	cfg := config.New("")
	cfg.Set("repository_id", "repo1", false)
	root := t.TempDir()
	cfg.Set("local_root", root, false)
	cfg.Set("delete_dir_guard_min_files", float64(1000), false)
	info, err := os.Stat(root)
	if err != nil {
		t.Fatal(err)
	}
	marker := map[string]any{
		"version": 1, "local_root": root,
		"st_dev": info.Sys().(*syscall.Stat_t).Dev, "repository_id": "repo1",
	}
	markerPath := filepath.Join(base, "sync-daemon", "repository-marker.json")
	if err := os.MkdirAll(filepath.Dir(markerPath), 0o700); err != nil {
		t.Fatal(err)
	}
	content, _ := json.Marshal(marker)
	if err := os.WriteFile(markerPath, content, 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := state.Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	return cfg, store, root
}

func nasConfig() map[string]string {
	return map[string]string{
		"REPOSITORY_READY": "true", "JOURNAL_READY": "true", "REPOSITORY_ID": "repo1",
	}
}

func TestScanLocalFailsClosedOnUnreadableFile(t *testing.T) {
	cfg, _, root := setupTest(t)
	path := filepath.Join(root, "locked.txt")
	if err := os.WriteFile(path, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o000); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(path, 0o600)
	if _, err := scanLocal(cfg, map[string]state.Identity{}); err == nil {
		t.Fatal("unreadable file must stop the scan")
	}
}

func TestScanLocalFailsClosedOnSymlink(t *testing.T) {
	cfg, _, root := setupTest(t)
	target := filepath.Join(root, "target.txt")
	if err := os.WriteFile(target, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(root, "link.txt")); err != nil {
		t.Fatal(err)
	}
	if _, err := scanLocal(cfg, map[string]state.Identity{}); err == nil {
		t.Fatal("symlink must stop the scan")
	}
}

func TestSyncOnceDeletesMissingFileWithoutTouchingPresentOne(t *testing.T) {
	cfg, store, root := setupTest(t)
	cfg.Set("sync_mode", "push_only", false)
	cfg.Set("delete_enabled", true, false)
	if err := os.WriteFile(filepath.Join(root, "keep.txt"), []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.BackfillIdentities("repo1", root, nil, nil); err != nil {
		t.Fatal(err)
	}
	manifest := fmt.Sprintf("NASBOX_MANIFEST_V1\n"+
		"keep.txt\t%s\t5\t1700000000000000000\tdev\t1700000000\t\n"+
		"gone.txt\t%s\t3\t1700000000000000000\tdev\t1700000000\t\n",
		digest("hello"), digest("abc"))
	nas := &fakeNAS{config: nasConfig(), manifest: manifest}
	transfers := &fakeTransfer{}
	result, err := SyncOnce(cfg, store, nas, transfers)
	if err != nil {
		t.Fatal(err)
	}
	if result.Blocked != "" {
		t.Fatalf("unexpected block: %s", result.Blocked)
	}
	if result.Uploaded != 0 || result.Deleted != 1 {
		t.Fatalf("bad result: %+v", result)
	}
	if len(nas.deleted) != 1 || nas.deleted[0].Path != "gone.txt" {
		t.Fatalf("bad delete request: %+v", nas.deleted)
	}
}

func TestDeleteDisabledNeverDeletes(t *testing.T) {
	cfg, store, _ := setupTest(t)
	cfg.Set("delete_enabled", false, false)
	manifest := fmt.Sprintf("NASBOX_MANIFEST_V1\ngone.txt\t%s\t3\t1700000000000000000\tdev\t1700000000\t\n", digest("abc"))
	nas := &fakeNAS{config: nasConfig(), manifest: manifest}
	transfers := &fakeTransfer{}
	result, err := SyncOnce(cfg, store, nas, transfers)
	if err != nil {
		t.Fatal(err)
	}
	if result.Deleted != 0 || len(nas.deleted) != 0 || len(transfers.downloaded) != 1 {
		t.Fatalf("delete policy ignored: %+v deleted=%v downloads=%v", result, nas.deleted, transfers.downloaded)
	}
}

func TestPullOnlyNeverUploads(t *testing.T) {
	cfg, store, root := setupTest(t)
	cfg.Set("sync_mode", "pull_only", false)
	if err := os.WriteFile(filepath.Join(root, "local.txt"), []byte("local"), 0o600); err != nil {
		t.Fatal(err)
	}
	nas := &fakeNAS{config: nasConfig(), manifest: "NASBOX_MANIFEST_V1\n"}
	transfers := &fakeTransfer{}
	result, err := SyncOnce(cfg, store, nas, transfers)
	if err != nil {
		t.Fatal(err)
	}
	if result.Uploaded != 0 || len(transfers.uploaded) != 0 {
		t.Fatalf("pull_only uploaded: %+v %v", result, transfers.uploaded)
	}
}

func TestPushOnlyNeverDownloads(t *testing.T) {
	cfg, store, _ := setupTest(t)
	cfg.Set("sync_mode", "push_only", false)
	manifest := fmt.Sprintf("NASBOX_MANIFEST_V1\ngone.txt\t%s\t3\t1700000000000000000\tdev\t1700000000\t\n", digest("abc"))
	nas := &fakeNAS{config: nasConfig(), manifest: manifest}
	transfers := &fakeTransfer{}
	result, err := SyncOnce(cfg, store, nas, transfers)
	if err != nil {
		t.Fatal(err)
	}
	if result.Downloads != 0 || len(transfers.downloaded) != 0 {
		t.Fatalf("push_only downloaded: %+v %v", result, transfers.downloaded)
	}
}

func TestSyncOnceBlocksDirectoryAnomalyBeforeAnyMutation(t *testing.T) {
	cfg, store, _ := setupTest(t)
	cfg.Set("sync_mode", "push_only", false)
	cfg.Set("delete_enabled", true, false)
	cfg.Set("delete_dir_guard_min_files", float64(20), false)
	manifest := "NASBOX_MANIFEST_V1\n"
	for i := 0; i < 30; i++ {
		manifest += fmt.Sprintf("OptAlarm/f%d.txt\t%s\t3\t1700000000000000000\tdev\t1700000000\t\n", i, digest("abc"))
	}
	nas := &fakeNAS{config: nasConfig(), manifest: manifest}
	transfers := &fakeTransfer{}
	result, err := SyncOnce(cfg, store, nas, transfers)
	if err != nil {
		t.Fatal(err)
	}
	if result.Blocked == "" || result.Deleted != 0 || len(transfers.uploaded) != 0 {
		t.Fatalf("guard did not block: %+v", result)
	}
}
