package update

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"nasbox-go/internal/config"
)

type fakeReader struct {
	files map[string][]byte
}

func (f *fakeReader) ReadRemoteFile(path string) ([]byte, error) {
	data, ok := f.files[path]
	if !ok {
		return nil, os.ErrNotExist
	}
	return data, nil
}

func TestNewestAndDownloadVerifySha256(t *testing.T) {
	cfg := config.New("")
	cfg.Set("remote_prefix", "/volume1/NASBox", false)
	path := ManifestPath(cfg)
	binary := []byte("fake-go-binary")
	sum := sha256.Sum256(binary)
	manifestJSON := fmt.Sprintf(
		`{"version":"9.9.9","channel":"go-stable","artifact":"nasbox-go-9.9.9","sha256":"%s","size":%d}`,
		hex.EncodeToString(sum[:]), len(binary),
	)
	reader := &fakeReader{files: map[string][]byte{
		path: []byte(manifestJSON),
		"/volume1/NASBox/.nasbox-client-update-go/nasbox-go-9.9.9": binary,
	}}
	manifest, err := Newest(cfg, reader)
	if err != nil || manifest.Version != "9.9.9" || !Available(manifest) {
		t.Fatalf("manifest wrong: %+v %v", manifest, err)
	}
	destination := filepath.Join(t.TempDir(), "nasbox")
	if err := Download(cfg, reader, manifest, destination); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(destination)
	if err != nil || string(content) != string(binary) {
		t.Fatalf("download wrong: %q %v", content, err)
	}
}

func TestDownloadRejectsWrongSha(t *testing.T) {
	cfg := config.New("")
	cfg.Set("remote_prefix", "/volume1/NASBox", false)
	manifest := &Manifest{Artifact: "x", SHA256: "deadbeef", Size: 1}
	reader := &fakeReader{files: map[string][]byte{
		"/volume1/NASBox/.nasbox-client-update-go/x": []byte("different"),
	}}
	if err := Download(cfg, reader, manifest, filepath.Join(t.TempDir(), "nasbox")); err == nil {
		t.Fatal("expected sha256 mismatch")
	}
}
