package serverd

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const (
	txIntentDBName        = "tx-intent.sqlite3"
	txIntentSchemaVersion = 1
	txIntentPrepared      = "PREPARED"
	txIntentReady         = "READY"
	txIntentCommitted     = "COMMITTED"
	publishIntentVersion  = 1
)

// txIntentFailpoint is deliberately unset in production. Tests use it to stop
// execution at the durable boundaries without depending on SIGKILL timing.
var txIntentFailpoint func(stage, txid string)

func runTxIntentFailpoint(stage, txid string) {
	if txIntentFailpoint != nil {
		txIntentFailpoint(stage, txid)
	}
}

// publishIntentFileState identifies the target as it was before the publish.
// Device/inode make an idempotent rollback distinguishable from an unrelated
// file that happened to appear at the same path while the lease was unavailable.
type publishIntentFileState struct {
	Mode    uint32 `json:"mode"`
	Size    int64  `json:"size"`
	MTimeNS int64  `json:"mtime_ns"`
	Dev     uint64 `json:"dev"`
	Ino     uint64 `json:"ino"`
}

type publishIntentOp struct {
	Path           string                  `json:"path"`
	Digest         string                  `json:"digest"`
	Size           int64                   `json:"size"`
	MTimeNS        int64                   `json:"mtime_ns"`
	Causal         string                  `json:"causal,omitempty"`
	Chunks         []string                `json:"chunks"`
	BackupRelative string                  `json:"backup_relative"`
	Prior          *publishIntentFileState `json:"prior,omitempty"`
}

type publishIntentPayload struct {
	Version        int               `json:"version"`
	TxID           string            `json:"txid"`
	Device         string            `json:"device"`
	RunTS          string            `json:"run_ts"`
	Mode           string            `json:"mode"`
	TargetRevision int64             `json:"target_revision"`
	Rows           []string          `json:"journal_rows"`
	Ops            []publishIntentOp `json:"ops"`
}

// publishIntentRequest is the immutable part of a retry. Target revision,
// timestamps and prior file identity are intentionally excluded: after a
// PREPARED rollback the same txid may be attempted again against the restored
// filesystem.
type publishIntentRequest struct {
	TxID   string
	Device string
	RunTS  string
	Mode   string
	Ops    []publishIntentOp
}

type txIntentRecord struct {
	TxID           string
	Status         string
	Payload        string
	TargetRevision int64
	CreatedAt      int64
	UpdatedAt      int64
}

type txIntentStore struct {
	db *sql.DB
}

func openTxIntentStore(paths StatePaths) (*txIntentStore, error) {
	if err := os.MkdirAll(paths.Dir, 0o700); err != nil {
		return nil, err
	}
	_, statErr := os.Stat(paths.TxIntent)
	dbExisted := statErr == nil
	if statErr != nil && !os.IsNotExist(statErr) {
		return nil, statErr
	}
	db, err := sql.Open("sqlite", paths.TxIntent)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	closeWithError := func(err error) (*txIntentStore, error) {
		_ = db.Close()
		return nil, err
	}
	for _, pragma := range []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA synchronous=FULL",
		"PRAGMA foreign_keys=ON",
		"PRAGMA busy_timeout=10000",
	} {
		if _, err := db.Exec(pragma); err != nil {
			return closeWithError(fmt.Errorf("tx-intent sqlite %s: %w", pragma, err))
		}
	}
	var journalMode string
	if err := db.QueryRow(`PRAGMA journal_mode`).Scan(&journalMode); err != nil {
		return closeWithError(err)
	}
	if !strings.EqualFold(journalMode, "wal") {
		return closeWithError(fmt.Errorf("tx-intent sqlite non è in WAL mode: %q", journalMode))
	}
	var synchronous int64
	if err := db.QueryRow(`PRAGMA synchronous`).Scan(&synchronous); err != nil {
		return closeWithError(err)
	}
	if synchronous != 2 { // SQLITE_FULL
		return closeWithError(fmt.Errorf("tx-intent sqlite synchronous=%d, atteso FULL", synchronous))
	}
	var schemaVersion int64
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&schemaVersion); err != nil {
		return closeWithError(err)
	}
	if schemaVersion != 0 && schemaVersion != txIntentSchemaVersion {
		return closeWithError(fmt.Errorf("tx-intent sqlite schema %d non supportato", schemaVersion))
	}
	tx, err := db.Begin()
	if err != nil {
		return closeWithError(err)
	}
	defer tx.Rollback()
	schemaSQL := `
		CREATE TABLE IF NOT EXISTS intent (
			txid TEXT PRIMARY KEY,
			status TEXT NOT NULL CHECK(status IN ('PREPARED', 'READY', 'COMMITTED')),
			payload TEXT NOT NULL,
			target_revision INTEGER NOT NULL CHECK(target_revision > 0),
			created_at INTEGER NOT NULL,
			updated_at INTEGER NOT NULL
		) WITHOUT ROWID;
		CREATE INDEX IF NOT EXISTS idx_intent_status_revision
		ON intent(status, target_revision, created_at);
	`
	if schemaVersion == 0 {
		schemaSQL += `PRAGMA user_version=1;`
	}
	if _, err := tx.Exec(schemaSQL); err != nil {
		return closeWithError(err)
	}
	if err := tx.Commit(); err != nil {
		return closeWithError(err)
	}
	// Make creation of the database/WAL directory entries durable as well as
	// the schema transaction itself. Existing databases need no extra directory
	// sync on every lease acquisition.
	if !dbExisted {
		if err := syncDir(paths.Dir); err != nil {
			return closeWithError(err)
		}
	}
	return &txIntentStore{db: db}, nil
}

