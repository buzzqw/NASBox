package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
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

type SyncResult struct {
	Uploaded  int
	Deleted   int
	Blocked   string
	Missing   int
	Renames   int
	Conflicts int
	Downloads int
	// Stale counts overwrites skipped because the NAS changed after planning.
	Stale int
	// LocalDeleted counts files removed locally because another PC deleted
	// them (moved to the local trash, always recoverable).
	LocalDeleted int
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

// SyncOnceWithManifest runs a pass against an already-authoritative snapshot.
// The Runner uses this to merge server deltas without exporting the full tree
// on every filesystem event.
func SyncOnceWithManifest(
	cfg *config.Config,
	store *state.Store,
	client ConfigSource,
	transfers Transferer,
	manifest map[string]classify.RemoteState,
	stormTrackers ...*conflicts.StormTracker,
) (SyncResult, error) {
	result := SyncResult{}
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
	identities, err := store.IdentitySnapshot(cfg.LocalRepositoryID())
	if err != nil {
		return result, err
	}
	entries, err := store.EntrySnapshot(cfg.LocalRepositoryID())
	if err != nil {
		return result, err
	}
	local, err := scanLocal(cfg, identities)
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
		if _, present := local[path]; !present {
			observations[path] = state.Observation{Present: false}
		}
	}
	localCausals := make(map[string]string, len(observations))
	for path, observation := range observations {
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
		if remoteState.Kind != "FILE" || excluded(cfg, path) {
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
			return result, fmt.Errorf("sincronizzazione bloccata per %s: %s", path, decision.Detail)
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
	result.Missing = report.MissingTotal
	result.Renames = len(report.Renames)
	if message := guardMessage(cfg, report); message != "" {
		result.Blocked = message
		return result, nil
	}
	if message := localDeleteGuard(cfg, len(localDeletes)); message != "" {
		result.Blocked = message
		return result, nil
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
		if len(created) > 0 {
			if err := transfers.UploadStaged(created, device, files); err != nil {
				return result, err
			}
			// Only now, after the server committed the publish, promote the
			// local observations to baselines. A failed publish leaves them
			// untouched so the next pass retries with the very same causal.
			for _, file := range files {
				if err := store.RecordEntry(cfg.LocalRepositoryID(), file.Path, state.EntryState{
					Digest: file.Digest, Size: file.Size, MTimeNS: file.MTimeNS,
					Causal: file.Causal,
				}); err != nil {
					return result, err
				}
			}
			result.Uploaded += len(files)
		}
		if len(replacements) > 0 {
			replaced, err := transfers.UploadReplace(replacements, device, cfg.String("repository_id"), newRunTS())
			if err != nil {
				return result, err
			}
			// The baseline is what the NAS journaled. If the local file
			// changed during the upload it differs from the local version, so
			// the next pass uploads the newer bytes.
			for _, put := range replaced.Replaced {
				if err := store.RecordEntry(cfg.LocalRepositoryID(), put.Path, state.EntryState{
					Digest: put.Digest, Size: put.Size, MTimeNS: put.MTimeNS, Causal: put.Causal,
				}); err != nil {
					return result, err
				}
			}
			result.Uploaded += len(replaced.Replaced)
			result.Stale += len(replaced.Stale)
		}
	}
	if len(downloads) > 0 {
		if err := transfers.Download(downloads); err != nil {
			return result, err
		}
		for _, path := range downloads {
			remoteState := manifest[path]
			remoteCausal := remoteState.Causal
			if _, ok := causal.Parse(remoteCausal); !ok {
				remoteCausal = ""
			}
			if err := store.RecordEntry(cfg.LocalRepositoryID(), path, state.EntryState{
				Digest: remoteState.Digest, Size: remoteState.Size,
				MTimeNS: remoteState.MTimeNS, Causal: remoteCausal,
			}); err != nil {
				return result, err
			}
		}
		result.Downloads = len(downloads)
	}
	if len(localDeletes) > 0 {
		deleted, err := applyRemoteDeletions(cfg, store, local, manifest, localDeletes)
		result.LocalDeleted = deleted
		if err != nil {
			return result, err
		}
	}
	deletions := make([]string, 0, len(report.Unexplained))
	for _, path := range report.Unexplained {
		if allowDelete && deleteApproved[path] {
			deletions = append(deletions, path)
		}
	}
	if err := deletesafety.Shared().Check(cfg, len(deletions), "sync"); err != nil {
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
		runTS := newRunTS()
		device := store.DeviceID()
		requests := make([]remote.CheckedRequest, 0, len(deletions))
		for _, path := range deletions {
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
			}
		}
		if err := store.RecordUndo(cfg.LocalRepositoryID(), done, runTS); err != nil {
			return result, err
		}
		// Record the confirmed deletion as a tombstone carrying the local
		// absence observation's causal, not a freshly bumped clock: the server
		// already wrote the matching journal entry under that same version.
		for _, path := range done {
			if err := store.RecordEntry(cfg.LocalRepositoryID(), path, state.EntryState{
				Causal: localCausals[path],
			}); err != nil {
				return result, err
			}
		}
		result.Deleted = len(done)
		if err := deletesafety.Shared().Record(cfg, len(done), "sync"); err != nil {
			return result, err
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

func scanLocal(cfg *config.Config, identities map[string]state.Identity) (map[string]localFile, error) {
	root := cfg.LocalRoot()
	result := make(map[string]localFile)
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
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
		if excluded(cfg, rel) || strings.HasPrefix(rel, ".git/") ||
			strings.HasPrefix(rel, ".venv/") || strings.HasPrefix(rel, ".nasbox-client-update/") ||
			strings.HasPrefix(rel, ".nasbox-client-update-go/") ||
			strings.HasPrefix(rel, ".sync-partial/") || strings.HasPrefix(rel, ".sync-trash/") ||
			strings.HasPrefix(rel, ".nasbox-staging/") || strings.HasPrefix(rel, "@eaDir/") ||
			strings.HasSuffix(rel, ".pyc") {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.IsDir() {
			return nil
		}
		if !entry.Type().IsRegular() {
			return fmt.Errorf("percorso locale non regolare, sync interrotto: %s", rel)
		}
		info, err := entry.Info()
		if err != nil {
			return fmt.Errorf("stat locale non riuscito per %s: %w", rel, err)
		}
		before := statOf(info)
		digest := ""
		if known, ok := identities[rel]; ok && known.Size == before.size &&
			known.MTimeNS == before.mtimeNS && known.CtimeNS == before.ctimeNS && known.Digest != "" {
			digest = known.Digest
		} else {
			sum, err := hashFile(path)
			if err != nil {
				return fmt.Errorf("lettura locale non riuscita per %s: %w", rel, err)
			}
			digest = sum
			afterInfo, err := os.Lstat(path)
			if err != nil {
				return fmt.Errorf("file cambiato durante la lettura: %s: %w", rel, err)
			}
			after := statOf(afterInfo)
			if before != after {
				return fmt.Errorf("file instabile durante la lettura, sync interrotto: %s", rel)
			}
		}
		result[rel] = localFile{Digest: digest, Size: before.size, MTimeNS: before.mtimeNS}
		return nil
	})
	return result, err
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
