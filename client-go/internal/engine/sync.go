package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"nasbox-go/internal/causal"
	"nasbox-go/internal/classify"
	"nasbox-go/internal/config"
	"nasbox-go/internal/conflicts"
	"nasbox-go/internal/deletesafety"
	"nasbox-go/internal/history"
	"nasbox-go/internal/i18n"
	"nasbox-go/internal/reconcile"
	"nasbox-go/internal/remote"
	"nasbox-go/internal/repository"
	"nasbox-go/internal/state"
)

// ConfigSource is the subset of the NAS client needed by one sync pass.
type ConfigSource interface {
	PrintConfig() (map[string]string, error)
	ManifestExport(since string) ([]byte, error)
	JournalStatus() (map[string]string, error)
	CheckedDelete(requests []remote.CheckedRequest, runTS, device string) (map[string]string, error)
	RetryPendingJournal(device string) (bool, string, error)
	JournalAppend(repositoryID, txid, device string, puts []remote.JournalPut) (bool, error)
	RenameV2(src, dst, kind, digest string, size, mtimeNS int64, device string) (bool, error)
	WaitForRevision(previous, timeout int) (remote.RevisionWait, error)
}

// Transferer uploads and downloads batches of paths. Uploads go through the
// private staging tree so the canonical remote path is only written once, in
// the same server transaction that records the journal.
type Transferer interface {
	Download(paths []string) error
	UploadStaged(paths []string, device string, files []remote.StagedFile) error
	// UploadReplace overwrites files that already exist on the NAS
	// (staging-publish never overwrites), under the NAS lease and with the
	// previous version kept in the NAS history.
	UploadReplace(files []remote.ReplaceFile, device, repositoryID, runTS string) (remote.ReplaceResult, error)
	SetContext(ctx context.Context)
}

// sizeHinter is implemented by transfer.Transfer: the planned size of every
// file lets the UI compute percentage and ETA from one source. Fakes in tests
// do not implement it and nothing changes for them.
type sizeHinter interface {
	HintSizes(sizes map[string]int64)
}

func hintSizes(transfers Transferer, paths []string, size func(string) int64) {
	hinter, ok := transfers.(sizeHinter)
	if !ok {
		return
	}
	sizes := make(map[string]int64, len(paths))
	for _, path := range paths {
		sizes[path] = size(path)
	}
	hinter.HintSizes(sizes)
}

// nasStateReader is implemented by remote.Client: it reads what the NAS holds
// on disk for some paths (also files the manifest does not know). Test fakes
// do not implement it and skip the checks that need it.
type nasStateReader interface {
	FileStatesCurrent(paths []string) (map[string]remote.FileState, error)
}

// nasNameChecker answers "which of these names already exist on the NAS?"
// without hashing anything (see remote.Client.ExistingPaths).
type nasNameChecker interface {
	ExistingPaths(paths []string) (map[string]bool, error)
}

// deleteChunk is how many files one CheckedDelete request carries and
// deletionBudget how long a pass keeps deleting before it yields.
const (
	deleteChunk    = 100
	deletionBudget = 4 * time.Minute
)

type SyncResult struct {
	Uploaded int
	Deleted  int
	Blocked  string
	Missing  int
	Renames  int
	// RenamesRefused counts moves the NAS refused; they fall back to
	// upload + guarded deletion.
	RenamesRefused int
	Conflicts      int
	Downloads      int
	// DeletesLeft counts approved deletions not yet done when the pass time
	// budget ended; the next pass continues them.
	DeletesLeft int
	// Problems is the number of files waiting for a retry (see Issues).
	Problems int
	// Stale counts overwrites skipped because the NAS changed after planning.
	Stale int
	// LocalDeleted counts files removed locally because another PC deleted
	// them (moved to the local trash, always recoverable).
	LocalDeleted int
	// Local is the local snapshot of this pass (for the next incremental
	// scan); nil when the scan failed.
	Local *LocalSnapshot
	// Timings records how long each phase took ("scan 1.62s"), logged as
	// PERF events so slow passes can be diagnosed on real machines.
	Timings []string
}

// phaseTimer appends "name 1.23s" to result.Timings for each phase.
type phaseTimer struct {
	result *SyncResult
	last   time.Time
}

func (p *phaseTimer) mark(name string) {
	now := time.Now()
	p.result.Timings = append(p.result.Timings, fmt.Sprintf("%s %.2fs", name, now.Sub(p.last).Seconds()))
	p.last = now
}

type localFile struct {
	Digest  string
	Size    int64
	MTimeNS int64
}

func newRunTS() string {
	now := time.Now().UTC()
	return now.Format("2006-01-02") + "--" +
		strings.Replace(now.Format("15-04-05.000000"), ".", "-", 1) + "Z"
}

