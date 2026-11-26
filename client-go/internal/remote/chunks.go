package remote

import (
	"bytes"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// ChunkUpload is one content-addressed block sent to the NAS.
type ChunkUpload struct {
	Digest string
	Data   []byte
}

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

// ChunkGet downloads one chunk. Binary data follows a NUL-terminated header.
func (c *Client) ChunkGet(digest string) ([]byte, error) {
	output, err := c.Run([]string{c.script(), "--chunk-get", digest}, nil, 5*time.Minute)
	if err != nil {
		return nil, err
	}
	parts := bytes.SplitN(output, []byte{0}, 3)
	if len(parts) < 3 || string(parts[0]) != "CHUNK_GET_V1" {
		return nil, fmt.Errorf("risposta chunk-get non valida")
	}
	size, err := strconv.Atoi(string(parts[1]))
	if err != nil || size < 0 || len(parts[2]) < size {
		return nil, fmt.Errorf("chunk %s troncato", digest)
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
func (c *Client) FilePublishBatch(device, txid, runTS string, files []ChunkPublishFile, useLease bool) error {
	if len(files) == 0 {
		return nil
	}
	var payload bytes.Buffer
	payload.WriteString("FILE_PUBLISH_BATCH_V1\x00")
	payload.WriteString(strconv.Itoa(len(files)) + "\x00")
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
	if len(chunks) == 0 {
		return nil, fmt.Errorf("nessun chunk da pubblicare")
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
