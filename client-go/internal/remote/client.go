package remote

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"nasbox-go/internal/config"
)

// Connection is the route to the NAS, mirroring Python's NasConnection. Host is
// the final destination (normally the LAN address); ViaJump means the bastion
// makes the last hop, so Host is still the NAS's LAN address, not the bastion.
type Connection struct {
	Host    string
	ViaJump bool
}

// Client runs commands on the NAS through SSH. It deliberately uses the same
// server script and the same NUL protocols as the Python client. Every command
// — and every rsync/update invocation built from Builder() — shares SSHBuilder
// so the transport options can never drift apart.
type Client struct {
	cfg  *config.Config
	Host string
	User string
	conn Connection
}

func New(cfg *config.Config) *Client {
	return NewWithConnection(cfg, Connection{Host: cfg.String("nas_lan")})
}

// NewWithConnection constructs a client on an already resolved route. Tests use
// it to build synthetic LAN/WAN/jump commands without touching the network.
func NewWithConnection(cfg *config.Config, conn Connection) *Client {
	return &Client{
		cfg:  cfg,
		Host: conn.Host,
		User: cfg.String("nas_user"),
		conn: conn,
	}
}

// NewResolved probes LAN, WAN and the bastion (in that order) like Python's
// resolve_connection(), then returns a client on the working route.
func NewResolved(cfg *config.Config) (*Client, error) {
	conn, err := ResolveConnection(cfg)
	if err != nil {
		return nil, err
	}
	return NewWithConnection(cfg, conn), nil
}

func (c *Client) SetConnection(conn Connection) {
	c.conn = conn
	c.Host = conn.Host
}

func (c *Client) Connection() Connection { return c.conn }

// Builder returns the shared SSH transport builder for this client's route.
func (c *Client) Builder() *SSHBuilder { return NewSSHBuilder(c.cfg, c.conn) }

// shlexQuote is the Go counterpart of Python's shlex.quote: a value is left
// bare only when it contains exclusively ASCII word characters or @%+=:,./-.
// Everything else (notably spaces, quotes and shell metacharacters) is wrapped
// in single quotes, with embedded single quotes escaped as backslash-quote-quote.
func shlexQuote(value string) string {
	if value == "" {
		return "''"
	}
	safe := true
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') ||
			strings.ContainsRune("_@%+=:,./-", r) {
			continue
		}
		safe = false
		break
	}
	if safe {
		return value
	}
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}

// shellQuote protects a single argument for a POSIX shell on the NAS.
func shellQuote(value string) string { return shlexQuote(value) }

func sshPort(cfg *config.Config) int {
	if port := cfg.Int("ssh_port"); port > 0 {
		return port
	}
	return 22
}

func jumpPort(cfg *config.Config) int {
	if port := cfg.Int("jump_port"); port > 0 {
		return port
	}
	return 22
}

// CheckPort is Python's check_port: a short TCP connect to decide whether a
// candidate route is usable before selecting it.
func CheckPort(host string, port int, timeout time.Duration) bool {
	if host == "" || port <= 0 {
		return false
	}
	conn, err := net.DialTimeout("tcp", net.JoinHostPort(host, strconv.Itoa(port)), timeout)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// probePort is an indirection so tests can supply synthetic reachability.
var probePort = func(host string, port int, timeout time.Duration) bool {
	return CheckPort(host, port, timeout)
}

// ResolveConnection follows Python's priority: direct LAN, then direct WAN,
// then the bastion reaching the NAS on its LAN. The bastion itself is checked
// on jump_port; the final destination is always the NAS LAN address.
func ResolveConnection(cfg *config.Config) (Connection, error) {
	lan := strings.TrimSpace(cfg.String("nas_lan"))
	wan := strings.TrimSpace(cfg.String("nas_wan"))
	jump := strings.TrimSpace(cfg.String("jump_host"))
	port := sshPort(cfg)
	if lan != "" && probePort(lan, port, 2*time.Second) {
		return Connection{Host: lan}, nil
	}
	if wan != "" && probePort(wan, port, 5*time.Second) {
		return Connection{Host: wan}, nil
	}
	if jump != "" && lan != "" && probePort(jump, jumpPort(cfg), 5*time.Second) {
		return Connection{Host: lan, ViaJump: true}, nil
	}
	return Connection{}, fmt.Errorf(
		"NAS non raggiungibile: LAN=%s WAN=%s bastione=%s (porta %d)", lan, wan, jump, port,
	)
}

// multiplexSocketDirFn is replaceable in tests; a real run caches one directory
// per uid, exactly like Python's _multiplex_socket_dir().
var multiplexSocketDirFn = func() string {
	directory := filepath.Join(os.TempDir(), fmt.Sprintf("nasbox-ssh-%d", os.Getuid()))
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return ""
	}
	_ = os.Chmod(directory, 0o700)
	return directory
}