// SyncOnce performs one conservative bidirectional pass. It never mutates
// anything while a guard is active, and all decisions are made on the PC.
func SyncOnce(
	cfg *config.Config,
	store *state.Store,
	client ConfigSource,
	transfers Transferer,
) (SyncResult, error) {
	result := SyncResult{}
	configValues, err := client.PrintConfig()
	if err != nil {
		return result, err
	}
	if strings.ToLower(configValues["REPOSITORY_READY"]) != "true" ||
		strings.ToLower(configValues["JOURNAL_READY"]) != "true" {
		return result, fmt.Errorf("repository NAS non pronto")
	}
	if configValues["REPOSITORY_ID"] != "" && configValues["REPOSITORY_ID"] != cfg.RepositoryID() {
		return result, fmt.Errorf("ID repository NAS cambiato")
	}
	raw, err := client.ManifestExport("")
	if err != nil {
		return result, err
	}
	manifest, err := remote.ParseManifest(raw)
	if err != nil {
		return result, err
	}
	return SyncOnceWithManifest(cfg, store, client, transfers, manifest)
}

// SyncOnceWithManifest runs a pass with a complete local scan.
func SyncOnceWithManifest(
	cfg *config.Config,
	store *state.Store,
	client ConfigSource,
	transfers Transferer,
	manifest map[string]classify.RemoteState,
	stormTrackers ...*conflicts.StormTracker,
) (SyncResult, error) {
	return SyncPass(cfg, store, client, transfers, manifest, nil, stormTrackers...)
}

// SyncOnceWithManifest runs a pass against an already-authoritative snapshot.
// The Runner uses this to merge server deltas without exporting the full tree
// on every filesystem event.
// SyncPass runs one pass; scope (optional) allows an incremental local scan.
func SyncPass(
	cfg *config.Config,
	store *state.Store,
	client ConfigSource,
	transfers Transferer,
	manifest map[string]classify.RemoteState,
	scope *ScanScope,
	stormTrackers ...*conflicts.StormTracker,
) (SyncResult, error) {
	Issues.BeginPass()
	result, err := syncPass(cfg, store, client, transfers, manifest, scope, stormTrackers...)
	Issues.EndPass()
	result.Problems = Issues.Count()
	return result, err
}

