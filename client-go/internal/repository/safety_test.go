package repository

import (
	"encoding/json"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestBindRefusesDifferentRepository(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	root := t.TempDir()
	if err := Initialize(root, "repo1"); err != nil {
		t.Fatal(err)
	}
	if err := Bind(root, "repo2"); err == nil {
		t.Fatal("bind must refuse a different repository")
	}
	if err := Bind(root, "repo1"); err != nil {
		t.Fatalf("same repository bind failed: %v", err)
	}
}

func TestValidateRejectsWrongVolume(t *testing.T) {
	base := t.TempDir()
	t.Setenv("XDG_STATE_HOME", base)
	root := t.TempDir()
	info, err := os.Stat(root)
	if err != nil {
		t.Fatal(err)
	}
	raw := info.Sys().(*syscall.Stat_t)
	value := marker{Version: 1, LocalRoot: root, Dev: raw.Dev + 1, RepositoryID: "repo1"}
	content, _ := json.Marshal(value)
	path := filepath.Join(base, "sync-daemon", "repository-marker.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Validate(root, "repo1", true); err == nil {
		t.Fatal("wrong st_dev must be rejected")
	}
	value.Dev = raw.Dev
	content, _ = json.Marshal(value)
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Validate(root, "repo1", true); err != nil {
		t.Fatalf("valid marker rejected: %v", err)
	}
}
