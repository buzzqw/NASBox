package serverd

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

const (
	restoreTestDevice = "restore-test"
	restoreTestTS     = "2026-11-26--12-00-00-000000Z"
)

type restoreTestRequest struct {
	path, version string
}

func newRestoreTestContext(t *testing.T) (*Context, string) {
	t.Helper()
	ctx, share := newTestContext(t)
	if err := ctx.ensureJournalFiles(); err != nil {
		t.Fatal(err)
	}
	p := ctx.paths()
	lock, err := os.OpenFile(p.Lock, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		lock.Close()
		t.Fatal(err)
	}
	if err := os.WriteFile(p.Owner, []byte(restoreTestDevice+"|test|1||0|0\n"), 0o600); err != nil {
		lock.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
		_ = lock.Close()
	})
	return ctx, share
}

func runRestoreTestCommand(t *testing.T, ctx *Context, requests []restoreTestRequest) ([]byte, error) {
	t.Helper()
	var payload bytes.Buffer
	fmt.Fprintf(&payload, "RESTORE_BATCH_V1\x00%s\x00%d\x00", restoreTestDevice, len(requests))
	for _, request := range requests {
		fmt.Fprintf(&payload, "%s\x00%s\x00", request.path, request.version)
	}
	input, err := os.CreateTemp(t.TempDir(), "restore-stdin-*")
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	if _, err := input.Write(payload.Bytes()); err != nil {
		t.Fatal(err)
	}
	if _, err := input.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	output, err := os.CreateTemp(t.TempDir(), "restore-stdout-*")
	if err != nil {
		t.Fatal(err)
	}
	defer output.Close()

	oldStdin, oldStdout := os.Stdin, os.Stdout
	os.Stdin, os.Stdout = input, output
	defer func() { os.Stdin, os.Stdout = oldStdin, oldStdout }()
	commandErr := Dispatch(ctx, "--restore-batch", nil)
	data, readErr := os.ReadFile(output.Name())
	if readErr != nil {
		t.Fatal(readErr)
	}
	return data, commandErr
}

func parseRestoreTestOutput(t *testing.T, output []byte) map[string]string {
	t.Helper()
	fields := strings.Split(string(output), "\x00")
	if len(fields) > 0 && fields[len(fields)-1] == "" {
		fields = fields[:len(fields)-1]
	}
	if len(fields) < 2 || fields[0] != "RESTORE_BATCH_V1" {
		t.Fatalf("framing restore non valido: %q", fields)
	}
	var count int
	if _, err := fmt.Sscan(fields[1], &count); err != nil || count < 0 || len(fields) != 2+count*2 {
		t.Fatalf("conteggio restore non valido: %q", fields)
	}
	statuses := make(map[string]string, count)
	for i := 0; i < count; i++ {
		statuses[fields[2+i*2]] = fields[3+i*2]
	}
	return statuses
}

func TestRestoreBatchRestoresVersionBacksUpCurrentAndJournals(t *testing.T) {
	ctx, share := newRestoreTestContext(t)
	relative := "documenti/file.txt"
	source := filepath.Join(share, ".sync-trash", "documenti", "file.txt-"+restoreTestTS)
	target := filepath.Join(share, relative)
	restored := []byte("versione ripristinata")
	current := []byte("versione corrente")
	if err := os.MkdirAll(filepath.Dir(source), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, restored, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, current, 0o600); err != nil {
		t.Fatal(err)
	}
	sourceStamp := time.Unix(1_700_000_000, 123456789)
	if err := os.Chtimes(source, sourceStamp, sourceStamp); err != nil {
		t.Fatal(err)
	}
	targetStamp := time.Unix(1_700_000_100, 0)
	if err := os.Chtimes(target, targetStamp, targetStamp); err != nil {
		t.Fatal(err)
	}

	request := []restoreTestRequest{{path: relative, version: restoreTestTS}}
	output, err := runRestoreTestCommand(t, ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if got := parseRestoreTestOutput(t, output); got[relative] != "RESTORED" || len(got) != 1 {
		t.Fatalf("stato restore inatteso: %v", got)
	}
	if data, err := os.ReadFile(target); err != nil || !bytes.Equal(data, restored) {
		t.Fatalf("target non ripristinato: dati=%q errore=%v", data, err)
	}
	if _, err := os.Lstat(source); !os.IsNotExist(err) {
		t.Fatalf("la versione ripristinata è ancora nel cestino: %v", err)
	}

	trashDir := filepath.Join(share, ".sync-trash", "documenti")
	entries, err := os.ReadDir(trashDir)
	if err != nil {
		t.Fatal(err)
	}
	var backups [][]byte
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "file.txt-") {
			data, err := os.ReadFile(filepath.Join(trashDir, entry.Name()))
			if err != nil {
				t.Fatal(err)
			}
			backups = append(backups, data)
		}
	}
	if len(backups) != 1 || !bytes.Equal(backups[0], current) {
		t.Fatalf("backup della versione corrente non corretto: %q", backups)
	}

	digest := fmt.Sprintf("%x", sha256.Sum256(restored))
	sourceInfo, err := os.Lstat(target)
	if err != nil {
		t.Fatal(err)
	}
	journal := readString(ctx.paths().Journal)
	wantPUT := "PUT\t" + encodeField(relative) + "\t" + digest + "\t" + fmt.Sprint(len(restored)) + "\t" + fmt.Sprint(sourceInfo.ModTime().UnixNano()) + "\t"
	if strings.Count(journal, wantPUT) != 1 {
		t.Fatalf("PUT restore mancante o duplicato:\n%s", journal)
	}
	if got := readString(ctx.paths().Revision); got != "1" {
		t.Fatalf("revisione dopo restore: %q, voglio 1", got)
	}

	// The trash source is gone after the first restore. A retry recognises the
	// journaled bytes at the target and does not append a second PUT.
	output, err = runRestoreTestCommand(t, ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if got := parseRestoreTestOutput(t, output); got[relative] != "RESTORED" || len(got) != 1 {
		t.Fatalf("stato retry inatteso: %v", got)
	}
	if got := readString(ctx.paths().Revision); got != "1" {
		t.Fatalf("revisione dopo retry: %q, voglio 1", got)
	}
	if strings.Count(readString(ctx.paths().Journal), wantPUT) != 1 {
		t.Fatal("il retry ha duplicato il PUT restore")
	}
}

