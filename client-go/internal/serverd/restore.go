package serverd

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type restoreRequest struct {
	path, version string
}

type restorePlan struct {
	request restoreRequest
	source  string
}

type restoreFingerprint struct {
	digest, size, mtimeNS string
}

type restoreUndo struct {
	source, target, backup string
	movedSource            bool
}

func (u *restoreUndo) rollback() error {
	if u.movedSource {
		if err := os.Rename(u.target, u.source); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("ripristino della versione dal cestino non riuscito: %w", err)
		}
	}
	if u.backup != "" {
		if err := os.Rename(u.backup, u.target); err != nil {
			return fmt.Errorf("ripristino del backup non riuscito: %w", err)
		}
	}
	return nil
}

func validRestoreVersion(version string) bool {
	return version == "latest" || runTSPattern.MatchString(version)
}

func restoreTrashDirRelative(relative string) string {
	dir := filepath.Dir(relative)
	if dir == "." {
		return ".sync-trash"
	}
	return filepath.ToSlash(filepath.Join(".sync-trash", dir))
}

// verifyRestorePaths rejects traversal through symlinks in both the live target
// and the trash tree before any file in the batch is moved. Missing components
// are safe: they are created only if a restore actually needs them.
func (c *Context) verifyRestorePaths(item restoreRequest) error {
	for _, relative := range []string{
		item.path,
		restoreTrashDirRelative(item.path),
		publishBackupRelative(item.path, "2000-01-01--00-00-00-000000Z"),
	} {
		if err := verifyPublishPath(c.ShareRoot, relative); err != nil {
			return err
		}
	}
	return nil
}

// findRestoreSource resolves one history entry without following symlinks. An
// exact version selects only that suffix; latest selects the lexicographically
// newest valid timestamp for the same basename.
func (c *Context) findRestoreSource(item restoreRequest) (string, error) {
	dirRelative := restoreTrashDirRelative(item.path)
	dir := filepath.Join(c.ShareRoot, dirRelative)
	info, err := os.Lstat(dir)
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("%w: directory dello storico", errInvalidPath)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", err
	}
	prefix := filepath.Base(item.path) + "-"
	newestName := ""
	newestPath := ""
	for _, entry := range entries {
		name := entry.Name()
		if item.version == "latest" {
			if !strings.HasPrefix(name, prefix) || historySuffix.FindStringSubmatch(name) == nil {
				continue
			}
		} else if name != prefix+item.version {
			continue
		}
		relative := filepath.ToSlash(filepath.Join(dirRelative, name))
		candidate := filepath.Join(c.ShareRoot, relative)
		if err := verifyPublishPath(c.ShareRoot, relative); err != nil {
			return "", err
		}
		info, err := os.Lstat(candidate)
		if err != nil {
			return "", err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("%w: %s", errInvalidPath, relative)
		}
		if !info.Mode().IsRegular() {
			continue
		}
		if newestPath == "" || name > newestName {
			newestName, newestPath = name, candidate
		}
	}
	return newestPath, nil
}

func restoreFingerprintFile(path string) (restoreFingerprint, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return restoreFingerprint{}, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return restoreFingerprint{}, errInvalidPath
	}
	digest, err := sha256File(path)
	if err != nil {
		return restoreFingerprint{}, err
	}
	info, err = os.Lstat(path)
	if err != nil {
		return restoreFingerprint{}, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return restoreFingerprint{}, errInvalidPath
	}
	return restoreFingerprint{
		digest: digest, size: strconv.FormatInt(info.Size(), 10),
		mtimeNS: strconv.FormatInt(info.ModTime().UnixNano(), 10),
	}, nil
}

func (f restoreFingerprint) valid() bool {
	if !digest64.MatchString(f.digest) {
		return false
	}
	if _, err := strconv.ParseInt(f.size, 10, 64); err != nil {
		return false
	}
	_, err := strconv.ParseInt(f.mtimeNS, 10, 64)
	return err == nil
}

