package engine

import (
	"context"
	"nasbox-go/internal/config"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"nasbox-go/internal/i18n"
)

func TestRunnerMergesManifestDelta(t *testing.T) {
	cfg, store, _ := setupTest(t)
	full := "NASBOX_MANIFEST_V1\na.txt\t" + strings.Repeat("a", 64) + "\t1\t1000\t\t\t\n"
	delta := "NASBOX_MANIFEST_DELTA_V1\nBASE_REVISION=1\nCURRENT_REVISION=2\nCHANGED=1\n" +
		"b.txt\t" + strings.Repeat("b", 64) + "\t1\t2000\t\t2000\t\nREMOVED=\n"
	nas := &fakeNAS{config: nasConfig(), manifest: full, delta: delta, revision: 2}
	runner := NewRunner(cfg, store, nas, &fakeTransfer{})

	first, err := runner.loadManifest(true)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := first["a.txt"]; !ok {
		t.Fatal("full manifest missing a.txt")
	}
	second, err := runner.loadManifest(false)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := second["a.txt"]; !ok {
		t.Fatal("delta merge lost a.txt")
	}
	if _, ok := second["b.txt"]; !ok {
		t.Fatal("delta merge missed b.txt")
	}
	if runner.manifestRevision != 2 {
		t.Fatalf("revision = %d, want 2", runner.manifestRevision)
	}
}

func TestRunnerUsesExistingTranslationKeys(t *testing.T) {
	// The runner emits localized messages using the shared table; no new key
	// may be required for the statuses it produces.
	for _, key := range []string{
		"log.action.cancelled", "metrics.sync_busy_title", "assurance.activity",
		"overview.reason_conflicts", "history.prune_local_body", "audit.running",
		"audit.error", "audit.ok", "audit.differences",
	} {
		if value := i18n.T(key, nil); value == key || value == "" {
			t.Fatalf("missing translation for %q", key)
		}
	}
}

func TestRunnerSchedulesAuditAfterSuccessfulSync(t *testing.T) {
	cfg, store, root := setupTest(t)
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	manifest := "NASBOX_MANIFEST_V1\na.txt\t" + strings.Repeat("a", 64) + "\t5\t1000\t\t\t\n"
	nas := &fakeNAS{config: nasConfig(), manifest: manifest}

	cfg.Set("mirror_interval_seconds", 3600, false)
	cfg.Set("integrity_audit_enabled", true, false)
	cfg.Set("integrity_check_interval_seconds", 1, false)
	cfg.Set("integrity_startup_delay_seconds", 0, false)
	cfg.Set("integrity_audit_startup_delay_seconds", 0, false)
	cfg.Set("last_integrity_check_ts", float64(time.Now().Add(-2*time.Hour).Unix()), false)

	runner := NewRunner(cfg, store, nas, &fakeTransfer{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = runner.Run(ctx) }()
	runner.SyncNow()

	reportPath := filepath.Join(os.Getenv("XDG_STATE_HOME"), "sync-daemon", "integrity-audit.json")
	deadline := time.After(8 * time.Second)
	for {
		content, err := os.ReadFile(reportPath)
		if err == nil {
			if !strings.Contains(string(content), `"reports"`) ||
				!strings.Contains(string(content), `"path": "a.txt"`) ||
				!strings.Contains(string(content), `"detail"`) {
				t.Fatalf("unexpected audit report: %s", content)
			}
			return
		}
		select {
		case <-deadline:
			t.Fatalf("audit was not scheduled after sync: %v", err)
		case <-time.After(50 * time.Millisecond):
		}
	}
}

func TestRunnerSkipsAuditWhenDisabled(t *testing.T) {
	cfg, store, _ := setupTest(t)
	cfg.Set("mirror_interval_seconds", 3600, false)
	cfg.Set("integrity_audit_enabled", false, false)
	nas := &fakeNAS{config: nasConfig(), manifest: "NASBOX_MANIFEST_V1\n"}
	runner := NewRunner(cfg, store, nas, &fakeTransfer{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = runner.Run(ctx) }()
	runner.SyncNow()

	deadline := time.After(500 * time.Millisecond)
	for {
		select {
		case event := <-runner.Events():
			if event.Kind == "sync" {
				// Give a possible background audit time to write its report.
				time.Sleep(200 * time.Millisecond)
				reportPath := filepath.Join(os.Getenv("XDG_STATE_HOME"), "sync-daemon", "integrity-audit.json")
				if _, err := os.Stat(reportPath); err == nil {
					t.Fatal("audit ran while disabled")
				}
				return
			}
		case <-deadline:
			t.Fatal("runner did not sync")
		}
	}
}

func TestRunnerSyncsImmediatelyAtStartup(t *testing.T) {
	cfg, store, _ := setupTest(t)
	cfg.Set("poll_interval", 3600, false)
	cfg.Set("mirror_interval_seconds", 3600, false)
	nas := &fakeNAS{config: nasConfig(), manifest: "NASBOX_MANIFEST_V1\n"}
	runner := NewRunner(cfg, store, nas, &fakeTransfer{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = runner.Run(ctx) }()

	deadline := time.After(5 * time.Second)
	for {
		select {
		case event := <-runner.Events():
			if event.Kind == "sync" {
				return
			}
		case <-deadline:
			t.Fatal("runner did not sync at startup")
		}
	}
}

func TestRunnerSyncsAfterFilesystemEvent(t *testing.T) {
	cfg, store, root := setupTest(t)
	cfg.Set("debounce_seconds", float64(1), false)
	if err := os.MkdirAll(filepath.Join(root, "folder"), 0o700); err != nil {
		t.Fatal(err)
	}
	nas := &fakeNAS{config: nasConfig(), manifest: "NASBOX_MANIFEST_V1\n"}
	runner := NewRunner(cfg, store, nas, &fakeTransfer{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = runner.Run(ctx) }()
	time.Sleep(300 * time.Millisecond) // let the watcher install its inotify watch
	if err := os.WriteFile(filepath.Join(root, "folder", "new.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	deadline := time.After(8 * time.Second)
	for {
		select {
		case event := <-runner.Events():
			t.Logf("event: %s %s", event.Kind, event.Message)
			if event.Kind == "sync" {
				return
			}
		case <-deadline:
			t.Fatal("runner did not sync after a filesystem event")
		}
	}
}

func TestPowerSavingIdleOnlyAfterQuietPeriod(t *testing.T) {
	cfg := config.New("")
	r := &Runner{cfg: cfg, activity: make(chan struct{}, 1), lastActivity: time.Now().Add(-time.Hour)}
	if quiet, _ := r.powerSavingIdle(); quiet {
		t.Fatal("power saving must be off by default")
	}
	cfg.Set("power_saving", "auto", false)
	cfg.Set("power_saving_poll_minutes", 7, false)
	quiet, poll := r.powerSavingIdle()
	if !quiet || poll != 7*time.Minute {
		t.Fatalf("quiet=%v poll=%v", quiet, poll)
	}
	r.touchActivity()
	if quiet, _ := r.powerSavingIdle(); quiet {
		t.Fatal("local activity must end the quiet period")
	}
	select {
	case <-r.activity:
	default:
		t.Fatal("activity must wake a sleeping change feed")
	}
}
