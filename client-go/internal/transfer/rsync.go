// Package transfer wraps rsync for the Go client. It reuses rsync's own delta
// algorithm (rolling checksum) instead of building a block index on the NAS.
package transfer

import (
	"bytes"
	"fmt"
	"os/exec"
	"strings"

	"nasbox-go/internal/config"
	"nasbox-go/internal/remote"
)

type Transfer struct {
	cfg    *config.Config
	client *remote.Client
}

func New(cfg *config.Config, client *remote.Client) *Transfer {
	return &Transfer{cfg: cfg, client: client}
}

// BuildCommand returns the exact rsync argv for a batch (also used by tests).
func (t *Transfer) BuildCommand(paths []string, upload bool) ([]string, []byte) {
	root := t.cfg.LocalRoot()
	remoteSpec := fmt.Sprintf("%s@%s:%s/", t.client.User, t.client.Host, t.cfg.RemotePrefix())
	sshArgs := "-o BatchMode=yes -o ConnectTimeout=10"
	if port := t.cfg.Int("ssh_port"); port > 0 && port != 22 {
		sshArgs += fmt.Sprintf(" -p %d", port)
	}
	args := []string{
		"-a", "--partial", "--fuzzy", "--delay-updates", "--mkpath",
		"--files-from=-", "--from0", "-e", "ssh " + sshArgs,
	}
	if t.cfg.String("compression_mode") == "on" {
		args = append(args, "--compress")
	}
	source, destination := remoteSpec, root
	if upload {
		source, destination = root, remoteSpec
	}
	args = append(args, source, destination)
	var payload bytes.Buffer
	for _, path := range paths {
		payload.WriteString(path)
		payload.WriteByte(0)
	}
	return args, payload.Bytes()
}

func (t *Transfer) run(paths []string, upload bool) error {
	if len(paths) == 0 {
		return nil
	}
	args, payload := t.BuildCommand(paths, upload)
	cmd := exec.Command("rsync", args...)
	cmd.Stdin = bytes.NewReader(payload)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("rsync: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

func (t *Transfer) Upload(paths []string) error   { return t.run(paths, true) }
func (t *Transfer) Download(paths []string) error { return t.run(paths, false) }