// SSHBuilder is the single source of truth for SSH transport arguments. Server
// commands (Client.Run/AcquireLease), rsync remote specs and the update path
// all derive their options from here, matching Python's ssh_opts/_ssh_e_arg.
type SSHBuilder struct {
	cfg       *config.Config
	conn      Connection
	socketDir string
}

func NewSSHBuilder(cfg *config.Config, conn Connection) *SSHBuilder {
	return &SSHBuilder{cfg: cfg, conn: conn, socketDir: multiplexSocketDirFn()}
}

func (b *SSHBuilder) Connection() Connection { return b.conn }

func (b *SSHBuilder) User() string { return b.cfg.String("nas_user") }

func (b *SSHBuilder) Target() string { return b.User() + "@" + b.conn.Host }

// Options mirrors Python's ssh_opts: explicit port, non-interactive batch mode,
// an explicit StrictHostKeyChecking policy, timeouts, optional ControlMaster
// multiplexing, a dedicated known_hosts file when pinned and optional ProxyJump.
func (b *SSHBuilder) Options() []string {
	strict := "StrictHostKeyChecking=accept-new"
	if b.cfg.Bool("ssh_host_key_pinned") || b.cfg.Bool("ssh_jump_key_pinned") {
		strict = "StrictHostKeyChecking=yes"
	}
	opts := []string{
		"-p", strconv.Itoa(sshPort(b.cfg)),
		"-o", "BatchMode=yes",
		"-o", strict,
		"-o", "ConnectTimeout=15",
		"-o", "ServerAliveInterval=30",
	}
	if b.cfg.Bool("ssh_multiplexing") && b.socketDir != "" {
		opts = append(opts,
			"-o", "ControlMaster=auto",
			"-o", "ControlPath="+b.socketDir+"/cm-%C",
			"-o", "ControlPersist=60",
		)
	}
	if b.cfg.Bool("ssh_host_key_pinned") {
		if knownHosts := strings.TrimSpace(b.cfg.String("ssh_known_hosts")); knownHosts != "" {
			opts = append(opts, "-o", "UserKnownHostsFile="+knownHosts)
		}
	}
	if b.conn.ViaJump {
		jumpUser := strings.TrimSpace(b.cfg.String("jump_user"))
		if jumpUser == "" {
			jumpUser = b.User()
		}
		jumpHost := strings.TrimSpace(b.cfg.String("jump_host"))
		opts = append(opts, "-o", "ProxyJump="+jumpUser+"@"+jumpHost+":"+strconv.Itoa(jumpPort(b.cfg)))
	}
	return opts
}

// Args returns the complete ssh argv, including the binary name.
func (b *SSHBuilder) Args(command string) []string {
	args := append([]string{"ssh"}, b.Options()...)
	return append(args, b.Target(), command)
}

// EArg is the rsync `-e` argument, equivalent to Python's _ssh_e_arg: each
// option is shlex-quoted so no space or metacharacter reaches the local shell
// unescaped when rsync hands the string to ssh.
func (b *SSHBuilder) EArg() string {
	parts := []string{"ssh"}
	for _, option := range b.Options() {
		parts = append(parts, shlexQuote(option))
	}
	return strings.Join(parts, " ")
}