func syncPass(
	cfg *config.Config,
	store *state.Store,
	client ConfigSource,
	transfers Transferer,
	manifest map[string]classify.RemoteState,
	scope *ScanScope,
	stormTrackers ...*conflicts.StormTracker,
) (SyncResult, error) {
	result := SyncResult{}
	timer := &phaseTimer{result: &result, last: time.Now()}
	storm := (*conflicts.StormTracker)(nil)
	if len(stormTrackers) > 0 {
		storm = stormTrackers[0]
	}
	if err := repository.Validate(cfg.LocalRoot(), cfg.RepositoryID(), true); err != nil {
		return result, err
	}
	if ok, detail, err := client.RetryPendingJournal(store.DeviceID()); err != nil || !ok {
		if err != nil {
			return result, err
		}
		return result, fmt.Errorf("journal NAS: %s", detail)
	}
	timer.mark("journal")
	identities, err := store.IdentitySnapshot(cfg.LocalRepositoryID())
	if err != nil {
		return result, err
	}
	entries, err := store.EntrySnapshot(cfg.LocalRepositoryID())
	if err != nil {
		return result, err
	}
	timer.mark("state")
	local, snapshot, scanMode, err := localScan(cfg, identities, scope, entries, manifest)
	result.Local = snapshot
	if err != nil {
		return result, err
	}

	mode := strings.ToLower(cfg.String("sync_mode"))
	allowUpload := mode == "" || mode == "bidirectional" || mode == "push_only" || mode == "archive"
	allowDownload := mode == "" || mode == "bidirectional" || mode == "pull_only"
	allowDelete := cfg.Bool("delete_enabled") && mode != "archive"

	missing := make([]string, 0)
	uploads := make([]string, 0)
	downloads := make([]string, 0)
	// Only paths for which reconcile explicitly authorised a remote deletion
	// may reach CheckedDelete. classify may still turn them into renames, but
	// it must never resurrect a stale local absence as a delete.
	deleteApproved := make(map[string]bool)
	uploadSeen := make(map[string]bool)
	downloadSeen := make(map[string]bool)
	device := store.DeviceID()
	policy := cfg.String("conflict_policy")
	manualConflicts := strings.EqualFold(policy, "manual")

	// Observe every relevant local path once. Missing paths get a real causal
	// deletion observation; read errors never reach this point because
	// scanLocal fails closed.
	observations := make(map[string]state.Observation, len(local)+len(manifest))
	for path, current := range local {
		observations[path] = state.Observation{
			Digest: current.Digest, Size: current.Size,
			MTimeNS: current.MTimeNS, Present: true,
		}
	}
	for path, remoteState := range manifest {
		if remoteState.Kind != "FILE" || excluded(cfg, path) {
			continue
		}
		if _, present := local[path]; !present && !Issues.Held(path) {
			observations[path] = state.Observation{Present: false}
		}
	}
	timer.mark("scan " + scanMode)
	localCausals := make(map[string]string, len(observations))
	stored, err := store.ObservationSnapshot(cfg.LocalRepositoryID())
	if err != nil {
		return result, err
	}
	for path, observation := range observations {
		record, known := stored[path]
		if version, unchanged := state.UnchangedCausal(record, known, observation); unchanged {
			localCausals[path] = version
			continue
		}
		version, err := store.Observe(cfg.LocalRepositoryID(), path, device, observation)
		if err != nil {
			return result, fmt.Errorf("osservazione locale non salvata per %s: %w", path, err)
		}
		localCausals[path] = version
	}

	plan := func(path string, remoteState reconcile.RemoteState) (reconcile.Decision, error) {
		var baseline *reconcile.Fingerprint
		if entry, ok := entries[path]; ok && entry.Digest != "" {
			baseline = &reconcile.Fingerprint{
				Digest: entry.Digest, Size: entry.Size,
				MTimeNS: entry.MTimeNS, Causal: entry.Causal,
			}
		}
		var localFP *reconcile.Fingerprint
		if current, present := local[path]; present {
			localFP = &reconcile.Fingerprint{
				Digest: current.Digest, Size: current.Size,
				MTimeNS: current.MTimeNS, Causal: localCausals[path],
			}
		}
		return reconcile.Plan(baseline, localFP, remoteState, localCausals[path], allowDelete, policy), nil
	}

	// Deletions made on other PCs arrive as manifest tombstones. Before
	// 0.20.0 they were filtered out here and never reached the planner, so a
	// file deleted on one PC stayed on every other PC forever.
	localDeletes := make([]string, 0)
	for path, remoteState := range manifest {
		if remoteState.Kind != "TOMBSTONE" || excluded(cfg, path) {
			continue
		}
		if _, present := local[path]; !present {
			continue
		}
		decision, err := plan(path, reconcile.RemoteState{
			Kind: reconcile.Tombstone, MTimeNS: remoteState.MTimeNS, Causal: remoteState.Causal,
		})
		if err != nil {
			return result, err
		}
		switch decision.Action {
		case reconcile.RemoteWins, reconcile.ConflictRemoteWins:
			if allowDownload && allowDelete {
				localDeletes = append(localDeletes, path)
			}
		case reconcile.Upload, reconcile.ConflictLocalWins:
			if allowUpload && !uploadSeen[path] {
				uploads = append(uploads, path)
				uploadSeen[path] = true
			}
		}
	}

	for path, remoteState := range manifest {
		if remoteState.Kind != "FILE" || excluded(cfg, path) || Issues.Held(path) {
			continue
		}
		decision, err := plan(path, reconcile.RemoteState{
			Kind: reconcile.File, Digest: remoteState.Digest, Size: remoteState.Size,
			MTimeNS: remoteState.MTimeNS, Causal: remoteState.Causal,
		})
		if err != nil {
			return result, err
		}
		switch decision.Action {
		case reconcile.Nothing, reconcile.Adopt:
			continue
		case reconcile.Block:
			// One file the planner will not touch must not stop the others.
			Issues.Report(path, PhasePlan, decision.Detail)
			continue
		case reconcile.DeleteRemote:
			if allowDelete {
				missing = append(missing, path)
				deleteApproved[path] = true
			}
		case reconcile.Upload, reconcile.ConflictLocalWins:
			if allowUpload && !uploadSeen[path] {
				uploads = append(uploads, path)
				uploadSeen[path] = true
			}
		case reconcile.RemoteWins, reconcile.ConflictRemoteWins:
			if !allowDownload {
				continue
			}
			if decision.Action == reconcile.ConflictRemoteWins && manualConflicts {
				if _, present := local[path]; present {
					var conflictPath string
					var conflictErr error
					if storm != nil {
						conflictPath, conflictErr = storm.Create(cfg.LocalRoot(), path, remoteDevice(remoteState.Causal), local[path].Digest)
					} else {
						conflictPath, conflictErr = conflicts.Create(cfg.LocalRoot(), path, remoteDevice(remoteState.Causal), local[path].Digest)
					}
					if conflictErr != nil {
						return result, conflictErr
					}
					_ = conflictPath
					result.Conflicts++
				}
			}
			if !downloadSeen[path] {
				downloads = append(downloads, path)
				downloadSeen[path] = true
			}
		}
	}
	if allowUpload {
		for path := range local {
			if _, known := manifest[path]; known {
				continue
			}
			decision, err := plan(path, reconcile.RemoteState{Kind: reconcile.Absent})
			if err != nil {
				return result, err
			}
			if decision.Action == reconcile.Upload || decision.Action == reconcile.ConflictLocalWins {
				if !uploadSeen[path] {
					uploads = append(uploads, path)
					uploadSeen[path] = true
				}
			}
		}
	}

	timer.mark("observe+plan")
	ratio, minFiles := guardThresholds(cfg)
	// state.Identity carries the lstat identity (including ctime) while
	// classify.Identity is the compact digest/size view used for rename
	// matching. Start from the former and refresh digest/size with the fresh
	// scan so a rename is matched against the bytes actually on disk now.
	currentIdentities := toClassifyIdentities(identities)
	for path, current := range local {
		identity := currentIdentities[path]
		identity.Digest = current.Digest
		identity.Size = current.Size
		currentIdentities[path] = identity
	}
	report := classify.Classify(missing, manifest, currentIdentities, ratio, minFiles)
	report = dropRenamesOntoExistingFiles(client, report)
	result.Missing = report.MissingTotal
	result.Renames = len(report.Renames)
	// A safety guard holds back the deletions only. Uploads, downloads and
	// renames are not dangerous and keep flowing: a duplicate folder removed
	// by hand must not freeze the whole sync.
	deletionsHeld := false
	if message := guardMessage(cfg, report); message != "" {
		result.Blocked = message
		deletionsHeld = true
	}
	if message := localDeleteGuard(cfg, len(localDeletes)); message != "" {
		result.Blocked = message
		localDeletes = nil
	}

	// Renames are atomic server mutations. A failed rename must abort the pass:
	// falling back to delete+upload would risk losing the file if the upload
	// then failed.
	for _, rename := range report.Renames {
		remoteState := manifest[rename.Missing]
		ok, err := client.RenameV2(
			rename.Missing, rename.Local, "FILE",
			remoteState.Digest, remoteState.Size, remoteState.MTimeNS, device,
		)
		if err != nil || !ok {
			if err == nil {
				err = fmt.Errorf("rename remoto rifiutato: %s -> %s", rename.Missing, rename.Local)
			}
			// The NAS refusing one move (destination already there, source
			// changed since the manifest...) must not stop the whole pass for
			// ever. The file is uploaded under its new name (uploads run
			// first) and the old name goes through the normal deletion
			// safeguards, which keep a copy in the NAS history: nothing can be
			// lost this way. Connection and server failures still abort.
			if isNASRefusal(err) {
				report.Unexplained = append(report.Unexplained, rename.Missing)
				result.RenamesRefused++
				continue
			}
			return result, err
		}
		delete(manifest, rename.Missing)
		// The rename already moved the very file that was queued for upload.
		// Re-uploading it would write a second version of the same bytes and
		// bump the clock once more. Move the baseline instead: a tombstone at
		// the source and the observed remote fingerprint at the destination.
		renameCausal := localCausals[rename.Missing]
		_ = store.RecordEntry(cfg.LocalRepositoryID(), rename.Missing, state.EntryState{Causal: renameCausal})
		_ = store.RecordEntry(cfg.LocalRepositoryID(), rename.Local, state.EntryState{
			Digest: remoteState.Digest, Size: remoteState.Size, MTimeNS: remoteState.MTimeNS,
			Causal: renameCausal,
		})
		entries[rename.Missing] = state.EntryState{Causal: renameCausal}
		entries[rename.Local] = state.EntryState{
			Digest: remoteState.Digest, Size: remoteState.Size, MTimeNS: remoteState.MTimeNS,
			Causal: renameCausal,
		}
		if uploadSeen[rename.Local] {
			uploadSeen[rename.Local] = false
			kept := uploads[:0]
			for _, path := range uploads {
				if path != rename.Local {
					kept = append(kept, path)
				}
			}
			uploads = kept
		}
	}

	// Files that failed recently wait for their retry delay.
	uploads = withoutWaiting(uploads)
	downloads = withoutWaiting(downloads)

	// Uploads first: a file that still exists locally must never be lost to a
	// subsequent delete pass. The clock was already advanced once by Observe;
	// staging publishes that exact version, so no second increment is allowed.
	if len(uploads) > 0 {
		// staging-publish only creates files: a path the NAS already holds is
		// overwritten through UploadReplace, with the planned NAS version as
		// precondition. Mixing both in one staging batch made the server
		// reject the whole batch ("destinazione gia' esistente").
		created := make([]string, 0, len(uploads))
		files := make([]remote.StagedFile, 0, len(uploads))
		replacements := make([]remote.ReplaceFile, 0)
		for _, path := range uploads {
			file := local[path]
			staged := remote.StagedFile{
				Path: path, Digest: file.Digest, Size: file.Size,
				MTimeNS: file.MTimeNS, Causal: localCausals[path],
			}
			if remoteState, exists := manifest[path]; exists && remoteState.Kind == "FILE" {
				replacements = append(replacements, remote.ReplaceFile{
					StagedFile: staged, ExpectedDigest: remoteState.Digest, ExpectedSize: remoteState.Size,
				})
				continue
			}
			created = append(created, path)
			files = append(files, staged)
		}
		if reader, ok := client.(nasStateReader); ok && len(created) > 0 {
			// The NAS may already hold some of these paths on disk without the
			// manifest knowing them (copied there by hand, or left by an
			// interrupted run). staging-publish refuses them and used to abort
			// the whole pass every time: identical files are adopted into the
			// journal without a transfer, different ones are replaced under
			// the NAS lease with the current NAS version as precondition.
			candidates := created
			if checker, ok := client.(nasNameChecker); ok {
				taken, err := checker.ExistingPaths(created)
				if err != nil {
					return result, err
				}
				candidates = candidates[:0:0]
				for _, path := range created {
					if taken[path] {
						candidates = append(candidates, path)
					}
				}
			}
			states := map[string]remote.FileState{}
			if len(candidates) > 0 {
				var err error
				if states, err = reader.FileStatesCurrent(candidates); err != nil {
					return result, err
				}
			}
			keptCreated := created[:0:0]
			keptFiles := files[:0:0]
			adopted := make([]remote.JournalPut, 0)
			for index, path := range created {
				state, exists := states[path]
				if !exists || state.Kind != "FILE" {
					keptCreated = append(keptCreated, path)
					keptFiles = append(keptFiles, files[index])
					continue
				}
				staged := files[index]
				if state.Digest == staged.Digest && state.Size == staged.Size {
					adopted = append(adopted, remote.JournalPut{
						Path: path, Digest: staged.Digest, Size: staged.Size,
						MTimeNS: staged.MTimeNS, Causal: staged.Causal,
					})
					continue
				}
				replacements = append(replacements, remote.ReplaceFile{
					StagedFile: staged, ExpectedDigest: state.Digest, ExpectedSize: state.Size,
				})
			}
			if len(adopted) > 0 {
				ok, err := client.JournalAppend(cfg.String("repository_id"), "adopt-"+newRunTS(), device, adopted)
				if err != nil || !ok {
					if err == nil {
						err = fmt.Errorf("journal NAS non aggiornato per i file già presenti")
					}
					return result, err
				}
				for _, put := range adopted {
					if err := store.RecordEntry(cfg.LocalRepositoryID(), put.Path, state.EntryState{
						Digest: put.Digest, Size: put.Size, MTimeNS: put.MTimeNS, Causal: put.Causal,
					}); err != nil {
						return result, err
					}
				}
				result.Uploaded += len(adopted)
			}
			created, files = keptCreated, keptFiles
		}
		if len(created) > 0 {
			stagedByPath := make(map[string]remote.StagedFile, len(files))
			for _, file := range files {
				stagedByPath[file.Path] = file
			}
			err := isolateBatch(created, PhaseUpload, 60, func(batch []string) error {
				batchFiles := make([]remote.StagedFile, 0, len(batch))
				for _, path := range batch {
					batchFiles = append(batchFiles, stagedByPath[path])
				}
				hintSizes(transfers, batch, func(path string) int64 { return local[path].Size })
				if err := transfers.UploadStaged(batch, device, batchFiles); err != nil {
					return err
				}
				// Only now, after the server committed the publish, promote the
				// local observations to baselines. A failed publish leaves them
				// untouched so the next pass retries with the very same causal.
				for _, file := range batchFiles {
					if err := store.RecordEntry(cfg.LocalRepositoryID(), file.Path, state.EntryState{
						Digest: file.Digest, Size: file.Size, MTimeNS: file.MTimeNS,
						Causal: file.Causal,
					}); err != nil {
						return fatalError{err}
					}
				}
				result.Uploaded += len(batchFiles)
				return nil
			})
			if err != nil {
				return result, unwrapFatal(err)
			}
		}
		if len(replacements) > 0 {
			replacementByPath := make(map[string]remote.ReplaceFile, len(replacements))
			replacementPaths := make([]string, 0, len(replacements))
			for _, file := range replacements {
				replacementByPath[file.Path] = file
				replacementPaths = append(replacementPaths, file.Path)
			}
			err := isolateBatch(replacementPaths, PhaseUpload, 60, func(batch []string) error {
				subset := make([]remote.ReplaceFile, 0, len(batch))
				for _, path := range batch {
					subset = append(subset, replacementByPath[path])
				}
				hintSizes(transfers, batch, func(path string) int64 { return local[path].Size })
				replaced, err := transfers.UploadReplace(subset, device, cfg.String("repository_id"), newRunTS())
				if err != nil {
					return err
				}
				// The baseline is what the NAS journaled. If the local file
				// changed during the upload it differs from the local version, so
				// the next pass uploads the newer bytes.
				for _, put := range replaced.Replaced {
					if err := store.RecordEntry(cfg.LocalRepositoryID(), put.Path, state.EntryState{
						Digest: put.Digest, Size: put.Size, MTimeNS: put.MTimeNS, Causal: put.Causal,
					}); err != nil {
						return fatalError{err}
					}
				}
				result.Uploaded += len(replaced.Replaced)
				result.Stale += len(replaced.Stale)
				return nil
			})
			if err != nil {
				return result, unwrapFatal(err)
			}
		}
	}
	if len(downloads) > 0 {
		err := isolateBatch(downloads, PhaseDownload, 60, func(batch []string) error {
			hintSizes(transfers, batch, func(path string) int64 { return manifest[path].Size })
			if err := transfers.Download(batch); err != nil {
				return err
			}
			for _, path := range batch {
				remoteState := manifest[path]
				remoteCausal := remoteState.Causal
				if _, ok := causal.Parse(remoteCausal); !ok {
					remoteCausal = ""
				}
				if err := store.RecordEntry(cfg.LocalRepositoryID(), path, state.EntryState{
					Digest: remoteState.Digest, Size: remoteState.Size,
					MTimeNS: remoteState.MTimeNS, Causal: remoteCausal,
				}); err != nil {
					return fatalError{err}
				}
			}
			result.Downloads += len(batch)
			return nil
		})
		if err != nil {
			return result, unwrapFatal(err)
		}
	}
	if len(localDeletes) > 0 {
		deleted, err := applyRemoteDeletions(cfg, store, local, manifest, localDeletes)
		result.LocalDeleted = deleted
		if err != nil {
			return result, err
		}
	}
	deletions := make([]string, 0, len(report.Unexplained))
	proposed := make([]string, 0, len(report.Unexplained))
	for _, path := range report.Unexplained {
		if allowDelete && deleteApproved[path] && !Issues.Waiting(path) {
			proposed = append(proposed, path)
			if !deletionsHeld {
				deletions = append(deletions, path)
			}
		}
	}
	if deletionsHeld {
		PendingDeletes.Set(proposed)
	} else {
		PendingDeletes.Set(nil)
	}
	if err := deletesafety.Shared().Check(cfg, len(deletions), "sync"); err != nil {
		PendingDeletes.Set(proposed)
		if blocked, ok := err.(*deletesafety.BlockedError); ok {
			result.Blocked = i18n.T("safety.blocked_cumulative", map[string]any{
				"count": blocked.Total, "limit": blocked.Limit,
			})
		} else {
			result.Blocked = err.Error()
		}
		return result, nil
	}
	if len(deletions) > 0 {
		// The NAS deletes file by file (move to history, journal, fsync): a
		// weak NAS manages a few files per second, so one request for
		// thousands of files outlived its timeout and was killed. Deletions
		// go in chunks, each recorded as soon as it is done; whatever is left
		// when the pass time budget ends is simply continued by the next one.
		runTS := newRunTS()
		device := store.DeviceID()
		sort.Strings(deletions)
		budget := time.Now().Add(deletionBudget)
		attempted := 0
		for start := 0; start < len(deletions); start += deleteChunk {
			if start > 0 && time.Now().After(budget) {
				break
			}
			end := start + deleteChunk
			if end > len(deletions) {
				end = len(deletions)
			}
			chunk := deletions[start:end]
			attempted = end
			requests := make([]remote.CheckedRequest, 0, len(chunk))
			for _, path := range chunk {
				remoteState := manifest[path]
				requests = append(requests, remote.CheckedRequest{
					Path: path, Digest: remoteState.Digest,
					MTimeSeconds: remoteState.MTimeNS / 1_000_000_000,
				})
			}
			statuses, err := client.CheckedDelete(requests, runTS, device)
			if err != nil {
				return result, err
			}
			done := make([]string, 0, len(statuses))
			for path, status := range statuses {
				if status == "DELETED" || status == "ABSENT" {
					done = append(done, path)
					Issues.Clear(path)
				} else {
					// The NAS did not delete it ("STALE": it changed since the
					// manifest, or an error). Leave it alone for a while
					// instead of asking again at every pass.
					Issues.Report(path, PhaseDelete, "NAS: "+status)
				}
			}
			if err := store.RecordUndo(cfg.LocalRepositoryID(), done, runTS); err != nil {
				return result, err
			}
			// Record the confirmed deletion as a tombstone carrying the local
			// absence observation's causal, not a freshly bumped clock: the
			// server already wrote the matching journal entry under that same
			// version.
			for _, path := range done {
				if err := store.RecordEntry(cfg.LocalRepositoryID(), path, state.EntryState{
					Causal: localCausals[path],
				}); err != nil {
					return result, err
				}
			}
			result.Deleted += len(done)
			if err := deletesafety.Shared().Record(cfg, len(done), "sync"); err != nil {
				return result, err
			}
		}
		if attempted < len(deletions) {
			// The time budget ended: the rest continues in the next pass. Files
			// the NAS answered "stale" or "error" for are NOT counted here, or
			// a path that can never be deleted would keep the client in a loop
			// of immediate passes.
			PendingDeletes.Set(nil)
			result.DeletesLeft = len(deletions) - attempted
		}
	}
	return result, nil
}

