package remote

import (
	"bytes"
	"compress/zlib"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// ChunkUpload is one content-addressed block sent to the NAS.
type ChunkUpload struct {
	Digest string
	Data   []byte
}

const emptyFileDigest = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"

var digest64Remote = regexp.MustCompile(`^[0-9a-f]{64}$`)

// ChunkPutBatch uploads only chunks the NAS does not already have. The framing
// is NUL for the text fields and raw bytes of exactly len(Data) afterwards.
func (c *Client) ChunkPutBatch(device string, chunks []ChunkUpload) (int, error) {
	if len(chunks) == 0 {
		return 0, nil
	}
	var payload bytes.Buffer
	payload.WriteString("CHUNK_PUT_V1\x00")
	payload.WriteString(strconv.Itoa(len(chunks)) + "\x00")
	for _, chunk := range chunks {
		payload.WriteString(chunk.Digest + "\x00")
		payload.WriteString(strconv.Itoa(len(chunk.Data)) + "\x00")
		payload.Write(chunk.Data)
	}
	output, err := c.Run([]string{c.script(), "--chunk-put-batch"}, payload.Bytes(), 10*time.Minute)
	if err != nil {
		return 0, err
	}
	fields := bytes.Split(output, []byte{0})
	if len(fields) < 3 || string(fields[0]) != "CHUNK_PUT_V1" {
		return 0, fmt.Errorf("risposta chunk-put non valida")
	}
	stored, _ := strconv.Atoi(string(fields[1]))
	return stored, nil
}

// ChunkMissing asks the NAS which objects are absent before any file bytes are
// sent. This is the network-side half of content-addressed deduplication.
func (c *Client) ChunkMissing(ctx context.Context, device string, digests []string) ([]string, error) {
	if len(digests) == 0 {
		return nil, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	var payload bytes.Buffer
	payload.WriteString("CHUNK_HAS_V1\x00")
	payload.WriteString(strconv.Itoa(len(digests)) + "\x00")
	for _, digest := range digests {
		payload.WriteString(digest + "\x00")
	}
	output, err := c.runStream(ctx, []string{c.script(), "--chunk-has"}, &payload, 2*time.Minute)
	if err != nil {
		return nil, err
	}
	fields := bytes.Split(output, []byte{0})
	if len(fields) < 2 || string(fields[0]) != "CHUNK_HAS_V1" {
		return nil, fmt.Errorf("risposta chunk-has non valida")
	}
	count, err := strconv.Atoi(string(fields[1]))
	if err != nil || count < 0 || count > len(fields)-2 {
		return nil, fmt.Errorf("conteggio chunk-has non valido")
	}
	missing := make([]string, 0, count)
	for index := 0; index < count; index++ {
		digest := string(fields[2+index])
		if !digest64Remote.MatchString(digest) {
			return nil, fmt.Errorf("chunk-has digest non valido")
		}
		missing = append(missing, digest)
	}
	return missing, nil
}

// ChunkPutBatchStream sends only missing chunks, compressing each one when that
// reduces its size. The NAS stores the original raw bytes, so existing objects
// and readers remain compatible.
func (c *Client) ChunkPutBatchStream(
	ctx context.Context, device, local string, digests, missing []string, chunkSize int, expectedSize int64,
	bytesPerSecond int64, onProgress func(int64),
) (int, error) {
	if chunkSize <= 0 {
		chunkSize = 1024 * 1024
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	file, err := os.Open(local)
	if err != nil {
		return 0, err
	}
	if len(missing) == 0 {
		file.Close()
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		return 0, nil
	}
	missingSet := make(map[string]bool, len(missing))
	for _, digest := range missing {
		missingSet[digest] = true
	}
	reader, writer := io.Pipe()
	done := make(chan error, 1)
	streamDone := make(chan struct{})
	go func() {
		defer close(streamDone)
		var outputWriter io.Writer = writer
		if bytesPerSecond > 0 {
			outputWriter = &rateLimitedWriter{ctx: ctx, writer: writer, limit: bytesPerSecond}
		}
		err := writeChunkStream(outputWriter, file, digests, missingSet, chunkSize, expectedSize, onProgress)
		_ = file.Close()
		_ = writer.CloseWithError(err)
		done <- err
	}()
	go func() {
		select {
		case <-ctx.Done():
			_ = reader.CloseWithError(ctx.Err())
		case <-streamDone:
		}
	}()
	output, runErr := c.runStream(ctx, []string{c.script(), "--chunk-put-compressed"}, reader, 10*time.Minute)
	_ = reader.CloseWithError(runErr)
	writeErr := <-done
	if runErr != nil {
		return 0, runErr
	}
	if writeErr != nil {
		return 0, writeErr
	}
	fields := bytes.Split(output, []byte{0})
	if len(fields) < 3 || string(fields[0]) != "CHUNK_PUT_V1" {
		return 0, fmt.Errorf("risposta chunk-put non valida")
	}
	stored, err := strconv.Atoi(string(fields[1]))
	if err != nil || stored < 0 || stored > len(missing) {
		return 0, fmt.Errorf("conteggio chunk-put non valido")
	}
	return stored, nil
}

func writeChunkStream(writer io.Writer, file io.Reader, digests []string, missing map[string]bool, chunkSize int, expectedSize int64, onProgress func(int64)) error {
	if _, err := io.WriteString(writer, "CHUNK_PUT_COMPRESSED_V1\x00"+strconv.Itoa(len(missing))+"\x00"); err != nil {
		return err
	}
	buffer := make([]byte, chunkSize)
	var total int64
	for _, expected := range digests {
		n, err := io.ReadFull(file, buffer)
		if err != nil && !(err == io.ErrUnexpectedEOF && n > 0) {
			return fmt.Errorf("file cambiato o troncato durante il caricamento: %w", err)
		}
		block := buffer[:n]
		sum := sha256.Sum256(block)
		actual := hex.EncodeToString(sum[:])
		if actual != expected {
			return fmt.Errorf("file locale cambiato durante il caricamento: chunk atteso %s, trovato %s", expected, actual)
		}
		if missing[expected] {
			wire, encoding := compressChunk(block)
			if _, err := io.WriteString(writer, expected+"\x00"+strconv.Itoa(n)+"\x00"+encoding+"\x00"+strconv.Itoa(len(wire))+"\x00"); err != nil {
				return err
			}
			if _, err := writer.Write(wire); err != nil {
				return err
			}
			if onProgress != nil {
				onProgress(int64(n))
			}
			delete(missing, expected)
		}
		total += int64(n)
	}
	var extra [1]byte
	n, err := file.Read(extra[:])
	if n > 0 {
		return fmt.Errorf("file locale cresciuto durante il caricamento")
	}
	if err != nil && err != io.EOF {
		return err
	}
	if total != expectedSize {
		return fmt.Errorf("dimensione locale cambiata durante il caricamento: attesa %d, letta %d", expectedSize, total)
	}
	return nil
}

func compressChunk(block []byte) ([]byte, string) {
	var compressed bytes.Buffer
	writer, err := zlib.NewWriterLevel(&compressed, zlib.BestSpeed)
	if err != nil {
		return block, "none"
	}
	if _, err := writer.Write(block); err != nil {
		_ = writer.Close()
		return block, "none"
	}
	if err := writer.Close(); err != nil {
		return block, "none"
	}
	if compressed.Len() < len(block) {
		return compressed.Bytes(), "zlib"
	}
	return block, "none"
}

// ChunkGet downloads one chunk. Binary data follows a NUL-terminated header.
func (c *Client) ChunkGet(digest string) ([]byte, error) {
	return c.ChunkGetContext(context.Background(), digest)
}

// ChunkGetContext is ChunkGet with cancellation from the active sync run.
func (c *Client) ChunkGetContext(ctx context.Context, digest string) ([]byte, error) {
	return c.ChunkGetContextLimited(ctx, digest, 0)
}

// ChunkGetContextLimited also applies an average download limit. SSH may buffer
// one chunk, but waiting before returning it keeps sustained throughput bounded.
func (c *Client) ChunkGetContextLimited(ctx context.Context, digest string, bytesPerSecond int64) ([]byte, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	quoted := []string{c.script(), "--chunk-get", digest}
	args := make([]string, 0, len(quoted))
	for _, arg := range quoted {
		args = append(args, shellQuote(arg))
	}
	command := c.Builder().Args(strings.Join(args, " "))
	output, stderr, err := runCommand(ctx, nil, command[0], command[1:]...)
	if err != nil {
		return nil, fmt.Errorf("ssh: %w: %s", err, cleanSSHStderr(string(stderr)))
	}
	parts := bytes.SplitN(output, []byte{0}, 3)
	if len(parts) < 3 || string(parts[0]) != "CHUNK_GET_V1" {
		return nil, fmt.Errorf("risposta chunk-get non valida")
	}
	size, err := strconv.Atoi(string(parts[1]))
	if err != nil || size < 0 || len(parts[2]) < size {
		return nil, fmt.Errorf("chunk %s troncato", digest)
	}
	if err := waitTransferLimit(ctx, bytesPerSecond, int64(size)); err != nil {
		return nil, err
	}
	return parts[2][:size], nil
}

// FileChunks describes a published file in terms of its chunks.
type FileChunks struct {
	Digest  string
	Size    int64
	MTimeNS int64
	Chunks  []string
	Found   bool
}

func (c *Client) FileChunks(path string) (FileChunks, error) {
	output, err := c.Run([]string{c.script(), "--file-chunks", path}, nil, 2*time.Minute)
	if err != nil {
		return FileChunks{}, err
	}
	fields := bytes.Split(output, []byte{0})
	if len(fields) == 0 || string(fields[0]) != "FILE_CHUNKS_V1" {
		return FileChunks{}, fmt.Errorf("risposta file-chunks non valida")
	}
	if len(fields) < 2 || string(fields[1]) == "MISS" {
		return FileChunks{}, nil
	}
	if len(fields) < 5 {
		return FileChunks{}, fmt.Errorf("risposta file-chunks troncata")
	}
	result := FileChunks{
		Digest: string(fields[1]), Found: true,
		Chunks: []string{},
	}
	result.Size, _ = strconv.ParseInt(string(fields[2]), 10, 64)
	result.MTimeNS, _ = strconv.ParseInt(string(fields[3]), 10, 64)
	count, _ := strconv.Atoi(string(fields[4]))
	for i := 0; i < count && 5+i < len(fields); i++ {
		result.Chunks = append(result.Chunks, string(fields[5+i]))
	}
	return result, nil
}

// IngestFileChunks registers an existing NAS file in the chunk store. It is
// used when --file-chunks has no metadata yet (out-of-band edit or legacy file).
func (c *Client) IngestFileChunks(path string) (FileChunks, error) {
	output, err := c.Run([]string{c.script(), "--chunk-ingest", path}, nil, 2*time.Minute)
	if err != nil {
		return FileChunks{}, err
	}
	fields := bytes.Split(output, []byte{0})
	if len(fields) < 2 || string(fields[0]) != "FILE_CHUNKS_V1" {
		return FileChunks{}, fmt.Errorf("risposta chunk-ingest non valida")
	}
	if string(fields[1]) == "MISS" {
		return FileChunks{}, fmt.Errorf("file NAS non disponibile: %s", path)
	}
	result := FileChunks{Digest: string(fields[1]), Found: true, Chunks: []string{}}
	result.Size, _ = strconv.ParseInt(string(fields[2]), 10, 64)
	result.MTimeNS, _ = strconv.ParseInt(string(fields[3]), 10, 64)
	count, _ := strconv.Atoi(string(fields[4]))
	for i := 0; i < count && 5+i < len(fields); i++ {
		result.Chunks = append(result.Chunks, string(fields[5+i]))
	}
	return result, nil
}

// HistoryChunks returns the chunks of a version stored in .sync-trash.
func (c *Client) HistoryChunks(trashRel string) (FileChunks, error) {
	output, err := c.Run([]string{c.script(), "--history-chunks", trashRel}, nil, 2*time.Minute)
	if err != nil {
		return FileChunks{}, err
	}
	return parseFileChunksOutput(output)
}

func parseFileChunksOutput(output []byte) (FileChunks, error) {
	fields := bytes.Split(output, []byte{0})
	if len(fields) < 2 || string(fields[0]) != "FILE_CHUNKS_V1" {
		return FileChunks{}, fmt.Errorf("risposta file-chunks non valida")
	}
	if string(fields[1]) == "MISS" {
		return FileChunks{}, fmt.Errorf("versione NAS non disponibile")
	}
	if len(fields) < 5 {
		return FileChunks{}, fmt.Errorf("risposta file-chunks troncata")
	}
	result := FileChunks{Digest: string(fields[1]), Found: true, Chunks: []string{}}
	result.Size, _ = strconv.ParseInt(string(fields[2]), 10, 64)
	result.MTimeNS, _ = strconv.ParseInt(string(fields[3]), 10, 64)
	count, _ := strconv.Atoi(string(fields[4]))
	for i := 0; i < count && 5+i < len(fields); i++ {
		result.Chunks = append(result.Chunks, string(fields[5+i]))
	}
	return result, nil
}

// ChunkPublishFile is one entry of a batched journaled publish.
type ChunkPublishFile struct {
	Path, Digest, Causal string
	Size, MTimeNS        int64
	Chunks               []string
}

// FilePublishBatch publishes many files in a single journal transaction.
func (c *Client) FilePublishBatch(device, txid, runTS string, files []ChunkPublishFile, mode string, useLease bool) error {
	if len(files) == 0 {
		return nil
	}
	if mode != "create" && mode != "replace" {
		return fmt.Errorf("modalità pubblicazione non valida")
	}
	var payload bytes.Buffer
	payload.WriteString("FILE_PUBLISH_BATCH_V2\x00")
	payload.WriteString(strconv.Itoa(len(files)) + "\x00")
	payload.WriteString(mode + "\x00")
	for _, file := range files {
		for _, field := range []string{
			file.Path, file.Digest, strconv.FormatInt(file.Size, 10),
			strconv.FormatInt(file.MTimeNS, 10), file.Causal, strconv.Itoa(len(file.Chunks)),
		} {
			payload.WriteString(field + "\x00")
		}
		for _, digest := range file.Chunks {
			payload.WriteString(digest + "\x00")
		}
	}
	args := []string{c.script(), "--file-publish-batch", device, txid, runTS}
	var (
		output []byte
		err    error
	)
	if useLease {
		output, err = c.RunLocked(args, payload.Bytes(), device, 10*time.Minute)
	} else {
		output, err = c.Run(args, payload.Bytes(), 10*time.Minute)
	}
	if err != nil {
		return err
	}
	fields := bytes.Split(output, []byte{0})
	if len(fields) < 3 || string(fields[0]) != "FILE_PUBLISH_BATCH_V1" || string(fields[1]) != "OK" {
		return fmt.Errorf("pubblicazione batch fallita: %s", strings.TrimSpace(string(output)))
	}
	return nil
}

// FilePublish assembles a file from chunks on the NAS and records it in the
// journal under the caller's lease.
func (c *Client) FilePublish(
	path, digest string, size, mtimeNS int64, causal string, chunks []string,
	device, txid, runTS string,
) error {
	payload, err := c.filePublishPayload(path, digest, size, mtimeNS, causal, chunks)
	if err != nil {
		return err
	}
	output, err := c.RunLocked([]string{c.script(), "--file-publish", device, txid, runTS}, payload, device, 10*time.Minute)
	if err != nil {
		return err
	}
	return parseFilePublishOutput(output)
}

// FilePublishOwned is the same operation when the caller already holds the
// NAS lease (UploadReplace acquires it for the whole batch). Using RunLocked
// here would deadlock on the same flock.
func (c *Client) FilePublishOwned(
	path, digest string, size, mtimeNS int64, causal string, chunks []string,
	device, txid, runTS string,
) error {
	payload, err := c.filePublishPayload(path, digest, size, mtimeNS, causal, chunks)
	if err != nil {
		return err
	}
	output, err := c.Run([]string{c.script(), "--file-publish", device, txid, runTS}, payload, 10*time.Minute)
	if err != nil {
		return err
	}
	return parseFilePublishOutput(output)
}

func (c *Client) filePublishPayload(
	path, digest string, size, mtimeNS int64, causal string, chunks []string,
) ([]byte, error) {
	if len(chunks) == 0 && (size != 0 || digest != emptyFileDigest) {
		return nil, fmt.Errorf("file senza chunk non valido")
	}
	var payload bytes.Buffer
	for _, field := range []string{
		"FILE_PUBLISH_V1", path, digest, strconv.FormatInt(size, 10),
		strconv.FormatInt(mtimeNS, 10), causal, strconv.Itoa(len(chunks)),
	} {
		payload.WriteString(field + "\x00")
	}
	for _, chunk := range chunks {
		payload.WriteString(chunk + "\x00")
	}
	return payload.Bytes(), nil
}

func parseFilePublishOutput(output []byte) error {
	fields := bytes.Split(output, []byte{0})
	if len(fields) < 2 || string(fields[0]) != "FILE_PUBLISH_V1" || string(fields[1]) != "OK" {
		return fmt.Errorf("pubblicazione chunk fallita: %s", strings.TrimSpace(string(output)))
	}
	return nil
}
