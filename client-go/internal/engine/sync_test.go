package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"nasbox-go/internal/causal"
	"nasbox-go/internal/config"
	"nasbox-go/internal/remote"
	"nasbox-go/internal/state"
)

type fakeNAS struct {
	config         map[string]string
	manifest       string
	deleted        []remote.CheckedRequest
	renames        [][2]string
	journalAppends int
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
	f.journalAppends++
	return true, nil
}
func (f *fakeNAS) RenameV2(src, dst, _ string, _ string, _ int64, _ int64, _ string) (bool, error) {
	f.renames = append(f.renames, [2]string{src, dst})
	return true, nil
}

type fakeTransfer struct {
	uploaded     []string
	downloaded   []string
	stagedPaths  []string
	stagedFiles  []remote.StagedFile
	stagedDevice string
	stageErr     error
}

func (f *fakeTransfer) Upload(paths []string) error { f.uploaded = paths; return nil }
func (f *fakeTransfer) Download(paths []string) error {
	f.downloaded = append([]string(nil), paths...)
	return nil
}
func (f *fakeTransfer) UploadStaged(paths []string, device string, files []remote.StagedFile) error {
	if f.stageErr != nil {
		return f.stageErr
	}
	f.stagedPaths = append([]string(nil), paths...)
	f.stagedFiles = append([]remote.StagedFile(nil), files...)
	f.stagedDevice = device
	return nil
}
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
	// Under the NAS-authoritative model a deletion is only propagated when
	// the remote file is still the known baseline.
	recordBaseline(t, cfg, store, "gone.txt", digest("abc"), 1700000000000000000, "")
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
	if result.Uploaded != 0 || len(transfers.stagedPaths) != 0 {
		t.Fatalf("pull_only uploaded: %+v %v", result, transfers.stagedPaths)
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
		path := fmt.Sprintf("OptAlarm/f%d.txt", i)
		recordBaseline(t, cfg, store, path, digest("abc"), 1700000000000000000, "")
		manifest += fmt.Sprintf("%s\t%s\t3\t1700000000000000000\tdev\t1700000000\t\n", path, digest("abc"))
	}
	nas := &fakeNAS{config: nasConfig(), manifest: manifest}
	transfers := &fakeTransfer{}
	result, err := SyncOnce(cfg, store, nas, transfers)
	if err != nil {
		t.Fatal(err)
	}
	if result.Blocked == "" || result.Deleted != 0 || len(transfers.stagedPaths) != 0 {
		t.Fatalf("guard did not block: %+v", result)
	}
}

func recordBaseline(
	t *testing.T, cfg *config.Config, store *state.Store,
	path, digestValue string, mtimeNS int64, causalText string,
) {
	t.Helper()
	if err := store.RecordEntry(cfg.LocalRepositoryID(), path, state.EntryState{
		Digest: digestValue, Size: int64(len(digestValue)),
		MTimeNS: mtimeNS, Causal: causalText,
	}); err != nil {
		t.Fatal(err)
	}
}

func manifestLine(path, digestValue string, size, mtimeNS int64, causalText string) string {
	return fmt.Sprintf("%s\t%s\t%d\t%d\tnas\t%d\t%s\n",
		path, digestValue, size, mtimeNS, mtimeNS/1_000_000_000, causalText)
}

// A client that missed the NAS change must converge by downloading. Its own
// unchanged baseline must never be mistaken for a local edit or a deletion.
func TestSyncOnceDownloadsRemoteChangeWithStaleLocalBaseline(t *testing.T) {
	cfg, store, root := setupTest(t)
	recordBaseline(t, cfg, store, "stale.txt", digest("base"), 1700000000000000000, "dev-a:1")
	if err := os.WriteFile(filepath.Join(root, "stale.txt"), []byte("base"), 0o600); err != nil {
		t.Fatal(err)
	}
	manifest := "NASBOX_MANIFEST_V1\n" +
		manifestLine("stale.txt", digest("remote"), 6, 1700000100000000000, "dev-a:2")
	nas := &fakeNAS{config: nasConfig(), manifest: manifest}
	transfers := &fakeTransfer{}
	result, err := SyncOnce(cfg, store, nas, transfers)
	if err != nil {
		t.Fatal(err)
	}
	if result.Blocked != "" || result.Uploaded != 0 || result.Deleted != 0 || result.Downloads != 1 {
		t.Fatalf("stale client made the wrong decision: %+v", result)
	}
	if len(transfers.downloaded) != 1 || transfers.downloaded[0] != "stale.txt" {
		t.Fatalf("bad download: %v", transfers.downloaded)
	}
	if len(nas.deleted) != 0 || len(transfers.stagedPaths) != 0 || nas.journalAppends != 0 {
		t.Fatalf("stale client wrote to the NAS: deletes=%v uploads=%v journals=%d",
			nas.deleted, transfers.stagedPaths, nas.journalAppends)
	}
	entries, err := store.EntrySnapshot(cfg.LocalRepositoryID())
	if err != nil {
		t.Fatal(err)
	}
	if entries["stale.txt"].Digest != digest("remote") || entries["stale.txt"].Causal != "dev-a:2" {
		t.Fatalf("remote baseline not recorded: %+v", entries["stale.txt"])
	}
}

