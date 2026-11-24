package mirror

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"nasbox-go/internal/config"
)

func mirrorSetup(t *testing.T) (*config.Config, string, string) {
	t.Helper()
	root := t.TempDir()
	source := t.TempDir()
	cfg := config.New("")
	if err := cfg.Set("local_root", root, false); err != nil {
		t.Fatal(err)
	}
	return cfg, root, source
}

func writeFiles(t *testing.T, dir string, count int) {
	t.Helper()
	for i := 0; i < count; i++ {
		name := fmt.Sprintf("file-%03d.txt", i)
		if err := os.WriteFile(filepath.Join(dir, name), []byte(name), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

// fakeRsync puts a stub "rsync" first in PATH. By default it just logs its
// arguments, so tests can assert what would have been copied without touching
// real data. A custom script is used to simulate failures or slow transfers.
func fakeRsync(t *testing.T, script string) string {
	t.Helper()
	dir := t.TempDir()
	logPath := filepath.Join(dir, "rsync.log")
	if script == "" {
		script = "#!/bin/sh\nprintf '%s\\n' \"$@\" >> \"$RSYNC_LOG\"\nexit 0\n"
	}
	if err := os.WriteFile(filepath.Join(dir, "rsync"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("RSYNC_LOG", logPath)
	return logPath
}

func rsyncLog(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return ""
	}
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func hasArg(args []string, want string) bool {
	for _, arg := range args {
		if arg == want {
			return true
		}
	}
	return false
}

func TestConfiguredReadsMirrorsFromJSON(t *testing.T) {
	cfg := config.New("")
	if err := cfg.Set("mirrors", []any{
		map[string]any{"source": "/home/user/RPG", "dest": "/RPG/", "last_sync": float64(123)},
		map[string]any{"source": "/home/user/Musica", "dest": "Musica", "enabled": false},
	}, false); err != nil {
		t.Fatal(err)
	}
	mirrors := Configured(cfg)
	if len(mirrors) != 2 {
		t.Fatalf("got %d mirrors, want 2", len(mirrors))
	}
	if mirrors[0].Source != "/home/user/RPG" || mirrors[0].Dest != "RPG" ||
		!mirrors[0].Enabled || mirrors[0].LastSync != 123 {
		t.Fatalf("first mirror parsed wrong: %+v", mirrors[0])
	}
	if mirrors[1].Enabled {
		t.Fatalf("explicit enabled=false was lost: %+v", mirrors[1])
	}
}

func TestValidateMirror(t *testing.T) {
	cfg, root, source := mirrorSetup(t)
	inside := filepath.Join(root, "inside")
	if err := os.MkdirAll(inside, 0o700); err != nil {
		t.Fatal(err)
	}
	external := t.TempDir()
	symlinkDir := t.TempDir()
	if err := os.Symlink(external, filepath.Join(symlinkDir, "link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(external, filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	deepParts := make([]string, MaxDestDepth+1)
	for i := range deepParts {
		deepParts[i] = "a"
	}

	cases := []struct {
		name   string
		source string
		dest   string
		bad    bool
	}{
		{"valid", source, "Docs", false},
		{"valid nested", source, "Docs/Sub/Copy", false},
		{"empty source", "", "Docs", true},
		{"relative source", "relative/path", "Docs", true},
		{"missing source", filepath.Join(source, "missing"), "Docs", true},
		{"source inside root", inside, "Docs", true},
		{"source equals root", root, "Docs", true},
		{"source symlink component", filepath.Join(symlinkDir, "link"), "Docs", true},
		{"empty dest", source, "", true},
		{"dest is root", source, ".", true},
		{"dest traversal", source, "Docs/../Altro", true},
		{"absolute dest", source, "/assoluta", true},
		{"backslash dest", source, `Docs\Altro`, true},
		{"reserved dest", source, ".sync-trash", true},
		{"reserved nested dest", source, "Docs/.sync-partial", true},
		{"dest symlink component", source, "link/Docs", true},
		{"dest too deep", source, strings.Join(deepParts, "/"), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateMirror(cfg, tc.source, tc.dest)
			if tc.bad && err == nil {
				t.Fatalf("ValidateMirror(%q, %q) = nil, want error", tc.source, tc.dest)
			}
			if !tc.bad && err != nil {
				t.Fatalf("ValidateMirror(%q, %q) = %v, want nil", tc.source, tc.dest, err)
			}
		})
	}
}

func TestValidateMirrorRequiresConfiguredRoot(t *testing.T) {
	cfg, _, source := mirrorSetup(t)
	if err := cfg.Set("local_root", "", false); err != nil {
		t.Fatal(err)
	}
	if err := ValidateMirror(cfg, source, "Docs"); err == nil {
		t.Fatal("empty local_root must be rejected")
	}
}

func TestRsyncCommandIsLocalAndProtectsInternals(t *testing.T) {
	cfg, root, source := mirrorSetup(t)
	if err := cfg.Set("exclude_patterns", []any{"*.tmp", "  "}, false); err != nil {
		t.Fatal(err)
	}
	args := RsyncCommand(cfg, source, filepath.Join(root, "Docs"))

	wantPrefix := []string{
		"-a", "--delete-after", "--partial",
		"--partial-dir=.sync-partial", "--delay-updates", "--no-links",
	}
	if len(args) < len(wantPrefix) {
		t.Fatalf("command too short: %q", args)
	}
	for i, want := range wantPrefix {
		if args[i] != want {
			t.Fatalf("args[%d] = %q, want %q (full: %q)", i, args[i], want, args)
		}
	}
	for _, want := range []string{
		"--exclude", "*.tmp", ".sync-trash", ".sync-partial", ".nasbox-staging",
		ConflictGlob, ArchivedGlob, source + "/", root + "/Docs/",
	} {
		if !hasArg(args, want) {
			t.Errorf("missing %q in %q", want, args)
		}
	}
	for _, forbidden := range []string{"ssh", "-e", "--delete", "--bwlimit"} {
		if hasArg(args, forbidden) {
			t.Errorf("unexpected %q in local rsync command %q", forbidden, args)
		}
	}
}

func TestSyncAllSkipsSuspiciouslyEmptySource(t *testing.T) {
	cfg, root, source := mirrorSetup(t)
	writeFiles(t, source, 3)
	dest := filepath.Join(root, "Docs")
	if err := os.MkdirAll(dest, 0o700); err != nil {
		t.Fatal(err)
	}
	precious := filepath.Join(dest, "prezioso.txt")
	if err := os.WriteFile(precious, []byte("resta"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := cfg.Set("mirrors", []any{
		map[string]any{"source": source, "dest": "Docs", "enabled": true},
	}, false); err != nil {
		t.Fatal(err)
	}
	logPath := fakeRsync(t, "")

	count, err := SyncAll(context.Background(), cfg)
	if count != 0 {
		t.Fatalf("synced %d mirrors, want 0", count)
	}
	if err == nil || !strings.Contains(err.Error(), "sicurezza") {
		t.Fatalf("empty source should be blocked, got %v", err)
	}
	if log := rsyncLog(t, logPath); log != "" {
		t.Fatalf("rsync must not run on an empty source, log: %q", log)
	}
	content, err := os.ReadFile(precious)
	if err != nil || string(content) != "resta" {
		t.Fatalf("destination was touched: %q, %v", content, err)
	}
	entry := Configured(cfg)[0]
	if entry.LastSync != 0 || !strings.Contains(entry.Error, "sicurezza") {
		t.Fatalf("guard error not recorded: %+v", entry)
	}
}

func TestSyncAllRunsLocalCopyAndUpdatesLastSync(t *testing.T) {
	root := t.TempDir()
	source := t.TempDir()
	writeFiles(t, source, 21)
	otherSource := t.TempDir()
	configPath := filepath.Join(t.TempDir(), "client.json")
	initial := map[string]any{
		"local_root": root,
		"nas_lan":    "nas.example.org",
		"mirrors": []any{
			map[string]any{"source": source, "dest": "Docs", "enabled": true, "note": "keep-me"},
			map[string]any{"source": otherSource, "dest": "Altro", "enabled": false},
		},
	}
	content, err := json.MarshalIndent(initial, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, content, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadFrom(configPath)
	if err != nil {
		t.Fatal(err)
	}
	logPath := fakeRsync(t, "")

	count, err := SyncAll(context.Background(), cfg)
	if err != nil || count != 1 {
		t.Fatalf("SyncAll = (%d, %v), want (1, nil)", count, err)
	}
	lines := strings.Split(strings.TrimSpace(rsyncLog(t, logPath)), "\n")
	if len(lines) < 2 {
		t.Fatalf("rsync was not invoked: %q", lines)
	}
	if lines[len(lines)-2] != source+"/" || lines[len(lines)-1] != root+"/Docs/" {
		t.Fatalf("wrong local copy paths: %q", lines[len(lines)-2:])
	}

	// Re-read from disk: other config keys, other mirror entries and extra
	// fields must survive the last_sync write.
	reloaded, err := config.LoadFrom(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.String("nas_lan") != "nas.example.org" || reloaded.LocalRoot() != root {
		t.Fatalf("unrelated config keys were lost: %+v", reloaded.Data)
	}
	mirrors := Configured(reloaded)
	if len(mirrors) != 2 {
		t.Fatalf("lost mirror entries: %+v", mirrors)
	}
	if mirrors[0].LastSync <= 0 || mirrors[0].Error != "" {
		t.Fatalf("last_sync/error not persisted: %+v", mirrors[0])
	}
	if mirrors[1].LastSync != 0 {
		t.Fatalf("disabled mirror was modified: %+v", mirrors[1])
	}
	raw, _ := reloaded.Get("mirrors").([]any)
	first, ok := raw[0].(map[string]any)
	if !ok || first["note"] != "keep-me" {
		t.Fatalf("extra mirror field was lost: %q", raw[0])
	}
}

func TestSyncAllRecordsRsyncFailure(t *testing.T) {
	cfg, _, source := mirrorSetup(t)
	writeFiles(t, source, 21)
	if err := cfg.Set("mirrors", []any{
		map[string]any{"source": source, "dest": "Docs", "enabled": true},
	}, false); err != nil {
		t.Fatal(err)
	}
	fakeRsync(t, "#!/bin/sh\necho 'rsync guasto' >&2\nexit 1\n")

	count, err := SyncAll(context.Background(), cfg)
	if count != 0 || err == nil || !strings.Contains(err.Error(), "rsync guasto") {
		t.Fatalf("SyncAll = (%d, %v), want failure", count, err)
	}
	entry := Configured(cfg)[0]
	if !strings.Contains(entry.Error, "rsync guasto") || entry.LastSync != 0 {
		t.Fatalf("failure not recorded correctly: %+v", entry)
	}
}

func TestSyncAllIsCancellable(t *testing.T) {
	cfg, _, source := mirrorSetup(t)
	writeFiles(t, source, 21)
	if err := cfg.Set("mirrors", []any{
		map[string]any{"source": source, "dest": "Docs", "enabled": true},
	}, false); err != nil {
		t.Fatal(err)
	}
	fakeRsync(t, "#!/bin/sh\nexec sleep 30\n")
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(60*time.Millisecond, cancel)

	start := time.Now()
	count, err := SyncAll(ctx, cfg)
	elapsed := time.Since(start)
	if count != 0 || !errors.Is(err, context.Canceled) {
		t.Fatalf("SyncAll = (%d, %v), want cancel", count, err)
	}
	if elapsed > 5*time.Second {
		t.Fatalf("cancellation took too long: %s", elapsed)
	}
}

func TestPatchMirrorKeepsOtherEntries(t *testing.T) {
	path := filepath.Join(t.TempDir(), "client.json")
	initial := map[string]any{
		"local_root": "/root",
		"mirrors": []any{
			map[string]any{"source": "/uno", "dest": "uno", "extra": 7},
			map[string]any{"source": "/due", "dest": "due", "note": "intatta"},
		},
	}
	content, err := json.MarshalIndent(initial, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadFrom(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := patchMirror(cfg, "/uno", map[string]any{"last_sync": float64(42), "error": nil}); err != nil {
		t.Fatal(err)
	}
	reloaded, err := config.LoadFrom(path)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := reloaded.Get("mirrors").([]any)
	if len(raw) != 2 {
		t.Fatalf("lost entries: %q", raw)
	}
	first := raw[0].(map[string]any)
	second := raw[1].(map[string]any)
	if first["last_sync"] != float64(42) || first["extra"] != float64(7) {
		t.Fatalf("first entry not patched correctly: %q", first)
	}
	if _, ok := first["error"]; ok {
		t.Fatalf("empty error should be removed: %q", first)
	}
	if second["note"] != "intatta" {
		t.Fatalf("second entry changed: %q", second)
	}
	if reloaded.LocalRoot() != "/root" {
		t.Fatal("unrelated key lost")
	}
}
