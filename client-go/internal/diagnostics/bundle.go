package diagnostics

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const bundlePrefix = "nasbox-diagnostics/"

// BundleOptions describes a support archive. Only the fixed diagnostics files
// are read; the state directory is never walked, so SQLite data, SSH keys,
// known_hosts and unrelated private files cannot enter the archive by accident.
type BundleOptions struct {
	Output     string
	StateDir   string
	ConfigPath string
	Versions   map[string]any
	Warnings   []string
}

type bundleFile struct {
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

type bundleManifest struct {
	SchemaVersion int          `json:"schema_version"`
	GeneratedAt   string       `json:"generated_at"`
	Redacted      bool         `json:"redacted"`
	Files         []bundleFile `json:"files"`
	Warnings      []string     `json:"warnings,omitempty"`
}

// CreateBundle writes a private, atomically published tar.gz containing a
// redacted config, all retained logs/events, the integrity audit history and
// version information. Existing output files are replaced only after the new
// archive has been written and closed successfully.
func CreateBundle(options BundleOptions) error {
	if strings.TrimSpace(options.Output) == "" {
		return errors.New("percorso del bundle vuoto")
	}
	if strings.TrimSpace(options.StateDir) == "" {
		return errors.New("directory di stato del bundle vuota")
	}
	output, err := filepath.Abs(options.Output)
	if err != nil {
		return err
	}
	stateDir, err := filepath.Abs(options.StateDir)
	if err != nil {
		return err
	}

	sources := bundleSources(stateDir, options.ConfigPath)
	for _, source := range sources {
		if sameFile(output, source) {
			return fmt.Errorf("il bundle non può sovrascrivere un file diagnostico: %s", source)
		}
	}

	files := make(map[string][]byte)
	warnings := append([]string(nil), options.Warnings...)

	configData, err := os.ReadFile(options.ConfigPath)
	if err != nil {
		if options.ConfigPath == "" || os.IsNotExist(err) {
			configData = []byte("{}\n")
			warnings = append(warnings, "client.json non presente")
		} else {
			return fmt.Errorf("lettura configurazione: %w", err)
		}
	}
	redactedConfig, err := RedactJSON(configData)
	if err != nil {
		return fmt.Errorf("redazione configurazione: %w", err)
	}
	files[bundlePrefix+"config/client.json"] = redactedConfig

	for _, name := range []string{
		logName, logName + ".1", logName + ".2",
		eventName, eventName + ".1", eventName + ".2",
	} {
		path := filepath.Join(stateDir, name)
		data, err := os.ReadFile(path)
		if err != nil {
			if os.IsNotExist(err) {
				if name == logName || name == eventName {
					files[bundlePrefix+"logs/"+name] = nil
				}
				continue
			}
			return fmt.Errorf("lettura %s: %w", name, err)
		}
		if strings.HasPrefix(name, eventName) {
			files[bundlePrefix+"logs/"+name] = RedactJSONL(data)
		} else {
			files[bundlePrefix+"logs/"+name] = []byte(RedactText(string(data)))
		}
	}

	auditPath := filepath.Join(stateDir, "integrity-audit.json")
	auditData, err := os.ReadFile(auditPath)
	if err != nil {
		if os.IsNotExist(err) {
			auditData = []byte(`{"reports":[]}` + "\n")
			warnings = append(warnings, "report audit non presente")
		} else {
			return fmt.Errorf("lettura report audit: %w", err)
		}
	}
	redactedAudit, err := RedactJSON(auditData)
	if err != nil {
		return fmt.Errorf("redazione report audit: %w", err)
	}
	files[bundlePrefix+"integrity-audit.json"] = redactedAudit

	versions := options.Versions
	if versions == nil {
		versions = map[string]any{}
	}
	versionData, err := json.MarshalIndent(RedactValue(versions), "", "  ")
	if err != nil {
		return fmt.Errorf("serializzazione versioni: %w", err)
	}
	files[bundlePrefix+"versions.json"] = append(versionData, '\n')

	manifest := bundleManifest{
		SchemaVersion: 1,
		GeneratedAt:   time.Now().UTC().Format(time.RFC3339Nano),
		Redacted:      true,
		Warnings:      make([]string, 0, len(warnings)),
	}
	for _, warning := range warnings {
		manifest.Warnings = append(manifest.Warnings, RedactText(warning))
	}
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		data := files[name]
		digest := sha256.Sum256(data)
		manifest.Files = append(manifest.Files, bundleFile{
			Name:   name,
			Size:   int64(len(data)),
			SHA256: hex.EncodeToString(digest[:]),
		})
	}
	manifestData, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	files[bundlePrefix+"manifest.json"] = append(manifestData, '\n')
	names = append(names, bundlePrefix+"manifest.json")
	sort.Strings(names)

	if err := writeArchive(output, names, files); err != nil {
		return err
	}
	return nil
}

func bundleSources(stateDir, configPath string) []string {
	sources := make([]string, 0, 8)
	if configPath != "" {
		sources = append(sources, configPath)
	}
	for _, name := range []string{
		logName, logName + ".1", logName + ".2",
		eventName, eventName + ".1", eventName + ".2",
		"integrity-audit.json",
	} {
		sources = append(sources, filepath.Join(stateDir, name))
	}
	return sources
}

func sameFile(left, right string) bool {
	leftAbs, leftErr := filepath.Abs(left)
	rightAbs, rightErr := filepath.Abs(right)
	if leftErr == nil && rightErr == nil && leftAbs == rightAbs {
		return true
	}
	leftInfo, leftErr := os.Stat(left)
	rightInfo, rightErr := os.Stat(right)
	return leftErr == nil && rightErr == nil && os.SameFile(leftInfo, rightInfo)
}

func writeArchive(output string, names []string, files map[string][]byte) (err error) {
	dir := filepath.Dir(output)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(dir, ".nasbox-diagnostics-*.tar.gz")
	if err != nil {
		return err
	}
	temporaryName := temporary.Name()
	defer func() {
		_ = temporary.Close()
		_ = os.Remove(temporaryName)
	}()
	if err := temporary.Chmod(0o600); err != nil {
		return err
	}

	gzipWriter := gzip.NewWriter(temporary)
	tarWriter := tar.NewWriter(gzipWriter)
	for _, name := range names {
		data := files[name]
		header := &tar.Header{
			Name:     name,
			Mode:     0o600,
			Size:     int64(len(data)),
			Typeflag: tar.TypeReg,
			ModTime:  time.Now().UTC(),
		}
		if err := tarWriter.WriteHeader(header); err != nil {
			_ = tarWriter.Close()
			_ = gzipWriter.Close()
			return err
		}
		if _, err := tarWriter.Write(data); err != nil {
			_ = tarWriter.Close()
			_ = gzipWriter.Close()
			return err
		}
	}
	if err := tarWriter.Close(); err != nil {
		_ = gzipWriter.Close()
		return err
	}
	if err := gzipWriter.Close(); err != nil {
		return err
	}
	if err := temporary.Sync(); err != nil {
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryName, output); err != nil {
		return err
	}
	if directory, err := os.Open(dir); err == nil {
		_ = directory.Sync()
		_ = directory.Close()
	}
	return nil
}
