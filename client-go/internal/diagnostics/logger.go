// Package diagnostics provides persistent, bounded client diagnostics and
// redacted support bundles. It deliberately shares the Python client's XDG
// state directory and JSON-lines event schema so both clients can coexist.
package diagnostics

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"nasbox-go/internal/filelock"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// MaxLogBytes is the soft size limit for each active diagnostics file.
	// A single oversized record is kept intact rather than split.
	MaxLogBytes int64 = 5 * 1024 * 1024
	// LogBackups is the number of rotated files retained (.1 and .2).
	LogBackups = 2

	logName   = "client.log"
	eventName = "events.jsonl"
)

// ErrClosed is returned when a write is attempted after Close.
var ErrClosed = errors.New("diagnostics logger chiuso")

// Entry is one diagnostic record. Timestamp and Level are derived from the
// action when reading the Python-compatible event format, which has no level
// field of its own.
type Entry struct {
	Timestamp time.Time `json:"timestamp"`
	Level     string    `json:"level"`
	Action    string    `json:"action"`
	Path      string    `json:"path"`
	Detail    string    `json:"detail"`
}

// Logger serialises all reads, writes, rotations and Close calls. Files stay
// open for the logger lifetime; Tail and Recent read through the same mutex so
// they never observe a half-completed rotation.
type Logger struct {
	mu        sync.Mutex
	dir       string
	logPath   string
	eventPath string
	logFile   *os.File
	eventFile *os.File
	maxBytes  int64
	backups   int
	closed    bool
}

// DefaultDir returns the XDG state directory shared with the Python client.
func DefaultDir() (string, error) {
	base := os.Getenv("XDG_STATE_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		base = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(base, "sync-daemon"), nil
}

// Open opens the default diagnostics files, creating them with private modes.
func Open() (*Logger, error) {
	dir, err := DefaultDir()
	if err != nil {
		return nil, err
	}
	return OpenDir(dir)
}

// OpenDir opens diagnostics in an explicit state directory.
func OpenDir(dir string) (*Logger, error) {
	if strings.TrimSpace(dir) == "" {
		return nil, errors.New("directory diagnostica vuota")
	}
	return openLogger(dir, MaxLogBytes, LogBackups)
}

func openLogger(dir string, maxBytes int64, backups int) (*Logger, error) {
	if maxBytes <= 0 {
		maxBytes = MaxLogBytes
	}
	if backups < 0 {
		return nil, errors.New("numero di backup diagnostici non valido")
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(abs, 0o700); err != nil {
		return nil, err
	}
	// Tighten a directory left behind by an older installation when possible.
	_ = os.Chmod(abs, 0o700)

	logger := &Logger{
		dir:       abs,
		logPath:   filepath.Join(abs, logName),
		eventPath: filepath.Join(abs, eventName),
		maxBytes:  maxBytes,
		backups:   backups,
	}
	logger.logFile, err = openPrivateAppend(logger.logPath)
	if err != nil {
		return nil, err
	}
	logger.eventFile, err = openPrivateAppend(logger.eventPath)
	if err != nil {
		_ = logger.logFile.Close()
		return nil, err
	}
	return logger, nil
}

func openPrivateAppend(path string) (*os.File, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return nil, err
	}
	return file, nil
}

// Dir returns the absolute state directory used by this logger.
func (l *Logger) Dir() string { return l.dir }

// Log appends a human-readable record to client.log. The format is
// timestamp<TAB>LEVEL<TAB>action<TAB>JSON-quoted-path<TAB>JSON-quoted-detail.
// Quoting keeps embedded newlines and control characters on one physical line.
func (l *Logger) Log(action, path, detail string) error {
	entry := Entry{
		Timestamp: time.Now(),
		Level:     LevelForAction(action),
		Action:    singleLine(action),
		Path:      path,
		Detail:    detail,
	}
	quotedPath, _ := json.Marshal(entry.Path)
	quotedDetail, _ := json.Marshal(entry.Detail)
	line := fmt.Sprintf("%s\t%s\t%s\t%s\t%s\n",
		entry.Timestamp.Format(time.RFC3339Nano), entry.Level, entry.Action,
		quotedPath, quotedDetail,
	)
	return l.append(l.logPath, &l.logFile, []byte(line))
}

// Event appends one Python-compatible JSON line to events.jsonl. The on-disk
// schema is exactly {ts, action, path, detail}; adding fields would make the
// Python GUI skip the event when the two clients share this file.
func (l *Logger) Event(action, path, detail string) error {
	record := struct {
		TS     float64 `json:"ts"`
		Action string  `json:"action"`
		Path   string  `json:"path"`
		Detail string  `json:"detail"`
	}{
		TS:     float64(time.Now().UnixNano()) / 1e9,
		Action: action,
		Path:   path,
		Detail: detail,
	}
	line, err := json.Marshal(record)
	if err != nil {
		return err
	}
	line = append(line, '\n')
	return l.append(l.eventPath, &l.eventFile, line)
}

func (l *Logger) append(path string, file **os.File, line []byte) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return ErrClosed
	}
	info, err := (*file).Stat()
	if err != nil {
		return err
	}
	if info.Size() > 0 && info.Size()+int64(len(line)) > l.maxBytes {
		if err := l.rotate(path, file); err != nil {
			return err
		}
	}
	if _, err := (*file).Write(line); err != nil {
		return err
	}
	return nil
}

