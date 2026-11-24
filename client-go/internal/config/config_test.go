package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadOverlaysFileOnDefaults(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sync-daemon", "client.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	content := `{"nas_lan":"192.168.1.119","delete_enabled":true,"local_root":"/tmp/NASBox"}`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadFrom(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.String("nas_lan") != "192.168.1.119" {
		t.Fatalf("wrong host: %q", cfg.String("nas_lan"))
	}
	if !cfg.Bool("delete_enabled") {
		t.Fatal("delete_enabled not read from file")
	}
	// A key not present in the file must still come from the Python defaults.
	if cfg.Int("max_delete_files_per_window") != 1000 {
		t.Fatalf("default lost: %v", cfg.Get("max_delete_files_per_window"))
	}
}

func TestSaveIsAtomic(t *testing.T) {
	path := filepath.Join(t.TempDir(), "client.json")
	cfg := New(path)
	cfg.Set("nas_user", "andres", false)
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Fatal("temporary file was left behind")
	}
	reloaded, err := LoadFrom(path)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.String("nas_user") != "andres" {
		t.Fatalf("save did not persist: %q", reloaded.String("nas_user"))
	}
}
