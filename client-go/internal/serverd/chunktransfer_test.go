package serverd

import (
	"bytes"
	"compress/zlib"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"strconv"
	"strings"
	"testing"
)

func runChunkTransferInput(t *testing.T, input []byte, fn func() error) error {
	t.Helper()
	file, err := os.CreateTemp(t.TempDir(), "chunk-input-*")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if _, err := file.Write(input); err != nil {
		t.Fatal(err)
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	old := os.Stdin
	os.Stdin = file
	defer func() { os.Stdin = old }()
	return fn()
}

func chunkTransferDigest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func TestChunkHasReturnsOnlyMissingObjects(t *testing.T) {
	ctx, _ := newTestContext(t)
	store, err := ctx.chunkStore()
	if err != nil {
		t.Fatal(err)
	}
	existing := []byte("already present")
	existingDigest, err := store.Put(existing)
	if err != nil {
		t.Fatal(err)
	}
	missing := []byte("not present")
	missingDigest := chunkTransferDigest(missing)
	input := "CHUNK_HAS_V1\x002\x00" + existingDigest + "\x00" + missingDigest + "\x00"
	var output bytes.Buffer
	oldStdout := os.Stdout
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = writer
	err = runChunkTransferInput(t, []byte(input), func() error { return cmdChunkHas(ctx) })
	_ = writer.Close()
	os.Stdout = oldStdout
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(&output, reader)
	_ = reader.Close()
	want := "CHUNK_HAS_V1\x001\x00" + missingDigest + "\x00"
	if output.String() != want {
		t.Fatalf("chunk-has=%q, want %q", output.String(), want)
	}
}

func TestChunkPutCompressedStoresRawBytes(t *testing.T) {
	for _, encoding := range []string{"none", "zlib"} {
		t.Run(encoding, func(t *testing.T) {
			ctx, _ := newTestContext(t)
			store, err := ctx.chunkStore()
			if err != nil {
				t.Fatal(err)
			}
			raw := []byte(strings.Repeat("compressible chunk ", 1000))
			digest := chunkTransferDigest(raw)
			wire := raw
			if encoding == "zlib" {
				var compressed bytes.Buffer
				zw := zlib.NewWriter(&compressed)
				if _, err := zw.Write(raw); err != nil {
					t.Fatal(err)
				}
				if err := zw.Close(); err != nil {
					t.Fatal(err)
				}
				wire = compressed.Bytes()
			}
			var frame bytes.Buffer
			frame.WriteString("CHUNK_PUT_COMPRESSED_V1\x001\x00")
			frame.WriteString(digest + "\x00" + strconv.Itoa(len(raw)) + "\x00" + encoding + "\x00" + strconv.Itoa(len(wire)) + "\x00")
			frame.Write(wire)
			oldStdout := os.Stdout
			reader, writer, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			os.Stdout = writer
			err = runChunkTransferInput(t, frame.Bytes(), func() error { return cmdChunkPutCompressed(ctx) })
			_ = writer.Close()
			os.Stdout = oldStdout
			if err != nil {
				t.Fatal(err)
			}
			_, _ = io.Copy(io.Discard, reader)
			_ = reader.Close()
			file, _, err := store.Open(digest)
			if err != nil {
				t.Fatal(err)
			}
			defer file.Close()
			got, err := io.ReadAll(file)
			if err != nil || !bytes.Equal(got, raw) {
				t.Fatalf("oggetto salvato diverso: len=%d err=%v", len(got), err)
			}
			sum := sha256.Sum256(got)
			if hex.EncodeToString(sum[:]) != digest {
				t.Fatal("digest oggetto diverso")
			}
		})
	}
}
