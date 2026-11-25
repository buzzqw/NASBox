package engine

import (
	"strconv"
	"strings"
	"time"

	"nasbox-go/internal/config"
)

// Real-time sync is not a switch: it is the combination of two capabilities.
//
//   - local changes are pushed as soon as the filesystem reports them (inotify,
//     debounced by "debounce_seconds");
//   - changes made by another PC arrive through the NAS change feed, a long
//     poll on manifest.revision that the server advertises with
//     CHANGE_FEED_AVAILABLE.
//
// The "realtime" setting decides what to do when the NAS cannot push:
// "auto" (default) keeps the ordinary poll, "on" shortens it so a NAS without
// the change feed is still followed closely, "off" disables the long poll.
func realtimeMode(cfg *config.Config) string {
	switch strings.ToLower(strings.TrimSpace(cfg.String("realtime"))) {
	case "off", "no", "false", "disabled", "spento":
		return "off"
	case "on", "yes", "true", "always", "sempre":
		return "on"
	default:
		return "auto"
	}
}

// changeFeedEnabled reports whether the NAS long poll should run.
func changeFeedEnabled(cfg *config.Config) bool {
	if realtimeMode(cfg) == "off" {
		return false
	}
	return cfg.Bool("remote_change_feed_available")
}

// realtimeDescription is what doctor prints, so the state of real-time sync is
// visible instead of inferred: the user must be able to tell "the NAS cannot do
// it" from "someone turned it off".
func realtimeDescription(cfg *config.Config) string {
	mode := realtimeMode(cfg)
	feed := cfg.Bool("remote_change_feed_available")
	switch {
	case mode == "off":
		return "disattivato (impostazione realtime=off)"
	case feed:
		wait := cfg.Int("change_feed_wait_seconds")
		if wait <= 0 {
			wait = 55
		}
		return "attivo · change feed NAS (attesa " + strconv.Itoa(wait) + " s) + eventi locali"
	case mode == "on":
		return "senza change feed sul NAS: controllo ogni " + strconv.Itoa(int(realtimePollFallback(cfg).Seconds())) + " s"
	default:
		return "non offerto dal NAS (" + valueOrDash(cfg.String("server_version")) + "): polling ogni " +
			strconv.Itoa(int(pollInterval(cfg).Seconds())) + " s"
	}
}

// pollInterval is the ordinary timer between passes.
func pollInterval(cfg *config.Config) time.Duration {
	seconds := cfg.Int("poll_interval")
	if seconds <= 0 {
		seconds = 300
	}
	return time.Duration(seconds) * time.Second
}

// realtimePollFallback is the timer used when the user asked for real time but
// the NAS cannot push changes. Close enough to feel immediate on a small share,
// far enough not to hammer a NAS that is already busy.
func realtimePollFallback(cfg *config.Config) time.Duration {
	seconds := cfg.Int("realtime_poll_seconds")
	if seconds <= 0 {
		seconds = 60
	}
	if seconds > 300 {
		seconds = 300
	}
	return time.Duration(seconds) * time.Second
}

// effectivePollInterval is the timer the sync loop actually uses.
func effectivePollInterval(cfg *config.Config) time.Duration {
	if realtimeMode(cfg) == "on" && !cfg.Bool("remote_change_feed_available") {
		return realtimePollFallback(cfg)
	}
	return pollInterval(cfg)
}

func valueOrDash(value string) string {
	if strings.TrimSpace(value) == "" {
		return "sconosciuta"
	}
	return value
}

// RealtimeSummary is realtimeDescription for the CLI: doctor must show whether
// real-time sync is active, off, or impossible on this NAS.
func RealtimeSummary(cfg *config.Config) string { return realtimeDescription(cfg) }

// CapabilitiesSummary is the one-line "what this NAS is" description used by
// doctor. The values come from the last bootstrap, so they describe the NAS the
// client really talked to.
func CapabilitiesSummary(cfg *config.Config) string {
	parts := make([]string, 0, 6)
	if cores := cfg.Int("nas_cpu_count"); cores > 0 {
		parts = append(parts, strconv.Itoa(cores)+" core")
	}
	if mem := cfg.Int("nas_mem_total_mb"); mem > 0 {
		parts = append(parts, strconv.Itoa(mem)+" MB")
	}
	switch strings.TrimSpace(cfg.String("nas_disk_rotational")) {
	case "0":
		parts = append(parts, "dischi SSD")
	case "1":
		parts = append(parts, "dischi rotanti")
	}
	if value := cfg.String("nas_rsync_version"); value != "" {
		parts = append(parts, "rsync "+value)
	}
	if value := cfg.String("nas_fs_type"); value != "" {
		parts = append(parts, value)
	}
	if len(parts) == 0 {
		return "capacità non dichiarate dal NAS"
	}
	return strings.Join(parts, " · ")
}
