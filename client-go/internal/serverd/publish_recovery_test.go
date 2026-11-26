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
)

const (
	publishTestDevice = "publish-test"
	publishTestRunTS  = "2026-11-26--12-00-00-000000Z"
	publishTestMTime  = int64(1700000000123456789)
)

type publishTestRecord struct {
	path, digest, size string
	chunks             []string
}

func newPublishTestContext(t *testing.T) (*Context, string) {
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
	if err := os.WriteFile(p.Owner, []byte(publishTestDevice+"|test|1|||0|0\n"), 0o600); err != nil {
		lock.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
		_ = lock.Close()
	})
	return ctx, share
}

func makePublishTestRecord(t *testing.T, ctx *Context, path string, data []byte) publishTestRecord {
	t.Helper()
	whole := sha256.Sum256(data)
	record := publishTestRecord{
		path:   path,
		digest: fmt.Sprintf("%x", whole),
		size:   strconv.Itoa(len(data)),
		chunks: []string{},
	}
	if len(data) == 0 {
		return record
	}
	store, err := ctx.chunkStore()
	if err != nil {
		t.Fatal(err)
	}
	chunk, err := store.Put(data)
	if err != nil {
		t.Fatal(err)
	}
	record.chunks = append(record.chunks, chunk)
	return record
}

func appendPublishTestRecord(frame *bytes.Buffer, record publishTestRecord) {
	for _, field := range []string{
		record.path,
		record.digest,
		record.size,
		strconv.FormatInt(publishTestMTime, 10),
		"",
	} {
		frame.WriteString(field)
		frame.WriteByte(0)
	}
	fmt.Fprintf(frame, "%d\x00", len(record.chunks))
	for _, chunk := range record.chunks {
		frame.WriteString(chunk)
		frame.WriteByte(0)
	}
}

func publishTestBatchFrame(records []publishTestRecord) []byte {
	return publishTestBatchFrameMode(records, "replace")
}

func publishTestBatchFrameMode(records []publishTestRecord, mode string) []byte {
	var frame bytes.Buffer
	frame.WriteString("FILE_PUBLISH_BATCH_V2\x00")
	fmt.Fprintf(&frame, "%d\x00", len(records))
	frame.WriteString(mode + "\x00")
	for _, record := range records {
		appendPublishTestRecord(&frame, record)
	}
	return frame.Bytes()
}

func publishTestSingleFrame(record publishTestRecord) []byte {
	var frame bytes.Buffer
	frame.WriteString("FILE_PUBLISH_V1\x00")
	appendPublishTestRecord(&frame, record)
	return frame.Bytes()
}

func runPublishTestCommand(t *testing.T, ctx *Context, batch bool, frame []byte) error {
	t.Helper()
	input, err := os.CreateTemp(t.TempDir(), "publish-stdin-*")
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	if _, err := input.Write(frame); err != nil {
		t.Fatal(err)
	}
	if _, err := input.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	oldStdin := os.Stdin
	os.Stdin = input
	defer func() { os.Stdin = oldStdin }()
	args := []string{publishTestDevice, "tx-publish-test", publishTestRunTS}
	if batch {
		return cmdFilePublishBatch(ctx, args)
	}
	return cmdFilePublish(ctx, args)
}

func requirePublishTestContents(t *testing.T, path string, want []byte) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("%s contiene %q, voglio %q", path, got, want)
	}
}

func requirePublishTestAbsent(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("%s dovrebbe essere assente, errore Lstat: %v", path, err)
	}
}

func TestFilePublishBatchRollbackRemovesNewFilesAndRestoresBackups(t *testing.T) {
	ctx, share := newPublishTestContext(t)
	oldPath := filepath.Join(share, "vecchio.txt")
	oldData := []byte("contenuto precedente")
	if err := os.WriteFile(oldPath, oldData, 0o600); err != nil {
		t.Fatal(err)
	}
	replaced := makePublishTestRecord(t, ctx, "vecchio.txt", []byte("nuovo contenuto"))
	created := makePublishTestRecord(t, ctx, "nuovo.txt", []byte("appena creato"))
	broken := makePublishTestRecord(t, ctx, "rotto.txt", []byte("chunk mancante"))
	broken.chunks = []string{strings.Repeat("f", 64)}

	err := runPublishTestCommand(t, ctx, true, publishTestBatchFrame([]publishTestRecord{replaced, created, broken}))
	if err == nil {
		t.Fatal("la pubblicazione con un chunk mancante dovrebbe fallire")
	}
	requirePublishTestContents(t, oldPath, oldData)
	requirePublishTestAbsent(t, filepath.Join(share, created.path))
	requirePublishTestAbsent(t, filepath.Join(share, broken.path))
	requirePublishTestAbsent(t, filepath.Join(share, publishBackupRelative(replaced.path, publishTestRunTS)))
	if journal := readString(ctx.paths().Journal); strings.Contains(journal, "BEGIN\ttx-publish-test") {
		t.Fatalf("una transazione non commessa è rimasta nel journal: %q", journal)
	}
}

