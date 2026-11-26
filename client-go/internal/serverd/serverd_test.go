package serverd

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestEncodeDecodeField(t *testing.T) {
	original := "cartella/con%tab\tnewline\nfine\r"
	if got := decodeField(encodeField(original)); got != original {
		t.Fatalf("roundtrip: %q != %q", got, original)
	}
	if got := decodeField("%2509"); got != "%09" {
		t.Fatalf("%%2509 must decode to literal %%09, got %q", got)
	}
}

func TestConfigDefaultsApplied(t *testing.T) {
	dir := t.TempDir()
	conf := filepath.Join(dir, "server.conf")
	content := "SHARE_ROOT=" + dir + "\nLOG_MAX_BYTES=5242880 # commento\n"
	if err := os.WriteFile(conf, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := readConfig(conf)
	if err != nil {
		t.Fatal(err)
	}
	applyDefaults(cfg)
	if cfg["JOURNAL_MAX_BYTES"] != "10485760" {
		t.Errorf("missing default: %q", cfg["JOURNAL_MAX_BYTES"])
	}
	if cfg["LOG_MAX_BYTES"] != "5242880" {
		t.Errorf("inline comment not stripped: %q", cfg["LOG_MAX_BYTES"])
	}
}

func newTestContext(t *testing.T) (*Context, string) {
	t.Helper()
	dir := t.TempDir()
	state := filepath.Join(dir, "state")
	share := filepath.Join(dir, "share")
	if err := os.MkdirAll(state, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(share, 0o700); err != nil {
		t.Fatal(err)
	}
	conf := filepath.Join(dir, "server.conf")
	if err := os.WriteFile(conf, []byte("SHARE_ROOT="+share+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	opts, _, err := ParseOptions([]string{"-c", conf})
	if err != nil {
		t.Fatal(err)
	}
	ctx, err := Open(opts)
	if err != nil {
		t.Fatal(err)
	}
	ctx.StateDir = state
	return ctx, share
}

func TestRenderManifestAppliesJournalAndFiltersInternal(t *testing.T) {
	ctx, _ := newTestContext(t)
	p := ctx.paths()
	base := "NASBOX_MANIFEST_V1\n" +
		"vecchio\t" + strings.Repeat("a", 64) + "\t10\t1000000000\tdev\t100\n" +
		"RPG/.sync-trash/x\t" + strings.Repeat("b", 64) + "\t10\t1000000000\tdev\t100\n"
	journal := "NASBOX_JOURNAL_V1\n" +
		"BEGIN\ttx1\t200\tdev\n" +
		"DELETE\tvecchio\t\t0\t0\tdev\t200\n" +
		"PUT\tnuovo\t" + strings.Repeat("c", 64) + "\t20\t2000000000\tdev\t200\tdev:1\n" +
		"COMMIT\ttx1\n" +
		"BEGIN\ttx2\t300\tdev\n" +
		"PUT\tmai\td\t1\t1\tdev\t300\n"
	if err := os.WriteFile(p.Manifest, []byte(base), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p.Journal, []byte(journal), 0o600); err != nil {
		t.Fatal(err)
	}
	rows, err := ctx.renderManifest()
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(rows, "\n")
	if !strings.Contains(joined, "nuovo\t"+strings.Repeat("c", 64)+"\t20\t2000000000\tdev\t200\tdev:1") {
		t.Fatalf("committed PUT missing: %s", joined)
	}
	if strings.Contains(joined, "RPG/.sync-trash") {
		t.Fatal("internal path leaked into manifest")
	}
	if strings.Contains(joined, "mai\t") {
		t.Fatal("uncommitted transaction leaked into manifest")
	}
	if !strings.Contains(joined, "vecchio\t\t0\t0\tdev\t200\t") {
		t.Fatalf("DELETE tombstone row wrong: %s", joined)
	}
}

func TestFileStatesCurrent(t *testing.T) {
	ctx, share := newTestContext(t)
	p := ctx.paths()
	digest := strings.Repeat("a", 64)
	manifest := "NASBOX_MANIFEST_V1\n" +
		"file.txt\t" + digest + "\t3\t1000000000\tdev\t100\n" +
		"sparito\t\t0\t0\tdev\t50\tdev:1\n"
	if err := os.WriteFile(p.Manifest, []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	filePath := filepath.Join(share, "file.txt")
	if err := os.WriteFile(filePath, []byte("abc"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(filePath, time.Unix(1, 0), time.Unix(1, 0)); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(share, "cartella"), 0o700); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := ctx.fileStates("FILE_STATES_V1", []string{"file.txt", "sparito", "cartella", "assente"}, &out); err != nil {
		t.Fatal(err)
	}
	fields := strings.Split(out.String(), "\x00")
	if fields[0] != "FILE_STATES_V1" || fields[1] != "4" {
		t.Fatalf("bad framing: %q", fields[:2])
	}
	want := []string{
		"file.txt", "FILE", digest, "3", "1000000000",
		"sparito", "TOMBSTONE", "", "0", "50000000000",
		"cartella", "OTHER", "", "0", "0",
		"assente", "ABSENT", "", "0", "0",
	}
	for i, expected := range want {
		if fields[i+2] != expected {
			t.Errorf("field %d: want %q, got %q", i, expected, fields[i+2])
		}
	}
}
