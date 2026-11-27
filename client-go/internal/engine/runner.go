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
	"nasbox-go/internal/schedule"
	"nasbox-go/internal/state"
	"nasbox-go/internal/version"
	"nasbox-go/internal/watch"
)

// SyncCounts are the transfers of one pass, carried with notify events so the
// UI can group several completions into a single desktop notification.
type SyncCounts struct {
	Uploads   int
	Downloads int
	Deletes   int
	Conflicts int
}

type Event struct {
	Kind    string // status | sync | blocked | error | notify
	Message string
	Counts  SyncCounts // set on notify events, zero otherwise
}

// Runner is the always-on sync loop: watcher, debounce, backoff and pauses.
// It never mutates the NAS while a guard is active (SyncOnce enforces that).
type Runner struct {
	cfg       *config.Config
	store     *state.Store
	client    ConfigSource
	transfers Transferer

	mu            sync.Mutex
	paused        bool
	dirty         map[string]bool
	remotePending bool
	mirrorTick    bool // the mirror timer fired; cleared once the mirrors ran
	backoff       time.Duration
	nextTry       time.Time

	events         chan Event
	wake           chan struct{}
	transferCtx    context.Context
	cancelTransfer context.CancelFunc
	lastPrune      time.Time
	lastFullSync   time.Time
	localSnapshot  *LocalSnapshot
	watchReliable  func() bool
	lastActivity   time.Time
	activity       chan struct{}
	nextMirror     time.Time
	auditRunning   atomic.Bool

	manifestMu       sync.Mutex
	manifest         map[string]classify.RemoteState
	manifestRevision int
	storm            *conflicts.StormTracker
	logger           *diagnostics.Logger
	skippedReported  map[string]bool
}

// persistedManifestRevision lets a restarted client ask for a delta instead of
// downloading the whole 11 MB manifest again. The cursor is bound to the
// repository id: a different NAS must never be merged with an old snapshot.
func persistedManifestRevision(cfg *config.Config) int {
	if cfg.String("last_manifest_repository") != cfg.RepositoryID() {
		return -1
	}
	revision := cfg.Int("last_manifest_revision")
	if revision < 0 {
		return -1
	}
	return revision
}

// WatcherStatus reports whether the local filesystem watcher has started and,
// when it has, whether every folder is really watched. The UI uses it instead
// of checking an external inotifywait binary: the engine uses fsnotify in Go,
// so a missing command-line tool says nothing about change detection.
func (r *Runner) WatcherStatus() (known, reliable bool) {
	if r == nil {
		return false, false
	}
	r.mu.Lock()
	check := r.watchReliable
	r.mu.Unlock()
	if check == nil {
		return false, false
	}
	return true, check()
}

func (r *Runner) saveManifestRevision() {
	if r.manifestRevision < 0 {
		return
	}
	if r.cfg.Int("last_manifest_revision") == r.manifestRevision &&
		r.cfg.String("last_manifest_repository") == r.cfg.RepositoryID() {
		return
	}
	_ = r.cfg.Set("last_manifest_repository", r.cfg.RepositoryID(), true)
	_ = r.cfg.Set("last_manifest_revision", r.manifestRevision, true)
}

