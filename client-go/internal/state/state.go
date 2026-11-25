// Package state is the Go counterpart of sync_state.py. It opens the SAME
// SQLite file as the Python client, so a rollback to Python is always possible.
package state

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	_ "modernc.org/sqlite"

	"nasbox-go/internal/causal"
	"nasbox-go/internal/config"
)

// Store wraps the shared SQLite database.
type Store struct {
	db *sql.DB
}

// Open opens (and schema-initialises) the local state database.
func Open(cfg *config.Config) (*Store, error) {
	base := os.Getenv("XDG_STATE_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, err
		}
		base = filepath.Join(home, ".local", "state")
	}
	dir := filepath.Join(base, "sync-daemon")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", filepath.Join(dir, "sync-state.sqlite3"))
	if err != nil {
		return nil, err
	}
	// One writer at a time keeps WAL contention predictable on a desktop.
	db.SetMaxOpenConns(1)
	store := &Store{db: db}
	if err := store.init(); err != nil {
		db.Close()
		return nil, err
	}
	return store, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) init() error {
	_, err := s.db.Exec(`
		PRAGMA journal_mode=WAL;
		CREATE TABLE IF NOT EXISTS meta (
			key TEXT PRIMARY KEY, value TEXT NOT NULL
		);
		CREATE TABLE IF NOT EXISTS entry (
			repository TEXT NOT NULL, path TEXT NOT NULL,
			digest TEXT, size INTEGER, mtime_ns INTEGER, causal TEXT,
			synced_at REAL NOT NULL, PRIMARY KEY (repository, path)
		);
		CREATE TABLE IF NOT EXISTS local_observation (
			repository TEXT NOT NULL, path TEXT NOT NULL,
			digest TEXT, size INTEGER, mtime_ns INTEGER, causal TEXT,
			observed_at REAL NOT NULL, PRIMARY KEY (repository, path)
		);
		CREATE TABLE IF NOT EXISTS file_identity (
			repository TEXT NOT NULL, path TEXT NOT NULL,
			dev INTEGER, ino INTEGER, ctime_ns INTEGER, size INTEGER,
			mtime_ns INTEGER, digest TEXT, observed_at REAL NOT NULL,
			PRIMARY KEY (repository, path)
		);
		CREATE INDEX IF NOT EXISTS idx_file_identity_digest
			ON file_identity(repository, digest);
		CREATE TABLE IF NOT EXISTS undo_event (
			repository TEXT NOT NULL, batch_id TEXT NOT NULL, path TEXT NOT NULL,
			version_ts TEXT NOT NULL, created_at REAL NOT NULL,
			applied INTEGER NOT NULL DEFAULT 0,
			PRIMARY KEY (repository, batch_id, path)
		);
		CREATE INDEX IF NOT EXISTS idx_undo_created ON undo_event(repository, created_at);
		CREATE TABLE IF NOT EXISTS pending (
			repository TEXT NOT NULL, path TEXT NOT NULL, queued_at REAL NOT NULL,
			last_reason TEXT NOT NULL DEFAULT '', last_attempt_at REAL,
			attempt_count INTEGER NOT NULL DEFAULT 0, PRIMARY KEY (repository, path)
		);
		CREATE TABLE IF NOT EXISTS scheduler_queue (
			repository TEXT NOT NULL, request_id TEXT NOT NULL, device_id TEXT NOT NULL,
			kind TEXT NOT NULL, sequence INTEGER NOT NULL, queued_at REAL NOT NULL,
			PRIMARY KEY (repository, request_id)
		);
	`)
	return err
}

// DeviceID returns the stable per-client identifier stored in meta.
func (s *Store) DeviceID() string {
	var value string
	err := s.db.QueryRow(`SELECT value FROM meta WHERE key='device_id'`).Scan(&value)
	if err == nil && value != "" {
		return value
	}
	buffer := make([]byte, 16)
	if _, err := rand.Read(buffer); err != nil {
		return fmt.Sprintf("go-%d", time.Now().UnixNano())
	}
	value = hex.EncodeToString(buffer)
	_, _ = s.db.Exec(
		`INSERT OR IGNORE INTO meta(key, value) VALUES('device_id', ?)`, value,
	)
	_ = s.db.QueryRow(`SELECT value FROM meta WHERE key='device_id'`).Scan(&value)
	return value
}

