package integrity

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"nasbox-go/internal/classify"
	"nasbox-go/internal/config"
)

func TestRunDetectsChecksumDifference(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := config.New("")
	cfg.Set("local_root", root, false)
	manifest := map[string]classify.RemoteState{
		"a.txt": {Kind: "FILE", Digest: "wrong", Size: 5},
	}
	report := Run(cfg, manifest, "0.2.0", nil)
	if report.Status != "differences" || len(report.Differences) != 1 || report.FilesChecked != 1 {
		t.Fatalf("bad report: %+v", report)
	}
	if report.Differences[0].Path != "a.txt" || report.Differences[0].Detail != "content_mismatch" {
		t.Fatalf("bad difference: %+v", report.Differences[0])
	}
	if len(Load()) != 1 {
		t.Fatal("report was not persisted")
	}
}

func stateFile(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", dir)
	target, err := reportPath()
	if err != nil {
		t.Fatal(err)
	}
	return target
}

func TestSaveWritesPythonCompatibleFormat(t *testing.T) {
	target := stateFile(t)
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		t.Fatal(err)
	}
	report := Report{
		Status: "differences", StartedAt: 100, FinishedAt: 101, FilesChecked: 2,
		Differences: []Difference{{Path: "docs/a.txt", Detail: "content_mismatch"}},
		Issues:      []Issue{{Code: "pending_changes", Severity: "info", Detail: "1 modifica"}},
		Checks:      map[string]int{"excluded_manifest_paths": 1},
	}
	if err := Save(report); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	var document struct {
		Reports []map[string]any `json:"reports"`
	}
	if err := json.Unmarshal(content, &document); err != nil {
		t.Fatalf("not a JSON object with reports: %v", err)
	}
	if len(document.Reports) != 1 {
		t.Fatalf("reports = %d, want 1", len(document.Reports))
	}
	differences, ok := document.Reports[0]["differences"].([]any)
	if !ok || len(differences) != 1 {
		t.Fatalf("differences = %v", document.Reports[0]["differences"])
	}
	entry, ok := differences[0].(map[string]any)
	if !ok || entry["path"] != "docs/a.txt" || entry["detail"] != "content_mismatch" {
		t.Fatalf("difference entry = %v", differences[0])
	}
	// Reading it back must keep both the status and the structured issue.
	loaded := Load()
	if len(loaded) != 1 || loaded[0].Status != "differences" ||
		loaded[0].Issues[0].Code != "pending_changes" || loaded[0].Checks["excluded_manifest_paths"] != 1 {
		t.Fatalf("loaded = %+v", loaded)
	}
}

func TestLoadPythonLegacyFormat(t *testing.T) {
	target := stateFile(t)
	content := `{"reports": [{"status": "differences", "started_at": 1, "finished_at": 2,
		"files_checked": 2, "differences": [{"path": "a.txt", "reason": "missing_local"}],
		"issues": [{"code": "stale_pending", "severity": "warning", "count": "2", "detail": "attesa"}],
		"checks": {"pending_paths": 2}, "manifest_revision": 7, "client_version": "1.33.0", "error": ""}]}`
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	loaded := Load()
	if len(loaded) != 1 {
		t.Fatalf("loaded %d reports, want 1", len(loaded))
	}
	report := loaded[0]
	if report.Status != "differences" || report.FilesChecked != 2 || report.ManifestReview != 7 {
		t.Fatalf("bad report: %+v", report)
	}
	if len(report.Differences) != 1 || report.Differences[0].Path != "a.txt" ||
		report.Differences[0].Detail != "missing_local" {
		t.Fatalf("bad differences: %+v", report.Differences)
	}
	if len(report.Issues) != 1 || report.Issues[0].Code != "stale_pending" ||
		report.Issues[0].Severity != "warning" || report.Issues[0].Count != "2" {
		t.Fatalf("bad issues: %+v", report.Issues)
	}
}

func TestLoadLegacyGoArrayFormat(t *testing.T) {
	target := stateFile(t)
	content := `[{"status": "ok", "started_at": 3, "finished_at": 4, "files_checked": 1,
		"differences": ["vecchio.txt"], "issues": ["vecchio problema"], "manifest_revision": 0,
		"client_version": "0.2.0", "error": ""}]`
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	loaded := Load()
	if len(loaded) != 1 || loaded[0].FilesChecked != 1 {
		t.Fatalf("loaded = %+v", loaded)
	}
	if loaded[0].Differences[0].Path != "vecchio.txt" || loaded[0].Differences[0].Detail == "" {
		t.Fatalf("legacy difference lost: %+v", loaded[0].Differences)
	}
	if loaded[0].Issues[0].Detail != "vecchio problema" {
		t.Fatalf("legacy issue lost: %+v", loaded[0].Issues)
	}
	// Saving a new report must not drop the legacy one.
	if err := Save(Report{Status: "ok", StartedAt: 5, FinishedAt: 6}); err != nil {
		t.Fatal(err)
	}
	if loaded := Load(); len(loaded) != 2 {
		t.Fatalf("history after save = %d, want 2", len(loaded))
	}
}

