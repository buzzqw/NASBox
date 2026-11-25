package transfer

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestProgressParserReadsRsyncOutput(t *testing.T) {
	var mu sync.Mutex
	var snapshots []Progress
	var files []string
	parser := newProgressParser(true, 3, 0,
		func(p Progress) { mu.Lock(); snapshots = append(snapshots, p); mu.Unlock() },
		func(f FileEvent) { mu.Lock(); files = append(files, f.Path); mu.Unlock() })
	for _, line := range []string{
		"sending incremental file list",
		"Documenti/",
		"Documenti/preventivo.txt",
		"      1,048,576B  33%   10.00MB/s    0:00:02",
		"Foto/mare 2026.jpg",
		"      3,145,728B  66%   12.50MB/s    0:01:05 (xfr#2, to-chk=1/4)",
		"sent 3,200,000 bytes  received 120 bytes",
		"total size is 3,145,728  speedup is 1.00",
	} {
		parser.line(line)
	}
	parser.finish(nil)
	if strings.Join(files, "|") != "Documenti/preventivo.txt|Foto/mare 2026.jpg" {
		t.Fatalf("files: %v", files)
	}
	var mid Progress
	for _, s := range snapshots {
		if s.Percent == 66 {
			mid = s
		}
	}
	if mid.Done != 2 || mid.Total != 3 || mid.Rate != "12.50MB/s" || mid.ETA != "1:05" || mid.Bytes != 3145728 {
		t.Fatalf("mid snapshot: %+v", mid)
	}
	last := snapshots[len(snapshots)-1]
	if !last.Finished || last.Percent != 100 || last.Done != 3 || !last.Upload {
		t.Fatalf("final snapshot: %+v", last)
	}
}

func TestScanLinesSplitsCarriageReturns(t *testing.T) {
	var lines []string
	scanLines(strings.NewReader("a.txt\n   10  1%  1.00kB/s  0:00:01\r   20  2%  1.00kB/s  0:00:01\rb.txt\n"),
		func(line string) { lines = append(lines, line) })
	if len(lines) != 4 || lines[3] != "b.txt" {
		t.Fatalf("lines: %q", lines)
	}
}

func TestObserversEnableStreamingFlags(t *testing.T) {
	cfg := baseConfig()
	tr := New(cfg, nil)
	old := runRsyncStreaming
	var argv []string
	runRsyncStreaming = func(_ context.Context, _ []byte, onLine func(string), args ...string) ([]byte, error) {
		argv = args
		onLine("x.txt")
		return nil, nil
	}
	t.Cleanup(func() { runRsyncStreaming = old })
	var got []string
	tr.SetObservers(func(Progress) {}, func(f FileEvent) { got = append(got, f.Path) })
	if _, err := tr.runObserved(context.Background(), nil, false, 1, []string{"-a", "src", "dst"}); err != nil {
		t.Fatal(err)
	}
	if !hasArg(argv, "--info=progress2,name1") || argv[len(argv)-1] != "dst" {
		t.Fatalf("argv: %v", argv)
	}
	if len(got) != 1 || got[0] != "x.txt" {
		t.Fatalf("file events: %v", got)
	}
}

// Output captured from rsync 3.5.2 (CachyOS client, 2026-11-24).
func TestProgressParserRealRsync352Output(t *testing.T) {
	var last Progress
	var files []string
	parser := newProgressParser(false, 2, 0,
		func(p Progress) { last = p },
		func(f FileEvent) { files = append(files, f.Path) })
	for _, line := range []string{
		"Documenti/", "Documenti/big.bin",
		"         32,768B   0%       0B/s    0:00:00  ",
		"     21,626,880B  72%   20.5MB/s    0:00:00  ",
		"     30,000,000B  99%   21.8MB/s    0:00:01 (xfr#1, to-chk=1/3)",
		"Documenti/nota uno.txt",
		"     30,000,005B 100%   21.8MB/s    0:00:01 (xfr#2, to-chk=0/3)",
	} {
		parser.line(line)
	}
	if last.Percent != 100 || last.Bytes != 30000005 || last.Rate != "21.8MB/s" || last.Done != 2 || last.Total != 2 {
		t.Fatalf("snapshot before finish: %+v", last)
	}
	parser.finish(nil)
	if last.Done != 2 || !last.Finished || len(files) != 2 {
		t.Fatalf("final %+v files %v", last, files)
	}
}

func TestParseRsyncVersion(t *testing.T) {
	for input, want := range map[string]string{
		"rsync  version 3.1.2  protocol version 31\n":           "3.1.2",
		"rsync  version 3.5.2-g41bbd895  protocol version 33\n": "3.5.2",
		"garbage": "",
	} {
		if got := ParseRsyncVersion(input); got != want {
			t.Fatalf("%q -> %q, want %q", input, got, want)
		}
	}
}