func remoteDevice(causalText string) string {
	clock, ok := causal.Parse(causalText)
	if !ok || len(clock.Clock) == 0 {
		return "nas"
	}
	return clock.Clock[len(clock.Clock)-1].Device
}

func toClassifyIdentities(identities map[string]state.Identity) map[string]classify.Identity {
	out := make(map[string]classify.Identity, len(identities))
	for path, id := range identities {
		out[path] = classify.Identity{Digest: id.Digest, Size: id.Size, CtimeNS: id.CtimeNS}
	}
	return out
}

func guardThresholds(cfg *config.Config) (float64, int) {
	ratio, minFiles := 0.5, 20
	if value, ok := cfg.Get("delete_dir_guard_ratio").(float64); ok {
		ratio = value
	}
	if value, ok := cfg.Get("delete_dir_guard_min_files").(float64); ok {
		minFiles = int(value)
	}
	return ratio, minFiles
}

func guardMessage(cfg *config.Config, report classify.Report) string {
	root := cfg.LocalRoot()
	for _, value := range strings.Split(cfg.String("delete_sentinels"), ",") {
		sentinel := strings.TrimSpace(value)
		if sentinel == "" {
			continue
		}
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(sentinel))); err != nil {
			return i18n.T("guard.blocked_sentinel", map[string]any{"path": sentinel})
		}
	}
	if len(report.Anomalies) > 0 {
		item := report.Anomalies[0]
		text := i18n.T("guard.blocked_directory", map[string]any{
			"directory": item.Dir, "missing": item.Missing, "remote": item.Remote,
		})
		return strings.ReplaceAll(
			text, "{ratio:.0%}", fmt.Sprintf("%.0f%%", item.Ratio()*100),
		)
	}
	limit := cfg.Int("max_delete_files")
	if limit <= 0 {
		limit = 1000
	}
	if len(report.Unexplained) > limit {
		return fmt.Sprintf("%s %s", i18n.T("safety.blocked_proposed", map[string]any{
			"count": len(report.Unexplained), "limit": limit,
		}), i18n.T("guard.preview", map[string]any{"summary": report.Summary()}))
	}
	return ""
}

