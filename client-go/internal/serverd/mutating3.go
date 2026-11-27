package serverd

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
)

func randomSuffix() string {
	buf := make([]byte, 4)
	if _, err := rand.Read(buf); err != nil {
		return strconv.Itoa(os.Getpid())
	}
	return hex.EncodeToString(buf)
}

func deleteRows(paths []string, device, ts string) []string {
	rows := make([]string, 0, len(paths))
	for _, path := range paths {
		rows = append(rows, journalRowDELETE(encodeField(path), "", "0", "0", encodeField(device), ts))
	}
	return rows
}

func pruneEmptyDirs(root, relative string) {
	dir := filepath.Dir(relative)
	for dir != "." && dir != string(os.PathSeparator) {
		full := filepath.Join(root, dir)
		base := filepath.Base(dir)
		if base == ".sync-trash" || base == ".nasbox-staging" || base == ".sync-partial" {
			return
		}
		if err := os.Remove(full); err != nil {
			return
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return
		}
		dir = parent
	}
}

func cmdCheckedDelete(ctx *Context) error {
	fields, err := readNULFields(os.Stdin)
	if err != nil {
		return err
	}
	if len(fields) < 4 || fields[0] != "CHECKED_DELETE_V1" {
		return exitCode(1)
	}
	runTS, device := fields[1], fields[2]
	count, err := strconv.Atoi(fields[3])
	if err != nil || count < 0 || count > 100000 || len(fields) < 4+count*3 {
		return exitCode(1)
	}
	if !runTSPattern.MatchString(runTS) || !tokenPattern.MatchString(device) {
		return exitCode(1)
	}
	items := make([]struct{ path, digest, mtime string }, 0, count)
	for i := 0; i < count; i++ {
		item := struct{ path, digest, mtime string }{fields[4+i*3], fields[5+i*3], fields[6+i*3]}
		if !validRelative(item.path) || !digest64.MatchString(item.digest) {
			return exitCode(1)
		}
		if _, err := strconv.ParseInt(item.mtime, 10, 64); err != nil {
			return exitCode(1)
		}
		items = append(items, item)
	}
	if err := ctx.requireLease(device); err != nil {
		return mapLeaseError(err)
	}
	statuses := make([]string, count)
	repairRows := []string{}
	type checkedMove struct {
		index                          int
		target, backup, backupRelative string
		rows                           []string
	}
	var moves []checkedMove

	err = ctx.withJournalLock(func() error {
		if err := ctx.recoverMutationIntentsLocked(device); err != nil {
			return err
		}
		for index, item := range items {
			target := filepath.Join(ctx.ShareRoot, item.path)
			info, statErr := os.Lstat(target)
			if os.IsNotExist(statErr) {
				statuses[index] = "ABSENT"
				repairRows = append(repairRows, deleteRows([]string{item.path}, device, journalNow())...)
				continue
			}
			if statErr != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
				statuses[index] = "ERROR"
				continue
			}
			digest, err := sha256File(target)
			if err != nil || digest != item.digest || strconv.FormatInt(info.ModTime().Unix(), 10) != item.mtime {
				statuses[index] = "STALE"
				continue
			}
			backupRelative := filepath.ToSlash(filepath.Join(".sync-trash", item.path+"-"+runTS))
			backup := filepath.Join(ctx.ShareRoot, filepath.FromSlash(backupRelative))
			if _, err := publishPriorFromInfo(info); err != nil {
				statuses[index] = "ERROR"
				continue
			}
			moves = append(moves, checkedMove{
				index: index, target: target, backup: backup, backupRelative: backupRelative,
				rows: deleteRows([]string{item.path}, device, journalNow()),
			})
		}
		if len(moves) == 0 && len(repairRows) == 0 {
			return nil
		}
		txid := "delete-batch-" + runTS + "-" + strconv.Itoa(os.Getpid()) + "-" + randomSuffix()
		var intent *mutationIntent
		if len(moves) > 0 {
			ops := make([]mutationIntentOp, 0, len(moves))
			for _, move := range moves {
				info, err := os.Lstat(move.target)
				if err != nil {
					return err
				}
				prior, err := publishPriorFromInfo(info)
				if err != nil {
					return err
				}
				ops = append(ops, mutationIntentOp{
					Path: items[move.index].path, Backup: move.backupRelative,
					Prior: prior, Rows: move.rows,
				})
			}
			var err error
			intent, err = ctx.prepareMutationIntent(mutationIntentPayload{
				Version: mutationIntentVersion, TxID: txid, Device: device, RunTS: runTS,
				Kind: mutationKindDelete, Ops: ops,
			})
			if err != nil {
				return err
			}
			defer intent.close()
			runTxIntentFailpoint("mutation_prepared", txid)
		}
		var moved []struct{ from, to string }
		movedRows := []string{}
		for _, move := range moves {
			index := move.index
			if err := os.MkdirAll(filepath.Dir(move.backup), 0o755); err != nil {
				statuses[index] = "ERROR"
				continue
			}
			if err := os.Rename(move.target, move.backup); err != nil {
				statuses[index] = "ERROR"
				continue
			}
			moved = append(moved, struct{ from, to string }{move.backup, move.target})
			movedRows = append(movedRows, move.rows...)
			statuses[index] = "DELETED"
		}
		if len(moved) > 0 {
			runTxIntentFailpoint("mutation_moved", txid)
		}
		rows := append(append([]string{}, repairRows...), movedRows...)
		if len(rows) == 0 {
			if intent != nil {
				_ = intent.finish()
			}
			return nil
		}
		if err := ctx.appendTransactionLocked(txid, device, rows); err != nil {
			for _, pair := range moved {
				_ = os.MkdirAll(filepath.Dir(pair.to), 0o755)
				_ = os.Rename(pair.from, pair.to)
			}
			for i := range statuses {
				if statuses[i] == "DELETED" || statuses[i] == "ABSENT" {
					statuses[i] = "ERROR"
				}
			}
			if intent != nil {
				_ = intent.finish()
			}
			return err
		}
		runTxIntentFailpoint("mutation_committed", txid)
		if intent != nil {
			_ = intent.markCommitted()
			if err := intent.finish(); err != nil {
				return err
			}
		}
		for _, item := range items {
			pruneEmptyDirs(ctx.ShareRoot, item.path)
		}
		return nil
	})
	if err != nil {
		return err
	}
	out := fmt.Sprintf("CHECKED_DELETE_V1\x00%d\x00", count)
	for i, item := range items {
		out += item.path + "\x00" + statuses[i] + "\x00"
	}
	fmt.Print(out)
	return nil
}
