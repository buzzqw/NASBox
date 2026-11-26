package serverd

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
)

type renameRecord struct{ rel, digest, size, mtime string }

func listRegularFiles(root string) ([]string, error) {
	files := []string{}
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.Mode().IsRegular() && info.Mode()&os.ModeSymlink == 0 {
			relative, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			files = append(files, relative)
		}
		return nil
	})
	return files, err
}

func renameRows(src, dst string, records []renameRecord, device, ts string) []string {
	rows := make([]string, 0, len(records)*2)
	for _, rec := range records {
		from := encodeField(filepath.ToSlash(filepath.Join(src, rec.rel)))
		to := encodeField(filepath.ToSlash(filepath.Join(dst, rec.rel)))
		rows = append(rows, journalRowDELETE(from, "", "0", "0", encodeField(device), ts))
		rows = append(rows, journalRowPUT(to, rec.digest, rec.size, rec.mtime, encodeField(device), ts, ""))
	}
	return rows
}

func cmdBrowseRename(ctx *Context) error {
	fields, err := readNULFields(os.Stdin)
	if err != nil {
		return err
	}
	if len(fields) < 5 {
		return exitCode(1)
	}
	magic := fields[0]
	runTS, device, src, dst := fields[1], fields[2], fields[3], fields[4]
	if !runTSPattern.MatchString(runTS) || !tokenPattern.MatchString(device) || !validRelative(src) || !validRelative(dst) {
		return failBrowse(magic, "parametri non validi")
	}
	source := filepath.Join(ctx.ShareRoot, src)
	target := filepath.Join(ctx.ShareRoot, dst)
	info, err := os.Lstat(source)
	if err != nil || !info.IsDir() && !info.Mode().IsRegular() {
		return failBrowse(magic, "sorgente non trovata")
	}
	if _, err := os.Lstat(target); err == nil {
		return failBrowse(magic, "destinazione gia' esistente")
	}

	records := []renameRecord{}
	kind := ""
	switch magic {
	case "BROWSE_RENAME_V1":
		if len(fields) != 5 {
			return exitCode(1)
		}
	case "BROWSE_RENAME_V2", "BROWSE_RENAME_V3":
		if len(fields) < 9 {
			return exitCode(1)
		}
		kind, _, _, _ = fields[5], fields[6], fields[7], fields[8]
		if kind != "FILE" && kind != "DIR" {
			return failBrowse(magic, "kind non valido")
		}
		if magic == "BROWSE_RENAME_V3" && kind != "DIR" {
			return failBrowse(magic, "V3 richiede DIR")
		}
		if kind == "FILE" {
			if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
				return failBrowse(magic, "sorgente non regolare")
			}
			expectedSize, _ := strconv.ParseInt(fields[7], 10, 64)
			expectedMTime, _ := strconv.ParseInt(fields[8], 10, 64)
			if info.Size() != expectedSize || info.ModTime().Unix() != expectedMTime {
				return failBrowse(magic, "sorgente cambiata")
			}
			digest, err := sha256File(source)
			if err != nil || digest != fields[6] {
				return failBrowse(magic, "digest non corrispondente")
			}
			records = append(records, renameRecord{"", fields[6], fields[7], fields[8]})
		} else {
			files, err := listRegularFiles(source)
			if err != nil {
				return failBrowse(magic, "elenco file fallito")
			}
			if magic == "BROWSE_RENAME_V3" {
				count, err := strconv.Atoi(fields[9])
				if err != nil || count != len(files) || len(fields) != 10+count*4 {
					return failBrowse(magic, "conteggio file non corrispondente")
				}
				for i := range files {
					rec := renameRecord{
						rel: fields[10+i*4], digest: fields[11+i*4], size: fields[12+i*4], mtime: fields[13+i*4],
					}
					if !validRelative(rec.rel) {
						return failBrowse(magic, "percorso file non valido")
					}
					full := filepath.Join(source, rec.rel)
					fileInfo, err := os.Lstat(full)
					if err != nil {
						return failBrowse(magic, "file sorgente mancante")
					}
					size, _ := strconv.ParseInt(rec.size, 10, 64)
					mtime, _ := strconv.ParseInt(rec.mtime, 10, 64)
					if fileInfo.Size() != size || fileInfo.ModTime().Unix() != mtime/1_000_000_000 {
						return failBrowse(magic, "file sorgente cambiato")
					}
					records = append(records, rec)
				}
			} else {
				for _, relative := range files {
					full := filepath.Join(source, relative)
					digest, err := sha256File(full)
					if err != nil {
						return failBrowse(magic, "hash fallito")
					}
					fileInfo, _ := os.Lstat(full)
					records = append(records, renameRecord{
						rel: filepath.ToSlash(relative), digest: digest,
						size: strconv.FormatInt(fileInfo.Size(), 10), mtime: fileMTimeNS(fileInfo),
					})
				}
			}
		}
	default:
		return exitCode(1)
	}
	if err := ctx.requireLease(device); err != nil {
		return mapLeaseError(err)
	}
	err = ctx.withJournalLock(func() error {
		if _, err := os.Lstat(target); err == nil {
			return failBrowse(magic, "destinazione gia' esistente")
		}
		if err := os.Rename(source, target); err != nil {
			return failBrowse(magic, "rinomina fallita")
		}
		txid := "browse-rename-" + runTS + "-" + strconv.Itoa(os.Getpid()) + "-" + randomSuffix()
		var rows []string
		if magic == "BROWSE_RENAME_V1" {
			paths := []string{src}
			if info.IsDir() {
				files, _ := listRegularFiles(target)
				paths = nil
				for _, relative := range files {
					paths = append(paths, filepath.ToSlash(filepath.Join(src, relative)))
				}
			}
			rows = deleteRows(paths, device, journalNow())
		} else {
			rows = renameRows(src, dst, records, device, journalNow())
		}
		if err := ctx.appendTransactionLocked(txid, device, rows); err != nil {
			_ = os.Rename(target, source)
			return err
		}
		return nil
	})
	if err != nil {
		return err
	}
	fmt.Printf("%s\x00OK\x00", magic)
	return nil
}

func failBrowse(magic, reason string) error {
	fmt.Fprintf(os.Stderr, "browse-rename: %s: %s\n", magic, reason)
	fmt.Printf("%s\x00ERROR\x00%s\x00", magic, reason)
	return exitCode(1)
}
