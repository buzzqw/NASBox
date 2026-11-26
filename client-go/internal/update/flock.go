package update

import (
	"os"
	"path/filepath"

	"nasbox-go/internal/filelock"
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
	if err := filelock.Lock(file, true, false); err != nil {
		return err
	}
	defer filelock.Unlock(file)
	return fn()
}
