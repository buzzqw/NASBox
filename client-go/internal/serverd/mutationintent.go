package serverd

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Mutation intents cover the operations that used to have only a synchronous
// rollback: delete, rename and restore. The intent is durable before the
// filesystem move, so a crash can be resolved on the next lease acquisition.
// Publish intents keep their existing payload and recovery path.
const (
	mutationIntentVersion = 1
	mutationKindDelete    = "delete"
	mutationKindRename    = "rename"
	mutationKindRestore   = "restore"
)

type mutationIntentOp struct {
	// Delete uses Path. Rename uses Src/Dst. Restore uses Src (version in
	// history) and Dst (live target).
	Path   string                  `json:"path,omitempty"`
	Src    string                  `json:"src,omitempty"`
	Dst    string                  `json:"dst,omitempty"`
	Backup string                  `json:"backup,omitempty"`
	Prior  *publishIntentFileState `json:"prior,omitempty"`
	// Restore records the bytes that must appear at Dst.
	Digest  string `json:"digest,omitempty"`
	Size    int64  `json:"size,omitempty"`
	MTimeNS int64  `json:"mtime_ns,omitempty"`
	// Rows belong to this op. A checked-delete batch can therefore recover
	// only the files that were actually moved.
	Rows []string `json:"rows,omitempty"`
}

type mutationIntentPayload struct {
	Version int                `json:"version"`
	TxID    string             `json:"txid"`
	Device  string             `json:"device"`
	RunTS   string             `json:"run_ts"`
	Kind    string             `json:"kind"`
	Ops     []mutationIntentOp `json:"ops"`
}

type mutationIntentRecord struct {
	TxID      string
	Kind      string
	Status    string
	Payload   string
	CreatedAt int64
	UpdatedAt int64
}

// mutationIntent is the live handle used by a mutating command.
type mutationIntent struct {
	store  *txIntentStore
	txid   string
	raw    string
	status string
}

func (m *mutationIntent) close() {
	if m != nil && m.store != nil {
		_ = m.store.Close()
	}
}

func (m *mutationIntent) finish() error {
	if m == nil || m.store == nil {
		return nil
	}
	return m.store.removeMutation(m.txid, m.raw)
}

func (m *mutationIntent) markCommitted() error {
	if m == nil || m.store == nil {
		return nil
	}
	return m.store.updateMutationStatus(m.txid, m.raw, m.status, txIntentCommitted)
}

func marshalMutationIntentPayload(payload mutationIntentPayload) (string, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	if _, err := decodeMutationIntentPayload(string(raw)); err != nil {
		return "", err
	}
	return string(raw), nil
}

func decodeMutationIntentPayload(raw string) (mutationIntentPayload, error) {
	var payload mutationIntentPayload
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&payload); err != nil {
		return payload, fmt.Errorf("payload mutation intent JSON non valido: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			err = errors.New("dati aggiuntivi dopo il JSON")
		}
		return payload, fmt.Errorf("payload mutation intent JSON non valido: %w", err)
	}
	if err := validateMutationIntentPayload(payload); err != nil {
		return payload, err
	}
	return payload, nil
}

