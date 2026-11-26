package remote

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func fakeRunStreamCommand(t *testing.T, fn streamCommandFunc) {
	t.Helper()
	old := runStreamCommand
	runStreamCommand = fn
	t.Cleanup(func() { runStreamCommand = old })
}

func streamTestDigest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func TestChunkPutBatchStreamUsesBoundedCompressedFraming(t *testing.T) {
	path := filepath.Join(t.TempDir(), "large.bin")
	content := []byte(strings.Repeat("aaaabbbbcc", 1000))
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
	const chunkSize = 4096
	digests := make([]string, 0, len(content)/chunkSize+1)
	for start := 0; start < len(content); start += chunkSize {
		end := start + chunkSize
		if end > len(content) {
			end = len(content)
		}
		digests = append(digests, streamTestDigest(content[start:end]))
	}
	var captured []byte
	fakeRunStreamCommand(t, func(_ context.Context, stdin io.Reader, _ string, _ ...string) ([]byte, []byte, error) {
		var err error
		captured, err = io.ReadAll(stdin)
		return []byte("CHUNK_PUT_V1\x00" + strconv.Itoa(len(digests)) + "\x00"), nil, err
	})
	client := New(testConfig())
	stored, err := client.ChunkPutBatchStream(context.Background(), "repo", path, digests, digests, chunkSize, int64(len(content)), 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if stored != len(digests) {
		t.Fatalf("stored=%d, want %d", stored, len(digests))
	}
	var want bytes.Buffer
	want.WriteString("CHUNK_PUT_COMPRESSED_V1\x00" + strconv.Itoa(len(digests)) + "\x00")
	for index, digest := range digests {
		start := index * chunkSize
		end := start + chunkSize
		if end > len(content) {
			end = len(content)
		}
		block := content[start:end]
		wire, encoding := compressChunk(block)
		want.WriteString(digest + "\x00" + strconv.Itoa(len(block)) + "\x00" + encoding + "\x00" + strconv.Itoa(len(wire)) + "\x00")
		want.Write(wire)
	}
	if !bytes.Equal(captured, want.Bytes()) {
		t.Fatalf("stream framing diverso:\n got %q\nwant %q", captured, want.Bytes())
	}
}

func TestChunkPutBatchStreamSendsDuplicateDigestOnlyOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "duplicate.bin")
	if err := os.WriteFile(path, []byte("aaaaaaaa"), 0o600); err != nil {
		t.Fatal(err)
	}
	digest := streamTestDigest([]byte("aaaa"))
	var captured []byte
	fakeRunStreamCommand(t, func(_ context.Context, stdin io.Reader, _ string, _ ...string) ([]byte, []byte, error) {
		captured, _ = io.ReadAll(stdin)
		return []byte("CHUNK_PUT_V1\x001\x00"), nil, nil
	})
	client := New(testConfig())
	if _, err := client.ChunkPutBatchStream(context.Background(), "repo", path, []string{digest, digest}, []string{digest}, 4, 8, 0, nil); err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(captured, []byte("CHUNK_PUT_COMPRESSED_V1\x001\x00")) {
		t.Fatalf("conteggio dichiarato errato: %q", captured)
	}
	if bytes.Count(captured, []byte(digest)) != 1 {
		t.Fatalf("digest duplicato inviato più volte: %q", captured)
	}
}

func TestChunkPutBatchStreamRejectsChangedFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "changed.bin")
	if err := os.WriteFile(path, []byte("bbbb"), 0o600); err != nil {
		t.Fatal(err)
	}
	digest := streamTestDigest([]byte("aaaa"))
	fakeRunStreamCommand(t, func(_ context.Context, stdin io.Reader, _ string, _ ...string) ([]byte, []byte, error) {
		_, _ = io.Copy(io.Discard, stdin)
		return []byte("CHUNK_PUT_V1\x001\x00"), nil, nil
	})
	client := New(testConfig())
	if _, err := client.ChunkPutBatchStream(context.Background(), "repo", path, []string{digest}, []string{digest}, 4, 4, 0, nil); err == nil {
		t.Fatal("file cambiato accettato")
	}
}

func TestChunkPutBatchStreamAllowsEmptyFileWithoutCommand(t *testing.T) {
	path := filepath.Join(t.TempDir(), "empty.bin")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	fakeRunStreamCommand(t, func(_ context.Context, _ io.Reader, _ string, _ ...string) ([]byte, []byte, error) {
		t.Fatal("file vuoto non deve aprire SSH")
		return nil, nil, nil
	})
	client := New(testConfig())
	stored, err := client.ChunkPutBatchStream(context.Background(), "repo", path, nil, nil, 1024, 0, 0, nil)
	if err != nil || stored != 0 {
		t.Fatalf("empty file: stored=%d err=%v", stored, err)
	}
}

func TestChunkMissingParsesServerResponse(t *testing.T) {
	digest := streamTestDigest([]byte("missing"))
	var request []byte
	fakeRunStreamCommand(t, func(_ context.Context, stdin io.Reader, _ string, _ ...string) ([]byte, []byte, error) {
		request, _ = io.ReadAll(stdin)
		return []byte("CHUNK_HAS_V1\x001\x00" + digest + "\x00"), nil, nil
	})
	client := New(testConfig())
	missing, err := client.ChunkMissing(context.Background(), "repo", []string{digest})
	if err != nil || len(missing) != 1 || missing[0] != digest {
		t.Fatalf("missing=%v err=%v", missing, err)
	}
	if string(request) != "CHUNK_HAS_V1\x001\x00"+digest+"\x00" {
		t.Fatalf("richiesta chunk-has errata: %q", request)
	}
}