func (s *txIntentStore) Close() error { return s.db.Close() }

func (s *txIntentStore) prepare(txid string, payload publishIntentPayload) (string, error) {
	raw, err := marshalPublishIntentPayload(payload)
	if err != nil {
		return "", err
	}
	now := time.Now().Unix()
	tx, err := s.db.Begin()
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`
		INSERT INTO intent(txid, status, payload, target_revision, created_at, updated_at)
		VALUES(?, ?, ?, ?, ?, ?)
	`, txid, txIntentPrepared, raw, payload.TargetRevision, now, now); err != nil {
		return "", fmt.Errorf("creazione intent %s: %w", txid, err)
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	return raw, nil
}

func (s *txIntentStore) get(txid string) (txIntentRecord, bool, error) {
	var record txIntentRecord
	err := s.db.QueryRow(`
		SELECT txid, status, payload, target_revision, created_at, updated_at
		FROM intent WHERE txid=?
	`, txid).Scan(
		&record.TxID, &record.Status, &record.Payload,
		&record.TargetRevision, &record.CreatedAt, &record.UpdatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return txIntentRecord{}, false, nil
	}
	if err != nil {
		return txIntentRecord{}, false, err
	}
	return record, true, nil
}

func (s *txIntentStore) list() ([]txIntentRecord, error) {
	rows, err := s.db.Query(`
		SELECT txid, status, payload, target_revision, created_at, updated_at
		FROM intent
		ORDER BY target_revision, created_at, txid
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var records []txIntentRecord
	for rows.Next() {
		var record txIntentRecord
		if err := rows.Scan(
			&record.TxID, &record.Status, &record.Payload,
			&record.TargetRevision, &record.CreatedAt, &record.UpdatedAt,
		); err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	return records, rows.Err()
}

func (s *txIntentStore) updateStatus(txid, payload, from, to string) error {
	query := `UPDATE intent SET status=?, updated_at=? WHERE txid=? AND payload=?`
	args := []any{to, time.Now().Unix(), txid, payload}
	if from != "" {
		query += ` AND status=?`
		args = append(args, from)
	}
	result, err := s.db.Exec(query, args...)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 1 {
		return nil
	}
	record, ok, err := s.get(txid)
	if err != nil {
		return err
	}
	if ok && record.Payload == payload && record.Status == to {
		return nil
	}
	return fmt.Errorf("intent %s non è passabile da %s a %s", txid, from, to)
}

func (s *txIntentStore) remove(txid, payload string) error {
	result, err := s.db.Exec(`DELETE FROM intent WHERE txid=? AND payload=?`, txid, payload)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		if _, ok, err := s.get(txid); err != nil {
			return err
		} else if ok {
			return fmt.Errorf("payload intent %s non corrispondente durante cleanup", txid)
		}
	}
	return nil
}

func marshalPublishIntentPayload(payload publishIntentPayload) (string, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	// Validate the exact representation that will be persisted, not only the
	// in-memory value used by the caller.
	if _, err := decodePublishIntentPayload(string(raw)); err != nil {
		return "", err
	}
	return string(raw), nil
}

func decodePublishIntentPayload(raw string) (publishIntentPayload, error) {
	var payload publishIntentPayload
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&payload); err != nil {
		return payload, fmt.Errorf("payload intent JSON non valido: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			err = errors.New("dati aggiuntivi dopo il JSON")
		}
		return payload, fmt.Errorf("payload intent JSON non valido: %w", err)
	}
	if err := validatePublishIntentPayload(payload); err != nil {
		return payload, err
	}
	return payload, nil
}

func validatePublishIntentPayload(payload publishIntentPayload) error {
	if payload.Version != publishIntentVersion {
		return fmt.Errorf("versione payload intent %d non supportata", payload.Version)
	}
	if !tokenPattern.MatchString(payload.TxID) || !tokenPattern.MatchString(payload.Device) {
		return errors.New("txid/device intent non validi")
	}
	if !runTSPattern.MatchString(payload.RunTS) {
		return errors.New("run_ts intent non valido")
	}
	if payload.Mode != "create" && payload.Mode != "replace" {
		return fmt.Errorf("modalità intent non valida: %q", payload.Mode)
	}
	if payload.TargetRevision <= 0 {
		return errors.New("target_revision intent non valido")
	}
	if len(payload.Ops) == 0 || len(payload.Ops) > 100000 {
		return errors.New("numero operazioni intent non valido")
	}
	if len(payload.Rows) != len(payload.Ops) {
		return errors.New("righe journal intent non corrispondono alle operazioni")
	}
	seen := make(map[string]bool, len(payload.Ops))
	for index, op := range payload.Ops {
		if !validRelative(op.Path) || seen[op.Path] {
			return fmt.Errorf("percorso intent non valido o duplicato: %q", op.Path)
		}
		seen[op.Path] = true
		if !digest64.MatchString(op.Digest) || len(op.Chunks) > 100000 {
			return fmt.Errorf("digest/chunk intent non validi per %q", op.Path)
		}
		for _, chunk := range op.Chunks {
			if !digest64.MatchString(chunk) {
				return fmt.Errorf("chunk intent non valido per %q", op.Path)
			}
		}
		if err := validatePublishContent(op.Digest, strconv.FormatInt(op.Size, 10), op.Chunks); err != nil {
			return fmt.Errorf("contenuto intent non valido per %q: %w", op.Path, err)
		}
		if op.Causal != "" && !causalPattern.MatchString(op.Causal) {
			return fmt.Errorf("causal intent non valido per %q", op.Path)
		}
		if op.BackupRelative != publishBackupRelative(op.Path, payload.RunTS) {
			return fmt.Errorf("backup intent non valido per %q", op.Path)
		}
		if op.Prior != nil {
			if op.Prior.Size < 0 || (op.Prior.Dev == 0 && op.Prior.Ino == 0) {
				return fmt.Errorf("stato precedente intent non valido per %q", op.Path)
			}
		}
		row := payload.Rows[index]
		if !strings.HasPrefix(row, "PUT\t") {
			return fmt.Errorf("riga journal intent non PUT per %q", op.Path)
		}
		entry, ok, err := parseJournalEntry(row, payload.TargetRevision)
		if err != nil {
			return fmt.Errorf("riga journal intent non valida per %q: %w", op.Path, err)
		}
		if !ok || entry.Deleted || entry.Path != op.Path || entry.Digest != op.Digest ||
			entry.Size != op.Size || entry.MTimeNS != op.MTimeNS || entry.Device != payload.Device ||
			entry.Causal != op.Causal {
			return fmt.Errorf("riga journal intent incoerente per %q", op.Path)
		}
	}
	return nil
}

func publishPriorFromInfo(info os.FileInfo) (*publishIntentFileState, error) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || (stat.Dev == 0 && stat.Ino == 0) {
		return nil, fmt.Errorf("identità filesystem non disponibile per %q", info.Name())
	}
	return &publishIntentFileState{
		Mode: uint32(info.Mode()), Size: info.Size(), MTimeNS: info.ModTime().UnixNano(),
		Dev: uint64(stat.Dev), Ino: stat.Ino,
	}, nil
}

func samePublishPrior(info os.FileInfo, prior *publishIntentFileState) bool {
	if info == nil || prior == nil {
		return false
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return false
	}
	return uint32(info.Mode()) == prior.Mode && info.Size() == prior.Size &&
		info.ModTime().UnixNano() == prior.MTimeNS &&
		uint64(stat.Dev) == prior.Dev && stat.Ino == prior.Ino
}

func samePublishOpContent(a, b publishIntentOp) bool {
	return a.Path == b.Path && a.Digest == b.Digest && a.Size == b.Size &&
		a.MTimeNS == b.MTimeNS && a.Causal == b.Causal &&
		a.BackupRelative == b.BackupRelative && slices.Equal(a.Chunks, b.Chunks)
}

func (payload publishIntentPayload) matchesRequest(request publishIntentRequest) bool {
	if payload.TxID != request.TxID || payload.Device != request.Device ||
		payload.RunTS != request.RunTS || payload.Mode != request.Mode ||
		len(payload.Ops) != len(request.Ops) {
		return false
	}
	for index := range payload.Ops {
		if !samePublishOpContent(payload.Ops[index], request.Ops[index]) {
			return false
		}
	}
	return true
}

func (c *Context) validatePublishIntentPaths(payload publishIntentPayload) error {
	for _, op := range payload.Ops {
		if err := verifyPublishPath(c.ShareRoot, op.Path); err != nil {
			return fmt.Errorf("percorso target intent non sicuro per %q: %w", op.Path, err)
		}
		if err := verifyPublishPath(c.ShareRoot, op.BackupRelative); err != nil {
			return fmt.Errorf("percorso backup intent non sicuro per %q: %w", op.Path, err)
		}
	}
	return nil
}

// recoverTxIntentsLocked is called only while the global transfer lease and the
// journal lock are held. A journal commit always wins over the marker state.
func (c *Context) recoverTxIntentsLocked(device string, expected map[string]publishIntentRequest) error {
	if err := c.requireLease(device); err != nil {
		return mapLeaseError(err)
	}
	store, err := openTxIntentStore(c.paths())
	if err != nil {
		return err
	}
	defer store.Close()
	records, err := store.list()
	if err != nil {
		return err
	}
	for _, record := range records {
		payload, err := decodePublishIntentPayload(record.Payload)
		if err != nil {
			return fmt.Errorf("recovery intent %s: %w", record.TxID, err)
		}
		if record.TxID != payload.TxID || record.TargetRevision != payload.TargetRevision {
			return fmt.Errorf("recovery intent %s: metadati SQLite e payload incoerenti", record.TxID)
		}
		committed, err := c.transactionCommitted(record.TxID)
		if err != nil {
			return fmt.Errorf("recovery intent %s, verifica commit: %w", record.TxID, err)
		}
		if committed {
			if err := c.finalizeCommittedTxIntentLocked(store, record, payload); err != nil {
				return fmt.Errorf("recovery intent %s già commesso: %w", record.TxID, err)
			}
			continue
		}
		if err := c.validatePublishIntentPaths(payload); err != nil {
			return err
		}
		if request, ok := expected[record.TxID]; ok && !payload.matchesRequest(request) {
			return fmt.Errorf("recovery intent %s: retry txid con payload diverso", record.TxID)
		}
		switch record.Status {
		case txIntentPrepared:
			if err := c.rollbackPreparedTxIntentLocked(store, record, payload); err != nil {
				return fmt.Errorf("recovery PREPARED %s: %w", record.TxID, err)
			}
		case txIntentReady:
			if err := c.rollForwardReadyTxIntentLocked(store, record, payload); err != nil {
				return fmt.Errorf("recovery READY %s: %w", record.TxID, err)
			}
		case txIntentCommitted:
			// COMMITTED is written only after a commit was observed. Missing
			// evidence is ambiguous and must never trigger a filesystem rollback.
			return fmt.Errorf("recovery intent %s COMMITTED senza commit nel journal", record.TxID)
		default:
			return fmt.Errorf("recovery intent %s con stato sconosciuto %q", record.TxID, record.Status)
		}
	}
	return nil
}

func (c *Context) finalizeCommittedTxIntentLocked(store *txIntentStore, record txIntentRecord, payload publishIntentPayload) error {
	if err := store.updateStatus(record.TxID, record.Payload, "", txIntentCommitted); err != nil {
		return err
	}
	current, err := c.journalRevision()
	if err != nil {
		return err
	}
	if current < payload.TargetRevision {
		if err := c.writeRevision(payload.TargetRevision); err != nil {
			return fmt.Errorf("riparazione revisione: %w", err)
		}
	}
	index, err := openMetadataIndex(c.paths())
	if err != nil {
		return fmt.Errorf("sync metadata: %w", err)
	}
	defer index.Close()
	if err := index.syncLocked(); err != nil {
		return fmt.Errorf("sync metadata: %w", err)
	}
	if err := store.remove(record.TxID, record.Payload); err != nil {
		return fmt.Errorf("cleanup intent: %w", err)
	}
	if err := c.compactIfNeeded(index); err != nil {
		// Commit, revision, index and marker cleanup are complete. Compaction is
		// maintenance and must not turn a committed publish into a failure.
		fmt.Fprintf(os.Stderr, "compattazione journal non riuscita: %v\n", err)
	}
	return nil
}

func optionalLstat(path string) (os.FileInfo, error) {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	return info, err
}

func (c *Context) rollbackPreparedTxIntentLocked(store *txIntentStore, record txIntentRecord, payload publishIntentPayload) error {
	if err := c.validatePublishIntentPaths(payload); err != nil {
		return err
	}
	for index := len(payload.Ops) - 1; index >= 0; index-- {
		op := payload.Ops[index]
		target := filepath.Join(c.ShareRoot, op.Path)
		backup := filepath.Join(c.ShareRoot, op.BackupRelative)
		targetInfo, err := optionalLstat(target)
		if err != nil {
			return err
		}
		backupInfo, err := optionalLstat(backup)
		if err != nil {
			return err
		}
		if op.Prior == nil {
			if backupInfo != nil {
				return fmt.Errorf("rollback %s: backup inatteso per un target nuovo", op.Path)
			}
			if targetInfo == nil {
				continue
			}
			if !publishTargetBytesMatch(targetInfo, target, op) {
				return fmt.Errorf("rollback %s: target nuovo non verificabile", op.Path)
			}
			if err := os.Remove(target); err != nil {
				return fmt.Errorf("rollback %s: %w", op.Path, err)
			}
			continue
		}
		if backupInfo != nil {
			if !samePublishPrior(backupInfo, op.Prior) {
				return fmt.Errorf("rollback %s: backup non corrisponde al target precedente", op.Path)
			}
			if targetInfo != nil {
				if !publishTargetBytesMatch(targetInfo, target, op) {
					return fmt.Errorf("rollback %s: target pubblicato non verificabile", op.Path)
				}
				if err := os.Remove(target); err != nil {
					return fmt.Errorf("rollback %s: %w", op.Path, err)
				}
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return fmt.Errorf("rollback %s: %w", op.Path, err)
			}
			if err := os.Rename(backup, target); err != nil {
				return fmt.Errorf("rollback %s: %w", op.Path, err)
			}
			continue
		}
		// No backup means either the first rename never happened or a previous
		// recovery already restored it. Both are safe only if the target still
		// has the exact pre-publish identity.
		if targetInfo == nil || !samePublishPrior(targetInfo, op.Prior) {
			return fmt.Errorf("rollback %s: target precedente assente o modificato senza backup", op.Path)
		}
	}
	// Also flush no-op branches: a crash may have happened after remove/rename
	// but before its directory fsync, and the retry must finish that durability.
	if err := c.syncPublishDirectories(payload); err != nil {
		return err
	}
	return store.remove(record.TxID, record.Payload)
}

func publishTargetBytesMatch(info os.FileInfo, target string, op publishIntentOp) bool {
	if info == nil || !info.Mode().IsRegular() || info.Size() != op.Size {
		return false
	}
	digest, err := sha256File(target)
	return err == nil && digest == op.Digest
}

func publishTargetExactMatch(info os.FileInfo, target string, op publishIntentOp) bool {
	if !publishTargetBytesMatch(info, target, op) || info.ModTime().UnixNano() != op.MTimeNS {
		return false
	}
	return true
}

func (c *Context) rollForwardReadyTxIntentLocked(store *txIntentStore, record txIntentRecord, payload publishIntentPayload) error {
	if err := c.validatePublishIntentPaths(payload); err != nil {
		return err
	}
	for _, op := range payload.Ops {
		target := filepath.Join(c.ShareRoot, op.Path)
		backup := filepath.Join(c.ShareRoot, op.BackupRelative)
		targetInfo, err := optionalLstat(target)
		if err != nil {
			return err
		}
		if !publishTargetExactMatch(targetInfo, target, op) {
			return fmt.Errorf("roll-forward %s: target READY assente o non verificabile", op.Path)
		}
		backupInfo, err := optionalLstat(backup)
		if err != nil {
			return err
		}
		if op.Prior == nil {
			if backupInfo != nil {
				return fmt.Errorf("roll-forward %s: backup inatteso", op.Path)
			}
		} else if backupInfo == nil || !samePublishPrior(backupInfo, op.Prior) {
			return fmt.Errorf("roll-forward %s: backup precedente assente o non verificabile", op.Path)
		}
	}
	if err := c.savePublishMetadataAndSync(payload); err != nil {
		return err
	}
	return c.appendReadyTxIntentLocked(store, record, payload)
}

func (c *Context) executePreparedTxIntentLocked(payload publishIntentPayload) error {
	store, err := openTxIntentStore(c.paths())
	if err != nil {
		return err
	}
	defer store.Close()
	raw, err := store.prepare(payload.TxID, payload)
	if err != nil {
		return err
	}
	record := txIntentRecord{
		TxID: payload.TxID, Status: txIntentPrepared,
		Payload: raw, TargetRevision: payload.TargetRevision,
	}
	runTxIntentFailpoint("prepared", payload.TxID)
	if err := c.applyPublishOpsLocked(payload); err != nil {
		committed, checkErr := c.transactionCommitted(payload.TxID)
		if checkErr != nil {
			return errors.Join(err, fmt.Errorf("verifica commit dopo errore PREPARED: %w", checkErr))
		}
		if committed {
			return c.finalizeCommittedTxIntentLocked(store, record, payload)
		}
		if rollbackErr := c.rollbackPreparedTxIntentLocked(store, record, payload); rollbackErr != nil {
			return errors.Join(err, fmt.Errorf("rollback PREPARED: %w", rollbackErr))
		}
		return err
	}
	if err := c.savePublishMetadataAndSync(payload); err != nil {
		if rollbackErr := c.rollbackPreparedTxIntentLocked(store, record, payload); rollbackErr != nil {
			return errors.Join(err, fmt.Errorf("rollback PREPARED: %w", rollbackErr))
		}
		return err
	}
	if err := store.updateStatus(payload.TxID, record.Payload, txIntentPrepared, txIntentReady); err != nil {
		// The SQLite update may have committed even if it returned an error.
		// Never infer that rollback is safe from an ambiguous status write.
		return fmt.Errorf("promozione intent %s a READY: %w", payload.TxID, err)
	}
	record.Status = txIntentReady
	runTxIntentFailpoint("ready", payload.TxID)
	return c.appendReadyTxIntentLocked(store, record, payload)
}

func (c *Context) applyPublishOpsLocked(payload publishIntentPayload) error {
	if err := c.validatePublishIntentPaths(payload); err != nil {
		return err
	}
	store, err := c.chunkStore()
	if err != nil {
		return err
	}
	for _, op := range payload.Ops {
		target := filepath.Join(c.ShareRoot, op.Path)
		backup := filepath.Join(c.ShareRoot, op.BackupRelative)
		targetInfo, err := optionalLstat(target)
		if err != nil {
			return err
		}
		backupInfo, err := optionalLstat(backup)
		if err != nil {
			return err
		}
		if backupInfo != nil {
			return fmt.Errorf("backup già esistente: %s", op.BackupRelative)
		}
		if op.Prior == nil {
			if targetInfo != nil {
				return fmt.Errorf("target già esistente: %s", op.Path)
			}
		} else if !samePublishPrior(targetInfo, op.Prior) {
			return fmt.Errorf("target modificato dopo PREPARED: %s", op.Path)
		}
		if op.Prior != nil {
			if err := os.MkdirAll(filepath.Dir(backup), 0o755); err != nil {
				return err
			}
			if err := os.Rename(target, backup); err != nil {
				return err
			}
			runTxIntentFailpoint("after_backup", payload.TxID)
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		if err := store.Assemble(op.Chunks, target); err != nil {
			return err
		}
		runTxIntentFailpoint("after_assemble", payload.TxID)
		stamp := time.Unix(0, op.MTimeNS)
		if err := os.Chtimes(target, stamp, stamp); err != nil {
			return err
		}
		info, err := os.Lstat(target)
		if err != nil {
			return err
		}
		if !publishTargetExactMatch(info, target, op) {
			return exitCode(1)
		}
	}
	return nil
}

func (c *Context) savePublishMetadataAndSync(payload publishIntentPayload) error {
	store, err := c.chunkStore()
	if err != nil {
		return err
	}
	for _, op := range payload.Ops {
		if err := store.SaveFileMeta(op.Digest, op.Chunks); err != nil {
			return err
		}
		metaPath := store.fileMetaPath(op.Digest)
		if err := syncFile(metaPath); err != nil {
			return err
		}
		if err := syncDir(filepath.Dir(metaPath)); err != nil {
			return err
		}
	}
	return c.syncPublishFilesystem(payload)
}

func (c *Context) syncPublishFilesystem(payload publishIntentPayload) error {
	for _, op := range payload.Ops {
		if err := syncFile(filepath.Join(c.ShareRoot, op.Path)); err != nil {
			return err
		}
	}
	return c.syncPublishDirectories(payload)
}

func (c *Context) syncPublishDirectories(payload publishIntentPayload) error {
	root, err := filepath.Abs(c.ShareRoot)
	if err != nil {
		return err
	}
	dirs := map[string]bool{}
	addTree := func(relative string) {
		dir := filepath.Dir(filepath.Join(root, relative))
		for {
			dirs[dir] = true
			if dir == root {
				return
			}
			parent := filepath.Dir(dir)
			if parent == dir {
				return
			}
			dir = parent
		}
	}
	for _, op := range payload.Ops {
		addTree(op.Path)
		addTree(op.BackupRelative)
	}
	ordered := make([]string, 0, len(dirs))
	for dir := range dirs {
		ordered = append(ordered, dir)
	}
	// Sync children before their parents so every newly created ancestor is
	// covered by a directory fsync, not only the file's immediate parent.
	sort.Slice(ordered, func(i, j int) bool {
		if len(ordered[i]) != len(ordered[j]) {
			return len(ordered[i]) > len(ordered[j])
		}
		return ordered[i] < ordered[j]
	})
	for _, dir := range ordered {
		if err := syncDir(dir); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

func (c *Context) appendReadyTxIntentLocked(store *txIntentStore, record txIntentRecord, payload publishIntentPayload) error {
	err := c.appendTransactionLockedAtRevision(payload.TxID, payload.Device, payload.Rows, payload.TargetRevision)
	if err == nil {
		return c.finalizeCommittedTxIntentLocked(store, record, payload)
	}
	committed, checkErr := c.transactionCommitted(payload.TxID)
	if checkErr != nil {
		return errors.Join(err, fmt.Errorf("verifica commit dopo errore READY: %w", checkErr))
	}
	if committed {
		return c.finalizeCommittedTxIntentLocked(store, record, payload)
	}
	// READY is the point of no return. Leave the marker and files in place so a
	// later lease holder can finish the journal append; never roll back here.
	return err
}
