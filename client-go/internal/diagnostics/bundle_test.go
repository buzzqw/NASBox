package diagnostics

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCreateBundleIsRedactedCompleteAndPrivate(t *testing.T) {
	base := t.TempDir()
	stateDir := filepath.Join(base, "state")
	configPath := filepath.Join(base, "client.json")
	outputPath := filepath.Join(base, "support.tar.gz")
	config := `{
		"nas_user": "andres",
		"local_root": "/home/andres/NASBox",
		"password": "config-secret",
		"mirrors": [{"source": "/data", "token": "mirror-secret"}],
		"ssh_known_hosts": "nas.example ssh-ed25519 AAAAKNOWN",
		"private_key_path": "/home/andres/.ssh/id_ed25519"
	}`
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	audit := `{"reports":[{"status":"error","error":"api_key=audit-secret","differences":[{"path":"a.txt","detail":"token=audit-detail-secret"}]}]}`
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, "integrity-audit.json"), []byte(audit), 0o600); err != nil {
		t.Fatal(err)
	}
	// This file must never be included: bundles read an allow-list, not a walk.
	if err := os.WriteFile(filepath.Join(stateDir, "id_ed25519"), []byte("PRIVATE-FILE-BODY"), 0o600); err != nil {
		t.Fatal(err)
	}

	logger, err := openLogger(stateDir, 220, 2)
	if err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 8; index++ {
		action := "SYNC"
		detail := "password=log-secret"
		if index == 7 {
			action = "SYNC_ERROR"
			detail = "password=log-secret\n-----BEGIN OPENSSH PRIVATE KEY-----\nPRIVATE-KEY-BODY\n-----END OPENSSH PRIVATE KEY-----"
		}
		if err := logger.Log(action, "file.txt", detail); err != nil {
			t.Fatal(err)
		}
		if err := logger.Event(action, "file.txt", "token=event-secret"); err != nil {
			t.Fatal(err)
		}
	}
	if err := logger.Close(); err != nil {
		t.Fatal(err)
	}

	err = CreateBundle(BundleOptions{
		Output:     outputPath,
		StateDir:   stateDir,
		ConfigPath: configPath,
		Versions: map[string]any{
			"client_version": "0.16.1",
			"go_version":     "go1.27",
			"server_error":   "token=version-secret",
		},
		Warnings: []string{"password=warning-secret"},
	})
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(outputPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("bundle mode = %o, want 600", info.Mode().Perm())
	}

	files := readBundle(t, outputPath)
	for _, required := range []string{
		bundlePrefix + "config/client.json",
		bundlePrefix + "logs/client.log",
		bundlePrefix + "logs/client.log.1",
		bundlePrefix + "logs/client.log.2",
		bundlePrefix + "logs/events.jsonl",
		bundlePrefix + "integrity-audit.json",
		bundlePrefix + "versions.json",
		bundlePrefix + "manifest.json",
	} {
		if _, ok := files[required]; !ok {
			t.Fatalf("bundle missing %s; entries: %v", required, sortedKeys(files))
		}
	}
	if _, ok := files[bundlePrefix+"logs/id_ed25519"]; ok {
		t.Fatal("private key entered the bundle")
	}

	var combined strings.Builder
	for name, data := range files {
		if name == bundlePrefix+"manifest.json" {
			continue
		}
		combined.Write(data)
	}
	for _, secret := range []string{
		"config-secret", "mirror-secret", "AAAAKNOWN", "id_ed25519",
		"audit-secret", "audit-detail-secret", "log-secret", "PRIVATE-KEY-BODY",
		"event-secret", "version-secret", "warning-secret", "PRIVATE-FILE-BODY",
	} {
		if strings.Contains(combined.String(), secret) {
			t.Fatalf("bundle leaked %q", secret)
		}
	}
	if !strings.Contains(combined.String(), redacted) {
		t.Fatal("bundle has no redaction markers")
	}

	var configSnapshot map[string]any
	if err := json.Unmarshal(files[bundlePrefix+"config/client.json"], &configSnapshot); err != nil {
		t.Fatal(err)
	}
	if configSnapshot["password"] != redacted || configSnapshot["nas_user"] != "andres" {
		t.Fatalf("config snapshot is not correctly redacted: %v", configSnapshot)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(files[bundlePrefix+"logs/events.jsonl"])), "\n") {
		var event any
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatalf("bundled event is not JSONL: %v\n%s", err, line)
		}
	}

	var manifest bundleManifest
	if err := json.Unmarshal(files[bundlePrefix+"manifest.json"], &manifest); err != nil {
		t.Fatal(err)
	}
	if !manifest.Redacted || manifest.SchemaVersion != 1 || len(manifest.Files) == 0 {
		t.Fatalf("invalid bundle manifest: %+v", manifest)
	}
	if len(manifest.Warnings) != 1 || strings.Contains(manifest.Warnings[0], "warning-secret") {
		t.Fatalf("bundle warning was not redacted: %+v", manifest.Warnings)
	}
	for _, entry := range manifest.Files {
		data, ok := files[entry.Name]
		if !ok {
			t.Fatalf("manifest references missing file %s", entry.Name)
		}
		digest := sha256.Sum256(data)
		if entry.Size != int64(len(data)) || entry.SHA256 != hex.EncodeToString(digest[:]) {
			t.Fatalf("manifest checksum mismatch for %s", entry.Name)
		}
	}
}

