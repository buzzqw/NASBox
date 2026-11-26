package serverd

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
)

func failBrowseDelete(reason string) error {
	fmt.Printf("BROWSE_DELETE_V1\x00ERROR\x00%s\x00", reason)
	return exitCode(1)
}

func cmdBrowseDelete(ctx *Context) error {
	fields, err := readNULFields(os.Stdin)
	if err != nil {
		return err
	}
	if len(fields) != 4 || fields[0] != "BROWSE_DELETE_V1" {
		return exitCode(1)
	}
	runTS, device, relative := fields[1], fields[2], fields[3]
	if !runTSPattern.MatchString(runTS) || !tokenPattern.MatchString(device) || !validRelative(relative) {
		return failBrowseDelete("parametri non validi")
	}
	if err := ctx.requireLease(device); err != nil {
		return mapLeaseError(err)
	}
	target := filepath.Join(ctx.ShareRoot, relative)
	backup := filepath.Join(ctx.ShareRoot, ".sync-trash", relative+"-"+runTS)
	err = ctx.withJournalLock(func() error {
		info, err := os.Lstat(target)
		if err != nil || info.Mode()&os.ModeSymlink != 0 {
			return failBrowseDelete("elemento non trovato")
		}
		paths := []string{relative}
		if info.IsDir() {
			files, err := listRegularFiles(target)
			if err != nil {
				return failBrowseDelete("elenco file fallito")
			}
			paths = nil
			for _, file := range files {
				paths = append(paths, filepath.ToSlash(filepath.Join(relative, file)))
			}
		}
		if err := os.MkdirAll(filepath.Dir(backup), 0o755); err != nil {
			return failBrowseDelete("cartella storico non creata")
		}
		if err := os.Rename(target, backup); err != nil {
			return failBrowseDelete("spostamento fallito")
		}
		txid := "browse-delete-" + runTS + "-" + strconv.Itoa(os.Getpid()) + "-" + randomSuffix()
		if err := ctx.appendTransactionLocked(txid, device, deleteRows(paths, device, journalNow())); err != nil {
			_ = os.MkdirAll(filepath.Dir(target), 0o755)
			_ = os.Rename(backup, target)
			return err
		}
		pruneEmptyDirs(ctx.ShareRoot, relative)
		return nil
	})
	if err != nil {
		return err
	}
	fmt.Print("BROWSE_DELETE_V1\x00OK\x00")
	return nil
}
