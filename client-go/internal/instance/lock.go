// Package instance prevents two Go clients from running on the same machine.
package instance

import (
	"fmt"
	"nasbox-go/internal/filelock"
	"os"
	"path/filepath"
)

// Acquire returns a release function and keeps the lock file open for the
// process lifetime. A second instance fails immediately.
func Acquire() (func(), error) {
	base := os.Getenv("XDG_STATE_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, err
		}
		base = filepath.Join(home, ".local", "state")
	}
	dir := filepath.Join(base, "sync-daemon")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	// Same lock as the Python client: the two implementations must never write
	// config/SQLite or synchronise the same tree concurrently.
	path := filepath.Join(dir, "client.instance.lock")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := filelock.Lock(file, true, true); err != nil {
		file.Close()
		return nil, fmt.Errorf("un'altra istanza del client Go e' gia' in esecuzione")
	}
	return func() {
		filelock.Unlock(file)
		_ = file.Close()
	}, nil
}
