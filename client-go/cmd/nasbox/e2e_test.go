package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"
)

// End-to-end tests: the real nasbox CLI (this test binary re-executed), real
// rsync and the real server script against a disposable "NAS" share. A fake
// ssh on PATH runs the remote command locally, so every byte goes through the
// same code paths as in production: bootstrap, lease, staging-publish,
// precondition overwrite with history, checked delete, journal and manifest.
//
// These tests would have caught the 0.18.0 staging bug (edits to existing
// files never uploaded). They are skipped when bash, rsync, flock or sha256sum
// are missing.

func TestMain(m *testing.M) {
	if os.Getenv("NASBOX_E2E_CHILD") == "1" {
		os.Args = append([]string{"nasbox"}, strings.Fields(os.Getenv("NASBOX_E2E_ARGS"))...)
		main()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

const fakeSSH = `#!/usr/bin/env bash
# Fake ssh for e2e tests: drop options and target, run the command locally.
while [ $# -gt 0 ]; do
  case "$1" in
    -p|-o|-J|-i|-l|-S|-F|-b|-c|-E|-L|-R|-D|-W) shift 2 ;;
    -*) shift ;;
    *) break ;;
  esac
done
shift
exec bash -c "$*"
`

type e2eNAS struct {
	t      *testing.T
	root   string
	share  string
	script string
	bin    string
}

func newE2ENAS(t *testing.T) *e2eNAS {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("e2e richiede Linux")
	}
	for _, tool := range []string{"bash", "rsync", "flock", "sha256sum"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("e2e: %s non disponibile", tool)
		}
	}
	source, err := filepath.Abs("../../../server/sync-daemon-server.sh")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(source); err != nil {
		t.Skip("e2e: script server non trovato")
	}
	root := t.TempDir()
	n := &e2eNAS{t: t, root: root, share: filepath.Join(root, "share"),
		script: filepath.Join(root, "server", "sync-daemon-server.sh"), bin: filepath.Join(root, "bin")}
	for _, dir := range []string{n.share, filepath.Dir(n.script), n.bin} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	content, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(n.script, content, 0o755); err != nil {
		t.Fatal(err)
	}
	conf := "SHARE_ROOT=" + n.share + "\nRETENTION_DAYS=30\n"
	if err := os.WriteFile(filepath.Join(filepath.Dir(n.script), "server.conf"), []byte(conf), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(n.bin, "ssh"), []byte(fakeSSH), 0o755); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(n.script, "--init-repository").CombinedOutput(); err != nil {
		t.Fatalf("init repository: %v\n%s", err, out)
	}
	return n
}

type e2eClient struct {
	n      *e2eNAS
	name   string
	local  string
	home   string
	config string
}

func (n *e2eNAS) client(name string, overrides ...map[string]any) *e2eClient {
	n.t.Helper()
	c := &e2eClient{n: n, name: name,
		local: filepath.Join(n.root, name, "NASBox"), home: filepath.Join(n.root, name, "home")}
	if err := os.MkdirAll(c.local, 0o755); err != nil {
		n.t.Fatal(err)
	}
	if err := os.MkdirAll(c.home, 0o755); err != nil {
		n.t.Fatal(err)
	}
	c.config = filepath.Join(c.home, "client.json")
	values := map[string]any{
		"local_root": c.local, "remote_prefix": n.share,
		"nas_lan": "nas.e2e", "nas_user": "e2e", "ssh_multiplexing": false,
		"remote_server_script": n.script, "delete_enabled": true,
		"conflict_policy": "newest", "sync_mode": "bidirectional",
		"delete_dir_guard_min_files": 1000,
	}
	for _, override := range overrides {
		for key, value := range override {
			values[key] = value
		}
	}
	content, _ := json.MarshalIndent(values, "", "  ")
	if err := os.WriteFile(c.config, content, 0o600); err != nil {
		n.t.Fatal(err)
	}
	return c
}

