package serverd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type txIntentTestCrash struct{}

func installTxIntentTestFailpoint(t *testing.T, stage string) {
	t.Helper()
	previous := txIntentFailpoint
	txIntentFailpoint = func(got, _ string) {
		if got == stage {
			panic(txIntentTestCrash{})
		}
	}
	t.Cleanup(func() { txIntentFailpoint = previous })
}

func runPublishTestCommandCatchingPanic(t *testing.T, ctx *Context, batch bool, frame []byte) (err error, panicValue any) {
	t.Helper()
	defer func() { panicValue = recover() }()
	err = runPublishTestCommand(t, ctx, batch, frame)
	return err, nil
}

func requireTxIntentStatus(t *testing.T, ctx *Context, txid, want string) txIntentRecord {
	t.Helper()
	store, err := openTxIntentStore(ctx.paths())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	record, ok, err := store.get(txid)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatalf("intent %s assente, voglio %s", txid, want)
	}
	if record.Status != want {
		t.Fatalf("stato intent %s = %s, voglio %s", txid, record.Status, want)
	}
	return record
}

func requireNoTxIntents(t *testing.T, ctx *Context) {
	t.Helper()
	store, err := openTxIntentStore(ctx.paths())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	records, err := store.list()
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 0 {
		t.Fatalf("restano %d intent: %+v", len(records), records)
	}
}

func recoverTxIntentsForTest(t *testing.T, ctx *Context) error {
	t.Helper()
	return ctx.withJournalLock(func() error {
		return ctx.recoverTxIntentsLocked(publishTestDevice, nil)
	})
}

func mustMarshalPublishIntentPayload(payload publishIntentPayload) string {
	raw, err := marshalPublishIntentPayload(payload)
	if err != nil {
		panic(err)
	}
	return raw
}

func requirePublishTestCommitCount(t *testing.T, ctx *Context, txid string, want int) {
	t.Helper()
	commits, _, err := readJournalCommits(ctx.paths().Journal, 0)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, commit := range commits {
		if commit.TxID == txid {
			count++
		}
	}
	if count != want {
		t.Fatalf("commit %s presenti %d volte, voglio %d", txid, count, want)
	}
}

