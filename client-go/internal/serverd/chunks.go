package serverd

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// ChunkStore is a content-addressed object store. Objects are immutable and
// written atomically, so an interrupted snapshot never leaves a half object
// behind. This is the storage primitive that will replace full-file copies in
// .sync-trash once the client transfer layer follows.
type ChunkStore struct {
	root string
}

func NewChunkStore(root string) (*ChunkStore, error) {
	for _, dir := range []string{
		filepath.Join(root, "objects"),
		filepath.Join(root, "snapshots"),
		filepath.Join(root, "tmp"),
	} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, err
		}
	}
	return &ChunkStore{root: root}, nil
}

func (s *ChunkStore) objectPath(digest string) string {
	return filepath.Join(s.root, "objects", digest[:2], digest[2:])
}

// Has reports whether a chunk is already stored.
func (s *ChunkStore) Has(digest string) bool {
	info, err := os.Stat(s.objectPath(digest))
	return err == nil && info.Mode().IsRegular()
}

// Put stores bytes under their SHA-256. An existing object is left untouched.
func (s *ChunkStore) Put(data []byte) (string, error) {
	sum := sha256.Sum256(data)
	digest := hex.EncodeToString(sum[:])
	if s.Has(digest) {
		return digest, nil
	}
	tmp, err := os.CreateTemp(filepath.Join(s.root, "tmp"), "chunk-*")
	if err != nil {
		return "", err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return "", err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	path := s.objectPath(digest)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", err
	}
	if err := os.Rename(name, path); err != nil && !errors.Is(err, os.ErrExist) {
		return "", err
	}
	return digest, nil
}

// Open returns a reader and the stored size for a chunk.
func (s *ChunkStore) Open(digest string) (*os.File, int64, error) {
	file, err := os.Open(s.objectPath(digest))
	if err != nil {
		return nil, 0, err
	}
	info, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, 0, err
	}
	return file, info.Size(), nil
}

// StoreFile splits a file into fixed-size chunks, stores new ones and returns
// the whole-file digest, its size and the ordered chunk digests.
func (s *ChunkStore) StoreFile(path string, chunkSize int) (string, int64, []string, error) {
	if chunkSize <= 0 {
		chunkSize = 1024 * 1024
	}
	file, err := os.Open(path)
	if err != nil {
		return "", 0, nil, err
	}
	defer file.Close()
	whole := sha256.New()
	chunks := []string{}
	buffer := make([]byte, chunkSize)
	for {
		n, err := io.ReadFull(file, buffer)
		if n > 0 {
			digest, putErr := s.Put(buffer[:n])
			if putErr != nil {
				return "", 0, nil, putErr
			}
			chunks = append(chunks, digest)
			if _, err := whole.Write(buffer[:n]); err != nil {
				return "", 0, nil, err
			}
		}
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			break
		}
		if err != nil {
			return "", 0, nil, err
		}
	}
	info, err := file.Stat()
	if err != nil {
		return "", 0, nil, err
	}
	return hex.EncodeToString(whole.Sum(nil)), info.Size(), chunks, nil
}

// Assemble writes chunks to a temporary file and atomically renames it.
func (s *ChunkStore) Assemble(chunks []string, target string) error {
	tmp, err := os.CreateTemp(filepath.Dir(target), ".nasbox-assemble-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	for _, digest := range chunks {
		file, size, err := s.Open(digest)
		if err != nil {
			tmp.Close()
			return fmt.Errorf("chunk %s: %w", digest, err)
		}
		written, err := io.CopyN(tmp, file, size)
		file.Close()
		if err != nil || written != size {
			tmp.Close()
			return fmt.Errorf("chunk %s incompleto", digest)
		}
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, target)
}

type SnapshotFile struct {
	Path    string   `json:"path"`
	Digest  string   `json:"digest"`
	Size    int64    `json:"size"`
	MTimeNS int64    `json:"mtime_ns"`
	Chunks  []string `json:"chunks"`
}

