package remote

import (
	"bytes"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// ReplaceFile is a local version that must overwrite an existing NAS file.
// Expected* is the NAS version the plan was made against: if the NAS changed
// meanwhile the file is not touched (another PC won; the next pass decides).
type ReplaceFile struct {
	StagedFile
	ExpectedDigest string
	ExpectedSize   int64
}

// ReplaceResult lists the versions now on the NAS (as journaled) and the
// paths skipped because the NAS no longer held the expected version.
type ReplaceResult struct {
	Replaced []JournalPut
	Stale    []string
	// Adopted lists files changed directly on the NAS (outside NASBox): their
	// NAS version was journaled so the next pass resolves them as conflicts.
	Adopted []string
}

// FileState is the live state of one path on the NAS disk, as reported by
// the server's --file-states-current (hash computed on the NAS when the
// manifest record does not match the file metadata).
type FileState struct {
	Kind    string // FILE | TOMBSTONE | ABSENT | OTHER
	Digest  string
	Size    int64
	MTimeNS int64
}

// FileStatesCurrent reads the current NAS state of paths without compacting
// the journal, so it is safe while this device holds the NAS lease.
func (c *Client) FileStatesCurrent(paths []string) (map[string]FileState, error) {
	result := make(map[string]FileState, len(paths))
	if len(paths) == 0 {
		return result, nil
	}
	fields := [][]byte{[]byte("FILE_STATES_V1"), []byte(strconv.Itoa(len(paths)))}
	for _, path := range paths {
		fields = append(fields, []byte(path))
	}
	// Hashing on the NAS: lower priority so DSM and other services stay responsive.
	output, err := c.Run([]string{"nice", "-n", "10", c.script(), "--file-states-current"}, nulJoin(fields), 10*time.Minute)
	if err != nil {
		return nil, err
	}
	return parseFileStates(output)
}

func parseFileStates(output []byte) (map[string]FileState, error) {
	parts := bytes.Split(output, []byte{0})
	if len(parts) > 0 && len(parts[len(parts)-1]) == 0 {
		parts = parts[:len(parts)-1]
	}
	if len(parts) < 2 || string(parts[0]) != "FILE_STATES_V1" {
		return nil, fmt.Errorf("file-states: risposta non valida")
	}
	count, err := strconv.Atoi(string(parts[1]))
	if err != nil || count < 0 || len(parts) != 2+count*5 {
		return nil, fmt.Errorf("file-states: conteggio non valido")
	}
	result := make(map[string]FileState, count)
	for index := 0; index < count; index++ {
		base := 2 + index*5
		size, sizeErr := strconv.ParseInt(string(parts[base+3]), 10, 64)
		mtime, mtimeErr := strconv.ParseInt(string(parts[base+4]), 10, 64)
		if sizeErr != nil || mtimeErr != nil {
			return nil, fmt.Errorf("file-states: valori non numerici")
		}
		result[string(parts[base])] = FileState{
			Kind: string(parts[base+1]), Digest: string(parts[base+2]),
			Size: size, MTimeNS: mtime,
		}
	}
	return result, nil
}

// journalPayload builds the --journal-append payload (V3 with causal
// versions when the server supports them).
func (c *Client) journalPayload(repositoryID, txid, device string, puts []JournalPut) []byte {
	useV3 := c.cfg.Bool("remote_causal_versions_available")
	fields := [][]byte{[]byte("JOURNAL_V3")}
	if !useV3 {
		fields = [][]byte{[]byte("JOURNAL_V2")}
	}
	fields = append(fields,
		[]byte(repositoryID), []byte(txid), []byte(device),
		[]byte(fmt.Sprint(time.Now().Unix())), []byte(fmt.Sprint(len(puts))),
	)
	for _, put := range puts {
		fields = append(fields,
			[]byte("PUT"), []byte(put.Path), []byte(put.Digest),
			[]byte(fmt.Sprint(put.Size)), []byte(fmt.Sprint(put.MTimeNS)),
		)
		if useV3 {
			fields = append(fields, []byte(put.Causal))
		}
	}
	return nulJoin(fields)
}

// JournalAppendOwned appends to the journal while this device already holds
// the NAS lease (AcquireLease). Using RunLocked here would deadlock: it waits
// for the very flock the lease session owns.
func (c *Client) JournalAppendOwned(repositoryID, txid, device string, puts []JournalPut) error {
	if len(puts) == 0 {
		return nil
	}
	_, err := c.Run([]string{c.script(), "--journal-append"},
		c.journalPayload(repositoryID, txid, device, puts), 5*time.Minute)
	return err
}

// ManifestDigest returns the digest the NAS manifest records for path ("" and
// false when the manifest has no entry or records a tombstone).
func (c *Client) ManifestDigest(path string) (string, bool, error) {
	output, err := c.Run([]string{c.script(), "--manifest-get", path}, nil, 2*time.Minute)
	if err != nil {
		return "", false, err
	}
	return parseManifestHit(output)
}

func parseManifestHit(output []byte) (string, bool, error) {
	line := strings.TrimSpace(string(output))
	if line == "MANIFEST_MISS" {
		return "", false, nil
	}
	fields := strings.Split(line, "\t")
	if len(fields) < 4 || fields[0] != "MANIFEST_HIT" {
		return "", false, fmt.Errorf("manifest-get: risposta non valida")
	}
	return fields[2], fields[2] != "", nil
}

// DirSizes returns the size in bytes of each sub-directory of relative on the
// NAS ("" = repository root), computed on the NAS with du (on demand only:
// it reads the directory tree).
func (c *Client) DirSizes(relative string) (map[string]int64, error) {
	base := strings.TrimSuffix(c.cfg.RemotePrefix(), "/")
	if relative = strings.Trim(relative, "/"); relative != "" {
		if strings.Contains(relative, "..") {
			return nil, fmt.Errorf("percorso non valido")
		}
		base += "/" + relative
	}
	output, err := c.Run([]string{"sh", "-c",
		"cd " + shellQuote(base) + " && nice -n 10 du -sk -- */ 2>/dev/null; true"}, nil, 10*time.Minute)
	if err != nil {
		return nil, err
	}
	return parseDirSizes(string(output)), nil
}

func parseDirSizes(output string) map[string]int64 {
	sizes := map[string]int64{}
	for _, line := range strings.Split(output, "\n") {
		kilobytes, name, ok := strings.Cut(strings.TrimSpace(line), "\t")
		if !ok {
			continue
		}
		value, err := strconv.ParseInt(strings.TrimSpace(kilobytes), 10, 64)
		name = strings.TrimSuffix(strings.TrimSpace(name), "/")
		if err != nil || name == "" || strings.HasPrefix(name, ".") {
			continue
		}
		sizes[name] = value * 1024
	}
	return sizes
}

// ExistingPaths reports which of paths exist on the NAS (file, folder or
// anything else) without hashing them. It is the cheap question "is this name
// already taken?": FileStatesCurrent reads every file to digest it, which on a
// small NAS turns a check of a few thousand names into minutes under the lease.
// Paths containing a newline cannot be reported reliably and are left out.
func (c *Client) ExistingPaths(paths []string) (map[string]bool, error) {
	existing := make(map[string]bool, len(paths))
	if len(paths) == 0 {
		return existing, nil
	}
	var payload []byte
	for _, path := range paths {
		if strings.ContainsAny(path, "\n\x00") {
			continue
		}
		payload = append(payload, path...)
		payload = append(payload, 0)
	}
	prefix := strings.TrimSuffix(c.cfg.RemotePrefix(), "/")
	output, err := c.Run([]string{"sh", "-c", `cd "$1" && xargs -0 -r stat -c %n -- 2>/dev/null; true`, "sh", prefix},
		payload, 5*time.Minute)
	if err != nil {
		return nil, err
	}
	for _, line := range strings.Split(string(output), "\n") {
		if line != "" {
			existing[line] = true
		}
	}
	return existing, nil
}
