// Package integrity runs the periodic checksum audit. Reports are stored in
// the same JSON file as the Python client so both clients see one history.
//
// On-disk format (both clients):
//
//	{"reports": [{"status": ..., "differences": [{"path": ..., "detail": ...}], ...}]}
//
// Older Go builds wrote a bare array with plain-string differences and Python
// writes "reason" instead of "detail": Load() accepts all three shapes.
package integrity

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"math"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"nasbox-go/internal/classify"
	"nasbox-go/internal/config"
)

const (
	// MaxReports matches Python's integrity_audit.MAX_REPORTS.
	MaxReports          = 5
	DefaultIntervalDays = 7.0
	DefaultBatchSize    = 250
)

// Difference is one mismatch found by the audit. The Python client uses the
// key "reason" for the same value; both spellings are accepted on read, while
// new reports always use "detail" as required by the shared report format.
type Difference struct {
	Path   string `json:"path"`
	Detail string `json:"detail"`
}

// UnmarshalJSON accepts {"path":..., "detail":...}, Python's
// {"path":..., "reason":...} and the legacy plain-string form.
func (d *Difference) UnmarshalJSON(data []byte) error {
	var text string
	if err := json.Unmarshal(data, &text); err == nil {
		d.Path, d.Detail = text, "content_mismatch"
		return nil
	}
	var object map[string]any
	if err := json.Unmarshal(data, &object); err != nil {
		return err
	}
	for _, key := range []string{"path", "Path"} {
		if value, ok := object[key].(string); ok {
			d.Path = value
			break
		}
	}
	for _, key := range []string{"detail", "reason", "detail_text"} {
		if value, ok := object[key].(string); ok {
			d.Detail = value
			break
		}
	}
	return nil
}

// Issue mirrors Python's structured audit issues. Legacy Go reports stored
// issues as plain strings, which are read back into Detail.
type Issue struct {
	Code     string `json:"code,omitempty"`
	Severity string `json:"severity,omitempty"`
	Count    string `json:"count,omitempty"`
	Detail   string `json:"detail,omitempty"`
}

// UnmarshalJSON accepts both the structured Python form and legacy strings.
func (i *Issue) UnmarshalJSON(data []byte) error {
	var text string
	if err := json.Unmarshal(data, &text); err == nil {
		i.Detail = text
		return nil
	}
	var object map[string]any
	if err := json.Unmarshal(data, &object); err != nil {
		return err
	}
	for key, target := range map[string]*string{
		"code": &i.Code, "severity": &i.Severity, "count": &i.Count, "detail": &i.Detail,
	} {
		if value, ok := object[key].(string); ok {
			*target = value
		}
	}
	if i.Count == "" {
		if value, ok := object["count"].(float64); ok {
			i.Count = strconv.FormatInt(int64(value), 10)
		}
	}
	return nil
}

// Report is compatible with Python's integrity_audit.AuditReport.
type Report struct {
	Status         string         `json:"status"`
	StartedAt      float64        `json:"started_at"`
	FinishedAt     float64        `json:"finished_at"`
	Duration       float64        `json:"duration_seconds"`
	FilesChecked   int            `json:"files_checked"`
	Differences    []Difference   `json:"differences"`
	Issues         []Issue        `json:"issues"`
	Checks         map[string]int `json:"checks"`
	ManifestReview int            `json:"manifest_revision"`
	ClientVersion  string         `json:"client_version"`
	Error          string         `json:"error"`
}

type reportFile struct {
	Reports []Report `json:"reports"`
}

