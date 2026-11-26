//go:build windows

package filelock

import (
	"os"
	"syscall"
	"unsafe"
)

var (
	kernel32         = syscall.NewLazyDLL("kernel32.dll")
	procLockFileEx   = kernel32.NewProc("LockFileEx")
	procUnlockFileEx = kernel32.NewProc("UnlockFileEx")
)

const (
	lockfileFailImmediately = 0x1
	lockfileExclusiveLock   = 0x2
	errorLockViolation      = syscall.Errno(33)
)

// Lock takes an exclusive or shared lock on the first byte of f.
func Lock(f *os.File, exclusive, nonblock bool) error {
	var flags uint32
	if exclusive {
		flags |= lockfileExclusiveLock
	}
	if nonblock {
		flags |= lockfileFailImmediately
	}
	var overlapped syscall.Overlapped
	r1, _, err := procLockFileEx.Call(f.Fd(), uintptr(flags), 0, 1, 0, uintptr(unsafe.Pointer(&overlapped)))
	if r1 == 0 {
		if err == errorLockViolation {
			return ErrLocked
		}
		return err
	}
	return nil
}

// Unlock releases the lock taken by Lock.
func Unlock(f *os.File) {
	var overlapped syscall.Overlapped
	_, _, _ = procUnlockFileEx.Call(f.Fd(), 0, 1, 0, uintptr(unsafe.Pointer(&overlapped)))
}
