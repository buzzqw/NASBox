package serverd

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Access log for the web UI and API. One line per request, tab separated, with
// size-based rotation: access.log, access.log.1 ... access.log.<keep>.
// Tokens and query strings are never written.
const (
	defaultAccessLogMaxBytes = 5 * 1024 * 1024
	defaultAccessLogKeep     = 3
)

type accessLogger struct {
	mu   sync.Mutex
	path string
	file *os.File
	size int64
	max  int64
	keep int
}

var accessLoggers sync.Map // path -> *accessLogger

func (c *Context) accessLog() *accessLogger {
	path := filepath.Join(c.StateDir, "access.log")
	if existing, ok := accessLoggers.Load(path); ok {
		return existing.(*accessLogger)
	}
	logger := &accessLogger{
		path: path,
		max:  parseConfigInt(c.Config["ACCESS_LOG_MAX_BYTES"], defaultAccessLogMaxBytes),
		keep: int(parseConfigInt(c.Config["ACCESS_LOG_KEEP"], defaultAccessLogKeep)),
	}
	if logger.keep < 1 {
		logger.keep = 1
	}
	actual, _ := accessLoggers.LoadOrStore(path, logger)
	return actual.(*accessLogger)
}

// write appends one line, rotating first when the current file is full.
func (l *accessLogger) write(line string) {
	if l == nil || l.max <= 0 {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file == nil {
		file, err := os.OpenFile(l.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			return
		}
		info, err := file.Stat()
		if err == nil {
			l.size = info.Size()
		}
		l.file = file
	}
	if l.size > 0 && l.size+int64(len(line)) > l.max {
		l.rotateLocked()
	}
	if l.file == nil {
		return
	}
	n, err := l.file.WriteString(line)
	if err != nil {
		return
	}
	l.size += int64(n)
}

func (l *accessLogger) rotateLocked() {
	if l.file != nil {
		_ = l.file.Close()
		l.file = nil
	}
	// Shift generations: access.log.(N-1) -> access.log.N, ..., then the
	// current file becomes .1. Rename overwrites the oldest generation.
	for index := l.keep; index >= 2; index-- {
		_ = os.Rename(l.path+"."+strconv.Itoa(index-1), l.path+"."+strconv.Itoa(index))
	}
	_ = os.Rename(l.path, l.path+".1")
	file, err := os.OpenFile(l.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		l.size = 0
		return
	}
	l.file = file
	l.size = 0
}

// Close lets tests and a graceful shutdown release the file handle.
func (l *accessLogger) Close() {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file != nil {
		_ = l.file.Close()
		l.file = nil
	}
}

// accessRecorder keeps the status and the byte count without breaking
// sendfile: implementing ReadFrom preserves the fast path used by ServeFile.
type accessRecorder struct {
	http.ResponseWriter
	status int
	bytes  int64
}

func (r *accessRecorder) WriteHeader(code int) {
	if r.status == 0 {
		r.status = code
	}
	r.ResponseWriter.WriteHeader(code)
}

func (r *accessRecorder) Write(chunk []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	written, err := r.ResponseWriter.Write(chunk)
	r.bytes += int64(written)
	return written, err
}

func (r *accessRecorder) ReadFrom(source io.Reader) (int64, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	if readerFrom, ok := r.ResponseWriter.(io.ReaderFrom); ok {
		written, err := readerFrom.ReadFrom(source)
		r.bytes += written
		return written, err
	}
	return io.Copy(struct{ io.Writer }{r.ResponseWriter}, source)
}

func (r *accessRecorder) Flush() {
	if flusher, ok := r.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

// Unwrap keeps http.ResponseController working through the wrapper.
func (r *accessRecorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

func sanitizeAccessPath(path string) string {
	if index := strings.IndexByte(path, '?'); index >= 0 {
		path = path[:index]
	}
	if strings.HasPrefix(path, "/s/") {
		return "/s/[token]"
	}
	if len(path) > 512 {
		path = path[:512]
	}
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, path)
}

func (c *Context) withAccessLog(next http.Handler) http.Handler {
	logger := c.accessLog()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		recorder := &accessRecorder{ResponseWriter: w}
		next.ServeHTTP(recorder, r)
		status := recorder.status
		if status == 0 {
			status = http.StatusOK
		}
		user := "-"
		if name, _, ok := r.BasicAuth(); ok && name != "" {
			user = name
		}
		logger.write(fmt.Sprintf("%s\t%s\t%s\t%s\t%d\t%d\t%dms\t%s\n",
			start.UTC().Format(time.RFC3339),
			clientIP(r),
			r.Method,
			sanitizeAccessPath(r.URL.Path),
			status,
			recorder.bytes,
			time.Since(start).Milliseconds(),
			user,
		))
	})
}