// EntryCount mirrors SyncStateStore.entry_count().
func (s *Store) EntryCount(repository string) int {
	var count int
	_ = s.db.QueryRow(
		`SELECT COUNT(*) FROM entry WHERE repository = ?`, repository,
	).Scan(&count)
	return count
}

// Identity is the Go view of file_identity.
type Identity struct {
	Dev, Ino, CtimeNS, Size, MTimeNS int64
	Digest                           string
}

// RecordIdentities stores cheap filesystem identity (no hashing).
func (s *Store) RecordIdentities(repository, root string, digests map[string]string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stmt, err := tx.Prepare(`
		INSERT INTO file_identity(repository, path, dev, ino, ctime_ns, size, mtime_ns, digest, observed_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(repository, path) DO UPDATE SET
			dev=excluded.dev, ino=excluded.ino, ctime_ns=excluded.ctime_ns,
			size=excluded.size, mtime_ns=excluded.mtime_ns,
			digest=COALESCE(excluded.digest, file_identity.digest),
			observed_at=excluded.observed_at
	`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	now := float64(time.Now().UnixNano()) / 1e9
	for path, digest := range digests {
		info, err := os.Lstat(filepath.Join(root, filepath.FromSlash(path)))
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		stat := statInfo(info)
		if _, err := stmt.Exec(
			repository, path, stat.dev, stat.ino, stat.ctimeNS,
			stat.size, stat.mtimeNS, digest, now,
		); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// EntryState is one authoritative baseline row from the same table the Python
// client uses for causal decisions.
type EntryState struct {
	Digest, Causal string
	Size, MTimeNS  int64
}

func (s *Store) EntrySnapshot(repository string) (map[string]EntryState, error) {
	rows, err := s.db.Query(`
		SELECT path, digest, size, mtime_ns, causal FROM entry WHERE repository = ?
	`, repository)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make(map[string]EntryState)
	for rows.Next() {
		var path string
		var digest, causal sql.NullString
		var size, mtimeNS sql.NullInt64
		if err := rows.Scan(&path, &digest, &size, &mtimeNS, &causal); err != nil {
			return nil, err
		}
		entry := EntryState{}
		if digest.Valid {
			entry.Digest = digest.String
		}
		if causal.Valid {
			entry.Causal = causal.String
		}
		if size.Valid {
			entry.Size = size.Int64
		}
		if mtimeNS.Valid {
			entry.MTimeNS = mtimeNS.Int64
		}
		result[path] = entry
	}
	return result, rows.Err()
}

type Observation struct {
	Digest  string
	Size    int64
	MTimeNS int64
	Present bool
}

// Observe records a live local state and returns its causal version. Identical
// observations reuse the previous clock; real changes merge baseline+previous
// and increment this device exactly once.
func (s *Store) Observe(repository, path, device string, observation Observation) (string, error) {
	var previousDigest sql.NullString
	var previousSize, previousMTime sql.NullInt64
	var previousCausal sql.NullString
	err := s.db.QueryRow(`
		SELECT digest, size, mtime_ns, causal FROM local_observation
		WHERE repository = ? AND path = ?
	`, repository, path).Scan(&previousDigest, &previousSize, &previousMTime, &previousCausal)
	if err != nil && err != sql.ErrNoRows {
		return "", err
	}
	same := false
	if observation.Present {
		same = previousDigest.Valid && previousDigest.String == observation.Digest &&
			previousSize.Valid && previousSize.Int64 == observation.Size &&
			previousMTime.Valid && previousMTime.Int64 == observation.MTimeNS
	} else {
		same = !previousDigest.Valid && !previousSize.Valid && !previousMTime.Valid
	}
	if same && previousCausal.Valid {
		if _, ok := causal.Parse(previousCausal.String); ok {
			return previousCausal.String, nil
		}
	}
	var baselineCausal sql.NullString
	_ = s.db.QueryRow(`SELECT causal FROM entry WHERE repository = ? AND path = ?`,
		repository, path).Scan(&baselineCausal)
	base, _ := causal.Parse(baselineCausal.String)
	previous, _ := causal.Parse(previousCausal.String)
	version := (&causal.Version{}).Merge(base).Merge(previous).Increment(device)
	encoded := version.Encode()
	var digest, size, mtime any
	if observation.Present {
		digest, size, mtime = observation.Digest, observation.Size, observation.MTimeNS
	}
	_, err = s.db.Exec(`
		INSERT INTO local_observation(repository, path, digest, size, mtime_ns, causal, observed_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(repository, path) DO UPDATE SET
			digest=excluded.digest, size=excluded.size, mtime_ns=excluded.mtime_ns,
			causal=excluded.causal, observed_at=excluded.observed_at
	`, repository, path, digest, size, mtime, encoded, float64(time.Now().UnixNano())/1e9)
	return encoded, err
}

func (s *Store) RecordEntry(repository, path string, entry EntryState) error {
	_, err := s.db.Exec(`
		INSERT INTO entry(repository, path, digest, size, mtime_ns, causal, synced_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(repository, path) DO UPDATE SET
			digest=excluded.digest, size=excluded.size, mtime_ns=excluded.mtime_ns,
			causal=excluded.causal, synced_at=excluded.synced_at
	`, repository, path, entry.Digest, entry.Size, entry.MTimeNS, entry.Causal,
		float64(time.Now().UnixNano())/1e9)
	return err
}

// ForgetPaths drops the sync baseline and the local observation of paths, so
// the next pass treats the NAS copy as a new file and downloads it again. It
// is how "restore what I deleted" works.
func (s *Store) ForgetPaths(repository string, paths []string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for _, path := range paths {
		if _, err := tx.Exec(`DELETE FROM entry WHERE repository = ? AND path = ?`, repository, path); err != nil {
			return err
		}
		if _, err := tx.Exec(`DELETE FROM local_observation WHERE repository = ? AND path = ?`, repository, path); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// BackfillIdentities records lstat identity for the whole local tree, without
// hashing file contents. Digests already known in `entry` are preserved.
func (s *Store) BackfillIdentities(
	repository, root string,
	excluded func(string) bool,
	progress func(int, int),
) (int, error) {
	known := map[string]string{}
	rows, err := s.db.Query(`SELECT path, digest FROM entry WHERE repository = ?`, repository)
	if err != nil {
		return 0, err
	}
	for rows.Next() {
		var path string
		var digest sql.NullString
		if err := rows.Scan(&path, &digest); err != nil {
			rows.Close()
			return 0, err
		}
		if digest.Valid {
			known[path] = digest.String
		}
	}
	rows.Close()

	type identityRow struct {
		path, digest                     string
		dev, ino, ctimeNS, size, mtimeNS int64
	}
	collected := make([]identityRow, 0, 1024)
	seen := 0
	err = filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if rel == "." {
			return nil
		}
		if excluded != nil && excluded(rel) {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.IsDir() || !entry.Type().IsRegular() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return nil
		}
		stat := statInfo(info)
		seen++
		collected = append(collected, identityRow{
			path: rel, digest: known[rel], dev: stat.dev, ino: stat.ino,
			ctimeNS: stat.ctimeNS, size: stat.size, mtimeNS: stat.mtimeNS,
		})
		return nil
	})
	if err != nil {
		return 0, err
	}
	if len(collected) == 0 {
		return 0, nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	stmt, err := tx.Prepare(`
		INSERT INTO file_identity(repository, path, dev, ino, ctime_ns, size, mtime_ns, digest, observed_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(repository, path) DO UPDATE SET
			dev=excluded.dev, ino=excluded.ino, ctime_ns=excluded.ctime_ns,
			size=excluded.size, mtime_ns=excluded.mtime_ns,
			digest=COALESCE(excluded.digest, file_identity.digest),
			observed_at=excluded.observed_at
	`)
	if err != nil {
		return 0, err
	}
	defer stmt.Close()
	now := float64(time.Now().UnixNano()) / 1e9
	for _, row := range collected {
		if _, err := stmt.Exec(repository, row.path, row.dev, row.ino, row.ctimeNS,
			row.size, row.mtimeNS, row.digest, now); err != nil {
			return 0, err
		}
	}
	if progress != nil {
		progress(len(collected), seen)
	}
	return len(collected), tx.Commit()
}

// IdentitySnapshot returns all known local identities for a repository.
func (s *Store) IdentitySnapshot(repository string) (map[string]Identity, error) {
	rows, err := s.db.Query(`
		SELECT path, dev, ino, ctime_ns, size, mtime_ns, digest
		FROM file_identity WHERE repository = ?
	`, repository)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make(map[string]Identity)
	for rows.Next() {
		var path, digest string
		var id Identity
		if err := rows.Scan(&path, &id.Dev, &id.Ino, &id.CtimeNS, &id.Size, &id.MTimeNS, &digest); err != nil {
			return nil, err
		}
		id.Digest = digest
		result[path] = id
	}
	return result, rows.Err()
}

// ObservationRecord is the stored local observation of one path.
type ObservationRecord struct {
	// AllNull: digest, size and mtime are all NULL (a recorded absence).
	AllNull bool
	Present bool
	Digest  string
	Size    int64
	MTimeNS int64
	Causal  string
}

// ObservationSnapshot loads every stored observation of a repository in one
// query. The sync pass uses it to skip Observe (one SELECT per file) for
// the files that did not change: on 120k files that was ~4 s per pass.
func (s *Store) ObservationSnapshot(repository string) (map[string]ObservationRecord, error) {
	rows, err := s.db.Query(`
		SELECT path, digest, size, mtime_ns, causal FROM local_observation WHERE repository = ?
	`, repository)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make(map[string]ObservationRecord)
	for rows.Next() {
		var path string
		var digest, causalText sql.NullString
		var size, mtime sql.NullInt64
		if err := rows.Scan(&path, &digest, &size, &mtime, &causalText); err != nil {
			return nil, err
		}
		result[path] = ObservationRecord{
			AllNull: !digest.Valid && !size.Valid && !mtime.Valid,
			Present: digest.Valid, Digest: digest.String, Size: size.Int64,
			MTimeNS: mtime.Int64, Causal: causalText.String,
		}
	}
	return result, rows.Err()
}

// UnchangedCausal returns the stored causal version when observation equals
// the stored one exactly as Observe would decide (same digest, size, mtime;
// or still absent), so Observe can be skipped.
func UnchangedCausal(stored ObservationRecord, known bool, observation Observation) (string, bool) {
	if !known || stored.Causal == "" {
		return "", false
	}
	if _, ok := causal.Parse(stored.Causal); !ok {
		return "", false
	}
	if observation.Present {
		if stored.Present && stored.Digest == observation.Digest &&
			stored.Size == observation.Size && stored.MTimeNS == observation.MTimeNS {
			return stored.Causal, true
		}
		return "", false
	}
	if stored.AllNull {
		return stored.Causal, true
	}
	return "", false
}

// UndoEntry is one restorable deletion.
type UndoEntry struct {
	BatchID, Path, VersionTS string
	CreatedAt                float64
}

func (s *Store) RecordUndo(repository string, paths []string, versionTS string) error {
	if len(paths) == 0 || versionTS == "" {
		return nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stmt, err := tx.Prepare(`
		INSERT INTO undo_event(repository, batch_id, path, version_ts, created_at, applied)
		VALUES (?, ?, ?, ?, ?, 0) ON CONFLICT(repository, batch_id, path) DO NOTHING
	`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	now := float64(time.Now().UnixNano()) / 1e9
	for _, path := range paths {
		if _, err := stmt.Exec(repository, versionTS, path, versionTS, now); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) RecentUndo(repository string, hours float64, limit int) ([]UndoEntry, error) {
	cutoff := float64(time.Now().UnixNano())/1e9 - hours*3600
	rows, err := s.db.Query(`
		SELECT batch_id, path, version_ts, created_at FROM undo_event
		WHERE repository = ? AND applied = 0 AND created_at >= ?
		ORDER BY created_at DESC LIMIT ?
	`, repository, cutoff, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []UndoEntry
	for rows.Next() {
		var item UndoEntry
		if err := rows.Scan(&item.BatchID, &item.Path, &item.VersionTS, &item.CreatedAt); err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func (s *Store) UndoCount(repository string, hours float64) int {
	cutoff := float64(time.Now().UnixNano())/1e9 - hours*3600
	var count int
	_ = s.db.QueryRow(`
		SELECT COUNT(*) FROM undo_event
		WHERE repository = ? AND applied = 0 AND created_at >= ?
	`, repository, cutoff).Scan(&count)
	return count
}

func (s *Store) MarkUndoApplied(repository, batchID string, paths []string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stmt, err := tx.Prepare(`
		UPDATE undo_event SET applied = 1
		WHERE repository = ? AND batch_id = ? AND path = ?
	`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, path := range paths {
		if _, err := stmt.Exec(repository, batchID, path); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) String(key string) (string, error) {
	var value string
	err := s.db.QueryRow(`SELECT value FROM meta WHERE key=?`, key).Scan(&value)
	return value, err
}

type fileStat struct {
	dev, ino, ctimeNS, size, mtimeNS int64
}

func statInfo(info os.FileInfo) fileStat {
	raw, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fileStat{size: info.Size(), mtimeNS: info.ModTime().UnixNano()}
	}
	return fileStat{
		dev:     int64(raw.Dev),
		ino:     int64(raw.Ino),
		ctimeNS: raw.Ctim.Sec*1e9 + int64(raw.Ctim.Nsec),
		size:    info.Size(),
		mtimeNS: info.ModTime().UnixNano(),
	}
}

// SetString stores a small value in meta (used for "already done once" marks).
func (s *Store) SetString(key, value string) error {
	_, err := s.db.Exec(
		`INSERT INTO meta(key, value) VALUES(?, ?)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	return err
}

// PruneExcluded drops every record for paths the current exclusion rules
// ignore. Such rows can never be evaluated again -- the local scan and the
// planner both skip them -- and leaving them behind makes two machines with
// identical rules still report different counts, which is exactly how a real
// gap in the tracked set stayed hidden.
func (s *Store) PruneExcluded(repository string, excluded func(string) bool) (int, error) {
	rows, err := s.db.Query(`SELECT path FROM entry WHERE repository = ?`, repository)
	if err != nil {
		return 0, err
	}
	victims := make([]string, 0)
	for rows.Next() {
		var path string
		if scanErr := rows.Scan(&path); scanErr != nil {
			rows.Close()
			return 0, scanErr
		}
		if excluded(path) {
			victims = append(victims, path)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	if len(victims) == 0 {
		return 0, nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	const batch = 400
	for start := 0; start < len(victims); start += batch {
		end := start + batch
		if end > len(victims) {
			end = len(victims)
		}
		chunk := victims[start:end]
		placeholders := strings.TrimSuffix(strings.Repeat("?,", len(chunk)), ",")
		args := make([]any, 0, len(chunk)+1)
		args = append(args, repository)
		for _, path := range chunk {
			args = append(args, path)
		}
		for _, table := range []string{"entry", "local_observation", "file_identity", "pending"} {
			if _, execErr := tx.Exec(
				`DELETE FROM `+table+` WHERE repository = ? AND path IN (`+placeholders+`)`, args...,
			); execErr != nil {
				return 0, execErr
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return len(victims), nil
}
