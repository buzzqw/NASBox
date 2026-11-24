package update

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"nasbox-go/internal/config"
	"nasbox-go/internal/version"
)

const (
	testRemote    = "/volume1/NASBox"
	testBinaryURL = testRemote + "/.nasbox-client-update-go"
)

type fakeReader struct {
	files         map[string][]byte
	serverVersion string
}

func (f *fakeReader) ReadRemoteFile(path string) ([]byte, error) {
	data, ok := f.files[path]
	if !ok {
		return nil, os.ErrNotExist
	}
	return data, nil
}

func (f *fakeReader) ServerVersion() (string, error) { return f.serverVersion, nil }

// streamingReader exercises the zero-copy path that never buffers the whole
// artifact: it writes the remote content in small chunks.
type streamingReader struct{ *fakeReader }

func (s *streamingReader) StreamRemoteFile(path string, destination io.Writer) error {
	data, err := s.fakeReader.ReadRemoteFile(path)
	if err != nil {
		return err
	}
	for offset := 0; offset < len(data); offset += 3 {
		end := offset + 3
		if end > len(data) {
			end = len(data)
		}
		if _, err := destination.Write(data[offset:end]); err != nil {
			return err
		}
	}
	return nil
}

func makeSetup(t *testing.T, versionName string, binary []byte, extra string) (*config.Config, *fakeReader, string) {
	t.Helper()
	cfg := config.New("")
	cfg.Set("remote_prefix", testRemote, false)
	sum := sha256.Sum256(binary)
	artifact := "nasbox-go-" + versionName
	fields := fmt.Sprintf(`"version":%q,"channel":"go-stable","artifact":%q,"sha256":%q,"size":%d,"target":%q`,
		versionName, artifact, hex.EncodeToString(sum[:]), len(binary), runtime.GOOS+"/"+runtime.GOARCH)
	if extra != "" {
		fields += "," + extra
	}
	manifestJSON := "{" + fields + "}"
	reader := &fakeReader{files: map[string][]byte{
		ManifestPath(cfg):              []byte(manifestJSON),
		testBinaryURL + "/" + artifact: binary,
	}}
	return cfg, reader, manifestJSON
}

