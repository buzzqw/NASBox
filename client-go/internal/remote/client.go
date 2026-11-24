package remote

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
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

// AcquireLease holds the NAS global lock for up to one hour via a long-running
// SSH command. The returned function releases it. This is used by operations
// (mirrors, browse) that span several local commands.
func (c *Client) AcquireLease(device string) (func(), error) {
	lock := c.cfg.String("server_lock_file_remote")
	owner := c.cfg.String("server_lock_owner_file_remote")
	if lock == "" || owner == "" {
		return nil, fmt.Errorf("lock NAS non configurato")
	}
	ownerLine := fmt.Sprintf("%s|go-client|0|maintenance|0|0", device)
	inner := fmt.Sprintf(
		"printf '%%s\\n' %s > %s; trap 'rm -f %s' EXIT; sleep 3600",
		shellQuote(ownerLine), shellQuote(owner), shellQuote(owner),
	)
	command := append(c.sshBase(), "flock", "-x", shellQuote(lock), "-c", shellQuote(inner))
	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, "ssh", command...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		cancel()
		return nil, err
	}
	time.Sleep(400 * time.Millisecond)
	if ctx.Err() != nil {
		return nil, fmt.Errorf("lease NAS non acquisito: %s", stderr.String())
	}
	return func() { cancel(); _ = cmd.Wait() }, nil
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

// RunTimestamp returns the NASBox run timestamp used by mutating protocols.
func RunTimestamp() string {
	now := time.Now().UTC()
	return now.Format("2006-01-02") + "--" +
		strings.Replace(now.Format("15-04-05.000000"), ".", "-", 1) + "Z"
}

type BrowseEntry struct {
	Name, Kind  string
	Size, MTime int64
}

func (c *Client) BrowseList(relative string) ([]BrowseEntry, error) {
	output, err := c.Run([]string{c.script(), "--browse-list", relative}, nil, 60*time.Second)
	if err != nil {
		return nil, err
	}
	parts := bytes.Split(output, []byte{0})
	if len(parts) > 0 && len(parts[len(parts)-1]) == 0 {
		parts = parts[:len(parts)-1]
	}
	if len(parts) < 2 || string(parts[0]) != "BROWSE_LIST_V1" {
		return nil, fmt.Errorf("risposta browse non valida")
	}
	count := 0
	if _, err := fmt.Sscan(string(parts[1]), &count); err != nil || len(parts) != 2+count*4 {
		return nil, fmt.Errorf("risposta browse incompleta")
	}
	entries := make([]BrowseEntry, 0, count)
	for i := 0; i < count; i++ {
		entry := BrowseEntry{Name: string(parts[2+i*4]), Kind: string(parts[3+i*4])}
		entry.Size, _ = strconv.ParseInt(string(parts[4+i*4]), 10, 64)
		entry.MTime, _ = strconv.ParseInt(string(parts[5+i*4]), 10, 64)
		entries = append(entries, entry)
	}
	return entries, nil
}

func (c *Client) BrowseDelete(relative, runTS, device string) (string, error) {
	payload := nulJoin([][]byte{
		[]byte("BROWSE_DELETE_V1"), []byte(runTS), []byte(device), []byte(relative),
	})
	output, err := c.RunLocked([]string{c.script(), "--browse-delete"}, payload, device, 3*time.Minute)
	if err != nil {
		return "", err
	}
	parts := bytes.Split(output, []byte{0})
	if len(parts) >= 2 && string(parts[0]) == "BROWSE_DELETE_V1" {
		return string(parts[1]), nil
	}
	return "", fmt.Errorf("risposta browse-delete non valida")
}

func (c *Client) BrowseRename(src, dst, runTS, device string) (string, error) {
	payload := nulJoin([][]byte{
		[]byte("BROWSE_RENAME_V1"), []byte(runTS), []byte(device),
		[]byte(src), []byte(dst),
	})
	output, err := c.RunLocked([]string{c.script(), "--browse-rename"}, payload, device, 3*time.Minute)
	if err != nil {
		return "", err
	}
	parts := bytes.Split(output, []byte{0})
	if len(parts) >= 2 {
		return string(parts[1]), nil
	}
	return "", fmt.Errorf("risposta browse-rename non valida")
}

func (c *Client) Metrics() (map[string]string, error) {
	output, err := c.Run([]string{c.script(), "--metrics"}, nil, 30*time.Second)
	if err != nil {
		return nil, err
	}
	return ParseConfig(output), nil
}

func (c *Client) Devices() ([]string, error) {
	output, err := c.Run([]string{c.script(), "--devices"}, nil, 30*time.Second)
	if err != nil {
		return nil, err
	}
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	result := make([]string, 0, len(lines))
	for _, line := range lines {
		if line == "" || strings.HasPrefix(line, "NASBOX_DEVICES_") {
			continue
		}
		result = append(result, line)
	}
	return result, nil
}

// RetryPendingJournal replays a journal payload that was saved locally before
// a failed append. The local file is removed only after the NAS acknowledges.
func (c *Client) RetryPendingJournal(device string) (bool, string, error) {
	base := os.Getenv("XDG_STATE_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return false, "", err
		}
		base = filepath.Join(home, ".local", "state")
	}
	path := filepath.Join(base, "sync-daemon", "journal-pending.bin")
	content, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return true, "", nil
		}
		return false, "", err
	}
	output, err := c.RunLocked([]string{c.script(), "--journal-append"}, content, device, 2*time.Minute)
	if err != nil {
		detail := strings.ToLower(err.Error())
		if strings.Contains(detail, "unknown option") || strings.Contains(detail, "opzione sconosciuta") {
			_ = os.Remove(path)
			return true, "journal non supportato", nil
		}
		return false, "", err
	}
	_ = os.Remove(path)
	return true, strings.TrimSpace(string(output)), nil
}

type StagedFile struct {
	Path, Digest, Causal string
	Size, MTimeNS        int64
}

// StagingPublish publishes staged files atomically and bumps the manifest.
func (c *Client) StagingPublish(
	stagingDir, txID, device string, files []StagedFile,
) (bool, error) {
	if len(files) == 0 {
		return true, nil
	}
	fields := [][]byte{
		[]byte("STAGING_PUBLISH_V2"), []byte(stagingDir), []byte(txID),
		[]byte(device), []byte(fmt.Sprint(len(files))),
	}
	for _, file := range files {
		fields = append(fields,
			[]byte(file.Path), []byte(file.Digest),
			[]byte(fmt.Sprint(file.Size)), []byte(fmt.Sprint(file.MTimeNS)),
			[]byte(file.Causal),
		)
	}
	_, err := c.RunLocked(
		[]string{c.script(), "--staging-publish"}, nulJoin(fields), device, 5*time.Minute,
	)
	return err == nil, err
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
