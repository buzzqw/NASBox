package remote

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// This file is what makes the first connection painless: NASBox talks to the
// NAS with an SSH key, and a person should not have to create and copy one by
// hand. The wizard asks for the NAS password once, installs this PC's public
// key, and never stores the password.

// LocalPublicKey returns the path of this PC's public SSH key, creating an
// ed25519 key when there is none.
func LocalPublicKey() (path string, created bool, err error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", false, err
	}
	dir := filepath.Join(home, ".ssh")
	for _, name := range []string{"id_ed25519.pub", "id_ecdsa.pub", "id_rsa.pub"} {
		candidate := filepath.Join(dir, name)
		if _, statErr := os.Stat(candidate); statErr == nil {
			return candidate, false, nil
		}
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", false, err
	}
	private := filepath.Join(dir, "id_ed25519")
	hostname, _ := os.Hostname()
	cmd := exec.Command("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-C", "nasbox@"+hostname, "-f", private)
	if output, runErr := cmd.CombinedOutput(); runErr != nil {
		return "", false, fmt.Errorf("creazione della chiave SSH non riuscita: %s", strings.TrimSpace(string(output)))
	}
	return private + ".pub", true, nil
}

// ErrWrongPassword is returned when the NAS refused the password.
var ErrWrongPassword = errors.New("password non corretta")

// authorizeScript runs on the NAS: it appends the key (read from stdin) to
// authorized_keys once, and tightens the permissions sshd insists on (a home
// folder writable by others makes sshd ignore the key, which is common on
// Synology).
const authorizeScript = `umask 077
read -r key || exit 2
mkdir -p "$HOME/.ssh" || exit 3
touch "$HOME/.ssh/authorized_keys" || exit 3
grep -qxF "$key" "$HOME/.ssh/authorized_keys" || printf '%s\n' "$key" >> "$HOME/.ssh/authorized_keys"
chmod 700 "$HOME/.ssh"
chmod 600 "$HOME/.ssh/authorized_keys"
chmod go-w "$HOME" 2>/dev/null
exit 0`

// InstallKey logs in to the NAS with the password, appends publicKey to the
// user's authorized_keys and checks that key login now works. The password is
// handed to ssh through a throw-away SSH_ASKPASS helper and the environment;
// it never appears in an argument list and is not kept.
func InstallKey(ctx context.Context, host string, port int, user, password, publicKey string) error {
	publicKey = strings.TrimSpace(publicKey)
	if publicKey == "" || strings.ContainsAny(publicKey, "\n\r") {
		return errors.New("chiave pubblica non valida")
	}
	helperDir, err := os.MkdirTemp("", "nasbox-askpass-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(helperDir)
	helper := filepath.Join(helperDir, "askpass.sh")
	if err := os.WriteFile(helper, []byte("#!/bin/sh\nprintf '%s\\n' \"$NASBOX_PASSWORD\"\n"), 0o700); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	target := user + "@" + host
	cmd := exec.CommandContext(ctx, "ssh",
		"-p", strconv.Itoa(port),
		"-o", "BatchMode=no",
		"-o", "PubkeyAuthentication=no",
		"-o", "PreferredAuthentications=password,keyboard-interactive",
		"-o", "NumberOfPasswordPrompts=1",
		"-o", "StrictHostKeyChecking=accept-new",
		"-o", "ConnectTimeout=15",
		target, "sh -c "+shellSingleQuote(authorizeScript),
	)
	cmd.Env = append(os.Environ(),
		"SSH_ASKPASS="+helper, "SSH_ASKPASS_REQUIRE=force", "DISPLAY=:0", "NASBOX_PASSWORD="+password)
	cmd.Stdin = strings.NewReader(publicKey + "\n")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		text := strings.ToLower(stderr.String())
		if strings.Contains(text, "permission denied") {
			return ErrWrongPassword
		}
		return fmt.Errorf("%w: %s", err, cleanSSHStderr(stderr.String()))
	}
	// Prove it: a key login without any password must work now.
	check := exec.CommandContext(ctx, "ssh",
		"-p", strconv.Itoa(port), "-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=accept-new",
		"-o", "ConnectTimeout=15", target, "true")
	if output, err := check.CombinedOutput(); err != nil {
		return fmt.Errorf("la chiave è stata copiata ma il NAS non la accetta ancora (controlla che l'accesso SSH con chiave sia consentito): %s",
			cleanSSHStderr(string(output)))
	}
	return nil
}

func shellSingleQuote(text string) string {
	return "'" + strings.ReplaceAll(text, "'", `'\''`) + "'"
}

// KeyLoginWorks reports whether this PC can already log in to the NAS with a
// key (no password): when it can, the wizard skips the key step entirely.
func KeyLoginWorks(ctx context.Context, host string, port int, user string) bool {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "ssh", "-p", strconv.Itoa(port), "-o", "BatchMode=yes",
		"-o", "StrictHostKeyChecking=accept-new", "-o", "ConnectTimeout=10", user+"@"+host, "true")
	return cmd.Run() == nil
}

// NeedsKeySetup tells an authentication failure (the NAS answered but does
// not know this PC's key) from "the NAS is not reachable".
func NeedsKeySetup(detail string) bool {
	low := strings.ToLower(detail)
	return strings.Contains(low, "permission denied") || strings.Contains(low, "publickey")
}
