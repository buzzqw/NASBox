package serverd

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

func readNULString(reader *bufio.Reader) (string, error) {
	value, err := reader.ReadString('\x00')
	if err != nil {
		return "", err
	}
	return strings.TrimSuffix(value, "\x00"), nil
}

// cmdChunkPutBatch stores chunks sent by a client. Framing is NUL for the
// digest/size fields and raw bytes of exactly `size` after them, so binary
// chunk contents are never confused with delimiters.
func cmdChunkPutBatch(ctx *Context) error {
	reader := bufio.NewReader(os.Stdin)
	magic, err := readNULString(reader)
	if err != nil || magic != "CHUNK_PUT_V1" {
		return exitCode(1)
	}
	countRaw, err := readNULString(reader)
	if err != nil {
		return exitCode(1)
	}
	count, err := strconv.Atoi(countRaw)
	if err != nil || count < 0 || count > 100000 {
		return exitCode(1)
	}
	store, err := ctx.chunkStore()
	if err != nil {
		return err
	}
	stored := 0
	for i := 0; i < count; i++ {
		digest, err := readNULString(reader)
		if err != nil || !digest64.MatchString(digest) {
			return exitCode(1)
		}
		sizeRaw, err := readNULString(reader)
		if err != nil {
			return exitCode(1)
		}
		size, err := strconv.ParseInt(sizeRaw, 10, 64)
		if err != nil || size < 0 || size > 64*1024*1024 {
			return exitCode(1)
		}
		data := make([]byte, size)
		if _, err := io.ReadFull(reader, data); err != nil {
			return exitCode(1)
		}
		sum := sha256.Sum256(data)
		if hex.EncodeToString(sum[:]) != digest {
			return exitCode(1)
		}
		if !store.Has(digest) {
			stored++
		}
		if _, err := store.Put(data); err != nil {
			return err
		}
	}
	fmt.Printf("CHUNK_PUT_V1\x00%d\x00%d\x00", stored, count)
	return nil
}

func cmdChunkGet(ctx *Context, args []string) error {
	if len(args) != 1 || !digest64.MatchString(args[0]) {
		return exitCode(1)
	}
	store, err := ctx.chunkStore()
	if err != nil {
		return err
	}
	file, size, err := store.Open(args[0])
	if err != nil {
		return exitCode(1)
	}
	defer file.Close()
	out := bufio.NewWriter(os.Stdout)
	defer out.Flush()
	fmt.Fprintf(out, "CHUNK_GET_V1\x00%d\x00", size)
	if _, err := io.CopyN(out, file, size); err != nil {
		return err
	}
	return nil
}

func cmdFileChunks(ctx *Context, args []string) error {
	if len(args) != 1 || !validRelative(args[0]) {
		return exitCode(1)
	}
	rows, err := ctx.renderManifest()
	if err != nil {
		return err
	}
	row, ok := manifestLookup(rows, args[0])
	if !ok {
		fmt.Print("FILE_CHUNKS_V1\x00MISS\x00")
		return nil
	}
	fields := strings.Split(row, "\t")
	if len(fields) < 6 || !digest64.MatchString(fields[1]) {
		fmt.Print("FILE_CHUNKS_V1\x00MISS\x00")
		return nil
	}
	store, err := ctx.chunkStore()
	if err != nil {
		return err
	}
	chunks, err := store.LoadFileMeta(fields[1])
	if err != nil {
		fmt.Print("FILE_CHUNKS_V1\x00MISS\x00")
		return nil
	}
	out := bufio.NewWriter(os.Stdout)
	defer out.Flush()
	fmt.Fprintf(out, "FILE_CHUNKS_V1\x00%s\x00%s\x00%s\x00%d\x00", fields[1], fields[2], fields[3], len(chunks))
	for _, digest := range chunks {
		fmt.Fprintf(out, "%s\x00", digest)
	}
	return nil
}

func cmdFilePublish(ctx *Context, args []string) error {
	if len(args) != 3 {
		return exitCode(1)
	}
	device, txid, runTS := args[0], args[1], args[2]
	if !tokenPattern.MatchString(device) || !tokenPattern.MatchString(txid) || !runTSPattern.MatchString(runTS) {
		return exitCode(1)
	}
	reader := bufio.NewReader(os.Stdin)
	magic, err := readNULString(reader)
	if err != nil || magic != "FILE_PUBLISH_V1" {
		return exitCode(1)
	}
	path, err := readNULString(reader)
	if err != nil || !validRelative(path) {
		return exitCode(1)
	}
	digest, err := readNULString(reader)
	if err != nil || !digest64.MatchString(digest) {
		return exitCode(1)
	}
	sizeRaw, err := readNULString(reader)
	if err != nil {
		return exitCode(1)
	}
	mtime, err := readNULString(reader)
	if err != nil {
		return exitCode(1)
	}
	causal, err := readNULString(reader)
	if err != nil {
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
	chunks := make([]string, 0, count)
	for i := 0; i < count; i++ {
		chunk, err := readNULString(reader)
		if err != nil || !digest64.MatchString(chunk) {
			return exitCode(1)
		}
		chunks = append(chunks, chunk)
	}
	if err := ctx.requireLease(device); err != nil {
		return mapLeaseError(err)
	}
	store, err := ctx.chunkStore()
	if err != nil {
		return err
	}
	target := filepath.Join(ctx.ShareRoot, path)
	backup := filepath.Join(ctx.ShareRoot, ".sync-trash", path+"-"+runTS)
	err = ctx.withJournalLock(func() error {
		if _, err := os.Lstat(target); err == nil {
			if err := os.MkdirAll(filepath.Dir(backup), 0o755); err != nil {
				return err
			}
			if err := os.Rename(target, backup); err != nil {
				return err
			}
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		if err := store.Assemble(chunks, target); err != nil {
			return err
		}
		requestedSeconds, _ := strconv.ParseInt(mtime, 10, 64)
		stamp := time.Unix(requestedSeconds/1_000_000_000, 0)
		if err := os.Chtimes(target, stamp, stamp); err != nil {
			return err
		}
		info, err := os.Lstat(target)
		mtimeNS, _ := strconv.ParseInt(mtime, 10, 64)
		if err != nil || strconv.FormatInt(info.Size(), 10) != sizeRaw ||
			strconv.FormatInt(info.ModTime().Unix(), 10) != strconv.FormatInt(mtimeNS/1_000_000_000, 10) {
			if _, statErr := os.Lstat(backup); statErr == nil {
				_ = os.Rename(backup, target)
			}
			return exitCode(1)
		}
		mtime = fileMTimeNS(info)
		actualDigest, err := sha256File(target)
		if err != nil || actualDigest != digest {
			if _, statErr := os.Lstat(backup); statErr == nil {
				_ = os.Rename(backup, target)
			}
			return exitCode(1)
		}
		if err := store.SaveFileMeta(digest, chunks); err != nil {
			return err
		}
		rows := []string{journalRowPUT(encodeField(path), digest, sizeRaw, mtime, encodeField(device), journalNow(), causal)}
		return ctx.appendTransactionLocked(txid, device, rows)
	})
	if err != nil {
		return err
	}
	fmt.Print("FILE_PUBLISH_V1\x00OK\x00")
	return nil
}
