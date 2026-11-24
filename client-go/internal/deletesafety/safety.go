// Package deletesafety is the cumulative guard against mass deletions.
// It runs entirely on the PC and writes one small JSON file in XDG state.
package deletesafety

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"

	"nasbox-go/internal/config"
)

const (
	defaultWindow       = 3600.0
	defaultMaxPerWindow = 1000
	confirmSeconds      = 600
	maxEvents           = 4096
)

type event struct {
	Time      float64 `json:"t"`
	Count     int     `json:"c"`
	Direction string  `json:"d"`
}

type state struct {
	Events         []event `json:"events"`
	ConfirmedUntil float64 `json:"confirmed_until"`
}

var (
	mu     sync.Mutex
	shared *Guard
)

// Guard holds the rolling deletion history.
type Guard struct {
	path  string
	state state
}

func New() *Guard {
	base := os.Getenv("XDG_STATE_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err == nil {
			base = filepath.Join(home, ".local", "state")
		}
	}
	return &Guard{path: filepath.Join(base, "sync-daemon", "delete-safety.json")}
}

// Shared returns the process-wide guard.
func Shared() *Guard {
	mu.Lock()
	defer mu.Unlock()
	if shared == nil {
		shared = New()
	}
	return shared
}

func (g *Guard) load() {
	content, err := os.ReadFile(g.path)
	if err != nil {
		return
	}
	var value state
	if err := json.Unmarshal(content, &value); err == nil {
		g.state = value
	}
}

func (g *Guard) save() error {
	if err := os.MkdirAll(filepath.Dir(g.path), 0o700); err != nil {
		return err
	}
	content, err := json.Marshal(g.state)
	if err != nil {
		return err
	}
	tmp := g.path + ".tmp"
	if err := os.WriteFile(tmp, content, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, g.path)
}

func windowSeconds(cfg *config.Config) float64 {
	if value, ok := cfg.Get("delete_safety_window_seconds").(float64); ok && value >= 60 {
		return value
	}
	return defaultWindow
}

func maxPerWindow(cfg *config.Config) int {
	if value, ok := cfg.Get("max_delete_files_per_window").(float64); ok && value >= 1 {
		return int(value)
	}
	return defaultMaxPerWindow
}

func (g *Guard) prune(window float64, now float64) {
	kept := g.state.Events[:0]
	for _, item := range g.state.Events {
		if now-item.Time <= window {
			kept = append(kept, item)
		}
	}
	g.state.Events = kept
}

// Check returns an error when a batch would exceed the cumulative limit.
func (g *Guard) Check(cfg *config.Config, count int, direction string) error {
	if !cfg.Bool("delete_enabled") || count <= 0 {
		return nil
	}
	mu.Lock()
	defer mu.Unlock()
	g.load()
	now := float64(time.Now().UnixNano()) / 1e9
	window := windowSeconds(cfg)
	g.prune(window, now)
	total := count
	for _, item := range g.state.Events {
		total += item.Count
	}
	if total <= maxPerWindow(cfg) || now < g.state.ConfirmedUntil {
		return nil
	}
	return &BlockedError{Total: total, Limit: maxPerWindow(cfg)}
}

// Record adds a completed batch to the rolling history.
func (g *Guard) Record(cfg *config.Config, count int, direction string) error {
	if !cfg.Bool("delete_enabled") || count <= 0 {
		return nil
	}
	mu.Lock()
	defer mu.Unlock()
	g.load()
	now := float64(time.Now().UnixNano()) / 1e9
	g.prune(windowSeconds(cfg), now)
	g.state.Events = append(g.state.Events, event{Time: now, Count: count, Direction: direction})
	if len(g.state.Events) > maxEvents {
		g.state.Events = g.state.Events[len(g.state.Events)-maxEvents:]
	}
	return g.save()
}

// Confirm allows crossing the limit for a short, explicit window.
func (g *Guard) Confirm(cfg *config.Config) (float64, error) {
	mu.Lock()
	defer mu.Unlock()
	g.load()
	seconds := float64(confirmSeconds)
	if value, ok := cfg.Get("delete_confirmation_seconds").(float64); ok && value >= 60 {
		seconds = value
	}
	g.state.ConfirmedUntil = float64(time.Now().UnixNano())/1e9 + seconds
	return g.state.ConfirmedUntil, g.save()
}

type Snapshot struct {
	Used      int
	Limit     int
	Remaining int
	Confirmed bool
}

func (g *Guard) Snapshot(cfg *config.Config) Snapshot {
	mu.Lock()
	defer mu.Unlock()
	g.load()
	now := float64(time.Now().UnixNano()) / 1e9
	g.prune(windowSeconds(cfg), now)
	used := 0
	for _, item := range g.state.Events {
		used += item.Count
	}
	limit := maxPerWindow(cfg)
	remaining := limit - used
	if remaining < 0 {
		remaining = 0
	}
	return Snapshot{
		Used: used, Limit: limit, Remaining: remaining,
		Confirmed: now < g.state.ConfirmedUntil,
	}
}

type BlockedError struct {
	Total, Limit int
}

func (e *BlockedError) Error() string {
	return "operazione bloccata: troppi file cancellati nell'ultima ora"
}
