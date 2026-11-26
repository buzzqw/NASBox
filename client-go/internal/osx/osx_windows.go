//go:build windows

package osx

import (
	"os/exec"
	"syscall"
	"unsafe"
)

const (
	createNewProcessGroup = 0x00000200
	detachedProcess       = 0x00000008
)

func openCommand(target string) *exec.Cmd {
	// "start" is a cmd built-in; the empty title keeps a quoted path from being
	// taken as the window title.
	return exec.Command("cmd", "/c", "start", "", target)
}

// Detach makes cmd outlive the current process.
func Detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createNewProcessGroup | detachedProcess}
}

var procGetDiskFreeSpaceEx = syscall.NewLazyDLL("kernel32.dll").NewProc("GetDiskFreeSpaceExW")

// FreeBytes returns the bytes available to the user on the volume holding path.
func FreeBytes(path string) (free, total uint64, ok bool) {
	name, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return 0, 0, false
	}
	var available, totalBytes, totalFree uint64
	r1, _, _ := procGetDiskFreeSpaceEx.Call(uintptr(unsafe.Pointer(name)),
		uintptr(unsafe.Pointer(&available)), uintptr(unsafe.Pointer(&totalBytes)), uintptr(unsafe.Pointer(&totalFree)))
	if r1 == 0 {
		return 0, 0, false
	}
	return available, totalBytes, true
}

// RelaunchCommand returns a command that, after a short delay, starts binary
// with args. "ping -n 3" is the classic portable two-second sleep in cmd.
func RelaunchCommand(binary string, args ...string) *exec.Cmd {
	line := `ping -n 3 127.0.0.1 >nul & start "" "` + binary + `"`
	for _, arg := range args {
		line += ` "` + arg + `"`
	}
	return exec.Command("cmd", "/c", line)
}
