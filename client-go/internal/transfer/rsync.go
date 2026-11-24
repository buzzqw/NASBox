// Package transfer wraps rsync for the Go client. It reuses rsync's own delta
// algorithm (rolling checksum) instead of building a block index on the NAS.
package transfer

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"

	"nasbox-go/internal/config"
	"nasbox-go/internal/paths"
	"nasbox-go/internal/remote"
)

type Transfer struct {
	cfg    *config.Config
	client *remote.Client
	ctx    context.Context
}

func New(cfg *config.Config, client *remote.Client) *Transfer {
	return &Transfer{cfg: cfg, client: client, ctx: context.Background()}
}

// SetContext makes in-flight transfers cancellable.
func (t *Transfer) SetContext(ctx context.Context) {
	if ctx != nil {
		t.ctx = ctx
	}
}

// DownloadPath fetches one remote relative path into the same local path.
func (t *Transfer) DownloadPath(relative string) error {
	if relative == "" {
		return fmt.Errorf("percorso remoto vuoto")
	}
	root := t.cfg.LocalRoot()
	local, err := paths.SafeDestination(root, relative)
	if err != nil {
		return err
	}
	remoteSpec := fmt.Sprintf("%s@%s:%s/%s", t.client.User, t.client.Host, t.cfg.RemotePrefix(), relative)
	args := []string{"-a", "--partial", "--partial-dir=.sync-partial", "--mkpath", "--ignore-times", "--protect-args", "-e", "ssh -o BatchMode=yes", remoteSpec, local}
	if limit := t.cfg.Int("bandwidth_kbps"); limit > 0 {
		args = append(args[:1], append([]string{"--bwlimit", fmt.Sprint(limit)}, args[1:]...)...)
	}
	cmd := exec.CommandContext(t.ctx, "rsync", args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	return cmd.Run()
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
		"-a", "--partial", "--partial-dir=.sync-partial", "--fuzzy",
		"--delay-updates", "--mkpath", "--ignore-times", "--protect-args",
		"--files-from=-", "--from0", "-e", "ssh " + sshArgs,
	}
	if t.cfg.String("compression_mode") == "on" {
		args = append(args, "--compress")
	}
	if limit := t.cfg.Int("bandwidth_kbps"); limit > 0 {
		args = append(args, "--bwlimit", fmt.Sprint(limit))
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
	cmd := exec.CommandContext(t.ctx, "rsync", args...)
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
