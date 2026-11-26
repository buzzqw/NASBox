//go:build !windows

package engine

import (
	"os"
	"syscall"
)

func statOf(info os.FileInfo) fileStat {
	stat := fileStat{size: info.Size(), mtimeNS: info.ModTime().UnixNano()}
	if raw, ok := info.Sys().(*syscall.Stat_t); ok {
		stat.ctimeNS = raw.Ctim.Sec*1e9 + int64(raw.Ctim.Nsec)
	}
	return stat
}
