// Package osx holds the few operating-system differences the client needs:
// opening a file or folder with the desktop's default program, detaching a
// child process, free disk space and a writability check.
package osx

import (
	"os"
	"os/exec"
	"path/filepath"
)

// Writable reports whether files can be created in dir.
func Writable(dir string) bool {
	file, err := os.CreateTemp(dir, ".nasbox-write-test-*")
	if err != nil {
		return false
	}
	name := file.Name()
	_ = file.Close()
	_ = os.Remove(name)
	return true
}

// OpenCommand returns the command that opens target (file or folder) with the
// default application.
func OpenCommand(target string) *exec.Cmd {
	return openCommand(filepath.Clean(target))
}

// Open starts the default application for target without waiting for it.
func Open(target string) error { return OpenCommand(target).Start() }
