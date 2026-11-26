//go:build !windows

package osx

import (
	"os/exec"
	"runtime"
	"syscall"
)

func openCommand(target string) *exec.Cmd {
	if runtime.GOOS == "darwin" {
		return exec.Command("open", target)
	}
	return exec.Command("xdg-open", target)
}

// Detach makes cmd outlive the current process (its own session).
func Detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}

// FreeBytes returns the bytes available to the user on the volume holding path.
func FreeBytes(path string) (free, total uint64, ok bool) {
	var stat syscall.Statfs_t
	if syscall.Statfs(path, &stat) != nil {
		return 0, 0, false
	}
	return uint64(stat.Bavail) * uint64(stat.Bsize), uint64(stat.Blocks) * uint64(stat.Bsize), true
}

// RelaunchCommand returns a command that, after a short delay (so the current
// process can exit and release its locks), starts binary with args.
func RelaunchCommand(binary string, args ...string) *exec.Cmd {
	return exec.Command("sh", append([]string{"-c", `sleep 2; exec "$0" "$@"`, binary}, args...)...)
}
