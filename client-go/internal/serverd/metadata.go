package serverd

import (
	"bufio"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

const (
	metadataSchemaVersion  = "1"
	metadataDBName         = "metadata.sqlite3"
	journalHashWindow      = 64 * 1024
	metadataEntryUpsertSQL = `
		INSERT INTO entry(
			path, digest, size, mtime_ns, device, event_ts, causal,
			causal_present, deleted, updated_revision
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(path) DO UPDATE SET
			digest=excluded.digest, size=excluded.size, mtime_ns=excluded.mtime_ns,
			device=excluded.device, event_ts=excluded.event_ts, causal=excluded.causal,
			causal_present=excluded.causal_present, deleted=excluded.deleted,
			updated_revision=excluded.updated_revision
	`
)

// metadataIndex is a derived, disposable SQLite view of manifest.tsv plus the
// committed part of transfer-journal.tsv. The text files remain the recovery
// source and the compatibility format; this index makes lookups and revision
// deltas proportional to the changed set instead of the whole repository.
type metadataIndex struct {
	db    *sql.DB
	paths StatePaths
}

type metadataEntry struct {
	Path          string
	Digest        string
	Size          int64
	MTimeNS       int64
	Device        string
	EventTS       int64
	Causal        string
	CausalPresent bool
	Deleted       bool
	Revision      int64
}

type journalCommit struct {
	TxID string
	Rows []string
}

type journalCheckpoint struct {
	Offset int64
	Head   string
	Tail   string
}

type manifestView struct {
	Current      int64
	FallbackFull bool
	Rows         []string
}

func openMetadataIndex(paths StatePaths) (*metadataIndex, error) {
	index, err := openMetadataIndexOnce(paths)
	if err == nil || !isSQLiteCorruption(err) {
		return index, err
	}
	// The index is derived from the text journal/manifest. Quarantine a damaged
	// copy and rebuild once instead of leaving every reader on the slow fallback.
	path := filepath.Join(paths.Dir, metadataDBName)
	stamp := strconv.FormatInt(time.Now().UnixNano(), 10)
	for _, suffix := range []string{"", "-wal", "-shm"} {
		_ = os.Rename(path+suffix, path+suffix+".broken-"+stamp)
	}
	return openMetadataIndexOnce(paths)
}

func isSQLiteCorruption(err error) bool {
	if err == nil {
		return false
	}
	text := strings.ToLower(err.Error())
	return strings.Contains(text, "not a database") ||
		strings.Contains(text, "malformed") ||
		strings.Contains(text, "corrupt")
}

func openMetadataIndexOnce(paths StatePaths) (*metadataIndex, error) {
	if err := os.MkdirAll(paths.Dir, 0o700); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", filepath.Join(paths.Dir, metadataDBName))
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	index := &metadataIndex{db: db, paths: paths}
	for _, pragma := range []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA synchronous=NORMAL",
		"PRAGMA foreign_keys=ON",
		"PRAGMA busy_timeout=10000",
	} {
		if _, err := db.Exec(pragma); err != nil {
			db.Close()
			return nil, fmt.Errorf("metadata sqlite %s: %w", pragma, err)
		}
	}
	tx, err := db.Begin()
	if err != nil {
		db.Close()
		return nil, err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`
		CREATE TABLE IF NOT EXISTS meta (
			key TEXT PRIMARY KEY,
			value TEXT NOT NULL
		);
		CREATE TABLE IF NOT EXISTS entry (
			path TEXT PRIMARY KEY,
			digest TEXT NOT NULL DEFAULT '',
			size INTEGER NOT NULL DEFAULT 0,
			mtime_ns INTEGER NOT NULL DEFAULT 0,
			device TEXT NOT NULL DEFAULT '',
			event_ts INTEGER NOT NULL DEFAULT 0,
			causal TEXT NOT NULL DEFAULT '',
			causal_present INTEGER NOT NULL DEFAULT 0,
			deleted INTEGER NOT NULL DEFAULT 0,
			updated_revision INTEGER NOT NULL DEFAULT 0
		) WITHOUT ROWID;
		CREATE INDEX IF NOT EXISTS idx_entry_updated_revision
			ON entry(updated_revision);
		CREATE INDEX IF NOT EXISTS idx_entry_deleted_ts
			ON entry(deleted, event_ts);
		CREATE TABLE IF NOT EXISTS journal_tx (
			txid TEXT PRIMARY KEY,
			revision INTEGER NOT NULL,
			committed_at INTEGER NOT NULL DEFAULT 0
		) WITHOUT ROWID;
		CREATE INDEX IF NOT EXISTS idx_journal_tx_revision
			ON journal_tx(revision);
		INSERT OR IGNORE INTO meta(key, value) VALUES('schema_version', '1');
	`); err != nil {
		db.Close()
		return nil, err
	}
	var schema string
	if err := tx.QueryRow(`SELECT value FROM meta WHERE key='schema_version'`).Scan(&schema); err != nil {
		db.Close()
		return nil, err
	}
	if schema != metadataSchemaVersion {
		db.Close()
		return nil, fmt.Errorf("metadata sqlite schema %q non supportata (attesa %s)", schema, metadataSchemaVersion)
	}
	if err := tx.Commit(); err != nil {
		db.Close()
		return nil, err
	}
	return index, nil
}

