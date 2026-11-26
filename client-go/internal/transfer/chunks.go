package transfer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"nasbox-go/internal/config"
	"nasbox-go/internal/history"
	"nasbox-go/internal/paths"
	"nasbox-go/internal/remote"
)

// Chunks replaces rsync with the NASBox chunk protocol. Files are still
// published through one journaled transaction; the difference is that only
// chunks the NAS does not already have cross the network.
type Chunks struct {
	cfg    *config.Config
	client *remote.Client
	ctx    context.Context

	observerMu sync.Mutex
	onFile     FileHandler
}

func NewChunks(cfg *config.Config, client *remote.Client) *Chunks {
	return &Chunks{cfg: cfg, client: client, ctx: context.Background()}
}

func (t *Chunks) SetContext(ctx context.Context) {
	if ctx != nil {
		t.ctx = ctx
	}
}

func (t *Chunks) SetObservers(_ ProgressHandler, files FileHandler) {
	t.observerMu.Lock()
	t.onFile = files
	t.observerMu.Unlock()
}

func (t *Chunks) emitFile(upload bool, path string) {
	t.observerMu.Lock()
	handler := t.onFile
	t.observerMu.Unlock()
	if handler != nil {
		handler(FileEvent{Upload: upload, Path: path, At: time.Now()})
	}
}

func (t *Chunks) chunkSize() int {
	if raw := strings.Fields(t.cfg.String("chunk_size")); len(raw) > 0 {
		if value, err := strconv.Atoi(raw[0]); err == nil && value >= 4096 {
			return value
		}
	}
	return 1024 * 1024
}

func chunkFile(local string, chunkSize int) (string, int64, int64, []remote.ChunkUpload, []string, error) {
	file, err := os.Open(local)
	if err != nil {
		return "", 0, 0, nil, nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return "", 0, 0, nil, nil, err
	}
	whole := sha256.New()
	uploads := []remote.ChunkUpload{}
	digests := []string{}
	buffer := make([]byte, chunkSize)
	for {
		n, readErr := io.ReadFull(file, buffer)
		if n > 0 {
			block := buffer[:n]
			sum := sha256.Sum256(block)
			digest := hex.EncodeToString(sum[:])
			uploads = append(uploads, remote.ChunkUpload{Digest: digest, Data: append([]byte{}, block...)})
			digests = append(digests, digest)
			_, _ = whole.Write(block)
		}
		if readErr == io.EOF || readErr == io.ErrUnexpectedEOF {
			break
		}
		if readErr != nil {
			return "", 0, 0, nil, nil, readErr
		}
	}
	return hex.EncodeToString(whole.Sum(nil)), info.Size(), info.ModTime().UnixNano(), uploads, digests, nil
}

func (t *Chunks) localPath(relative string) (string, error) {
	return paths.SafeDestination(t.cfg.LocalRoot(), relative)
}

func (t *Chunks) upload(relative string) (string, int64, int64, []string, error) {
	local, err := t.localPath(relative)
	if err != nil {
		return "", 0, 0, nil, err
	}
	digest, size, mtime, uploads, digests, err := chunkFile(local, t.chunkSize())
	if err != nil {
		return "", 0, 0, nil, err
	}
	if _, err := t.client.ChunkPutBatch(t.cfg.String("repository_id"), uploads); err != nil {
		return "", 0, 0, nil, err
	}
	return digest, size, mtime, digests, nil
}

func (t *Chunks) UploadStaged(_ []string, device string, files []remote.StagedFile) error {
	if len(files) == 0 {
		return nil
	}
	if err := t.client.ValidateHostKeyPin(context.Background()); err != nil {
		return err
	}
	txID := "chunk-stage-" + time.Now().UTC().Format("20060102-150405-000000")
	runTS := remote.RunTimestamp()
	batch := make([]remote.ChunkPublishFile, 0, len(files))
	for _, file := range files {
		digest, size, mtime, chunks, err := t.upload(file.Path)
		if err != nil {
			return fmt.Errorf("chunk upload %s: %w", file.Path, err)
		}
		if file.Digest != "" && digest != file.Digest {
			return fmt.Errorf("file locale cambiato durante il caricamento: %s", file.Path)
		}
		batch = append(batch, remote.ChunkPublishFile{
			Path: file.Path, Digest: digest, Causal: file.Causal,
			Size: size, MTimeNS: mtime, Chunks: chunks,
		})
	}
	if err := t.client.FilePublishBatch(device, txID, runTS, batch, true); err != nil {
		return err
	}
	for _, file := range batch {
		t.emitFile(true, file.Path)
	}
	return nil
}