// RemoteSpec builds user@host:path. The path is shlex-quoted exactly like the
// Python client does for rsync sources, so paths with spaces or metacharacters
// are never split; --protect-args keeps the argument intact in transit.
func (b *SSHBuilder) RemoteSpec(path string) string {
	if path == "" {
		return b.Target() + ":"
	}
	return b.Target() + ":" + shlexQuote(path)
}

// runCommand is the only place that spawns a process in this package. Tests
// replace it to capture the exact argv of synthetic SSH/host-key commands.
type commandFunc func(ctx context.Context, stdin []byte, name string, args ...string) (stdout, stderr []byte, err error)

var runCommand commandFunc = func(ctx context.Context, stdin []byte, name string, args ...string) ([]byte, []byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var out, errOut bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errOut
	err := cmd.Run()
	return out.Bytes(), errOut.Bytes(), err
}

// cleanSSHStderr strips OpenSSH advisory banners (lines starting with "**")
// so they cannot bury the real remote error, as in Python's _clean_ssh_stderr.
func cleanSSHStderr(text string) string {
	lines := make([]string, 0, strings.Count(text, "\n")+1)
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "**") {
			continue
		}
		lines = append(lines, line)
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
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
	command := c.Builder().Args(strings.Join(quoted, " "))
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	stdout, stderr, err := runCommand(ctx, stdin, command[0], command[1:]...)
	if err != nil {
		return stdout, fmt.Errorf("ssh: %w: %s", err, cleanSSHStderr(string(stderr)))
	}
	return stdout, nil
}

func parseVersion(value string) []int {
	parts := strings.Split(value, ".")
	out := make([]int, 0, len(parts))
	for _, part := range parts {
		n, _ := strconv.Atoi(part)
		out = append(out, n)
	}
	return out
}

func versionLess(left, right string) bool {
	a, b := parseVersion(left), parseVersion(right)
	for index := 0; index < len(a) || index < len(b); index++ {
		x, y := 0, 0
		if index < len(a) {
			x = a[index]
		}
		if index < len(b) {
			y = b[index]
		}
		if x < y {
			return true
		}
		if x > y {
			return false
		}
	}
	return false
}

// ServerOutdated compares the NAS script version with the minimum required.
func (c *Client) ServerOutdated(minimum string) (bool, string, error) {
	values, err := c.PrintConfig()
	if err != nil {
		return false, "", err
	}
	current := values["VERSION"]
	if current == "" {
		return true, "", fmt.Errorf("versione server sconosciuta")
	}
	return versionLess(current, minimum), current, nil
}

// Bootstrap validates the NAS contract and persists the authoritative paths,
// repository ID and capabilities before any destructive operation.
func (c *Client) Bootstrap() error {
	values, err := c.PrintConfig()
	if err != nil {
		return err
	}
	minimum := c.cfg.String("min_server_version")
	if minimum == "" || versionLess(minimum, "3.25.0") {
		minimum = "3.25.0"
	}
	c.cfg.Set("min_server_version", minimum, true)
	if values["VERSION"] == "" || versionLess(values["VERSION"], minimum) {
		return fmt.Errorf("server NAS non aggiornato: trovato %s, richiesto %s", values["VERSION"], minimum)
	}
	if strings.ToLower(values["REPOSITORY_READY"]) != "true" ||
		strings.ToLower(values["JOURNAL_READY"]) != "true" ||
		strings.ToLower(values["PATH_RECONCILIATION_AVAILABLE"]) != "true" {
		return fmt.Errorf("repository NAS non pronto")
	}
	if values["REPOSITORY_ID"] == "" {
		return fmt.Errorf("ID repository NAS assente")
	}
	remoteRoot := strings.TrimSuffix(c.cfg.RemotePrefix(), "/")
	if serverRoot := strings.TrimSuffix(values["SHARE_ROOT"], "/"); serverRoot != remoteRoot {
		return fmt.Errorf("percorso NAS incoerente: client %s, server %s", remoteRoot, serverRoot)
	}
	c.cfg.Set("repository_id", values["REPOSITORY_ID"], true)
	if values["SYNC_LOCK_FILE"] != "" {
		c.cfg.Set("server_lock_file_remote", values["SYNC_LOCK_FILE"], true)
	}
	if values["SYNC_LOCK_OWNER_FILE"] != "" {
		c.cfg.Set("server_lock_owner_file_remote", values["SYNC_LOCK_OWNER_FILE"], true)
	}
	c.cfg.Set("remote_causal_versions_available",
		strings.ToLower(values["CAUSAL_VERSIONS_AVAILABLE"]) == "true", true)
	c.cfg.Set("remote_change_feed_available",
		strings.ToLower(values["CHANGE_FEED_AVAILABLE"]) == "true", true)
	return nil
}

