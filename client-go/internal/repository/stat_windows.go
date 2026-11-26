//go:build windows

package repository

import (
	"os"
	"syscall"
)

// deviceID returns the volume serial number of the drive holding path.
func deviceID(path string, _ os.FileInfo) (uint64, bool) {
	name, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return 0, false
	}
	handle, err := syscall.CreateFile(name, 0, syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE|syscall.FILE_SHARE_DELETE,
		nil, syscall.OPEN_EXISTING, syscall.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		return 0, false
	}
	defer syscall.CloseHandle(handle)
	var data syscall.ByHandleFileInformation
	if err := syscall.GetFileInformationByHandle(handle, &data); err != nil {
		return 0, false
	}
	return uint64(data.VolumeSerialNumber), true
}