func (t *Chunks) UploadReplace(files []remote.ReplaceFile, device, repositoryID, runTS string) (remote.ReplaceResult, error) {
	result := remote.ReplaceResult{}
	if len(files) == 0 {
		return result, nil
	}
	if err := t.client.ValidateHostKeyPin(context.Background()); err != nil {
		return result, err
	}
	nas := t.client
	release, err := nas.AcquireLease(device)
	if err != nil {
		return result, err
	}
	defer release()

	pathsWant := make([]string, 0, len(files))
	for _, file := range files {
		pathsWant = append(pathsWant, file.Path)
	}
	before, err := nas.FileStatesCurrent(pathsWant)
	if err != nil {
		return result, fmt.Errorf("stato NAS non leggibile prima della sostituzione: %w", err)
	}
	ready := make([]remote.ReplaceFile, 0, len(files))
	for _, file := range files {
		current, ok := before[file.Path]
		if !ok || current.Kind != "FILE" || current.Digest != file.ExpectedDigest || current.Size != file.ExpectedSize {
			result.Stale = append(result.Stale, file.Path)
			continue
		}
		ready = append(ready, file)
	}
	if err := adoptOutOfBand(nas, files, before, &result, device, repositoryID, runTS); err != nil {
		return result, err
	}
	txID := "chunk-replace-" + runTS
	batch := make([]remote.ChunkPublishFile, 0, len(ready))
	for _, file := range ready {
		digest, size, mtime, chunks, err := t.upload(file.Path)
		if err != nil {
			return result, fmt.Errorf("chunk upload %s: %w", file.Path, err)
		}
		if digest != file.Digest {
			result.Stale = append(result.Stale, file.Path)
			continue
		}
		batch = append(batch, remote.ChunkPublishFile{
			Path: file.Path, Digest: digest, Causal: file.Causal,
			Size: size, MTimeNS: mtime, Chunks: chunks,
		})
	}
	if len(batch) > 0 {
		if err := t.client.FilePublishBatch(device, txID, runTS, batch, false); err != nil {
			return result, err
		}
		for _, file := range batch {
			t.emitFile(true, file.Path)
		}
	}
	if len(ready) > 0 {
		after, err := nas.FileStatesCurrent(pathsWant)
		if err != nil {
			return result, fmt.Errorf("stato NAS non leggibile dopo la sostituzione: %w", err)
		}
		for _, file := range ready {
			state, ok := after[file.Path]
			if !ok || state.Kind != "FILE" {
				return result, fmt.Errorf("file assente sul NAS dopo la pubblicazione: %s", file.Path)
			}
			result.Replaced = append(result.Replaced, remote.JournalPut{
				Path: file.Path, Digest: state.Digest, Size: state.Size,
				MTimeNS: state.MTimeNS, Causal: file.Causal,
			})
		}
	}
	return result, nil
}

func (t *Chunks) Download(paths []string) error {
	if len(paths) == 0 {
		return nil
	}
	if err := t.client.ValidateHostKeyPin(context.Background()); err != nil {
		return err
	}
	for _, relative := range paths {
		info, err := t.client.FileChunks(relative)
		if err != nil {
			return err
		}
		if !info.Found {
			info, err = t.client.IngestFileChunks(relative)
			if err != nil {
				return fmt.Errorf("file NAS non disponibile: %s: %w", relative, err)
			}
		}
		if err := t.assembleLocal(relative, info, true); err != nil {
			return err
		}
	}
	return nil
}