func (c *e2eClient) sync() string {
	c.n.t.Helper()
	cmd := exec.Command(os.Args[0])
	cmd.Env = append(os.Environ(),
		"NASBOX_E2E_CHILD=1",
		"NASBOX_E2E_ARGS=sync --config "+c.config,
		"PATH="+c.n.bin+string(os.PathListSeparator)+os.Getenv("PATH"),
		"HOME="+c.home,
		"XDG_STATE_HOME="+filepath.Join(c.home, "state"),
		"XDG_CONFIG_HOME="+filepath.Join(c.home, "config"),
		"XDG_RUNTIME_DIR="+filepath.Join(c.home, "run"),
	)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Run(); err != nil {
		c.n.t.Fatalf("%s: sync fallito: %v\n%s", c.name, err, out.String())
	}
	return out.String()
}

func (c *e2eClient) write(relative, content string) {
	c.n.t.Helper()
	path := filepath.Join(c.local, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		c.n.t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		c.n.t.Fatal(err)
	}
}

func readOrEmpty(path string) string {
	content, err := os.ReadFile(path)
	if err != nil {
		return "<assente>"
	}
	return string(content)
}

func (n *e2eNAS) lockFree() bool {
	path := filepath.Join(filepath.Dir(n.script), "state", "sync-transfer.lock")
	file, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return false
	}
	defer file.Close()
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return false
	}
	_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
	return true
}

func TestE2ETwoClientsCreateEditDelete(t *testing.T) {
	nas := newE2ENAS(t)
	a, b := nas.client("pc-a"), nas.client("pc-b")

	// 1. New files (staging-publish), including spaces and subfolders.
	a.write("a.txt", "prima versione")
	a.write("cartella con spazi/b.txt", "bee")
	out := a.sync()
	if got := readOrEmpty(filepath.Join(nas.share, "cartella con spazi", "b.txt")); got != "bee" {
		t.Fatalf("file nuovo non pubblicato sul NAS: %q\n%s", got, out)
	}
	if !strings.Contains(out, "↑ a.txt") {
		t.Fatalf("la CLI deve elencare i file trasferiti:\n%s", out)
	}

	// 2. Second PC downloads everything.
	out = b.sync()
	if got := readOrEmpty(filepath.Join(b.local, "a.txt")); got != "prima versione" {
		t.Fatalf("download mancante su B: %q\n%s", got, out)
	}

	// 3. Edit of an existing file: precondition overwrite + NAS history.
	a.write("a.txt", "seconda versione, piu lunga")
	out = a.sync()
	if got := readOrEmpty(filepath.Join(nas.share, "a.txt")); got != "seconda versione, piu lunga" {
		t.Fatalf("modifica di un file esistente non caricata: %q\n%s", got, out)
	}
	history, _ := filepath.Glob(filepath.Join(nas.share, ".sync-trash", "a.txt-*"))
	if len(history) != 1 || readOrEmpty(history[0]) != "prima versione" {
		t.Fatalf("versione precedente non conservata nello storico NAS: %v", history)
	}

	// 4. The other PC receives the edit.
	out = b.sync()
	if got := readOrEmpty(filepath.Join(b.local, "a.txt")); got != "seconda versione, piu lunga" {
		t.Fatalf("modifica non arrivata su B: %q\n%s", got, out)
	}

	// 5. Delete on B propagates to the NAS (history kept) and then to A.
	if err := os.Remove(filepath.Join(b.local, "cartella con spazi", "b.txt")); err != nil {
		t.Fatal(err)
	}
	out = b.sync()
	if _, err := os.Stat(filepath.Join(nas.share, "cartella con spazi", "b.txt")); !os.IsNotExist(err) {
		t.Fatalf("cancellazione non propagata al NAS\n%s", out)
	}
	out = a.sync()
	if _, err := os.Stat(filepath.Join(a.local, "cartella con spazi", "b.txt")); !os.IsNotExist(err) {
		t.Fatalf("cancellazione non propagata ad A\n%s", out)
	}

	// 6. Nothing left holding the NAS.
	if !nas.lockFree() {
		t.Fatal("il lock NAS deve essere libero dopo ogni sync")
	}
}

