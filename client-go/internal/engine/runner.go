package engine

import (
	"context"
	"fmt"
	"math/rand"
	"strings"
	"sync"
	"time"

	"nasbox-go/internal/config"
	"nasbox-go/internal/history"
	"nasbox-go/internal/state"
	"nasbox-go/internal/watch"
)

type Event struct {
	Kind    string // status | sync | blocked | error
	Message string
}

// Runner is the always-on sync loop: watcher, debounce, backoff and pauses.
// It never mutates the NAS while a guard is active (SyncOnce enforces that).
type Runner struct {
	cfg       *config.Config
	store     *state.Store
	client    ConfigSource
	transfers Transferer

	mu      sync.Mutex
	paused  bool
	dirty   map[string]bool
	backoff time.Duration
	nextTry time.Time

	events         chan Event
	wake           chan struct{}
	transferCtx    context.Context
	cancelTransfer context.CancelFunc
	lastPrune      time.Time
}

func NewRunner(
	cfg *config.Config, store *state.Store, client ConfigSource, transfers Transferer,
) *Runner {
	ctx, cancel := context.WithCancel(context.Background())
	return &Runner{
		cfg: cfg, store: store, client: client, transfers: transfers,
		dirty: map[string]bool{}, events: make(chan Event, 64), wake: make(chan struct{}, 1),
		transferCtx: ctx, cancelTransfer: cancel,
	}
}

func (r *Runner) Events() <-chan Event { return r.events }

func (r *Runner) SetPaused(paused bool) {
	r.mu.Lock()
	r.paused = paused
	r.mu.Unlock()
	r.notify()
}

// Cancel stops the in-flight transfer and lets the current pass fail safely.
func (r *Runner) Cancel() {
	r.mu.Lock()
	if r.cancelTransfer != nil {
		r.cancelTransfer()
	}
	ctx, cancel := context.WithCancel(context.Background())
	r.transferCtx, r.cancelTransfer = ctx, cancel
	r.mu.Unlock()
	r.transfers.SetContext(ctx)
	r.emit("status", "trasferimento annullato")
}

func (r *Runner) IsPaused() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.paused
}

func (r *Runner) SyncNow() {
	r.mu.Lock()
	r.dirty[""] = true
	r.mu.Unlock()
	r.notify()
}

func (r *Runner) notify() {
	select {
	case r.wake <- struct{}{}:
	default:
	}
}

func (r *Runner) markDirty(path string) {
	r.mu.Lock()
	r.dirty[path] = true
	r.mu.Unlock()
	r.notify()
}

func (r *Runner) takeDirty() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	paths := make([]string, 0, len(r.dirty))
	for path := range r.dirty {
		paths = append(paths, path)
	}
	r.dirty = map[string]bool{}
	return paths
}

func (r *Runner) emit(kind, message string) {
	select {
	case r.events <- Event{Kind: kind, Message: message}:
	default:
	}
}

func (r *Runner) excluded(relative string) bool {
	for _, pattern := range r.cfg.Get("exclude_patterns").([]any) {
		text, ok := pattern.(string)
		if !ok {
			continue
		}
		text = strings.TrimSuffix(text, "/")
		if text != "" && (relative == text || strings.HasPrefix(relative, text+"/")) {
			return true
		}
	}
	return false
}

// Run blocks until ctx is cancelled. Filesystem events and the periodic timer
// wake the same singleflight-ish loop; only one sync runs at a time.
func (r *Runner) Run(ctx context.Context) error {
	debounce := time.Duration(r.cfg.Int("debounce_seconds")) * time.Second
	if debounce <= 0 {
		debounce = 2 * time.Second
	}
	interval := time.Duration(r.cfg.Int("poll_interval")) * time.Second
	if interval <= 0 {
		interval = 300 * time.Second
	}
	watcher := watch.New(r.cfg.LocalRoot(), debounce, r.excluded)
	watchCtx, stopWatch := context.WithCancel(ctx)
	defer stopWatch()
	go watcher.Run(watchCtx)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case path := <-watcher.Events():
			r.markDirty(path)
		case <-ticker.C:
			r.markDirty("")
		case <-r.wake:
		case <-ctx.Done():
			return nil
		}
		r.mu.Lock()
		paused, next := r.paused, r.nextTry
		pending := len(r.dirty) > 0
		r.mu.Unlock()
		if paused || scheduledPause(r.cfg) || !pending || time.Now().Before(next) {
			continue
		}
		paths := r.takeDirty()
		_ = paths // SyncOnce reconciles the whole tree; paths only drive the wake-up.
		r.mu.Lock()
		transferCtx := r.transferCtx
		r.mu.Unlock()
		r.transfers.SetContext(transferCtx)
		r.emit("status", "sincronizzazione in corso…")
		result, err := SyncOnce(r.cfg, r.store, r.client, r.transfers)
		if err != nil {
			r.handleFailure(err)
			continue
		}
		if result.Blocked != "" {
			r.emit("blocked", result.Blocked)
			r.mu.Lock()
			r.backoff = 5 * time.Minute
			r.nextTry = time.Now().Add(r.backoff)
			r.mu.Unlock()
			continue
		}
		r.mu.Lock()
		r.backoff = 0
		r.nextTry = time.Time{}
		r.mu.Unlock()
		r.emit("sync", fmt.Sprintf(
			" caricati %d, scaricati %d, cancellati %d, conflitti %d",
			result.Uploaded, result.Downloads, result.Deleted, result.Conflicts,
		))
		if time.Since(r.lastPrune) >= 24*time.Hour {
			r.lastPrune = time.Now()
			removed, err := history.Prune(r.cfg.Int("retention_days_local"))
			if err == nil && removed > 0 {
				r.emit("status", fmt.Sprintf("rimossi %d file dallo storico locale", removed))
			}
		}
	}
}

func scheduledPause(cfg *config.Config) bool {
	if !cfg.Bool("pause_schedule_enabled") {
		return false
	}
	values, ok := cfg.Get("pause_schedule").([]any)
	if !ok {
		return false
	}
	now := time.Now()
	current := now.Hour()*60 + now.Minute()
	for _, raw := range values {
		entry, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		start, end := fmt.Sprint(entry["start"]), fmt.Sprint(entry["end"])
		startMinutes, err1 := parseHHMM(start)
		endMinutes, err2 := parseHHMM(end)
		if err1 != nil || err2 != nil {
			continue
		}
		if endMinutes >= startMinutes {
			if current >= startMinutes && current < endMinutes {
				return true
			}
		} else if current >= startMinutes || current < endMinutes {
			return true
		}
	}
	return false
}

func parseHHMM(value string) (int, error) {
	var hour, minute int
	if _, err := fmt.Sscanf(value, "%d:%d", &hour, &minute); err != nil {
		return 0, err
	}
	return hour*60 + minute, nil
}

func (r *Runner) handleFailure(err error) {
	message := err.Error()
	busy := strings.Contains(strings.ToLower(message), "lock") ||
		strings.Contains(strings.ToLower(message), "occupato")
	r.mu.Lock()
	if busy {
		if r.backoff <= 0 {
			r.backoff = 2 * time.Second
		} else if r.backoff < 5*time.Minute {
			r.backoff *= 2
		}
		jitter := time.Duration(rand.Int63n(int64(r.backoff/2) + 1))
		r.nextTry = time.Now().Add(r.backoff + jitter)
	} else {
		r.nextTry = time.Now().Add(30 * time.Second)
	}
	r.mu.Unlock()
	r.emit("error", message)
}
