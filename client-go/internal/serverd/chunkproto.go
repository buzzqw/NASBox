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
)

func readNULString(reader *bufio.Reader) (string, error) {
	value, err := reader.ReadString('\x00')
	if err != nil {
		return "", err
	}
	return strings.TrimSuffix(value, "\x00"), nil
}

// validatePublishContent keeps empty files representable without requiring a
// synthetic chunk. Every non-empty file must carry at least one chunk.
func validatePublishContent(digest, sizeRaw string, chunks []string) error {
	size, err := strconv.ParseInt(sizeRaw, 10, 64)
	if err != nil || size < 0 {
		return exitCode(1)
	}
	if size == 0 {
		empty := sha256.Sum256(nil)
		if len(chunks) != 0 || digest != hex.EncodeToString(empty[:]) {
			return exitCode(1)
		}
		return nil
	}
	if len(chunks) == 0 {
		return exitCode(1)
	}
	return nil
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
	row, ok, err := ctx.lookupManifest(args[0])
	if err != nil {
		return err
	}
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

// ingestFileChunks registers an existing NAS file in the chunk store and
// writes FILE_CHUNKS framing. It never touches the journal.
func ingestFileChunks(ctx *Context, full string, out io.Writer) error {
	info, err := os.Lstat(full)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		fmt.Fprint(out, "FILE_CHUNKS_V1\x00MISS\x00")
		return nil
	}
	store, err := ctx.chunkStore()
	if err != nil {
		return err
	}
	chunkSize := ctx.ChunkSize()
	file, err := os.Open(full)
	if err != nil {
		return err
	}
	defer file.Close()
	whole := sha256.New()
	digests := []string{}
	buffer := make([]byte, chunkSize)
	for {
		n, readErr := io.ReadFull(file, buffer)
		if n > 0 {
			block := buffer[:n]
			sum := sha256.Sum256(block)
			digest := hex.EncodeToString(sum[:])
			if _, err := store.Put(block); err != nil {
				return err
			}
			digests = append(digests, digest)
			_, _ = whole.Write(block)
		}
		if readErr == io.EOF || readErr == io.ErrUnexpectedEOF {
			break
		}
		if readErr != nil {
			return err
		}
	}
	digest := hex.EncodeToString(whole.Sum(nil))
	if err := store.SaveFileMeta(digest, digests); err != nil {
		return err
	}
	writer := bufio.NewWriter(out)
	defer writer.Flush()
	fmt.Fprintf(writer, "FILE_CHUNKS_V1\x00%s\x00%d\x00%d\x00%d\x00", digest, info.Size(), info.ModTime().UnixNano(), len(digests))
	for _, chunk := range digests {
		fmt.Fprintf(writer, "%s\x00", chunk)
	}
	return nil
}

// cmdChunkIngest registers a file that already exists on the NAS but has no
// chunk metadata (out-of-band edit, legacy file). The manifest already
// describes it, so no journal write is needed.
func cmdChunkIngest(ctx *Context, args []string) error {
	if len(args) != 1 || !validRelative(args[0]) {
		return exitCode(1)
	}
	return ingestFileChunks(ctx, filepath.Join(ctx.ShareRoot, args[0]), os.Stdout)
}

// cmdHistoryChunks serves a version kept in .sync-trash. Trash paths are
// internal for the manifest but valid here: they are the history store.
func cmdHistoryChunks(ctx *Context, args []string) error {
	if len(args) != 1 || args[0] == "" || filepath.IsAbs(args[0]) {
		return exitCode(1)
	}
	for _, component := range strings.Split(args[0], "/") {
		if component == ".." || component == ".nasbox-staging" || component == ".sync-partial" || component == "@eaDir" {
			return exitCode(1)
		}
	}
	return ingestFileChunks(ctx, filepath.Join(ctx.ShareRoot, ".sync-trash", args[0]), os.Stdout)
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
	if _, err := strconv.ParseInt(mtime, 10, 64); err != nil {
		return exitCode(1)
	}
	causal, err := readNULString(reader)
	if err != nil {
		return exitCode(1)
	}
	if causal != "" && !causalPattern.MatchString(causal) {
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
	chunks := make([]string, 0, count)
	for i := 0; i < count; i++ {
		chunk, err := readNULString(reader)
		if err != nil || !digest64.MatchString(chunk) {
			return exitCode(1)
		}
		chunks = append(chunks, chunk)
	}
	if err := validatePublishContent(digest, sizeRaw, chunks); err != nil {
		return err
	}
	record := publishRecord{
		path: path, digest: digest, size: sizeRaw, mtime: mtime, causal: causal,
		chunks: chunks,
	}
	if err := ctx.publishChunkRecords(device, txid, runTS, "replace", []publishRecord{record}); err != nil {
		return err
	}
	fmt.Print("FILE_PUBLISH_V1\x00OK\x00")
	return nil
}
