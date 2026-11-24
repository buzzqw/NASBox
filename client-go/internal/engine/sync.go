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
	"nasbox-go/internal/i18n"
	"nasbox-go/internal/remote"
	"nasbox-go/internal/repository"
	"nasbox-go/internal/state"
)

// ConfigSource is the subset of the NAS client needed by one sync pass.
type ConfigSource interface {
	PrintConfig() (map[string]string, error)
	ManifestExport(since string) ([]byte, error)
	CheckedDelete(requests []remote.CheckedRequest, runTS, device string) (map[string]string, error)
	RetryPendingJournal(device string) (bool, string, error)
	JournalAppend(repositoryID, txid, device string, puts []remote.JournalPut) (bool, error)
	RenameV2(src, dst, kind, digest string, size, mtimeNS int64, device string) (bool, error)
}

// Transferer uploads and downloads batches of paths.
type Transferer interface {
	Upload(paths []string) error
	Download(paths []string) error
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
	device := store.DeviceID()
	manualConflicts := cfg.String("conflict_policy") == "manual"
	for path, remoteState := range manifest {
		if remoteState.Kind != "FILE" || excluded(cfg, path) {
			continue
		}
		current, present := local[path]
		if !present {
			if allowDownload {
				downloads = append(downloads, path)
			} else if allowDelete {
				missing = append(missing, path)
			}
			continue
		}
		if current.Digest == remoteState.Digest {
			continue
		}
		baseline := entries[path]
		localClock, _ := causal.Parse(baseline.Causal)
		remoteClock, _ := causal.Parse(remoteState.Causal)
		preferUpload := false
		switch {
		case localClock != nil && localClock.Dominates(remoteClock):
			preferUpload = true
		case remoteClock != nil && remoteClock.Dominates(localClock):
			preferUpload = false
		case manualConflicts && allowUpload && allowDownload:
			if _, err := conflicts.Create(cfg.LocalRoot(), path, remoteDevice(remoteState.Causal), remoteState.Digest); err != nil {
				return result, err
			}
			result.Conflicts++
			preferUpload = false
		case current.MTimeNS >= remoteState.MTimeNS:
			preferUpload = true
		default:
			preferUpload = false
		}
		if preferUpload && allowUpload {
			uploads = append(uploads, path)
		} else if !preferUpload && allowDownload {
			downloads = append(downloads, path)
		}
	}
	if allowUpload {
		for path := range local {
			if _, known := manifest[path]; !known {
				uploads = append(uploads, path)
			}
		}
	}

	ratio, minFiles := guardThresholds(cfg)
	report := classify.Classify(missing, manifest, toClassifyIdentities(identities), ratio, minFiles)
	result.Missing = report.MissingTotal
	result.Renames = len(report.Renames)
	if message := guardMessage(cfg, report); message != "" {
		result.Blocked = message
		return result, nil
	}

	// Renames are atomic server mutations. A failed rename must abort the pass:
	// falling back to delete+upload would risk losing the file if the upload
	// then failed.
	for _, rename := range report.Renames {
		state := manifest[rename.Missing]
		ok, err := client.RenameV2(
			rename.Missing, rename.Local, "FILE",
			state.Digest, state.Size, state.MTimeNS, device,
		)
		if err != nil || !ok {
			if err == nil {
				err = fmt.Errorf("rename remoto rifiutato: %s -> %s", rename.Missing, rename.Local)
			}
			return result, err
		}
		delete(manifest, rename.Missing)
	}

	// Uploads first: a file that still exists locally must never be lost to a
	// subsequent delete pass.
	if len(uploads) > 0 {
		if err := transfers.Upload(uploads); err != nil {
			return result, err
		}
		result.Uploaded = len(uploads)
		puts := make([]remote.JournalPut, 0, len(uploads))
		for _, path := range uploads {
			clock, _ := causal.Parse(entries[path].Causal)
			if clock == nil {
				clock = &causal.Version{}
			}
			file := local[path]
			next := clock.Increment(device).Encode()
			_ = store.RecordEntry(cfg.LocalRepositoryID(), path, state.EntryState{
				Digest: file.Digest, Size: file.Size, MTimeNS: file.MTimeNS,
				Causal: next,
			})
			puts = append(puts, remote.JournalPut{
				Path: path, Digest: file.Digest, Size: file.Size,
				MTimeNS: file.MTimeNS, Causal: next,
			})
		}
		if ok, err := client.JournalAppend(
			cfg.RepositoryID(), "upload-"+newRunTS(), device, puts,
		); err != nil || !ok {
			if err == nil {
				err = fmt.Errorf("journal NAS non aggiornato dopo upload")
			}
			return result, err
		}
	}
	if len(downloads) > 0 {
		if err := transfers.Download(downloads); err != nil {
			return result, err
		}
		result.Downloads = len(downloads)
		for _, path := range downloads {
			remoteState := manifest[path]
			clock, _ := causal.Parse(remoteState.Causal)
			if clock == nil {
				clock = &causal.Version{}
			}
			_ = store.RecordEntry(cfg.LocalRepositoryID(), path, state.EntryState{
				Digest: remoteState.Digest, Size: remoteState.Size,
				MTimeNS: remoteState.MTimeNS, Causal: clock.Encode(),
			})
		}
	}
	deletions := report.Unexplained
	if !allowDelete {
		deletions = nil
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
			state := manifest[path]
			requests = append(requests, remote.CheckedRequest{
				Path: path, Digest: state.Digest,
				MTimeSeconds: state.MTimeNS / 1_000_000_000,
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
