//go:build !windows

package state

import (
	"os"
	"syscall"
)

// statInfo captures the identity used by the stat/digest cache and by rename
// detection: device, inode and ctime change when a file is replaced or touched.
func statInfo(info os.FileInfo) fileStat {
	raw, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fileStat{size: info.Size(), mtimeNS: info.ModTime().UnixNano()}
	}
	return fileStat{
		dev:     int64(raw.Dev),
		ino:     int64(raw.Ino),
		ctimeNS: raw.Ctim.Sec*1e9 + int64(raw.Ctim.Nsec),
		size:    info.Size(),
		mtimeNS: info.ModTime().UnixNano(),
	}
}