func TestTxIntentStoreIsWALFullAndPayloadIsValidated(t *testing.T) {
	ctx, share := newPublishTestContext(t)
	record := makePublishTestRecord(t, ctx, "file.txt", []byte("contenuto"))
	payload, err := ctx.buildPublishIntentPayloadLocked(
		publishTestDevice, "tx-publish-test", publishTestRunTS, "replace",
		[]publishRecord{{path: record.path, digest: record.digest, size: record.size, mtime: "1700000000123456789", chunks: record.chunks}},
	)
	if err != nil {
		t.Fatal(err)
	}
	store, err := openTxIntentStore(ctx.paths())
	if err != nil {
		t.Fatal(err)
	}
	var mode string
	if err := store.db.QueryRow(`PRAGMA journal_mode`).Scan(&mode); err != nil {
		t.Fatal(err)
	}
	if !strings.EqualFold(mode, "wal") {
		t.Fatalf("journal_mode=%q, voglio wal", mode)
	}
	var synchronous int64
	if err := store.db.QueryRow(`PRAGMA synchronous`).Scan(&synchronous); err != nil {
		t.Fatal(err)
	}
	if synchronous != 2 {
		t.Fatalf("synchronous=%d, voglio FULL(2)", synchronous)
	}
	raw, err := store.prepare(payload.TxID, payload)
	if err != nil {
		t.Fatal(err)
	}
	_ = raw
	if _, err := store.db.Exec(`UPDATE intent SET status='INVALID' WHERE txid=?`, payload.TxID); err == nil {
		t.Fatal("il vincolo CHECK ha accettato uno stato sconosciuto")
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	store, err = openTxIntentStore(ctx.paths())
	if err != nil {
		t.Fatal(err)
	}
	recordIntent, ok, err := store.get(payload.TxID)
	if err != nil || !ok {
		t.Fatalf("intent non persistito: ok=%v err=%v", ok, err)
	}
	if recordIntent.Status != txIntentPrepared {
		t.Fatalf("stato persistito=%s", recordIntent.Status)
	}
	if _, err := store.db.Exec(`UPDATE intent SET payload=? WHERE txid=?`, `{"version":1}`, payload.TxID); err != nil {
		t.Fatal(err)
	}
	store.Close()

	if err := recoverTxIntentsForTest(t, ctx); err == nil {
		t.Fatal("un payload JSON incompleto dovrebbe essere rifiutato")
	}
	requirePublishTestAbsent(t, filepath.Join(share, record.path))
}

func TestTxIntentRecoveryPreparedRollsBackPartialBatchIdempotently(t *testing.T) {
	ctx, share := newPublishTestContext(t)
	oldPath := filepath.Join(share, "vecchio.txt")
	oldData := []byte("contenuto precedente")
	if err := os.WriteFile(oldPath, oldData, 0o600); err != nil {
		t.Fatal(err)
	}
	replaced := makePublishTestRecord(t, ctx, "vecchio.txt", []byte("nuovo contenuto"))
	created := makePublishTestRecord(t, ctx, "nuovo.txt", []byte("appena creato"))
	records := []publishTestRecord{replaced, created}
	installTxIntentTestFailpoint(t, "after_assemble")

	err, panicValue := runPublishTestCommandCatchingPanic(t, ctx, true, publishTestBatchFrame(records))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := panicValue.(txIntentTestCrash); !ok {
		t.Fatalf("crash atteso dopo assemble, ottenuto %#v", panicValue)
	}
	requireTxIntentStatus(t, ctx, "tx-publish-test", txIntentPrepared)
	requirePublishTestCommitCount(t, ctx, "tx-publish-test", 0)
	requirePublishTestContents(t, filepath.Join(share, publishBackupRelative(replaced.path, publishTestRunTS)), oldData)
	requirePublishTestContents(t, filepath.Join(share, replaced.path), []byte("nuovo contenuto"))
	requirePublishTestAbsent(t, filepath.Join(share, created.path))

	txIntentFailpoint = nil
	if err := recoverTxIntentsForTest(t, ctx); err != nil {
		t.Fatal(err)
	}
	if err := recoverTxIntentsForTest(t, ctx); err != nil {
		t.Fatalf("secondo recovery idempotente: %v", err)
	}
	requirePublishTestContents(t, oldPath, oldData)
	requirePublishTestAbsent(t, filepath.Join(share, created.path))
	requirePublishTestAbsent(t, filepath.Join(share, publishBackupRelative(replaced.path, publishTestRunTS)))
	requireNoTxIntents(t, ctx)
	if got := readString(ctx.paths().Revision); got != "0" {
		t.Fatalf("revisione dopo rollback=%s, voglio 0", got)
	}

	// The same txid can be retried after the PREPARED rollback completed.
	if err := runPublishTestCommand(t, ctx, true, publishTestBatchFrame(records)); err != nil {
		t.Fatalf("retry dopo rollback: %v", err)
	}
	requirePublishTestCommitCount(t, ctx, "tx-publish-test", 1)
	requireNoTxIntents(t, ctx)
}

func TestTxIntentRecoveryReadyRollsForwardIdempotently(t *testing.T) {
	ctx, share := newPublishTestContext(t)
	target := filepath.Join(share, "file.txt")
	oldData := []byte("precedente")
	if err := os.WriteFile(target, oldData, 0o600); err != nil {
		t.Fatal(err)
	}
	record := makePublishTestRecord(t, ctx, "file.txt", []byte("pubblicato"))
	installTxIntentTestFailpoint(t, "ready")

	err, panicValue := runPublishTestCommandCatchingPanic(t, ctx, false, publishTestSingleFrame(record))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := panicValue.(txIntentTestCrash); !ok {
		t.Fatalf("crash atteso dopo READY, ottenuto %#v", panicValue)
	}
	requireTxIntentStatus(t, ctx, "tx-publish-test", txIntentReady)
	requirePublishTestCommitCount(t, ctx, "tx-publish-test", 0)
	requirePublishTestContents(t, target, []byte("pubblicato"))
	requirePublishTestContents(t, filepath.Join(share, publishBackupRelative(record.path, publishTestRunTS)), oldData)

	txIntentFailpoint = nil
	if err := recoverTxIntentsForTest(t, ctx); err != nil {
		t.Fatal(err)
	}
	if err := recoverTxIntentsForTest(t, ctx); err != nil {
		t.Fatalf("secondo recovery idempotente: %v", err)
	}
	requirePublishTestContents(t, target, []byte("pubblicato"))
	requirePublishTestContents(t, filepath.Join(share, publishBackupRelative(record.path, publishTestRunTS)), oldData)
	requirePublishTestCommitCount(t, ctx, "tx-publish-test", 1)
	if got := readString(ctx.paths().Revision); got != "1" {
		t.Fatalf("revisione=%s, voglio 1", got)
	}
	row, found, err := ctx.lookupManifest(record.path)
	if err != nil || !found || !strings.Contains(row, "\t"+record.digest+"\t") {
		t.Fatalf("metadata non sincronizzata: found=%v row=%q err=%v", found, row, err)
	}
	requireNoTxIntents(t, ctx)

	if err := runPublishTestCommand(t, ctx, false, publishTestSingleFrame(record)); err != nil {
		t.Fatalf("retry idempotente: %v", err)
	}
	requirePublishTestCommitCount(t, ctx, "tx-publish-test", 1)
}

func TestTxIntentRecoveryCommitWinsAndJournalIsNotTruncated(t *testing.T) {
	ctx, share := newPublishTestContext(t)
	target := filepath.Join(share, "file.txt")
	oldData := []byte("precedente")
	if err := os.WriteFile(target, oldData, 0o600); err != nil {
		t.Fatal(err)
	}
	record := makePublishTestRecord(t, ctx, "file.txt", []byte("pubblicato"))
	installTxIntentTestFailpoint(t, "journal_committed")

	err, panicValue := runPublishTestCommandCatchingPanic(t, ctx, false, publishTestSingleFrame(record))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := panicValue.(txIntentTestCrash); !ok {
		t.Fatalf("crash atteso dopo fsync journal, ottenuto %#v", panicValue)
	}
	requireTxIntentStatus(t, ctx, "tx-publish-test", txIntentReady)
	requirePublishTestCommitCount(t, ctx, "tx-publish-test", 1)
	if got := readString(ctx.paths().Revision); got != "0" {
		t.Fatalf("revisione prima del recovery=%s, voglio 0", got)
	}
	requirePublishTestContents(t, target, []byte("pubblicato"))

	// Even an inconsistent older PREPARED marker cannot override a commit that
	// is already durable in the journal.
	store, err := openTxIntentStore(ctx.paths())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE intent SET status='PREPARED' WHERE txid=?`, "tx-publish-test"); err != nil {
		t.Fatal(err)
	}
	store.Close()

	txIntentFailpoint = nil
	if err := recoverTxIntentsForTest(t, ctx); err != nil {
		t.Fatal(err)
	}
	if err := recoverTxIntentsForTest(t, ctx); err != nil {
		t.Fatalf("secondo recovery idempotente: %v", err)
	}
	requirePublishTestContents(t, target, []byte("pubblicato"))
	requirePublishTestContents(t, filepath.Join(share, publishBackupRelative(record.path, publishTestRunTS)), oldData)
	requirePublishTestCommitCount(t, ctx, "tx-publish-test", 1)
	if got := readString(ctx.paths().Revision); got != "1" {
		t.Fatalf("revisione dopo recovery=%s, voglio 1", got)
	}
	requireNoTxIntents(t, ctx)
}

func TestTxIntentRecoveryReadyNeverRollsBackAmbiguousTarget(t *testing.T) {
	ctx, share := newPublishTestContext(t)
	target := filepath.Join(share, "file.txt")
	if err := os.WriteFile(target, []byte("precedente"), 0o600); err != nil {
		t.Fatal(err)
	}
	record := makePublishTestRecord(t, ctx, "file.txt", []byte("pubblicato"))
	installTxIntentTestFailpoint(t, "ready")
	_, panicValue := runPublishTestCommandCatchingPanic(t, ctx, false, publishTestSingleFrame(record))
	if _, ok := panicValue.(txIntentTestCrash); !ok {
		t.Fatalf("crash atteso, ottenuto %#v", panicValue)
	}
	if err := os.WriteFile(target, []byte("modifica esterna"), 0o600); err != nil {
		t.Fatal(err)
	}
	txIntentFailpoint = nil
	if err := recoverTxIntentsForTest(t, ctx); err == nil {
		t.Fatal("un target READY diverso dovrebbe bloccare il recovery")
	}
	requirePublishTestContents(t, target, []byte("modifica esterna"))
	requirePublishTestContents(t, filepath.Join(share, publishBackupRelative(record.path, publishTestRunTS)), []byte("precedente"))
	requireTxIntentStatus(t, ctx, "tx-publish-test", txIntentReady)
	requirePublishTestCommitCount(t, ctx, "tx-publish-test", 0)
}

func TestTxIntentRecoveryPreparedAmbiguousStateFailsClosed(t *testing.T) {
	ctx, share := newPublishTestContext(t)
	target := filepath.Join(share, "file.txt")
	if err := os.WriteFile(target, []byte("precedente"), 0o600); err != nil {
		t.Fatal(err)
	}
	record := makePublishTestRecord(t, ctx, "file.txt", []byte("pubblicato"))
	installTxIntentTestFailpoint(t, "after_backup")
	_, panicValue := runPublishTestCommandCatchingPanic(t, ctx, false, publishTestSingleFrame(record))
	if _, ok := panicValue.(txIntentTestCrash); !ok {
		t.Fatalf("crash atteso, ottenuto %#v", panicValue)
	}
	backup := filepath.Join(share, publishBackupRelative(record.path, publishTestRunTS))
	if err := os.Remove(backup); err != nil {
		t.Fatal(err)
	}
	txIntentFailpoint = nil
	if err := recoverTxIntentsForTest(t, ctx); err == nil {
		t.Fatal("target e backup entrambi assenti dovrebbero essere ambigui")
	}
	requireTxIntentStatus(t, ctx, "tx-publish-test", txIntentPrepared)
	requirePublishTestCommitCount(t, ctx, "tx-publish-test", 0)
}

func TestTxIntentRecoveryRejectsDifferentPayloadForSameTxID(t *testing.T) {
	ctx, share := newPublishTestContext(t)
	original := makePublishTestRecord(t, ctx, "original.txt", []byte("originale"))
	installTxIntentTestFailpoint(t, "prepared")
	_, panicValue := runPublishTestCommandCatchingPanic(t, ctx, false, publishTestSingleFrame(original))
	if _, ok := panicValue.(txIntentTestCrash); !ok {
		t.Fatalf("crash atteso, ottenuto %#v", panicValue)
	}
	txIntentFailpoint = nil
	different := makePublishTestRecord(t, ctx, "diverso.txt", []byte("diverso"))
	if err := runPublishTestCommand(t, ctx, false, publishTestSingleFrame(different)); err == nil {
		t.Fatal("un txid PREPARED non può essere riusato per un payload diverso")
	}
	requireTxIntentStatus(t, ctx, "tx-publish-test", txIntentPrepared)
	requirePublishTestAbsent(t, filepath.Join(share, original.path))
	requirePublishTestAbsent(t, filepath.Join(share, different.path))
	if err := recoverTxIntentsForTest(t, ctx); err != nil {
		t.Fatal(err)
	}
	requireNoTxIntents(t, ctx)
}

func TestTxIntentRecoveryCommittedWithoutCommitFailsClosed(t *testing.T) {
	ctx, share := newPublishTestContext(t)
	target := filepath.Join(share, "file.txt")
	if err := os.WriteFile(target, []byte("precedente"), 0o600); err != nil {
		t.Fatal(err)
	}
	record := makePublishTestRecord(t, ctx, "file.txt", []byte("pubblicato"))
	installTxIntentTestFailpoint(t, "ready")
	_, panicValue := runPublishTestCommandCatchingPanic(t, ctx, false, publishTestSingleFrame(record))
	if _, ok := panicValue.(txIntentTestCrash); !ok {
		t.Fatalf("crash atteso, ottenuto %#v", panicValue)
	}
	store, err := openTxIntentStore(ctx.paths())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE intent SET status='COMMITTED' WHERE txid=?`, "tx-publish-test"); err != nil {
		t.Fatal(err)
	}
	store.Close()
	txIntentFailpoint = nil
	if err := recoverTxIntentsForTest(t, ctx); err == nil {
		t.Fatal("COMMITTED senza prova nel journal è ambiguo")
	}
	requirePublishTestContents(t, target, []byte("pubblicato"))
	requireTxIntentStatus(t, ctx, "tx-publish-test", txIntentCommitted)
	requirePublishTestCommitCount(t, ctx, "tx-publish-test", 0)
}