// DownloadPath fetches one remote path without making a local backup: it is
// used by "nasbox show"/preview, where the caller only wants a local copy.
func (t *Chunks) DownloadPath(relative string) error {
	if relative == "" {
		return fmt.Errorf("percorso remoto vuoto")
	}
	if err := t.client.ValidateHostKeyPin(context.Background()); err != nil {
		return err
	}
	info, err := t.client.FileChunks(relative)
	if err != nil {
		return err
	}
	if !info.Found {
		info, err = t.client.IngestFileChunks(relative)
		if err != nil {
			return err
		}
	}
	return t.assembleLocal(relative, info, false)
}

// DownloadHistoryVersion restores a .sync-trash version. The caller has
// already saved the current local file, so no additional backup is needed.
func (t *Chunks) DownloadHistoryVersion(trashRel, relative string) error {
	if trashRel == "" || filepath.IsAbs(trashRel) || strings.Contains(trashRel, "..") {
		return fmt.Errorf("percorso storico NAS non valido")
	}
	if err := t.client.ValidateHostKeyPin(context.Background()); err != nil {
		return err
	}
	info, err := t.client.HistoryChunks(trashRel)
	if err != nil {
		return err
	}
	return t.assembleLocal(relative, info, false)
}

func (t *Chunks) assembleLocal(relative string, info remote.FileChunks, backup bool) error {
	local, err := t.localPath(relative)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(local), 0o755); err != nil {
		return err
	}
	if backup {
		if _, err := os.Lstat(local); err == nil {
			trash, err := history.TrashDir()
			if err != nil {
				return err
			}
			if err := os.MkdirAll(trash, 0o700); err != nil {
				return err
			}
			now := time.Now().UTC()
			stamp := now.Format("2006-01-02") + "--" + strings.Replace(now.Format("15-04-05.000000"), ".", "-", 1) + "Z"
			target := filepath.Join(trash, filepath.FromSlash(relative)+"-"+stamp)
			if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
				return err
			}
			if err := copyLocalFile(target, local); err != nil {
				return err
			}
		}
	}
	temporary := filepath.Join(filepath.Dir(local), "."+filepath.Base(local)+".nasbox-chunks")
	file, err := os.OpenFile(temporary, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	for _, digest := range info.Chunks {
		data, err := t.client.ChunkGet(digest)
		if err != nil {
			file.Close()
			os.Remove(temporary)
			return err
		}
		sum := sha256.Sum256(data)
		if hex.EncodeToString(sum[:]) != digest {
			file.Close()
			os.Remove(temporary)
			return fmt.Errorf("chunk corrotto: %s", digest)
		}
		if _, err := file.Write(data); err != nil {
			file.Close()
			os.Remove(temporary)
			return err
		}
	}
	if err := file.Close(); err != nil {
		os.Remove(temporary)
		return err
	}
	stat, err := os.Stat(temporary)
	if err != nil || stat.Size() != info.Size {
		os.Remove(temporary)
		return fmt.Errorf("dimensione locale non corrisponde per %s", relative)
	}
	stampTime := time.Unix(0, info.MTimeNS)
	if err := os.Chtimes(temporary, stampTime, stampTime); err != nil {
		os.Remove(temporary)
		return err
	}
	if err := os.Rename(temporary, local); err != nil {
		os.Remove(temporary)
		return err
	}
	t.emitFile(false, relative)
	return nil
}

func copyLocalFile(target, source string) error {
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// Backend is the subset of the sync engine used for file transfers.
type Backend interface {
	Download(paths []string) error
	DownloadPath(relative string) error
	DownloadHistoryVersion(trashRel, relative string) error
	UploadStaged(paths []string, device string, files []remote.StagedFile) error
	UploadReplace(files []remote.ReplaceFile, device, repositoryID, runTS string) (remote.ReplaceResult, error)
	SetContext(ctx context.Context)
	SetObservers(progress ProgressHandler, files FileHandler)
}

// NewForConfig selects the data path. rsync remains the default until the
// NAS runs the Go daemon with the chunk verbs; a client pointed at that daemon
// can opt in with transfer_backend=chunks.
func NewForConfig(cfg *config.Config, client *remote.Client) Backend {
	if strings.EqualFold(cfg.String("transfer_backend"), "chunks") {
		return NewChunks(cfg, client)
	}
	return New(cfg, client)
}