func (l *Logger) rotate(path string, file **os.File) error {
	if err := (*file).Close(); err != nil {
		return err
	}
	// Restore an append handle even when a rename fails, so one diagnostics
	// failure does not make every subsequent write return ErrClosed.
	reopen := func() error {
		opened, err := openPrivateAppend(path)
		if err == nil {
			*file = opened
		}
		return err
	}

	oldest := path + "." + strconv.Itoa(l.backups)
	if l.backups == 0 {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return reopen()
		}
		return reopen()
	}
	if err := os.Remove(oldest); err != nil && !os.IsNotExist(err) {
		return reopen()
	}
	for index := l.backups - 1; index >= 1; index-- {
		from := path + "." + strconv.Itoa(index)
		to := path + "." + strconv.Itoa(index+1)
		if err := os.Rename(from, to); err != nil && !os.IsNotExist(err) {
			return reopen()
		}
	}
	if err := os.Rename(path, path+".1"); err != nil {
		return reopen()
	}
	return reopen()
}

// Tail returns the last limit parsed client.log records, oldest first. It
// includes rotated files. A non-positive limit returns all retained records.
// Malformed lines are ignored so a truncated final record cannot hide the log.
func (l *Logger) Tail(limit int) ([]Entry, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	paths := rotatedPaths(l.logPath, l.backups)
	entries, err := readEntries(paths, parseTextEntry)
	if err != nil {
		return nil, err
	}
	return limitEntries(entries, limit), nil
}

// Recent returns the last limit parsed events, oldest first, including rotated
// files. A non-positive limit returns all retained events. Invalid JSON lines
// are ignored, matching the Python client's tolerant reader.
func (l *Logger) Recent(limit int) ([]Entry, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	paths := rotatedPaths(l.eventPath, l.backups)
	entries, err := readEntries(paths, parseEventEntry)
	if err != nil {
		return nil, err
	}
	return limitEntries(entries, limit), nil
}

func limitEntries(entries []Entry, limit int) []Entry {
	if limit <= 0 || len(entries) <= limit {
		return entries
	}
	return entries[len(entries)-limit:]
}

// Close flushes and closes both files. It is safe to call more than once.
func (l *Logger) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return nil
	}
	l.closed = true
	logErr := l.logFile.Close()
	eventErr := l.eventFile.Close()
	if logErr != nil {
		return logErr
	}
	return eventErr
}

func rotatedPaths(base string, backups int) []string {
	paths := make([]string, 0, backups+1)
	for index := backups; index >= 1; index-- {
		paths = append(paths, base+"."+strconv.Itoa(index))
	}
	return append(paths, base)
}

type entryParser func(string) (Entry, bool)

