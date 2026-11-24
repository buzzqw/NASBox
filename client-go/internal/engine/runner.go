package engine

import (
	"context"
	"fmt"
	"math/rand"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"nasbox-go/internal/config"
	"nasbox-go/internal/history"
	"nasbox-go/internal/i18n"
	"nasbox-go/internal/integrity"
	"nasbox-go/internal/mirror"
	"nasbox-go/internal/remote"
	"nasbox-go/internal/state"
	"nasbox-go/internal/version"
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
	nextMirror     time.Time
	auditRunning   atomic.Bool
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
	r.emit("status", i18n.T("log.action.cancelled", nil))
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

// mirrorDue reports whether a local mirror pass is due. The first eligible
// sync pass always runs the mirrors; later ones follow mirror_interval_seconds.
func (r *Runner) mirrorDue() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return !time.Now().Before(r.nextMirror)
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
	mirrorInterval := time.Duration(r.cfg.Int("mirror_interval_seconds")) * time.Second
	if mirrorInterval <= 0 {
		mirrorInterval = 300 * time.Second
	}
	mirrorTicker := time.NewTicker(mirrorInterval)
	defer mirrorTicker.Stop()
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
		case <-mirrorTicker.C:
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
		// Local mirrors run first: the normal push below then picks up the
		// freshly written copies in local_root and propagates them with the
		// usual journal and backup. This uses the same cancellable context as
		// the transfer instead of a remote lease or a sleeping idle loop.
		if r.mirrorDue() {
			r.mu.Lock()
			r.nextMirror = time.Now().Add(mirrorInterval)
			r.mu.Unlock()
			count, mirrorErr := mirror.SyncAll(transferCtx, r.cfg)
			if mirrorErr != nil {
				r.emit("error", "mirror: "+mirrorErr.Error())
			}
			if count > 0 {
				r.emit("status", fmt.Sprintf("mirror: %d cartelle locali aggiornate", count))
			}
		}
		r.emit("status", i18n.T("metrics.sync_busy_title", nil))
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
		r.emit("sync", i18n.T("assurance.activity", map[string]any{
			"uploads": result.Uploaded, "downloads": result.Downloads, "deletes": result.Deleted,
		})+", "+i18n.T("overview.reason_conflicts", map[string]any{"count": result.Conflicts}))
		// A successful sync is the natural point for the rare full audit. It
		// runs in its own goroutine so the UI and the next sync never block.
		r.maybeAudit()
		if time.Since(r.lastPrune) >= 24*time.Hour {
			r.lastPrune = time.Now()
			removed, err := history.Prune(r.cfg.Int("retention_days_local"))
			if err == nil && removed > 0 {
				r.emit("status", i18n.T("history.prune_local_body", map[string]any{"count": removed}))
			}
		}
	}
}

// maybeAudit starts the scheduled integrity audit after a successful sync.
// IsDue owns the cadence (enable flag, check/audit intervals and startup
// delays); the manifest is read fresh so the audit compares what the sync just
// published. The work never runs on the sync/UI goroutine.
func (r *Runner) maybeAudit() {
	if !integrity.IsDue(r.cfg) || !r.auditRunning.CompareAndSwap(false, true) {
		return
	}
	go func() {
		defer r.auditRunning.Store(false)
		raw, err := r.client.ManifestExport("")
		if err != nil {
			r.emit("error", i18n.T("audit.error", map[string]any{"detail": err.Error()}))
			return
		}
		manifest, err := remote.ParseManifest(raw)
		if err != nil {
			r.emit("error", i18n.T("audit.error", map[string]any{"detail": err.Error()}))
			return
		}
		report := integrity.Run(r.cfg, manifest, version.AppVersion, nil)
		when := time.Unix(int64(report.FinishedAt), 0).Format("15:04:05")
		if report.Error != "" {
			r.emit("error", i18n.T("audit.error", map[string]any{"detail": report.Error}))
			return
		}
		if len(report.Differences) > 0 {
			r.emit("status", i18n.T("audit.differences", map[string]any{
				"when": when, "count": len(report.Differences),
			}))
			return
		}
		r.emit("status", i18n.T("audit.ok", map[string]any{
			"when": when, "files": report.FilesChecked, "duration": int(report.Duration),
		}))
	}()
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
