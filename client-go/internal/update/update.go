// Package update implements the separate Go-channel updater. The Python
// channel is untouched: old clients keep reading manifest.json, the Go client
// reads manifest-go.json in its own directory.
package update

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"

	"nasbox-go/internal/config"
	"nasbox-go/internal/version"
)

const (
	channelGo      = "go-stable"
	stateSuffix    = ".update-state.json"
	maxManifestLen = 1 << 20 // a manifest is a small JSON document
)

// ErrRollbackPerformed reports that an interrupted or failed update was undone
// and the process should be restarted before doing any sync work.
var ErrRollbackPerformed = errors.New("rollback aggiornamento applicato")

// Manifest is the Go channel's manifest. Besides the legacy fields it carries
// platform and server requirements used to refuse an incompatible artifact.
type Manifest struct {
	Version        string `json:"version"`
	Channel        string `json:"channel"`
	Artifact       string `json:"artifact"`
	SHA256         string `json:"sha256"`
	Size           int64  `json:"size"`
	Notes          string `json:"notes"`
	RequiresServer string `json:"requires_server,omitempty"`
	Target         string `json:"target,omitempty"`
	OS             string `json:"os,omitempty"`
	Arch           string `json:"arch,omitempty"`
}

// UnmarshalJSON accepts the common spellings for the platform fields while
// exposing one normalized set to the validation code.
func (m *Manifest) UnmarshalJSON(data []byte) error {
	var raw struct {
		Version        string `json:"version"`
		Channel        string `json:"channel"`
		Artifact       string `json:"artifact"`
		SHA256         string `json:"sha256"`
		Size           int64  `json:"size"`
		Notes          string `json:"notes"`
		RequiresServer string `json:"requires_server"`
		Target         string `json:"target"`
		Platform       string `json:"platform"`
		OS             string `json:"os"`
		GOOS           string `json:"goos"`
		Arch           string `json:"arch"`
		GOARCH         string `json:"goarch"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	*m = Manifest{
		Version:        raw.Version,
		Channel:        raw.Channel,
		Artifact:       raw.Artifact,
		SHA256:         raw.SHA256,
		Size:           raw.Size,
		Notes:          raw.Notes,
		RequiresServer: raw.RequiresServer,
		Target:         raw.Target,
		OS:             raw.OS,
		Arch:           raw.Arch,
	}
	if m.Target == "" {
		m.Target = raw.Platform
	}
	if m.OS == "" {
		m.OS = raw.GOOS
	}
	if m.Arch == "" {
		m.Arch = raw.GOARCH
	}
	return nil
}

// ManifestPath follows remote_prefix + client_update_remote_path_go.
func ManifestPath(cfg *config.Config) string {
	return remoteDir(cfg) + "/manifest-go.json"
}

func remoteDir(cfg *config.Config) string {
	prefix := strings.TrimSuffix(cfg.RemotePrefix(), "/")
	relative := strings.Trim(cfg.String("client_update_remote_path_go"), "/")
	if relative == "" {
		relative = ".nasbox-client-update-go"
	}
	return prefix + "/" + relative
}

// FileReader is the small subset of the remote client used by the updater.
type FileReader interface {
	ReadRemoteFile(path string) ([]byte, error)
}

// StreamReader is an optional interface for remote clients able to stream a
// file into an io.Writer. When implemented, Download never buffers the whole
// artifact in memory.
type StreamReader interface {
	StreamRemoteFile(path string, destination io.Writer) error
}

// ServerVersionProvider is an optional interface used to enforce the
// manifest's requires_server field.
type ServerVersionProvider interface {
	ServerVersion() (string, error)
}

func Newest(cfg *config.Config, client FileReader) (*Manifest, error) {
	raw, err := client.ReadRemoteFile(ManifestPath(cfg))
	if err != nil {
		return nil, err
	}
	if len(raw) > maxManifestLen {
		return nil, fmt.Errorf("manifest Go troppo grande")
	}
	manifest := &Manifest{}
	if err := json.Unmarshal(raw, manifest); err != nil {
		return nil, err
	}
	if err := ValidateManifest(manifest); err != nil {
		return nil, err
	}
	return manifest, nil
}

// ValidateManifest rejects any manifest that is malformed or not meant for
// this client: only the go-stable channel, a strict semver and a matching
// GOOS/GOARCH target are accepted.
func ValidateManifest(manifest *Manifest) error {
	if manifest == nil {
		return fmt.Errorf("manifest Go assente")
	}
	if _, err := parseSemver(manifest.Version); err != nil {
		return err
	}
	if strings.TrimSpace(manifest.Channel) != channelGo {
		return fmt.Errorf("canale Go non valido: %q (atteso %s)", manifest.Channel, channelGo)
	}
	osName, archName := manifest.OS, manifest.Arch
	if osName == "" || archName == "" {
		target := manifest.Target
		if parts := strings.Split(target, "/"); len(parts) == 2 {
			osName, archName = parts[0], parts[1]
		} else if parts := strings.Split(target, "-"); len(parts) == 2 {
			osName, archName = parts[0], parts[1]
		}
	}
	if osName == "" || archName == "" {
		return fmt.Errorf("target Go mancante")
	}
	if osName != runtime.GOOS || archName != runtime.GOARCH {
		return fmt.Errorf("artifact Go per %s/%s, questo client e' %s/%s",
			osName, archName, runtime.GOOS, runtime.GOARCH)
	}
	if !validArtifactName(manifest.Artifact) {
		return fmt.Errorf("nome artifact Go non valido: %q", manifest.Artifact)
	}
	if !validSHA256(manifest.SHA256) {
		return fmt.Errorf("sha256 Go non valido")
	}
	if manifest.Size <= 0 {
		return fmt.Errorf("dimensione artifact Go mancante")
	}
	if manifest.RequiresServer != "" {
		if _, err := parseSemver(manifest.RequiresServer); err != nil {
			return fmt.Errorf("requires_server non valido: %w", err)
		}
	}
	return nil
}

func validArtifactName(name string) bool {
	if name == "" || name == "." || name == ".." || len(name) > 255 {
		return false
	}
	if strings.ContainsAny(name, "/\\\x00\n\r\t") || strings.Contains(name, "..") {
		return false
	}
	if strings.HasPrefix(name, "-") {
		return false
	}
	return true
}

func validSHA256(value string) bool {
	if len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

// CheckServerRequirement enforces requires_server when the manifest declares
// it. An unknown server version is refused rather than ignored.
func CheckServerRequirement(manifest *Manifest, serverVersion string) error {
	if manifest == nil || manifest.RequiresServer == "" {
		return nil
	}
	required, err := parseSemver(manifest.RequiresServer)
	if err != nil {
		return fmt.Errorf("requires_server non valido: %w", err)
	}
	current, err := parseSemver(serverVersion)
	if err != nil {
		return fmt.Errorf("versione server sconosciuta, impossibile verificare requires_server: %w", err)
	}
	if semverLess(current, required) {
		return fmt.Errorf("server NAS %s obsoleto per l'aggiornamento (richiesto %s)", serverVersion, manifest.RequiresServer)
	}
	return nil
}

// Available reports whether the remote manifest advertises a newer, valid
// version. Downgrades and artifacts for other platforms are refused.
func Available(manifest *Manifest) bool {
	if err := ValidateManifest(manifest); err != nil {
		return false
	}
	remote, err := parseSemver(manifest.Version)
	if err != nil {
		return false
	}
	current, err := parseSemver(version.AppVersion)
	if err != nil {
		return false
	}
	return semverLess(current, remote)
}

// verifiedWriter streams the artifact into a temporary file while computing
// its digest and byte count in a single pass.
type verifiedWriter struct {
	file io.Writer
	hash hash.Hash
	read int64
}

func (w *verifiedWriter) Write(data []byte) (int, error) {
	n, err := w.file.Write(data)
	if n > 0 {
		_, _ = w.hash.Write(data[:n])
		w.read += int64(n)
	}
	return n, err
}

// Download streams the artifact into a unique temporary file, verifies both
// the known size and sha256 while copying, and only then makes it visible at
// destination with an atomic rename.
func Download(cfg *config.Config, client FileReader, manifest *Manifest, destination string) error {
	if err := ValidateManifest(manifest); err != nil {
		return err
	}
	artifactPath := remoteDir(cfg) + "/" + manifest.Artifact
	tmp := destination + ".tmp." + strconv.Itoa(os.Getpid())
	file, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		return err
	}
	cleanup := func() {
		_ = file.Close()
		_ = os.Remove(tmp)
	}
	writer := &verifiedWriter{file: file, hash: sha256.New()}
	if streamer, ok := client.(StreamReader); ok {
		err = streamer.StreamRemoteFile(artifactPath, writer)
	} else {
		var raw []byte
		raw, err = client.ReadRemoteFile(artifactPath)
		if err == nil {
			_, err = io.Copy(writer, bytes.NewReader(raw))
		}
	}
	if err != nil {
		cleanup()
		return err
	}
	if writer.read != manifest.Size {
		cleanup()
		return fmt.Errorf("dimensione dell'artifact Go non corrisponde: %d != %d", writer.read, manifest.Size)
	}
	if hex.EncodeToString(writer.hash.Sum(nil)) != strings.ToLower(manifest.SHA256) {
		cleanup()
		return fmt.Errorf("sha256 dell'artifact Go non corrisponde")
	}
	if err := file.Chmod(0o755); err != nil {
		cleanup()
		return err
	}
	if err := file.Sync(); err != nil {
		cleanup()
		return err
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, destination); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// UpdateState is the persistent, error-protected state machine. It is written
// before the first rename so a crash at any point can be recovered at startup.
type UpdateState struct {
	State           string `json:"state"`
	Version         string `json:"version"`
	Binary          string `json:"binary"`
	Backup          string `json:"backup"`
	Staged          string `json:"staged,omitempty"`
	PreviousVersion string `json:"previous_version,omitempty"`
	UpdatedAt       int64  `json:"updated_at"`
	ConfirmedStarts int    `json:"confirmed_starts,omitempty"`
	StartupFailures int    `json:"startup_failures,omitempty"`
}

const (
	StatePrepared     = "PREPARED"
	StateOldMoved     = "OLD_MOVED"
	StateNewInstalled = "NEW_INSTALLED"
	StateConfirmed    = "CONFIRMED"
	StateFailed       = "FAILED"
)

func statePath(binary string) string { return binary + stateSuffix }

func readState(binary string) (*UpdateState, error) {
	path := statePath(binary)
	var state *UpdateState
	err := withFileLock(path, func() error {
		content, err := os.ReadFile(path)
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		state = &UpdateState{}
		if err := json.Unmarshal(content, state); err != nil {
			return fmt.Errorf("stato aggiornamento non valido: %w", err)
		}
		return nil
	})
	return state, err
}

func writeState(binary string, state *UpdateState) error {
	path := statePath(binary)
	return withFileLock(path, func() error {
		state.UpdatedAt = time.Now().Unix()
		content, err := json.MarshalIndent(state, "", "  ")
		if err != nil {
			return err
		}
		tmp := path + ".tmp." + strconv.Itoa(os.Getpid())
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
		if err := os.Rename(tmp, path); err != nil {
			_ = os.Remove(tmp)
			return err
		}
		return nil
	})
}

// Apply checks, downloads and installs an available update. The state file is
// committed before the first filesystem rename, so an interruption can always
// be detected and rolled back by RollbackIfNeeded at the next startup.
// It returns true when a new binary is ready and a restart is required.
func Apply(cfg *config.Config, client FileReader, binaryPath string) (bool, string, error) {
	manifest, err := Newest(cfg, client)
	if err != nil {
		return false, "", err
	}
	if manifest.RequiresServer != "" {
		serverVersion := ""
		if provider, ok := client.(ServerVersionProvider); ok {
			serverVersion, err = provider.ServerVersion()
			if err != nil {
				return false, "", fmt.Errorf("versione server non disponibile: %w", err)
			}
		}
		if err := CheckServerRequirement(manifest, serverVersion); err != nil {
			return false, "", err
		}
	}
	if !Available(manifest) {
		return false, "", nil
	}
	pending, err := readState(binaryPath)
	if err != nil {
		return false, "", err
	}
	if pending != nil && pending.State == StateNewInstalled && pending.Version == version.AppVersion {
		// The process asking for this update is the freshly installed binary:
		// it started and is healthy enough to check the channel, so waiting for
		// a second start must not block the next release.
		pending.State = StateConfirmed
		pending.ConfirmedStarts = 2
		pending.UpdatedAt = time.Now().Unix()
		if err := writeState(binaryPath, pending); err != nil {
			return false, "", err
		}
	}
	if pending != nil && pending.State != StateConfirmed && pending.State != StateFailed {
		return false, "", fmt.Errorf("aggiornamento in sospeso (%s): riavvia il client", pending.State)
	}
	// Anti-rollback: never reinstall a version already confirmed on disk, even
	// if the executable was manually replaced by an older build. A FAILED state
	// is excluded so the same version can be retried after a rollback.
	if pending != nil && pending.State == StateConfirmed && pending.Version != "" {
		if installed, err := parseSemver(pending.Version); err == nil {
			remote, _ := parseSemver(manifest.Version)
			if !semverLess(installed, remote) {
				return false, "", nil
			}
		}
	}

	staged := binaryPath + ".new." + strconv.Itoa(os.Getpid())
	if err := Download(cfg, client, manifest, staged); err != nil {
		return false, "", err
	}
	backup := binaryPath + ".bak"
	state := &UpdateState{
		State:           StatePrepared,
		Version:         manifest.Version,
		Binary:          binaryPath,
		Backup:          backup,
		Staged:          staged,
		PreviousVersion: version.AppVersion,
	}
	// ERROR-PROTECTED WRITE: no rename may happen before this succeeds.
	if err := writeState(binaryPath, state); err != nil {
		_ = os.Remove(staged)
		return false, "", err
	}
	if _, err := os.Lstat(backup); err == nil {
		if err := os.Remove(backup); err != nil {
			_ = RollbackIfNeeded(binaryPath)
			return false, "", err
		}
	}
	if err := os.Rename(binaryPath, backup); err != nil {
		_ = RollbackIfNeeded(binaryPath)
		return false, "", err
	}
	state.State = StateOldMoved
	if err := writeState(binaryPath, state); err != nil {
		_ = RollbackIfNeeded(binaryPath)
		return false, "", err
	}
	if err := os.Rename(staged, binaryPath); err != nil {
		_ = RollbackIfNeeded(binaryPath)
		return false, "", err
	}
	state.State = StateNewInstalled
	state.Staged = ""
	if err := writeState(binaryPath, state); err != nil {
		_ = RollbackIfNeeded(binaryPath)
		return false, "", err
	}
	return true, manifest.Version, nil
}

func healthCheck(binary, expected string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, binary, "version").CombinedOutput()
	if err != nil {
		return fmt.Errorf("nuovo binario non avviabile: %w: %s", err, strings.TrimSpace(string(output)))
	}
	// A responsive `version` subcommand is the health signal. The exact text is
	// not enforced: the artifact digest already guarantees the right build,
	// and version strings may carry suffixes that differ from the manifest.
	if strings.TrimSpace(string(output)) == "" {
		return fmt.Errorf("nuovo binario muto al comando version (attesa %s)", expected)
	}
	return nil
}

// ConfirmStartedOK is called after a successful startup. It health-checks a
// newly installed binary, promotes the state to CONFIRMED after two good
// starts and rolls back after two consecutive failed starts.
func ConfirmStartedOK(binaryPath string) error {
	state, err := readState(binaryPath)
	if err != nil {
		return err
	}
	if state == nil || state.State == StateConfirmed || state.State == StateFailed {
		return nil
	}
	if state.State == StatePrepared || state.State == StateOldMoved {
		return RollbackIfNeeded(binaryPath)
	}
	if state.State != StateNewInstalled {
		return nil
	}
	if err := healthCheck(binaryPath, state.Version); err != nil {
		state.StartupFailures++
		state.UpdatedAt = time.Now().Unix()
		if state.StartupFailures >= 2 {
			return restoreBackup(binaryPath, state.Backup, state)
		}
		if err := writeState(binaryPath, state); err != nil {
			return err
		}
		return fmt.Errorf("health check del nuovo binario fallito (%d/2): %w", state.StartupFailures, err)
	}
	state.ConfirmedStarts++
	if state.ConfirmedStarts >= 2 {
		state.State = StateConfirmed
		state.UpdatedAt = time.Now().Unix()
		if err := writeState(binaryPath, state); err != nil {
			return err
		}
		if state.Backup != "" {
			_ = os.Remove(state.Backup)
		}
		_ = os.Remove(binaryPath + ".failed")
		return nil
	}
	return writeState(binaryPath, state)
}

// RollbackIfNeeded repairs an interrupted or failed update before any sync
// work starts. ErrRollbackPerformed signals that a restart is required.
func RollbackIfNeeded(binaryPath string) error {
	state, err := readState(binaryPath)
	if err != nil {
		return err
	}
	if state == nil {
		// A crash between the PREPARED write and the backup creation can leave
		// no state file only if it was manually removed: restore if evident.
		if _, err := os.Lstat(binaryPath); os.IsNotExist(err) {
			backup := binaryPath + ".bak"
			if _, err := os.Lstat(backup); err == nil {
				return restoreBackup(binaryPath, backup, nil)
			}
		}
		return nil
	}
	switch state.State {
	case StateConfirmed, StateFailed:
		return nil
	case StatePrepared:
		if state.Staged != "" {
			_ = os.Remove(state.Staged)
		}
		if _, err := os.Lstat(binaryPath); os.IsNotExist(err) {
			return restoreBackup(binaryPath, state.Backup, state)
		}
		state.State = StateFailed
		return writeState(binaryPath, state)
	case StateOldMoved:
		return restoreBackup(binaryPath, state.Backup, state)
	case StateNewInstalled:
		if state.StartupFailures >= 2 {
			return restoreBackup(binaryPath, state.Backup, state)
		}
		return nil
	default:
		return fmt.Errorf("stato aggiornamento sconosciuto: %q", state.State)
	}
}

func restoreBackup(binaryPath, backup string, state *UpdateState) error {
	if backup == "" {
		backup = binaryPath + ".bak"
	}
	if _, err := os.Lstat(backup); err != nil {
		return fmt.Errorf("copia di rollback assente: %w", err)
	}
	failed := binaryPath + ".failed"
	if _, err := os.Lstat(binaryPath); err == nil {
		if _, err := os.Lstat(failed); err == nil {
			_ = os.Remove(failed)
		}
		if err := os.Rename(binaryPath, failed); err != nil {
			return fmt.Errorf("impossibile scostare il binario difettoso: %w", err)
		}
	}
	if err := os.Rename(backup, binaryPath); err != nil {
		// Never leave the machine without an executable: put the defective
		// binary back so the user can retry the rollback manually.
		if _, e := os.Lstat(failed); e == nil {
			_ = os.Rename(failed, binaryPath)
		}
		return fmt.Errorf("ripristino del binario fallito: %w", err)
	}
	if state != nil {
		state.State = StateFailed
		state.UpdatedAt = time.Now().Unix()
		if err := writeState(binaryPath, state); err != nil {
			return err
		}
	}
	return ErrRollbackPerformed
}

// Install keeps a rollback copy of the current binary. It is retained for
// compatibility with callers that only need the copy, not a full update.
func Install(binaryPath string) (string, error) {
	backup := binaryPath + ".bak"
	source, err := os.Open(binaryPath)
	if err != nil {
		return "", err
	}
	defer source.Close()
	tmp := backup + ".tmp." + strconv.Itoa(os.Getpid())
	destination, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		return "", err
	}
	_, err = io.Copy(destination, source)
	if err != nil {
		_ = destination.Close()
		_ = os.Remove(tmp)
		return "", err
	}
	if err := destination.Close(); err != nil {
		_ = os.Remove(tmp)
		return "", err
	}
	if err := os.Rename(tmp, backup); err != nil {
		_ = os.Remove(tmp)
		return "", err
	}
	return backup, nil
}

// Rollback restores the previous binary from the .bak copy created by Install.
func Rollback(binaryPath string) error {
	return restoreBackup(binaryPath, binaryPath+".bak", nil)
}