func TestCreateBundleRefusesToOverwriteDiagnosticInputs(t *testing.T) {
	base := t.TempDir()
	configPath := filepath.Join(base, "client.json")
	original := []byte(`{"password":"keep-secret"}`)
	if err := os.WriteFile(configPath, original, 0o600); err != nil {
		t.Fatal(err)
	}
	err := CreateBundle(BundleOptions{
		Output:     configPath,
		StateDir:   filepath.Join(base, "state"),
		ConfigPath: configPath,
	})
	if err == nil || !strings.Contains(err.Error(), "sovrascrivere") {
		t.Fatalf("overwriting a diagnostic input should fail, got %v", err)
	}
	got, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(original) {
		t.Fatal("failed bundle changed the config input")
	}
}

func TestCreateBundleRedactsBrokenConfig(t *testing.T) {
	base := t.TempDir()
	configPath := filepath.Join(base, "client.json")
	if err := os.WriteFile(configPath, []byte(`{"password":"broken-config-secret"`), 0o600); err != nil {
		t.Fatal(err)
	}
	outputPath := filepath.Join(base, "bundle.tar.gz")
	if err := CreateBundle(BundleOptions{
		Output:     outputPath,
		StateDir:   filepath.Join(base, "state"),
		ConfigPath: configPath,
	}); err != nil {
		t.Fatal(err)
	}
	files := readBundle(t, outputPath)
	content := string(files[bundlePrefix+"config/client.json"])
	if strings.Contains(content, "broken-config-secret") || !strings.Contains(content, redacted) {
		t.Fatalf("broken config was not safely redacted: %s", content)
	}
}

func readBundle(t *testing.T, path string) map[string][]byte {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	gzipReader, err := gzip.NewReader(file)
	if err != nil {
		t.Fatal(err)
	}
	defer gzipReader.Close()
	tarReader := tar.NewReader(gzipReader)
	files := make(map[string][]byte)
	for {
		header, err := tarReader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if header.Typeflag != tar.TypeReg || !strings.HasPrefix(header.Name, bundlePrefix) ||
			strings.Contains(header.Name, "..") {
			t.Fatalf("unsafe bundle entry: %+v", header)
		}
		data, err := io.ReadAll(tarReader)
		if err != nil {
			t.Fatal(err)
		}
		files[header.Name] = data
	}
	return files
}

func sortedKeys(values map[string][]byte) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	for left := 1; left < len(keys); left++ {
		for right := left; right > 0 && keys[right] < keys[right-1]; right-- {
			keys[right], keys[right-1] = keys[right-1], keys[right]
		}
	}
	return keys
}