// RunLocked runs a mutating command while holding the global NAS lease and
// writing the owner file expected by the server script.
func (c *Client) RunLocked(args []string, stdin []byte, device string, timeout time.Duration) ([]byte, error) {
	if err := c.ValidateHostKeyPin(context.Background()); err != nil {
		return nil, err
	}
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
	if err := c.ValidateHostKeyPin(context.Background()); err != nil {
		return nil, err
	}
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
	builder := c.Builder()
	command := append([]string{"ssh"}, builder.Options()...)
	remote := "flock -x " + shellQuote(lock) + " -c " + shellQuote(inner)
	command = append(command, builder.Target(), remote)
	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, command[0], command[1:]...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		cancel()
		return nil, err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case err := <-done:
			cancel()
			return nil, fmt.Errorf("lease NAS non acquisito: %w: %s", err, stderr.String())
		default:
		}
		output, err := c.Run([]string{"cat", owner}, nil, 3*time.Second)
		if err == nil && strings.HasPrefix(strings.TrimSpace(string(output)), device+"|") {
			return func() { cancel(); <-done }, nil
		}
		time.Sleep(150 * time.Millisecond)
	}
	cancel()
	return nil, fmt.Errorf("timeout acquisizione lease NAS: %s", stderr.String())
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
	output, err := c.MetricsOutput()
	if err != nil {
		return nil, err
	}
	return ParseConfig(output), nil
}

// MetricsOutput returns the raw NASBOX_METRICS_V1 payload so the metrics
// package can validate every required counter instead of silently accepting a
// partial map.
func (c *Client) MetricsOutput() ([]byte, error) {
	return c.Run([]string{c.script(), "--metrics"}, nil, 30*time.Second)
}

// RevisionWait is the response of the read-only change feed.
type RevisionWait struct {
	Result   string // CHANGED or TIMEOUT
	Revision int
}

// ParseRevisionWait validates the deliberately small change-feed response.
func ParseRevisionWait(output []byte) (RevisionWait, error) {
	lines := strings.Split(strings.TrimRight(string(output), "\r\n"), "\n")
	if len(lines) != 3 || strings.TrimSpace(lines[0]) != "NASBOX_CHANGE_WAIT_V1" {
		return RevisionWait{}, fmt.Errorf("risposta change feed non valida")
	}
	values := map[string]string{}
	for _, line := range lines[1:] {
		key, value, found := strings.Cut(line, "=")
		if !found || key == "" {
			return RevisionWait{}, fmt.Errorf("risposta change feed non valida")
		}
		if _, exists := values[key]; exists || (key != "RESULT" && key != "REVISION") {
			return RevisionWait{}, fmt.Errorf("risposta change feed non valida")
		}
		values[key] = value
	}
	result, revision := values["RESULT"], values["REVISION"]
	if (result != "CHANGED" && result != "TIMEOUT") || revision == "" {
		return RevisionWait{}, fmt.Errorf("risposta change feed non valida")
	}
	value, err := strconv.ParseInt(revision, 10, 64)
	if err != nil || value < 0 {
		return RevisionWait{}, fmt.Errorf("revisione change feed non valida")
	}
	return RevisionWait{Result: result, Revision: int(value)}, nil
}