func TestTxIntentRecoveryTruncatesOnlyIncompleteJournalTail(t *testing.T) {
	ctx, share := newPublishTestContext(t)
	record := makePublishTestRecord(t, ctx, "file.txt", []byte("pubblicato"))
	installTxIntentTestFailpoint(t, "ready")
	_, panicValue := runPublishTestCommandCatchingPanic(t, ctx, false, publishTestSingleFrame(record))
	if _, ok := panicValue.(txIntentTestCrash); !ok {
		t.Fatalf("crash atteso, ottenuto %#v", panicValue)
	}
	txIntentFailpoint = nil
	journal, err := os.OpenFile(ctx.paths().Journal, os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := journal.WriteString("BEGIN\ttx-publish-test\t1700000000\tpublish-test\nPUT\tfile.txt\tparziale"); err != nil {
		t.Fatal(err)
	}
	if err := journal.Close(); err != nil {
		t.Fatal(err)
	}
	if err := recoverTxIntentsForTest(t, ctx); err != nil {
		t.Fatal(err)
	}
	requirePublishTestContents(t, filepath.Join(share, record.path), []byte("pubblicato"))
	requirePublishTestCommitCount(t, ctx, "tx-publish-test", 1)
	if strings.Contains(readString(ctx.paths().Journal), "parziale") {
		t.Fatal("la coda journal incompleta non è stata rimossa")
	}
	requireNoTxIntents(t, ctx)
}

func TestTransferWaitRecoversReadyBeforeGrantingLease(t *testing.T) {
	ctx, share := newTestContext(t)
	if err := ctx.ensureJournalFiles(); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(share, "file.txt")
	if err := os.WriteFile(target, []byte("precedente"), 0o600); err != nil {
		t.Fatal(err)
	}
	record := makePublishTestRecord(t, ctx, "file.txt", []byte("pubblicato"))
	payload, err := ctx.buildPublishIntentPayloadLocked(
		publishTestDevice, "tx-publish-test", publishTestRunTS, "replace",
		[]publishRecord{{path: record.path, digest: record.digest, size: record.size, mtime: "1700000000123456789", chunks: record.chunks}},
	)
	if err != nil {
		t.Fatal(err)
	}
	installTxIntentTestFailpoint(t, "ready")
	var panicValue any
	func() {
		defer func() { panicValue = recover() }()
		err = ctx.withJournalLock(func() error {
			return ctx.executePreparedTxIntentLocked(payload)
		})
	}()
	if _, ok := panicValue.(txIntentTestCrash); !ok {
		t.Fatalf("crash atteso dopo READY, ottenuto %#v", panicValue)
	}
	requireTxIntentStatus(t, ctx, "tx-publish-test", txIntentReady)
	txIntentFailpoint = nil

	input, err := os.CreateTemp(t.TempDir(), "transfer-wait-stdin-*")
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	if _, err := input.WriteString("TRANSFER_WAIT_V1\x00" + publishTestDevice + "\x000\x00request01\x00test-host\x00"); err != nil {
		t.Fatal(err)
	}
	if _, err := input.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	output, err := os.CreateTemp(t.TempDir(), "transfer-wait-stdout-*")
	if err != nil {
		t.Fatal(err)
	}
	defer output.Close()
	oldStdin, oldStdout := os.Stdin, os.Stdout
	os.Stdin, os.Stdout = input, output
	err = cmdTransferWait(ctx)
	os.Stdin, os.Stdout = oldStdin, oldStdout
	if err != nil {
		t.Fatal(err)
	}
	if _, err := output.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	var stdout strings.Builder
	if _, err := output.WriteTo(&stdout); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), "NASBOX_LOCKED") {
		t.Fatalf("lease concessa senza recovery, stdout=%q", stdout.String())
	}
	requirePublishTestContents(t, target, []byte("pubblicato"))
	requirePublishTestCommitCount(t, ctx, "tx-publish-test", 1)
	if got := readString(ctx.paths().Revision); got != "1" {
		t.Fatalf("revisione=%s, voglio 1", got)
	}
	requireNoTxIntents(t, ctx)
}