func restoreFileMatches(path string, want restoreFingerprint) (bool, error) {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return false, errInvalidPath
	}
	if strconv.FormatInt(info.Size(), 10) != want.size || strconv.FormatInt(info.ModTime().UnixNano(), 10) != want.mtimeNS {
		return false, nil
	}
	digest, err := sha256File(path)
	if err != nil {
		return false, err
	}
	return digest == want.digest, nil
}

func restoreManifestSnapshot(rows []string) map[string]restoreFingerprint {
	entries := make(map[string]restoreFingerprint, len(rows))
	for _, row := range rows {
		if row == "NASBOX_MANIFEST_V1" {
			continue
		}
		fields := strings.Split(row, "\t")
		if len(fields) < 6 {
			continue
		}
		path := decodeField(fields[0])
		if !validRelative(path) {
			continue
		}
		entry := restoreFingerprint{digest: fields[1], size: fields[2], mtimeNS: fields[3]}
		if entry.valid() {
			entries[path] = entry
		}
	}
	return entries
}

func (c *Context) restoreBackupRelative(path string, now time.Time) (string, error) {
	stamp := now.UTC()
	for attempt := 0; attempt < 1000; attempt++ {
		suffix := stamp.Format("2006-01-02") + "--" +
			strings.Replace(stamp.Format("15-04-05.000000"), ".", "-", 1) + "Z"
		relative := publishBackupRelative(path, suffix)
		if err := verifyPublishPath(c.ShareRoot, relative); err != nil {
			return "", err
		}
		if _, err := os.Lstat(filepath.Join(c.ShareRoot, relative)); os.IsNotExist(err) {
			return relative, nil
		} else if err != nil {
			return "", err
		}
		stamp = stamp.Add(time.Microsecond)
	}
	return "", fmt.Errorf("impossibile allocare una versione di backup per %s", path)
}

func restorePutRow(device string, item restoreRequest, fingerprint restoreFingerprint) string {
	return journalRowPUT(
		encodeField(item.path), fingerprint.digest, fingerprint.size, fingerprint.mtimeNS,
		encodeField(device), journalNow(), "",
	)
}

func (c *Context) appendRestorePUT(device string, item restoreRequest, fingerprint restoreFingerprint, index int) (string, error) {
	txid := fmt.Sprintf("restore-%d-%d-%d-%s", time.Now().Unix(), index, os.Getpid(), randomSuffix())
	return txid, c.appendTransactionLocked(txid, device, []string{restorePutRow(device, item, fingerprint)})
}

// restoreJournalError keeps the restored bytes whenever the transaction is
// durable (or its state cannot be established). Otherwise it undoes only the
// filesystem moves made by this attempt before reporting ERROR to the client.
func (c *Context) restoreJournalError(txid string, undo *restoreUndo) (bool, error) {
	committed, err := c.transactionCommitted(txid)
	if err != nil {
		return false, fmt.Errorf("stato della transazione restore %s sconosciuto: %w", txid, err)
	}
	if committed {
		return true, nil
	}
	if undo != nil {
		if err := undo.rollback(); err != nil {
			return false, err
		}
	}
	return false, nil
}

