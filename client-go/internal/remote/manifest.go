// Package remote parses the NAS server's read-only streaming output.
// It never holds more than one line in memory at a time.
package remote

import (
	"bufio"
	"bytes"
	"errors"
	"strconv"
	"strings"

	"nasbox-go/internal/classify"
)

const manifestHeader = "NASBOX_MANIFEST_V1"

// ParseManifest decodes --manifest-export output into path -> remote state.
func ParseManifest(data []byte) (map[string]classify.RemoteState, error) {
	result := make(map[string]classify.RemoteState)
	scanner := bufio.NewScanner(bytes.NewReader(data))
	// Long paths plus seven TSV fields can exceed the default 64 KiB token.
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	first := true
	for scanner.Scan() {
		line := scanner.Bytes()
		if first {
			first = false
			if string(line) == manifestHeader {
				continue
			}
		}
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		fields := bytes.Split(line, []byte{'\t'})
		if len(fields) < 6 {
			return nil, errors.New("manifest: campi insufficienti")
		}
		path := decodeField(fields[0])
		digest := string(fields[1])
		size, err := strconv.ParseInt(string(fields[2]), 10, 64)
		if err != nil {
			return nil, err
		}
		kind := "TOMBSTONE"
		if digest != "" {
			kind = "FILE"
		}
		result[path] = classify.RemoteState{Kind: kind, Digest: digest, Size: size}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

// decodeField reverses the server's escaping order: controls first, % last.
func decodeField(value []byte) string {
	out := bytes.ReplaceAll(value, []byte("%09"), []byte{'\t'})
	out = bytes.ReplaceAll(out, []byte("%0A"), []byte{'\n'})
	out = bytes.ReplaceAll(out, []byte("%0D"), []byte{'\r'})
	out = bytes.ReplaceAll(out, []byte("%25"), []byte{'%'})
	return string(out)
}

// ParseConfig reads KEY=VALUE lines from --print-config.
func ParseConfig(data []byte) map[string]string {
	result := map[string]string{}
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, found := strings.Cut(line, "=")
		if found {
			result[strings.TrimSpace(key)] = strings.TrimSpace(value)
		}
	}
	return result
}
