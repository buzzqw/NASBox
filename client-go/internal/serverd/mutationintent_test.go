package serverd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type mutationTestCrash struct{}

func crashOnMutationStage(t *testing.T, stage string) {
	t.Helper()
	previous := txIntentFailpoint
	txIntentFailpoint = func(got, _ string) {
		if got == stage {
			panic(mutationTestCrash{})
		}
	}
	t.Cleanup(func() { txIntentFailpoint = previous })
}

func recoverMutationTestIntent(t *testing.T, ctx *Context) {
	t.Helper()
	if err := ctx.withJournalLock(func() error {
		return ctx.recoverMutationIntentsLocked("web")
	}); err != nil {
		t.Fatal(err)
	}
}

func TestMutationDeleteRecoversAfterMove(t *testing.T) {
	ctx, share := newWebTestContext(t, "segreta")
	writeWebTestFile(t, share, "documenti/nota.txt", "da recuperare")
	crashOnMutationStage(t, "mutation_moved")

	func() {
		defer func() {
			if value := recover(); value != nil {
				if _, ok := value.(mutationTestCrash); !ok {
					t.Fatalf("panic inatteso: %v", value)
				}
			}
		}()
		_ = ctx.withWebLease(5*time.Second, func() error {
			_, err := ctx.deleteEntryLocked("web", restoreTestTS, "documenti/nota.txt")
			return err
		})
		t.Fatal("il failpoint non ha interrotto il delete")
	}()

	trash, _ := filepath.Glob(filepath.Join(share, ".sync-trash", "documenti", "nota.txt-*"))
	if len(trash) != 1 {
		t.Fatalf("copia nel cestino mancante: %v", trash)
	}
	if _, err := os.Stat(filepath.Join(share, "documenti", "nota.txt")); !os.IsNotExist(err) {
		t.Fatalf("il file non è stato spostato: %v", err)
	}
	recoverMutationTestIntent(t, ctx)

	journal := readString(ctx.paths().Journal)
	if !strings.Contains(journal, "DELETE\tdocumenti/nota.txt") {
		t.Fatalf("DELETE non recuperata: %q", journal)
	}
	if data, err := os.ReadFile(trash[0]); err != nil || string(data) != "da recuperare" {
		t.Fatalf("contenuto cestino: %q %v", data, err)
	}
	store, err := openTxIntentStore(ctx.paths())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if records, err := store.listMutations(); err != nil || len(records) != 0 {
		t.Fatalf("intent non ripulito: %v %v", records, err)
	}
}

func TestMutationRenameRecoversAfterMove(t *testing.T) {
	ctx, share := newWebTestContext(t, "segreta")
	writeWebTestFile(t, share, "uno.txt", "contenuto")
	crashOnMutationStage(t, "mutation_moved")

	func() {
		defer func() {
			if value := recover(); value != nil {
				if _, ok := value.(mutationTestCrash); !ok {
					t.Fatalf("panic inatteso: %v", value)
				}
			}
		}()
		_ = ctx.withWebLease(5*time.Second, func() error {
			_, err := ctx.renameEntryLocked("web", restoreTestTS, "uno.txt", "due.txt")
			return err
		})
		t.Fatal("il failpoint non ha interrotto il rename")
	}()

	recoverMutationTestIntent(t, ctx)

	if data, err := os.ReadFile(filepath.Join(share, "due.txt")); err != nil || string(data) != "contenuto" {
		t.Fatalf("contenuto rinominato: %q %v", data, err)
	}
	row, ok, err := ctx.lookupManifest("due.txt")
	if err != nil || !ok || !strings.HasPrefix(row, "due.txt\t") {
		t.Fatalf("manifest dopo recovery: %q ok=%v err=%v", row, ok, err)
	}
	journal := readString(ctx.paths().Journal)
	if !strings.Contains(journal, "DELETE\tuno.txt") || !strings.Contains(journal, "PUT\tdue.txt") {
		t.Fatalf("journal rename incompleto: %q", journal)
	}
}

func TestMutationRestoreRecoversAfterMove(t *testing.T) {
	ctx, share := newWebTestContext(t, "segreta")
	writeWebTestFile(t, share, "live.txt", "vecchio")
	version := "2026-11-26--12-00-00-000000Z"
	source := filepath.Join(share, ".sync-trash", "live.txt-"+version)
	writeWebTestFile(t, share, ".sync-trash/live.txt-"+version, "nuovo")
	crashOnMutationStage(t, "mutation_moved")

	func() {
		defer func() {
			if value := recover(); value != nil {
				if _, ok := value.(mutationTestCrash); !ok {
					t.Fatalf("panic inatteso: %v", value)
				}
			}
		}()
		_ = ctx.withWebLease(5*time.Second, func() error {
			_, _, _, err := ctx.restoreOne("web", restorePlan{
				request: restoreRequest{path: "live.txt", version: version},
				source:  source,
			}, restoreFingerprint{}, false, 0, time.Now().UTC())
			return err
		})
		t.Fatal("il failpoint non ha interrotto il restore")
	}()

	recoverMutationTestIntent(t, ctx)
	if data, err := os.ReadFile(filepath.Join(share, "live.txt")); err != nil || string(data) != "nuovo" {
		t.Fatalf("contenuto ripristinato: %q %v", data, err)
	}
	journal := readString(ctx.paths().Journal)
	if !strings.Contains(journal, "PUT\tlive.txt") {
		t.Fatalf("PUT restore non recuperato: %q", journal)
	}
}

func TestMutationRecoveryLeavesUnknownStateAlone(t *testing.T) {
	ctx, share := newWebTestContext(t, "segreta")
	writeWebTestFile(t, share, "nota.txt", "x")
	// A PREPARED intent whose source still exists is a no-op: the crash happened
	// before the move. Recovery must remove the marker without changing bytes.
	prior, err := os.Lstat(filepath.Join(share, "nota.txt"))
	if err != nil {
		t.Fatal(err)
	}
	state, err := publishPriorFromInfo(prior)
	if err != nil {
		t.Fatal(err)
	}
	intent, err := ctx.prepareMutationIntent(mutationIntentPayload{
		Version: mutationIntentVersion, TxID: "delete-before-move", Device: "web",
		RunTS: restoreTestTS, Kind: mutationKindDelete,
		Ops: []mutationIntentOp{{
			Path: "nota.txt", Backup: ".sync-trash/nota.txt-" + restoreTestTS,
			Prior: state, Rows: deleteRows([]string{"nota.txt"}, "web", journalNow()),
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	intent.close()
	recoverMutationTestIntent(t, ctx)
	if data, err := os.ReadFile(filepath.Join(share, "nota.txt")); err != nil || string(data) != "x" {
		t.Fatalf("file modificato durante recovery no-op: %q %v", data, err)
	}
}
