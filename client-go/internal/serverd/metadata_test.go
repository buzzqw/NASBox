package serverd

import (
	"bytes"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

func writeMetadataFixture(t *testing.T, ctx *Context, manifest, journal string, revision int64) {
	t.Helper()
	p := ctx.paths()
	if err := os.WriteFile(p.Manifest, []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p.Journal, []byte(journal), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p.Revision, []byte(strconv.FormatInt(revision, 10)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestManifestDeltaIsIncremental(t *testing.T) {
	ctx, _ := newTestContext(t)
	digestA := strings.Repeat("a", 64)
	digestB := strings.Repeat("b", 64)
	writeMetadataFixture(t, ctx,
		"NASBOX_MANIFEST_V1\n"+"a.txt\t"+digestA+"\t1\t100\tdev\t100\n",
		"NASBOX_JOURNAL_V1\n", 10,
	)
	if err := ctx.appendTransaction("tx1", "dev", []string{
		journalRowPUT("b.txt", digestB, "2", "200", "dev", "101", "dev:1"),
	}); err != nil {
		t.Fatal(err)
	}

	view, err := ctx.exportManifest(10, true)
	if err != nil {
		t.Fatal(err)
	}
	if view.Current != 11 || view.FallbackFull || len(view.Rows) != 1 {
		t.Fatalf("delta non incrementale: %+v", view)
	}
	if !strings.Contains(view.Rows[0], "b.txt\t"+digestB) || strings.Contains(view.Rows[0], "a.txt\t") {
		t.Fatalf("delta contiene righe sbagliate: %q", view.Rows)
	}

	unchanged, err := ctx.exportManifest(11, true)
	if err != nil || unchanged.Current != 11 || len(unchanged.Rows) != 0 || unchanged.FallbackFull {
		t.Fatalf("delta corrente non vuoto: %+v (%v)", unchanged, err)
	}

	fallback, err := ctx.exportManifest(9, true)
	if err != nil || !fallback.FallbackFull || len(fallback.Rows) != 2 {
		t.Fatalf("fallback per base non disponibile: %+v (%v)", fallback, err)
	}
}

func TestMetadataIndexReplaysOnlyCompleteTransactions(t *testing.T) {
	ctx, _ := newTestContext(t)
	digestA := strings.Repeat("a", 64)
	digestB := strings.Repeat("b", 64)
	writeMetadataFixture(t, ctx,
		"NASBOX_MANIFEST_V1\n",
		"NASBOX_JOURNAL_V1\n"+
			"BEGIN\ttx1\t100\tdev\n"+
			"PUT\ta.txt\t"+digestA+"\t1\t100\tdev\t100\n"+
			"COMMIT\ttx1\n"+
			"BEGIN\ttx2\t101\tdev\n"+
			"PUT\tb.txt\t"+digestB+"\t2\t200\tdev\t101\n", 5,
	)
	rows, err := ctx.renderManifest()
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(rows, "\n")
	if !strings.Contains(joined, "a.txt\t"+digestA) || strings.Contains(joined, "b.txt\t") {
		t.Fatalf("transazioni incomplete applicate: %s", joined)
	}

	p := ctx.paths()
	file, err := os.OpenFile(p.Journal, os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString("COMMIT\ttx2\n"); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p.Revision, []byte("6\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	view, err := ctx.exportManifest(5, true)
	if err != nil {
		t.Fatal(err)
	}
	if view.FallbackFull || view.Current != 6 || len(view.Rows) != 1 || !strings.Contains(view.Rows[0], "b.txt\t") {
		t.Fatalf("delta dopo completamento errato: %+v", view)
	}
}

func TestRetryRepairsRevisionAfterJournalCommit(t *testing.T) {
	ctx, _ := newTestContext(t)
	writeMetadataFixture(t, ctx, "NASBOX_MANIFEST_V1\n", "NASBOX_JOURNAL_V1\n", 0)
	if _, err := ctx.renderManifest(); err != nil {
		t.Fatal(err)
	}
	digest := strings.Repeat("a", 64)
	block := "BEGIN\ttx1\t100\tdev\n" +
		journalRowPUT("a.txt", digest, "1", "100", "dev", "100", "") + "\n" +
		"COMMIT\ttx1\n"
	file, err := os.OpenFile(ctx.paths().Journal, os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString(block); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	// Simulate a crash after the journal fsync and before bumpRevision.
	if err := ctx.appendTransaction("tx1", "dev", nil); err != nil {
		t.Fatal(err)
	}
	revision, err := ctx.journalRevision()
	if err != nil || revision != 1 {
		t.Fatalf("revisione non riparata: %d (%v)", revision, err)
	}
	view, err := ctx.exportManifest(0, true)
	if err != nil || view.Current != 1 || len(view.Rows) != 1 {
		t.Fatalf("transazione riparata non visibile: %+v (%v)", view, err)
	}
}

func TestCompactionPreservesDeltaHistory(t *testing.T) {
	ctx, _ := newTestContext(t)
	ctx.Config["JOURNAL_MAX_BYTES"] = "1"
	writeMetadataFixture(t, ctx, "NASBOX_MANIFEST_V1\n", "NASBOX_JOURNAL_V1\n", 0)
	for index, name := range []string{"a.txt", "b.txt"} {
		digest := strings.Repeat(string(rune('a'+index)), 64)
		if err := ctx.appendTransaction("tx"+name, "dev", []string{
			journalRowPUT(name, digest, "1", "100", "dev", "100", ""),
		}); err != nil {
			t.Fatal(err)
		}
	}
	journal, err := os.ReadFile(ctx.paths().Journal)
	if err != nil {
		t.Fatal(err)
	}
	if string(journal) != "NASBOX_JOURNAL_V1\n" {
		t.Fatalf("journal non compattato: %q", journal)
	}
	view, err := ctx.exportManifest(0, true)
	if err != nil {
		t.Fatal(err)
	}
	if view.FallbackFull || view.Current != 2 || len(view.Rows) != 2 {
		t.Fatalf("storico delta perso durante compattazione: %+v", view)
	}
}

func TestCompactionFailureDoesNotFailCommittedMutation(t *testing.T) {
	ctx, _ := newTestContext(t)
	ctx.Config["JOURNAL_MAX_BYTES"] = "1"
	writeMetadataFixture(t, ctx, "NASBOX_MANIFEST_V1\n", "NASBOX_JOURNAL_V1\n", 0)
	// Build the index before making the manifest path unusable for compaction.
	if _, err := ctx.renderManifest(); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(ctx.paths().Manifest); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(ctx.paths().Manifest, 0o700); err != nil {
		t.Fatal(err)
	}
	digest := strings.Repeat("a", 64)
	if err := ctx.appendTransaction("tx1", "dev", []string{
		journalRowPUT("a.txt", digest, "1", "100", "dev", "100", ""),
	}); err != nil {
		t.Fatalf("errore di manutenzione trasformato in fallimento mutazione: %v", err)
	}
	revision, err := ctx.journalRevision()
	if err != nil || revision != 1 {
		t.Fatalf("revisione non committata: %d (%v)", revision, err)
	}
	view, err := ctx.exportManifest(0, true)
	if err != nil || view.Current != 1 || len(view.Rows) != 1 {
		t.Fatalf("commit non leggibile dopo errore compattazione: %+v (%v)", view, err)
	}
}

func TestMetadataIndexMatchesLegacyRenderer(t *testing.T) {
	ctx, _ := newTestContext(t)
	digest := strings.Repeat("a", 64)
	writeMetadataFixture(t, ctx,
		"NASBOX_MANIFEST_V1\n"+
			"vecchio\t"+digest+"\t1\t100\tdev\t100\n"+
			"tombo\t\t0\t0\tdev\t50\n"+
			"RPG/.sync-trash/x\t"+digest+"\t1\t100\tdev\t100\n",
		"NASBOX_JOURNAL_V1\n"+
			"BEGIN\ttx1\t200\tdev\n"+
			"DELETE\tvecchio\t"+digest+"\t1\t100\tdev\t200\n"+
			"PUT\tnuovo%09x\t"+digest+"\t2\t2000000000\tdev\t200\tdev:1\n"+
			"COMMIT\ttx1\n", 1,
	)
	indexed, err := ctx.renderManifest()
	if err != nil {
		t.Fatal(err)
	}
	legacy, err := ctx.renderManifestLegacy()
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(indexed)
	sort.Strings(legacy)
	if strings.Join(indexed, "\n") != strings.Join(legacy, "\n") {
		t.Fatalf("renderer SQLite diverso dal legacy\nindexed=%q\nlegacy=%q", indexed, legacy)
	}
}

func TestFileStatesUsesIndexedJournalAndNanosecondMTime(t *testing.T) {
	ctx, share := newTestContext(t)
	fakeDigest := strings.Repeat("f", 64)
	const mtimeNS = int64(1_123_456_789)
	writeMetadataFixture(t, ctx,
		"NASBOX_MANIFEST_V1\n",
		"NASBOX_JOURNAL_V1\n"+
			"BEGIN\ttx1\t100\tdev\n"+
			"PUT\ta.txt\t"+fakeDigest+"\t3\t"+strconv.FormatInt(mtimeNS, 10)+"\tdev\t100\tdev:1\n"+
			"COMMIT\ttx1\n", 1,
	)
	full := filepath.Join(share, "a.txt")
	if err := os.WriteFile(full, []byte("abc"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(full, time.Unix(1, 123456789), time.Unix(1, 123456789)); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := ctx.fileStates("FILE_STATES_V2", []string{"a.txt"}, &out); err != nil {
		t.Fatal(err)
	}
	fields := strings.Split(out.String(), "\x00")
	want := []string{"FILE_STATES_V2", "1", "a.txt", "FILE", fakeDigest, "3", strconv.FormatInt(mtimeNS, 10), "dev:1"}
	if len(fields) < len(want) {
		t.Fatalf("framing troppo corto: %q", fields)
	}
	for index, expected := range want {
		if fields[index] != expected {
			t.Fatalf("campo %d = %q, atteso %q", index, fields[index], expected)
		}
	}
}

func TestMetadataIndexRebuildsAfterCorruption(t *testing.T) {
	ctx, _ := newTestContext(t)
	digest := strings.Repeat("a", 64)
	writeMetadataFixture(t, ctx,
		"NASBOX_MANIFEST_V1\n"+"a.txt\t"+digest+"\t1\t100\tdev\t100\n",
		"NASBOX_JOURNAL_V1\n", 1,
	)
	dbPath := filepath.Join(ctx.paths().Dir, metadataDBName)
	if err := os.WriteFile(dbPath, []byte("not sqlite"), 0o600); err != nil {
		t.Fatal(err)
	}
	view, err := ctx.exportManifest(0, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(view.Rows) != 1 {
		t.Fatalf("indice corrotto non ricostruito: %+v", view)
	}
	broken, err := filepath.Glob(dbPath + ".broken-*")
	if err != nil || len(broken) != 1 {
		t.Fatalf("copia corrotta non conservata: %v (%v)", broken, err)
	}
	if info, err := os.Stat(dbPath); err != nil || info.Size() == 0 {
		t.Fatalf("nuovo indice non creato: %v", err)
	}
}

func TestMetadataIndexFailureFallsBackToText(t *testing.T) {
	ctx, _ := newTestContext(t)
	digest := strings.Repeat("a", 64)
	writeMetadataFixture(t, ctx,
		"NASBOX_MANIFEST_V1\n"+"a.txt\t"+digest+"\t1\t100\tdev\t100\n",
		"NASBOX_JOURNAL_V1\n", 1,
	)
	if err := os.Mkdir(filepath.Join(ctx.paths().Dir, metadataDBName), 0o700); err != nil {
		t.Fatal(err)
	}
	view, err := ctx.exportManifest(0, true)
	if err != nil {
		t.Fatal(err)
	}
	if !view.FallbackFull || len(view.Rows) != 1 {
		t.Fatalf("fallback legacy non usato: %+v", view)
	}
}
