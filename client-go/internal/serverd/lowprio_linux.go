//go:build linux

package serverd

import (
	"runtime"
	"syscall"
)

// withLowPriority runs fn with a positive nice value on the calling OS thread
// and restores the original priority afterwards. Nice is per-thread on Linux,
// so the goroutine must stay on that thread for the whole operation. Only the
// maintenance work is slowed down: the web UI and client verbs keep running at
// their normal priority.
func withLowPriority[T any](fn func() (T, error), nice int) (T, error) {
	if nice < 1 {
		return fn()
	}
	if nice > 19 {
		nice = 19
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	var result T
	var err error
	if _, _, errno := syscall.Syscall(syscall.SYS_SETPRIORITY,
		uintptr(syscall.PRIO_PROCESS), uintptr(0), uintptr(nice)); errno == 0 {
		defer syscall.Syscall(syscall.SYS_SETPRIORITY,
			uintptr(syscall.PRIO_PROCESS), uintptr(0), uintptr(0))
	}
	result, err = fn()
	return result, err
}
