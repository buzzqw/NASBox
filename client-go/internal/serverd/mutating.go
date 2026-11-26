package serverd

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// cmdInitRepository creates the persistent repository marker, with the same
// format as the bash daemon. It is needed so the E2E harness can boot the Go
// daemon on a fresh share.
func cmdInitRepository(ctx *Context) error {
	root, err := filepath.Abs(ctx.ShareRoot)
	if err != nil {
		return err
	}
	if root == "/" {
		fmt.Fprintln(os.Stderr, "SHARE_ROOT non puo' essere la radice del filesystem (/).")
		return exitCode(1)
	}
	marker := filepath.Join(ctx.ShareRoot, ".nasbox-root")
	if id := repositoryID(ctx.ShareRoot); id != "" {
		fmt.Printf("Repository NASBox già inizializzato (ID %s).\n", id)
		return nil
	}
	if _, err := os.Lstat(marker); err == nil {
		fmt.Fprintln(os.Stderr, "Marker repository non riconosciuto o corrotto: "+marker)
		return exitCode(1)
	}
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return err
	}
	id := hex.EncodeToString(buf)
	tmp := marker + ".tmp." + fmt.Sprint(os.Getpid())
	if err := os.WriteFile(tmp, []byte("NASBOX_REPOSITORY_V1\nID="+id+"\n"), 0o644); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, marker); err != nil {
		os.Remove(tmp)
		return err
	}
	fmt.Printf("Repository NASBox inizializzato (ID %s).\n", id)
	return nil
}

// requireOwner verifies the lock owner written by the client's RunLocked
// wrapper. Mutating verbs must never proceed when the lease belongs to another
// device. The bash daemon uses the same first field of the owner file.
func (c *Context) requireOwner(device string) error {
	data, err := os.ReadFile(c.paths().Owner)
	if err != nil {
		return fmt.Errorf("lease non attivo: %w", err)
	}
	fields := strings.Split(strings.TrimSpace(string(data)), "|")
	if len(fields) == 0 || fields[0] == "" {
		return fmt.Errorf("lease senza proprietario")
	}
	if fields[0] != device {
		return fmt.Errorf("lease di un altro dispositivo: %s", fields[0])
	}
	return nil
}