// WaitForRevision blocks on the NAS without taking the transfer lock. It lets
// a remote change wake the client immediately instead of waiting for the next
// poll interval, matching the Python client's change feed.
func (c *Client) WaitForRevision(previous, timeout int) (RevisionWait, error) {
	if previous < 0 || timeout < 0 || timeout > 3600 {
		return RevisionWait{}, fmt.Errorf("parametri change feed non validi")
	}
	output, err := c.Run(
		[]string{c.script(), "--wait-for-revision", strconv.Itoa(previous), strconv.Itoa(timeout)},
		nil, time.Duration(timeout+20)*time.Second,
	)
	if err != nil {
		return RevisionWait{}, err
	}
	wait, err := ParseRevisionWait(output)
	if err != nil {
		return RevisionWait{}, err
	}
	if wait.Revision < previous {
		return RevisionWait{}, fmt.Errorf("revisione change feed regressiva")
	}
	if wait.Result == "CHANGED" && wait.Revision == previous {
		return RevisionWait{}, fmt.Errorf("change feed senza avanzamento")
	}
	if wait.Result == "TIMEOUT" && wait.Revision != previous {
		return RevisionWait{}, fmt.Errorf("change feed incoerente")
	}
	return wait, nil
}

// DiscoverServerScripts finds running sync-daemon-server.sh instances on the
// NAS. BusyBox grep variants differ, so the scrape uses ps+awk like Python.
func (c *Client) DiscoverServerScripts() ([]string, error) {
	command := `ps aux 2>/dev/null | awk '{ for (i = 1; i <= NF; i++) if ($i ~ /sync-daemon-server[.]sh$/) print $i }' | sort -u`
	output, err := c.Run([]string{command}, nil, 20*time.Second)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	result := make([]string, 0)
	for _, line := range strings.Split(string(output), "\n") {
		path := strings.TrimSpace(line)
		if path == "" || !strings.Contains(path, "/") || seen[path] {
			continue
		}
		seen[path] = true
		result = append(result, path)
	}
	return result, nil
}

// Devices returns the raw NAS device activity rows.
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

// RenameV2 atomically moves a path on the NAS and writes both journal records
// in one recoverable transaction (src precondition included).
func (c *Client) RenameV2(
	src, dst, kind, digest string, size, mtimeNS int64, device string,
) (bool, error) {
	fields := [][]byte{
		[]byte("BROWSE_RENAME_V2"), []byte(RunTimestamp()), []byte(device),
		[]byte(src), []byte(dst), []byte(kind), []byte(digest),
		[]byte(fmt.Sprint(size)), []byte(fmt.Sprint(mtimeNS)),
	}
	output, err := c.RunLocked(
		[]string{c.script(), "--browse-rename"}, nulJoin(fields), device, 5*time.Minute,
	)
	if err != nil {
		return false, err
	}
	parts := bytes.Split(output, []byte{0})
	if len(parts) < 2 || string(parts[0]) != "BROWSE_RENAME_V2" {
		return false, fmt.Errorf("risposta rename non valida")
	}
	if string(parts[1]) == "OK" {
		return true, nil
	}
	detail := ""
	if len(parts) > 2 {
		detail = string(parts[2])
	}
	return false, fmt.Errorf("rename remoto fallito: %s", detail)
}

type HistoryVersion struct {
	Original, Suffix, TrashRel string
	Size                       int64
}

func (c *Client) HistoryList() ([]HistoryVersion, error) {
	output, err := c.Run([]string{c.script(), "--history-list"}, nil, 2*time.Minute)
	if err != nil {
		return nil, err
	}
	parts := bytes.Split(output, []byte{0})
	if len(parts) > 0 && len(parts[len(parts)-1]) == 0 {
		parts = parts[:len(parts)-1]
	}
	if len(parts) == 0 || string(parts[0]) != "HISTORY_V1" {
		return nil, fmt.Errorf("risposta history non valida")
	}
	var result []HistoryVersion
	for index := 1; index+3 < len(parts); index += 4 {
		size, _ := strconv.ParseInt(string(parts[index+3]), 10, 64)
		result = append(result, HistoryVersion{
			Original: string(parts[index]), Suffix: string(parts[index+1]),
			TrashRel: string(parts[index+2]), Size: size,
		})
	}
	return result, nil
}

