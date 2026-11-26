package serverd

import (
	"bufio"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// State paths resolved from the server directory.
type StatePaths struct {
	Dir         string
	Journal     string
	Manifest    string
	Revision    string
	Snapshot    string
	Lock        string
	Owner       string
	JournalLock string
	QueueDir    string
	LastRun     string
	TrashUsage  string
	DaemonPID   string
	ServerLog   string
	Events      string
}

func (c *Context) paths() StatePaths {
	dir := c.StateDir
	return StatePaths{
		Dir:         dir,
		Journal:     filepath.Join(dir, "transfer-journal.tsv"),
		Manifest:    filepath.Join(dir, "manifest.tsv"),
		Revision:    filepath.Join(dir, "manifest.revision"),
		Snapshot:    filepath.Join(dir, "manifest.snapshot"),
		Lock:        filepath.Join(dir, "sync-transfer.lock"),
		Owner:       filepath.Join(dir, "sync-transfer.lock.owner"),
		JournalLock: filepath.Join(dir, "transfer-journal.lock"),
		QueueDir:    filepath.Join(dir, "transfer-queue"),
		LastRun:     filepath.Join(dir, "last-run"),
		TrashUsage:  filepath.Join(dir, "trash-usage"),
		DaemonPID:   filepath.Join(dir, "daemon.pid"),
		ServerLog:   filepath.Join(dir, "server.log"),
		Events:      filepath.Join(dir, "events.jsonl"),
	}
}

var internalPath = regexp.MustCompile(`(^|/)(\.sync-trash|\.sync-partial|\.nasbox-staging|\.nasbox-root|@eaDir)(/|$)`)

// encodeField escapes a path/field exactly like encode_journal_field in the
// bash daemon. Decode order is reversed for compatibility with %2509.
func encodeField(value string) string {
	value = strings.ReplaceAll(value, "%", "%25")
	value = strings.ReplaceAll(value, "\t", "%09")
	value = strings.ReplaceAll(value, "\n", "%0A")
	return strings.ReplaceAll(value, "\r", "%0D")
}

func decodeField(value string) string {
	value = strings.ReplaceAll(value, "%09", "\t")
	value = strings.ReplaceAll(value, "%0A", "\n")
	value = strings.ReplaceAll(value, "%0D", "\r")
	return strings.ReplaceAll(value, "%25", "%")
}

func readLines(path string) ([]string, error) {
	file, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer file.Close()
	var lines []string
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 1024*1024), 8*1024*1024)
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	return lines, scanner.Err()
}

func readString(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

func fileSize(path string) int64 {
	info, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return info.Size()
}

// renderManifestLegacy applies the committed part of the journal to manifest.tsv.
// It is kept as the compatibility and disaster-recovery fallback for the SQLite
// index. The bash daemon iterates an awk associative array, so row order is not
// stable; the Go daemon sorts by encoded path. Clients parse into maps, so
// the order is semantically irrelevant, but comparison must be order-free.
func (c *Context) renderManifestLegacy() ([]string, error) {
	p := c.paths()
	state := map[string]string{}
	base, err := readLines(p.Manifest)
	if err != nil {
		return nil, err
	}
	for _, line := range base {
		if line == "NASBOX_MANIFEST_V1" {
			continue
		}
		fields := strings.Split(line, "\t")
		if len(fields) >= 6 {
			state[fields[0]] = line
		}
	}

	journal, err := readLines(p.Journal)
	if err != nil {
		return nil, err
	}
	tx := ""
	var pending []string
	flush := func() {
		for _, row := range pending {
			fields := strings.Split(row, "\t")
			switch fields[0] {
			case "DELETE":
				if len(fields) < 7 {
					continue
				}
				path := fields[1]
				state[path] = strings.Join([]string{path, "", "0", "0", fields[5], fields[6], fieldOr(fields, 7, "")}, "\t")
			case "PUT":
				if len(fields) < 7 || fields[1] == "" {
					continue
				}
				// The bash renderer always writes fields[8]; when the journal
				// row has no causal version that produces a trailing tab.
				row := []string{
					fields[1], fields[2], fields[3], fields[4], fields[5], fields[6],
					fieldOr(fields, 7, ""),
				}
				state[fields[1]] = strings.Join(row, "\t")
			}
		}
		pending = nil
	}
	for _, line := range journal {
		if line == "NASBOX_JOURNAL_V1" {
			continue
		}
		fields := strings.Split(line, "\t")
		switch fields[0] {
		case "BEGIN":
			if len(fields) > 1 {
				tx = fields[1]
			}
			pending = nil
		case "PUT", "DELETE":
			if tx != "" {
				pending = append(pending, line)
			}
		case "COMMIT":
			if tx != "" && len(fields) > 1 && fields[1] == tx {
				flush()
				tx = ""
			}
		}
	}

	paths := make([]string, 0, len(state))
	for path := range state {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	out := make([]string, 0, len(paths)+1)
	out = append(out, "NASBOX_MANIFEST_V1")
	cutoff := int64(0)
	if raw := c.Config["TOMBSTONE_TTL_DAYS"]; raw != "" {
		cutoff, _ = strconv.ParseInt(raw, 10, 64)
		cutoff *= 86400
	}
	now := time.Now().Unix()
	for _, path := range paths {
		if internalPath.MatchString(path) {
			continue
		}
		row := state[path]
		if cutoff > 0 {
			f := strings.Split(row, "\t")
			if len(f) > 2 && f[1] == "" && f[2] == "0" {
				ts, _ := strconv.ParseInt(f[5], 10, 64)
				if now-ts > cutoff {
					continue
				}
			}
		}
		out = append(out, row)
	}
	return out, nil
}

func fieldOr(fields []string, index int, fallback string) string {
	if index < len(fields) {
		return fields[index]
	}
	return fallback
}

func manifestLookup(rows []string, path string) (string, bool) {
	if len(rows) == 0 {
		return "", false
	}
	target := encodeField(path)
	for _, row := range rows[1:] {
		fields := strings.Split(row, "\t")
		if len(fields) > 0 && fields[0] == target {
			return row, true
		}
	}
	return "", false
}

// journalRevision reads and normalizes manifest.revision like the daemon.
func (c *Context) journalRevision() (int64, error) {
	raw := readString(c.paths().Revision)
	if raw == "" {
		return 0, nil
	}
	if !regexp.MustCompile(`^[0-9]{1,18}$`).MatchString(raw) {
		return 0, errors.New("manifest.revision non valido")
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0, err
	}
	return value, nil
}
