// Package repository validates the local mount before destructive work.
// It mirrors repository_safety.py and reads the same marker file.
package repository

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

type marker struct {
	Version      int    `json:"version"`
	LocalRoot    string `json:"local_root"`
	Dev          uint64 `json:"st_dev"`
	RepositoryID string `json:"repository_id"`
}

func markerPath() (string, error) {
	base := os.Getenv("XDG_STATE_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		base = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(base, "sync-daemon", "repository-marker.json"), nil
}

// Initialize creates/updates the local marker after an explicit setup.
func Initialize(localRoot, repositoryID string) error {
	root, err := filepath.Abs(localRoot)
	if err != nil {
		return err
	}
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		return fmt.Errorf("cartella locale non accessibile: %s", root)
	}
	path, err := markerPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	raw, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("informazioni filesystem non disponibili")
	}
	value := marker{
		Version: 1, LocalRoot: root, Dev: raw.Dev, RepositoryID: repositoryID,
	}
	content, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(content, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Validate checks path, filesystem device and repository identity.
func Validate(localRoot, repositoryID string, destructive bool) error {
	root, err := filepath.Abs(localRoot)
	if err != nil {
		return err
	}
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		return fmt.Errorf("cartella locale non accessibile: %s", root)
	}
	path, err := markerPath()
	if err != nil {
		return err
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("marker locale assente o non valido: %s", path)
	}
	var value marker
	if err := json.Unmarshal(content, &value); err != nil {
		return fmt.Errorf("marker locale non valido: %s", path)
	}
	markerRoot, err := filepath.Abs(value.LocalRoot)
	if err != nil || markerRoot != root {
		return fmt.Errorf("la cartella locale non e' quella configurata: %s", root)
	}
	raw, ok := info.Sys().(*syscall.Stat_t)
	if !ok || raw.Dev != value.Dev {
		return fmt.Errorf("il filesystem locale e' cambiato (volume non montato?)")
	}
	if destructive && repositoryID != "" && value.RepositoryID != "" && value.RepositoryID != repositoryID {
		return fmt.Errorf("la cartella locale e' associata a un repository NASBox diverso")
	}
	return nil
}
