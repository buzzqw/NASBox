package update

import (
	"os"
	"path/filepath"
	"syscall"
)

// withFileLock serializes state-file writes across processes and goroutines.
// The lock lives on a sibling .lock file so replacing the JSON file itself
// never invalidates the lock.
func withFileLock(path string, fn func() error) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	file, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer file.Close()
	for {
		err = syscall.Flock(int(file.Fd()), syscall.LOCK_EX)
		if err != syscall.EINTR {
			break
		}
	}
	if err != nil {
		return err
	}
	defer func() {
		for {
			err := syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
			if err != syscall.EINTR {
				break
			}
		}
	}()
	return fn()
}
