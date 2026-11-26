package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"nasbox-go/internal/config"
	"nasbox-go/internal/remote"
	"nasbox-go/internal/transfer"
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
	conf := "SHARE_ROOT=" + n.share + "\nRETENTION_DAYS=30\n"
	confPath := filepath.Join(filepath.Dir(n.script), "server.conf")
	if err := os.WriteFile(confPath, []byte(conf), 0o644); err != nil {
		t.Fatal(err)
	}
	if alternate := os.Getenv("NASBOX_E2E_SERVER"); alternate != "" {
		// Wrapper: the Go daemon needs -c FILE, while the client (and fakeSSH)
		// invoke the script path with the verb only.
		bin := filepath.Join(filepath.Dir(n.script), "nasbox-server-bin")
		content, err := os.ReadFile(alternate)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(bin, content, 0o755); err != nil {
			t.Fatal(err)
		}
		wrapper := "#!/bin/sh\nexec " + bin + " -c " + confPath + " \"$@\"\n"
		if err := os.WriteFile(n.script, []byte(wrapper), 0o755); err != nil {
			t.Fatal(err)
		}
	} else {
		content, err := os.ReadFile(source)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(n.script, content, 0o755); err != nil {
			t.Fatal(err)
		}
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
	if os.Getenv("NASBOX_E2E_CHUNKS") != "" {
		values["transfer_backend"] = "chunks"
	}
	content, _ := json.MarshalIndent(values, "", "  ")
	if err := os.WriteFile(c.config, content, 0o600); err != nil {
		n.t.Fatal(err)
	}
	return c
}

func (c *e2eClient) sync() string { return c.run("sync") }

