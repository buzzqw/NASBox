package engine

import (
	"strings"
	"testing"
	"time"

	"nasbox-go/internal/config"
	"nasbox-go/internal/remote"
)

// The real-time policy is driven by what the NAS declares, not by assumptions:
// a NAS with the change feed is followed instantly, one without it keeps the
// ordinary timer unless the user asked for "always", and "off" must really stop
// the long poll.
func TestRealtimeModeParsing(t *testing.T) {
	cases := map[string]string{
		"":         "auto",
		"auto":     "auto",
		"banana":   "auto",
		"on":       "on",
		"Always":   "on",
		"off":      "off",
		"disabled": "off",
	}
	for value, want := range cases {
		cfg := config.New("")
		if value != "" {
			cfg.Set("realtime", value, false)
		}
		if got := realtimeMode(cfg); got != want {
			t.Fatalf("realtime=%q: atteso %s, ottenuto %s", value, want, got)
		}
	}
}

func TestChangeFeedFollowsTheNasCapability(t *testing.T) {
	cfg := config.New("")
	cfg.Set("realtime", "auto", false)
	cfg.Set("remote_change_feed_available", true, false)
	if !changeFeedEnabled(cfg) {
		t.Fatal("change feed disponibile ma non usato")
	}

	cfg.Set("remote_change_feed_available", false, false)
	if changeFeedEnabled(cfg) {
		t.Fatal("change feed usato su un NAS che non lo offre")
	}

	cfg.Set("remote_change_feed_available", true, false)
	cfg.Set("realtime", "off", false)
	if changeFeedEnabled(cfg) {
		t.Fatal("realtime=off non ha fermato il change feed")
	}
}

func TestEffectivePollInterval(t *testing.T) {
	cfg := config.New("")
	cfg.Set("poll_interval", float64(300), false)

	// NAS with the feed: the ordinary timer is only a safety net.
	cfg.Set("remote_change_feed_available", true, false)
	if got := effectivePollInterval(cfg); got != 300*time.Second {
		t.Fatalf("con change feed: %v", got)
	}

	// NAS without it and "auto": unchanged behaviour.
	cfg.Set("remote_change_feed_available", false, false)
	if got := effectivePollInterval(cfg); got != 300*time.Second {
		t.Fatalf("auto senza change feed: %v", got)
	}

	// "on" without the feed: a minute, so "always" still means close.
	cfg.Set("realtime", "on", false)
	if got := effectivePollInterval(cfg); got != 60*time.Second {
		t.Fatalf("on senza change feed: %v", got)
	}

	// ...but never more often than the user's explicit value allows.
	cfg.Set("realtime_poll_seconds", float64(15), false)
	if got := effectivePollInterval(cfg); got != 15*time.Second {
		t.Fatalf("on con intervallo esplicito: %v", got)
	}
	// ...and never slower than five minutes even if configured so.
	cfg.Set("realtime_poll_seconds", float64(3600), false)
	if got := effectivePollInterval(cfg); got != 300*time.Second {
		t.Fatalf("intervallo oltre il tetto: %v", got)
	}
}

func TestRealtimeDescriptionTellsTheThreeStates(t *testing.T) {
	cfg := config.New("")
	cfg.Set("remote_change_feed_available", true, false)
	cfg.Set("change_feed_wait_seconds", float64(55), false)
	if got := RealtimeSummary(cfg); got == "" || !strings.Contains(got, "attivo") {
		t.Fatalf("con change feed: %q", got)
	}
	cfg.Set("realtime", "off", false)
	if got := RealtimeSummary(cfg); !strings.Contains(got, "disattivato") {
		t.Fatalf("off: %q", got)
	}
	cfg.Set("realtime", "auto", false)
	cfg.Set("remote_change_feed_available", false, false)
	cfg.Set("server_version", "3.24.0", false)
	if got := RealtimeSummary(cfg); !strings.Contains(got, "non offerto") || !strings.Contains(got, "3.24.0") {
		t.Fatalf("senza change feed: %q", got)
	}
}

// A NAS with solid-state disks has nothing to spin down: "auto" must not turn
// real time into a five-minute poll there. An unknown disk type keeps the old
// conservative behaviour.
func TestPowerSavingRespectsTheDiskType(t *testing.T) {
	cfg := config.New("")
	cfg.Set("power_saving", "auto", false)
	cases := map[string]bool{"1": true, "0": false, "": true}
	for disk, want := range cases {
		cfg.Set("nas_disk_rotational", disk, false)
		if got := remote.PowerSaving(cfg); got != want {
			t.Fatalf("disco %q: risparmio=%v, atteso %v", disk, got, want)
		}
	}
	cfg.Set("power_saving", "off", false)
	cfg.Set("nas_disk_rotational", "1", false)
	if remote.PowerSaving(cfg) {
		t.Fatal("power_saving=off ha attivato il risparmio")
	}
}
