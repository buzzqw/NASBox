//go:build !windows

package filelock

import (
	"os"
	"syscall"
)

// Lock takes an exclusive or shared lock on f. With nonblock it returns
// ErrLocked instead of waiting.
func Lock(f *os.File, exclusive, nonblock bool) error {
	how := syscall.LOCK_SH
	if exclusive {
		how = syscall.LOCK_EX
	}
	if nonblock {
		how |= syscall.LOCK_NB
	}
	for {
		err := syscall.Flock(int(f.Fd()), how)
		if err == syscall.EINTR {
			continue
		}
		if err == syscall.EWOULDBLOCK || err == syscall.EAGAIN {
			return ErrLocked
		}
		return err
	}
}

// Unlock releases the lock taken by Lock.
func Unlock(f *os.File) {
	for {
		if err := syscall.Flock(int(f.Fd()), syscall.LOCK_UN); err != syscall.EINTR {
			return
		}
	}
}