func TestLoadKeepsAtMostMaxReports(t *testing.T) {
	stateFile(t)
	for index := 0; index < MaxReports+4; index++ {
		if err := Save(Report{Status: "ok", StartedAt: float64(index), FinishedAt: float64(index)}); err != nil {
			t.Fatal(err)
		}
	}
	loaded := Load()
	if len(loaded) != MaxReports {
		t.Fatalf("history length = %d, want %d", len(loaded), MaxReports)
	}
	if loaded[len(loaded)-1].StartedAt != float64(MaxReports+3) {
		t.Fatalf("latest report lost: %+v", loaded[len(loaded)-1])
	}
}

func TestIsDueRespectsIntervalsAndStartup(t *testing.T) {
	stateFile(t)
	base := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	oldNow, oldStart := nowFunc, processStart
	nowFunc, processStart = func() time.Time { return base }, base
	t.Cleanup(func() { nowFunc, processStart = oldNow, oldStart })

	cfg := config.New("")
	cfg.Set("integrity_audit_enabled", true, false)
	cfg.Set("integrity_check_interval_seconds", 3600, false)
	cfg.Set("integrity_startup_delay_seconds", 0, false)
	cfg.Set("integrity_audit_startup_delay_seconds", 0, false)
	cfg.Set("integrity_audit_interval_days", 7, false)
	cfg.Set("last_integrity_check_ts", float64(base.Add(-2*time.Hour).Unix()), false)

	if !IsDue(cfg) {
		t.Fatal("audit should be due when both intervals elapsed")
	}
	cfg.Set("integrity_audit_startup_delay_seconds", 1800, false)
	cfg.Set("integrity_startup_delay_seconds", 1800, false)
	if IsDue(cfg) {
		t.Fatal("audit ran before the startup delay")
	}
	cfg.Set("integrity_audit_startup_delay_seconds", 0, false)
	cfg.Set("integrity_startup_delay_seconds", 0, false)

	if err := Save(Report{Status: "ok", StartedAt: float64(base.Unix()), FinishedAt: float64(base.Unix())}); err != nil {
		t.Fatal(err)
	}
	if IsDue(cfg) {
		t.Fatal("audit repeated before the interval")
	}
	nowFunc = func() time.Time { return base.Add(8 * 24 * time.Hour) }
	cfg.Set("last_integrity_check_ts", float64(base.Add(8*24*time.Hour-2*time.Hour).Unix()), false)
	if !IsDue(cfg) {
		t.Fatal("audit not due after the interval")
	}

	cfg.Set("integrity_check_interval_seconds", 0, false)
	if IsDue(cfg) {
		t.Fatal("disabled periodic check must disable automatic audit")
	}
	cfg.Set("integrity_check_interval_seconds", 3600, false)
	cfg.Set("integrity_audit_enabled", false, false)
	if IsDue(cfg) {
		t.Fatal("disabled audit must not be due")
	}
}

func TestRunAppliesAllExclusionsAndSampleSize(t *testing.T) {
	stateFile(t)
	root := t.TempDir()
	cfg := config.New("")
	cfg.Set("local_root", root, false)
	cfg.Set("exclude_patterns", []any{"node_modules/", "*.tmp"}, false)
	cfg.Set("selective_sync_excludes", []any{"privato"}, false)
	cfg.Set("integrity_check_sample_size", 2, false)
	manifest := map[string]classify.RemoteState{}
	for _, name := range []string{
		"a.txt", "b.txt", "c.txt", "d.txt", "e.txt",
		"node_modules/lib.js", "cache.tmp", "privato/x.txt", ".sync-trash/old.txt", ".git/config",
	} {
		manifest[name] = classify.RemoteState{Kind: "FILE", Digest: "wrong", Size: 1}
		if err := os.MkdirAll(filepath.Dir(filepath.Join(root, filepath.FromSlash(name))), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(name)), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, path := range []string{"node_modules/lib.js", "cache.tmp", "privato/x.txt", ".sync-trash/old.txt", ".git/config"} {
		if !IsExcludedBy(cfg, path) {
			t.Fatalf("path %q was not excluded", path)
		}
	}
	if IsExcludedBy(cfg, "a.txt") {
		t.Fatal("ordinary file was excluded")
	}
	progress := []string{}
	report := Run(cfg, manifest, "test", func(done, total int) {
		progress = append(progress, fmt.Sprintf("%d/%d", done, total))
	})
	if report.FilesChecked != 2 {
		t.Fatalf("files checked = %d, want sample size 2", report.FilesChecked)
	}
	if report.Checks["excluded_manifest_paths"] != 5 {
		t.Fatalf("excluded paths = %d, want 5", report.Checks["excluded_manifest_paths"])
	}
	if len(progress) == 0 || !strings.HasSuffix(progress[len(progress)-1], "/2") {
		t.Fatalf("progress = %v", progress)
	}
	for _, difference := range report.Differences {
		if IsExcludedBy(cfg, difference.Path) {
			t.Fatalf("excluded path %q reached the report", difference.Path)
		}
	}
}
