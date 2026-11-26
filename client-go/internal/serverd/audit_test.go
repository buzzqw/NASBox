package serverd

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeAged(t *testing.T, share, rel, content string, age time.Duration) {
	t.Helper()
	full := filepath.Join(share, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	when := time.Now().Add(-age)
	if err := os.Chtimes(full, when, when); err != nil {
		t.Fatal(err)
	}
}

func putRow(ctx *Context, t *testing.T, path, digest string) {
	t.Helper()
	row := journalRowPUT(encodeField(path), digest, "1", "1", "pc", journalNow(), "")
	if err := ctx.appendTransaction("tx-"+strings.ReplaceAll(path, "/", "-"), "pc", []string{row}); err != nil {
		t.Fatal(err)
	}
}

func TestAuditOrphansIgnoresInternalAndTemporaryPaths(t *testing.T) {
	ctx, share := newWebTestContext(t, "")
	const old = time.Hour
	writeAged(t, share, "orfano.txt", "x", old)
	writeAged(t, share, "Cartella/annidato.txt", "x", old)
	writeAged(t, share, "RPG/.sync-trash/vecchio.txt-2026-11-20--10-00-00", "x", old)
	writeAged(t, share, ".sync-trash/radice.txt", "x", old)
	writeAged(t, share, "Doc/.report.pdf.Ab12Cd", "x", old) // temporaneo di rsync
	writeAged(t, share, "Doc/@eaDir/meta", "x", old)
	writeAged(t, share, "vivo.txt", "x", old)
	putRow(ctx, t, "vivo.txt", strings.Repeat("a", 64))

	orphans, err := ctx.orphanPaths()
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(orphans, ","); got != "Cartella/annidato.txt,orfano.txt" {
		t.Fatalf("orfani = %q", got)
	}
}

func TestAuditAdoptJournalsRealDigestAndSkipsFreshFiles(t *testing.T) {
	ctx, share := newWebTestContext(t, "")
	writeAged(t, share, "adottabile.txt", "contenuto vero", time.Hour)
	writeAged(t, share, "appena-scritto.txt", "in corso", 5*time.Second)

	if err := cmdAuditAdopt(ctx, []string{"--dry-run"}); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := ctx.lookupManifest("adottabile.txt"); ok {
		t.Fatal("--dry-run non deve scrivere il journal")
	}
	if err := cmdAuditAdopt(ctx, nil); err != nil {
		t.Fatal(err)
	}
	want := fmt.Sprintf("%x", sha256.Sum256([]byte("contenuto vero")))
	row, ok, err := ctx.lookupManifest("adottabile.txt")
	if err != nil || !ok || !strings.Contains(row, want) {
		t.Fatalf("riga adottata mancante o con digest errato: %q ok=%v err=%v", row, ok, err)
	}
	if _, ok, _ := ctx.lookupManifest("appena-scritto.txt"); ok {
		t.Fatal("un file scritto da pochi secondi può essere un trasferimento in corso")
	}
	// Il mtime è in nanosecondi esatti: il fast path di --file-states-current lo riconosce.
	info, _ := os.Stat(filepath.Join(share, "adottabile.txt"))
	if !strings.Contains(row, fmt.Sprint(info.ModTime().UnixNano())) {
		t.Fatalf("mtime non in nanosecondi esatti: %q", row)
	}
	orphans, _ := ctx.orphanPaths()
	if len(orphans) != 1 || orphans[0] != "appena-scritto.txt" {
		t.Fatalf("dopo l'adozione resta solo il file recente: %v", orphans)
	}
}

func TestAuditPhantomsForgetTombstonesRowsWithoutFile(t *testing.T) {
	ctx, share := newWebTestContext(t, "")
	writeAged(t, share, "presente.txt", "x", time.Hour)
	putRow(ctx, t, "presente.txt", strings.Repeat("b", 64))
	putRow(ctx, t, "sparito.txt", strings.Repeat("c", 64))
	putRow(ctx, t, "e-una-cartella", strings.Repeat("d", 64))
	if err := os.Mkdir(filepath.Join(share, "e-una-cartella"), 0o700); err != nil {
		t.Fatal(err)
	}

	phantoms, err := ctx.phantomPaths()
	if err != nil {
		t.Fatal(err)
	}
	if len(phantoms) != 1 || phantoms[0] != "sparito.txt" {
		t.Fatalf("fantasmi = %v (una cartella al posto del file non è un fantasma)", phantoms)
	}
	if err := cmdAuditPhantoms(ctx, []string{"--forget", "--dry-run"}); err != nil {
		t.Fatal(err)
	}
	if after, _ := ctx.phantomPaths(); len(after) != 1 {
		t.Fatal("--dry-run non deve scrivere il journal")
	}
	if err := cmdAuditPhantoms(ctx, []string{"--forget"}); err != nil {
		t.Fatal(err)
	}
	if after, _ := ctx.phantomPaths(); len(after) != 0 {
		t.Fatalf("dopo --forget restano fantasmi: %v", after)
	}
}

func TestJournalForgetTombstonesPathsAndRefusesUnsafeOnes(t *testing.T) {
	ctx, share := newWebTestContext(t, "")
	putRow(ctx, t, "RPG/.sync-trash/x.txt", strings.Repeat("e", 64))
	if err := os.MkdirAll(filepath.Join(share, "reale"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(share, filepath.Join(share, "reale", "link")); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]bool{
		"RPG/.sync-trash/x.txt": true, "semplice.txt": true,
		"": false, "/assoluto": false, "../fuori": false, "a/../b": false, "reale/link/dentro.txt": false,
	} {
		if got := ctx.validForgetPath(path); got != want {
			t.Fatalf("validForgetPath(%q) = %v, atteso %v", path, got, want)
		}
	}
	reader, writer, _ := os.Pipe()
	old := os.Stdin
	os.Stdin = reader
	defer func() { os.Stdin = old }()
	go func() {
		_, _ = writer.WriteString("RPG/.sync-trash/x.txt\x00../fuori\x00")
		_ = writer.Close()
	}()
	if err := cmdJournalForget(ctx); err != nil {
		t.Fatal(err)
	}
	if row, ok, _ := ctx.lookupManifest("RPG/.sync-trash/x.txt"); ok && len(strings.Split(row, "\t")[1]) == 64 {
		t.Fatalf("la riga doveva diventare una tombstone: %q", row)
	}
}
