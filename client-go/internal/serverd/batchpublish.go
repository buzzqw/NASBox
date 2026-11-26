package serverd

import (
	"bufio"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type publishRecord struct {
	path, digest, size, mtime, causal string
	chunks                            []string
}

func publishBackupRelative(path, runTS string) string {
	return filepath.ToSlash(filepath.Join(".sync-trash", path+"-"+runTS))
}

// transactionCommitted makes publish retries idempotent before any filesystem
// move. The caller holds the journal lock.
func (c *Context) transactionCommitted(txid string) (bool, error) {
	index, err := openMetadataIndex(c.paths())
	if err == nil {
		defer index.Close()
		if err := index.syncLocked(); err == nil {
			exists, err := index.transactionExists(txid)
			if err != nil {
				return false, err
			}
			if exists {
				// The index may have observed a complete block whose original
				// journal fsync failed. Make the authoritative text commit durable
				// before treating it as the winner over an intent marker.
				if err := syncFile(c.paths().Journal); err != nil {
					return false, err
				}
				return true, nil
			}
			return false, nil
		}
	}
	commits, _, err := readJournalCommits(c.paths().Journal, 0)
	if err != nil {
		return false, err
	}
	for _, commit := range commits {
		if commit.TxID == txid {
			if err := syncFile(c.paths().Journal); err != nil {
				return false, err
			}
			return true, nil
		}
	}
	return false, nil
}

// verifyPublishPath rejects symlinks in the target itself and in every ancestor
// below ShareRoot. Missing components are safe: MkdirAll will create them later.
func verifyPublishPath(root, relative string) error {
	current := root
	if info, err := os.Lstat(current); err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return exitCode(1)
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	for _, component := range strings.Split(relative, "/") {
		current = filepath.Join(current, component)
		info, err := os.Lstat(current)
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return exitCode(1)
		}
	}
	return nil
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
	if err != nil || count < 0 || count > 100000 {
		return record, fmt.Errorf("conteggio chunk non valido")
	}
	record.chunks = make([]string, 0, count)
	for i := 0; i < count; i++ {
		digest, err := readNULString(reader)
		if err != nil || !digest64.MatchString(digest) {
			return record, fmt.Errorf("chunk non valido")
		}
		record.chunks = append(record.chunks, digest)
	}
	return record, nil
}

func publishIntentOpFromRecord(record publishRecord, runTS string) (publishIntentOp, error) {
	if !validRelative(record.path) || !digest64.MatchString(record.digest) {
		return publishIntentOp{}, exitCode(1)
	}
	size, err := strconv.ParseInt(record.size, 10, 64)
	if err != nil || size < 0 {
		return publishIntentOp{}, exitCode(1)
	}
	mtime, err := strconv.ParseInt(record.mtime, 10, 64)
	if err != nil {
		return publishIntentOp{}, exitCode(1)
	}
	if err := validatePublishContent(record.digest, record.size, record.chunks); err != nil {
		return publishIntentOp{}, err
	}
	if record.causal != "" && !causalPattern.MatchString(record.causal) {
		return publishIntentOp{}, exitCode(1)
	}
	return publishIntentOp{
		Path: record.path, Digest: record.digest, Size: size, MTimeNS: mtime,
		Causal: record.causal, Chunks: append([]string(nil), record.chunks...),
		BackupRelative: publishBackupRelative(record.path, runTS),
	}, nil
}

func publishIntentRequestFromRecords(device, txid, runTS, mode string, records []publishRecord) (publishIntentRequest, error) {
	request := publishIntentRequest{TxID: txid, Device: device, RunTS: runTS, Mode: mode}
	seen := make(map[string]bool, len(records))
	for _, record := range records {
		op, err := publishIntentOpFromRecord(record, runTS)
		if err != nil {
			return request, err
		}
		if seen[op.Path] {
			return request, exitCode(1)
		}
		seen[op.Path] = true
		request.Ops = append(request.Ops, op)
	}
	return request, nil
}

