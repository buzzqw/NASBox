package remote

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"nasbox-go/internal/config"
)

// Client runs commands on the NAS through SSH. It deliberately uses the same
// server script and the same NUL protocols as the Python client.
type Client struct {
	cfg  *config.Config
	Host string
	User string
}

func New(cfg *config.Config) *Client {
	return &Client{cfg: cfg, Host: cfg.String("nas_lan"), User: cfg.String("nas_user")}
}

// shellQuote protects a single argument for a POSIX shell on the NAS.
func shellQuote(value string) string {
	if value != "" && !strings.ContainsAny(value, " \t\n'\"\\$`;&|<>*?[]{}()#!") {
		return value
	}
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}

func (c *Client) sshBase() []string {
	args := []string{"-o", "BatchMode=yes", "-o", "ConnectTimeout=10"}
	if port := c.cfg.Int("ssh_port"); port > 0 && port != 22 {
		args = append(args, "-p", fmt.Sprint(port))
	}
	return append(args, c.User+"@"+c.Host)
}

// Run executes `script args...` on the NAS and returns raw stdout.
func (c *Client) Run(args []string, stdin []byte, timeout time.Duration) ([]byte, error) {
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	quoted := make([]string, 0, len(args))
	for _, arg := range args {
		quoted = append(quoted, shellQuote(arg))
	}
	command := append(c.sshBase(), strings.Join(quoted, " "))
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "ssh", command...)
	cmd.Stdin = bytes.NewReader(stdin)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return stdout.Bytes(), fmt.Errorf("ssh: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return stdout.Bytes(), nil
}

// RunLocked runs a mutating command while holding the global NAS lease and
// writing the owner file expected by the server script.
func (c *Client) RunLocked(args []string, stdin []byte, device string, timeout time.Duration) ([]byte, error) {
	lock := c.cfg.String("server_lock_file_remote")
	owner := c.cfg.String("server_lock_owner_file_remote")
	if lock == "" || owner == "" {
		return nil, fmt.Errorf("lock NAS non configurato")
	}
	ownerLine := fmt.Sprintf("%s|go-client|0|checking|0|0", device)
	inner := fmt.Sprintf(
		"printf '%%s\\n' %s > %s; trap 'rm -f %s' EXIT; exec %s",
		shellQuote(ownerLine), shellQuote(owner), shellQuote(owner),
		strings.Join(func() []string {
			out := make([]string, 0, len(args))
			for _, arg := range args {
				out = append(out, shellQuote(arg))
			}
			return out
		}(), " "),
	)
	wrapped := []string{"flock", "-x", lock, "-c", inner}
	return c.Run(wrapped, stdin, timeout)
}

// ReadRemoteFile returns the raw content of a small remote file (manifest).
func (c *Client) ReadRemoteFile(path string) ([]byte, error) {
	return c.Run([]string{"cat", path}, nil, 30*time.Second)
}

func (c *Client) script() string { return c.cfg.RemoteScript() }

// PrintConfig returns the server's authoritative KEY=VALUE configuration.
func (c *Client) PrintConfig() (map[string]string, error) {
	output, err := c.Run([]string{c.script(), "--print-config"}, nil, 30*time.Second)
	if err != nil {
		return nil, err
	}
	return ParseConfig(output), nil
}

// ManifestExport returns the current (or delta) manifest as raw TSV bytes.
func (c *Client) ManifestExport(since string) ([]byte, error) {
	args := []string{c.script(), "--manifest-export"}
	if since != "" {
		args = append(args, since)
	}
	return c.Run(args, nil, 5*time.Minute)
}

// JournalStatus returns the journal/manifest counters.
func (c *Client) JournalStatus() (map[string]string, error) {
	output, err := c.Run([]string{c.script(), "--journal-status"}, nil, 30*time.Second)
	if err != nil {
		return nil, err
	}
	return ParseConfig(output), nil
}

type CheckedRequest struct {
	Path, Digest string
	MTimeSeconds int64
}

// CheckedDelete deletes only files that still match the observed fingerprint.
func (c *Client) CheckedDelete(
	requests []CheckedRequest, runTS, device string,
) (map[string]string, error) {
	if len(requests) == 0 {
		return map[string]string{}, nil
	}
	fields := [][]byte{
		[]byte("CHECKED_DELETE_V1"), []byte(runTS), []byte(device),
		[]byte(fmt.Sprint(len(requests))),
	}
	for _, item := range requests {
		fields = append(fields, []byte(item.Path), []byte(item.Digest), []byte(fmt.Sprint(item.MTimeSeconds)))
	}
	payload := nulJoin(fields)
	output, err := c.RunLocked(
		[]string{c.script(), "--checked-delete"}, payload, device, 5*time.Minute,
	)
	if err != nil {
		return nil, err
	}
	return parsePairedStatus(output, "CHECKED_DELETE_V1")
}

type RestoreRequest struct {
	Path, VersionTS string
}

// RestoreBatch restores files from the NAS trash under the global lease.
func (c *Client) RestoreBatch(requests []RestoreRequest, device string) (map[string]string, error) {
	if len(requests) == 0 {
		return map[string]string{}, nil
	}
	fields := [][]byte{
		[]byte("RESTORE_BATCH_V1"), []byte(device), []byte(fmt.Sprint(len(requests))),
	}
	for _, item := range requests {
		fields = append(fields, []byte(item.Path), []byte(item.VersionTS))
	}
	output, err := c.RunLocked(
		[]string{c.script(), "--restore-batch"}, nulJoin(fields), device, 5*time.Minute,
	)
	if err != nil {
		return nil, err
	}
	return parsePairedStatus(output, "RESTORE_BATCH_V1")
}

func nulJoin(fields [][]byte) []byte {
	var out bytes.Buffer
	for _, field := range fields {
		out.Write(field)
		out.WriteByte(0)
	}
	return out.Bytes()
}

func parsePairedStatus(output []byte, magic string) (map[string]string, error) {
	parts := bytes.Split(output, []byte{0})
	if len(parts) > 0 && len(parts[len(parts)-1]) == 0 {
		parts = parts[:len(parts)-1]
	}
	if len(parts) < 2 || string(parts[0]) != magic {
		return nil, fmt.Errorf("risposta NAS non valida: %s", magic)
	}
	count := 0
	if _, err := fmt.Sscan(string(parts[1]), &count); err != nil || len(parts) != 2+count*2 {
		return nil, fmt.Errorf("risposta NAS incompleta: %s", magic)
	}
	result := make(map[string]string, count)
	for i := 0; i < count; i++ {
		result[string(parts[2+i*2])] = string(parts[3+i*2])
	}
	return result, nil
}