func TestFilePublishSingleRollbackRestoresPreviousFile(t *testing.T) {
	ctx, share := newPublishTestContext(t)
	target := filepath.Join(share, "file.txt")
	oldData := []byte("precedente")
	if err := os.WriteFile(target, oldData, 0o600); err != nil {
		t.Fatal(err)
	}
	record := makePublishTestRecord(t, ctx, "file.txt", []byte("nuovo"))
	record.chunks = []string{strings.Repeat("f", 64)}

	if err := runPublishTestCommand(t, ctx, false, publishTestSingleFrame(record)); err == nil {
		t.Fatal("la pubblicazione con un chunk mancante dovrebbe fallire")
	}
	requirePublishTestContents(t, target, oldData)
	requirePublishTestAbsent(t, filepath.Join(share, publishBackupRelative(record.path, publishTestRunTS)))
}

func TestFilePublishAcceptsEmptyFile(t *testing.T) {
	for _, batch := range []bool{false, true} {
		name := "single"
		if batch {
			name = "batch"
		}
		t.Run(name, func(t *testing.T) {
			ctx, share := newPublishTestContext(t)
			record := makePublishTestRecord(t, ctx, "vuoto.txt", nil)
			frame := publishTestSingleFrame(record)
			if batch {
				frame = publishTestBatchFrame([]publishTestRecord{record})
			}
			if err := runPublishTestCommand(t, ctx, batch, frame); err != nil {
				t.Fatal(err)
			}
			target := filepath.Join(share, record.path)
			info, err := os.Lstat(target)
			if err != nil {
				t.Fatal(err)
			}
			if !info.Mode().IsRegular() || info.Size() != 0 {
				t.Fatalf("il file vuoto non è stato pubblicato correttamente: %+v", info)
			}
			store, err := ctx.chunkStore()
			if err != nil {
				t.Fatal(err)
			}
			chunks, err := store.LoadFileMeta(record.digest)
			if err != nil {
				t.Fatal(err)
			}
			if len(chunks) != 0 {
				t.Fatalf("il file vuoto ha %d chunk, voglio 0", len(chunks))
			}
		})
	}
}

func TestFilePublishRejectsEmptyChunkListForNonEmptyFile(t *testing.T) {
	for _, batch := range []bool{false, true} {
		name := "single"
		if batch {
			name = "batch"
		}
		t.Run(name, func(t *testing.T) {
			ctx, share := newPublishTestContext(t)
			record := makePublishTestRecord(t, ctx, "non-vuoto.txt", []byte("dati"))
			record.chunks = []string{}
			frame := publishTestSingleFrame(record)
			if batch {
				frame = publishTestBatchFrame([]publishTestRecord{record})
			}
			if err := runPublishTestCommand(t, ctx, batch, frame); err == nil {
				t.Fatal("un file non vuoto senza chunk dovrebbe essere rifiutato")
			}
			requirePublishTestAbsent(t, filepath.Join(share, record.path))
		})
	}
}

func TestFilePublishCommitFailureRollsBackBeforeCommit(t *testing.T) {
	for _, batch := range []bool{false, true} {
		name := "single"
		if batch {
			name = "batch"
		}
		t.Run(name, func(t *testing.T) {
			ctx, share := newPublishTestContext(t)
			target := filepath.Join(share, "file.txt")
			oldData := []byte("precedente")
			if err := os.WriteFile(target, oldData, 0o600); err != nil {
				t.Fatal(err)
			}
			record := makePublishTestRecord(t, ctx, "file.txt", []byte("pubblicato"))
			// Force bumpRevision to fail after appendTransactionLocked has written
			// the journal block. Its error path truncates the journal again.
			revisionTmp := ctx.paths().Revision + ".tmp." + strconv.Itoa(os.Getpid())
			if err := os.Mkdir(revisionTmp, 0o700); err != nil {
				t.Fatal(err)
			}
			frame := publishTestSingleFrame(record)
			if batch {
				frame = publishTestBatchFrame([]publishTestRecord{record})
			}
			if err := runPublishTestCommand(t, ctx, batch, frame); err == nil {
				t.Fatal("il commit dovrebbe fallire per il percorso revisione temporaneo non scrivibile")
			}
			requirePublishTestContents(t, target, oldData)
			requirePublishTestAbsent(t, filepath.Join(share, publishBackupRelative(record.path, publishTestRunTS)))
		})
	}
}

