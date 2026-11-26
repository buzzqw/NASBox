package serverd

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

func readNULFields(r io.Reader) ([]string, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	parts := strings.Split(string(data), "\x00")
	if len(parts) > 0 && parts[len(parts)-1] == "" {
		parts = parts[:len(parts)-1]
	}
	return parts, nil
}

func nulField(value string) string { return value + "\x00" }

func journalNow() string { return strconv.FormatInt(time.Now().Unix(), 10) }

func (c *Context) withJournalLock(fn func() error) error {
	p := c.paths()
	file, err := os.OpenFile(p.JournalLock, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return err
	}
	defer file.Close()
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
	return fn()
}

func journalContainsCommit(journal []byte, txid string) bool {
	return strings.Contains(string(journal), "COMMIT\t"+txid+"\n")
}

// appendTransaction writes one BEGIN/.../COMMIT block and bumps the manifest
// revision exactly once. It is the shared primitive for every mutating verb.
func (c *Context) appendTransaction(txid, device string, rows []string) error {
	return c.withJournalLock(func() error {
		return c.appendTransactionLocked(txid, device, rows)
	})
}

// appendTransactionLocked is the same operation for callers that already hold
// the journal lock (all mutating verbs do). Never acquire the global lock here.
func (c *Context) appendTransactionLocked(txid, device string, rows []string) error {
	return c.appendTransactionLockedOptions(txid, device, rows, 0, true)
}

// appendTransactionLockedAtRevision appends a recovered READY publication at
// its original revision. Compaction is deferred until the intent marker has
// been finalized, otherwise the commit evidence could disappear before cleanup.
func (c *Context) appendTransactionLockedAtRevision(txid, device string, rows []string, targetRevision int64) error {
	if targetRevision <= 0 {
		return errors.New("target revision non valido")
	}
	return c.appendTransactionLockedOptions(txid, device, rows, targetRevision, false)
}

func (c *Context) appendTransactionLockedOptions(txid, device string, rows []string, targetRevision int64, compact bool) error {
	p := c.paths()
	index, indexErr := openMetadataIndex(p)
	if indexErr == nil {
		defer index.Close()
		indexErr = index.syncLocked()
	}
	if indexErr != nil {
		// The SQLite index is derived. If it cannot be opened or caught up,
		// preserve the legacy journal path instead of blocking a mutation.
		if index != nil {
			_ = index.Close()
			index = nil
		}
		journal, err := os.ReadFile(p.Journal)
		if err != nil {
			return err
		}
		if journalContainsCommit(journal, txid) {
			// A full COMMIT may be visible even when the original fsync returned
			// an ambiguous error. Make it durable before reporting idempotent
			// success to recovery.
			return syncFile(p.Journal)
		}
	} else {
		exists, err := index.transactionExists(txid)
		if err != nil {
			return err
		}
		if exists {
			return syncFile(p.Journal)
		}
	}

	current, err := revisionFromFile(p.Revision)
	if err != nil {
		return err
	}
	recovered := targetRevision > 0
	if targetRevision == 0 {
		if current == math.MaxInt64 {
			return errors.New("manifest.revision ha raggiunto il limite")
		}
		targetRevision = current + 1
	} else if current >= targetRevision {
		return fmt.Errorf("revisione %d non disponibile per txid %s (corrente %d)", targetRevision, txid, current)
	} else if current+1 != targetRevision {
		return fmt.Errorf("revisione target %d incoerente per txid %s (corrente %d)", targetRevision, txid, current)
	}
	// Catch deterministic failures before a journal block can become durable.
	// The append path repeats this check defensively.
	if err := preflightRevisionWrite(p.Revision); err != nil {
		return err
	}

	info, err := os.Stat(p.Journal)
	if err != nil {
		return err
	}
	oldSize := info.Size()
	if recovered {
		// A process may have died halfway through WriteString. Only an incomplete
		// trailing transaction is removed; complete fsynced commits are retained.
		oldSize, err = truncateIncompleteJournalTail(p.Journal)
		if err != nil {
			return err
		}
	}
	lines := []string{"BEGIN\t" + txid + "\t" + journalNow() + "\t" + device}
	lines = append(lines, rows...)
	lines = append(lines, "COMMIT\t"+txid)
	block := strings.Join(lines, "\n") + "\n"

	file, err := os.OpenFile(p.Journal, os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	n, writeErr := file.WriteString(block)
	if writeErr == nil && n != len(block) {
		writeErr = io.ErrShortWrite
	}
	if writeErr != nil && n != len(block) {
		_ = file.Close()
		// No fsync was attempted, so removing a partial append cannot discard a
		// successfully persisted commit.
		_ = os.Truncate(p.Journal, oldSize)
		_ = syncFile(p.Journal)
		return writeErr
	}
	syncErr := file.Sync()
	closeErr := file.Close()
	if syncErr != nil {
		// The complete block may be visible even though fsync failed. Preserve
		// it: READY recovery must decide from commit evidence, never roll back.
		return syncErr
	}
	if closeErr != nil {
		return closeErr
	}
	runTxIntentFailpoint("journal_committed", txid)
	if err := c.writeRevision(targetRevision); err != nil {
		// The journal fsync succeeded. Never truncate the commit just because
		// publishing the revision cursor failed; recovery repairs the cursor.
		return err
	}
	if index != nil {
		if err := index.syncLocked(); err != nil {
			// The journal and revision are already committed. Keep the mutation
			// successful and let the next reader rebuild/catch up the index.
			fmt.Fprintf(os.Stderr, "metadata sqlite non aggiornato: %v\n", err)
			index = nil
		}
	}
	if compact {
		if err := c.compactIfNeeded(index); err != nil {
			// Journal and revision are already durable. Compaction is maintenance:
			// reporting it as a failed mutation would make callers roll back files
			// while the metadata still records the operation as committed.
			fmt.Fprintf(os.Stderr, "compattazione journal non riuscita: %v\n", err)
		}
	}
	return nil
}

func preflightRevisionWrite(path string) error {
	tmp := path + ".tmp." + strconv.Itoa(os.Getpid())
	info, err := os.Lstat(tmp)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("percorso revisione temporaneo non scrivibile: %s", tmp)
	}
	return nil
}