func (c *Context) restoreOne(device string, plan restorePlan, manifest restoreFingerprint, manifestFound bool, index int, now time.Time) (string, restoreFingerprint, bool, error) {
	item := plan.request
	target := filepath.Join(c.ShareRoot, item.path)
	if plan.source == "" {
		if manifestFound {
			matches, err := restoreFileMatches(target, manifest)
			if err != nil {
				if errors.Is(err, errInvalidPath) {
					return "", restoreFingerprint{}, false, err
				}
				return "ERROR", restoreFingerprint{}, false, nil
			}
			if matches {
				return "RESTORED", manifest, false, nil
			}
		}
		return "NO_VERSION", restoreFingerprint{}, false, nil
	}

	fingerprint, err := restoreFingerprintFile(plan.source)
	if err != nil {
		if errors.Is(err, errInvalidPath) {
			return "", restoreFingerprint{}, false, err
		}
		return "ERROR", restoreFingerprint{}, false, nil
	}
	info, err := os.Lstat(target)
	existed := err == nil
	if err != nil && !os.IsNotExist(err) {
		return "ERROR", restoreFingerprint{}, false, nil
	}
	if existed {
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return "", restoreFingerprint{}, false, errInvalidPath
		}
		matches, err := restoreFileMatches(target, fingerprint)
		if err != nil {
			if errors.Is(err, errInvalidPath) {
				return "", restoreFingerprint{}, false, err
			}
			return "ERROR", restoreFingerprint{}, false, nil
		}
		if matches {
			if manifestFound && manifest == fingerprint {
				return "RESTORED", fingerprint, false, nil
			}
			txid, err := c.appendRestorePUT(device, item, fingerprint, index)
			if err != nil {
				committed, checkErr := c.restoreJournalError(txid, nil)
				if checkErr != nil {
					return "", restoreFingerprint{}, false, checkErr
				}
				if committed {
					return "RESTORED", fingerprint, true, nil
				}
				return "ERROR", restoreFingerprint{}, false, nil
			}
			return "RESTORED", fingerprint, true, nil
		}
	}

	if err := verifyPublishPath(c.ShareRoot, item.path); err != nil {
		return "", restoreFingerprint{}, false, err
	}
	sourceRelative, err := filepath.Rel(c.ShareRoot, plan.source)
	if err != nil {
		return "", restoreFingerprint{}, false, err
	}
	if err := verifyPublishPath(c.ShareRoot, filepath.ToSlash(sourceRelative)); err != nil {
		return "", restoreFingerprint{}, false, err
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return "ERROR", restoreFingerprint{}, false, nil
	}

	backupRelative := ""
	var prior *publishIntentFileState
	if existed {
		backupRelative, err = c.restoreBackupRelative(item.path, now)
		if err != nil {
			return "ERROR", restoreFingerprint{}, false, nil
		}
		prior, err = publishPriorFromInfo(info)
		if err != nil {
			return "ERROR", restoreFingerprint{}, false, nil
		}
	}
	size, _ := strconv.ParseInt(fingerprint.size, 10, 64)
	mtimeNS, _ := strconv.ParseInt(fingerprint.mtimeNS, 10, 64)
	txid := fmt.Sprintf("restore-%d-%d-%d-%s", time.Now().Unix(), index, os.Getpid(), randomSuffix())
	row := restorePutRow(device, item, fingerprint)
	intent, err := c.prepareMutationIntent(mutationIntentPayload{
		Version: mutationIntentVersion, TxID: txid, Device: device, RunTS: webRunTS(),
		Kind: mutationKindRestore,
		Ops: []mutationIntentOp{{
			Src: sourceRelative, Dst: item.path, Backup: backupRelative, Prior: prior,
			Digest: fingerprint.digest, Size: size, MTimeNS: mtimeNS, Rows: []string{row},
		}},
	})
	if err != nil {
		return "ERROR", restoreFingerprint{}, false, nil
	}
	defer intent.close()
	runTxIntentFailpoint("mutation_prepared", txid)

	undo := restoreUndo{source: plan.source, target: target}
	if existed {
		undo.backup = filepath.Join(c.ShareRoot, backupRelative)
		if err := os.MkdirAll(filepath.Dir(undo.backup), 0o755); err != nil {
			_ = intent.finish()
			return "ERROR", restoreFingerprint{}, false, nil
		}
		// Keep the current inode reachable under its history name, then replace
		// the live path with one atomic rename. No window exposes a partial file.
		if err := os.Link(target, undo.backup); err != nil {
			_ = intent.finish()
			return "ERROR", restoreFingerprint{}, false, nil
		}
	}
	if err := os.Rename(plan.source, target); err != nil {
		if undo.backup != "" {
			_ = os.Remove(undo.backup)
		}
		_ = intent.finish()
		return "ERROR", restoreFingerprint{}, false, nil
	}
	undo.movedSource = true
	runTxIntentFailpoint("mutation_moved", txid)

	matches, err := restoreFileMatches(target, fingerprint)
	if err != nil || !matches {
		if rollbackErr := undo.rollback(); rollbackErr != nil {
			return "", restoreFingerprint{}, false, rollbackErr
		}
		_ = intent.finish()
		return "ERROR", restoreFingerprint{}, false, nil
	}
	_ = syncFile(target)
	_ = syncDir(filepath.Dir(target))
	_ = syncDir(filepath.Dir(plan.source))
	if undo.backup != "" {
		_ = syncDir(filepath.Dir(undo.backup))
	}

	if err := c.appendTransactionLocked(txid, device, []string{row}); err != nil {
		committed, checkErr := c.restoreJournalError(txid, &undo)
		if checkErr != nil {
			return "", restoreFingerprint{}, false, checkErr
		}
		if committed {
			_ = intent.markCommitted()
			_ = intent.finish()
			return "RESTORED", fingerprint, true, nil
		}
		_ = intent.finish()
		return "ERROR", restoreFingerprint{}, false, nil
	}
	runTxIntentFailpoint("mutation_committed", txid)
	_ = intent.markCommitted()
	if err := intent.finish(); err != nil {
		return "", restoreFingerprint{}, false, err
	}
	return "RESTORED", fingerprint, true, nil
}