func TestNewestAndDownloadVerifySha256(t *testing.T) {
	binary := []byte("fake-go-binary")
	cfg, reader, _ := makeSetup(t, "9.9.9", binary, "")
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

func TestStreamingDownloadVerifiesDuringCopy(t *testing.T) {
	binary := []byte("streamed-go-binary-with-a-longer-body")
	cfg, reader, _ := makeSetup(t, "9.9.9", binary, "")
	manifest, err := Newest(cfg, reader)
	if err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(t.TempDir(), "nasbox")
	streamer := &streamingReader{fakeReader: reader}
	if err := Download(cfg, streamer, manifest, destination); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(destination)
	if err != nil || string(content) != string(binary) {
		t.Fatalf("streamed download wrong: %q %v", content, err)
	}
	info, err := os.Stat(destination)
	if err != nil || info.Mode().Perm()&0o100 == 0 {
		t.Fatalf("downloaded binary is not executable: %v %v", info, err)
	}
}

func TestAvailableRefusesDowngradeAndInvalidVersion(t *testing.T) {
	binary := []byte("x")
	_, _, downgradeJSON := makeSetup(t, "0.1.0", binary, "")
	cfg := config.New("")
	cfg.Set("remote_prefix", testRemote, false)
	reader := &fakeReader{files: map[string][]byte{ManifestPath(cfg): []byte(downgradeJSON)}}
	manifest, err := Newest(cfg, reader)
	if err != nil {
		t.Fatal(err)
	}
	if Available(manifest) {
		t.Fatal("downgrade must not be offered")
	}
	// The running version itself is not an upgrade either.
	_, _, currentJSON := makeSetup(t, version.AppVersion, binary, "")
	current := &Manifest{}
	if err := current.UnmarshalJSON([]byte(currentJSON)); err != nil {
		t.Fatal(err)
	}
	if Available(current) {
		t.Fatal("same version must not be offered")
	}
	if Available(&Manifest{Version: "not-a-version", Channel: "go-stable"}) {
		t.Fatal("invalid version must not be offered")
	}
}

func TestNewestRejectsWrongChannelTargetAndVersion(t *testing.T) {
	binary := []byte("binary")
	tests := []struct {
		name   string
		edit   func(string) string
		wantIn string
	}{
		{"canale sbagliato", func(s string) string {
			return strings.Replace(s, `"channel":"go-stable"`, `"channel":"python-stable"`, 1)
		}, "canale"},
		{"versione non semver", func(s string) string {
			return strings.Replace(s, `"version":"9.9.9"`, `"version":"9.9.x"`, 1)
		}, "versione"},
		{"target sbagliato", func(s string) string {
			other := "amd64"
			if runtime.GOARCH == "amd64" {
				other = "arm64"
			}
			return strings.Replace(s, runtime.GOOS+"/"+runtime.GOARCH, runtime.GOOS+"/"+other, 1)
		}, "artifact Go per"},
		{"target mancante", func(s string) string {
			return strings.Replace(s, `,"target":"`+runtime.GOOS+"/"+runtime.GOARCH+`"`, "", 1)
		}, "target"},
		{"requires_server non valido", func(s string) string {
			return strings.Replace(s, `"size":6`, `"size":6,"requires_server":"3.x"`, 1)
		}, "requires_server"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg, reader, manifestJSON := makeSetup(t, "9.9.9", binary, "")
			manifestJSON = test.edit(manifestJSON)
			reader.files[ManifestPath(cfg)] = []byte(manifestJSON)
			if _, err := Newest(cfg, reader); err == nil || !strings.Contains(err.Error(), test.wantIn) {
				t.Fatalf("expected rejection containing %q, got %v", test.wantIn, err)
			}
		})
	}
}

func TestCheckServerRequirement(t *testing.T) {
	manifest := &Manifest{RequiresServer: "3.25.0"}
	if err := CheckServerRequirement(manifest, "3.24.9"); err == nil {
		t.Fatal("outdated server must be rejected")
	}
	if err := CheckServerRequirement(manifest, "3.26.0"); err != nil {
		t.Fatal("newer server must be accepted:", err)
	}
	if err := CheckServerRequirement(manifest, ""); err == nil {
		t.Fatal("unknown server version must be rejected")
	}
	manifest.RequiresServer = ""
	if err := CheckServerRequirement(manifest, ""); err != nil {
		t.Fatal("no requirement means no check:", err)
	}
}

