package engine

import (
	"context"
	"fmt"
	"math/rand"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"nasbox-go/internal/classify"
	"nasbox-go/internal/config"
	"nasbox-go/internal/conflicts"
	"nasbox-go/internal/diagnostics"
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
	lastFullSync   time.Time
	nextMirror     time.Time
	auditRunning   atomic.Bool

	manifestMu       sync.Mutex
	manifest         map[string]classify.RemoteState
	manifestRevision int
	storm            *conflicts.StormTracker
	logger           *diagnostics.Logger
}

func NewRunner(
	cfg *config.Config, store *state.Store, client ConfigSource, transfers Transferer,
	loggers ...*diagnostics.Logger,
) *Runner {
	ctx, cancel := context.WithCancel(context.Background())
	runner := &Runner{
		cfg: cfg, store: store, client: client, transfers: transfers,
		dirty: map[string]bool{}, events: make(chan Event, 64), wake: make(chan struct{}, 1),
		transferCtx: ctx, cancelTransfer: cancel,
		manifestRevision: -1,
		storm: conflicts.NewStormTracker(conflicts.StormConfig{
			ConflictStormLimit:         cfg.Int("conflict_storm_limit"),
			ConflictStormWindowSeconds: cfg.Int("conflict_storm_window_seconds"),
		}),
	}
	if len(loggers) > 0 {
		runner.logger = loggers[0]
	}
	return runner
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
	if r.logger != nil {
		action := strings.ToUpper(kind)
		if kind == "status" {
			action = "INFO"
		}
		r.logger.Log(action, "-", message)
	}
}

func (r *Runner) excluded(relative string) bool {
	return excluded(r.cfg, relative)
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
	fullPullInterval := time.Duration(r.cfg.Int("full_pull_interval_seconds")) * time.Second
	if fullPullInterval <= 0 {
		fullPullInterval = 2 * time.Hour
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
	// Converge immediately after a login or restart instead of waiting for the
	// first poll tick, which can be several minutes away.
	r.SyncNow()
	for {
		select {
		case path := <-watcher.Events():
			r.markDirty(path)
		case <-ticker.C:
			r.mu.Lock()
			fullDue := r.lastFullSync.IsZero() || time.Since(r.lastFullSync) >= fullPullInterval
			r.mu.Unlock()
			if fullDue {
				r.markDirty("")
			}
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
		fullPass := false
		for _, path := range paths {
			if path == "" {
				fullPass = true
				break
			}
		}
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
		manifest, err := r.loadManifest(fullPass)
		if err != nil {
			r.handleFailure(err)
			continue
		}
		result, err := SyncOnceWithManifest(r.cfg, r.store, r.client, r.transfers, manifest, r.storm)
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
		if fullPass {
			r.lastFullSync = time.Now()
		}
		r.mu.Unlock()
		summary := i18n.T("assurance.activity", map[string]any{
			"uploads": result.Uploaded, "downloads": result.Downloads, "deletes": result.Deleted,
		}) + ", " + i18n.T("overview.reason_conflicts", map[string]any{"count": result.Conflicts})
		r.emit("sync", summary)
		if r.cfg.Bool("notify_sync_completion") &&
			(result.Uploaded+result.Downloads+result.Deleted > 0) {
			r.emit("notify", summary)
		}
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

// loadManifest keeps a client-side snapshot and applies server deltas. A full
// export is only needed on first run, after a long gap, or when the server
// explicitly asks for fallback.
func (r *Runner) loadManifest(full bool) (map[string]classify.RemoteState, error) {
	r.manifestMu.Lock()
	defer r.manifestMu.Unlock()
	if full || r.manifest == nil || r.manifestRevision < 0 {
		raw, err := r.client.ManifestExport("")
		if err != nil {
			return nil, err
		}
		manifest, err := remote.ParseManifest(raw)
		if err != nil {
			return nil, err
		}
		revision := 0
		if status, err := r.client.JournalStatus(); err == nil {
			revision, _ = strconv.Atoi(status["MANIFEST_REVISION"])
		}
		r.manifest = manifest
		r.manifestRevision = revision
		return manifest, nil
	}
	raw, err := r.client.ManifestExport(strconv.Itoa(r.manifestRevision))
	if err != nil {
		return nil, err
	}
	delta, err := remote.ParseManifestDelta(raw)
	if err != nil {
		return nil, err
	}
	if delta.FallbackFull {
		raw, err = r.client.ManifestExport("")
		if err != nil {
			return nil, err
		}
		manifest, err := remote.ParseManifest(raw)
		if err != nil {
			return nil, err
		}
		r.manifest = manifest
		r.manifestRevision = int(delta.CurrentRevision)
		return manifest, nil
	}
	r.manifest = remote.MergeDelta(r.manifest, delta)
	r.manifestRevision = int(delta.CurrentRevision)
	return r.manifest, nil
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
