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
	protocolTestDevice = "protocol-test"
	protocolTestRunTS  = "2026-11-26--12-00-00-000000Z"
)

type protocolJournalRow struct {
	action, path, digest, size, mtime string
}

func newProtocolTestContext(t *testing.T) (*Context, string) {
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
	if err := os.WriteFile(p.Owner, []byte(protocolTestDevice+"|test|1|||0|0\n"), 0o600); err != nil {
		lock.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
		_ = lock.Close()
	})
	return ctx, share
}

func protocolNULFields(fields ...string) []byte {
	return []byte(strings.Join(fields, "\x00") + "\x00")
}

func protocolJournalFrame(txid string, rows ...protocolJournalRow) []byte {
	fields := []string{"JOURNAL_V1", txid, protocolTestDevice, "1700000000", strconv.Itoa(len(rows))}
	for _, row := range rows {
		fields = append(fields, row.action, row.path, row.digest, row.size, row.mtime)
	}
	return protocolNULFields(fields...)
}

func protocolRenameV2Frame(src, dst, digest, size, mtime string) []byte {
	return protocolNULFields(
		"BROWSE_RENAME_V2", protocolTestRunTS, protocolTestDevice, src, dst,
		"FILE", digest, size, mtime,
	)
}

func runProtocolCommand(t *testing.T, ctx *Context, frame []byte, command func(*Context) error) error {
	t.Helper()
	input, err := os.CreateTemp(t.TempDir(), "protocol-stdin-*")
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
	return command(ctx)
}

func TestJournalAppendAcceptsCanonicalPathsAndValidRecords(t *testing.T) {
	ctx, _ := newProtocolTestContext(t)
	digest := strings.Repeat("a", 64)
	frame := protocolJournalFrame("tx-valid",
		protocolJournalRow{"PUT", "documenti/file.txt", digest, "3", "1700000000123456789"},
		protocolJournalRow{"DELETE", "vecchio.txt", "", "0", "0"},
	)
	if err := runProtocolCommand(t, ctx, frame, cmdJournalAppend); err != nil {
		t.Fatal(err)
	}
	journalBytes, err := os.ReadFile(ctx.paths().Journal)
	if err != nil {
		t.Fatal(err)
	}
	journal := string(journalBytes)
	if !strings.Contains(journal, "BEGIN\ttx-valid\t") || !strings.Contains(journal, "COMMIT\ttx-valid\n") {
		t.Fatalf("transazione non commessa: %q", journal)
	}
	if !strings.Contains(journal, "PUT\tdocumenti/file.txt\t"+digest+"\t3\t1700000000123456789\t") {
		t.Fatalf("PUT mancante o alterato: %q", journal)
	}
	if !strings.Contains(journal, "DELETE\tvecchio.txt\t\t0\t0\t") {
		t.Fatalf("DELETE mancante o alterato: %q", journal)
	}
	if got := readString(ctx.paths().Revision); got != "1" {
		t.Fatalf("revisione = %q, attesa 1", got)
	}
}

func TestJournalAppendRejectsInvalidRecordsWithoutPartialWrites(t *testing.T) {
	digest := strings.Repeat("a", 64)
	valid := protocolJournalRow{"PUT", "primo.txt", digest, "1", "1"}
	tests := []struct {
		name string
		row  protocolJournalRow
	}{
		{"empty path", protocolJournalRow{"PUT", "", digest, "1", "1"}},
		{"absolute path", protocolJournalRow{"PUT", "/esterno.txt", digest, "1", "1"}},
		{"parent traversal", protocolJournalRow{"PUT", "../esterno.txt", digest, "1", "1"}},
		{"nested traversal", protocolJournalRow{"PUT", "cartella/../../esterno.txt", digest, "1", "1"}},
		{"dot component", protocolJournalRow{"PUT", "cartella/./file.txt", digest, "1", "1"}},
		{"empty component", protocolJournalRow{"PUT", "cartella//file.txt", digest, "1", "1"}},
		{"trailing slash", protocolJournalRow{"PUT", "cartella/", digest, "1", "1"}},
		{"trash path", protocolJournalRow{"PUT", ".sync-trash/file.txt", digest, "1", "1"}},
		{"nested trash path", protocolJournalRow{"PUT", "cartella/.sync-trash/file.txt", digest, "1", "1"}},
		{"partial path", protocolJournalRow{"PUT", "cartella/.sync-partial/file.txt", digest, "1", "1"}},
		{"staging path", protocolJournalRow{"PUT", ".nasbox-staging/file.txt", digest, "1", "1"}},
		{"repository marker path", protocolJournalRow{"PUT", ".nasbox-root", digest, "1", "1"}},
		{"metadata path", protocolJournalRow{"PUT", "cartella/@eaDir/file.txt", digest, "1", "1"}},
		{"put without digest", protocolJournalRow{"PUT", "file.txt", "", "1", "1"}},
		{"put with short digest", protocolJournalRow{"PUT", "file.txt", "abc", "1", "1"}},
		{"put with uppercase digest", protocolJournalRow{"PUT", "file.txt", strings.Repeat("A", 64), "1", "1"}},
		{"delete with malformed digest", protocolJournalRow{"DELETE", "file.txt", "abc", "0", "0"}},
		{"size overflow", protocolJournalRow{"PUT", "file.txt", digest, "9223372036854775808", "1"}},
		{"size underflow", protocolJournalRow{"PUT", "file.txt", digest, "-9223372036854775809", "1"}},
		{"size not numeric", protocolJournalRow{"PUT", "file.txt", digest, "grande", "1"}},
		{"mtime overflow", protocolJournalRow{"PUT", "file.txt", digest, "1", "9223372036854775808"}},
		{"mtime underflow", protocolJournalRow{"PUT", "file.txt", digest, "1", "-9223372036854775809"}},
		{"mtime not numeric", protocolJournalRow{"PUT", "file.txt", digest, "1", "adesso"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, _ := newProtocolTestContext(t)
			frame := protocolJournalFrame("tx-invalid", valid, tt.row)
			if err := runProtocolCommand(t, ctx, frame, cmdJournalAppend); err == nil {
				t.Fatal("record non valido accettato")
			}
			if got := readString(ctx.paths().Journal); got != "NASBOX_JOURNAL_V1" {
				t.Fatalf("journal modificato da una richiesta non valida: %q", got)
			}
			if got := readString(ctx.paths().Revision); got != "0" {
				t.Fatalf("revisione modificata da una richiesta non valida: %q", got)
			}
		})
	}
}