func TestE2EConcurrentEditsNewestWins(t *testing.T) {
	nas := newE2ENAS(t)
	a, b := nas.client("pc-a"), nas.client("pc-b")
	a.write("doc.txt", "base")
	a.sync()
	b.sync()
	a.write("doc.txt", "modifica di A")
	b.write("doc.txt", "modifica di B, successiva")
	if err := os.Chtimes(filepath.Join(a.local, "doc.txt"),
		mustTime(t, "2026-01-01T10:00:00Z"), mustTime(t, "2026-01-01T10:00:00Z")); err != nil {
		t.Fatal(err)
	}
	a.sync()
	out := b.sync()
	a.sync()
	got := readOrEmpty(filepath.Join(nas.share, "doc.txt"))
	if got != readOrEmpty(filepath.Join(a.local, "doc.txt")) || got != readOrEmpty(filepath.Join(b.local, "doc.txt")) {
		t.Fatalf("dopo i conflitti i tre lati devono coincidere: NAS=%q A=%q B=%q\n%s", got,
			readOrEmpty(filepath.Join(a.local, "doc.txt")), readOrEmpty(filepath.Join(b.local, "doc.txt")), out)
	}
	// No version may be lost: the losing edit must survive somewhere
	// (history on the NAS or a conflict copy).
	found := false
	_ = filepath.Walk(nas.root, func(path string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() && path != filepath.Join(nas.share, "doc.txt") &&
			(readOrEmpty(path) == "modifica di A" || readOrEmpty(path) == "modifica di B, successiva") &&
			readOrEmpty(path) != got {
			found = true
		}
		return nil
	})
	if !found {
		t.Fatal("la versione perdente del conflitto non e' recuperabile da nessuna parte")
	}
	if !nas.lockFree() {
		t.Fatal("lock NAS non rilasciato")
	}
}

func mustTime(t *testing.T, value string) time.Time {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}

// Another PC deleting many files must not silently empty this PC: above the
// batch limit the pass is blocked and every local file stays in place.
func TestE2EMassRemoteDeletionIsBlocked(t *testing.T) {
	nas := newE2ENAS(t)
	a := nas.client("pc-a", map[string]any{"max_delete_files": 2})
	b := nas.client("pc-b")
	for _, name := range []string{"x1.txt", "x2.txt", "x3.txt"} {
		a.write("foto/"+name, name)
	}
	a.sync()
	b.sync()
	for _, name := range []string{"x1.txt", "x2.txt", "x3.txt"} {
		if err := os.Remove(filepath.Join(b.local, "foto", name)); err != nil {
			t.Fatal(err)
		}
	}
	b.sync()
	out := a.sync()
	if !strings.Contains(out, "BLOCCATO") {
		t.Fatalf("la cancellazione di massa deve bloccare la sync:\n%s", out)
	}
	for _, name := range []string{"x1.txt", "x2.txt", "x3.txt"} {
		if readOrEmpty(filepath.Join(a.local, "foto", name)) != name {
			t.Fatalf("file locale toccato nonostante il blocco: %s", name)
		}
	}
}

// A file edited directly on the NAS (SMB, Synology File Station) is unknown to
// the manifest. A local edit of the same file must not be skipped forever:
// the NAS version is adopted, then the conflict policy decides and the
// losing version stays recoverable.
func TestE2EOutOfBandNASEditConverges(t *testing.T) {
	nas := newE2ENAS(t)
	a := nas.client("pc-a")
	a.write("nota.txt", "base")
	a.sync()
	if err := os.WriteFile(filepath.Join(nas.share, "nota.txt"), []byte("scritto sul NAS via SMB"), 0o644); err != nil {
		t.Fatal(err)
	}
	a.write("nota.txt", "modifica locale di A")
	var out string
	for pass := 0; pass < 3; pass++ {
		out += a.sync()
	}
	shared := readOrEmpty(filepath.Join(nas.share, "nota.txt"))
	if shared != readOrEmpty(filepath.Join(a.local, "nota.txt")) {
		t.Fatalf("NAS e PC non convergono: NAS=%q A=%q\n%s", shared,
			readOrEmpty(filepath.Join(a.local, "nota.txt")), out)
	}
	lost := "scritto sul NAS via SMB"
	if shared == lost {
		lost = "modifica locale di A"
	}
	found := false
	_ = filepath.Walk(nas.root, func(path string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() && readOrEmpty(path) == lost {
			found = true
		}
		return nil
	})
	if !found {
		t.Fatalf("la versione perdente %q non e' recuperabile\n%s", lost, out)
	}
}
