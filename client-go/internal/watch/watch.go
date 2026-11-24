// Package watch converts filesystem events into a small stream of dirty paths.
// It uses inotify via fsnotify and falls back to a bounded periodic scan.
package watch

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/fsnotify/fsnotify"
)

// Watcher emits relative paths (or "" for "rescan everything").
type Watcher struct {
	root     string
	events   chan string
	debounce time.Duration
	excluded func(string) bool
	degraded atomic.Bool
}

// Reliable reports whether every folder is watched by inotify. When false
// (no inotify, a folder that could not be added, a queue overflow) callers
// must not trust incremental scans and fall back to complete walks.
func (w *Watcher) Reliable() bool { return !w.degraded.Load() }

// fallbackInterval is the rescan cadence without inotify: a complete walk
// every debounce (2 s) on 100k+ files kept the CPU and disk busy forever.
const fallbackInterval = 60 * time.Second

func New(root string, debounce time.Duration, excluded func(string) bool) *Watcher {
	if debounce <= 0 {
		debounce = 2 * time.Second
	}
	return &Watcher{root: root, events: make(chan string, 256), debounce: debounce, excluded: excluded}
}

func (w *Watcher) Events() <-chan string { return w.events }

// Run blocks until ctx is cancelled. It never closes the events channel; the
// caller owns its lifetime.
func (w *Watcher) Run(ctx context.Context) {
	fsw, err := fsnotify.NewWatcher()
	pending := map[string]time.Time{}
	flush := func() {
		if len(pending) == 0 {
			return
		}
		for path := range pending {
			select {
			case w.events <- path:
			case <-ctx.Done():
				return
			}
		}
		pending = map[string]time.Time{}
	}
	ticker := time.NewTicker(w.debounce)
	defer ticker.Stop()
	fallback := time.NewTicker(fallbackInterval)
	defer fallback.Stop()
	if err != nil {
		// No inotify: a coarse scan sentinel is enough to keep sync alive.
		w.degraded.Store(true)
		for {
			select {
			case <-fallback.C:
				select {
				case w.events <- "":
				case <-ctx.Done():
					return
				}
			case <-ctx.Done():
				return
			}
		}
	}
	defer fsw.Close()
	if err := fsw.Add(w.root); err != nil {
		w.degraded.Store(true)
		for {
			select {
			case <-fallback.C:
				select {
				case w.events <- "":
				case <-ctx.Done():
					return
				}
			case <-ctx.Done():
				return
			}
		}
	}
	addDirectories := func() {
		_ = filepath.WalkDir(w.root, func(path string, entry os.DirEntry, err error) error {
			if err != nil || !entry.IsDir() {
				return nil
			}
			rel, _ := filepath.Rel(w.root, path)
			if rel != "." && w.excluded != nil && w.excluded(filepath.ToSlash(rel)) {
				return filepath.SkipDir
			}
			if err := fsw.Add(path); err != nil {
				// Typically fs.inotify.max_user_watches reached.
				w.degraded.Store(true)
			}
			return nil
		})
	}
	addDirectories()
	for {
		select {
		case event, ok := <-fsw.Events:
			if !ok {
				continue
			}
			rel, err := filepath.Rel(w.root, event.Name)
			if err != nil {
				continue
			}
			rel = filepath.ToSlash(rel)
			if rel == "." || (w.excluded != nil && w.excluded(rel)) {
				continue
			}
			if event.Op&fsnotify.Create != 0 {
				if info, err := os.Stat(event.Name); err == nil && info.IsDir() {
					addDirectories()
				}
			}
			if strings.HasPrefix(rel, ".sync-trash/") {
				continue
			}
			pending[rel] = time.Now()
		case err, ok := <-fsw.Errors:
			if ok && err != nil {
				pending[""] = time.Now() // overflow / watch lost: full rescan
			}
		case <-ticker.C:
			flush()
		case <-ctx.Done():
			return
		}
	}
}
