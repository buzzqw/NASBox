package state

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"nasbox-go/internal/causal"
	"nasbox-go/internal/config"
)

func TestEntryStatsSeparatesTombstonesAndDropsLegacyDirectories(t *testing.T) {
	base := t.TempDir()
	t.Setenv("XDG_STATE_HOME", base)
	cfg := config.New("")
	store, err := Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	repo := "repo"
	if err := store.RecordEntry(repo, "a.txt", EntryState{Digest: "abc", Size: 3, MTimeNS: 1}); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordEntry(repo, "deleted.txt", EntryState{}); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordEntry(repo, "legacy-dir/", EntryState{}); err != nil {
		t.Fatal(err)
	}
	if active, tombstones := store.EntryStats(repo); active != 1 || tombstones != 2 {
		t.Fatalf("stats active=%d tombstones=%d", active, tombstones)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	// Reopening runs the legacy directory cleanup.
	store, err = Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if got := store.EntryCount(repo); got != 2 {
		t.Fatalf("legacy directory row not removed: %d", got)
	}
	if active, tombstones := store.EntryStats(repo); active != 1 || tombstones != 1 {
		t.Fatalf("stats after cleanup active=%d tombstones=%d", active, tombstones)
	}
}

func TestObservationClockIsStableThenIncrements(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	store, err := Open(config.New(""))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	first, err := store.Observe("repo1", "a.txt", "pc1", Observation{
		Digest: "one", Size: 3, MTimeNS: 10, Present: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	same, err := store.Observe("repo1", "a.txt", "pc1", Observation{
		Digest: "one", Size: 3, MTimeNS: 10, Present: true,
	})
	if err != nil || same != first {
		t.Fatalf("identical observation changed clock: %q -> %q (%v)", first, same, err)
	}
	second, err := store.Observe("repo1", "a.txt", "pc1", Observation{
		Digest: "two", Size: 3, MTimeNS: 20, Present: true,
	})
	if err != nil || second == first {
		t.Fatalf("changed observation did not increment: %q -> %q (%v)", first, second, err)
	}
}

func TestDeviceIDIsPersistentAndUnique(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	cfg := config.New("")
	store, err := Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	first := store.DeviceID()
	if first == "" || first == "go-client" {
		t.Fatalf("invalid device ID %q", first)
	}
	if second := store.DeviceID(); second != first {
		t.Fatalf("device ID changed: %q -> %q", first, second)
	}
}

func TestBackfillRecordsWholeTree(t *testing.T) {
	base := t.TempDir()
	t.Setenv("XDG_STATE_HOME", base)
	store, err := Open(config.New(""))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	root := t.TempDir()
	for _, name := range []string{"a.txt", "sub/b.txt", "sub/c.bin"} {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	count, err := store.BackfillIdentities("repo1", root, nil, nil)
	if err != nil || count != 3 {
		t.Fatalf("backfill failed: count=%d err=%v", count, err)
	}
	identities, err := store.IdentitySnapshot("repo1")
	if err != nil || len(identities) != 3 {
		t.Fatalf("snapshot failed: %+v %v", identities, err)
	}
}

func TestIdentityAndUndoRoundTrip(t *testing.T) {
	base := t.TempDir()
	t.Setenv("XDG_STATE_HOME", base)
	cfg := config.New("")
	store, err := Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	root := t.TempDir()
	file := filepath.Join(root, "a.txt")
	if err := os.WriteFile(file, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordIdentities("repo1", root, map[string]string{"a.txt": "deadbeef"}); err != nil {
		t.Fatal(err)
	}
	identities, err := store.IdentitySnapshot("repo1")
	if err != nil {
		t.Fatal(err)
	}
	if identities["a.txt"].Digest != "deadbeef" || identities["a.txt"].Ino == 0 {
		t.Fatalf("identity not stored: %+v", identities)
	}

	if err := store.RecordUndo("repo1", []string{"gone.txt"}, "2026-11-24--07-00-00-000001Z"); err != nil {
		t.Fatal(err)
	}
	if store.UndoCount("repo1", 24) != 1 {
		t.Fatal("undo count should be 1")
	}
	entries, err := store.RecentUndo("repo1", 24, 10)
	if err != nil || len(entries) != 1 || entries[0].Path != "gone.txt" {
		t.Fatalf("recent undo wrong: %+v %v", entries, err)
	}
	if err := store.MarkUndoApplied("repo1", "2026-11-24--07-00-00-000001Z", []string{"gone.txt"}); err != nil {
		t.Fatal(err)
	}
	if store.UndoCount("repo1", 24) != 0 {
		t.Fatal("undo should be consumed after apply")
	}
}

// The snapshot fast path must return exactly what Observe returns, and only
// skip Observe when Observe would not change anything.
func TestUnchangedCausalMatchesObserve(t *testing.T) {
	store := openTestStore(t)
	repo, device := "repo1", "dev-a"
	present := Observation{Digest: "d1", Size: 3, MTimeNS: 10, Present: true}
	absent := Observation{Present: false}
	first, err := store.Observe(repo, "a.txt", device, present)
	if err != nil {
		t.Fatal(err)
	}
	gone, err := store.Observe(repo, "b.txt", device, absent)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := store.ObservationSnapshot(repo)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		path        string
		observation Observation
		skip        bool
		want        string
	}{
		{"a.txt", present, true, first},
		{"a.txt", Observation{Digest: "d2", Size: 3, MTimeNS: 10, Present: true}, false, ""},
		{"a.txt", absent, false, ""},
		{"b.txt", absent, true, gone},
		{"b.txt", present, false, ""},
		{"nuovo.txt", present, false, ""},
	} {
		record, known := snapshot[tc.path]
		got, skip := UnchangedCausal(record, known, tc.observation)
		if skip != tc.skip || got != tc.want {
			t.Fatalf("%s %+v: skip=%v got=%q, want skip=%v %q", tc.path, tc.observation, skip, got, tc.skip, tc.want)
		}
		if skip {
			again, err := store.Observe(repo, tc.path, device, tc.observation)
			if err != nil || again != got {
				t.Fatalf("Observe disagrees with the fast path: %q vs %q (%v)", again, got, err)
			}
		}
	}
}

func TestOpenMigratesLegacySchemaIdempotently(t *testing.T) {
	dbPath := testStatePath(t)
	raw := openRawStateDB(t, dbPath)
	t.Cleanup(func() { _ = raw.Close() })
	for _, statement := range []string{
		`CREATE TABLE meta (key TEXT PRIMARY KEY, value TEXT NOT NULL)`,
		`CREATE TABLE entry (
			repository TEXT NOT NULL, path TEXT NOT NULL, digest TEXT,
			size INTEGER, mtime_ns INTEGER, synced_at REAL NOT NULL,
			PRIMARY KEY (repository, path)
		)`,
		`CREATE TABLE pending (
			repository TEXT NOT NULL, path TEXT NOT NULL, queued_at REAL NOT NULL,
			PRIMARY KEY (repository, path)
		)`,
		`CREATE TABLE local_observation (
			repository TEXT NOT NULL, path TEXT NOT NULL, digest TEXT,
			size INTEGER, mtime_ns INTEGER, observed_at REAL NOT NULL,
			PRIMARY KEY (repository, path)
		)`,
		`CREATE TABLE file_identity (
			repository TEXT NOT NULL, path TEXT NOT NULL, dev INTEGER, ino INTEGER,
			ctime_ns INTEGER, size INTEGER, mtime_ns INTEGER, observed_at REAL NOT NULL,
			PRIMARY KEY (repository, path)
		)`,
		`CREATE TABLE undo_event (
			repository TEXT NOT NULL, batch_id TEXT NOT NULL, path TEXT NOT NULL,
			version_ts TEXT NOT NULL, created_at REAL NOT NULL,
			PRIMARY KEY (repository, batch_id, path)
		)`,
	} {
		if _, err := raw.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	for _, statement := range []string{
		`INSERT INTO meta(key, value) VALUES('device_id', 'python-device')`,
		`INSERT INTO meta(key, value) VALUES('custom', 'keep-me')`,
		`INSERT INTO entry(repository, path, digest, size, mtime_ns, synced_at)
		 VALUES('repo1', 'legacy.txt', 'legacy-digest', 123, 456, 789.5)`,
		`INSERT INTO pending(repository, path, queued_at) VALUES('repo1', 'queued.txt', 1000)`,
		`INSERT INTO local_observation(repository, path, digest, size, mtime_ns, observed_at)
		 VALUES('repo1', 'seen.txt', 'seen-digest', 7, 8, 9)`,
		`INSERT INTO file_identity(repository, path, dev, ino, ctime_ns, size, mtime_ns, observed_at)
		 VALUES('repo1', 'seen.txt', 1, 2, 3, 7, 8, 9)`,
		`INSERT INTO undo_event(repository, batch_id, path, version_ts, created_at)
		 VALUES('repo1', 'batch-1', 'gone.txt', 'version-1', CAST(strftime('%s', 'now') AS REAL))`,
	} {
		if _, err := raw.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}

	store, err := Open(config.New(""))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if got := store.DeviceID(); got != "python-device" {
		t.Fatalf("legacy device ID was not preserved: %q", got)
	}
	if got, err := store.String("custom"); err != nil || got != "keep-me" {
		t.Fatalf("legacy meta value was not preserved: %q, %v", got, err)
	}
	if got, err := store.String(schemaVersionKey); err != nil || got != strconv.Itoa(schemaVersion) {
		t.Fatalf("schema version = %q, %v", got, err)
	}
	entries, err := store.EntrySnapshot("repo1")
	if err != nil {
		t.Fatal(err)
	}
	entry := entries["legacy.txt"]
	if entry.Digest != "legacy-digest" || entry.Size != 123 || entry.MTimeNS != 456 || entry.Causal != "" {
		t.Fatalf("legacy entry changed during migration: %+v", entry)
	}
	observations, err := store.ObservationSnapshot("repo1")
	if err != nil {
		t.Fatal(err)
	}
	observation := observations["seen.txt"]
	if !observation.Present || observation.Digest != "seen-digest" || observation.Size != 7 ||
		observation.MTimeNS != 8 || observation.Causal != "" {
		t.Fatalf("legacy observation changed during migration: %+v", observation)
	}
	identities, err := store.IdentitySnapshot("repo1")
	if err != nil {
		t.Fatal(err)
	}
	identity := identities["seen.txt"]
	if identity.Dev != 1 || identity.Ino != 2 || identity.CtimeNS != 3 || identity.Digest != "" {
		t.Fatalf("legacy identity changed during migration: %+v", identity)
	}
	if got := store.UndoCount("repo1", 24); got != 1 {
		t.Fatalf("legacy undo row was not preserved: count=%d", got)
	}
	var reason string
	var lastAttempt sql.NullFloat64
	var attempts int
	if err := store.db.QueryRow(`
		SELECT last_reason, last_attempt_at, attempt_count FROM pending
		WHERE repository = 'repo1' AND path = 'queued.txt'
	`).Scan(&reason, &lastAttempt, &attempts); err != nil {
		t.Fatal(err)
	}
	if reason != "" || lastAttempt.Valid || attempts != 0 {
		t.Fatalf("legacy pending defaults are wrong: %q, %+v, %d", reason, lastAttempt, attempts)
	}
	for _, table := range []string{"conflict_group", "conflict_member", "scheduler_queue"} {
		var name string
		if err := store.db.QueryRow(
			`SELECT name FROM sqlite_master WHERE type = 'table' AND name = ?`, table,
		).Scan(&name); err != nil {
			t.Fatalf("Python table %s was not created: %v", table, err)
		}
	}

	// Reopening runs the same idempotent migration and must not duplicate or
	// reset any row.
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(config.New(""))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	entries, err = reopened.EntrySnapshot("repo1")
	if err != nil || len(entries) != 1 || entries["legacy.txt"].Digest != "legacy-digest" {
		t.Fatalf("entry changed after reopen: %+v, %v", entries, err)
	}
	if got := reopened.UndoCount("repo1", 24); got != 1 {
		t.Fatalf("undo row changed after reopen: count=%d", got)
	}
}

func TestOpenConfiguresPragmasAndIndexes(t *testing.T) {
	store := openTestStore(t)
	var busyTimeout, foreignKeys, synchronous int
	var journalMode string
	for _, check := range []struct {
		name  string
		dest  any
		value any
	}{
		{"busy_timeout", &busyTimeout, 10000},
		{"foreign_keys", &foreignKeys, 1},
		{"journal_mode", &journalMode, "wal"},
		{"synchronous", &synchronous, 2},
	} {
		if err := store.db.QueryRow(`PRAGMA ` + check.name).Scan(check.dest); err != nil {
			t.Fatalf("PRAGMA %s: %v", check.name, err)
		}
		if !reflect.DeepEqual(reflect.ValueOf(check.dest).Elem().Interface(), check.value) {
			t.Fatalf("PRAGMA %s = %v, want %v", check.name, reflect.ValueOf(check.dest).Elem().Interface(), check.value)
		}
	}
	if got, err := store.String(schemaVersionKey); err != nil || got != strconv.Itoa(schemaVersion) {
		t.Fatalf("schema version = %q, %v", got, err)
	}

	for index, want := range map[string]string{
		"idx_file_identity_digest":     "repository,digest",
		"idx_undo_created":             "repository,created_at",
		"idx_undo_pending_created":     "repository,applied,created_at",
		"idx_pending_last_attempt":     "repository,last_attempt_at",
		"idx_scheduler_queue_sequence": "repository,sequence",
		"idx_conflict_group_open":      "repository,status,original_path",
	} {
		if got := sqliteIndexColumns(t, store.db, index); got != want {
			t.Fatalf("index %s columns = %q, want %q", index, got, want)
		}
	}
}

func TestSchemaInitializationRollsBackOnIncompatibleTable(t *testing.T) {
	dbPath := testStatePath(t)
	raw := openRawStateDB(t, dbPath)
	for _, statement := range []string{
		`CREATE TABLE meta (key TEXT PRIMARY KEY, value TEXT NOT NULL)`,
		`CREATE TABLE pending (
			repository TEXT NOT NULL, queued_at REAL NOT NULL,
			PRIMARY KEY (repository)
		)`,
		`INSERT INTO pending(repository, queued_at) VALUES('repo1', 1)`,
	} {
		if _, err := raw.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}

	if _, err := Open(config.New("")); err == nil || !strings.Contains(err.Error(), "pending is missing required column path") {
		t.Fatalf("Open should reject the incompatible pending table, got %v", err)
	}
	raw = openRawStateDB(t, dbPath)
	defer raw.Close()
	if got := sqliteTableColumns(t, raw, "pending"); !reflect.DeepEqual(got, []string{"repository", "queued_at"}) {
		t.Fatalf("failed migration left pending partially altered: %v", got)
	}
	for _, table := range []string{"entry", "local_observation", "scheduler_queue"} {
		var count int
		if err := raw.QueryRow(
			`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, table,
		).Scan(&count); err != nil || count != 0 {
			t.Fatalf("failed migration left table %s behind: count=%d, %v", table, count, err)
		}
	}
	var count int
	if err := raw.QueryRow(
		`SELECT COUNT(*) FROM meta WHERE key = ?`, schemaVersionKey,
	).Scan(&count); err != nil || count != 0 {
		t.Fatalf("failed migration wrote its schema version: count=%d, %v", count, err)
	}
}

func TestOpenRejectsNewerSchemaVersion(t *testing.T) {
	dbPath := testStatePath(t)
	raw := openRawStateDB(t, dbPath)
	for _, statement := range []string{
		`CREATE TABLE meta (key TEXT PRIMARY KEY, value TEXT NOT NULL)`,
		fmt.Sprintf(`INSERT INTO meta(key, value) VALUES('%s', '%d')`, schemaVersionKey, schemaVersion+1),
	} {
		if _, err := raw.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(config.New("")); err == nil || !strings.Contains(err.Error(), "newer than supported") {
		t.Fatalf("Open should reject a newer schema, got %v", err)
	}
	raw = openRawStateDB(t, dbPath)
	defer raw.Close()
	var version string
	if err := raw.QueryRow(`SELECT value FROM meta WHERE key = ?`, schemaVersionKey).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != strconv.Itoa(schemaVersion+1) {
		t.Fatalf("newer schema version was overwritten: %q", version)
	}
}

func TestObserveRollsBackOnWriteFailure(t *testing.T) {
	store := openTestStore(t)
	const device = "pc1"
	if err := store.RecordEntry("repo1", "a.txt", EntryState{
		Digest: "baseline", Size: 8, MTimeNS: 80, Causal: "remote:4",
	}); err != nil {
		t.Fatal(err)
	}
	first, err := store.Observe("repo1", "a.txt", device, Observation{
		Digest: "one", Size: 3, MTimeNS: 10, Present: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	before, err := store.ObservationSnapshot("repo1")
	if err != nil {
		t.Fatal(err)
	}
	var beforeObservedAt float64
	if err := store.db.QueryRow(
		`SELECT observed_at FROM local_observation WHERE repository = 'repo1' AND path = 'a.txt'`,
	).Scan(&beforeObservedAt); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`
		CREATE TRIGGER reject_observation_update
		BEFORE UPDATE ON local_observation
		BEGIN
			SELECT RAISE(ABORT, 'forced observation failure');
		END
	`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Observe("repo1", "a.txt", device, Observation{
		Digest: "two", Size: 3, MTimeNS: 20, Present: true,
	}); err == nil {
		t.Fatal("Observe should surface the forced write failure")
	}
	after, err := store.ObservationSnapshot("repo1")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("failed Observe changed the stored observation: before=%+v after=%+v", before, after)
	}
	var afterObservedAt float64
	if err := store.db.QueryRow(
		`SELECT observed_at FROM local_observation WHERE repository = 'repo1' AND path = 'a.txt'`,
	).Scan(&afterObservedAt); err != nil {
		t.Fatal(err)
	}
	if beforeObservedAt != afterObservedAt {
		t.Fatalf("failed Observe changed observed_at: %v -> %v", beforeObservedAt, afterObservedAt)
	}
	if _, err := store.db.Exec(`DROP TRIGGER reject_observation_update`); err != nil {
		t.Fatal(err)
	}
	second, err := store.Observe("repo1", "a.txt", device, Observation{
		Digest: "two", Size: 3, MTimeNS: 20, Present: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	firstVersion, ok := causal.Parse(first)
	if !ok {
		t.Fatalf("invalid first causal version %q", first)
	}
	secondVersion, ok := causal.Parse(second)
	if !ok || secondVersion.Counter(device) != firstVersion.Counter(device)+1 || secondVersion.Counter("remote") != 4 {
		t.Fatalf("retry lost causal history: first=%q second=%q", first, second)
	}
}

func TestObserveConcurrentUpdatesPreserveEveryIncrement(t *testing.T) {
	testStatePath(t)
	const workers = 8
	const device = "pc-concurrent"
	stores := make([]*Store, workers)
	for i := range stores {
		store, err := Open(config.New(""))
		if err != nil {
			t.Fatal(err)
		}
		stores[i] = store
		t.Cleanup(func() { _ = store.Close() })
	}

	type result struct {
		index   int
		version string
		err     error
	}
	start := make(chan struct{})
	results := make(chan result, workers)
	for i := range stores {
		go func(index int) {
			<-start
			version, err := stores[index].Observe("repo1", "shared.txt", device, Observation{
				Digest: fmt.Sprintf("digest-%d", index), Size: int64(index + 1),
				MTimeNS: int64(index + 10), Present: true,
			})
			results <- result{index: index, version: version, err: err}
		}(i)
	}
	close(start)

	byCounter := make(map[int]result, workers)
	for range workers {
		item := <-results
		if item.err != nil {
			t.Fatalf("concurrent Observe failed: %v", item.err)
		}
		version, ok := causal.Parse(item.version)
		if !ok {
			t.Fatalf("invalid concurrent causal version %q", item.version)
		}
		counter := version.Counter(device)
		if counter < 1 || counter > workers {
			t.Fatalf("concurrent counter out of range: %q", item.version)
		}
		if _, duplicate := byCounter[counter]; duplicate {
			t.Fatalf("concurrent observers returned the same increment %d", counter)
		}
		byCounter[counter] = item
	}
	finalSnapshot, err := stores[0].ObservationSnapshot("repo1")
	if err != nil {
		t.Fatal(err)
	}
	final := finalSnapshot["shared.txt"]
	finalVersion, ok := causal.Parse(final.Causal)
	if !ok || finalVersion.Counter(device) != workers || len(byCounter) != workers {
		t.Fatalf("concurrent increments were lost: final=%q results=%d", final.Causal, len(byCounter))
	}
	winner := byCounter[workers]
	if final.Causal != winner.version || final.Digest != fmt.Sprintf("digest-%d", winner.index) ||
		final.Size != int64(winner.index+1) || final.MTimeNS != int64(winner.index+10) {
		t.Fatalf("stored observation does not match the last transaction: final=%+v winner=%+v", final, winner)
	}
}

func testStatePath(t *testing.T) string {
	t.Helper()
	base := t.TempDir()
	t.Setenv("XDG_STATE_HOME", base)
	return filepath.Join(base, "sync-daemon", "sync-state.sqlite3")
}

func openRawStateDB(t *testing.T, path string) *sql.DB {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(10000)")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	return db
}

func sqliteIndexColumns(t *testing.T, db *sql.DB, index string) string {
	t.Helper()
	rows, err := db.Query(`PRAGMA index_info(` + index + `)`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var columns []string
	for rows.Next() {
		var sequence, cid int
		var name string
		if err := rows.Scan(&sequence, &cid, &name); err != nil {
			t.Fatal(err)
		}
		columns = append(columns, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return strings.Join(columns, ",")
}

func sqliteTableColumns(t *testing.T, db *sql.DB, table string) []string {
	t.Helper()
	rows, err := db.Query(`PRAGMA table_info(` + table + `)`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var columns []string
	for rows.Next() {
		var cid, notNull, pk int
		var name, columnType string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &pk); err != nil {
			t.Fatal(err)
		}
		columns = append(columns, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return columns
}

func openTestStore(t *testing.T) *Store {
	t.Helper()
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	store, err := Open(config.New(""))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}
