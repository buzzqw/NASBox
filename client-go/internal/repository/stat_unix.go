//go:build !windows

package repository

import (
	"os"
	"syscall"
)

// deviceID identifies the filesystem a path lives on, so a root that is not
// mounted (and became an empty folder on the system disk) is detected.
func deviceID(path string, info os.FileInfo) (uint64, bool) {
	raw, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	return uint64(raw.Dev), true
}
