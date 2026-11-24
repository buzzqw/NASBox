// Package update implements the separate Go-channel updater. The Python
// channel is untouched: old clients keep reading manifest.json, the Go client
// reads manifest-go.json in its own directory.
package update

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"nasbox-go/internal/config"
	"nasbox-go/internal/version"
)

type Manifest struct {
	Version  string `json:"version"`
	Channel  string `json:"channel"`
	Artifact string `json:"artifact"`
	SHA256   string `json:"sha256"`
	Size     int64  `json:"size"`
	Notes    string `json:"notes"`
}

// ManifestPath follows remote_prefix + client_update_remote_path_go.
func ManifestPath(cfg *config.Config) string {
	prefix := strings.TrimSuffix(cfg.RemotePrefix(), "/")
	relative := strings.Trim(cfg.String("client_update_remote_path_go"), "/")
	if relative == "" {
		relative = ".nasbox-client-update-go"
	}
	return fmt.Sprintf("%s/%s/manifest-go.json", prefix, relative)
}

// FileReader is the small subset of the remote client used by the updater.
type FileReader interface {
	ReadRemoteFile(path string) ([]byte, error)
}

func Newest(cfg *config.Config, client FileReader) (*Manifest, error) {
	raw, err := client.ReadRemoteFile(ManifestPath(cfg))
	if err != nil {
		return nil, err
	}
	manifest := &Manifest{}
	if err := json.Unmarshal(raw, manifest); err != nil {
		return nil, err
	}
	return manifest, nil
}

// Available reports whether the remote manifest advertises a newer version.
func Available(manifest *Manifest) bool {
	return manifest != nil && manifest.Version != "" && manifest.Version != version.AppVersion
}

// Download verifies sha256 before making the artifact usable.
func Download(cfg *config.Config, client FileReader, manifest *Manifest, destination string) error {
	if manifest == nil || manifest.Artifact == "" {
		return fmt.Errorf("manifest go non valido")
	}
	prefix := strings.TrimSuffix(cfg.RemotePrefix(), "/")
	relative := strings.Trim(cfg.String("client_update_remote_path_go"), "/")
	artifactPath := fmt.Sprintf("%s/%s/%s", prefix, relative, manifest.Artifact)
	raw, err := client.ReadRemoteFile(artifactPath)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(raw)
	if hex.EncodeToString(sum[:]) != manifest.SHA256 {
		return fmt.Errorf("sha256 dell'artifact Go non corrisponde")
	}
	if manifest.Size > 0 && int64(len(raw)) != manifest.Size {
		return fmt.Errorf("dimensione dell'artifact Go non corrisponde")
	}
	tmp := destination + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o755); err != nil {
		return err
	}
	return os.Rename(tmp, destination)
}

// Apply checks, downloads and installs an available update, keeping a backup.
// It returns true when a new binary is ready and a restart is required.
func Apply(cfg *config.Config, client FileReader, binaryPath string) (bool, string, error) {
	manifest, err := Newest(cfg, client)
	if err != nil {
		return false, "", err
	}
	if !Available(manifest) {
		return false, "", nil
	}
	newBinary := binaryPath + ".new"
	if err := Download(cfg, client, manifest, newBinary); err != nil {
		return false, "", err
	}
	backup, err := Install(binaryPath)
	if err != nil {
		return false, "", err
	}
	if err := os.Rename(newBinary, binaryPath); err != nil {
		_ = Rollback(binaryPath)
		return false, "", err
	}
	_ = backup
	return true, manifest.Version, nil
}

// Install replaces the running binary after keeping a rollback copy. The new
// binary is not executed here; the tray menu or next start will use it.
func Install(binaryPath string) (string, error) {
	backup := binaryPath + ".bak"
	content, err := os.ReadFile(binaryPath)
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(backup, content, 0o755); err != nil {
		return "", err
	}
	return backup, nil
}

// Rollback restores the previous binary if the new one failed to start.
func Rollback(binaryPath string) error {
	backup := binaryPath + ".bak"
	content, err := os.ReadFile(backup)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Clean(binaryPath), content, 0o755); err != nil {
		return err
	}
	return os.Remove(backup)
}
