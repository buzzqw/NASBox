package serverd

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

type publishRecord struct {
	path, digest, size, mtime, causal string
	chunks                            []string
}

func readPublishRecord(reader *bufio.Reader) (publishRecord, error) {
	var record publishRecord
	var err error
	if record.path, err = readNULString(reader); err != nil {
		return record, err
	}
	if record.digest, err = readNULString(reader); err != nil {
		return record, err
	}
	if record.size, err = readNULString(reader); err != nil {
		return record, err
	}
	if record.mtime, err = readNULString(reader); err != nil {
		return record, err
	}
	if record.causal, err = readNULString(reader); err != nil {
		return record, err
	}
	countRaw, err := readNULString(reader)
	if err != nil {
		return record, err
	}
	count, err := strconv.Atoi(countRaw)
	if err != nil || count < 1 || count > 100000 {
		return record, fmt.Errorf("conteggio chunk non valido")
	}
	for i := 0; i < count; i++ {
		digest, err := readNULString(reader)
		if err != nil || !digest64.MatchString(digest) {
			return record, fmt.Errorf("chunk non valido")
		}
		record.chunks = append(record.chunks, digest)
	}
	return record, nil
}

// cmdFilePublishBatch publishes many files in one journal transaction. rsync's
// staging-publish had this property; the first chunk implementation did not.
func cmdFilePublishBatch(ctx *Context, args []string) error {
	if len(args) != 3 {
		return exitCode(1)
	}
	device, txid, runTS := args[0], args[1], args[2]
	if !tokenPattern.MatchString(device) || !tokenPattern.MatchString(txid) || !runTSPattern.MatchString(runTS) {
		return exitCode(1)
	}
	reader := bufio.NewReader(os.Stdin)
	magic, err := readNULString(reader)
	if err != nil || magic != "FILE_PUBLISH_BATCH_V1" {
		return exitCode(1)
	}
	countRaw, err := readNULString(reader)
	if err != nil {
		return exitCode(1)
	}
	count, err := strconv.Atoi(countRaw)
	if err != nil || count < 1 || count > 100000 {
		return exitCode(1)
	}
	records := make([]publishRecord, 0, count)
	seen := map[string]bool{}
	for i := 0; i < count; i++ {
		record, err := readPublishRecord(reader)
		if err != nil || !validRelative(record.path) || seen[record.path] || !digest64.MatchString(record.digest) {
			return exitCode(1)
		}
		if _, err := strconv.ParseInt(record.size, 10, 64); err != nil {
			return exitCode(1)
		}
		if _, err := strconv.ParseInt(record.mtime, 10, 64); err != nil {
			return exitCode(1)
		}
		if record.causal != "" && !causalPattern.MatchString(record.causal) {
			return exitCode(1)
		}
		seen[record.path] = true
		records = append(records, record)
	}
	if err := ctx.requireLease(device); err != nil {
		return mapLeaseError(err)
	}
	store, err := ctx.chunkStore()
	if err != nil {
		return err
	}
	type moved struct{ from, to string }
	rollback := func(entries []moved) {
		for i := len(entries) - 1; i >= 0; i-- {
			_ = os.MkdirAll(filepath.Dir(entries[i].to), 0o755)
			_ = os.Rename(entries[i].from, entries[i].to)
		}
	}
	err = ctx.withJournalLock(func() error {
		entries := []moved{}
		rows := make([]string, 0, count)
		for _, record := range records {
			target := filepath.Join(ctx.ShareRoot, record.path)
			backup := filepath.Join(ctx.ShareRoot, ".sync-trash", record.path+"-"+runTS)
			if _, err := os.Lstat(target); err == nil {
				if err := os.MkdirAll(filepath.Dir(backup), 0o755); err != nil {
					rollback(entries)
					return err
				}
				if err := os.Rename(target, backup); err != nil {
					rollback(entries)
					return err
				}
				entries = append(entries, moved{from: backup, to: target})
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				rollback(entries)
				return err
			}
			if err := store.Assemble(record.chunks, target); err != nil {
				rollback(entries)
				return err
			}
			requested, _ := strconv.ParseInt(record.mtime, 10, 64)
			stamp := time.Unix(0, requested)
			if err := os.Chtimes(target, stamp, stamp); err != nil {
				rollback(entries)
				return err
			}
			info, err := os.Lstat(target)
			if err != nil || strconv.FormatInt(info.Size(), 10) != record.size || info.ModTime().UnixNano() != requested {
				rollback(entries)
				return exitCode(1)
			}
			digest, err := sha256File(target)
			if err != nil || digest != record.digest {
				rollback(entries)
				return exitCode(1)
			}
			record.mtime = strconv.FormatInt(info.ModTime().UnixNano(), 10)
			if err := store.SaveFileMeta(digest, record.chunks); err != nil {
				rollback(entries)
				return err
			}
			rows = append(rows, journalRowPUT(encodeField(record.path), digest, record.size, record.mtime, encodeField(device), journalNow(), record.causal))
		}
		if err := ctx.appendTransactionLocked(txid, device, rows); err != nil {
			rollback(entries)
			return err
		}
		return nil
	})
	if err != nil {
		return err
	}
	fmt.Printf("FILE_PUBLISH_BATCH_V1\x00OK\x00%d\x00", count)
	return nil
}
