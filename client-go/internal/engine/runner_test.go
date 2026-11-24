package engine

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"nasbox-go/internal/i18n"
)

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
