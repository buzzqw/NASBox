package update

import (
	"errors"
	"os"
	"path/filepath"
)

// repositoryMarker is the file that identifies the root of a NASBox tree
// (local folder or NAS share). Anything below it is synchronized.
const repositoryMarker = ".nasbox-root"

// ErrUnmanagedBinary means the running executable lives inside a synchronized
// NASBox tree (typically a binary started straight from the update channel).
// Such a binary must never self-update: its state and lock files would be
// created inside the synced folder and propagated to every PC.
var ErrUnmanagedBinary = errors.New(
	"questo binario si trova dentro una cartella NASBox sincronizzata: " +
		"installalo in ~/.local/bin/nasbox-go per abilitare gli aggiornamenti automatici")

// InsideSyncedTree reports whether path is below a directory that contains
// the NASBox repository marker.
func InsideSyncedTree(path string) bool {
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		resolved = path
	}
	dir := filepath.Dir(filepath.Clean(resolved))
	for {
		if info, err := os.Lstat(filepath.Join(dir, repositoryMarker)); err == nil && !info.IsDir() {
			return true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return false
		}
		dir = parent
	}
}

// managed is the single gate used by every entry point that reads or writes
// the update state next to the binary.
func managed(binaryPath string) error {
	if InsideSyncedTree(binaryPath) {
		return ErrUnmanagedBinary
	}
	return nil
}