// cmdRestoreBatch implements the client's streaming NUL protocol:
// RESTORE_BATCH_V1, device, count, then (path, version) pairs. Versions are
// either an exact trash timestamp or "latest".
func cmdRestoreBatch(ctx *Context) error {
	fields, err := readNULFields(os.Stdin)
	if err != nil {
		return err
	}
	if len(fields) < 3 || fields[0] != "RESTORE_BATCH_V1" {
		return exitCode(1)
	}
	device := fields[1]
	count, err := strconv.Atoi(fields[2])
	if err != nil || count < 0 || count > 1000 || len(fields) != 3+count*2 || !tokenPattern.MatchString(device) {
		return exitCode(1)
	}
	items := make([]restoreRequest, count)
	for i := range items {
		item := restoreRequest{path: fields[3+i*2], version: fields[4+i*2]}
		if !validRelative(item.path) || !validRestoreVersion(item.version) {
			return exitCode(1)
		}
		items[i] = item
	}
	if err := ctx.requireLease(device); err != nil {
		return mapLeaseError(err)
	}
	for _, item := range items {
		if err := ctx.verifyRestorePaths(item); err != nil {
			return err
		}
	}

	statuses := make([]string, len(items))
	err = ctx.withJournalLock(func() error {
		if err := ctx.recoverMutationIntentsLocked(device); err != nil {
			return err
		}
		plans := make([]restorePlan, len(items))
		for i, item := range items {
			if err := ctx.verifyRestorePaths(item); err != nil {
				return err
			}
			source, err := ctx.findRestoreSource(item)
			if err != nil {
				return err
			}
			plans[i] = restorePlan{request: item, source: source}
		}
		rows, err := ctx.renderManifestLegacy()
		if err != nil {
			return err
		}
		manifest := restoreManifestSnapshot(rows)
		for i, plan := range plans {
			entry, found := manifest[plan.request.path]
			status, restored, update, err := ctx.restoreOne(device, plan, entry, found, i, time.Now().UTC())
			if err != nil {
				return err
			}
			statuses[i] = status
			if update {
				manifest[plan.request.path] = restored
			}
		}
		return nil
	})
	if err != nil {
		return err
	}

	out := bufio.NewWriter(os.Stdout)
	fmt.Fprintf(out, "RESTORE_BATCH_V1\x00%d\x00", len(items))
	for i, item := range items {
		fmt.Fprintf(out, "%s\x00%s\x00", item.path, statuses[i])
	}
	return out.Flush()
}
