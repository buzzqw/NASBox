// Package config reads and writes the same client.json as the Python client.
// Defaults are embedded from Python's DEFAULTS so both clients agree.
package config

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"nasbox-go/internal/filelock"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
)

//go:embed defaults.json
var rawDefaults []byte

// Config is a thread-safe view of client.json.
type Config struct {
	mu    sync.RWMutex
	Data  map[string]any
	Path  string
	dirty map[string]bool // keys changed since the last successful Save
	// revision increases on every change, so derived data (e.g. the compiled
	// exclusion rules) can be cached cheaply.
	revision atomic.Uint64
}

// Revision changes whenever a value is set or the file is reloaded.
func (c *Config) Revision() uint64 { return c.revision.Load() }

// New returns a config with defaults, optionally backed by path.
func New(path string) *Config {
	return &Config{Data: defaultData(), Path: path, dirty: map[string]bool{}}
}

func defaultData() map[string]any {
	data := map[string]any{}
	// The embedded defaults are generated from Python's DEFAULTS and are known
	// to be valid JSON; a malformed embed would be a build-time problem.
	_ = json.Unmarshal(rawDefaults, &data)
	return data
}

// DefaultPath mirrors the Python client's XDG path.
func DefaultPath() string {
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return filepath.Join(".config", "sync-daemon", "client.json")
		}
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "sync-daemon", "client.json")
}

// Load reads the file if present, overlaying values on the embedded defaults.
func Load() (*Config, error) {
	return LoadFrom(DefaultPath())
}

// LoadFrom is Load with an explicit path (used by tests).
//
// The read is guarded by the same interprocess lock as Save, so a concurrent
// writer in another process can never be observed half-way through a write.
func LoadFrom(path string) (*Config, error) {
	cfg := New(path)
	unlock, err := fileLock(path+".lock", false)
	if err != nil {
		return cfg, err
	}
	defer unlock()
	data, err := readDisk(path)
	if err != nil {
		return cfg, err
	}
	for key, value := range data {
		cfg.Data[key] = value
	}
	return cfg, nil
}

// readDisk reads the JSON object on disk. A missing file is not an error: the
// caller keeps the embedded defaults. This helper must be called while the
// config lock is held.
func readDisk(path string) (map[string]any, error) {
	data := map[string]any{}
	if path == "" {
		return data, nil
	}
	content, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return data, nil
		}
		return data, err
	}
	if err := json.Unmarshal(content, &data); err != nil {
		return data, fmt.Errorf("client.json non valido: %w", err)
	}
	return data, nil
}

// fileLock takes a POSIX advisory lock on path+".lock". It works both across
// processes and across goroutines that opened the lock file separately.
func fileLock(path string, exclusive bool) (func(), error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := filelock.Lock(file, exclusive, false); err != nil {
		_ = file.Close()
		return nil, err
	}
	return func() {
		filelock.Unlock(file)
		_ = file.Close()
	}, nil
}

func (c *Config) Get(key string) any {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.Data[key]
}

func (c *Config) String(key string) string {
	value, _ := c.Get(key).(string)
	return value
}

func (c *Config) Bool(key string) bool {
	value, _ := c.Get(key).(bool)
	return value
}

func (c *Config) Int(key string) int {
	switch value := c.Get(key).(type) {
	case float64:
		return int(value)
	case int:
		return value
	case json.Number:
		n, _ := value.Int64()
		return int(n)
	}
	return 0
}

// Set updates a value in memory and, when persist is true, atomically writes
// the config. It returns any save error: callers that change persistent keys
// must check it instead of silently losing the change.
func (c *Config) Set(key string, value any, persist bool) error {
	if persist && c.Path != "" {
		unlock, err := fileLock(c.Path+".lock", true)
		if err != nil {
			return err
		}
		defer unlock()
		c.mu.Lock()
		c.Data[key] = value
		c.dirty[key] = true
		c.mu.Unlock()
		c.revision.Add(1)
		return c.saveLocked()
	}
	c.mu.Lock()
	c.Data[key] = value
	c.mu.Unlock()
	c.revision.Add(1)
	return nil
}

// Save writes the config atomically so a crash cannot leave a half JSON file.
// A short-lived lock file and a PID-qualified temporary file make concurrent
// writers (including other processes) serialize and never clobber each other.
func (c *Config) Save() error {
	if c.Path == "" {
		return os.ErrInvalid
	}
	unlock, err := fileLock(c.Path+".lock", true)
	if err != nil {
		return err
	}
	defer unlock()
	return c.saveLocked()
}

// saveLocked persists the dirty keys over a fresh snapshot of the file, so a
// writer that only touched key A preserves key B written by another process.
// The caller must hold the config lock.
func (c *Config) saveLocked() error {
	if c.Path == "" {
		return os.ErrInvalid
	}
	disk, err := readDisk(c.Path)
	if err != nil {
		return err
	}
	merged := defaultData()
	for key, value := range disk {
		merged[key] = value
	}
	c.mu.RLock()
	for key := range c.dirty {
		if value, ok := c.Data[key]; ok {
			merged[key] = value
		}
	}
	c.mu.RUnlock()

	content, err := json.MarshalIndent(merged, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(c.Path), 0o700); err != nil {
		return err
	}
	tmp := c.Path + ".tmp." + strconv.Itoa(os.Getpid())
	file, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := file.Write(content); err != nil {
		_ = file.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, c.Path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if dir, err := os.Open(filepath.Dir(c.Path)); err == nil {
		_ = dir.Sync()
		_ = dir.Close()
	}

	c.mu.Lock()
	c.Data = merged
	c.dirty = map[string]bool{}
	c.mu.Unlock()
	c.revision.Add(1) // values written by other processes were merged in
	return nil
}

func (c *Config) LocalRoot() string    { return c.String("local_root") }
func (c *Config) RemoteScript() string { return c.String("remote_server_script") }
func (c *Config) RepositoryID() string { return c.String("repository_id") }

// Mode is "normal" (full two-way sync) or "ghost" (consultation only). Ghost
// mode is opt-in and never changes the default behaviour of an existing client.
func (c *Config) Mode() string {
	mode := strings.ToLower(strings.TrimSpace(c.String("mode")))
	if mode == "" {
		return "normal"
	}
	return mode
}

// Ghost reports whether automatic sync must stay off on this client.
func (c *Config) Ghost() bool { return c.Mode() == "ghost" }

// LocalRepositoryID is the key used in the local SQLite state. It matches the
// Python client's sha256("repository-id\0" + repository_id), so both clients
// read the same rows.
func (c *Config) LocalRepositoryID() string {
	id := c.RepositoryID()
	if id == "" {
		return ""
	}
	sum := sha256.Sum256([]byte("repository-id\x00" + id))
	return hex.EncodeToString(sum[:])
}
func (c *Config) RemotePrefix() string { return c.String("remote_prefix") }
