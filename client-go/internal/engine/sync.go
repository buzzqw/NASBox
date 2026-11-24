package engine

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"nasbox-go/internal/classify"
	"nasbox-go/internal/config"
	"nasbox-go/internal/i18n"
	"nasbox-go/internal/remote"
	"nasbox-go/internal/state"
)

// ConfigSource is the subset of the NAS client needed by one sync pass.
type ConfigSource interface {
	PrintConfig() (map[string]string, error)
	ManifestExport(since string) ([]byte, error)
	CheckedDelete(requests []remote.CheckedRequest, runTS, device string) (map[string]string, error)
}

// Transferer uploads and downloads batches of paths.
type Transferer interface {
	Upload(paths []string) error
	Download(paths []string) error
}

type SyncResult struct {
	Uploaded int
	Deleted  int
	Blocked  string
	Missing  int
	Renames  int
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
	identities, err := store.IdentitySnapshot(cfg.RepositoryID())
	if err != nil {
		return result, err
	}
	local, err := scanLocal(cfg.LocalRoot(), identities)
	if err != nil {
		return result, err
	}

	missing := make([]string, 0)
	uploads := make([]string, 0)
	for path, remoteState := range manifest {
		if remoteState.Kind != "FILE" {
			continue
		}
		current, present := local[path]
		if !present {
			missing = append(missing, path)
			continue
		}
		if current.Digest != remoteState.Digest {
			// The local version is the one we can prove right now; the losing
			// bytes stay in the NAS trash via rsync backups.
			uploads = append(uploads, path)
		}
	}
	for path := range local {
		if _, known := manifest[path]; !known {
			uploads = append(uploads, path)
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

	// Uploads first: a file that still exists locally must never be lost to a
	// subsequent delete pass.
	if len(uploads) > 0 {
		if err := transfers.Upload(uploads); err != nil {
			return result, err
		}
		result.Uploaded = len(uploads)
	}
	deletions := report.Unexplained
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
		if err := store.RecordUndo(cfg.RepositoryID(), done, runTS); err != nil {
			return result, err
		}
		result.Deleted = len(done)
	}
	return result, nil
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

func scanLocal(root string, identities map[string]state.Identity) (map[string]localFile, error) {
	result := make(map[string]localFile)
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info == nil || !info.Mode().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if strings.HasPrefix(rel, ".git/") || strings.HasPrefix(rel, ".venv/") ||
			strings.HasPrefix(rel, ".nasbox-client-update/") || strings.HasSuffix(rel, ".pyc") {
			return nil
		}
		stat := statOf(info)
		digest := ""
		if known, ok := identities[rel]; ok && known.Size == stat.size &&
			known.MTimeNS == stat.mtimeNS && known.CtimeNS == stat.ctimeNS && known.Digest != "" {
			digest = known.Digest
		} else {
			sum, err := hashFile(path)
			if err != nil {
				return nil
			}
			digest = sum
		}
		result[rel] = localFile{Digest: digest, Size: stat.size, MTimeNS: stat.mtimeNS}
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
