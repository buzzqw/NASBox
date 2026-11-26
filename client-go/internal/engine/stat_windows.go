//go:build windows

package engine

import "os"

// Windows has no change time without opening the file: size and mtime only.
func statOf(info os.FileInfo) fileStat {
	return fileStat{size: info.Size(), mtimeNS: info.ModTime().UnixNano()}
}