func NewRunner(
	cfg *config.Config, store *state.Store, client ConfigSource, transfers Transferer,
	loggers ...*diagnostics.Logger,
) *Runner {
	ctx, cancel := context.WithCancel(context.Background())
	runner := &Runner{
		cfg: cfg, store: store, client: client, transfers: transfers,
		dirty: map[string]bool{}, events: make(chan Event, 64), wake: make(chan struct{}, 1),
		activity: make(chan struct{}, 1), lastActivity: time.Now(),
		transferCtx: ctx, cancelTransfer: cancel,
		manifestRevision: persistedManifestRevision(cfg),
		skippedReported:  map[string]bool{},
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

// SyncNowForce starts a pass right away, ignoring any retry delay.
func (r *Runner) SyncNowForce() {
	r.mu.Lock()
	r.backoff = 0
	r.nextTry = time.Time{}
	r.mu.Unlock()
	r.SyncNow()
}

// RetryWaiting retries every waiting file at once.
func (r *Runner) RetryWaiting() {
	Issues.RetryNow()
	r.SyncNowForce()
}

// RestoreHeldDeletions brings back the files a safety guard was about to
// delete from the NAS: their baseline is forgotten, so the next pass sees the
// NAS copies as new files and downloads them. It returns how many.
func (r *Runner) RestoreHeldDeletions() (int, error) {
	paths := PendingDeletes.Paths()
	if len(paths) == 0 {
		return 0, nil
	}
	if err := r.store.ForgetPaths(r.cfg.LocalRepositoryID(), paths); err != nil {
		return 0, err
	}
	PendingDeletes.Set(nil)
	r.SyncNowForce()
	return len(paths), nil
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
	r.touchActivity()
	r.notify()
}

// touchActivity records local or remote activity and wakes a power-saving
// change feed so it returns to instant mode.
func (r *Runner) touchActivity() {
	r.mu.Lock()
	r.lastActivity = time.Now()
	r.mu.Unlock()
	select {
	case r.activity <- struct{}{}:
	default:
	}
}

// powerSavingIdle reports whether the change feed may relax: power saving is
// on and nothing happened locally or remotely for a while.
func (r *Runner) powerSavingIdle() (bool, time.Duration) {
	if !remote.PowerSaving(r.cfg) {
		return false, 0
	}
	idle := time.Duration(r.cfg.Int("power_saving_idle_minutes")) * time.Minute
	if idle <= 0 {
		idle = 15 * time.Minute
	}
	poll := time.Duration(r.cfg.Int("power_saving_poll_minutes")) * time.Minute
	if poll <= 0 {
		poll = 10 * time.Minute
	}
	r.mu.Lock()
	quiet := time.Since(r.lastActivity) >= idle
	r.mu.Unlock()
	return quiet, poll
}

// markRemoteDirty asks for a delta pull without forcing a full manifest
// export. The change feed uses it when another PC published something.
func (r *Runner) markRemoteDirty() {
	r.mu.Lock()
	r.remotePending = true
	r.mu.Unlock()
	r.notify()
}

func (r *Runner) takeDirty() ([]string, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	paths := make([]string, 0, len(r.dirty))
	for path := range r.dirty {
		paths = append(paths, path)
	}
	r.dirty = map[string]bool{}
	remote := r.remotePending
	r.remotePending = false
	return paths, remote
}

func (r *Runner) manifestCursor() int {
	r.manifestMu.Lock()
	defer r.manifestMu.Unlock()
	return r.manifestRevision
}

func (r *Runner) emit(kind, message string) {
	r.emitCounts(kind, message, SyncCounts{})
}

func (r *Runner) emitCounts(kind, message string, counts SyncCounts) {
	select {
	case r.events <- Event{Kind: kind, Message: message, Counts: counts}:
	default:
	}
	if r.logger != nil {
		action := strings.ToUpper(kind)
		switch kind {
		case "status":
			action = "INFO"
		case "busy":
			action = "LOCK_DEFERRED" // WARN, same name as the Python client
		}
		r.logger.Log(action, "-", message)
	}
}

func (r *Runner) excluded(relative string) bool {
	return excluded(r.cfg, relative)
}

// reportSkipped logs each non-regular local entry (symlink, fifo, socket) that
// the scan ignored, once per path. The Python client logged them as
// UNSUPPORTED: skipping is correct, but a file that silently never syncs must
// at least leave a trace. Repeats are suppressed because the pass runs often.
func (r *Runner) reportSkipped(snapshot *LocalSnapshot) {
	if r.logger == nil || snapshot == nil || len(snapshot.Skipped) == 0 {
		return
	}
	const limit = 50
	fresh := make([]string, 0, len(snapshot.Skipped))
	r.mu.Lock()
	if r.skippedReported == nil {
		r.skippedReported = map[string]bool{}
	}
	for _, rel := range snapshot.Skipped {
		if r.skippedReported[rel] {
			continue
		}
		r.skippedReported[rel] = true
		fresh = append(fresh, rel)
	}
	r.mu.Unlock()
	for index, rel := range fresh {
		if index == limit {
			_ = r.logger.Log("UNSUPPORTED", "-",
				fmt.Sprintf("%d percorsi non regolari ignorati", len(fresh)-limit))
			break
		}
		_ = r.logger.Log("UNSUPPORTED", rel, "link simbolico o file speciale ignorato")
	}
}

// Run blocks until ctx is cancelled. Filesystem events and the periodic timer
// wake the same singleflight-ish loop; only one sync runs at a time.
func (r *Runner) Run(ctx context.Context) error {
	debounce := time.Duration(r.cfg.Int("debounce_seconds")) * time.Second
	if debounce <= 0 {
		debounce = 2 * time.Second
	}
	// Real-time needs the NAS change feed; without it the timer is the only
	// source of remote changes, and "realtime": "on" makes it short.
	interval := effectivePollInterval(r.cfg)
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
	r.mu.Lock()
	r.watchReliable = watcher.Reliable
	r.mu.Unlock()
	if changeFeedEnabled(r.cfg) {
		go r.watchRemoteChanges(watchCtx)
	}

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
			// Only mirrors are due: run them in a normal (delta, incremental)
			// pass. Before 1.1.0 this forced a complete pass every 5 minutes
			// even with no mirror configured.
			if len(mirror.Configured(r.cfg)) == 0 {
				continue
			}
			// Only the mirrors are due. They run, and the rest of the pass is
			// skipped when they changed nothing (see below): it used to wake the
			// whole engine (manifest, scan, journal) every five minutes for
			// nothing.
			r.mu.Lock()
			r.mirrorTick = true
			r.mu.Unlock()
		case <-r.wake:
		case <-ctx.Done():
			return nil
		}
		r.mu.Lock()
		paused, next := r.paused, r.nextTry
		pending := len(r.dirty) > 0 || r.remotePending || r.mirrorTick
		r.mu.Unlock()
		if paused || timedPause(r.cfg) || scheduledPause(r.cfg) || !pending || time.Now().Before(next) {
			continue
		}
		paths, remoteOnly := r.takeDirty()
		if r.logger != nil && len(paths) > 0 && len(paths) <= 5 {
			// Which local paths woke this pass: the clue when passes come in bursts.
			_ = r.logger.Event("PERF", "-", "motivo passata: "+strings.Join(paths, ", "))
		}
		fullPass := false
		for _, path := range paths {
			if path == "" {
				fullPass = true
				break
			}
		}
		if remoteOnly && len(paths) == 0 {
			// A remote-only change uses the delta manifest; the full export is
			// reserved for startup, the periodic full pull and local "sync now".
			fullPass = false
		}
		r.mu.Lock()
		transferCtx := r.transferCtx
		r.mu.Unlock()
		r.transfers.SetContext(transferCtx)
		// Local mirrors run first: the normal push below then picks up the
		// freshly written copies in local_root and propagates them with the
		// usual journal and backup. This uses the same cancellable context as
		// the transfer instead of a remote lease or a sleeping idle loop.
		// The tick is consumed here whether or not a mirror is due, so a stale
		// tick can never keep requesting passes.
		r.mu.Lock()
		tick := r.mirrorTick
		r.mirrorTick = false
		r.mu.Unlock()
		if r.mirrorDue() {
			r.mu.Lock()
			r.nextMirror = time.Now().Add(mirrorInterval)
			r.mu.Unlock()
			count, changed, mirrorErr := mirror.SyncAllChanged(transferCtx, r.cfg)
			if mirrorErr != nil {
				r.emit("error", "mirror: "+mirrorErr.Error())
			}
			if changed > 0 {
				r.emit("status", fmt.Sprintf("mirror: %d cartelle locali aggiornate", count))
			}
			if tick && changed == 0 && mirrorErr == nil && len(paths) == 0 && !remoteOnly {
				continue // nothing changed anywhere: no pass at all
			}
		}
		r.emit("status", i18n.T("metrics.sync_busy_title", nil))
		manifestStart := time.Now()
		manifest, err := r.loadManifest(fullPass)
		if r.logger != nil {
			_ = r.logger.Event("PERF", "-", fmt.Sprintf("manifest: %.2fs (completo=%v)", time.Since(manifestStart).Seconds(), fullPass))
		}
		if err != nil {
			r.handleFailure(err)
			continue
		}
		result, err := SyncPass(r.cfg, r.store, r.client, r.transfers, manifest, r.scanScope(paths, fullPass), r.storm)
		r.mu.Lock()
		if err == nil && result.Local != nil {
			r.localSnapshot = result.Local
		} else {
			r.localSnapshot = nil // never reuse a snapshot from a failed pass
		}
		r.mu.Unlock()
		if err != nil {
			r.handleFailure(err)
			continue
		}
		r.reportSkipped(result.Local)
		if r.logger != nil && len(result.Timings) > 0 {
			_ = r.logger.Event("PERF", "-", "sync: "+strings.Join(result.Timings, ", "))
		}
		counts := SyncCounts{
			Uploads: result.Uploaded, Downloads: result.Downloads,
			Deletes: result.Deleted, Conflicts: result.Conflicts,
		}
		if result.Blocked != "" {
			// Only the deletions are held back; whatever else was transferred in
			// this pass is reported first, then the decision the user must take.
			if result.Uploaded+result.Downloads > 0 {
				r.emitCounts("notify", i18n.T("assurance.activity", map[string]any{
					"uploads": result.Uploaded, "downloads": result.Downloads, "deletes": result.Deleted,
				}), counts)
			}
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
		if result.Problems > 0 {
			summary += ", " + i18n.T("overview.reason_waiting", map[string]any{"count": result.Problems})
		}
		r.emit("sync", summary)
		if result.DeletesLeft > 0 && result.Deleted > 0 {
			// Large deletions run in chunks; keep going without waiting for the
			// next scheduled pass.
			r.SyncNow()
		}
		if r.cfg.Bool("notify_sync_completion") &&
			(result.Uploaded+result.Downloads+result.Deleted > 0) {
			r.emitCounts("notify", summary, counts)
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
		return r.loadFullManifest()
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
		// FALLBACK_FULL already carries the complete replacement. Fetching it
		// again doubles bandwidth and, with an older server, can pair an old
		// snapshot with a newer revision.
		r.manifest = remote.MergeDelta(nil, delta)
		r.manifestRevision = int(delta.CurrentRevision)
		r.saveManifestRevision()
		return r.manifest, nil
	}
	if delta.BaseRevision != int64(r.manifestRevision) {
		// A reset, restored repository or stale cursor cannot be merged safely.
		return r.loadFullManifest()
	}
	r.manifest = remote.MergeDelta(r.manifest, delta)
	r.manifestRevision = int(delta.CurrentRevision)
	r.saveManifestRevision()
	return r.manifest, nil
}

// loadFullManifest reads the revision before the manifest. If a write lands
// between the two calls, the cursor only lags behind the snapshot and the next
// delta replays it; it can never skip a change by pointing ahead of the data.
// The caller must hold manifestMu.
func (r *Runner) loadFullManifest() (map[string]classify.RemoteState, error) {
	revision := 0
	if status, err := r.client.JournalStatus(); err == nil {
		revision, _ = strconv.Atoi(status["MANIFEST_REVISION"])
	}
	raw, err := r.client.ManifestExport("")
	if err != nil {
		return nil, err
	}
	manifest, err := remote.ParseManifest(raw)
	if err != nil {
		return nil, err
	}
	r.manifest = manifest
	r.manifestRevision = revision
	r.saveManifestRevision()
	return manifest, nil
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

// watchRemoteChanges long-polls the NAS change feed when the server advertises
// it, so a file written by another PC wakes this client immediately instead of
// waiting for the next poll tick. The call is read-only and never holds the
// transfer lock.
func (r *Runner) watchRemoteChanges(ctx context.Context) {
	if !changeFeedEnabled(r.cfg) {
		return
	}
	waitSeconds := r.cfg.Int("change_feed_wait_seconds")
	if waitSeconds <= 0 {
		waitSeconds = 55
	}
	if waitSeconds > 3600 {
		waitSeconds = 3600
	}
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		// Power saving: after a quiet period, replace the continuous long
		// poll with a short check every few minutes, so the NAS (and its
		// disks) can rest. Any local change switches back immediately.
		if quiet, poll := r.powerSavingIdle(); quiet {
			select {
			case <-ctx.Done():
				return
			case <-r.activity:
				continue
			case <-time.After(poll):
			}
			if revision := r.manifestCursor(); revision >= 0 {
				if wait, err := r.client.WaitForRevision(revision, 0); err == nil && wait.Result == "CHANGED" {
					r.touchActivity()
					r.markRemoteDirty()
				}
			}
			continue
		}
		revision := r.manifestCursor()
		if revision < 0 {
			select {
			case <-ctx.Done():
				return
			case <-time.After(5 * time.Second):
			}
			continue
		}
		wait, err := r.client.WaitForRevision(revision, waitSeconds)
		if err != nil {
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Duration(waitSeconds) * time.Second / 4):
			}
			continue
		}
		if wait.Result == "CHANGED" {
			r.touchActivity()
			r.markRemoteDirty()
			// Give the sync loop time to consume the delta before asking again;
			// otherwise a burst of commits would spin the SSH channel.
			select {
			case <-ctx.Done():
				return
			case <-time.After(2 * time.Second):
			}
		}
	}
}

