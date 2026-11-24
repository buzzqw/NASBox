package transfer

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Progress is a snapshot of one rsync batch, built from rsync's own
// --info=progress2,name1 output. It is what the UI shows as "file X of N,
// percent, speed, time left": no extra protocol or NAS work is involved.
type Progress struct {
	Upload  bool
	File    string // file rsync is transferring now (relative path)
	Percent int    // overall percent of the batch, 0..100
	Bytes   int64  // bytes transferred so far in this batch
	Rate    string // e.g. "12.34MB/s"
	ETA     string // e.g. "1:05" (empty when unknown)
	Done    int    // files completed in this batch
	Total   int    // files in this batch
	// Finished marks the final callback of a batch (success or failure).
	Finished bool
	Err      error
}

// FileEvent reports one file transferred by rsync (name1 output).
type FileEvent struct {
	Upload bool
	Path   string
	At     time.Time
}

// ProgressHandler receives progress snapshots; FileHandler receives every
// transferred file. Both may be called from a background goroutine.
type ProgressHandler func(Progress)
type FileHandler func(FileEvent)

// SetObservers installs the UI callbacks. Passing nil disables streaming and
// keeps the plain, silent rsync invocation.
func (t *Transfer) SetObservers(progress ProgressHandler, files FileHandler) {
	t.observerMu.Lock()
	defer t.observerMu.Unlock()
	t.onProgress = progress
	t.onFile = files
}

func (t *Transfer) observers() (ProgressHandler, FileHandler) {
	t.observerMu.Lock()
	defer t.observerMu.Unlock()
	return t.onProgress, t.onFile
}

// progressArgs are client-side output options only: they do not change what
// rsync transfers and need rsync >= 3.1 on the client (the NAS is unaffected).
var progressArgs = []string{"--info=progress2,name1", "--no-inc-recursive", "--outbuf=L"}

// runRsyncStreaming is the streaming spawn point. Tests replace it.
var runRsyncStreaming = func(ctx context.Context, stdin []byte, onLine func(string), args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "rsync", args...)
	// Numbers and rates in a fixed format, whatever the desktop locale.
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("rsync: %w", err)
	}
	scanLines(stdout, onLine)
	if err := cmd.Wait(); err != nil {
		return stderr.Bytes(), fmt.Errorf("rsync: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return nil, nil
}

// scanLines splits rsync output on both \n and \r: progress2 redraws its
// line with carriage returns.
func scanLines(reader io.Reader, onLine func(string)) {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	scanner.Split(func(data []byte, atEOF bool) (int, []byte, error) {
		for index, value := range data {
			if value == '\n' || value == '\r' {
				return index + 1, data[:index], nil
			}
		}
		if atEOF && len(data) > 0 {
			return len(data), data, nil
		}
		return 0, nil, nil
	})
	for scanner.Scan() {
		if line := scanner.Text(); strings.TrimSpace(line) != "" {
			onLine(line)
		}
	}
	// Drain anything left so rsync never blocks on a full pipe.
	_, _ = io.Copy(io.Discard, reader)
}

var progressLine = regexp.MustCompile(
	`^\s*([\d,.]+)B?\s+(\d{1,3})%\s+(\S+/s)\s+(\d+:\d{2}:\d{2})`)

// progressParser turns rsync lines into Progress/FileEvent callbacks.
type progressParser struct {
	mu       sync.Mutex
	state    Progress
	seen     int
	onUpdate ProgressHandler
	onFile   FileHandler
}

func newProgressParser(upload bool, total int, onUpdate ProgressHandler, onFile FileHandler) *progressParser {
	return &progressParser{
		state:    Progress{Upload: upload, Total: total},
		onUpdate: onUpdate,
		onFile:   onFile,
	}
}

func (p *progressParser) line(raw string) {
	p.mu.Lock()
	if match := progressLine.FindStringSubmatch(raw); match != nil {
		p.state.Bytes, _ = strconv.ParseInt(strings.NewReplacer(",", "", ".", "").Replace(match[1]), 10, 64)
		p.state.Percent, _ = strconv.Atoi(match[2])
		p.state.Rate = match[3]
		p.state.ETA = compactETA(match[4])
		// rsync's to-chk counter includes directories, so the file count is
		// derived from the names it prints instead.
		snapshot := p.state
		p.mu.Unlock()
		if p.onUpdate != nil {
			p.onUpdate(snapshot)
		}
		return
	}
	name := strings.TrimSpace(raw)
	if !isTransferredName(raw, name) {
		p.mu.Unlock()
		return
	}
	p.state.File = name
	p.seen++
	p.state.Done = p.seen - 1 // the previous file is complete
	if p.seen > p.state.Total {
		p.state.Total = p.seen
	}
	snapshot := p.state
	p.mu.Unlock()
	if p.onFile != nil {
		p.onFile(FileEvent{Upload: snapshot.Upload, Path: name, At: time.Now()})
	}
	if p.onUpdate != nil {
		p.onUpdate(snapshot)
	}
}

func (p *progressParser) finish(err error) {
	p.mu.Lock()
	p.state.Finished = true
	p.state.Err = err
	if err == nil {
		p.state.Percent = 100
		if p.state.Total > 0 {
			p.state.Done = p.state.Total
		}
		p.state.ETA = ""
	}
	snapshot := p.state
	p.mu.Unlock()
	if p.onUpdate != nil {
		p.onUpdate(snapshot)
	}
}

// isTransferredName keeps only real file names printed by name1: directory
// entries, summaries and rsync's informational messages are skipped.
func isTransferredName(raw, name string) bool {
	if name == "" || strings.HasSuffix(name, "/") || name == "./" || name == "." {
		return false
	}
	if strings.HasPrefix(raw, " ") || strings.HasPrefix(raw, "\t") {
		return false
	}
	for _, prefix := range []string{
		"sending incremental", "receiving incremental", "building file list",
		"sent ", "total size", "created directory", "rsync:", "rsync error",
		"deleting ", "cannot delete", "skipping ", "file has vanished",
	} {
		if strings.HasPrefix(name, prefix) {
			return false
		}
	}
	return true
}

// compactETA turns rsync's H:MM:SS into M:SS when under one hour.
func compactETA(value string) string {
	if strings.HasPrefix(value, "0:") {
		value = strings.TrimPrefix(value, "0:")
		if strings.HasPrefix(value, "0") && len(value) > 4 {
			value = value[1:]
		}
	}
	return value
}

// runObserved runs rsync, streaming progress when observers are installed.
func (t *Transfer) runObserved(ctx context.Context, payload []byte, upload bool, total int, args []string) ([]byte, error) {
	onProgress, onFile := t.observers()
	if onProgress == nil && onFile == nil {
		return runRsync(ctx, payload, args...)
	}
	parser := newProgressParser(upload, total, onProgress, onFile)
	if onProgress != nil {
		onProgress(parser.state)
	}
	full := append(append([]string{}, progressArgs...), args...)
	stderr, err := runRsyncStreaming(ctx, payload, parser.line, full...)
	parser.finish(err)
	return stderr, err
}