// A causal local edit wins over a concurrent remote edit and is published
// through staging with the clock produced by Observe - not a second increment.
func TestSyncOnceStagesLocalChangeWithObservedCausal(t *testing.T) {
	cfg, store, root := setupTest(t)
	recordBaseline(t, cfg, store, "edit.txt", digest("base"), 1700000000000000000, "dev-a:1")
	localPath := filepath.Join(root, "edit.txt")
	if err := os.WriteFile(localPath, []byte("local"), 0o600); err != nil {
		t.Fatal(err)
	}
	manifest := "NASBOX_MANIFEST_V1\n" +
		manifestLine("edit.txt", digest("remote"), 6, 1700000000000000000, "dev-a:1")
	nas := &fakeNAS{config: nasConfig(), manifest: manifest}
	transfers := &fakeTransfer{}
	result, err := SyncOnce(cfg, store, nas, transfers)
	if err != nil {
		t.Fatal(err)
	}
	if result.Uploaded != 1 || result.Downloads != 0 || result.Deleted != 0 || result.Blocked != "" {
		t.Fatalf("local causal edit was not uploaded: %+v", result)
	}
	if len(transfers.stagedPaths) != 1 || transfers.stagedPaths[0] != "edit.txt" {
		t.Fatalf("upload did not use staging: %v", transfers.stagedPaths)
	}
	if transfers.stagedDevice != store.DeviceID() {
		t.Fatalf("staging used device %q, want %q", transfers.stagedDevice, store.DeviceID())
	}
	if nas.journalAppends != 0 {
		t.Fatalf("direct journal append must not be used after staging: %d", nas.journalAppends)
	}
	if len(transfers.stagedFiles) != 1 {
		t.Fatalf("no staged file metadata: %+v", transfers.stagedFiles)
	}
	staged := transfers.stagedFiles[0]
	if staged.Path != "edit.txt" || staged.Digest != digest("local") || staged.Size != 5 {
		t.Fatalf("bad staged file: %+v", staged)
	}
	version, ok := causal.Parse(staged.Causal)
	if !ok {
		t.Fatalf("staged causal is invalid: %q", staged.Causal)
	}
	base, _ := causal.Parse("dev-a:1")
	if !version.Dominates(base) {
		t.Fatalf("staged causal %q does not dominate baseline", staged.Causal)
	}
	info, err := os.Stat(localPath)
	if err != nil {
		t.Fatal(err)
	}
	observed, err := store.Observe(cfg.LocalRepositoryID(), "edit.txt", store.DeviceID(), state.Observation{
		Digest: staged.Digest, Size: staged.Size, MTimeNS: info.ModTime().UnixNano(), Present: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if observed != staged.Causal {
		t.Fatalf("clock bumped twice: staged %q, observed %q", staged.Causal, observed)
	}
	entries, err := store.EntrySnapshot(cfg.LocalRepositoryID())
	if err != nil {
		t.Fatal(err)
	}
	if entries["edit.txt"].Causal != staged.Causal || entries["edit.txt"].Digest != staged.Digest {
		t.Fatalf("baseline not committed after publish: %+v", entries["edit.txt"])
	}
}

// Remote deletions are accepted only when the remote file is still the
// baseline. A changed remote wins instead.
func TestSyncOnceDeletesRemoteOnlyWhenBaselineUnchanged(t *testing.T) {
	t.Run("unchanged baseline", func(t *testing.T) {
		cfg, store, _ := setupTest(t)
		cfg.Set("delete_enabled", true, false)
		recordBaseline(t, cfg, store, "gone.txt", digest("base"), 1700000000000000000, "dev-a:1")
		nas := &fakeNAS{config: nasConfig(), manifest: "NASBOX_MANIFEST_V1\n" +
			manifestLine("gone.txt", digest("base"), 5, 1700000000000000000, "dev-a:1")}
		transfers := &fakeTransfer{}
		result, err := SyncOnce(cfg, store, nas, transfers)
		if err != nil {
			t.Fatal(err)
		}
		if result.Deleted != 1 || len(nas.deleted) != 1 || nas.deleted[0].Path != "gone.txt" {
			t.Fatalf("baseline deletion refused: %+v %v", result, nas.deleted)
		}
		entries, err := store.EntrySnapshot(cfg.LocalRepositoryID())
		if err != nil {
			t.Fatal(err)
		}
		if entries["gone.txt"].Digest != "" || entries["gone.txt"].Causal == "" {
			t.Fatalf("tombstone not recorded: %+v", entries["gone.txt"])
		}
	})
	t.Run("changed remote wins", func(t *testing.T) {
		cfg, store, _ := setupTest(t)
		cfg.Set("delete_enabled", true, false)
		recordBaseline(t, cfg, store, "gone.txt", digest("base"), 1700000000000000000, "dev-a:1")
		nas := &fakeNAS{config: nasConfig(), manifest: "NASBOX_MANIFEST_V1\n" +
			manifestLine("gone.txt", digest("new"), 3, 1700000100000000000, "dev-a:2")}
		transfers := &fakeTransfer{}
		result, err := SyncOnce(cfg, store, nas, transfers)
		if err != nil {
			t.Fatal(err)
		}
		if result.Deleted != 0 || len(nas.deleted) != 0 || result.Downloads != 1 {
			t.Fatalf("stale local delete removed a changed remote file: %+v %v", result, nas.deleted)
		}
	})
}

// The rename already moved the file to its new local path: it must not also be
// uploaded to that path, which would create a second version and bump the clock.
func TestSyncOnceRenameTargetIsNotUploadedTwice(t *testing.T) {
	cfg, store, root := setupTest(t)
	cfg.Set("delete_enabled", true, false)
	if err := os.WriteFile(filepath.Join(root, "new.txt"), []byte("abc"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.BackfillIdentities(cfg.LocalRepositoryID(), root, nil, nil); err != nil {
		t.Fatal(err)
	}
	recordBaseline(t, cfg, store, "old.txt", digest("abc"), 1700000000000000000, "dev-a:1")
	nas := &fakeNAS{config: nasConfig(), manifest: "NASBOX_MANIFEST_V1\n" +
		manifestLine("old.txt", digest("abc"), 3, 1700000000000000000, "dev-a:1")}
	transfers := &fakeTransfer{}
	result, err := SyncOnce(cfg, store, nas, transfers)
	if err != nil {
		t.Fatal(err)
	}
	if result.Renames != 1 || result.Uploaded != 0 || result.Deleted != 0 {
		t.Fatalf("bad rename result: %+v", result)
	}
	if len(nas.renames) != 1 || nas.renames[0] != [2]string{"old.txt", "new.txt"} {
		t.Fatalf("rename not requested: %v", nas.renames)
	}
	if len(transfers.stagedPaths) != 0 || len(transfers.stagedFiles) != 0 {
		t.Fatalf("rename target was queued for upload: %v", transfers.stagedPaths)
	}
	if len(nas.deleted) != 0 || nas.journalAppends != 0 {
		t.Fatalf("rename must not fall back to delete/upload: %v %d", nas.deleted, nas.journalAppends)
	}
	entries, err := store.EntrySnapshot(cfg.LocalRepositoryID())
	if err != nil {
		t.Fatal(err)
	}
	if entries["old.txt"].Digest != "" || entries["old.txt"].Causal == "" {
		t.Fatalf("rename source tombstone missing: %+v", entries["old.txt"])
	}
	if entries["new.txt"].Digest != digest("abc") || entries["new.txt"].Causal != entries["old.txt"].Causal {
		t.Fatalf("rename target baseline not moved: %+v", entries["new.txt"])
	}
}

// A failed publish must not promote the local observation to a baseline: the
// next pass has to retry with the identical causal.
func TestSyncOnceStagingFailureLeavesBaselineUnchanged(t *testing.T) {
	cfg, store, root := setupTest(t)
	if err := os.WriteFile(filepath.Join(root, "pending.txt"), []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	nas := &fakeNAS{config: nasConfig(), manifest: "NASBOX_MANIFEST_V1\n"}
	transfers := &fakeTransfer{stageErr: errors.New("publish rifiutato")}
	result, err := SyncOnce(cfg, store, nas, transfers)
	if err == nil {
		t.Fatal("failed staging must abort the pass")
	}
	if result.Uploaded != 0 {
		t.Fatalf("failed upload counted as success: %+v", result)
	}
	entries, err := store.EntrySnapshot(cfg.LocalRepositoryID())
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("baseline recorded before publish: %+v", entries)
	}
}
