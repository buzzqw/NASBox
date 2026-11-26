package transfer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"nasbox-go/internal/remote"
)

type downloadResume struct {
	Digest    string `json:"digest"`
	Size      int64  `json:"size"`
	MTimeNS   int64  `json:"mtime_ns"`
	Completed int    `json:"completed"`
	Bytes     int64  `json:"bytes"`
}

func loadDownloadResume(path string, info remote.FileChunks) (downloadResume, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return downloadResume{}, false
	}
	var state downloadResume
	if err := json.Unmarshal(data, &state); err != nil {
		return downloadResume{}, false
	}
	if state.Digest != info.Digest || state.Size != info.Size || state.MTimeNS != info.MTimeNS ||
		state.Completed < 0 || state.Completed > len(info.Chunks) || state.Bytes < 0 || state.Bytes > info.Size {
		return downloadResume{}, false
	}
	return state, true
}

func saveDownloadResume(path string, state downloadResume) error {
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	if err := syncFile(tmp); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return syncDir(filepath.Dir(path))
}

func syncFile(path string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	return file.Sync()
}

func syncDir(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func removeDownloadResume(temporary, resume string) {
	_ = os.Remove(temporary)
	_ = os.Remove(resume)
	_ = os.Remove(resume + ".tmp")
}

func localFileMatches(path string, info remote.FileChunks) (bool, error) {
	stat, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	if !stat.Mode().IsRegular() || stat.Mode()&os.ModeSymlink != 0 || stat.Size() != info.Size {
		return false, nil
	}
	file, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer file.Close()
	digest, err := hashReader(file)
	if err != nil {
		return false, err
	}
	return digest == info.Digest, nil
}

func hashReader(file *os.File) (string, error) {
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func acquireDownloadLock(ctx context.Context, local string) (*os.File, error) {
	base := os.Getenv("XDG_STATE_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, err
		}
		base = filepath.Join(home, ".local", "state")
	}
	dir := filepath.Join(base, "sync-daemon", "download-locks")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	sum := sha256.Sum256([]byte(local))
	file, err := os.OpenFile(filepath.Join(dir, hex.EncodeToString(sum[:])+".lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	for {
		err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return file, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN) {
			file.Close()
			return nil, err
		}
		timer := time.NewTimer(50 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			file.Close()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

func releaseDownloadLock(file *os.File) {
	if file == nil {
		return
	}
	_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
	_ = file.Close()
}