// RestoreVersion restores one remote trash version by its history suffix.
func normaliseVersionSuffix(suffix string) string {
	version := strings.TrimPrefix(suffix, "-")
	if !strings.HasSuffix(version, "Z") {
		if regexp.MustCompile(`^\d{4}-\d{2}-\d{2}--\d{2}-\d{2}-\d{2}$`).MatchString(version) {
			version += "-000000Z"
		}
	}
	return version
}

func (c *Client) RestoreVersion(original, suffix, device string) (string, error) {
	version := normaliseVersionSuffix(suffix)
	statuses, err := c.RestoreBatch([]RestoreRequest{{Path: original, VersionTS: version}}, device)
	if err != nil {
		return "", err
	}
	return statuses[original], nil
}

type JournalPut struct {
	Path, Digest, Causal string
	Size, MTimeNS        int64
}

// JournalAppend records uploads in the authoritative NAS journal so other PCs
// see them immediately. It uses V3 when causal versions are available.
func (c *Client) JournalAppend(
	repositoryID, txid, device string, puts []JournalPut,
) (bool, error) {
	if len(puts) == 0 {
		return true, nil
	}
	useV3 := c.cfg.Bool("remote_causal_versions_available")
	fields := [][]byte{[]byte("JOURNAL_V3")}
	if !useV3 {
		fields = [][]byte{[]byte("JOURNAL_V2")}
	}
	fields = append(fields,
		[]byte(repositoryID), []byte(txid), []byte(device),
		[]byte(fmt.Sprint(time.Now().Unix())), []byte(fmt.Sprint(len(puts))),
	)
	for _, put := range puts {
		fields = append(fields,
			[]byte("PUT"), []byte(put.Path), []byte(put.Digest),
			[]byte(fmt.Sprint(put.Size)), []byte(fmt.Sprint(put.MTimeNS)),
		)
		if useV3 {
			fields = append(fields, []byte(put.Causal))
		}
	}
	_, err := c.RunLocked(
		[]string{c.script(), "--journal-append"}, nulJoin(fields), device, 5*time.Minute,
	)
	return err == nil, err
}

type StagedFile struct {
	Path, Digest, Causal string
	Size, MTimeNS        int64
}

