package deletesafety

import (
	"testing"

	"nasbox-go/internal/config"
)

func TestCumulativeGuardAndConfirm(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	cfg := config.New("")
	cfg.Set("delete_enabled", true, false)
	cfg.Set("max_delete_files_per_window", float64(10), false)
	guard := New()
	if err := guard.Record(cfg, 6, "download"); err != nil {
		t.Fatal(err)
	}
	if err := guard.Check(cfg, 5, "download"); err == nil {
		t.Fatal("cumulative limit should block")
	}
	if _, err := guard.Confirm(cfg); err != nil {
		t.Fatal(err)
	}
	if err := guard.Check(cfg, 5, "download"); err != nil {
		t.Fatalf("confirmation should allow the batch: %v", err)
	}
	snapshot := guard.Snapshot(cfg)
	if snapshot.Used != 6 || snapshot.Limit != 10 || !snapshot.Confirmed {
		t.Fatalf("bad snapshot: %+v", snapshot)
	}
}

func TestIsConfirmedFollowsConfirmationWindow(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	cfg := config.New("")
	guard := New()
	if guard.IsConfirmed() {
		t.Fatal("nessuna conferma all'inizio")
	}
	if _, err := guard.Confirm(cfg); err != nil {
		t.Fatal(err)
	}
	if !guard.IsConfirmed() {
		t.Fatal("conferma attiva dopo Confirm")
	}
}