func TestBrowseRenameV2ComparesMTimeSeconds(t *testing.T) {
	ctx, share := newProtocolTestContext(t)
	const src, dst = "vecchio.txt", "nuovo.txt"
	source := filepath.Join(share, src)
	target := filepath.Join(share, dst)
	data := []byte("contenuto")
	if err := os.WriteFile(source, data, 0o600); err != nil {
		t.Fatal(err)
	}
	mtime := time.Unix(1_700_000_000, 987_654_321)
	if err := os.Chtimes(source, mtime, mtime); err != nil {
		t.Fatal(err)
	}
	whole := sha256.Sum256(data)
	digest := fmt.Sprintf("%x", whole)
	mtimeNS := strconv.FormatInt(mtime.UnixNano(), 10)
	frame := protocolRenameV2Frame(src, dst, digest, strconv.Itoa(len(data)), mtimeNS)
	if err := runProtocolCommand(t, ctx, frame, cmdBrowseRename); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(source); !os.IsNotExist(err) {
		t.Fatalf("la sorgente non e stata rimossa: %v", err)
	}
	got, err := os.ReadFile(target)
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("destinazione errata: %q (%v)", got, err)
	}
	journal := readString(ctx.paths().Journal)
	if !strings.Contains(journal, "PUT\t"+dst+"\t"+digest+"\t"+strconv.Itoa(len(data))+"\t"+mtimeNS+"\t") {
		t.Fatalf("mtime ns non conservato nel journal: %q", journal)
	}
}

func TestBrowseRenameV2RejectsInvalidNumericPreconditions(t *testing.T) {
	tests := []struct {
		name, size, mtime string
		data              []byte
		stamp             time.Time
	}{
		{
			name: "mtime overflow at epoch", size: "0", mtime: "9223372036854775808",
			data: nil, stamp: time.Unix(0, 123_456_789),
		},
		{
			name: "size overflow for empty file", size: "9223372036854775808", mtime: "123456789",
			data: nil, stamp: time.Unix(0, 123_456_789),
		},
		{
			name: "mtime underflow at epoch", size: "0", mtime: "-9223372036854775809",
			data: nil, stamp: time.Unix(0, 123_456_789),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, share := newProtocolTestContext(t)
			const src, dst = "vecchio.txt", "nuovo.txt"
			source := filepath.Join(share, src)
			if err := os.WriteFile(source, tt.data, 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Chtimes(source, tt.stamp, tt.stamp); err != nil {
				t.Fatal(err)
			}
			whole := sha256.Sum256(tt.data)
			frame := protocolRenameV2Frame(src, dst, fmt.Sprintf("%x", whole), tt.size, tt.mtime)
			if err := runProtocolCommand(t, ctx, frame, cmdBrowseRename); err == nil {
				t.Fatal("precondizione numerica non valida accettata")
			}
			if _, err := os.Lstat(source); err != nil {
				t.Fatalf("la sorgente e stata modificata: %v", err)
			}
			if _, err := os.Lstat(filepath.Join(share, dst)); !os.IsNotExist(err) {
				t.Fatalf("la destinazione non dovrebbe esistere: %v", err)
			}
			if got := readString(ctx.paths().Journal); got != "NASBOX_JOURNAL_V1" {
				t.Fatalf("journal modificato da una richiesta non valida: %q", got)
			}
		})
	}
}

func TestTransactionCommittedRequiresExactTxID(t *testing.T) {
	ctx, _ := newTestContext(t)
	if err := ctx.ensureJournalFiles(); err != nil {
		t.Fatal(err)
	}
	journal := "NASBOX_JOURNAL_V1\n" +
		"BEGIN\ttx\t100\tdev\nCOMMIT\ttx\n" +
		"BEGIN\ttx-extra\t101\tdev\nCOMMIT\ttx-extra\n"
	if err := os.WriteFile(ctx.paths().Journal, []byte(journal), 0o600); err != nil {
		t.Fatal(err)
	}
	// Force the text-journal fallback so the exact COMMIT parser is exercised.
	if err := os.Mkdir(filepath.Join(ctx.paths().Dir, metadataDBName), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		txid string
		want bool
	}{
		{"tx", true},
		{"tx-extra", true},
		{"t", false},
		{"tx-other", false},
		{"tx-extra-more", false},
	} {
		t.Run(tt.txid, func(t *testing.T) {
			var got bool
			if err := ctx.withJournalLock(func() error {
				var err error
				got, err = ctx.transactionCommitted(tt.txid)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Fatalf("transactionCommitted(%q) = %v, atteso %v", tt.txid, got, tt.want)
			}
		})
	}
}