func scanLocal(cfg *config.Config, identities map[string]state.Identity, skipped *[]string) (map[string]localFile, error) {
	result := make(map[string]localFile)
	return result, scanTree(cfg, identities, "", result, skipped)
}

// scanTree walks the subtree at rel ("" = whole repository) into result,
// with the same fail-closed rules as a full scan for regular files.
func scanTree(
	cfg *config.Config, identities map[string]state.Identity, start string,
	result map[string]localFile, skipped *[]string,
) error {
	root := cfg.LocalRoot()
	base := root
	if start != "" {
		base = filepath.Join(root, filepath.FromSlash(start))
	}
	return filepath.WalkDir(base, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			// A folder or file NASBox cannot read right now (permissions, a
			// file in use) is held back, not fatal: everything else syncs.
			// "Held" keeps it from being uploaded or taken for deleted.
			if relPath, relErr := filepath.Rel(root, path); relErr == nil && relPath != "." {
				Issues.Report(filepath.ToSlash(relPath), PhaseRead, walkErr.Error())
				if entry != nil && entry.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			return fmt.Errorf("scan locale non riuscito per %s: %w", path, walkErr)
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if rel == "." {
			return nil
		}
		if skipLocalPath(cfg, rel) {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.IsDir() {
			return nil
		}
		if !entry.Type().IsRegular() {
			// Symlinks, fifos and sockets are ignored like the Python client
			// does: they are never uploaded (WalkDir does not follow links,
			// so the tree stays safe) and skipping them is not an error.
			if skipped != nil {
				*skipped = append(*skipped, rel)
			}
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			Issues.Report(rel, PhaseRead, err.Error())
			return nil
		}
		file, err := scanFile(path, rel, info, identities)
		if err != nil {
			Issues.Report(rel, PhaseRead, err.Error())
			return nil
		}
		result[rel] = file
		return nil
	})
}

// skipLocalPath: internal folders, caches and excluded paths.
func skipLocalPath(cfg *config.Config, rel string) bool {
	return excluded(cfg, rel) || strings.HasPrefix(rel, ".git/") ||
		strings.HasPrefix(rel, ".venv/") || strings.HasPrefix(rel, ".nasbox-client-update/") ||
		strings.HasPrefix(rel, ".nasbox-client-update-go/") ||
		strings.HasPrefix(rel, ".sync-partial/") || strings.HasPrefix(rel, ".sync-trash/") ||
		strings.HasPrefix(rel, ".nasbox-staging/") || strings.HasPrefix(rel, "@eaDir/") ||
		strings.HasSuffix(rel, ".pyc")
}

// scanFile fingerprints one regular file, reusing the stored digest when
// size, mtime and ctime are unchanged and failing closed if the file changes
// while it is being read.
func scanFile(path, rel string, info os.FileInfo, identities map[string]state.Identity) (localFile, error) {
	before := statOf(info)
	if known, ok := identities[rel]; ok && known.Size == before.size &&
		known.MTimeNS == before.mtimeNS && known.CtimeNS == before.ctimeNS && known.Digest != "" {
		return localFile{Digest: known.Digest, Size: before.size, MTimeNS: before.mtimeNS}, nil
	}
	sum, err := hashFile(path)
	if err != nil {
		return localFile{}, fmt.Errorf("lettura locale non riuscita per %s: %w", rel, err)
	}
	afterInfo, err := os.Lstat(path)
	if err != nil {
		return localFile{}, fmt.Errorf("file cambiato durante la lettura: %s: %w", rel, err)
	}
	if after := statOf(afterInfo); before != after {
		return localFile{}, fmt.Errorf("file instabile durante la lettura, sync interrotto: %s", rel)
	}
	return localFile{Digest: sum, Size: before.size, MTimeNS: before.mtimeNS}, nil
}

func hashFile(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hasher := sha256.New()
	if _, err := io.Copy(hasher, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}

type fileStat struct{ size, mtimeNS, ctimeNS int64 }

func statOf(info os.FileInfo) fileStat {
	stat := fileStat{size: info.Size(), mtimeNS: info.ModTime().UnixNano()}
	if raw, ok := info.Sys().(*syscall.Stat_t); ok {
		stat.ctimeNS = raw.Ctim.Sec*1e9 + int64(raw.Ctim.Nsec)
	}
	return stat
}

// localDeleteGuard blocks a pass that would remove more local files than the
// configured batch limit because of NAS deletions (for example a NAS folder
// deleted by mistake from another PC). Nothing is touched while blocked.
func localDeleteGuard(cfg *config.Config, count int) string {
	limit := cfg.Int("max_delete_files")
	if limit <= 0 {
		limit = 1000
	}
	if count <= limit {
		return ""
	}
	return i18n.T("guard.blocked_local_deletes", map[string]any{"count": count, "limit": limit})
}

// applyRemoteDeletions moves locally present files deleted on the NAS into
// the local trash. A file that changed since the scan is left alone: the next
// pass re-plans it (it may be a newer local edit that must be uploaded).
func applyRemoteDeletions(
	cfg *config.Config, store *state.Store, local map[string]localFile,
	manifest map[string]classify.RemoteState, paths []string,
) (int, error) {
	root := cfg.LocalRoot()
	deleted := 0
	for _, path := range paths {
		planned := local[path]
		info, err := os.Lstat(filepath.Join(root, filepath.FromSlash(path)))
		if err != nil || !info.Mode().IsRegular() || info.Size() != planned.Size ||
			info.ModTime().UnixNano() != planned.MTimeNS {
			continue
		}
		if err := history.MoveToTrash(root, path); err != nil {
			return deleted, fmt.Errorf("cancellazione locale di %s: %w", path, err)
		}
		remoteCausal := manifest[path].Causal
		if _, ok := causal.Parse(remoteCausal); !ok {
			remoteCausal = ""
		}
		if err := store.RecordEntry(cfg.LocalRepositoryID(), path, state.EntryState{Causal: remoteCausal}); err != nil {
			return deleted, err
		}
		deleted++
	}
	return deleted, nil
}

// dropRenamesOntoExistingFiles turns a rename whose destination the NAS
// already holds on disk into an ordinary missing path: the server refuses such
// a move ("esiste gia un percorso") and the refusal used to abort every pass.
// The vanished source then goes through the normal deletion safeguards.
func dropRenamesOntoExistingFiles(client ConfigSource, report classify.Report) classify.Report {
	if len(report.Renames) == 0 {
		return report
	}
	targets := make([]string, 0, len(report.Renames))
	for _, rename := range report.Renames {
		targets = append(targets, rename.Local)
	}
	taken := map[string]bool{}
	if checker, ok := client.(nasNameChecker); ok {
		found, err := checker.ExistingPaths(targets)
		if err != nil {
			return report // keep the plan; a real NAS problem surfaces on the rename itself
		}
		taken = found
	} else if reader, ok := client.(nasStateReader); ok {
		states, err := reader.FileStatesCurrent(targets)
		if err != nil {
			return report
		}
		for path, state := range states {
			if state.Kind == "FILE" {
				taken[path] = true
			}
		}
	} else {
		return report
	}
	kept := report.Renames[:0:0]
	for _, rename := range report.Renames {
		if taken[rename.Local] {
			report.Unexplained = append(report.Unexplained, rename.Missing)
			continue
		}
		kept = append(kept, rename)
	}
	report.Renames = kept
	return report
}

// isNASRefusal reports whether the error is the NAS answering "no" to one
// rename (as opposed to a connection or server failure).
func isNASRefusal(err error) bool {
	if err == nil {
		return false
	}
	text := err.Error()
	return strings.Contains(text, "il NAS ha rifiutato lo spostamento") ||
		strings.Contains(text, "rename remoto fallito") ||
		strings.Contains(text, "rename remoto rifiutato")
}

// withoutWaiting drops the files that failed recently and are still inside
// their retry delay; they are tried again by a later pass.
func withoutWaiting(paths []string) []string {
	kept := paths[:0:0]
	for _, path := range paths {
		if !Issues.Waiting(path) {
			kept = append(kept, path)
		}
	}
	return kept
}
