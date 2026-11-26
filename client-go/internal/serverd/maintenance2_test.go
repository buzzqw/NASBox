package serverd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRunOnceCollectsChunksAndBacksUpState(t *testing.T) {
	ctx, _ := newWebTestContext(t, "")
	store, err := ctx.chunkStore()
	if err != nil {
		t.Fatal(err)
	}
	orphan, _ := store.Put([]byte("chunk senza riferimenti"))
	ageFile(t, store.objectPath(orphan), 72*time.Hour)
	putRow(ctx, t, "a.txt", strings.Repeat("a", 64))

	if err := cmdRunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if store.Has(orphan) {
		t.Fatal("la GC notturna doveva rimuovere il chunk orfano vecchio")
	}
	backups, _ := os.ReadDir(filepath.Join(ctx.StateDir, "backup"))
	if len(backups) != 1 {
		t.Fatalf("attesa una copia dello stato, trovate %d", len(backups))
	}
	if data, err := os.ReadFile(filepath.Join(ctx.StateDir, "backup", backups[0].Name(), "transfer-journal.tsv")); err != nil || !strings.Contains(string(data), "a.txt") {
		t.Fatalf("il backup deve contenere il journal: %v", err)
	}
	// Una seconda esecuzione nello stesso giorno non crea altre copie.
	if err := cmdRunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if again, _ := os.ReadDir(filepath.Join(ctx.StateDir, "backup")); len(again) != 1 {
		t.Fatalf("troppe copie: %d", len(again))
	}
	events, _ := os.ReadFile(ctx.paths().Events)
	if !strings.Contains(string(events), `"chunk_gc"`) {
		t.Fatalf("evento chunk_gc mancante: %s", events)
	}
}

func TestStateBackupKeepsOnlyTheLastCopiesAndCanBeDisabled(t *testing.T) {
	ctx, _ := newWebTestContext(t, "")
	ctx.Config["STATE_BACKUP_KEEP"] = "2"
	root := filepath.Join(ctx.StateDir, "backup")
	for _, name := range []string{"20260101-000000", "20260102-000000", "20260103-000000"} {
		if err := os.MkdirAll(filepath.Join(root, name), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	ctx.backupState()
	entries, _ := os.ReadDir(root)
	if len(entries) != 2 {
		t.Fatalf("restano %d copie, attese 2", len(entries))
	}
	if entries[0].Name() == "20260101-000000" || entries[0].Name() == "20260102-000000" {
		t.Fatalf("vanno tolte le più vecchie: %v", entries[0].Name())
	}

	ctx.Config["STATE_BACKUP_KEEP"] = "0"
	_ = os.Remove(filepath.Join(ctx.StateDir, "state-backup.stamp"))
	before, _ := os.ReadDir(root)
	ctx.backupState()
	if after, _ := os.ReadDir(root); len(after) != len(before) {
		t.Fatal("con STATE_BACKUP_KEEP=0 non si fanno copie")
	}
}

func TestScrubWindowSkipsOutsideHours(t *testing.T) {
	ctx, _ := newTestContext(t)
	ctx.Config["CHUNK_SCRUB_HOUR"] = "3"
	ctx.Config["CHUNK_SCRUB_WINDOW_HOURS"] = "2"
	ctx.Now = func() time.Time { return time.Date(2026, 11, 26, 14, 0, 0, 0, time.Local) }
	if ctx.inScrubWindow() {
		t.Fatal("14:00 non deve essere nella finestra 03:00-04:59")
	}
	ctx.Now = func() time.Time { return time.Date(2026, 11, 26, 3, 30, 0, 0, time.Local) }
	if !ctx.inScrubWindow() {
		t.Fatal("03:30 deve essere nella finestra 03:00-04:59")
	}
	delete(ctx.Config, "CHUNK_SCRUB_WINDOW_HOURS")
	if !ctx.inScrubWindow() {
		t.Fatal("senza finestra configurata la manutenzione deve restare sempre attiva")
	}
}

func TestChunkGCCanBeDisabled(t *testing.T) {
	ctx, _ := newWebTestContext(t, "")
	ctx.Config["CHUNK_GC"] = "false"
	store, _ := ctx.chunkStore()
	orphan, _ := store.Put([]byte("resta"))
	ageFile(t, store.objectPath(orphan), 72*time.Hour)
	ctx.maintainChunks()
	if !store.Has(orphan) {
		t.Fatal("CHUNK_GC=false deve lasciare i chunk")
	}
}

func TestAuditMaintenanceReportsByDefaultAndAdoptsOnRequest(t *testing.T) {
	ctx, share := newWebTestContext(t, "")
	writeAged(t, share, "orfano.txt", "x", time.Hour)
	putRow(ctx, t, "fantasma.txt", strings.Repeat("f", 64))

	ctx.maintainAudit() // report: non scrive il journal
	if _, ok, _ := ctx.lookupManifest("orfano.txt"); ok {
		t.Fatal("in modalità report il file orfano non va adottato")
	}
	events, _ := os.ReadFile(ctx.paths().Events)
	if !strings.Contains(string(events), `"audit_drift"`) || !strings.Contains(string(events), `"orphans":1`) {
		t.Fatalf("evento audit_drift mancante: %s", events)
	}

	ctx.Config["AUDIT_AUTO"] = "adopt"
	ctx.maintainAudit()
	if _, ok, _ := ctx.lookupManifest("orfano.txt"); !ok {
		t.Fatal("con AUDIT_AUTO=adopt il file orfano va adottato")
	}
	// I fantasmi non si dimenticano mai da soli.
	if phantoms, _ := ctx.phantomPaths(); len(phantoms) != 1 {
		t.Fatalf("il fantasma deve restare: %v", phantoms)
	}

	ctx.Config["AUDIT_AUTO"] = "off"
	before, _ := os.ReadFile(ctx.paths().Events)
	ctx.maintainAudit()
	if after, _ := os.ReadFile(ctx.paths().Events); string(after) != string(before) {
		t.Fatal("con AUDIT_AUTO=off non si registra nulla")
	}
}