func scheduledPause(cfg *config.Config) bool {
	if !cfg.Bool("pause_schedule_enabled") {
		return false
	}
	return schedule.AnyActive(schedule.FromConfig(cfg.Get("pause_schedule")), time.Now())
}

// timedPause honours pause_until, the bounded pause shared with the Python
// client. An expired value is cleared so the button and config stay truthful.
func timedPause(cfg *config.Config) bool {
	until, ok := cfg.Get("pause_until").(float64)
	if !ok || until <= 0 {
		return false
	}
	if int64(until) > time.Now().Unix() {
		return true
	}
	_ = cfg.Set("pause_until", nil, true)
	return false
}

// Paused reports manual, timed or scheduled pause for the UI label.
func (r *Runner) Paused() bool {
	return r.IsPaused() || timedPause(r.cfg) || scheduledPause(r.cfg)
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
	if busy {
		// Another PC holds the NAS: normal multi-client contention, not a
		// fault. The pass is retried after the backoff above.
		r.emit("busy", message)
		return
	}
	r.emit("error", message)
}

// fullScanInterval bounds how long incremental scans are trusted.
func (r *Runner) fullScanInterval() time.Duration {
	if seconds := r.cfg.Int("full_scan_interval_seconds"); seconds > 0 {
		return time.Duration(seconds) * time.Second
	}
	return 30 * time.Minute
}

// scanScope decides between a complete walk and an incremental scan.
func (r *Runner) scanScope(paths []string, fullPass bool) *ScanScope {
	r.mu.Lock()
	defer r.mu.Unlock()
	scope := &ScanScope{Previous: r.localSnapshot, Full: fullPass}
	if r.localSnapshot == nil || r.watchReliable == nil || !r.watchReliable() ||
		time.Since(r.localSnapshot.FullAt) >= r.fullScanInterval() || len(paths) > 5000 {
		scope.Full = true
	}
	for _, path := range paths {
		if path == "" {
			scope.Full = true
			continue
		}
		scope.Dirty = append(scope.Dirty, path)
	}
	return scope
}