func validateMutationIntentPayload(payload mutationIntentPayload) error {
	if payload.Version != mutationIntentVersion {
		return fmt.Errorf("versione mutation intent %d non supportata", payload.Version)
	}
	if !tokenPattern.MatchString(payload.TxID) || !tokenPattern.MatchString(payload.Device) {
		return errors.New("txid/device mutation intent non validi")
	}
	if !runTSPattern.MatchString(payload.RunTS) {
		return errors.New("run_ts mutation intent non valido")
	}
	switch payload.Kind {
	case mutationKindDelete, mutationKindRename, mutationKindRestore:
	default:
		return fmt.Errorf("tipo mutation intent non valido: %q", payload.Kind)
	}
	if len(payload.Ops) == 0 || len(payload.Ops) > 100000 {
		return errors.New("numero operazioni mutation intent non valido")
	}
	for _, op := range payload.Ops {
		switch payload.Kind {
		case mutationKindDelete:
			if !validRelative(op.Path) || !validIntentPath(op.Backup) ||
				!strings.HasPrefix(op.Backup, ".sync-trash/") {
				return fmt.Errorf("percorso delete intent non valido: %q", op.Path)
			}
			if op.Prior == nil || op.Prior.Size < 0 || (op.Prior.Dev == 0 && op.Prior.Ino == 0) {
				return fmt.Errorf("stato precedente delete intent non valido: %q", op.Path)
			}
			if !allJournalRows(op.Rows, "DELETE\t") {
				return fmt.Errorf("righe delete intent non valide: %q", op.Path)
			}
		case mutationKindRename:
			if !validRelative(op.Src) || !validRelative(op.Dst) || op.Src == op.Dst {
				return fmt.Errorf("percorso rename intent non valido: %q -> %q", op.Src, op.Dst)
			}
			if op.Prior == nil || op.Prior.Size < 0 || (op.Prior.Dev == 0 && op.Prior.Ino == 0) {
				return fmt.Errorf("stato precedente rename intent non valido: %q", op.Src)
			}
			if !allJournalRows(op.Rows, "DELETE\t") && !allJournalRows(op.Rows, "PUT\t") {
				// Rename rows are DELETE + PUT; validate each prefix below.
				for _, row := range op.Rows {
					if !strings.HasPrefix(row, "DELETE\t") && !strings.HasPrefix(row, "PUT\t") {
						return fmt.Errorf("righe rename intent non valide: %q", op.Src)
					}
				}
			}
		case mutationKindRestore:
			if !validIntentPath(op.Src) || !validRelative(op.Dst) ||
				!strings.HasPrefix(op.Src, ".sync-trash/") {
				return fmt.Errorf("percorso restore intent non valido: %q -> %q", op.Src, op.Dst)
			}
			if op.Backup != "" && (!validIntentPath(op.Backup) || !strings.HasPrefix(op.Backup, ".sync-trash/")) {
				return fmt.Errorf("backup restore intent non valido: %q", op.Backup)
			}
			if !digest64.MatchString(op.Digest) || op.Size < 0 || op.MTimeNS < 0 {
				return fmt.Errorf("contenuto restore intent non valido: %q", op.Dst)
			}
			if op.Prior != nil && (op.Prior.Size < 0 || (op.Prior.Dev == 0 && op.Prior.Ino == 0)) {
				return fmt.Errorf("stato precedente restore intent non valido: %q", op.Dst)
			}
			if !allJournalRows(op.Rows, "PUT\t") || len(op.Rows) != 1 {
				return fmt.Errorf("righe restore intent non valide: %q", op.Dst)
			}
		}
	}
	return nil
}

// validIntentPath accepts internal history paths (.sync-trash/...) that
// validRelative intentionally rejects for live manifest entries.
func validIntentPath(path string) bool {
	if path == "" || filepath.IsAbs(path) {
		return false
	}
	for _, component := range strings.Split(path, "/") {
		if component == "" || component == "." || component == ".." {
			return false
		}
	}
	return true
}

func allJournalRows(rows []string, prefix string) bool {
	for _, row := range rows {
		if !strings.HasPrefix(row, prefix) {
			return false
		}
	}
	return true
}

// prepareMutationIntent durably records the operation before any rename/remove.
func (c *Context) prepareMutationIntent(payload mutationIntentPayload) (*mutationIntent, error) {
	raw, err := marshalMutationIntentPayload(payload)
	if err != nil {
		return nil, err
	}
	store, err := openTxIntentStore(c.paths())
	if err != nil {
		return nil, err
	}
	now := time.Now().Unix()
	tx, err := store.db.Begin()
	if err != nil {
		store.Close()
		return nil, err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`
		INSERT INTO mutation_intent(txid, kind, status, payload, created_at, updated_at)
		VALUES(?, ?, ?, ?, ?, ?)
	`, payload.TxID, payload.Kind, txIntentPrepared, raw, now, now); err != nil {
		store.Close()
		return nil, fmt.Errorf("creazione mutation intent %s: %w", payload.TxID, err)
	}
	if err := tx.Commit(); err != nil {
		store.Close()
		return nil, err
	}
	return &mutationIntent{store: store, txid: payload.TxID, raw: raw, status: txIntentPrepared}, nil
}

