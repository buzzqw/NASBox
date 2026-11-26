package serverd

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

var (
	tokenPattern  = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)
	causalPattern = regexp.MustCompile(`^(?:[A-Za-z0-9._:-]+:[1-9][0-9]*(?:,[A-Za-z0-9._:-]+:[1-9][0-9]*)*)?$`)
	runTSPattern  = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}--[0-9]{2}-[0-9]{2}-[0-9]{2}-[0-9]{6}Z$`)
)

func mapLeaseError(err error) error {
	if errors.Is(err, errNotLeaseOwned) {
		fmt.Fprintln(os.Stderr, err.Error())
		return exitCode(75)
	}
	return err
}

func cmdJournalAppend(ctx *Context) error {
	fields, err := readNULFields(os.Stdin)
	if err != nil {
		return err
	}
	if len(fields) == 0 {
		return exitCode(1)
	}
	magic := fields[0]
	causal := false
	index := 1
	switch magic {
	case "JOURNAL_V1":
	case "JOURNAL_V2", "JOURNAL_V3":
		causal = magic == "JOURNAL_V3"
		if len(fields) < 2 || fields[1] != repositoryID(ctx.ShareRoot) {
			return exitCode(1)
		}
		index = 2
	default:
		return exitCode(1)
	}
	if len(fields) < index+4 {
		return exitCode(1)
	}
	txid, device := fields[index], fields[index+1]
	count, err := strconv.Atoi(fields[index+3])
	if err != nil || count < 0 || count > 100000 || len(fields) < index+4+count*(4+boolInt(causal)) {
		return exitCode(1)
	}
	if !tokenPattern.MatchString(txid) || !tokenPattern.MatchString(device) {
		return exitCode(1)
	}
	if err := ctx.requireLease(device); err != nil {
		return mapLeaseError(err)
	}
	rows := make([]string, 0, count)
	offset := index + 4
	for i := 0; i < count; i++ {
		action := fields[offset]
		path := fields[offset+1]
		digest := fields[offset+2]
		size := fields[offset+3]
		mtime := fields[offset+4]
		causalValue := ""
		if causal {
			causalValue = fields[offset+5]
			offset += 6
		} else {
			offset += 5
		}
		if action != "PUT" && action != "DELETE" || !validRelative(path) {
			return exitCode(1)
		}
		if (action == "PUT" || digest != "") && !digest64.MatchString(digest) {
			return exitCode(1)
		}
		if _, err := strconv.ParseInt(size, 10, 64); err != nil {
			return exitCode(1)
		}
		if _, err := strconv.ParseInt(mtime, 10, 64); err != nil {
			return exitCode(1)
		}
		if causalValue != "" && !causalPattern.MatchString(causalValue) {
			return exitCode(1)
		}
		encodedPath := encodeField(path)
		encodedDevice := encodeField(device)
		ts := journalNow()
		if action == "PUT" {
			rows = append(rows, journalRowPUT(encodedPath, digest, size, mtime, encodedDevice, ts, causalValue))
		} else {
			rows = append(rows, journalRowDELETE(encodedPath, digest, size, mtime, encodedDevice, ts))
		}
	}
	if err := ctx.appendTransaction(txid, device, rows); err != nil {
		return err
	}
	fmt.Printf("Journal aggiornato: %d record (transazione %s).\n", count, txid)
	return nil
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func sha256File(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := bufio.NewReader(file).WriteTo(hash); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func fileMTimeNS(info os.FileInfo) string {
	return strconv.FormatInt(info.ModTime().Unix()*1_000_000_000, 10)
}

func validStagingDir(root, staging string) bool {
	base := filepath.Join(root, ".nasbox-staging")
	if !strings.HasPrefix(staging, base+string(os.PathSeparator)) {
		return false
	}
	info, err := os.Lstat(staging)
	return err == nil && info.IsDir() && !pathHasSymlinkAncestor(root, strings.TrimPrefix(staging, root+string(os.PathSeparator)))
}

func cmdStagingPublish(ctx *Context) error {
	fields, err := readNULFields(os.Stdin)
	if err != nil {
		return err
	}
	if len(fields) < 5 {
		return exitCode(1)
	}
	magic := fields[0]
	if magic != "STAGING_PUBLISH_V1" && magic != "STAGING_PUBLISH_V2" {
		return exitCode(1)
	}
	causal := magic == "STAGING_PUBLISH_V2"
	staging, txid, device := fields[1], fields[2], fields[3]
	count, err := strconv.Atoi(fields[4])
	if err != nil || count < 1 || count > 100000 {
		return exitCode(1)
	}
	perRecord := 4 + boolInt(causal)
	if len(fields) < 5+count*perRecord {
		return exitCode(1)
	}
	if !tokenPattern.MatchString(txid) || !tokenPattern.MatchString(device) || !validStagingDir(ctx.ShareRoot, staging) {
		return exitCode(1)
	}
	type record struct {
		path, digest, size, mtime, causal string
	}
	records := make([]record, 0, count)
	seen := map[string]bool{}
	offset := 5
	for i := 0; i < count; i++ {
		rec := record{path: fields[offset], digest: fields[offset+1], size: fields[offset+2], mtime: fields[offset+3]}
		if causal {
			rec.causal = fields[offset+4]
		}
		offset += perRecord
		if seen[rec.path] || !validRelative(rec.path) || !digest64.MatchString(rec.digest) {
			return exitCode(1)
		}
		seen[rec.path] = true
		if _, err := strconv.ParseInt(rec.size, 10, 64); err != nil {
			return exitCode(1)
		}
		if _, err := strconv.ParseInt(rec.mtime, 10, 64); err != nil {
			return exitCode(1)
		}
		if rec.causal != "" && !causalPattern.MatchString(rec.causal) {
			return exitCode(1)
		}
		staged := filepath.Join(staging, rec.path)
		info, err := os.Lstat(staged)
		if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return exitCode(1)
		}
		if strconv.FormatInt(info.Size(), 10) != rec.size {
			return exitCode(1)
		}
		mtimeNS, _ := strconv.ParseInt(rec.mtime, 10, 64)
		if strconv.FormatInt(info.ModTime().Unix(), 10) != strconv.FormatInt(mtimeNS/1_000_000_000, 10) {
			return exitCode(1)
		}
		rec.mtime = fileMTimeNS(info)
		digest, err := sha256File(staged)
		if err != nil || digest != rec.digest {
			return exitCode(1)
		}
		records = append(records, rec)
	}
	if err := ctx.requireLease(device); err != nil {
		return mapLeaseError(err)
	}
	return ctx.withJournalLock(func() error {
		present := 0
		for _, rec := range records {
			target := filepath.Join(ctx.ShareRoot, rec.path)
			info, err := os.Lstat(target)
			if err == nil {
				if !info.Mode().IsRegular() || strconv.FormatInt(info.Size(), 10) != rec.size {
					return exitCode(1)
				}
				digest, err := sha256File(target)
				if err != nil || digest != rec.digest {
					return exitCode(1)
				}
				present++
			} else if !os.IsNotExist(err) {
				return exitCode(1)
			}
		}
		if present > 0 && present < len(records) {
			return exitCode(1)
		}
		if present == len(records) {
			if err := os.RemoveAll(staging); err != nil {
				return err
			}
			fmt.Printf("%s\x00OK\x00%d\x00", magic, count)
			return nil
		}
		for _, rec := range records {
			source := filepath.Join(staging, rec.path)
			target := filepath.Join(ctx.ShareRoot, rec.path)
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			if err := os.Rename(source, target); err != nil {
				return err
			}
		}
		rows := make([]string, 0, count)
		ts := journalNow()
		for _, rec := range records {
			rows = append(rows, journalRowPUT(encodeField(rec.path), rec.digest, rec.size, rec.mtime, encodeField(device), ts, rec.causal))
		}
		if err := ctx.appendTransactionLocked(txid, device, rows); err != nil {
			return err
		}
		if err := os.RemoveAll(staging); err != nil {
			return err
		}
		fmt.Printf("%s\x00OK\x00%d\x00", magic, count)
		return nil
	})
}
