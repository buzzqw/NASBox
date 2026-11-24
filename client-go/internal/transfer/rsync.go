// Package transfer wraps rsync for the Go client. It reuses rsync's own delta
// algorithm (rolling checksum) instead of building a block index on the NAS.
package transfer

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"nasbox-go/internal/config"
	"nasbox-go/internal/paths"
	"nasbox-go/internal/remote"
)

// runRsync is the single rsync spawn point. Tests replace it to capture the
// exact argv (synthetic commands) without starting a real transfer.
var runRsync = func(ctx context.Context, stdin []byte, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "rsync", args...)
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return stderr.Bytes(), fmt.Errorf("rsync: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return nil, nil
}

type Transfer struct {
	cfg    *config.Config
	client *remote.Client
	ctx    context.Context

	// replaceClient overrides the NAS calls of UploadReplace in tests.
	replaceClient replaceRemote
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

// sshArgs returns the rsync -e value from the same SSHBuilder used by server
// commands and updates, so ports, jump, pinning and multiplexing never drift.
func (t *Transfer) sshArgs() string { return t.client.Builder().EArg() }

func (t *Transfer) remoteSpec(path string) string { return t.client.Builder().RemoteSpec(path) }

func (t *Transfer) bandwidthLimit(upload bool) int {
	if upload {
		if limit := t.cfg.Int("bandwidth_upload_kbps"); limit > 0 {
			return limit
		}
	} else if limit := t.cfg.Int("bandwidth_download_kbps"); limit > 0 {
		return limit
	}
	return t.cfg.Int("bandwidth_kbps")
}

func (t *Transfer) shouldCompress() bool {
	switch strings.ToLower(t.cfg.String("compression_mode")) {
	case "on":
		return true
	case "off":
		return false
	default:
		ip := net.ParseIP(t.client.Host)
		return ip == nil || !ip.IsPrivate()
	}
}

// DownloadPath fetches one remote relative path into the same local path.
func (t *Transfer) DownloadPath(relative string) error {
	if relative == "" {
		return fmt.Errorf("percorso remoto vuoto")
	}
	if err := t.client.ValidateHostKeyPin(context.Background()); err != nil {
		return err
	}
	root := t.cfg.LocalRoot()
	local, err := paths.SafeDestination(root, relative)
	if err != nil {
		return err
	}
	// The NAS runs rsync 3.1.2, which rejects --mkpath (rsync >= 3.2.3): the
	// local parent directory is created here instead.
	if err := os.MkdirAll(filepath.Dir(local), 0o755); err != nil {
		return err
	}
	remotePath := strings.TrimSuffix(t.cfg.RemotePrefix(), "/") + "/" + relative
	args := []string{
		"-a", "--partial", "--partial-dir=.sync-partial",
		"--ignore-times", "--protect-args", "-e", t.sshArgs(),
		t.remoteSpec(remotePath), local,
	}
	if limit := t.bandwidthLimit(false); limit > 0 {
		args = append(args[:1], append([]string{"--bwlimit", fmt.Sprint(limit)}, args[1:]...)...)
	}
	if t.shouldCompress() {
		args = append(args, "--compress")
	}
	_, err = runRsync(t.ctx, nil, args...)
	return err
}

// BuildCommand returns the exact rsync argv for a batch (also used by tests).
func (t *Transfer) BuildCommand(paths []string, upload bool) ([]string, []byte) {
	root := t.cfg.LocalRoot()
	remoteSpec := t.remoteSpec(strings.TrimSuffix(t.cfg.RemotePrefix(), "/") + "/")
	args := []string{
		"-a", "--partial", "--partial-dir=.sync-partial", "--fuzzy",
		// No --mkpath: rsync forwards options to the NAS (rsync 3.1.2), which
		// aborts on unknown ones. --files-from implies --relative, so parent
		// directories are created anyway.
		"--delay-updates", "--ignore-times", "--protect-args",
		"--files-from=-", "--from0", "-e", t.sshArgs(),
	}
	if t.shouldCompress() {
		args = append(args, "--compress")
	}
	if limit := t.bandwidthLimit(upload); limit > 0 {
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
	if err := t.client.ValidateHostKeyPin(context.Background()); err != nil {
		return err
	}
	args, payload := t.BuildCommand(paths, upload)
	_, err := runRsync(t.ctx, payload, args...)
	return err
}

func (t *Transfer) Upload(paths []string) error   { return t.run(paths, true) }
func (t *Transfer) Download(paths []string) error { return t.run(paths, false) }

// UploadStaged copies files into a private NAS tree and publishes them in one
// server transaction. The canonical path is never written by rsync directly.
func (t *Transfer) UploadStaged(paths []string, device string, files []remote.StagedFile) error {
	if len(paths) == 0 {
		return nil
	}
	if err := t.client.ValidateHostKeyPin(context.Background()); err != nil {
		return err
	}
	txID := "stage-" + time.Now().UTC().Format("20060102-150405-000000")
	stagingDir, err := t.client.CreateRemoteStaging(txID)
	if err != nil {
		return err
	}
	defer t.client.CleanupRemoteStaging(stagingDir)
	args, payload := t.BuildCommand(paths, true)
	args[len(args)-1] = t.remoteSpec(stagingDir)
	stderr, err := runRsync(t.ctx, payload, args...)
	if err != nil {
		return fmt.Errorf("rsync staging: %w: %s", err, strings.TrimSpace(string(stderr)))
	}
	ok, err := t.client.StagingPublish(stagingDir, txID, device, files)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("publish staging non riuscito")
	}
	return nil
}