func (s *txIntentStore) listMutations() ([]mutationIntentRecord, error) {
	rows, err := s.db.Query(`
		SELECT txid, kind, status, payload, created_at, updated_at
		FROM mutation_intent ORDER BY created_at, txid
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var records []mutationIntentRecord
	for rows.Next() {
		var record mutationIntentRecord
		if err := rows.Scan(&record.TxID, &record.Kind, &record.Status, &record.Payload,
			&record.CreatedAt, &record.UpdatedAt); err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	return records, rows.Err()
}

func (s *txIntentStore) updateMutationStatus(txid, payload, from, to string) error {
	query := `UPDATE mutation_intent SET status=?, updated_at=? WHERE txid=? AND payload=?`
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
	var status, stored string
	err = s.db.QueryRow(`SELECT status, payload FROM mutation_intent WHERE txid=?`, txid).Scan(&status, &stored)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if stored == payload && status == to {
		return nil
	}
	return fmt.Errorf("mutation intent %s non è passabile da %s a %s", txid, from, to)
}

func (s *txIntentStore) removeMutation(txid, payload string) error {
	result, err := s.db.Exec(`DELETE FROM mutation_intent WHERE txid=? AND payload=?`, txid, payload)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		var stored string
		err := s.db.QueryRow(`SELECT payload FROM mutation_intent WHERE txid=?`, txid).Scan(&stored)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if stored != payload {
			return fmt.Errorf("payload mutation intent %s non corrispondente durante cleanup", txid)
		}
	}
	return nil
}

// pendingRecovery is a read-only count shown by status/doctor. It never
// creates the database when no intent has ever been written.
func (c *Context) pendingRecovery() (int, error) {
	path := c.paths().TxIntent
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return 0, nil
	} else if err != nil {
		return 0, err
	}
	store, err := openTxIntentStore(c.paths())
	if err != nil {
		return 0, err
	}
	defer store.Close()
	publish, err := store.list()
	if err != nil {
		return 0, err
	}
	mutations, err := store.listMutations()
	if err != nil {
		return 0, err
	}
	pending := 0
	for _, record := range publish {
		if record.Status != txIntentCommitted {
			pending++
		}
	}
	for _, record := range mutations {
		if record.Status != txIntentCommitted {
			pending++
		}
	}
	return pending, nil
}

// recoverMutationIntentsLocked is called under the global lease and the
// journal lock. It finishes (or discards) every mutation whose filesystem
// effect is known, and refuses to guess when the state is ambiguous.
func (c *Context) recoverMutationIntentsLocked(device string) error {
	store, err := openTxIntentStore(c.paths())
	if err != nil {
		return err
	}
	defer store.Close()
	records, err := store.listMutations()
	if err != nil {
		return err
	}
	for _, record := range records {
		payload, err := decodeMutationIntentPayload(record.Payload)
		if err != nil {
			return fmt.Errorf("recovery mutation %s: %w", record.TxID, err)
		}
		if record.TxID != payload.TxID || record.Kind != payload.Kind {
			return fmt.Errorf("recovery mutation %s: metadati SQLite e payload incoerenti", record.TxID)
		}
		committed, err := c.transactionCommitted(record.TxID)
		if err != nil {
			return fmt.Errorf("recovery mutation %s, verifica commit: %w", record.TxID, err)
		}
		if committed {
			if err := store.updateMutationStatus(record.TxID, record.Payload, "", txIntentCommitted); err != nil {
				return err
			}
			// The journal is authoritative. Sync the derived index, which also
			// repairs manifest.revision if the crash landed after the journal
			// fsync but before the cursor bump.
			index, err := openMetadataIndex(c.paths())
			if err != nil {
				return err
			}
			if err := index.syncLocked(); err != nil {
				index.Close()
				return err
			}
			if err := index.Close(); err != nil {
				return err
			}
			if err := store.removeMutation(record.TxID, record.Payload); err != nil {
				return err
			}
			continue
		}
		if record.Status == txIntentCommitted {
			return fmt.Errorf("recovery mutation %s COMMITTED senza commit nel journal", record.TxID)
		}
		rows, err := c.recoverMutationOpsLocked(payload)
		if err != nil {
			return fmt.Errorf("recovery mutation %s: %w", record.TxID, err)
		}
		if len(rows) > 0 {
			if err := c.appendTransactionLocked(payload.TxID, payload.Device, rows); err != nil {
				return err
			}
		}
		if err := store.removeMutation(record.TxID, record.Payload); err != nil {
			return err
		}
	}
	return nil
}

// recoverMutationOpsLocked inspects every op and returns the journal rows that
// must still be committed. It returns no rows when no filesystem move happened.
func (c *Context) recoverMutationOpsLocked(payload mutationIntentPayload) ([]string, error) {
	var rows []string
	for _, op := range payload.Ops {
		moved, err := c.mutationOpMovedLocked(payload.Kind, op)
		if err != nil {
			return nil, err
		}
		if moved {
			rows = append(rows, op.Rows...)
		}
	}
	return rows, nil
}

func (c *Context) mutationOpMovedLocked(kind string, op mutationIntentOp) (bool, error) {
	switch kind {
	case mutationKindDelete:
		src, err := optionalLstat(filepath.Join(c.ShareRoot, op.Path))
		if err != nil {
			return false, err
		}
		backup, err := optionalLstat(filepath.Join(c.ShareRoot, op.Backup))
		if err != nil {
			return false, err
		}
		switch {
		case src == nil && backup != nil && samePublishPrior(backup, op.Prior):
			return true, nil
		case src != nil && backup == nil && samePublishPrior(src, op.Prior):
			return false, nil
		default:
			return false, fmt.Errorf("delete %s: stato filesystem ambiguo (sorgente=%v backup=%v)", op.Path, src != nil, backup != nil)
		}
	case mutationKindRename:
		src, err := optionalLstat(filepath.Join(c.ShareRoot, op.Src))
		if err != nil {
			return false, err
		}
		dst, err := optionalLstat(filepath.Join(c.ShareRoot, op.Dst))
		if err != nil {
			return false, err
		}
		switch {
		case src == nil && dst != nil && samePublishPrior(dst, op.Prior):
			return true, nil
		case src != nil && dst == nil && samePublishPrior(src, op.Prior):
			return false, nil
		default:
			return false, fmt.Errorf("rename %s -> %s: stato filesystem ambiguo (sorgente=%v destinazione=%v)", op.Src, op.Dst, src != nil, dst != nil)
		}
	case mutationKindRestore:
		src, err := optionalLstat(filepath.Join(c.ShareRoot, op.Src))
		if err != nil {
			return false, err
		}
		target := filepath.Join(c.ShareRoot, op.Dst)
		targetInfo, err := optionalLstat(target)
		if err != nil {
			return false, err
		}
		var backupInfo os.FileInfo
		if op.Backup != "" {
			backupInfo, err = optionalLstat(filepath.Join(c.ShareRoot, op.Backup))
			if err != nil {
				return false, err
			}
		}
		targetMatches := func() bool {
			matches, err := restoreFileMatches(target, restoreFingerprint{
				digest: op.Digest, size: strconv.FormatInt(op.Size, 10), mtimeNS: strconv.FormatInt(op.MTimeNS, 10),
			})
			return err == nil && matches
		}
		switch {
		case src == nil && targetInfo != nil && targetMatches():
			if op.Prior != nil && (backupInfo == nil || !samePublishPrior(backupInfo, op.Prior)) {
				return false, fmt.Errorf("restore %s: backup precedente assente o non verificabile", op.Dst)
			}
			if op.Prior == nil && backupInfo != nil {
				return false, fmt.Errorf("restore %s: backup inatteso", op.Dst)
			}
			return true, nil
		case src != nil && op.Prior != nil && targetInfo != nil && samePublishPrior(targetInfo, op.Prior):
			// The link/rename never happened; remove the hard-link backup if
			// it was created, but only when it is still the same inode.
			if backupInfo != nil && samePublishPrior(backupInfo, op.Prior) {
				if err := os.Remove(filepath.Join(c.ShareRoot, op.Backup)); err != nil && !os.IsNotExist(err) {
					return false, err
				}
			}
			return false, nil
		case src != nil && op.Prior == nil && targetInfo == nil && backupInfo == nil:
			return false, nil
		default:
			return false, fmt.Errorf("restore %s: stato filesystem ambiguo", op.Dst)
		}
	default:
		return false, fmt.Errorf("tipo mutation intent sconosciuto: %q", kind)
	}
}
