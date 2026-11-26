//go:build windows

package state

import "os"

// statInfo on Windows uses size and modification time only: there is no inode
// or change time without opening every file. A file whose bytes change without
// changing size and mtime is the (rare) case this cannot see; the periodic full
// verification still catches it.
func statInfo(info os.FileInfo) fileStat {
	return fileStat{size: info.Size(), mtimeNS: info.ModTime().UnixNano()}
}