func TestPublishIntentPayloadRejectsUnsafeOrIncoherentData(t *testing.T) {
	ctx, _ := newPublishTestContext(t)
	record := makePublishTestRecord(t, ctx, "file.txt", []byte("contenuto"))
	valid, err := ctx.buildPublishIntentPayloadLocked(
		publishTestDevice, "tx-publish-test", publishTestRunTS, "replace",
		[]publishRecord{{path: record.path, digest: record.digest, size: record.size, mtime: "1700000000123456789", chunks: record.chunks}},
	)
	if err != nil {
		t.Fatal(err)
	}
	tests := map[string]func(*publishIntentPayload){
		"traversal": func(p *publishIntentPayload) { p.Ops[0].Path = "../fuori.txt" },
		"backup":    func(p *publishIntentPayload) { p.Ops[0].BackupRelative = "/assoluto" },
		"row": func(p *publishIntentPayload) {
			p.Rows[0] = strings.Replace(p.Rows[0], record.digest, strings.Repeat("f", 64), 1)
		},
		"revision": func(p *publishIntentPayload) { p.TargetRevision = 0 },
		"chunk":    func(p *publishIntentPayload) { p.Ops[0].Chunks = []string{"non-valido"} },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			candidate := valid
			candidate.Ops = append([]publishIntentOp(nil), valid.Ops...)
			candidate.Rows = append([]string(nil), valid.Rows...)
			mutate(&candidate)
			if _, err := marshalPublishIntentPayload(candidate); err == nil {
				t.Fatal("payload non valido accettato")
			}
		})
	}
	unknown := strings.TrimSuffix(mustMarshalPublishIntentPayload(valid), "}") + `,"unknown":true}`
	if _, err := decodePublishIntentPayload(unknown); err == nil {
		t.Fatal("campo JSON sconosciuto accettato")
	}
	if _, err := decodePublishIntentPayload(mustMarshalPublishIntentPayload(valid) + `{}`); err == nil {
		t.Fatal("JSON aggiuntivo accettato")
	}
}