func readEntries(paths []string, parse entryParser) ([]Entry, error) {
	entries := make([]Entry, 0)
	for _, path := range paths {
		file, err := os.Open(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		reader := bufio.NewReader(file)
		for {
			line, readErr := reader.ReadString('\n')
			line = strings.TrimRight(line, "\r\n")
			if line != "" {
				if entry, ok := parse(line); ok {
					entries = append(entries, entry)
				}
			}
			if readErr != nil {
				if !errors.Is(readErr, io.EOF) {
					_ = file.Close()
					return nil, readErr
				}
				break
			}
		}
		if err := file.Close(); err != nil {
			return nil, err
		}
	}
	return entries, nil
}

func parseTextEntry(line string) (Entry, bool) {
	parts := strings.SplitN(line, "\t", 5)
	if len(parts) != 5 {
		return parseLegacyTextEntry(line)
	}
	timestamp, err := time.Parse(time.RFC3339Nano, parts[0])
	if err != nil {
		return Entry{}, false
	}
	path, pathOK := decodeQuoted(parts[3])
	detail, detailOK := decodeQuoted(parts[4])
	if !pathOK || !detailOK {
		return Entry{}, false
	}
	return Entry{
		Timestamp: timestamp,
		Level:     strings.ToUpper(parts[1]),
		Action:    parts[2],
		Path:      path,
		Detail:    detail,
	}, true
}

func decodeQuoted(value string) (string, bool) {
	if !strings.HasPrefix(value, `"`) {
		return value, true
	}
	var decoded string
	if err := json.Unmarshal([]byte(value), &decoded); err != nil {
		return "", false
	}
	return decoded, true
}

// parseLegacyTextEntry accepts the Python client's historical
// "[YYYY-MM-DD HH:MM:SS] [ACTION] path detail" format.
func parseLegacyTextEntry(line string) (Entry, bool) {
	if !strings.HasPrefix(line, "[") {
		return Entry{}, false
	}
	closeBracket := strings.IndexByte(line, ']')
	if closeBracket < 0 {
		return Entry{}, false
	}
	timestamp, err := time.ParseInLocation("2006-01-02 15:04:05", line[1:closeBracket], time.Local)
	if err != nil {
		return Entry{}, false
	}
	rest := strings.TrimLeft(line[closeBracket+1:], " ")
	if !strings.HasPrefix(rest, "[") {
		return Entry{}, false
	}
	closeAction := strings.IndexByte(rest, ']')
	if closeAction < 0 {
		return Entry{}, false
	}
	action := rest[1:closeAction]
	remaining := strings.TrimLeft(rest[closeAction+1:], " ")
	path, detail := remaining, ""
	if split := strings.IndexByte(remaining, ' '); split >= 0 {
		path, detail = remaining[:split], strings.TrimLeft(remaining[split+1:], " ")
	}
	return Entry{
		Timestamp: timestamp,
		Level:     LevelForAction(action),
		Action:    action,
		Path:      path,
		Detail:    detail,
	}, true
}

func parseEventEntry(line string) (Entry, bool) {
	var record struct {
		TS        float64    `json:"ts"`
		Timestamp *time.Time `json:"timestamp"`
		Action    string     `json:"action"`
		Path      string     `json:"path"`
		Detail    string     `json:"detail"`
	}
	if err := json.Unmarshal([]byte(line), &record); err != nil {
		return Entry{}, false
	}
	if record.Action == "" {
		return Entry{}, false
	}
	timestamp := time.Unix(0, int64(record.TS*1e9))
	if record.Timestamp != nil {
		timestamp = *record.Timestamp
	}
	return Entry{
		Timestamp: timestamp,
		Level:     LevelForAction(record.Action),
		Action:    record.Action,
		Path:      record.Path,
		Detail:    record.Detail,
	}, true
}

// LevelForAction classifies common action names for the human-readable log and
// doctor reports. Callers do not need a separate level argument.
func LevelForAction(action string) string {
	lower := strings.ToLower(action)
	for _, marker := range []string{
		"error", "fail", "failed", "blocked", "denied", "unsafe", "invalid",
		"outdated", "corrupt", "missing",
	} {
		if strings.Contains(lower, marker) {
			return "ERROR"
		}
	}
	for _, marker := range []string{
		"warn", "defer", "deferred", "busy", "conflict", "retry", "timeout",
	} {
		if strings.Contains(lower, marker) {
			return "WARN"
		}
	}
	return "INFO"
}

func singleLine(value string) string {
	return strings.Join(strings.Fields(value), " ")
}

// LocalLockStatus describes the shared client.instance.lock without acquiring
// it for the lifetime of the doctor process.
type LocalLockStatus struct {
	Path  string `json:"path"`
	State string `json:"state"`
	Error string `json:"error,omitempty"`
}

// ProbeLocalLock checks the same advisory lock used by the GUI and Python
// client. A missing lock file is created, just as instance.Acquire would do.
func ProbeLocalLock(dir string) LocalLockStatus {
	status := LocalLockStatus{
		Path:  filepath.Join(dir, "client.instance.lock"),
		State: "unknown",
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		status.Error = err.Error()
		return status
	}
	file, err := os.OpenFile(status.Path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		status.Error = err.Error()
		return status
	}
	defer file.Close()
	if err := filelock.Lock(file, true, true); err != nil {
		if errors.Is(err, filelock.ErrLocked) {
			status.State = "held"
			return status
		}
		status.Error = err.Error()
		return status
	}
	filelock.Unlock(file)
	status.State = "free"
	return status
}
