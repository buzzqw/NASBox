// Package serverd contains the Go replacement for the NAS-side bash daemon.
// Milestone 0 implements only read-only verbs and keeps their wire format
// identical to server/sync-daemon-server.sh so both daemons can run side by
// side and their outputs can be compared on real state.
package serverd

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const version = "3.32.4"

// Options are the global flags accepted before the verb.
type Options struct {
	ConfigPath string
}

// ParseOptions removes global flags from argv and returns the verb and its
// arguments. It accepts "-c FILE" anywhere, like the bash script does.
func ParseOptions(args []string) (Options, []string, error) {
	var opts Options
	rest := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-c":
			if i+1 >= len(args) {
				return opts, nil, fmt.Errorf("-c richiede un file di configurazione")
			}
			i++
			opts.ConfigPath = args[i]
		case "--help", "-h":
			return opts, []string{"--help"}, nil
		default:
			rest = append(rest, args[i])
		}
	}
	if opts.ConfigPath == "" {
		opts.ConfigPath = os.Getenv("NASBOX_SERVER_CONF")
	}
	if opts.ConfigPath == "" {
		opts.ConfigPath = "/volume1/Varie/sync-daemon/server/server.conf"
	}
	return opts, rest, nil
}

// Context holds the resolved paths and the parsed server.conf.
type Context struct {
	Options    Options
	Config     map[string]string
	ServerDir  string
	StateDir   string
	ShareRoot  string
	ScriptPath string
}

// Open reads server.conf. Missing files are allowed: the bash daemon falls
// back to built-in defaults, and read-only commands must still work.
var configDefaults = map[string]string{
	"SHARE_ROOT": "", "RETENTION_DAYS": "30", "TOMBSTONE_TTL_DAYS": "0",
	"SYNC_LOCK_MAX_AGE_MINUTES": "0", "CHECK_INTERVAL_MINUTES": "60",
	"PRUNE_MAX_FILES_PER_PASS": "500", "TRASH_MAX_MB": "0",
	"TRASH_MIN_KEEP_DAYS": "7", "STAGING_RETENTION_HOURS": "168",
	"PRUNE_MAX_STAGING_PER_PASS": "20", "LOG_MAX_BYTES": "5242880",
	"JOURNAL_MAX_BYTES": "10485760", "JOURNAL_MAX_LINES": "50000",
	"LOG_RETENTION_DAYS": "30", "EVENTS_MAX_BYTES": "5242880",
	"STATE_TEMP_MAX_AGE_HOURS": "24", "FILE_STATES_PARALLELISM": "",
}

func applyDefaults(cfg map[string]string) {
	for key, fallback := range configDefaults {
		if _, ok := cfg[key]; !ok {
			cfg[key] = fallback
		}
	}
}

func Open(opts Options) (*Context, error) {
	cfg, err := readConfig(opts.ConfigPath)
	if err != nil {
		return nil, err
	}
	applyDefaults(cfg)
	serverDir := filepath.Dir(opts.ConfigPath)
	stateDir := value(cfg, "STATE_DIR", filepath.Join(serverDir, "state"))
	shareRoot := value(cfg, "SHARE_ROOT", "/volume1/NASBox")
	script, err := os.Executable()
	if err != nil {
		script = opts.ConfigPath
	}
	return &Context{
		Options: opts, Config: cfg, ServerDir: serverDir,
		StateDir: stateDir, ShareRoot: shareRoot, ScriptPath: script,
	}, nil
}

func (c *Context) Close() {}

// ensureJournalFiles mirrors the bash daemon's prelude: every verb except
// --metrics/--devices/--wait-for-revision creates the three state files.
func (c *Context) ensureJournalFiles() error {
	p := c.paths()
	if err := os.MkdirAll(p.Dir, 0o700); err != nil {
		return err
	}
	files := map[string]string{
		p.Journal:  "NASBOX_JOURNAL_V1\n",
		p.Manifest: "NASBOX_MANIFEST_V1\n",
		p.Revision: "0\n",
	}
	for path, content := range files {
		if _, err := os.Stat(path); os.IsNotExist(err) {
			if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
				return err
			}
		} else if err != nil {
			return err
		}
	}
	return nil
}

func value(cfg map[string]string, key, fallback string) string {
	if v, ok := cfg[key]; ok && v != "" {
		return v
	}
	return fallback
}