type Snapshot struct {
	ID        string         `json:"id"`
	CreatedAt int64          `json:"created_at"`
	Device    string         `json:"device"`
	Files     []SnapshotFile `json:"files"`
}

func (s *ChunkStore) snapshotPath(id string) string {
	return filepath.Join(s.root, "snapshots", id+".json")
}

func newSnapshotID() (string, error) {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return fmt.Sprintf("%d-%s", time.Now().Unix(), hex.EncodeToString(buf)), nil
}

// CreateSnapshot stores the listed files as chunks and writes a snapshot.
func (s *ChunkStore) CreateSnapshot(root string, paths []string, device string) (*Snapshot, error) {
	id, err := newSnapshotID()
	if err != nil {
		return nil, err
	}
	snap := &Snapshot{ID: id, CreatedAt: time.Now().Unix(), Device: device, Files: []SnapshotFile{}}
	for _, relative := range paths {
		if !validRelative(relative) {
			return nil, fmt.Errorf("percorso non valido: %q", relative)
		}
		full := filepath.Join(root, relative)
		info, err := os.Lstat(full)
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		digest, size, chunks, err := s.StoreFile(full, 1024*1024)
		if err != nil {
			return nil, err
		}
		snap.Files = append(snap.Files, SnapshotFile{
			Path: encodeField(relative), Digest: digest, Size: size,
			MTimeNS: info.ModTime().UnixNano(), Chunks: chunks,
		})
	}
	if err := s.SaveSnapshot(snap); err != nil {
		return nil, err
	}
	return snap, nil
}

func (s *ChunkStore) SaveSnapshot(snap *Snapshot) error {
	data, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Join(s.root, "tmp"), "snapshot-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, s.snapshotPath(snap.ID))
}

func (s *ChunkStore) LoadSnapshot(id string) (*Snapshot, error) {
	data, err := os.ReadFile(s.snapshotPath(id))
	if err != nil {
		return nil, err
	}
	snap := &Snapshot{}
	if err := json.Unmarshal(data, snap); err != nil {
		return nil, err
	}
	return snap, nil
}

func (s *ChunkStore) ListSnapshots() ([]string, error) {
	entries, err := os.ReadDir(filepath.Join(s.root, "snapshots"))
	if err != nil {
		return nil, err
	}
	ids := []string{}
	for _, entry := range entries {
		name := entry.Name()
		if filepath.Ext(name) == ".json" {
			ids = append(ids, name[:len(name)-5])
		}
	}
	sort.Strings(ids)
	return ids, nil
}

// RestoreFile materializes one file from a snapshot into target.
func (s *ChunkStore) RestoreFile(snap *Snapshot, relative, target string) error {
	for _, file := range snap.Files {
		if file.Path != encodeField(relative) {
			continue
		}
		return s.Assemble(file.Chunks, target)
	}
	return fmt.Errorf("percorso %q non presente nello snapshot %s", relative, snap.ID)
}

func (s *ChunkStore) Stats() (objects int, bytes int64, err error) {
	objectRoot := filepath.Join(s.root, "objects")
	err = filepath.Walk(objectRoot, func(_ string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if info.Mode().IsRegular() {
			objects++
			bytes += info.Size()
		}
		return nil
	})
	return objects, bytes, err
}

func (c *Context) chunkStore() (*ChunkStore, error) {
	root := value(c.Config, "CHUNK_STORE_DIR", filepath.Join(c.StateDir, "chunks"))
	return NewChunkStore(root)
}

// ChunkSize returns the configured chunk size (default 1 MiB).
func (c *Context) ChunkSize() int {
	raw := strings.Fields(value(c.Config, "CHUNK_SIZE", "1048576"))
	if len(raw) == 0 {
		return 1024 * 1024
	}
	n, err := strconv.Atoi(raw[0])
	if err != nil || n < 4096 {
		return 1024 * 1024
	}
	return n
}
