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
	Percent int    // overall percent of the batch, 0..100 (never goes backwards)
	Bytes   int64  // bytes transferred so far in this batch
	Rate    string // e.g. "12.34MB/s"
	ETA     string // e.g. "1:05" (empty when unknown)
	Done    int    // files completed in this batch
	Total   int    // files in this batch
	// TotalBytes is the planned size of the batch (0 when the caller gave no
	// size hints). With it the percentage and the ETA are computed from the
	// same quantity, so they can never contradict each other.
	TotalBytes int64
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

// progressLine matches rsync's progress2 line, e.g.
//
//	14,481,160  4%  62.8MB/s  0:00:00 (xfr#19, to-chk=22/42)
//
// The optional tail carries xfr#N: the number of files rsync has actually
// completed, which is the only reliable file counter (to-chk includes
// directories and, with an incremental file list, grows while it runs).
var progressLine = regexp.MustCompile(
	`^\s*([\d,.]+)B?\s+(\d{1,3})%\s+(\S+/s)\s+(\d+:\d{2}:\d{2})(?:\s+\(xfr#(\d+),)?`)

// progressParser turns rsync lines into Progress/FileEvent callbacks.
type progressParser struct {
	mu       sync.Mutex
	state    Progress
	seen     int
	haveXfr  bool
	onUpdate ProgressHandler
	onFile   FileHandler

	now        func() time.Time
	rateAt     time.Time
	rateBytes  int64
	rateSmooth float64 // bytes per second, exponentially smoothed
}

func newProgressParser(
	upload bool, total int, totalBytes int64, onUpdate ProgressHandler, onFile FileHandler,
) *progressParser {
	return &progressParser{
		state:    Progress{Upload: upload, Total: total, TotalBytes: totalBytes},
		onUpdate: onUpdate,
		onFile:   onFile,
		now:      time.Now,
	}
}

// observeRate smooths the byte rate over windows of at least one second, so a
// burst of tiny files or a stalled one does not make the ETA jump around.
func (p *progressParser) observeRate() {
	now := p.now()
	if p.rateAt.IsZero() {
		p.rateAt, p.rateBytes = now, p.state.Bytes
		return
	}
	elapsed := now.Sub(p.rateAt).Seconds()
	if elapsed < 1 {
		return
	}
	instant := float64(p.state.Bytes-p.rateBytes) / elapsed
	if instant < 0 {
		instant = 0
	}
	if p.rateSmooth == 0 {
		p.rateSmooth = instant
	} else {
		p.rateSmooth = 0.7*p.rateSmooth + 0.3*instant
	}
	p.rateAt, p.rateBytes = now, p.state.Bytes
}

// applyBytes derives percent, rate and ETA. With a planned size they all come
// from the same byte counters; without one rsync's own figures are kept.
func (p *progressParser) applyBytes(rsyncPercent int, rsyncRate, rsyncETA string) {
	p.observeRate()
	if p.state.TotalBytes <= 0 {
		p.state.Percent = maxIntValue(p.state.Percent, rsyncPercent)
		p.state.Rate, p.state.ETA = rsyncRate, rsyncETA
		return
	}
	percent := int(p.state.Bytes * 100 / p.state.TotalBytes)
	if percent > 99 {
		percent = 99 // 100 is reserved for the end of the batch
	}
	p.state.Percent = maxIntValue(p.state.Percent, percent)
	if p.rateSmooth > 0 {
		p.state.Rate = FormatRate(p.rateSmooth)
		remaining := p.state.TotalBytes - p.state.Bytes
		if remaining > 0 {
			p.state.ETA = FormatDuration(time.Duration(float64(remaining) / p.rateSmooth * float64(time.Second)))
		} else {
			p.state.ETA = ""
		}
	}
}

