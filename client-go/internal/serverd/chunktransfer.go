package serverd

import (
	"bufio"
	"bytes"
	"compress/zlib"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"strconv"
)

// cmdChunkHas reports which content-addressed objects are not on the NAS yet.
// The client uses it before uploading, turning disk-side dedup into network-side
// dedup as well.
func cmdChunkHas(ctx *Context) error {
	reader := bufio.NewReader(os.Stdin)
	magic, err := readNULString(reader)
	if err != nil || magic != "CHUNK_HAS_V1" {
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
	missing := make([]string, 0)
	seen := make(map[string]bool, count)
	for index := 0; index < count; index++ {
		digest, err := readNULString(reader)
		if err != nil || !digest64.MatchString(digest) {
			return exitCode(1)
		}
		if seen[digest] {
			continue
		}
		seen[digest] = true
		if store.Has(digest) {
			store.Touch(digest) // protegge il chunk riusato dalla GC
		} else {
			missing = append(missing, digest)
		}
	}
	fmt.Printf("CHUNK_HAS_V1\x00%d\x00", len(missing))
	for _, digest := range missing {
		fmt.Print(digest + "\x00")
	}
	return nil
}

// cmdChunkPutCompressed accepts raw or zlib-compressed chunks. Objects remain
// uncompressed and content-addressed on disk, so the repository format and all
// existing readers stay compatible.
func cmdChunkPutCompressed(ctx *Context) error {
	reader := bufio.NewReader(os.Stdin)
	magic, err := readNULString(reader)
	if err != nil || magic != "CHUNK_PUT_COMPRESSED_V1" {
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
	for index := 0; index < count; index++ {
		digest, err := readNULString(reader)
		if err != nil || !digest64.MatchString(digest) {
			return exitCode(1)
		}
		rawSizeRaw, err := readNULString(reader)
		if err != nil {
			return exitCode(1)
		}
		rawSize, err := strconv.ParseInt(rawSizeRaw, 10, 64)
		if err != nil || rawSize < 0 || rawSize > 64*1024*1024 {
			return exitCode(1)
		}
		encoding, err := readNULString(reader)
		if err != nil || (encoding != "none" && encoding != "zlib") {
			return exitCode(1)
		}
		wireSizeRaw, err := readNULString(reader)
		if err != nil {
			return exitCode(1)
		}
		wireSize, err := strconv.ParseInt(wireSizeRaw, 10, 64)
		if err != nil || wireSize < 0 || wireSize > 65*1024*1024 {
			return exitCode(1)
		}
		wire := make([]byte, wireSize)
		if _, err := io.ReadFull(reader, wire); err != nil {
			return exitCode(1)
		}
		var data []byte
		if encoding == "zlib" {
			zr, err := zlib.NewReader(bytes.NewReader(wire))
			if err != nil {
				return exitCode(1)
			}
			data, err = io.ReadAll(io.LimitReader(zr, rawSize+1))
			closeErr := zr.Close()
			if err != nil || closeErr != nil || int64(len(data)) != rawSize {
				return exitCode(1)
			}
		} else {
			if wireSize != rawSize {
				return exitCode(1)
			}
			data = wire
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