// CreateRemoteStaging creates a private same-filesystem staging tree.
func (c *Client) CreateRemoteStaging(transactionID string) (string, error) {
	if !regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`).MatchString(transactionID) {
		return "", fmt.Errorf("ID transazione non valido")
	}
	stagingDir := strings.TrimSuffix(c.cfg.RemotePrefix(), "/") + "/.nasbox-staging/" + transactionID
	if _, err := c.Run([]string{"mkdir", "-p", stagingDir}, nil, 30*time.Second); err != nil {
		return "", err
	}
	return stagingDir, nil
}

// CleanupRemoteStaging removes a staging tree after publish or failure.
func (c *Client) CleanupRemoteStaging(stagingDir string) error {
	_, err := c.Run([]string{"rm", "-rf", stagingDir}, nil, 30*time.Second)
	return err
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

// expandHome is Path.expanduser() for the known_hosts path.
func expandHome(value string) string {
	if value == "~" {
		if home, err := os.UserHomeDir(); err == nil {
			return home
		}
	}
	if strings.HasPrefix(value, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, value[2:])
		}
	}
	return value
}

// scanHostKeys is the shared ssh-keyscan step used by fingerprint listing and
// by pinning, so the two can never disagree about what the host presented.
func scanHostKeys(ctx context.Context, host string, port int) ([]byte, error) {
	if strings.TrimSpace(host) == "" {
		return nil, fmt.Errorf("host SSH vuoto")
	}
	if port <= 0 {
		port = 22
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	output, _, err := runCommand(ctx, nil, "ssh-keyscan", "-T", "6", "-p", strconv.Itoa(port), host)
	if err != nil {
		return nil, fmt.Errorf("ssh-keyscan per %s:%d: %w", host, port, err)
	}
	if len(bytes.TrimSpace(output)) == 0 {
		return nil, fmt.Errorf("nessuna chiave host ricevuta da %s:%d", host, port)
	}
	return output, nil
}

// fingerprintsFromScan turns ssh-keyscan output (or known_hosts lines, which
// share the "host type base64" layout) into a deduplicated list of SHA256
// fingerprints, ignoring comment lines and unparsable output.
func fingerprintsFromScan(ctx context.Context, raw []byte) ([]string, error) {
	keys := make([]string, 0)
	for _, line := range strings.Split(string(raw), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 3 && !strings.HasPrefix(fields[0], "#") {
			keys = append(keys, strings.Join(fields[1:], " "))
		}
	}
	if len(keys) == 0 {
		return nil, fmt.Errorf("nessuna chiave host valida da improntare")
	}
	input := []byte(strings.Join(keys, "\n") + "\n")
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	output, _, err := runCommand(ctx, input, "ssh-keygen", "-lf", "-", "-E", "sha256")
	if err != nil {
		return nil, fmt.Errorf("ssh-keygen -lf: %w", err)
	}
	seen := map[string]bool{}
	fingerprints := make([]string, 0)
	for _, line := range strings.Split(string(output), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && strings.HasPrefix(fields[1], "SHA256:") && !seen[fields[1]] {
			seen[fields[1]] = true
			fingerprints = append(fingerprints, fields[1])
		}
	}
	return fingerprints, nil
}

// HostKeyFingerprints returns the live host-key fingerprints without modifying
// known_hosts (Python's host_key_fingerprints).
func HostKeyFingerprints(ctx context.Context, host string, port int) ([]string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	raw, err := scanHostKeys(ctx, host, port)
	if err != nil {
		return nil, err
	}
	return fingerprintsFromScan(ctx, raw)
}

func (c *Client) HostKeyFingerprints(host string, port int) ([]string, error) {
	return HostKeyFingerprints(context.Background(), host, port)
}

// PinHostKey stores a user-confirmed fingerprint in a dedicated known_hosts
// file (Python's pin_host_key), refusing to pin a fingerprint the host did not
// actually present. The file is created with 0600 permissions.
func PinHostKey(ctx context.Context, host string, port int, knownHosts, fingerprint string) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	raw, err := scanHostKeys(ctx, host, port)
	if err != nil {
		return "", err
	}
	fingerprints, err := fingerprintsFromScan(ctx, raw)
	if err != nil {
		return "", err
	}
	matched := false
	for _, candidate := range fingerprints {
		if candidate == fingerprint {
			matched = true
			break
		}
	}
	if !matched {
		return "", fmt.Errorf("l'impronta scelta non corrisponde alla chiave ricevuta da %s:%d", host, port)
	}
	if strings.TrimSpace(knownHosts) == "" {
		return "", fmt.Errorf("percorso known_hosts vuoto")
	}
	target := expandHome(knownHosts)
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return "", err
	}
	existing, err := os.ReadFile(target)
	if err != nil && !os.IsNotExist(err) {
		return "", err
	}
	known := map[string]bool{}
	for _, line := range strings.Split(string(existing), "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			known[trimmed] = true
		}
	}
	additions := make([]string, 0)
	for _, line := range strings.Split(string(raw), "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" && !known[trimmed] {
			known[trimmed] = true
			additions = append(additions, trimmed)
		}
	}
	if len(additions) > 0 {
		content := strings.Join(additions, "\n") + "\n"
		if len(existing) > 0 && !bytes.HasSuffix(existing, []byte("\n")) {
			content = "\n" + content
		}
		file, err := os.OpenFile(target, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			return "", err
		}
		if _, err := file.WriteString(content); err != nil {
			_ = file.Close()
			return "", err
		}
		if err := file.Close(); err != nil {
			return "", err
		}
	}
	if err := os.Chmod(target, 0o600); err != nil {
		return "", err
	}
	return target, nil
}

// hostPatternMatches recognises the host field of a known_hosts line, both the
// plain name and the [host]:port form. Hashed entries cannot be matched by name
// and are deliberately skipped.
func hostPatternMatches(pattern, host string, port int) bool {
	pattern = strings.TrimSpace(strings.Trim(pattern, `"`))
	if pattern == "" || strings.HasPrefix(pattern, "|") {
		return false
	}
	bracketed := "[" + host + "]:" + strconv.Itoa(port)
	for _, token := range strings.Split(pattern, ",") {
		token = strings.TrimSpace(token)
		if token == host || token == bracketed {
			return true
		}
		if matched, err := filepath.Match(token, host); err == nil && matched {
			return true
		}
		if matched, err := filepath.Match(token, bracketed); err == nil && matched {
			return true
		}
	}
	return false
}

