package serverd

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

var digest64 = regexp.MustCompile(`^[0-9a-f]{64}$`)

type manifestEntry struct {
	Digest  string
	Size    string
	MTimeNS string
	Causal  string
	Epoch   string
}

func (c *Context) rawManifest() map[string]manifestEntry {
	entries := map[string]manifestEntry{}
	lines, _ := readLines(c.paths().Manifest)
	for _, line := range lines {
		if line == "NASBOX_MANIFEST_V1" {
			continue
		}
		f := strings.Split(line, "\t")
		if len(f) < 6 {
			continue
		}
		entries[f[0]] = manifestEntry{
			Digest: f[1], Size: f[2], MTimeNS: f[3], Epoch: f[5],
			Causal: fieldOr(f, 6, ""),
		}
	}
	return entries
}

func pathHasSymlinkAncestor(root, relative string) bool {
	current := root
	for _, component := range strings.Split(relative, "/") {
		current = filepath.Join(current, component)
		info, err := os.Lstat(current)
		if err == nil && info.Mode()&os.ModeSymlink != 0 {
			return true
		}
	}
	return false
}

// cmdFileStates implements --file-states-current (the read-only variant the
// client uses). --file-states also compacts the journal and is not part of
// milestone 0.
func (c *Context) cmdFileStates(current bool) error {
	if !current {
		return fmt.Errorf("--file-states (con compattazione) non ancora implementato")
	}
	data, err := io.ReadAll(os.Stdin)
	if err != nil {
		return err
	}
	fields := strings.Split(string(data), "\x00")
	if len(fields) < 2 {
		return exitCode(1)
	}
	protocol := fields[0]
	if protocol != "FILE_STATES_V1" && protocol != "FILE_STATES_V2" {
		return exitCode(1)
	}
	count, err := strconv.Atoi(fields[1])
	if err != nil || count < 0 || count > 100000 || len(fields) < count+2 {
		return exitCode(1)
	}
	return c.fileStates(protocol, fields[2:count+2], os.Stdout)
}

func (c *Context) fileStates(protocol string, want []string, out io.Writer) error {
	count := len(want)
	manifest := c.rawManifest()
	type result struct {
		path, kind, digest, size, mtime, causal string
	}
	results := make([]result, 0, count)
	for _, path := range want {
		if !validRelative(path) || pathHasSymlinkAncestor(c.ShareRoot, path) {
			return exitCode(1)
		}
		full := filepath.Join(c.ShareRoot, path)
		encoded := encodeField(path)
		entry, known := manifest[encoded]
		info, statErr := os.Lstat(full)
		if statErr != nil {
			if !known {
				results = append(results, result{path: path, kind: "ABSENT", size: "0", mtime: "0"})
				continue
			}
			epoch, _ := strconv.ParseInt(entry.Epoch, 10, 64)
			results = append(results, result{
				path: path, kind: "TOMBSTONE", size: "0",
				mtime: strconv.FormatInt(epoch*1_000_000_000, 10), causal: entry.Causal,
			})
			continue
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			results = append(results, result{path: path, kind: "OTHER", size: "0", mtime: "0"})
			continue
		}
		size := strconv.FormatInt(info.Size(), 10)
		mtime := strconv.FormatInt(info.ModTime().Unix()*1_000_000_000, 10)
		digest := ""
		causal := ""
		if known && digest64.MatchString(entry.Digest) && entry.Size == size && entry.MTimeNS == mtime {
			digest = entry.Digest
			causal = entry.Causal
		} else {
			file, err := os.Open(full)
			if err != nil {
				return exitCode(1)
			}
			hash := sha256.New()
			if _, err := io.Copy(hash, file); err != nil {
				file.Close()
				return exitCode(1)
			}
			file.Close()
			digest = hex.EncodeToString(hash.Sum(nil))
		}
		results = append(results, result{
			path: path, kind: "FILE", digest: digest, size: size, mtime: mtime, causal: causal,
		})
	}
	writer := bufio.NewWriter(out)
	defer writer.Flush()
	fmt.Fprintf(writer, "%s\x00%d\x00", protocol, count)
	for _, entry := range results {
		fmt.Fprintf(writer, "%s\x00%s\x00%s\x00%s\x00%s\x00", entry.path, entry.kind, entry.digest, entry.size, entry.mtime)
		if protocol == "FILE_STATES_V2" {
			fmt.Fprintf(writer, "%s\x00", entry.causal)
		}
	}
	return nil
}