// readConfig parses KEY=VALUE lines, ignoring comments and inline comments.
// Values may be double quoted; trailing "# ..." is stripped when preceded by
// whitespace, matching the bash daemon's configuration style.
func readConfig(path string) (map[string]string, error) {
	out := map[string]string{}
	file, err := os.Open(path)
	if os.IsNotExist(err) {
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, raw, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		raw = strings.TrimSpace(stripInlineComment(raw))
		raw = strings.Trim(raw, `"'`)
		if key != "" {
			out[key] = raw
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func stripInlineComment(value string) string {
	if index := strings.Index(value, " #"); index >= 0 {
		return strings.TrimSpace(value[:index])
	}
	return value
}

// Dispatch routes a verb to its implementation.
func Dispatch(ctx *Context, verb string, args []string) error {
	switch verb {
	case "--metrics", "--devices", "--help", "-h", "--version", "--wait-for-revision":
	default:
		if err := ctx.ensureJournalFiles(); err != nil {
			return err
		}
	}
	switch verb {
	case "--help", "-h":
		return Usage()
	case "--version":
		fmt.Printf("sync-daemon-server v%s\n", version)
		return nil
	case "--capabilities":
		return cmdCapabilities(ctx)
	case "--print-config":
		return cmdPrintConfig(ctx)
	case "--status":
		return cmdStatus(ctx)
	case "--journal-status":
		return cmdJournalStatus(ctx)
	case "--diagnostics":
		return cmdDiagnostics(ctx)
	case "--devices":
		return cmdDevices(ctx)
	case "--metrics":
		return cmdMetrics(ctx)
	case "--manifest-export", "--manifest-export-delta":
		if verb == "--manifest-export-delta" {
			if len(args) == 0 {
				return fmt.Errorf("manifest export delta: since_revision mancante")
			}
			return cmdManifestExport(ctx, []string{args[0]})
		}
		return cmdManifestExport(ctx, args)
	case "--manifest-get":
		return cmdManifestGet(ctx, args)
	case "--browse-list":
		return cmdBrowseList(ctx, args)
	case "--history-list":
		return cmdHistoryList(ctx)
	case "--file-states-current":
		return ctx.cmdFileStates(true)
	case "--file-states":
		return ctx.cmdFileStates(false)
	case "--chunk-stats":
		return cmdChunkStats(ctx)
	case "--chunk-snapshot-create":
		return cmdChunkSnapshotCreate(ctx, args)
	case "--chunk-snapshot-list":
		return cmdChunkSnapshotList(ctx)
	case "--chunk-restore":
		return cmdChunkRestore(ctx, args)
	case "--init-repository":
		return cmdInitRepository(ctx)
	case "--journal-append":
		return cmdJournalAppend(ctx)
	case "--staging-publish":
		return cmdStagingPublish(ctx)
	case "--transfer-wait":
		return cmdTransferWait(ctx)
	case "--checked-delete":
		return cmdCheckedDelete(ctx)
	case "--browse-rename":
		return cmdBrowseRename(ctx)
	case "--browse-delete":
		return cmdBrowseDelete(ctx)
	case "--run-once":
		return cmdRunOnce(ctx)
	case "--start":
		return cmdStart(ctx)
	case "--stop":
		return cmdStop(ctx)
	case "--restart":
		return cmdRestart(ctx)
	case "--run-foreground":
		return cmdDaemonForeground(ctx)
	default:
		return fmt.Errorf("verbo %q non ancora implementato nella milestone 0", verb)
	}
}

// Usage prints the supported commands.
func Usage() error {
	fmt.Printf(`sync-daemon-server v%s

Demone autonomo NASBox (implementazione Go, milestone 0: sola lettura).

Uso:
  nasbox-server -c FILE --print-config
  nasbox-server -c FILE --capabilities
  nasbox-server -c FILE --status
  nasbox-server -c FILE --journal-status
  nasbox-server -c FILE --metrics
  nasbox-server -c FILE --devices
  nasbox-server -c FILE --diagnostics
  nasbox-server -c FILE --manifest-export [SINCE]
  nasbox-server -c FILE --manifest-get PATH
  nasbox-server -c FILE --file-states
  nasbox-server -c FILE --history-list

Datapath a chunk (milestone 1, non ancora usato dai client):
  nasbox-server -c FILE --chunk-stats
  nasbox-server -c FILE --chunk-snapshot-create DEVICE PATH [PATH...]
  nasbox-server -c FILE --chunk-snapshot-list
  nasbox-server -c FILE --chunk-restore ID PATH DEST
`, version)
	return nil
}
