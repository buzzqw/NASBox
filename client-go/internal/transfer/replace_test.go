package transfer

import (
	"context"
	"strings"
	"testing"

	"nasbox-go/internal/remote"
)

type fakeReplaceNAS struct {
	calls    []string
	before   map[string]remote.FileState
	after    map[string]remote.FileState
	reads    int
	puts     []remote.JournalPut
	txid     string
	release  int
	manifest map[string]string
}

func (f *fakeReplaceNAS) AcquireLease(string) (func(), error) {
	f.calls = append(f.calls, "lease")
	return func() { f.release++; f.calls = append(f.calls, "release") }, nil
}
func (f *fakeReplaceNAS) FileStatesCurrent([]string) (map[string]remote.FileState, error) {
	f.reads++
	f.calls = append(f.calls, "states")
	if f.reads == 1 {
		return f.before, nil
	}
	return f.after, nil
}
func (f *fakeReplaceNAS) ManifestDigest(path string) (string, bool, error) {
	if digest, ok := f.manifest[path]; ok {
		return digest, true, nil
	}
	return "", false, nil
}
func (f *fakeReplaceNAS) JournalAppendOwned(_, txid, _ string, puts []remote.JournalPut) error {
	f.calls = append(f.calls, "journal")
	f.txid = txid
	f.puts = puts
	return nil
}

func TestUploadReplaceChecksPreconditionKeepsHistoryAndJournals(t *testing.T) {
	cfg := baseConfig()
	tr := New(cfg, remote.New(cfg))
	nas := &fakeReplaceNAS{
		before: map[string]remote.FileState{
			"a.txt": {Kind: "FILE", Digest: "old-a", Size: 5},
			"b.txt": {Kind: "FILE", Digest: "changed-by-other-pc", Size: 9},
		},
		manifest: map[string]string{"b.txt": "changed-by-other-pc"},
		after: map[string]remote.FileState{
			"a.txt": {Kind: "FILE", Digest: "new-a", Size: 7, MTimeNS: 1700000000000000000},
		},
	}
	tr.replaceClient = nas
	args, stdin := captureRsync(t)
	files := []remote.ReplaceFile{
		{StagedFile: remote.StagedFile{Path: "a.txt", Digest: "new-a", Size: 7, MTimeNS: 1700000000123456789, Causal: "pc:2"}, ExpectedDigest: "old-a", ExpectedSize: 5},
		{StagedFile: remote.StagedFile{Path: "b.txt", Digest: "new-b", Size: 3, Causal: "pc:3"}, ExpectedDigest: "old-b", ExpectedSize: 4},
	}
	result, err := tr.UploadReplace(files, "pc", "repo1", "2026-11-24--21-56-55-123456Z")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(result.Stale, ",") != "b.txt" {
		t.Fatalf("changed NAS file must be skipped: %+v", result)
	}
	if string(*stdin) != "a.txt\x00" {
		t.Fatalf("only the verified file may be transferred: %q", *stdin)
	}
	joined := strings.Join(*args, " ")
	for _, want := range []string{"--backup", "--backup-dir=/volume1/NASBox/.sync-trash", "--suffix=-2026-11-24--21-56-55-123456Z"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing %s in %s", want, joined)
		}
	}
	if !strings.HasSuffix(joined, "/home/user/NASBox andres@192.168.1.119:/volume1/NASBox/") {
		t.Fatalf("endpoints must stay last: %s", joined)
	}
	if strings.Join(nas.calls, ",") != "lease,states,states,journal,release" {
		t.Fatalf("operations must run under one lease, in order: %v", nas.calls)
	}
	if len(nas.puts) != 1 || nas.puts[0].Digest != "new-a" || nas.puts[0].Causal != "pc:2" ||
		nas.puts[0].MTimeNS != 1700000000123456789 {
		t.Fatalf("journal must record the NAS bytes with the local causal: %+v", nas.puts)
	}
	if !strings.HasPrefix(nas.txid, "replace-") {
		t.Fatalf("txid: %q", nas.txid)
	}
}

func TestUploadReplaceAllStaleTransfersNothing(t *testing.T) {
	cfg := baseConfig()
	tr := New(cfg, remote.New(cfg))
	nas := &fakeReplaceNAS{before: map[string]remote.FileState{"a.txt": {Kind: "ABSENT"}}}
	tr.replaceClient = nas
	old := runRsync
	runRsync = func(context.Context, []byte, ...string) ([]byte, error) {
		t.Fatal("rsync must not run when every precondition fails")
		return nil, nil
	}
	t.Cleanup(func() { runRsync = old })
	result, err := tr.UploadReplace([]remote.ReplaceFile{{
		StagedFile: remote.StagedFile{Path: "a.txt"}, ExpectedDigest: "x", ExpectedSize: 1,
	}}, "pc", "repo1", "ts")
	if err != nil || len(result.Stale) != 1 || nas.release != 1 {
		t.Fatalf("result %+v err %v release %d", result, err, nas.release)
	}
}

func TestUploadReplaceAdoptsOutOfBandNASEdit(t *testing.T) {
	cfg := baseConfig()
	tr := New(cfg, remote.New(cfg))
	nas := &fakeReplaceNAS{
		before:   map[string]remote.FileState{"a.txt": {Kind: "FILE", Digest: "edited-via-smb", Size: 3, MTimeNS: 5}},
		manifest: map[string]string{"a.txt": "old"},
	}
	tr.replaceClient = nas
	result, err := tr.UploadReplace([]remote.ReplaceFile{{
		StagedFile: remote.StagedFile{Path: "a.txt", Digest: "local"}, ExpectedDigest: "old", ExpectedSize: 3,
	}}, "pc", "repo1", "ts")
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Adopted) != 1 || len(nas.puts) != 1 || nas.puts[0].Digest != "edited-via-smb" || nas.puts[0].Causal != "" {
		t.Fatalf("out-of-band NAS edit must be journaled as found: %+v puts %+v", result, nas.puts)
	}
	if !strings.HasPrefix(nas.txid, "adopt-") {
		t.Fatalf("txid %q", nas.txid)
	}
}
