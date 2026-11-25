package remote

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestAuthorizeScriptAddsTheKeyOnceAndFixesPermissions(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh")
	}
	home := t.TempDir()
	if err := os.Chmod(home, 0o777); err != nil {
		t.Fatal(err)
	}
	key := "ssh-ed25519 AAAATESTKEY nasbox@test"
	run := func() {
		cmd := exec.Command("sh", "-c", authorizeScript)
		cmd.Env = append(os.Environ(), "HOME="+home)
		cmd.Stdin = strings.NewReader(key + "\n")
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("script: %v %s", err, output)
		}
	}
	run()
	run() // idempotent
	data, err := os.ReadFile(filepath.Join(home, ".ssh", "authorized_keys"))
	if err != nil || strings.Count(string(data), key) != 1 {
		t.Fatalf("authorized_keys: %q %v", data, err)
	}
	info, _ := os.Stat(filepath.Join(home, ".ssh", "authorized_keys"))
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("authorized_keys perm %v", info.Mode().Perm())
	}
	home1, _ := os.Stat(home)
	if home1.Mode().Perm()&0o022 != 0 {
		t.Fatalf("home must not stay group/other writable: %v", home1.Mode().Perm())
	}
}

func TestNeedsKeySetupAndQuoting(t *testing.T) {
	if !NeedsKeySetup("ssh: exit status 255: andres@nas: Permission denied (publickey,password).") || NeedsKeySetup("Connection refused") {
		t.Fatal("NeedsKeySetup")
	}
	if got := shellSingleQuote("a'b"); got != `'a'\''b'` {
		t.Fatalf("quote: %s", got)
	}
}
