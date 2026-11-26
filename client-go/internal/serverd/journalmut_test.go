package serverd

import (
	"os"
	"strings"
	"testing"
)

func TestAppendTransactionIsIdempotentAndBumpsRevisionOnce(t *testing.T) {
	ctx, _ := newTestContext(t)
	p := ctx.paths()
	if err := os.WriteFile(p.Journal, []byte("NASBOX_JOURNAL_V1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p.Revision, []byte("41\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	rows := []string{journalRowPUT("file.txt", strings.Repeat("a", 64), "3", "1000", "dev", "200", "")}
	if err := ctx.appendTransaction("tx-1", "dev", rows); err != nil {
		t.Fatal(err)
	}
	if err := ctx.appendTransaction("tx-1", "dev", rows); err != nil {
		t.Fatal(err)
	}
	journal, err := readJournalLines(p.Journal)
	if err != nil {
		t.Fatal(err)
	}
	if count := strings.Count(strings.Join(journal, "\n"), "BEGIN\ttx-1"); count != 1 {
		t.Fatalf("transaction duplicated: %d", count)
	}
	if got := readString(p.Revision); got != "42" {
		t.Fatalf("revision = %s, want 42", got)
	}
}

func TestRequireLeaseRejectsOtherDevice(t *testing.T) {
	ctx, _ := newTestContext(t)
	p := ctx.paths()
	if err := os.WriteFile(p.Owner, []byte("altro|host|100|||0|0\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ctx.requireOwner("mio"); err == nil {
		t.Fatal("owner of another device must be rejected")
	}
	if err := ctx.requireOwner("altro"); err != nil {
		t.Fatalf("own lease accepted: %v", err)
	}
}
