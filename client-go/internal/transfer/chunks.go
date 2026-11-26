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
	ctxMu  sync.RWMutex
	ctx    context.Context

	observerMu sync.Mutex
	onProgress ProgressHandler
	onFile     FileHandler
}

func NewChunks(cfg *config.Config, client *remote.Client) *Chunks {
	return &Chunks{cfg: cfg, client: client, ctx: context.Background()}
}

func (t *Chunks) SetContext(ctx context.Context) {
	if ctx != nil {
		t.ctxMu.Lock()
		t.ctx = ctx
		t.ctxMu.Unlock()
	}
}

func (t *Chunks) context() context.Context {
	t.ctxMu.RLock()
	defer t.ctxMu.RUnlock()
	if t.ctx == nil {
		return context.Background()
	}
	return t.ctx
}

func (t *Chunks) SetObservers(progress ProgressHandler, files FileHandler) {
	t.observerMu.Lock()
	t.onProgress = progress
	t.onFile = files
	t.observerMu.Unlock()
}

func (t *Chunks) observers() (ProgressHandler, FileHandler) {
	t.observerMu.Lock()
	defer t.observerMu.Unlock()
	return t.onProgress, t.onFile
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

func (t *Chunks) bandwidthLimit(upload bool) int64 {
	limit := t.cfg.Int("bandwidth_kbps")
	if upload {
		if configured := t.cfg.Int("bandwidth_upload_kbps"); configured > 0 {
			limit = configured
		}
	} else if configured := t.cfg.Int("bandwidth_download_kbps"); configured > 0 {
		limit = configured
	}
	if limit <= 0 {
		return 0
	}
	return int64(limit) * 1024
}

func chunkFile(local string, chunkSize int) (string, int64, int64, []string, error) {
	file, err := os.Open(local)
	if err != nil {
		return "", 0, 0, nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return "", 0, 0, nil, err
	}
	whole := sha256.New()
	digests := []string{}
	buffer := make([]byte, chunkSize)
	var readSize int64
	for {
		n, readErr := io.ReadFull(file, buffer)
		if n > 0 {
			block := buffer[:n]
			sum := sha256.Sum256(block)
			digests = append(digests, hex.EncodeToString(sum[:]))
			_, _ = whole.Write(block)
			readSize += int64(n)
		}
		if readErr == io.EOF || readErr == io.ErrUnexpectedEOF {
			break
		}
		if readErr != nil {
			return "", 0, 0, nil, readErr
		}
	}
	after, err := file.Stat()
	if err != nil {
		return "", 0, 0, nil, err
	}
	if readSize != info.Size() || after.Size() != info.Size() || after.ModTime().UnixNano() != info.ModTime().UnixNano() {
		return "", 0, 0, nil, fmt.Errorf("file locale cambiato durante il caricamento: %s", local)
	}
	return hex.EncodeToString(whole.Sum(nil)), info.Size(), info.ModTime().UnixNano(), digests, nil
}

func (t *Chunks) localPath(relative string) (string, error) {
	return paths.SafeDestination(t.cfg.LocalRoot(), relative)
}

func (t *Chunks) upload(ctx context.Context, relative string, onProgress func(int64)) (string, int64, int64, []string, error) {
	local, err := t.localPath(relative)
	if err != nil {
		return "", 0, 0, nil, err
	}
	digest, size, mtime, digests, err := chunkFile(local, t.chunkSize())
	if err != nil {
		return "", 0, 0, nil, err
	}
	missing, err := t.client.ChunkMissing(ctx, t.cfg.String("repository_id"), digests)
	if err != nil {
		return "", 0, 0, nil, err
	}
	if _, err := t.client.ChunkPutBatchStream(ctx, t.cfg.String("repository_id"), local, digests, missing, t.chunkSize(), size, t.bandwidthLimit(true), onProgress); err != nil {
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
	ctx := t.context()
	runTS := remote.RunTimestamp()
	txID := "chunk-stage-" + runTS
	onProgress, _ := t.observers()
	var totalBytes int64
	for _, file := range files {
		totalBytes += file.Size
	}
	progress := newChunkProgress(onProgress, true, len(files), totalBytes)
	batch := make([]remote.ChunkPublishFile, 0, len(files))
	for _, file := range files {
		progress.setFile(file.Path)
		digest, size, mtime, chunks, err := t.upload(ctx, file.Path, progress.add)
		if err != nil {
			progress.finish(err)
			return fmt.Errorf("chunk upload %s: %w", file.Path, err)
		}
		if file.Digest != "" && digest != file.Digest {
			err := fmt.Errorf("file locale cambiato durante il caricamento: %s", file.Path)
			progress.finish(err)
			return err
		}
		batch = append(batch, remote.ChunkPublishFile{
			Path: file.Path, Digest: digest, Causal: file.Causal,
			Size: size, MTimeNS: mtime, Chunks: chunks,
		})
	}
	if err := ctx.Err(); err != nil {
		progress.finish(err)
		return err
	}
	if err := t.client.FilePublishBatch(device, txID, runTS, batch, "create", true); err != nil {
		progress.finish(err)
		return err
	}
	for _, file := range batch {
		progress.complete()
		t.emitFile(true, file.Path)
	}
	progress.finish(nil)
	return nil
}

func (t *Chunks) UploadReplace(files []remote.ReplaceFile, device, repositoryID, runTS string) (result remote.ReplaceResult, err error) {
	if len(files) == 0 {
		return result, nil
	}
	if err := t.client.ValidateHostKeyPin(context.Background()); err != nil {
		return result, err
	}
	ctx := t.context()
	nas := t.client
	pathsWant := make([]string, 0, len(files))
	for _, file := range files {
		pathsWant = append(pathsWant, file.Path)
	}
	before, err := nas.FileStatesCurrent(pathsWant)
	if err != nil {
		return result, fmt.Errorf("stato NAS non leggibile prima della sostituzione: %w", err)
	}
	candidates := make([]remote.ReplaceFile, 0, len(files))
	for _, file := range files {
		current, ok := before[file.Path]
		if !ok || current.Kind != "FILE" || current.Digest != file.ExpectedDigest || current.Size != file.ExpectedSize {
			result.Stale = append(result.Stale, file.Path)
			continue
		}
		candidates = append(candidates, file)
	}
	// Hash and transfer outside the NAS lease. Only the final precondition and
	// publish need to exclude other devices, so a multi-GB upload no longer
	// blocks every other client. Even with no candidates we continue to the
	// lease below so out-of-band NAS edits can be adopted.
	txID := "chunk-replace-" + runTS
	onProgress, _ := t.observers()
	var totalBytes int64
	for _, file := range candidates {
		totalBytes += file.Size
	}
	progress := newChunkProgress(onProgress, true, len(candidates), totalBytes)
	defer func() { progress.finish(err) }()
	type uploaded struct {
		file    remote.ReplaceFile
		publish remote.ChunkPublishFile
	}
	uploads := make([]uploaded, 0, len(candidates))
	for _, file := range candidates {
		progress.setFile(file.Path)
		digest, size, mtime, chunks, err := t.upload(ctx, file.Path, progress.add)
		if err != nil {
			return result, fmt.Errorf("chunk upload %s: %w", file.Path, err)
		}
		if digest != file.Digest {
			result.Stale = append(result.Stale, file.Path)
			continue
		}
		uploads = append(uploads, uploaded{file: file, publish: remote.ChunkPublishFile{
			Path: file.Path, Digest: digest, Causal: file.Causal,
			Size: size, MTimeNS: mtime, Chunks: chunks,
		}})
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	release, err := nas.AcquireLease(device)
	if err != nil {
		return result, err
	}
	defer release()
	before, err = nas.FileStatesCurrent(pathsWant)
	if err != nil {
		return result, fmt.Errorf("stato NAS non leggibile prima della pubblicazione: %w", err)
	}
	batch := make([]remote.ChunkPublishFile, 0, len(uploads))
	for _, item := range uploads {
		current, ok := before[item.file.Path]
		if !ok || current.Kind != "FILE" || current.Digest != item.file.ExpectedDigest || current.Size != item.file.ExpectedSize {
			result.Stale = append(result.Stale, item.file.Path)
			continue
		}
		batch = append(batch, item.publish)
	}
	if err := adoptOutOfBand(nas, files, before, &result, device, repositoryID, runTS); err != nil {
		return result, err
	}
	if len(batch) > 0 {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		if err := t.client.FilePublishBatch(device, txID, runTS, batch, "replace", false); err != nil {
			return result, err
		}
		for _, file := range batch {
			progress.complete()
			t.emitFile(true, file.Path)
		}
		after, err := nas.FileStatesCurrent(pathsWant)
		if err != nil {
			return result, fmt.Errorf("stato NAS non leggibile dopo la sostituzione: %w", err)
		}
		for _, file := range batch {
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

func (t *Chunks) Download(paths []string) (err error) {
	if len(paths) == 0 {
		return nil
	}
	if err := t.client.ValidateHostKeyPin(context.Background()); err != nil {
		return err
	}
	onProgress, _ := t.observers()
	ctx := t.context()
	progress := newChunkProgress(onProgress, false, len(paths), 0)
	defer func() { progress.finish(err) }()
	for _, relative := range paths {
		progress.setFile(relative)
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
		if err := t.assembleLocal(ctx, relative, info, true, progress.add); err != nil {
			return err
		}
		progress.complete()
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
	return t.assembleLocal(t.context(), relative, info, false, nil)
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
	return t.assembleLocal(t.context(), relative, info, false, nil)
}

func (t *Chunks) assembleLocal(ctx context.Context, relative string, info remote.FileChunks, backup bool, onProgress func(int64)) error {
	local, err := t.localPath(relative)
	if err != nil {
		return err
	}
	if info.Size < 0 {
		return fmt.Errorf("dimensione remota non valida per %s", relative)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(local), 0o755); err != nil {
		return err
	}
	downloadLock, err := acquireDownloadLock(ctx, local)
	if err != nil {
		return err
	}
	defer releaseDownloadLock(downloadLock)
	if matches, err := localFileMatches(local, info); err == nil && matches {
		t.emitFile(false, relative)
		return nil
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
	resumePath := temporary + ".resume"
	resume, resuming := loadDownloadResume(resumePath, info)
	if resuming {
		if stat, err := os.Stat(temporary); err != nil || stat.Size() != resume.Bytes {
			resuming = false
		}
	}
	flags := os.O_CREATE | os.O_WRONLY
	if !resuming {
		flags |= os.O_TRUNC
		resume = downloadResume{Digest: info.Digest, Size: info.Size, MTimeNS: info.MTimeNS}
	}
	file, err := os.OpenFile(temporary, flags, 0o600)
	if err != nil {
		return err
	}
	if resuming {
		if _, err := file.Seek(resume.Bytes, io.SeekStart); err != nil {
			file.Close()
			return err
		}
	}
	whole := sha256.New()
	if resuming && resume.Bytes > 0 {
		prefix, err := os.Open(temporary)
		if err != nil {
			file.Close()
			return err
		}
		_, copyErr := io.CopyN(whole, prefix, resume.Bytes)
		prefix.Close()
		if copyErr != nil {
			file.Close()
			removeDownloadResume(temporary, resumePath)
			return copyErr
		}
	}
	for index := resume.Completed; index < len(info.Chunks); index++ {
		digest := info.Chunks[index]
		data, err := t.client.ChunkGetContextLimited(ctx, digest, t.bandwidthLimit(false))
		if err != nil {
			// Keep the verified prefix and its sidecar. The next pass asks for
			// only the chunks that were not completed.
			file.Close()
			return err
		}
		sum := sha256.Sum256(data)
		if hex.EncodeToString(sum[:]) != digest {
			file.Close()
			removeDownloadResume(temporary, resumePath)
			return fmt.Errorf("chunk corrotto: %s", digest)
		}
		if _, err := file.Write(data); err != nil {
			file.Close()
			return err
		}
		_, _ = whole.Write(data)
		resume.Completed = index + 1
		resume.Bytes += int64(len(data))
		if err := saveDownloadResume(resumePath, resume); err != nil {
			file.Close()
			removeDownloadResume(temporary, resumePath)
			return err
		}
		if onProgress != nil {
			onProgress(int64(len(data)))
		}
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	stat, err := os.Stat(temporary)
	if err != nil || stat.Size() != info.Size {
		removeDownloadResume(temporary, resumePath)
		return fmt.Errorf("dimensione locale non corrisponde per %s", relative)
	}
	if actual := hex.EncodeToString(whole.Sum(nil)); actual != info.Digest {
		removeDownloadResume(temporary, resumePath)
		return fmt.Errorf("digest intero non corrisponde per %s: atteso %s, trovato %s", relative, info.Digest, actual)
	}
	stampTime := time.Unix(0, info.MTimeNS)
	if err := os.Chtimes(temporary, stampTime, stampTime); err != nil {
		removeDownloadResume(temporary, resumePath)
		return err
	}
	if err := os.Rename(temporary, local); err != nil {
		return err
	}
	_ = os.Remove(resumePath)
	_ = os.Remove(resumePath + ".tmp")
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

// NewForConfig returns a backend that follows the capability negotiated by
// Bootstrap. Before the first bootstrap it keeps the configured default; after
// negotiation it can switch to rsync without recreating the GUI or runner.
func NewForConfig(cfg *config.Config, client *remote.Client) Backend {
	return &autoBackend{cfg: cfg, client: client}
}

type autoBackend struct {
	cfg      *config.Config
	client   *remote.Client
	mu       sync.Mutex
	current  Backend
	kind     string
	ctx      context.Context
	progress ProgressHandler
	files    FileHandler
}

func (a *autoBackend) selected() Backend {
	kind := strings.ToLower(strings.TrimSpace(a.cfg.String("effective_transfer_backend")))
	if kind == "" {
		kind = strings.ToLower(strings.TrimSpace(a.cfg.String("transfer_backend")))
	}
	if kind == "chunks" && a.cfg.String("effective_transfer_backend") == "" &&
		!a.cfg.Bool("remote_chunk_transfer_available") {
		// Commands such as browse-download do not run Bootstrap first. Keep
		// them safe against an old NAS until capability negotiation succeeds.
		kind = "rsync"
	}
	if kind != "chunks" {
		kind = "rsync"
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.current != nil && a.kind == kind {
		return a.current
	}
	var next Backend
	if kind == "chunks" {
		next = NewChunks(a.cfg, a.client)
	} else {
		next = New(a.cfg, a.client)
	}
	if a.ctx != nil {
		next.SetContext(a.ctx)
	}
	next.SetObservers(a.progress, a.files)
	a.current = next
	a.kind = kind
	return next
}

func (a *autoBackend) Download(paths []string) error {
	return a.selected().Download(paths)
}

func (a *autoBackend) DownloadPath(relative string) error {
	return a.selected().DownloadPath(relative)
}

func (a *autoBackend) DownloadHistoryVersion(trashRel, relative string) error {
	return a.selected().DownloadHistoryVersion(trashRel, relative)
}

func (a *autoBackend) UploadStaged(paths []string, device string, files []remote.StagedFile) error {
	return a.selected().UploadStaged(paths, device, files)
}

func (a *autoBackend) UploadReplace(files []remote.ReplaceFile, device, repositoryID, runTS string) (remote.ReplaceResult, error) {
	return a.selected().UploadReplace(files, device, repositoryID, runTS)
}

func (a *autoBackend) SetContext(ctx context.Context) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.ctx = ctx
	if a.current != nil {
		a.current.SetContext(ctx)
	}
}

func (a *autoBackend) SetObservers(progress ProgressHandler, files FileHandler) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.progress = progress
	a.files = files
	if a.current != nil {
		a.current.SetObservers(progress, files)
	}
}

func (a *autoBackend) HintSizes(sizes map[string]int64) {
	current := a.selected()
	if hinter, ok := current.(interface{ HintSizes(map[string]int64) }); ok {
		hinter.HintSizes(sizes)
	}
}