// pinnedLinesForHost extracts the key portion of the known_hosts lines that
// belong to host, so their fingerprints can be compared with the live key.
func pinnedLinesForHost(content []byte, host string, port int) []byte {
	var out bytes.Buffer
	for _, line := range strings.Split(string(content), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 || strings.HasPrefix(fields[0], "#") {
			continue
		}
		if hostPatternMatches(fields[0], host, port) {
			// Keep the host field: fingerprintsFromScan expects the same
			// "host type base64" layout produced by ssh-keyscan.
			out.WriteString(strings.Join(fields, " ") + "\n")
		}
	}
	return out.Bytes()
}

func validatePinForHost(ctx context.Context, content []byte, host string, port int) error {
	pinnedRaw := pinnedLinesForHost(content, host, port)
	if len(bytes.TrimSpace(pinnedRaw)) == 0 {
		return fmt.Errorf("nessuna chiave pinned per %s:%d nel known_hosts", host, port)
	}
	pinned, err := fingerprintsFromScan(ctx, pinnedRaw)
	if err != nil {
		return fmt.Errorf("impronte pinned per %s:%d non leggibili: %w", host, port, err)
	}
	live, err := HostKeyFingerprints(ctx, host, port)
	if err != nil {
		return fmt.Errorf("impossibile verificare la chiave host di %s:%d: %w", host, port, err)
	}
	for _, candidate := range live {
		for _, expected := range pinned {
			if candidate == expected {
				return nil
			}
		}
	}
	return fmt.Errorf(
		"impronta host non corrisponde al pin per %s:%d: atteso %v, ricevuto %v",
		host, port, pinned, live,
	)
}

// ValidateHostKeyPin must run before any mutating remote operation. SSH itself
// already refuses a changed key when StrictHostKeyChecking=yes, but this
// preflight turns a misconfigured/mismatched pin into an explicit, actionable
// error instead of a generic SSH failure. It is a no-op when pinning is off.
func (c *Client) ValidateHostKeyPin(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	pinNAS := c.cfg.Bool("ssh_host_key_pinned")
	pinJump := c.cfg.Bool("ssh_jump_key_pinned")
	if !pinNAS && !pinJump {
		return nil
	}
	knownHosts := strings.TrimSpace(c.cfg.String("ssh_known_hosts"))
	if knownHosts == "" {
		return fmt.Errorf("pin host key attivo ma ssh_known_hosts non configurato")
	}
	path := expandHome(knownHosts)
	content, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("file known_hosts del pin non leggibile (%s): %w", path, err)
	}
	builder := c.Builder()
	if pinNAS {
		if err := validatePinForHost(ctx, content, builder.conn.Host, sshPort(c.cfg)); err != nil {
			return err
		}
	}
	if pinJump {
		jump := strings.TrimSpace(c.cfg.String("jump_host"))
		if jump == "" {
			return fmt.Errorf("pin del bastione attivo ma jump_host non configurato")
		}
		if err := validatePinForHost(ctx, content, jump, jumpPort(c.cfg)); err != nil {
			return err
		}
	}
	return nil
}
