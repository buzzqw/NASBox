// Package config reads and writes the same client.json as the Python client.
// Defaults are embedded from Python's DEFAULTS so both clients agree.
package config

import (
	_ "embed"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
)

//go:embed defaults.json
var rawDefaults []byte

// Config is a thread-safe view of client.json.
type Config struct {
	mu   sync.RWMutex
	Data map[string]any
	Path string
}

// New returns a config with defaults, optionally backed by path.
func New(path string) *Config {
	data := map[string]any{}
	_ = json.Unmarshal(rawDefaults, &data)
	return &Config{Data: data, Path: path}
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
func LoadFrom(path string) (*Config, error) {
	cfg := New(path)
	content, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}
		return cfg, err
	}
	fromFile := map[string]any{}
	if err := json.Unmarshal(content, &fromFile); err != nil {
		return cfg, err
	}
	for key, value := range fromFile {
		cfg.Data[key] = value
	}
	return cfg, nil
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

func (c *Config) Set(key string, value any, persist bool) {
	c.mu.Lock()
	c.Data[key] = value
	c.mu.Unlock()
	if persist && c.Path != "" {
		_ = c.Save()
	}
}

// Save writes the config atomically so a crash cannot leave a half JSON file.
func (c *Config) Save() error {
	if c.Path == "" {
		return os.ErrInvalid
	}
	if err := os.MkdirAll(filepath.Dir(c.Path), 0o700); err != nil {
		return err
	}
	c.mu.RLock()
	content, err := json.MarshalIndent(c.Data, "", "  ")
	c.mu.RUnlock()
	if err != nil {
		return err
	}
	tmp := c.Path + ".tmp"
	if err := os.WriteFile(tmp, content, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, c.Path)
}

func (c *Config) LocalRoot() string    { return c.String("local_root") }
func (c *Config) RemoteScript() string { return c.String("remote_server_script") }
func (c *Config) RepositoryID() string { return c.String("repository_id") }
func (c *Config) RemotePrefix() string { return c.String("remote_prefix") }