func (c *e2eClient) run(command string) string {
	c.n.t.Helper()
	cmd := exec.Command(os.Args[0])
	cmd.Env = append(os.Environ(),
		"NASBOX_E2E_CHILD=1",
		"NASBOX_E2E_ARGS="+command+" --config "+c.config,
		"PATH="+c.n.bin+string(os.PathListSeparator)+os.Getenv("PATH"),
		"HOME="+c.home,
		"XDG_STATE_HOME="+filepath.Join(c.home, "state"),
		"XDG_CONFIG_HOME="+filepath.Join(c.home, "config"),
		"XDG_RUNTIME_DIR="+filepath.Join(c.home, "run"),
	)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Run(); err != nil {
		c.n.t.Fatalf("%s: %s fallito: %v\n%s", c.name, command, err, out.String())
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

// Server self-update: a newer sync-daemon-server-<v>.sh beside the active
// script is installed, the daemon restarted and the version verified.
func TestE2EServerSelfUpdate(t *testing.T) {
	nas := newE2ENAS(t)
	a := nas.client("pc-a")
	a.sync() // bootstrap
	content, err := os.ReadFile(nas.script)
	if err != nil {
		t.Fatal(err)
	}
	newer := regexp.MustCompile(`(?m)^VERSION="[0-9.]+"`).ReplaceAllString(string(content), `VERSION="9.9.9"`)
	if newer == string(content) {
		t.Skip("versione del server non riconosciuta nello script")
	}
	dir := filepath.Dir(nas.script)
	if err := os.WriteFile(filepath.Join(dir, "sync-daemon-server-9.9.9.sh"), []byte(newer), 0o755); err != nil {
		t.Fatal(err)
	}
	// An older versioned script must never be offered.
	if err := os.WriteFile(filepath.Join(dir, "sync-daemon-server-1.0.0.sh"), content, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = exec.Command(nas.script, "--stop").Run() })
	if out := a.run("server-update-check"); !strings.Contains(out, "disponibile 9.9.9") {
		t.Fatalf("aggiornamento non trovato:\n%s", out)
	}
	if out := a.run("server-update-install"); !strings.Contains(out, "server aggiornato a 9.9.9") {
		t.Fatalf("installazione fallita:\n%s", out)
	}
	if out := a.run("server-update-check"); !strings.Contains(out, "nessun aggiornamento") {
		t.Fatalf("dopo l'aggiornamento non deve proporre altro:\n%s", out)
	}
	if _, err := os.Stat(nas.script + ".prev"); err != nil {
		t.Fatal("la versione precedente dello script deve restare per il rollback")
	}
	// Sync still works against the updated server.
	a.write("dopo-update.txt", "ok")
	a.sync()
	if readOrEmpty(filepath.Join(nas.share, "dopo-update.txt")) != "ok" {
		t.Fatal("sync non funzionante dopo l'aggiornamento del server")
	}
}

// The NAS history lists previous versions and an old version can be brought
// back onto the local file (used by "Versioni del file" when the file exists).
func TestE2ERestoreVersionFromNASHistory(t *testing.T) {
	nas := newE2ENAS(t)
	a := nas.client("pc-a")
	a.write("contratto.txt", "versione 1")
	a.sync()
	a.write("contratto.txt", "versione 2")
	a.sync()

	t.Setenv("PATH", nas.bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(a.home, "state"))
	t.Setenv("XDG_RUNTIME_DIR", filepath.Join(a.home, "run"))
	cfg, err := config.LoadFrom(a.config)
	if err != nil {
		t.Fatal(err)
	}
	client := remote.New(cfg)
	versions, err := client.HistoryList()
	if err != nil {
		t.Fatal(err)
	}
	trash := ""
	for _, version := range versions {
		if version.Original == "contratto.txt" {
			trash = version.TrashRel
		}
	}
	if trash == "" {
		t.Fatalf("lo storico NAS non elenca la versione precedente: %+v", versions)
	}
	if err := transfer.New(cfg, client).DownloadHistoryVersion(trash, "contratto.txt"); err != nil {
		t.Fatal(err)
	}
	if got := readOrEmpty(filepath.Join(a.local, "contratto.txt")); got != "versione 1" {
		t.Fatalf("versione ripristinata = %q", got)
	}
	// The restored bytes are a normal edit: the next sync publishes them and
	// keeps "versione 2" in the NAS history.
	a.sync()
	if got := readOrEmpty(filepath.Join(nas.share, "contratto.txt")); got != "versione 1" {
		t.Fatalf("il ripristino non e' arrivato sul NAS: %q", got)
	}
	kept := false
	matches, _ := filepath.Glob(filepath.Join(nas.share, ".sync-trash", "contratto.txt-*"))
	for _, match := range matches {
		kept = kept || readOrEmpty(match) == "versione 2"
	}
	if !kept {
		t.Fatal("la versione sostituita dal ripristino deve restare nello storico NAS")
	}
}

// Selective sync: an excluded folder (and only that folder) is not
// downloaded, and excluding it never deletes anything on the NAS.
func TestE2ESelectiveSyncIsSafe(t *testing.T) {
	nas := newE2ENAS(t)
	a := nas.client("pc-a")
	a.write("Foto/mare.jpg", "mare")
	a.write("Lavoro/Foto/ufficio.jpg", "ufficio")
	a.write("Documenti/Progetti/piano.txt", "piano")
	a.sync()
	b := nas.client("pc-b", map[string]any{"selective_sync_excludes": []string{"Foto", "Documenti/Progetti"}})
	b.sync()
	if readOrEmpty(filepath.Join(b.local, "Lavoro", "Foto", "ufficio.jpg")) != "ufficio" {
		t.Fatal("una cartella omonima a un'altra profondita' non deve essere esclusa")
	}
	for _, path := range []string{"Foto/mare.jpg", "Documenti/Progetti/piano.txt"} {
		if _, err := os.Stat(filepath.Join(b.local, filepath.FromSlash(path))); !os.IsNotExist(err) {
			t.Fatalf("%s e' in una cartella esclusa e non deve essere scaricato", path)
		}
		if readOrEmpty(filepath.Join(nas.share, filepath.FromSlash(path))) == "<assente>" {
			t.Fatalf("escludere una cartella non deve cancellare %s dal NAS", path)
		}
	}
}

// Server 3.27.0: the history quota removes the oldest versions first, keeps
// recent ones, reports the usage in --print-config; lock files are opened
// without truncation (no disk writes while idle: HDD spin-down).
func TestE2EServerTrashQuotaAndQuietLocks(t *testing.T) {
	nas := newE2ENAS(t)
	conf := filepath.Join(filepath.Dir(nas.script), "server.conf")
	if err := os.WriteFile(conf, []byte("SHARE_ROOT="+nas.share+"\nRETENTION_DAYS=0\nTRASH_MAX_MB=1\nTRASH_MIN_KEEP_DAYS=1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	trash := filepath.Join(nas.share, ".sync-trash")
	if err := os.MkdirAll(filepath.Join(trash, "doc"), 0o755); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	versions := []struct {
		name string
		age  time.Duration
	}{
		{"doc/a.bin-2026-01-01--00-00-00-000000Z", 90 * 24 * time.Hour},
		{"doc/a.bin-2026-02-01--00-00-00-000000Z", 60 * 24 * time.Hour},
		{"doc/a.bin-2026-03-01--00-00-00-000000Z", 30 * 24 * time.Hour},
		{"doc/a.bin-2026-11-24--00-00-00-000000Z", time.Hour}, // too recent: never removed
	}
	payload := bytes.Repeat([]byte("x"), 600*1024)
	for _, version := range versions {
		path := filepath.Join(trash, filepath.FromSlash(version.name))
		if err := os.WriteFile(path, payload, 0o644); err != nil {
			t.Fatal(err)
		}
		stamp := now.Add(-version.age)
		if err := os.Chtimes(path, stamp, stamp); err != nil {
			t.Fatal(err)
		}
	}
	if out, err := exec.Command(nas.script, "--run-once").CombinedOutput(); err != nil {
		t.Fatalf("run-once: %v\n%s", err, out)
	}
	left, _ := filepath.Glob(filepath.Join(trash, "doc", "a.bin-*"))
	names := []string{}
	for _, path := range left {
		names = append(names, filepath.Base(path))
	}
	// 4 x 600 KB with a 1 MB quota: the three old versions go (oldest
	// first) and the protected recent one stays.
	if strings.Join(names, ",") != "a.bin-2026-11-24--00-00-00-000000Z" {
		t.Fatalf("versioni rimaste: %v", names)
	}
	out, err := exec.Command(nas.script, "--print-config").CombinedOutput()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "TRASH_MAX_MB=1") || !strings.Contains(string(out), "TRASH_USED_BYTES=") ||
		strings.Contains(string(out), "TRASH_USED_BYTES=\n") {
		t.Fatalf("print-config deve riportare quota e occupazione:\n%s", out)
	}

	// Quiet locks: an idle read-only command must not change the lock files.
	state := filepath.Join(filepath.Dir(nas.script), "state")
	locks, _ := filepath.Glob(filepath.Join(state, "*.lock"))
	old := now.Add(-time.Hour)
	for _, lock := range locks {
		_ = os.Chtimes(lock, old, old)
	}
	for _, args := range [][]string{{"--print-config"}, {"--journal-status"}, {"--manifest-export", ""}} {
		if out, err := exec.Command(nas.script, args...).CombinedOutput(); err != nil {
			t.Fatalf("%v: %v\n%s", args, err, out)
		}
	}
	for _, lock := range locks {
		info, err := os.Stat(lock)
		if err == nil && info.ModTime().After(old.Add(time.Minute)) {
			t.Fatalf("%s modificato da un comando di sola lettura: i dischi non potrebbero dormire", filepath.Base(lock))
		}
	}
}

// A folder renamed as a whole moves on the NAS in one request (BROWSE_RENAME_V3)
// and the other PC follows without downloading anything again.
func TestE2EFolderRenameIsOneRequestAndKeepsBytes(t *testing.T) {
	nas := newE2ENAS(t)
	a, b := nas.client("pc-a"), nas.client("pc-b")
	for i := 0; i < 6; i++ {
		a.write(fmt.Sprintf("Progetto/sub/f%d.txt", i), fmt.Sprintf("contenuto %d", i))
	}
	a.write("Progetto/radice.txt", "alla radice")
	a.write("Altro/resta.txt", "non si muove")
	a.sync()
	b.sync()

	if err := os.Rename(filepath.Join(a.local, "Progetto"), filepath.Join(a.local, "Progetto 2026")); err != nil {
		t.Fatal(err)
	}
	out := a.sync()
	if _, err := os.Stat(filepath.Join(nas.share, "Progetto")); !os.IsNotExist(err) {
		t.Fatalf("la vecchia cartella è ancora sul NAS\n%s", out)
	}
	for i := 0; i < 6; i++ {
		want := fmt.Sprintf("contenuto %d", i)
		if got := readOrEmpty(filepath.Join(nas.share, "Progetto 2026", "sub", fmt.Sprintf("f%d.txt", i))); got != want {
			t.Fatalf("file %d non spostato: %q\n%s", i, got, out)
		}
	}
	if got := readOrEmpty(filepath.Join(nas.share, "Altro", "resta.txt")); got != "non si muove" {
		t.Fatalf("la cartella accanto è stata toccata: %q", got)
	}
	// One folder move = one journal transaction (per-file renames would leave seven).
	journal := readOrEmpty(filepath.Join(filepath.Dir(nas.script), "state", "transfer-journal.tsv"))
	if got := strings.Count(journal, "BEGIN\tbrowse-rename-"); got != 1 {
		t.Fatalf("attese 1 transazione di rinomina, trovate %d:\n%s", got, journal)
	}
	// Nothing was uploaded again: the move kept the bytes on the NAS.
	if strings.Contains(out, "↑ Progetto 2026") {
		t.Fatalf("la cartella rinominata è stata ricaricata invece che spostata:\n%s", out)
	}

	out = b.sync()
	if got := readOrEmpty(filepath.Join(b.local, "Progetto 2026", "sub", "f3.txt")); got != "contenuto 3" {
		t.Fatalf("B non ha seguito la rinomina: %q\n%s", got, out)
	}
	if strings.Contains(out, "↓ Progetto 2026") {
		t.Fatalf("B ha riscaricato la cartella rinominata invece di spostarla in locale:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(b.local, "Progetto")); !os.IsNotExist(err) {
		t.Fatalf("su B la vecchia cartella è rimasta\n%s", out)
	}
	if !nas.lockFree() {
		t.Fatal("il lock NAS deve essere libero")
	}
}

// TestE2EChunkProtocol exercises the in-house transfer primitive end to end:
// chunk upload, journaled file publish, chunk listing and chunk download. It
// uses the same fake SSH as the other E2E tests, so it works with both the
// bash script and the Go daemon.
func TestE2EChunkProtocol(t *testing.T) {
	if os.Getenv("NASBOX_E2E_SERVER") == "" {
		t.Skip("protocollo a chunk richiede il demone Go (NASBOX_E2E_SERVER)")
	}
	nas := newE2ENAS(t)
	stateDir := filepath.Join(filepath.Dir(nas.script), "state")
	c := nas.client("pc-chunks", map[string]any{
		"server_lock_file_remote":       filepath.Join(stateDir, "sync-transfer.lock"),
		"server_lock_owner_file_remote": filepath.Join(stateDir, "sync-transfer.lock.owner"),
	})
	t.Setenv("PATH", nas.bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	cfg, err := config.LoadFrom(c.config)
	if err != nil {
		t.Fatal(err)
	}
	client := remote.New(cfg)
	content := []byte(strings.Repeat("NASBox chunk protocol ", 100000))
	sum := sha256.Sum256(content)
	digest := hex.EncodeToString(sum[:])
	const chunkSize = 1 << 20
	chunks := []remote.ChunkUpload{}
	for offset := 0; offset < len(content); offset += chunkSize {
		end := offset + chunkSize
		if end > len(content) {
			end = len(content)
		}
		block := content[offset:end]
		blockSum := sha256.Sum256(block)
		chunks = append(chunks, remote.ChunkUpload{
			Digest: hex.EncodeToString(blockSum[:]), Data: block,
		})
	}
	if _, err := client.ChunkPutBatch("device-chunks", chunks); err != nil {
		t.Fatalf("chunk upload: %v", err)
	}
	digests := make([]string, 0, len(chunks))
	for _, chunk := range chunks {
		digests = append(digests, chunk.Digest)
	}
	stamp := time.Now().Unix() * 1_000_000_000
	if err := client.FilePublish("cartella/file.bin", digest, int64(len(content)), stamp, "device-chunks:1",
		digests, "device-chunks", "chunk-tx-1", remote.RunTimestamp()); err != nil {
		t.Fatalf("file publish: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(nas.share, "cartella", "file.bin"))
	if err != nil || !bytes.Equal(got, content) {
		t.Fatalf("file sul NAS diverso: len=%d err=%v", len(got), err)
	}
	info, err := client.FileChunks("cartella/file.bin")
	if err != nil || !info.Found || info.Digest != digest || len(info.Chunks) != len(chunks) {
		t.Fatalf("file-chunks: %+v err=%v", info, err)
	}
	for _, chunk := range info.Chunks {
		data, err := client.ChunkGet(chunk)
		if err != nil {
			t.Fatalf("chunk-get %s: %v", chunk, err)
		}
		blockSum := sha256.Sum256(data)
		if hex.EncodeToString(blockSum[:]) != chunk {
			t.Fatalf("chunk %s corrotto", chunk)
		}
	}
	if !nas.lockFree() {
		t.Fatal("il lock NAS deve essere libero")
	}
}