func TestFilePublishBatchCreateRejectsConcurrentTarget(t *testing.T) {
	ctx, share := newPublishTestContext(t)
	target := filepath.Join(share, "esistente.txt")
	oldData := []byte("creato da un altro dispositivo")
	if err := os.WriteFile(target, oldData, 0o600); err != nil {
		t.Fatal(err)
	}
	record := makePublishTestRecord(t, ctx, "esistente.txt", []byte("upload locale"))
	frame := publishTestBatchFrameMode([]publishTestRecord{record}, "create")
	if err := runPublishTestCommand(t, ctx, true, frame); err == nil {
		t.Fatal("una pubblicazione create ha sostituito un target concorrente")
	}
	requirePublishTestContents(t, target, oldData)
	requirePublishTestAbsent(t, filepath.Join(share, publishBackupRelative(record.path, publishTestRunTS)))
}

func TestFilePublishBatchCreateRetryIsIdempotent(t *testing.T) {
	ctx, share := newPublishTestContext(t)
	record := makePublishTestRecord(t, ctx, "nuovo.txt", []byte("contenuto"))
	frame := publishTestBatchFrameMode([]publishTestRecord{record}, "create")
	if err := runPublishTestCommand(t, ctx, true, frame); err != nil {
		t.Fatal(err)
	}
	if err := runPublishTestCommand(t, ctx, true, frame); err != nil {
		t.Fatalf("retry della stessa transazione: %v", err)
	}
	requirePublishTestContents(t, filepath.Join(share, record.path), []byte("contenuto"))
	requirePublishTestAbsent(t, filepath.Join(share, publishBackupRelative(record.path, publishTestRunTS)))
}

func TestFilePublishRejectsSymlinkAncestors(t *testing.T) {
	for _, batch := range []bool{false, true} {
		name := "single"
		if batch {
			name = "batch"
		}
		t.Run(name, func(t *testing.T) {
			ctx, share := newPublishTestContext(t)
			outside := t.TempDir()
			if err := os.Symlink(outside, filepath.Join(share, "collegamento")); err != nil {
				t.Fatal(err)
			}
			record := makePublishTestRecord(t, ctx, "collegamento/file.txt", []byte("non uscire dalla share"))
			frame := publishTestSingleFrame(record)
			if batch {
				frame = publishTestBatchFrame([]publishTestRecord{record})
			}
			if err := runPublishTestCommand(t, ctx, batch, frame); err == nil {
				t.Fatal("un percorso con antenato symlink dovrebbe essere rifiutato")
			}
			requirePublishTestAbsent(t, filepath.Join(outside, "file.txt"))
			requirePublishTestAbsent(t, filepath.Join(share, record.path))
		})
	}
}

func TestFilePublishRejectsSymlinkInBackupPath(t *testing.T) {
	for _, batch := range []bool{false, true} {
		name := "single"
		if batch {
			name = "batch"
		}
		t.Run(name, func(t *testing.T) {
			ctx, share := newPublishTestContext(t)
			outside := t.TempDir()
			if err := os.Symlink(outside, filepath.Join(share, ".sync-trash")); err != nil {
				t.Fatal(err)
			}
			record := makePublishTestRecord(t, ctx, "file.txt", []byte("dati"))
			frame := publishTestSingleFrame(record)
			if batch {
				frame = publishTestBatchFrame([]publishTestRecord{record})
			}
			if err := runPublishTestCommand(t, ctx, batch, frame); err == nil {
				t.Fatal("un percorso backup con antenato symlink dovrebbe essere rifiutato")
			}
			requirePublishTestAbsent(t, filepath.Join(share, record.path))
			requirePublishTestAbsent(t, filepath.Join(outside, record.path+"-"+publishTestRunTS))
		})
	}
}