func maxIntValue(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// FormatRate renders bytes per second as "16.5 MB/s".
func FormatRate(bytesPerSecond float64) string {
	units := []string{"B/s", "KB/s", "MB/s", "GB/s"}
	value := bytesPerSecond
	unit := 0
	for value >= 1000 && unit < len(units)-1 {
		value /= 1024
		unit++
	}
	if unit == 0 {
		return fmt.Sprintf("%.0f %s", value, units[unit])
	}
	return fmt.Sprintf("%.1f %s", value, units[unit])
}

// FormatDuration renders a remaining time as "M:SS" or "H:MM:SS".
func FormatDuration(d time.Duration) string {
	seconds := int(d.Round(time.Second).Seconds())
	if seconds < 0 {
		seconds = 0
	}
	if seconds >= 3600 {
		return fmt.Sprintf("%d:%02d:%02d", seconds/3600, seconds%3600/60, seconds%60)
	}
	return fmt.Sprintf("%d:%02d", seconds/60, seconds%60)
}

func (p *progressParser) line(raw string) {
	p.mu.Lock()
	if match := progressLine.FindStringSubmatch(raw); match != nil {
		p.state.Bytes, _ = strconv.ParseInt(strings.NewReplacer(",", "", ".", "").Replace(match[1]), 10, 64)
		rsyncPercent, _ := strconv.Atoi(match[2])
		// xfr#N is the count of files rsync has finished. It replaces the
		// count derived from the printed names, which runs one file ahead.
		if match[5] != "" {
			if done, err := strconv.Atoi(match[5]); err == nil {
				p.haveXfr = true
				p.state.Done = maxIntValue(p.state.Done, done)
				if p.state.Done > p.state.Total {
					p.state.Total = p.state.Done
				}
			}
		}
		p.applyBytes(rsyncPercent, match[3], compactETA(match[4]))
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
	if !p.haveXfr {
		p.state.Done = p.seen - 1 // the previous file is complete
	}
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
	if (onProgress == nil && onFile == nil) || !localRsyncSupportsProgress() {
		return runRsync(ctx, payload, args...)
	}
	parser := newProgressParser(upload, total, t.takeSizeHint(payload), onProgress, onFile)
	if onProgress != nil {
		onProgress(parser.state)
	}
	full := append(append([]string{}, progressArgs...), args...)
	stderr, err := runRsyncStreaming(ctx, payload, parser.line, full...)
	parser.finish(err)
	return stderr, err
}

var (
	progressOnce      sync.Once
	progressSupported = true
)

// LocalRsyncVersion returns the client's rsync version ("" when unknown).
func LocalRsyncVersion() string {
	output, err := exec.Command("rsync", "--version").Output()
	if err != nil {
		return ""
	}
	return ParseRsyncVersion(string(output))
}

// ParseRsyncVersion extracts "3.1.2" from "rsync  version 3.1.2  protocol...".
func ParseRsyncVersion(output string) string {
	fields := strings.Fields(output)
	for index, field := range fields {
		if field == "version" && index+1 < len(fields) {
			value := fields[index+1]
			if dash := strings.IndexAny(value, "-+"); dash > 0 {
				value = value[:dash]
			}
			return value
		}
	}
	return ""
}

// localRsyncSupportsProgress: --info=progress2 needs rsync >= 3.1 on the
// client. Older clients keep the silent transfer instead of failing.
func localRsyncSupportsProgress() bool {
	progressOnce.Do(func() {
		version := LocalRsyncVersion()
		if version == "" {
			return
		}
		parts := strings.Split(version, ".")
		major, _ := strconv.Atoi(parts[0])
		minor := 0
		if len(parts) > 1 {
			minor, _ = strconv.Atoi(parts[1])
		}
		progressSupported = major > 3 || (major == 3 && minor >= 1)
	})
	return progressSupported
}

// HintSizes tells the next transfers how big each planned file is, so the
// progress bar and the ETA are computed from planned bytes instead of
// rsync's estimate (which is only reliable once the whole file list is
// known). The hint is optional and is dropped by the next batch that uses it.
func (t *Transfer) HintSizes(sizes map[string]int64) {
	t.hintMu.Lock()
	defer t.hintMu.Unlock()
	t.sizeHints = sizes
}

// takeSizeHint sums the hinted sizes of the files in a --files-from payload
// (NUL separated) and clears the hint. It returns 0 when any file is unknown:
// a partial sum would make the percentage lie.
func (t *Transfer) takeSizeHint(payload []byte) int64 {
	t.hintMu.Lock()
	hints := t.sizeHints
	t.sizeHints = nil
	t.hintMu.Unlock()
	if len(hints) == 0 || len(payload) == 0 {
		return 0
	}
	var total int64
	for _, name := range bytes.Split(payload, []byte{0}) {
		if len(name) == 0 {
			continue
		}
		size, ok := hints[string(name)]
		if !ok {
			return 0
		}
		total += size
	}
	return total
}
