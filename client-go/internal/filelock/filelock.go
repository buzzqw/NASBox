// Package filelock is the small portability layer for advisory file locks used
// by the client (config, log, single-instance guard). On Unix it is flock(2); on
// Windows it is LockFileEx. Both are released when the file is closed or the
// process dies, which is what the callers rely on.
package filelock

import "errors"

// ErrLocked is returned by a non-blocking Lock when another holder has the lock.
var ErrLocked = errors.New("file locked by another process")
