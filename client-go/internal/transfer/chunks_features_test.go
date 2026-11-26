package transfer

import (
	"context"
	"path/filepath"
	"testing"

	"nasbox-go/internal/config"
	"nasbox-go/internal/remote"
)

func TestChunksHonorBandwidthSettings(t *testing.T) {
	cfg := config.New("")
	cfg.Set("bandwidth_kbps", 1000, false)
	cfg.Set("bandwidth_upload_kbps", 250, false)
	cfg.Set("bandwidth_download_kbps", 500, false)
	chunks := NewChunks(cfg, remote.New(cfg))
	if got := chunks.bandwidthLimit(true); got != 250*1024 {
		t.Fatalf("upload limit=%d", got)
	}
	if got := chunks.bandwidthLimit(false); got != 500*1024 {
		t.Fatalf("download limit=%d", got)
	}
	cfg.Set("bandwidth_upload_kbps", 0, false)
	cfg.Set("bandwidth_download_kbps", 0, false)
	if got := chunks.bandwidthLimit(true); got != 1000*1024 {
		t.Fatalf("fallback limit=%d", got)
	}
}

func TestDownloadResumeRoundTripAndValidation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "partial.resume")
	info := remote.FileChunks{Digest: "digest", Size: 10, MTimeNS: 20, Chunks: []string{"a", "b"}}
	state := downloadResume{Digest: info.Digest, Size: info.Size, MTimeNS: info.MTimeNS, Completed: 1, Bytes: 4}
	if err := saveDownloadResume(path, state); err != nil {
		t.Fatal(err)
	}
	got, ok := loadDownloadResume(path, info)
	if !ok || got != state {
		t.Fatalf("resume=%+v ok=%v", got, ok)
	}
	info.Digest = "different"
	if _, ok := loadDownloadResume(path, info); ok {
		t.Fatal("resume accettato per un digest diverso")
	}
}

func TestChunkProgressCompletesFullyDeduplicatedUpload(t *testing.T) {
	var last Progress
	progress := newChunkProgress(func(state Progress) { last = state }, true, 1, 100)
	progress.setFile("dedup.txt")
	progress.complete()
	progress.finish(nil)
	if last.Percent != 100 || !last.Finished || last.Done != 1 {
		t.Fatalf("progresso dedup incompleto: %+v", last)
	}
}

func TestAcquireDownloadLockHonorsCancellation(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	local := filepath.Join(t.TempDir(), "file.bin")
	first, err := acquireDownloadLock(context.Background(), local)
	if err != nil {
		t.Fatal(err)
	}
	defer releaseDownloadLock(first)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := acquireDownloadLock(ctx, local); err != context.Canceled {
		t.Fatalf("lock cancellato=%v", err)
	}
}
