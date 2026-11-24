package transfer

import (
	"context"
	"strings"
	"sync"
	"testing"
)

func TestProgressParserReadsRsyncOutput(t *testing.T) {
	var mu sync.Mutex
	var snapshots []Progress
	var files []string
	parser := newProgressParser(true, 3,
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
	if mid.Done != 1 || mid.Total != 3 || mid.Rate != "12.50MB/s" || mid.ETA != "1:05" || mid.Bytes != 3145728 {
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
	parser := newProgressParser(false, 2,
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
	if last.Percent != 100 || last.Bytes != 30000005 || last.Rate != "21.8MB/s" || last.Done != 1 || last.Total != 2 {
		t.Fatalf("snapshot before finish: %+v", last)
	}
	parser.finish(nil)
	if last.Done != 2 || !last.Finished || len(files) != 2 {
		t.Fatalf("final %+v files %v", last, files)
	}
}