func TestApplyRefusesDowngradeAndOldServer(t *testing.T) {
	oldBinary := []byte("old")
	binaryPath := filepath.Join(t.TempDir(), "nasbox")
	if err := os.WriteFile(binaryPath, oldBinary, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg, reader, _ := makeSetup(t, "0.1.0", []byte("new-binary"), "")
	applied, _, err := Apply(cfg, reader, binaryPath)
	if err != nil || applied {
		t.Fatalf("downgrade applied: %v %v", applied, err)
	}
	content, _ := os.ReadFile(binaryPath)
	if string(content) != string(oldBinary) {
		t.Fatalf("binary changed on downgrade: %q", content)
	}

	cfg, reader, _ = makeSetup(t, "9.9.9", []byte("new-binary"), `"requires_server":"3.25.0"`)
	reader.serverVersion = "3.24.0"
	applied, _, err = Apply(cfg, reader, binaryPath)
	if err == nil || applied {
		t.Fatalf("old server must block update: %v %v", applied, err)
	}
	if _, err := os.Stat(binaryPath + ".bak"); !os.IsNotExist(err) {
		t.Fatal("backup must not exist when the update was refused")
	}
}

func TestApplyInstallsAndKeepsBackup(t *testing.T) {
	cfg, reader, _ := makeSetup(t, "9.9.9", []byte("new-binary"), "")
	binaryPath := filepath.Join(t.TempDir(), "nasbox")
	if err := os.WriteFile(binaryPath, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	applied, versionName, err := Apply(cfg, reader, binaryPath)
	if err != nil || !applied || versionName != "9.9.9" {
		t.Fatalf("apply failed: %v %s %v", applied, versionName, err)
	}
	content, _ := os.ReadFile(binaryPath)
	if string(content) != "new-binary" {
		t.Fatalf("binary not replaced: %q", content)
	}
	backup, err := os.ReadFile(binaryPath + ".bak")
	if err != nil || string(backup) != "old" {
		t.Fatalf("rollback copy missing or wrong: %q %v", backup, err)
	}
	state, err := readState(binaryPath)
	if err != nil || state == nil || state.State != StateNewInstalled || state.Version != "9.9.9" {
		t.Fatalf("state wrong: %+v %v", state, err)
	}
	if state.Backup != binaryPath+".bak" {
		t.Fatalf("backup path not recorded: %+v", state)
	}
}

func TestApplyPromotesRunningNewInstalled(t *testing.T) {
	cfg, reader, _ := makeSetup(t, "9.9.9", []byte("new-binary"), "")
	binaryPath := filepath.Join(t.TempDir(), "nasbox")
	if err := os.WriteFile(binaryPath, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Simulate the first start after an update: the binary already runs the
	// new version, but only one start has been recorded so far.
	if err := writeState(binaryPath, &UpdateState{
		State: StateNewInstalled, Version: version.AppVersion,
		Binary: binaryPath, Backup: binaryPath + ".bak", ConfirmedStarts: 1,
	}); err != nil {
		t.Fatal(err)
	}
	applied, versionName, err := Apply(cfg, reader, binaryPath)
	if err != nil {
		t.Fatalf("a running new binary must not block the next update: %v", err)
	}
	if !applied || versionName != "9.9.9" {
		t.Fatalf("update not applied: %v %q", applied, versionName)
	}
}

func TestStateWrittenBeforeFirstRename(t *testing.T) {
	cfg, reader, _ := makeSetup(t, "9.9.9", []byte("new-binary"), "")
	binaryPath := filepath.Join(t.TempDir(), "nasbox")
	if err := os.WriteFile(binaryPath, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	// A non-empty directory at the backup path makes the first rename fail.
	backupDir := binaryPath + ".bak"
	if err := os.Mkdir(backupDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(backupDir, "block"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Apply(cfg, reader, binaryPath); err == nil {
		t.Fatal("expected the first rename to fail")
	}
	// No rename happened, but a state file was already committed.
	content, _ := os.ReadFile(binaryPath)
	if string(content) != "old" {
		t.Fatalf("binary moved despite failed backup: %q", content)
	}
	state, err := readState(binaryPath)
	if err != nil || state == nil || state.State == "" {
		t.Fatalf("state was not written before the rename: %+v %v", state, err)
	}
}

func TestDownloadRejectsWrongShaAndSize(t *testing.T) {
	cfg := config.New("")
	cfg.Set("remote_prefix", testRemote, false)
	manifest := &Manifest{Artifact: "x", SHA256: strings.Repeat("0", 64), Size: 1,
		Channel: channelGo, Version: "9.9.9", Target: runtime.GOOS + "/" + runtime.GOARCH}
	reader := &fakeReader{files: map[string][]byte{testBinaryURL + "/x": []byte("different")}}
	if err := Download(cfg, reader, manifest, filepath.Join(t.TempDir(), "nasbox")); err == nil {
		t.Fatal("expected sha256 mismatch")
	}
	manifest.Size = 2
	if err := Download(cfg, reader, manifest, filepath.Join(t.TempDir(), "nasbox")); err == nil {
		t.Fatal("expected size mismatch")
	}
}

func TestRollbackFromOldMoved(t *testing.T) {
	binaryPath := filepath.Join(t.TempDir(), "nasbox")
	if err := os.WriteFile(binaryPath+".bak", []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Crash simulation: the original was moved away, the new binary is not
	// yet installed and the state was already persisted.
	if err := writeState(binaryPath, &UpdateState{
		State:   StateOldMoved,
		Version: "9.9.9",
		Binary:  binaryPath,
		Backup:  binaryPath + ".bak",
	}); err != nil {
		t.Fatal(err)
	}
	if err := RollbackIfNeeded(binaryPath); !errors.Is(err, ErrRollbackPerformed) {
		t.Fatalf("expected rollback, got %v", err)
	}
	content, err := os.ReadFile(binaryPath)
	if err != nil || string(content) != "old" {
		t.Fatalf("binary not restored: %q %v", content, err)
	}
	state, err := readState(binaryPath)
	if err != nil || state == nil || state.State != StateFailed {
		t.Fatalf("state not marked failed: %+v %v", state, err)
	}
}

func healthyScript(t *testing.T, path, printed string, exitOK bool) {
	t.Helper()
	exitCode := "0"
	if !exitOK {
		exitCode = "1"
	}
	script := fmt.Sprintf("#!/bin/sh\necho '%s'\nexit %s\n", printed, exitCode)
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
}

func TestConfirmStartedOK(t *testing.T) {
	cfg, reader, _ := makeSetup(t, "9.9.9", []byte("placeholder"), "")
	binaryPath := filepath.Join(t.TempDir(), "nasbox")
	if err := os.WriteFile(binaryPath, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Apply(cfg, reader, binaryPath); err != nil {
		t.Fatal(err)
	}
	healthyScript(t, binaryPath, "NASBox Go 9.9.9", true)
	if err := ConfirmStartedOK(binaryPath); err != nil {
		t.Fatal(err)
	}
	state, err := readState(binaryPath)
	if err != nil || state.ConfirmedStarts != 1 || state.State != StateNewInstalled {
		t.Fatalf("first confirmation not recorded: %+v %v", state, err)
	}
	if err := ConfirmStartedOK(binaryPath); err != nil {
		t.Fatal(err)
	}
	state, err = readState(binaryPath)
	if err != nil || state.State != StateConfirmed {
		t.Fatalf("second confirmation failed: %+v %v", state, err)
	}
	if _, err := os.Stat(binaryPath + ".bak"); !os.IsNotExist(err) {
		t.Fatal("backup should be removed after confirmation")
	}
	if err := RollbackIfNeeded(binaryPath); err != nil {
		t.Fatal("confirmed update must not be rolled back:", err)
	}
}

func TestConfirmFailureRollsBackAfterTwoStarts(t *testing.T) {
	cfg, reader, _ := makeSetup(t, "9.9.9", []byte("placeholder"), "")
	binaryPath := filepath.Join(t.TempDir(), "nasbox")
	if err := os.WriteFile(binaryPath, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Apply(cfg, reader, binaryPath); err != nil {
		t.Fatal(err)
	}
	healthyScript(t, binaryPath, "NASBox Go broken", false)
	if err := ConfirmStartedOK(binaryPath); err == nil {
		t.Fatal("first failed health check should be reported")
	}
	if err := ConfirmStartedOK(binaryPath); !errors.Is(err, ErrRollbackPerformed) {
		t.Fatalf("second failed health check must roll back, got %v", err)
	}
	content, err := os.ReadFile(binaryPath)
	if err != nil || string(content) != "old" {
		t.Fatalf("binary not restored after failed starts: %q %v", content, err)
	}
	state, err := readState(binaryPath)
	if err != nil || state == nil || state.State != StateFailed {
		t.Fatalf("failed state missing: %+v %v", state, err)
	}
}