func reportPath() (string, error) {
	base := os.Getenv("XDG_STATE_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		base = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(base, "sync-daemon", "integrity-audit.json"), nil
}

// decodeReports reads the Python object form and the legacy bare-array form.
func decodeReports(content []byte) ([]Report, error) {
	var object reportFile
	if err := json.Unmarshal(content, &object); err == nil && object.Reports != nil {
		return object.Reports, nil
	}
	var legacy []Report
	if err := json.Unmarshal(content, &legacy); err != nil {
		return nil, err
	}
	return legacy, nil
}

// Load returns the latest reports, oldest first.
func Load() []Report {
	path, err := reportPath()
	if err != nil {
		return nil
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	reports, err := decodeReports(content)
	if err != nil {
		return nil
	}
	if len(reports) > MaxReports {
		reports = reports[len(reports)-MaxReports:]
	}
	return reports
}

// Save appends a report using the shared {"reports": [...]} format. Entries
// written by older clients or by Python are preserved (and normalised).
func Save(report Report) error {
	history := make([]Report, 0, MaxReports+1)
	for _, item := range Load() {
		if item.StartedAt != report.StartedAt {
			history = append(history, item)
		}
	}
	history = append(history, report)
	if len(history) > MaxReports {
		history = history[len(history)-MaxReports:]
	}
	path, err := reportPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	content, err := json.MarshalIndent(reportFile{Reports: history}, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".integrity-audit-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(content); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

// nowFunc and processStart make the scheduling rules testable without sleeps.
var (
	nowFunc                = time.Now
	processStart time.Time = time.Now()
)

// intSetting reads an integer key, falling back to def only when absent.
func intSetting(cfg *config.Config, key string, def int) int {
	if cfg.Get(key) == nil {
		return def
	}
	return cfg.Int(key)
}

func floatSetting(cfg *config.Config, key string, def float64) float64 {
	value := cfg.Get(key)
	switch typed := value.(type) {
	case float64:
		return typed
	case int:
		return float64(typed)
	case json.Number:
		parsed, err := typed.Float64()
		if err == nil {
			return parsed
		}
	}
	return def
}

// IsDue reports whether the scheduled audit should start now. It honours:
//   - integrity_audit_enabled (off disables the audit);
//   - integrity_audit_interval_days (at least one hour) since the last report;
//   - integrity_check_interval_seconds since the last recorded check (0 disables);
//   - integrity_startup_delay_seconds and integrity_audit_startup_delay_seconds,
//     so a fresh client never hammers the NAS right after startup;
//   - the sample size is applied by Run.
func IsDue(cfg *config.Config) bool {
	if cfg == nil || !cfg.Bool("integrity_audit_enabled") {
		return false
	}
	now := nowFunc()
	nowSeconds := float64(now.UnixNano()) / 1e9

	// The full audit waits after startup. Either startup-delay key can be set
	// explicitly to 0 to disable the guard (tests, eager installations).
	auditStartup := intSetting(cfg, "integrity_audit_startup_delay_seconds", 1800)
	checkStartup := intSetting(cfg, "integrity_startup_delay_seconds", 1800)
	startup := auditStartup
	if checkStartup > startup {
		startup = checkStartup
	}
	if auditStartup == 0 || checkStartup == 0 {
		startup = 0
	}
	if startup > 0 && now.Sub(processStart) < time.Duration(startup)*time.Second {
		return false
	}

	// A disabled periodic check also disables the automatic audit.
	checkInterval := intSetting(cfg, "integrity_check_interval_seconds", 86400)
	if checkInterval <= 0 {
		return false
	}
	lastCheck := floatSetting(cfg, "last_integrity_check_ts", 0)
	if lastCheck <= 0 {
		if startup > checkInterval {
			startup = checkInterval
		}
		lastCheck = float64(processStart.UnixNano())/1e9 - float64(checkInterval-startup)
	}
	if nowSeconds-lastCheck < float64(checkInterval) {
		return false
	}

	days := floatSetting(cfg, "integrity_audit_interval_days", DefaultIntervalDays)
	if days <= 0 {
		days = DefaultIntervalDays
	}
	interval := math.Max(3600, days*86400)
	last := 0.0
	for _, report := range Load() {
		if report.FinishedAt > last {
			last = report.FinishedAt
		}
	}
	return nowSeconds-last >= interval
}

func hashFile(filePath string) (string, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hasher := sha256.New()
	buffer := make([]byte, 128*1024)
	for {
		n, err := file.Read(buffer)
		if n > 0 {
			_, _ = hasher.Write(buffer[:n])
		}
		if err != nil {
			if err == io.EOF {
				break
			}
			return "", err
		}
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}

// internalDirs are never mirrored (server state, trash, staging, exports).
var internalDirs = map[string]bool{
	".sync-trash": true, ".sync-partial": true, ".nasbox-staging": true,
	".nasbox-root": true, "@eaDir": true, ".git": true, ".venv": true,
	".nasbox-client-update": true, ".nasbox-client-update-go": true,
	".nasbox": true,
}

// IsExcluded applies the built-in exclusions. It is kept for callers that do
// not have a Config; IsExcludedBy also applies user patterns and selective sync.
func IsExcluded(relative string) bool {
	return isExcluded(nil, relative)
}

// IsExcludedBy applies every shared exclusion before a path is compared:
// internal directories, rsync-style user patterns and selective-sync folders.
func IsExcludedBy(cfg *config.Config, relative string) bool {
	return isExcluded(cfg, relative)
}

func isExcluded(cfg *config.Config, relative string) bool {
	clean := strings.ReplaceAll(relative, "\\", "/")
	clean = strings.TrimPrefix(clean, "./")
	if clean == "" {
		return true
	}
	parts := strings.Split(clean, "/")
	for _, part := range parts {
		if internalDirs[part] {
			return true
		}
	}
	if strings.HasSuffix(clean, ".pyc") {
		return true
	}
	if cfg == nil {
		return false
	}
	for _, key := range []string{"exclude_patterns", "selective_sync_excludes"} {
		patterns, ok := cfg.Get(key).([]any)
		if !ok {
			continue
		}
		for _, raw := range patterns {
			pattern, ok := raw.(string)
			if !ok {
				continue
			}
			if matchesPattern(pattern, clean) {
				return true
			}
		}
	}
	return false
}

// matchesPattern implements the simple rsync-style patterns used by both
// clients: exact names, directory prefixes and * / ? globs on names or paths.
func matchesPattern(raw, relative string) bool {
	pattern := strings.TrimSuffix(strings.ReplaceAll(raw, "\\", "/"), "/")
	pattern = strings.TrimPrefix(pattern, "./")
	if pattern == "" {
		return false
	}
	if pattern == relative || strings.HasPrefix(relative, pattern+"/") {
		return true
	}
	if matched, err := path.Match(pattern, relative); err == nil && matched {
		return true
	}
	for _, part := range strings.Split(relative, "/") {
		if matched, err := path.Match(pattern, part); err == nil && matched {
			return true
		}
	}
	return false
}

// samplePaths applies integrity_check_sample_size: 0 means the whole tree,
// otherwise an evenly spaced deterministic sample (as in the Python client).
func samplePaths(paths []string, size int) []string {
	if size <= 0 || len(paths) <= size {
		return paths
	}
	result := make([]string, 0, size)
	for index := 0; index < size; index++ {
		position := index * len(paths) / size
		if position >= len(paths) {
			position = len(paths) - 1
		}
		result = append(result, paths[position])
	}
	return result
}

// Run compares local content with the manifest digest and records the result.
// Exclusions are applied before hashing; the (potentially expensive) work is
// safe to call from a background goroutine.
func Run(cfg *config.Config, manifest map[string]classify.RemoteState, version string, progress func(int, int)) Report {
	started := float64(time.Now().UnixNano()) / 1e9
	report := Report{
		Status: "error", StartedAt: started, ManifestReview: -1,
		Differences: []Difference{}, Issues: []Issue{}, Checks: map[string]int{},
		ClientVersion: version,
	}
	defer func() {
		report.FinishedAt = float64(time.Now().UnixNano()) / 1e9
		report.Duration = math.Max(0, report.FinishedAt-report.StartedAt)
		_ = Save(report)
	}()
	if cfg == nil {
		report.Error = "configurazione client non disponibile"
		return report
	}
	if len(manifest) == 0 {
		report.Error = "manifest NAS non disponibile per l'audit"
		return report
	}
	root := cfg.LocalRoot()
	candidates := make([]string, 0, len(manifest))
	excludedCount := 0
	for relative, state := range manifest {
		if isExcluded(cfg, relative) {
			excludedCount++
			continue
		}
		if state.Kind != "FILE" || state.Digest == "" || relative == "" {
			continue
		}
		candidates = append(candidates, relative)
	}
	report.Checks["excluded_manifest_paths"] = excludedCount
	sort.Strings(candidates)
	candidates = samplePaths(candidates, intSetting(cfg, "integrity_check_sample_size", 500))
	total := len(candidates)
	batch := intSetting(cfg, "integrity_audit_batch_size", DefaultBatchSize)
	if batch < 50 {
		batch = 50
	}
	for index, relative := range candidates {
		localPath := filepath.Join(root, filepath.FromSlash(relative))
		if _, err := os.Stat(localPath); err != nil {
			report.Differences = append(report.Differences, Difference{Path: relative, Detail: "missing_local"})
			continue
		}
		digest, err := hashFile(localPath)
		if err != nil {
			report.Issues = append(report.Issues, Issue{
				Code: "hash_error", Severity: "warning", Count: "1", Detail: relative + ": " + err.Error(),
			})
			continue
		}
		if digest != manifest[relative].Digest {
			report.Differences = append(report.Differences, Difference{Path: relative, Detail: "content_mismatch"})
		}
		if progress != nil && (index+1)%batch == 0 {
			progress(index+1, total)
		}
	}
	if progress != nil {
		progress(total, total)
	}
	report.FilesChecked = total
	switch {
	case len(report.Differences) > 0:
		report.Status = "differences"
	case len(report.Issues) > 0:
		report.Status = "problems"
	default:
		report.Status = "ok"
	}
	// Keep the periodic-check bookkeeping in sync with Python. Persisting the
	// timestamp avoids repeating the startup delay after every client restart.
	cfg.Set("last_integrity_check_ts", report.FinishedAt, true)
	cfg.Set("last_integrity_files", report.FilesChecked, true)
	cfg.Set("last_integrity_mismatches", len(report.Differences), true)
	cfg.Set("last_integrity_error", "", true)
	return report
}
