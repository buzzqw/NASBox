package serverd

import (
	"bufio"
	"errors"
	"fmt"
	"io"
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
			return nil
		}
	} else {
		exists, err := index.transactionExists(txid)
		if err != nil {
			return err
		}
		if exists {
			return nil
		}
	}
	info, err := os.Stat(p.Journal)
	if err != nil {
		return err
	}
	oldSize := info.Size()
	lines := []string{"BEGIN\t" + txid + "\t" + journalNow() + "\t" + device}
	lines = append(lines, rows...)
	lines = append(lines, "COMMIT\t"+txid)
	block := strings.Join(lines, "\n") + "\n"

	file, err := os.OpenFile(p.Journal, os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	_, writeErr := file.WriteString(block)
	if syncErr := file.Sync(); syncErr != nil && writeErr == nil {
		writeErr = syncErr
	}
	file.Close()
	if writeErr != nil {
		_ = os.Truncate(p.Journal, oldSize)
		return writeErr
	}
	if err := c.bumpRevision(); err != nil {
		_ = os.Truncate(p.Journal, oldSize)
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
	if err := c.compactIfNeeded(index); err != nil {
		// Journal and revision are already durable. Compaction is maintenance:
		// reporting it as a failed mutation would make callers roll back files
		// while the metadata still records the operation as committed.
		fmt.Fprintf(os.Stderr, "compattazione journal non riuscita: %v\n", err)
	}
	return nil
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
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	// Once rename succeeds the new cursor is visible. Keep the historical
	// best-effort fsync behavior: returning an error here would make the caller
	// truncate a journal whose revision has already advanced.
	_ = syncFile(path)
	_ = syncDir(filepath.Dir(path))
	return nil
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
