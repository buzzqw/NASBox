// Package integrity runs the periodic checksum audit. Reports are stored in
// the same JSON file as the Python client so both clients see one history.
package integrity

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"nasbox-go/internal/classify"
	"nasbox-go/internal/config"
)

type Report struct {
	Status         string   `json:"status"`
	StartedAt      float64  `json:"started_at"`
	FinishedAt     float64  `json:"finished_at"`
	Duration       float64  `json:"duration_seconds"`
	FilesChecked   int      `json:"files_checked"`
	Differences    []string `json:"differences"`
	Issues         []string `json:"issues"`
	ManifestReview int      `json:"manifest_revision"`
	ClientVersion  string   `json:"client_version"`
	Error          string   `json:"error"`
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

func Load() []Report {
	path, err := reportPath()
	if err != nil {
		return nil
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var history []Report
	if err := json.Unmarshal(content, &history); err != nil {
		return nil
	}
	return history
}

func Save(report Report) error {
	history := append(Load(), report)
	if len(history) > 10 {
		history = history[len(history)-10:]
	}
	path, err := reportPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	content, err := json.MarshalIndent(history, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, content, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func IsDue(cfg *config.Config) bool {
	if !cfg.Bool("integrity_audit_enabled") {
		return false
	}
	days := 7.0
	if value, ok := cfg.Get("integrity_audit_interval_days").(float64); ok {
		days = value
	}
	interval := days * 86400
	if interval < 3600 {
		interval = 3600
	}
	last := 0.0
	for _, report := range Load() {
		if report.FinishedAt > last {
			last = report.FinishedAt
		}
	}
	return float64(time.Now().UnixNano())/1e9-last >= interval
}

func hashFile(path string) (string, error) {
	file, err := os.Open(path)
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

// Run compares local content with the manifest digest and records the result.
func Run(cfg *config.Config, manifest map[string]classify.RemoteState, version string, progress func(int, int)) Report {
	report := Report{
		Status: "ok", StartedAt: float64(time.Now().UnixNano()) / 1e9,
		Differences: []string{}, Issues: []string{}, ClientVersion: version,
	}
	root := cfg.LocalRoot()
	checked := 0
	for path, state := range manifest {
		if state.Kind != "FILE" {
			continue
		}
		localPath := filepath.Join(root, filepath.FromSlash(path))
		if _, err := os.Stat(localPath); err != nil {
			continue
		}
		checked++
		digest, err := hashFile(localPath)
		if err != nil {
			report.Issues = append(report.Issues, path+": "+err.Error())
			continue
		}
		if digest != state.Digest {
			report.Differences = append(report.Differences, path)
		}
		if progress != nil && checked%250 == 0 {
			progress(checked, len(manifest))
		}
	}
	report.FilesChecked = checked
	if len(report.Differences) > 0 {
		report.Status = "differences"
	}
	if len(report.Issues) > 0 {
		report.Status = "problems"
	}
	report.FinishedAt = float64(time.Now().UnixNano()) / 1e9
	report.Duration = report.FinishedAt - report.StartedAt
	_ = Save(report)
	return report
}

// IsExcluded mirrors the simple built-in exclusions used by the Go client.
func IsExcluded(path string) bool {
	return strings.HasPrefix(path, ".sync-trash/") || strings.HasPrefix(path, ".git/") ||
		strings.HasPrefix(path, ".venv/") || strings.HasSuffix(path, ".pyc")
}
