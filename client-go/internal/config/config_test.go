package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
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

func TestModeDefaultsToNormalAndGhostIsOptIn(t *testing.T) {
	cfg := New("")
	if cfg.Mode() != "normal" || cfg.Ghost() {
		t.Fatalf("default must be normal sync, got %q", cfg.Mode())
	}
	if err := cfg.Set("mode", " GHOST ", false); err != nil {
		t.Fatal(err)
	}
	if cfg.Mode() != "ghost" || !cfg.Ghost() {
		t.Fatalf("ghost mode not recognised: %q", cfg.Mode())
	}
	if err := cfg.Set("mode", "normal", false); err != nil {
		t.Fatal(err)
	}
	if cfg.Ghost() {
		t.Fatal("normal mode must never report ghost")
	}
}

func TestTransferBackendNegotiationDefaults(t *testing.T) {
	cfg := New("")
	available, ok := cfg.Get("remote_chunk_transfer_available").(bool)
	if !ok || available {
		t.Fatalf("remote_chunk_transfer_available default = %#v, want false", cfg.Get("remote_chunk_transfer_available"))
	}
	effective, ok := cfg.Get("effective_transfer_backend").(string)
	if !ok || effective != "" {
		t.Fatalf("effective_transfer_backend default = %#v, want empty string", cfg.Get("effective_transfer_backend"))
	}
}

func TestSaveIsAtomic(t *testing.T) {
	path := filepath.Join(t.TempDir(), "client.json")
	cfg := New(path)
	if err := cfg.Set("nas_user", "andres", true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path + ".tmp." + fmt.Sprint(os.Getpid())); !os.IsNotExist(err) {
		t.Fatal("temporary file was left behind")
	}
	if _, err := os.Stat(path + ".lock"); err != nil {
		t.Fatal("lock file missing:", err)
	}
	reloaded, err := LoadFrom(path)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.String("nas_user") != "andres" {
		t.Fatalf("save did not persist: %q", reloaded.String("nas_user"))
	}
}

// TestConcurrentSaveChecksErrors simulates several independent Config views
// (as separate client processes would have) writing different keys at once.
// The interprocess lock must preserve every key and no write may be lost.
func TestConcurrentSaveChecksErrors(t *testing.T) {
	path := filepath.Join(t.TempDir(), "client.json")
	workers, perWorker := 8, 12
	var wg sync.WaitGroup
	errs := make(chan error, workers*perWorker)
	for worker := 0; worker < workers; worker++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			cfg, err := LoadFrom(path)
			if err != nil {
				errs <- err
				return
			}
			for i := 0; i < perWorker; i++ {
				key := fmt.Sprintf("key_%d_%d", worker, i)
				if err := cfg.Set(key, worker*perWorker+i, true); err != nil {
					errs <- err
				}
			}
		}(worker)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal("concurrent save failed:", err)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	data := map[string]any{}
	if err := json.Unmarshal(content, &data); err != nil {
		t.Fatal(err)
	}
	for worker := 0; worker < workers; worker++ {
		for i := 0; i < perWorker; i++ {
			key := fmt.Sprintf("key_%d_%d", worker, i)
			got, ok := data[key].(float64)
			if !ok || int(got) != worker*perWorker+i {
				t.Fatalf("lost concurrent write %s: %v", key, data[key])
			}
		}
	}
	// Defaults must survive the concurrent rewrites.
	if data["max_delete_files_per_window"] != float64(1000) {
		t.Fatal("defaults lost after concurrent saves")
	}
}

func TestSaveReturnsErrorOnUnwritablePath(t *testing.T) {
	dir := t.TempDir()
	blocker := filepath.Join(dir, "not-a-directory")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := New(filepath.Join(blocker, "nested", "client.json"))
	if err := cfg.Set("nas_user", "andres", true); err == nil {
		t.Fatal("Set(persist=true) must surface the save error")
	}
}