func TestRestoreBatchJournalFailureRollsBackFilesystem(t *testing.T) {
	ctx, share := newRestoreTestContext(t)
	relative := "rollback.txt"
	source := filepath.Join(share, ".sync-trash", "rollback.txt-"+restoreTestTS)
	target := filepath.Join(share, relative)
	if err := os.MkdirAll(filepath.Dir(source), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, []byte("da ripristinare"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("corrente"), 0o600); err != nil {
		t.Fatal(err)
	}
	revisionTmp := ctx.paths().Revision + ".tmp." + strconv.Itoa(os.Getpid())
	if err := os.Mkdir(revisionTmp, 0o700); err != nil {
		t.Fatal(err)
	}

	output, err := runRestoreTestCommand(t, ctx, []restoreTestRequest{{path: relative, version: restoreTestTS}})
	if err != nil {
		t.Fatal(err)
	}
	if got := parseRestoreTestOutput(t, output); got[relative] != "ERROR" || len(got) != 1 {
		t.Fatalf("stato rollback inatteso: %v", got)
	}
	if data, err := os.ReadFile(target); err != nil || !bytes.Equal(data, []byte("corrente")) {
		t.Fatalf("target non ripristinato dopo rollback: dati=%q errore=%v", data, err)
	}
	if data, err := os.ReadFile(source); err != nil || !bytes.Equal(data, []byte("da ripristinare")) {
		t.Fatalf("versione non restituita al cestino: dati=%q errore=%v", data, err)
	}
	if got := readString(ctx.paths().Revision); got != "0" {
		t.Fatalf("revisione dopo rollback: %q, voglio 0", got)
	}
	if strings.Contains(readString(ctx.paths().Journal), "PUT\t"+encodeField(relative)+"\t") {
		t.Fatal("un PUT non commesso è rimasto nel journal")
	}
}

func TestRestoreBatchMissingVersion(t *testing.T) {
	for _, version := range []string{"2026-11-26--13-00-00-000000Z", "latest"} {
		t.Run(version, func(t *testing.T) {
			ctx, share := newRestoreTestContext(t)
			relative := "mancante.txt"
			output, err := runRestoreTestCommand(t, ctx, []restoreTestRequest{{path: relative, version: version}})
			if err != nil {
				t.Fatal(err)
			}
			if got := parseRestoreTestOutput(t, output); got[relative] != "NO_VERSION" || len(got) != 1 {
				t.Fatalf("stato versione mancante inatteso: %v", got)
			}
			if _, err := os.Lstat(filepath.Join(share, relative)); !os.IsNotExist(err) {
				t.Fatalf("il target non dovrebbe essere stato creato: %v", err)
			}
			if got := readString(ctx.paths().Revision); got != "0" {
				t.Fatalf("una versione mancante ha modificato il journal: revisione %q", got)
			}
		})
	}
}

func TestRestoreBatchRejectsInvalidPath(t *testing.T) {
	for _, relative := range []string{
		"", "../escape.txt", "documenti/../../escape.txt", "/tmp/escape.txt",
		".sync-trash/file.txt", "documenti//file.txt",
	} {
		t.Run(relative, func(t *testing.T) {
			ctx, _ := newRestoreTestContext(t)
			output, err := runRestoreTestCommand(t, ctx, []restoreTestRequest{{path: relative, version: restoreTestTS}})
			if err == nil {
				t.Fatalf("percorso non valido accettato: %q, output=%q", relative, output)
			}
			if len(output) != 0 {
				t.Fatalf("percorso non valido con output parziale: %q", output)
			}
		})
	}
}

func TestRestoreBatchRejectsSymlinkAncestor(t *testing.T) {
	ctx, share := newRestoreTestContext(t)
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(share, "collegamento")); err != nil {
		t.Fatal(err)
	}
	relative := "collegamento/file.txt"
	source := filepath.Join(share, ".sync-trash", "collegamento", "file.txt-"+restoreTestTS)
	if err := os.MkdirAll(filepath.Dir(source), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, []byte("non uscire dalla share"), 0o600); err != nil {
		t.Fatal(err)
	}

	output, err := runRestoreTestCommand(t, ctx, []restoreTestRequest{{path: relative, version: restoreTestTS}})
	if err == nil {
		t.Fatalf("symlink accettato: output=%q", output)
	}
	if _, err := os.Lstat(filepath.Join(outside, "file.txt")); !os.IsNotExist(err) {
		t.Fatalf("file creato fuori dalla share: %v", err)
	}
	if _, err := os.Lstat(source); err != nil {
		t.Fatalf("la versione nel cestino è stata mossa nonostante il symlink: %v", err)
	}
}