func (i *metadataIndex) Close() error { return i.db.Close() }

func (i *metadataIndex) metaInt64(key string) (int64, bool, error) {
	var value string
	err := i.db.QueryRow(`SELECT value FROM meta WHERE key=?`, key).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	number, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return 0, false, fmt.Errorf("metadata %s non valido: %w", key, err)
	}
	return number, true, nil
}

func (i *metadataIndex) setMeta(tx *sql.Tx, key, value string) error {
	_, err := tx.Exec(`
		INSERT INTO meta(key, value) VALUES(?, ?)
		ON CONFLICT(key) DO UPDATE SET value=excluded.value
	`, key, value)
	return err
}

func (i *metadataIndex) setMetaInt64(tx *sql.Tx, key string, value int64) error {
	return i.setMeta(tx, key, strconv.FormatInt(value, 10))
}

func (i *metadataIndex) transactionExists(txid string) (bool, error) {
	var one int
	err := i.db.QueryRow(`SELECT 1 FROM journal_tx WHERE txid=?`, txid).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// syncLocked catches the derived index up with the append-only journal. The
// caller must hold the journal lock. A partial trailing transaction is never
// applied and remains before the checkpoint so a later sync can finish it.
func (i *metadataIndex) syncLocked() error {
	info, err := os.Stat(i.paths.Journal)
	if err != nil {
		return err
	}
	current, err := revisionFromFile(i.paths.Revision)
	if err != nil {
		return err
	}
	indexed, hasIndexed, err := i.metaInt64("indexed_revision")
	if err != nil {
		return err
	}
	checkpoint, hasCheckpoint := i.loadCheckpoint()
	if !hasIndexed || !hasCheckpoint || current < indexed || !checkpointMatches(i.paths.Journal, info.Size(), checkpoint) {
		return i.rebuildLocked(current)
	}
	if info.Size() == checkpoint.Offset {
		if indexed != current {
			return i.rebuildLocked(current)
		}
		return nil
	}
	commits, end, err := readJournalCommits(i.paths.Journal, checkpoint.Offset)
	if err != nil {
		return err
	}
	if len(commits) == 0 {
		return nil
	}
	head, tail, err := journalFingerprint(i.paths.Journal, end)
	if err != nil {
		return err
	}
	return i.applyCommitsLocked(commits, current, indexed, journalCheckpoint{Offset: end, Head: head, Tail: tail})
}

func revisionFromFile(path string) (int64, error) {
	raw := readString(path)
	if raw == "" {
		return 0, nil
	}
	revision, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || revision < 0 {
		return 0, fmt.Errorf("manifest.revision non valido")
	}
	return revision, nil
}

func (i *metadataIndex) loadCheckpoint() (journalCheckpoint, bool) {
	offset, okOffset, err := i.metaInt64("journal_offset")
	if err != nil || !okOffset || offset < 0 {
		return journalCheckpoint{}, false
	}
	var head, tail string
	if err := i.db.QueryRow(`SELECT value FROM meta WHERE key='journal_head'`).Scan(&head); err != nil {
		return journalCheckpoint{}, false
	}
	if err := i.db.QueryRow(`SELECT value FROM meta WHERE key='journal_tail'`).Scan(&tail); err != nil {
		return journalCheckpoint{}, false
	}
	return journalCheckpoint{Offset: offset, Head: head, Tail: tail}, true
}

func (i *metadataIndex) saveCheckpoint(tx *sql.Tx, checkpoint journalCheckpoint) error {
	if err := i.setMetaInt64(tx, "journal_offset", checkpoint.Offset); err != nil {
		return err
	}
	if err := i.setMeta(tx, "journal_head", checkpoint.Head); err != nil {
		return err
	}
	return i.setMeta(tx, "journal_tail", checkpoint.Tail)
}

func (i *metadataIndex) rebuildLocked(current int64) error {
	base, err := readLines(i.paths.Manifest)
	if err != nil {
		return err
	}
	commits, end, err := readJournalCommits(i.paths.Journal, 0)
	if err != nil {
		return err
	}
	head, tail, err := journalFingerprint(i.paths.Journal, end)
	if err != nil {
		return err
	}
	checkpoint := journalCheckpoint{Offset: end, Head: head, Tail: tail}
	tx, err := i.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM entry; DELETE FROM journal_tx;`); err != nil {
		return err
	}
	upsert, err := tx.Prepare(metadataEntryUpsertSQL)
	if err != nil {
		return err
	}
	defer upsert.Close()
	for _, line := range base {
		entry, ok, err := parseManifestEntry(line)
		if err != nil {
			return err
		}
		if !ok {
			continue
		}
		if err := execMetadataEntryArgs(upsert.Exec, entry); err != nil {
			return err
		}
	}
	start := current - int64(len(commits)) + 1
	effectiveRevision := current
	if current < int64(len(commits)) {
		effectiveRevision = int64(len(commits))
		start = 1
	}
	if start < 0 {
		start = 0
	}
	for index, commit := range commits {
		if err := applyMetadataCommit(tx, commit, start+int64(index)); err != nil {
			return err
		}
	}
	feedStart := current - int64(len(commits))
	if feedStart < 0 {
		feedStart = 0
	}
	if err := i.setMetaInt64(tx, "indexed_revision", effectiveRevision); err != nil {
		return err
	}
	if err := i.setMetaInt64(tx, "feed_start_revision", feedStart); err != nil {
		return err
	}
	if err := i.saveCheckpoint(tx, checkpoint); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if effectiveRevision != current {
		return i.writeRevision(effectiveRevision)
	}
	return nil
}

func (i *metadataIndex) applyCommitsLocked(commits []journalCommit, current, previous int64, checkpoint journalCheckpoint) error {
	tx, err := i.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	newCommits := make([]journalCommit, 0, len(commits))
	for _, commit := range commits {
		var one int
		err := tx.QueryRow(`SELECT 1 FROM journal_tx WHERE txid=?`, commit.TxID).Scan(&one)
		if errors.Is(err, sql.ErrNoRows) {
			newCommits = append(newCommits, commit)
			continue
		}
		if err != nil {
			return err
		}
	}
	start := current - int64(len(newCommits)) + 1
	if start < 0 {
		start = 0
	}
	effectiveRevision := current
	if len(newCommits) > 0 {
		minimumRevision := previous + int64(len(newCommits))
		if current < minimumRevision {
			// A crash can land after the journal fsync but before the revision
			// bump. Replay assigns the missing revisions and repairs the cursor.
			effectiveRevision = minimumRevision
			start = previous + 1
		}
	}
	for index, commit := range newCommits {
		if err := applyMetadataCommit(tx, commit, start+int64(index)); err != nil {
			return err
		}
	}
	if len(newCommits) > 0 {
		if err := i.setMetaInt64(tx, "indexed_revision", effectiveRevision); err != nil {
			return err
		}
	}
	if err := i.saveCheckpoint(tx, checkpoint); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if effectiveRevision != current {
		return i.writeRevision(effectiveRevision)
	}
	return nil
}

func (i *metadataIndex) writeRevision(revision int64) error {
	return writeRevisionFile(i.paths.Revision, revision)
}

func applyMetadataCommit(tx *sql.Tx, commit journalCommit, revision int64) error {
	if _, err := tx.Exec(`
		INSERT INTO journal_tx(txid, revision, committed_at) VALUES(?, ?, ?)
	`, commit.TxID, revision, time.Now().Unix()); err != nil {
		return err
	}
	for _, row := range commit.Rows {
		entry, ok, err := parseJournalEntry(row, revision)
		if err != nil {
			return err
		}
		if !ok {
			continue
		}
		if err := upsertMetadataEntry(tx, entry); err != nil {
			return err
		}
	}
	return nil
}

func parseManifestEntry(line string) (metadataEntry, bool, error) {
	if line == "" || line == "NASBOX_MANIFEST_V1" {
		return metadataEntry{}, false, nil
	}
	fields := strings.Split(line, "\t")
	if len(fields) < 6 || fields[0] == "" {
		return metadataEntry{}, false, nil
	}
	size, err := strconv.ParseInt(fields[2], 10, 64)
	if err != nil || size < 0 {
		return metadataEntry{}, false, fmt.Errorf("manifest size non valida per %q", fields[0])
	}
	mtime, err := strconv.ParseInt(fields[3], 10, 64)
	if err != nil {
		return metadataEntry{}, false, fmt.Errorf("manifest mtime non valido per %q", fields[0])
	}
	ts, err := strconv.ParseInt(fields[5], 10, 64)
	if err != nil {
		return metadataEntry{}, false, fmt.Errorf("manifest timestamp non valido per %q", fields[0])
	}
	entry := metadataEntry{
		Path: decodeField(fields[0]), Digest: decodeField(fields[1]),
		Size: size, MTimeNS: mtime, Device: decodeField(fields[4]), EventTS: ts,
		CausalPresent: len(fields) >= 7, Revision: 0,
	}
	if entry.CausalPresent {
		entry.Causal = decodeField(fields[6])
	}
	entry.Deleted = entry.Digest == "" && entry.Size == 0
	return entry, true, nil
}

func parseJournalEntry(row string, revision int64) (metadataEntry, bool, error) {
	fields := strings.Split(row, "\t")
	if len(fields) < 7 {
		return metadataEntry{}, false, nil
	}
	entry := metadataEntry{Path: decodeField(fields[1]), Revision: revision, CausalPresent: true}
	if entry.Path == "" {
		return metadataEntry{}, false, nil
	}
	switch fields[0] {
	case "PUT":
		entry.Digest = decodeField(fields[2])
		var err error
		entry.Size, err = strconv.ParseInt(fields[3], 10, 64)
		if err != nil || entry.Size < 0 {
			return metadataEntry{}, false, fmt.Errorf("journal PUT size non valida per %q", entry.Path)
		}
		entry.MTimeNS, err = strconv.ParseInt(fields[4], 10, 64)
		if err != nil {
			return metadataEntry{}, false, fmt.Errorf("journal PUT mtime non valido per %q", entry.Path)
		}
		entry.Device = decodeField(fields[5])
		entry.EventTS, err = strconv.ParseInt(fields[6], 10, 64)
		if err != nil {
			return metadataEntry{}, false, fmt.Errorf("journal PUT timestamp non valido per %q", entry.Path)
		}
		if len(fields) > 7 {
			entry.Causal = decodeField(fields[7])
		}
	case "DELETE":
		entry.Deleted = true
		entry.Device = decodeField(fields[5])
		var err error
		entry.EventTS, err = strconv.ParseInt(fields[6], 10, 64)
		if err != nil {
			return metadataEntry{}, false, fmt.Errorf("journal DELETE timestamp non valido per %q", entry.Path)
		}
		if len(fields) > 7 {
			entry.Causal = decodeField(fields[7])
		}
	default:
		return metadataEntry{}, false, nil
	}
	return entry, true, nil
}

func upsertMetadataEntry(tx *sql.Tx, entry metadataEntry) error {
	_, err := tx.Exec(metadataEntryUpsertSQL, metadataEntryArgs(entry)...)
	return err
}

func execMetadataEntryArgs(exec func(...any) (sql.Result, error), entry metadataEntry) error {
	_, err := exec(metadataEntryArgs(entry)...)
	return err
}

func metadataEntryArgs(entry metadataEntry) []any {
	deleted := 0
	if entry.Deleted {
		deleted = 1
	}
	causalPresent := 0
	if entry.CausalPresent {
		causalPresent = 1
	}
	return []any{
		entry.Path, entry.Digest, entry.Size, entry.MTimeNS, entry.Device, entry.EventTS,
		entry.Causal, causalPresent, deleted, entry.Revision,
	}
}

func readJournalCommits(path string, offset int64) ([]journalCommit, int64, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, offset, err
	}
	defer file.Close()
	if _, err := file.Seek(offset, io.SeekStart); err != nil {
		return nil, offset, err
	}
	reader := bufio.NewReader(file)
	position := offset
	completeEnd := offset
	var current *journalCommit
	commits := make([]journalCommit, 0)
	for {
		line, readErr := reader.ReadString('\n')
		if len(line) > 0 {
			position += int64(len(line))
			line = strings.TrimSuffix(line, "\n")
			line = strings.TrimSuffix(line, "\r")
			fields := strings.Split(line, "\t")
			switch fields[0] {
			case "BEGIN":
				if len(fields) > 1 {
					current = &journalCommit{TxID: fields[1]}
				}
			case "PUT", "DELETE":
				if current != nil {
					current.Rows = append(current.Rows, line)
				}
			case "COMMIT":
				if current != nil && len(fields) > 1 && fields[1] == current.TxID {
					commits = append(commits, *current)
					current = nil
					completeEnd = position
				}
			}
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return nil, completeEnd, readErr
		}
	}
	return commits, completeEnd, nil
}

func journalFingerprint(path string, offset int64) (string, string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", "", err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return "", "", err
	}
	if offset < 0 || offset > info.Size() {
		return "", "", fmt.Errorf("offset journal fuori intervallo")
	}
	head, err := hashSection(file, 0, minInt64(offset, journalHashWindow))
	if err != nil {
		return "", "", err
	}
	tailStart := int64(0)
	if offset > journalHashWindow {
		tailStart = offset - journalHashWindow
	}
	tail, err := hashSection(file, tailStart, offset-tailStart)
	if err != nil {
		return "", "", err
	}
	return head, tail, nil
}

func hashSection(file *os.File, offset, length int64) (string, error) {
	if length == 0 {
		hash := sha256.Sum256(nil)
		return hex.EncodeToString(hash[:]), nil
	}
	if _, err := file.Seek(offset, io.SeekStart); err != nil {
		return "", err
	}
	hash := sha256.New()
	if _, err := io.CopyN(hash, file, length); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func checkpointMatches(path string, size int64, checkpoint journalCheckpoint) bool {
	if checkpoint.Offset > size {
		return false
	}
	head, tail, err := journalFingerprint(path, checkpoint.Offset)
	return err == nil && head == checkpoint.Head && tail == checkpoint.Tail
}

func minInt64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}

func tombstoneCutoff(config string) int64 {
	fields := strings.Fields(config)
	if len(fields) == 0 {
		return 0
	}
	days, err := strconv.ParseInt(fields[0], 10, 64)
	if err != nil || days <= 0 {
		return 0
	}
	return time.Now().Unix() - days*86400
}

func (i *metadataIndex) manifestRows(changedOnly bool, since, current int64, ttlConfig string) ([]string, error) {
	cutoff := tombstoneCutoff(ttlConfig)
	query := `
		SELECT path, digest, size, mtime_ns, device, event_ts, causal,
		       causal_present, deleted
		FROM entry
		WHERE (? = 0 OR deleted = 0 OR event_ts >= ?)
	`
	args := []any{cutoff, cutoff}
	if changedOnly {
		query += ` AND updated_revision > ? AND updated_revision <= ?`
		args = append(args, since, current)
	}
	query += ` ORDER BY path`
	rows, err := i.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]string, 0)
	for rows.Next() {
		var entry metadataEntry
		var causalPresent, deleted int
		if err := rows.Scan(
			&entry.Path, &entry.Digest, &entry.Size, &entry.MTimeNS,
			&entry.Device, &entry.EventTS, &entry.Causal, &causalPresent, &deleted,
		); err != nil {
			return nil, err
		}
		if internalPath.MatchString(entry.Path) {
			continue
		}
		entry.CausalPresent = causalPresent != 0
		entry.Deleted = deleted != 0
		result = append(result, formatManifestEntry(entry))
	}
	return result, rows.Err()
}

func formatManifestEntry(entry metadataEntry) string {
	fields := []string{
		encodeField(entry.Path), encodeField(entry.Digest),
		strconv.FormatInt(entry.Size, 10), strconv.FormatInt(entry.MTimeNS, 10),
		encodeField(entry.Device), strconv.FormatInt(entry.EventTS, 10),
	}
	if entry.CausalPresent {
		fields = append(fields, encodeField(entry.Causal))
	}
	return strings.Join(fields, "\t")
}

func (i *metadataIndex) lookup(path string, ttlConfig string) (string, bool, error) {
	entry, found, err := i.lookupEntry(path, ttlConfig)
	if err != nil || !found {
		return "", found, err
	}
	return formatManifestEntry(entry), true, nil
}

func (i *metadataIndex) lookupEntry(path string, ttlConfig string) (metadataEntry, bool, error) {
	if internalPath.MatchString(path) {
		return metadataEntry{}, false, nil
	}
	var entry metadataEntry
	var causalPresent, deleted int
	err := i.db.QueryRow(`
		SELECT path, digest, size, mtime_ns, device, event_ts, causal,
		       causal_present, deleted
		FROM entry WHERE path=?
	`, path).Scan(
		&entry.Path, &entry.Digest, &entry.Size, &entry.MTimeNS,
		&entry.Device, &entry.EventTS, &entry.Causal, &causalPresent, &deleted,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return metadataEntry{}, false, nil
	}
	if err != nil {
		return metadataEntry{}, false, err
	}
	cutoff := tombstoneCutoff(ttlConfig)
	if deleted != 0 && cutoff > 0 && entry.EventTS < cutoff {
		return metadataEntry{}, false, nil
	}
	entry.CausalPresent = causalPresent != 0
	entry.Deleted = deleted != 0
	return entry, true, nil
}

func (i *metadataIndex) resetCheckpointAfterCompaction() error {
	info, err := os.Stat(i.paths.Journal)
	if err != nil {
		return err
	}
	head, tail, err := journalFingerprint(i.paths.Journal, info.Size())
	if err != nil {
		return err
	}
	tx, err := i.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := i.saveCheckpoint(tx, journalCheckpoint{Offset: info.Size(), Head: head, Tail: tail}); err != nil {
		return err
	}
	return tx.Commit()
}

// renderManifest keeps the historical helper contract while using the SQLite
// index. If the derived database cannot be used, the text renderer remains the
// compatibility and disaster-recovery fallback.
func (c *Context) renderManifest() ([]string, error) {
	view, err := c.exportManifest(0, false)
	if err != nil {
		return nil, err
	}
	return append([]string{"NASBOX_MANIFEST_V1"}, view.Rows...), nil
}

// exportManifest returns a consistent manifest snapshot while holding the same
// journal lock used by writers. True deltas are available from feed_start_revision
// onward; older clients receive the safe full fallback.
func (c *Context) exportManifest(since int64, delta bool) (manifestView, error) {
	var view manifestView
	err := c.withJournalLock(func() error {
		index, indexErr := openMetadataIndex(c.paths())
		if indexErr == nil {
			defer index.Close()
			indexErr = index.syncLocked()
		}
		if indexErr == nil {
			current, err := revisionFromFile(c.paths().Revision)
			if err != nil {
				return err
			}
			view.Current = current
			if !delta {
				view.Rows, err = index.manifestRows(false, 0, current, c.Config["TOMBSTONE_TTL_DAYS"])
				return err
			}
			if since == current {
				return nil
			}
			feedStart, _, err := index.metaInt64("feed_start_revision")
			if err != nil {
				return err
			}
			indexed, _, err := index.metaInt64("indexed_revision")
			if err != nil {
				return err
			}
			if since >= feedStart && indexed == current {
				view.Rows, err = index.manifestRows(true, since, current, c.Config["TOMBSTONE_TTL_DAYS"])
				return err
			}
			view.FallbackFull = true
			view.Rows, err = index.manifestRows(false, 0, current, c.Config["TOMBSTONE_TTL_DAYS"])
			return err
		}
		// The index is derived: never make reads unavailable because it is
		// missing, stale or damaged. Preserve the legacy full-export behavior.
		current, err := revisionFromFile(c.paths().Revision)
		if err != nil {
			return err
		}
		rows, err := c.renderManifestLegacy()
		if err != nil {
			return err
		}
		view.Current = current
		if delta && since != current {
			view.FallbackFull = true
			view.Rows = rows[1:]
		}
		return nil
	})
	return view, err
}

func (c *Context) lookupManifest(path string) (string, bool, error) {
	var row string
	var found bool
	err := c.withJournalLock(func() error {
		index, err := openMetadataIndex(c.paths())
		if err == nil {
			defer index.Close()
			err = index.syncLocked()
		}
		if err == nil {
			row, found, err = index.lookup(path, c.Config["TOMBSTONE_TTL_DAYS"])
			return err
		}
		rows, legacyErr := c.renderManifestLegacy()
		if legacyErr != nil {
			return legacyErr
		}
		row, found = manifestLookup(rows, path)
		return nil
	})
	return row, found, err
}

// lookupManifestMany resolves a batch against one consistent index snapshot.
// It is used by file-states-current so recent journal commits are visible and
// the whole manifest does not need to be rendered for every sync pass.
func (c *Context) lookupManifestMany(paths []string) (map[string]manifestEntry, error) {
	result := make(map[string]manifestEntry, len(paths))
	err := c.withJournalLock(func() error {
		index, err := openMetadataIndex(c.paths())
		if err == nil {
			defer index.Close()
			err = index.syncLocked()
		}
		if err == nil {
			for _, path := range paths {
				entry, found, lookupErr := index.lookupEntry(path, c.Config["TOMBSTONE_TTL_DAYS"])
				if lookupErr != nil {
					return lookupErr
				}
				if found {
					result[path] = manifestEntry{
						Digest: entry.Digest, Size: strconv.FormatInt(entry.Size, 10),
						MTimeNS: strconv.FormatInt(entry.MTimeNS, 10), Epoch: strconv.FormatInt(entry.EventTS, 10),
						Causal: entry.Causal,
					}
				}
			}
			return nil
		}
		rows, legacyErr := c.renderManifestLegacy()
		if legacyErr != nil {
			return legacyErr
		}
		for _, row := range rows[1:] {
			fields := strings.Split(row, "\t")
			if len(fields) < 6 {
				continue
			}
			result[decodeField(fields[0])] = manifestEntry{
				Digest: fields[1], Size: fields[2], MTimeNS: fields[3],
				Epoch: fields[5], Causal: fieldOr(fields, 6, ""),
			}
		}
		return nil
	})
	return result, err
}