// truncateIncompleteJournalTail removes only bytes after the last complete
// BEGIN/rows/COMMIT block. It is used by READY roll-forward after a crash during
// the original append; a successfully fsynced COMMIT is never truncated.
func truncateIncompleteJournalTail(path string) (int64, error) {
	_, completeEnd, err := readJournalCommits(path, 0)
	if err != nil {
		return 0, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return 0, err
	}
	if completeEnd == info.Size() {
		return info.Size(), nil
	}
	if completeEnd == 0 {
		file, err := os.Open(path)
		if err != nil {
			return 0, err
		}
		header := make([]byte, len("NASBOX_JOURNAL_V1\n"))
		n, readErr := io.ReadFull(file, header)
		_ = file.Close()
		if readErr == nil && n == len(header) && string(header) == "NASBOX_JOURNAL_V1\n" {
			completeEnd = int64(len(header))
		}
	}
	if completeEnd < 0 || completeEnd > info.Size() {
		return 0, fmt.Errorf("offset journal incompleto fuori intervallo")
	}
	if err := os.Truncate(path, completeEnd); err != nil {
		return 0, err
	}
	if err := syncFile(path); err != nil {
		return 0, err
	}
	return completeEnd, nil
}

func (c *Context) bumpRevision() error {
	current, err := revisionFromFile(c.paths().Revision)
	if err != nil || current < 0 {
		current = 0
	}
	return c.writeRevision(current + 1)
}

func (c *Context) writeRevision(revision int64) error {
	return writeRevisionFile(c.paths().Revision, revision)
}

func writeRevisionFile(path string, revision int64) error {
	tmp := path + ".tmp." + strconv.Itoa(os.Getpid())
	if err := os.WriteFile(tmp, []byte(strconv.FormatInt(revision, 10)+"\n"), 0o600); err != nil {
		return err
	}
	if err := syncFile(tmp); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	// The cursor is visible after rename. A directory fsync error is reported so
	// recovery can retry, but the already-fsynced journal commit is never undone.
	return syncDir(filepath.Dir(path))
}

func syncFile(path string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	return file.Sync()
}

func syncDir(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func (c *Context) compactIfNeeded(index *metadataIndex) error {
	p := c.paths()
	limitFields := strings.Fields(c.Config["JOURNAL_MAX_BYTES"])
	if len(limitFields) == 0 {
		return nil
	}
	limit, _ := strconv.ParseInt(limitFields[0], 10, 64)
	if limit <= 0 {
		return nil
	}
	info, err := os.Stat(p.Journal)
	if err != nil || info.Size() <= limit {
		return nil
	}
	var rows []string
	if index != nil {
		rows, err = index.manifestRows(false, 0, 0, c.Config["TOMBSTONE_TTL_DAYS"])
		rows = append([]string{"NASBOX_MANIFEST_V1"}, rows...)
	} else {
		rows, err = c.renderManifestLegacy()
	}
	if err != nil {
		return err
	}
	tmp := p.Manifest + ".tmp." + strconv.Itoa(os.Getpid())
	if err := os.WriteFile(tmp, []byte(strings.Join(rows, "\n")+"\n"), 0o600); err != nil {
		return err
	}
	if err := syncFile(tmp); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, p.Manifest); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := syncDir(p.Dir); err != nil {
		return err
	}
	if err := os.WriteFile(p.Journal, []byte("NASBOX_JOURNAL_V1\n"), 0o600); err != nil {
		return err
	}
	if err := syncFile(p.Journal); err != nil {
		return err
	}
	if err := syncDir(p.Dir); err != nil {
		return err
	}
	if index != nil {
		if err := index.resetCheckpointAfterCompaction(); err != nil {
			return err
		}
	}
	return os.WriteFile(filepath.Join(p.Dir, "manifest-sweep.stamp"), []byte(journalNow()+"\n"), 0o600)
}

// requireLease checks that the global transfer lock is held by this device.
// Mutating verbs must never acquire that lock themselves: the caller's SSH
// session already holds it.
func (c *Context) requireLease(device string) error {
	p := c.paths()
	file, err := os.OpenFile(p.Lock, os.O_RDWR, 0o600)
	if err != nil {
		return errNotLeaseOwned
	}
	defer file.Close()
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err == nil {
		_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
		return errNotLeaseOwned
	}
	return c.requireOwner(device)
}

var errNotLeaseOwned = errors.New("operazione mutante rifiutata: lease NAS non posseduto dal device")

func journalRowPUT(path, digest, size, mtime, device, ts, causal string) string {
	row := []string{"PUT", path, digest, size, mtime, device, ts}
	if causal != "" {
		row = append(row, causal)
	}
	return strings.Join(row, "\t")
}

func journalRowDELETE(path, digest, size, mtime, device, ts string) string {
	return strings.Join([]string{"DELETE", path, digest, size, mtime, device, ts}, "\t")
}

func readJournalLines(path string) ([]string, error) {
	file, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer file.Close()
	var lines []string
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 1024*1024), 8*1024*1024)
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	return lines, scanner.Err()
}