// buildPublishIntentPayloadLocked performs all read-only preconditions and
// captures the exact journal rows/revision before the first filesystem change.
func (c *Context) buildPublishIntentPayloadLocked(device, txid, runTS, mode string, records []publishRecord) (publishIntentPayload, error) {
	request, err := publishIntentRequestFromRecords(device, txid, runTS, mode, records)
	if err != nil {
		return publishIntentPayload{}, err
	}
	if len(request.Ops) == 0 || len(request.Ops) > 100000 {
		return publishIntentPayload{}, exitCode(1)
	}
	current, err := c.journalRevision()
	if err != nil {
		return publishIntentPayload{}, err
	}
	if current == math.MaxInt64 {
		return publishIntentPayload{}, errors.New("manifest.revision ha raggiunto il limite")
	}
	if err := preflightRevisionWrite(c.paths().Revision); err != nil {
		return publishIntentPayload{}, err
	}
	payload := publishIntentPayload{
		Version: publishIntentVersion, TxID: txid, Device: device, RunTS: runTS,
		Mode: mode, TargetRevision: current + 1, Ops: request.Ops,
		Rows: make([]string, 0, len(request.Ops)),
	}
	ts := journalNow()
	for _, op := range payload.Ops {
		if err := verifyPublishPath(c.ShareRoot, op.Path); err != nil {
			return publishIntentPayload{}, err
		}
		if err := verifyPublishPath(c.ShareRoot, op.BackupRelative); err != nil {
			return publishIntentPayload{}, err
		}
		target := filepath.Join(c.ShareRoot, op.Path)
		backup := filepath.Join(c.ShareRoot, op.BackupRelative)
		targetInfo, err := optionalLstat(target)
		if err != nil {
			return publishIntentPayload{}, err
		}
		backupInfo, err := optionalLstat(backup)
		if err != nil {
			return publishIntentPayload{}, err
		}
		if mode == "create" && targetInfo != nil {
			return publishIntentPayload{}, fmt.Errorf("target già esistente: %s", op.Path)
		}
		if backupInfo != nil {
			return publishIntentPayload{}, fmt.Errorf("backup già esistente: %s", op.BackupRelative)
		}
		if targetInfo != nil {
			prior, err := publishPriorFromInfo(targetInfo)
			if err != nil {
				return publishIntentPayload{}, err
			}
			op.Prior = prior
			payload.Ops[len(payload.Rows)] = op
		}
		payload.Rows = append(payload.Rows, journalRowPUT(
			encodeField(op.Path), op.Digest, strconv.FormatInt(op.Size, 10),
			strconv.FormatInt(op.MTimeNS, 10), encodeField(device), ts, op.Causal,
		))
	}
	return payload, nil
}

// publishChunkRecords is the common single/batch path. Recovery runs under the
// caller's global lease and the journal lock before any publish precondition or
// filesystem mutation.
func (c *Context) publishChunkRecords(device, txid, runTS, mode string, records []publishRecord) error {
	if !tokenPattern.MatchString(device) || !tokenPattern.MatchString(txid) || !runTSPattern.MatchString(runTS) {
		return exitCode(1)
	}
	if len(records) == 0 || len(records) > 100000 {
		return exitCode(1)
	}
	if err := c.requireLease(device); err != nil {
		return mapLeaseError(err)
	}
	for _, record := range records {
		if err := verifyPublishPath(c.ShareRoot, record.path); err != nil {
			return err
		}
		if err := verifyPublishPath(c.ShareRoot, publishBackupRelative(record.path, runTS)); err != nil {
			return err
		}
	}
	return c.withJournalLock(func() error {
		request, err := publishIntentRequestFromRecords(device, txid, runTS, mode, records)
		if err != nil {
			return err
		}
		if err := c.recoverTxIntentsLocked(device, map[string]publishIntentRequest{txid: request}); err != nil {
			return err
		}
		committed, err := c.transactionCommitted(txid)
		if err != nil {
			return err
		}
		if committed {
			return nil
		}
		payload, err := c.buildPublishIntentPayloadLocked(device, txid, runTS, mode, records)
		if err != nil {
			return err
		}
		return c.executePreparedTxIntentLocked(payload)
	})
}

// cmdFilePublishBatch publishes many files in one journal transaction.
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
	if err != nil || (magic != "FILE_PUBLISH_BATCH_V1" && magic != "FILE_PUBLISH_BATCH_V2") {
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
	mode := "replace"
	if magic == "FILE_PUBLISH_BATCH_V2" {
		mode, err = readNULString(reader)
		if err != nil || (mode != "create" && mode != "replace") {
			return exitCode(1)
		}
	}
	records := make([]publishRecord, 0, count)
	for i := 0; i < count; i++ {
		record, err := readPublishRecord(reader)
		if err != nil {
			return exitCode(1)
		}
		if _, err := strconv.ParseInt(record.mtime, 10, 64); err != nil {
			return exitCode(1)
		}
		if err := validatePublishContent(record.digest, record.size, record.chunks); err != nil {
			return err
		}
		if record.causal != "" && !causalPattern.MatchString(record.causal) {
			return exitCode(1)
		}
		records = append(records, record)
	}
	if err := ctx.publishChunkRecords(device, txid, runTS, mode, records); err != nil {
		return err
	}
	fmt.Printf("FILE_PUBLISH_BATCH_V1\x00OK\x00%d\x00", count)
	return nil
}