// Regression for the 1.1.1 screenshots: the bar said 79% while 53% of the
// files were done and the ETA implied about half of the bytes were left.
// With planned sizes the percentage, the rate and the ETA share one source.
func TestProgressWithPlannedSizesIsConsistentAndMonotonic(t *testing.T) {
	var last Progress
	parser := newProgressParser(false, 4, 4000,
		func(p Progress) { last = p }, nil)
	clock := time.Unix(1000, 0)
	parser.now = func() time.Time { return clock }
	feed := func(line string, advance time.Duration) {
		clock = clock.Add(advance)
		parser.line(line)
	}
	feed("a.bin", 0)
	feed("     500B  79%   1.00MB/s    0:00:01 (xfr#1, to-chk=3/5)", 0) // rsync's own figures are ignored
	if last.Percent != 12 || last.Done != 1 {
		t.Fatalf("percent must come from planned bytes: %+v", last)
	}
	feed("b.bin", time.Second)
	feed("    1000B  10%   1.00MB/s    0:00:09 (xfr#2, to-chk=2/5)", 0)
	if last.Percent != 25 || last.Done != 2 || last.Rate != "500 B/s" || last.ETA != "0:06" {
		t.Fatalf("after two files: %+v", last)
	}
	// A later, lower reading never moves the bar backwards.
	feed("     900B  5%   1.00MB/s    0:00:09 (xfr#2, to-chk=2/5)", time.Second)
	if last.Percent != 25 {
		t.Fatalf("percent went backwards: %+v", last)
	}
	feed("    4000B  100%   1.00MB/s    0:00:00 (xfr#4, to-chk=0/5)", time.Second)
	if last.Percent != 99 || last.ETA != "" || last.Done != 4 {
		t.Fatalf("99 until finish: %+v", last)
	}
	parser.finish(nil)
	if last.Percent != 100 || !last.Finished {
		t.Fatalf("final: %+v", last)
	}
}

func TestTakeSizeHintSumsOnlyKnownPlansAndIsOneShot(t *testing.T) {
	tr := New(baseConfig(), nil)
	tr.HintSizes(map[string]int64{"a": 10, "b": 20})
	if got := tr.takeSizeHint([]byte("a\x00b\x00")); got != 30 {
		t.Fatalf("sum = %d", got)
	}
	if got := tr.takeSizeHint([]byte("a\x00")); got != 0 {
		t.Fatalf("hint must be consumed, got %d", got)
	}
	tr.HintSizes(map[string]int64{"a": 10})
	if got := tr.takeSizeHint([]byte("a\x00zzz\x00")); got != 0 {
		t.Fatalf("an unknown file must disable the planned total, got %d", got)
	}
}

func TestFormatRateAndDuration(t *testing.T) {
	for in, want := range map[float64]string{500: "500 B/s", 1536: "1.5 KB/s", 17301504: "16.5 MB/s"} {
		if got := FormatRate(in); got != want {
			t.Fatalf("FormatRate(%v) = %q, want %q", in, got, want)
		}
	}
	for in, want := range map[time.Duration]string{6 * time.Second: "0:06", 974 * time.Second: "16:14", 3725 * time.Second: "1:02:05"} {
		if got := FormatDuration(in); got != want {
			t.Fatalf("FormatDuration(%v) = %q, want %q", in, got, want)
		}
	}
}

// Real rsync, local copy: with planned sizes the percentage is monotonic, the
// completed-file counter reaches the number of files and the batch ends at 100.
func TestRealRsyncProgressWithPlannedSizes(t *testing.T) {
	if _, err := exec.LookPath("rsync"); err != nil || !localRsyncSupportsProgress() {
		t.Skip("rsync >= 3.1 not available")
	}
	src, dst := t.TempDir(), t.TempDir()
	var names []string
	sizes := map[string]int64{}
	var payload []byte
	for index := 0; index < 30; index++ {
		name := fmt.Sprintf("d/f%02d.bin", index)
		size := int64(2000 + index*9000)
		if index == 29 {
			size = 6 << 20
		}
		full := filepath.Join(src, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, make([]byte, size), 0o644); err != nil {
			t.Fatal(err)
		}
		names = append(names, name)
		sizes[name] = size
		payload = append(payload, []byte(name)...)
		payload = append(payload, 0)
	}
	tr := New(baseConfig(), nil)
	tr.HintSizes(sizes)
	var mu sync.Mutex
	var snapshots []Progress
	tr.SetObservers(func(p Progress) { mu.Lock(); snapshots = append(snapshots, p); mu.Unlock() }, nil)
	args := []string{"-a", "--files-from=-", "--from0", src + "/", dst + "/"}
	if _, err := tr.runObserved(context.Background(), payload, false, len(names), args); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	last := snapshots[len(snapshots)-1]
	if !last.Finished || last.Percent != 100 || last.Done != 30 || last.Total != 30 {
		t.Fatalf("final snapshot: %+v", last)
	}
	previous, previousDone := 0, 0
	for _, snapshot := range snapshots {
		if snapshot.Percent < previous || snapshot.Done < previousDone {
			t.Fatalf("progress went backwards: %+v (after %d%%, %d files)", snapshot, previous, previousDone)
		}
		previous, previousDone = snapshot.Percent, snapshot.Done
		if !snapshot.Finished && snapshot.Percent > 99 {
			t.Fatalf("100%% before the end: %+v", snapshot)
		}
		if snapshot.TotalBytes == 0 {
			t.Fatalf("planned total missing: %+v", snapshot)
		}
	}
}
