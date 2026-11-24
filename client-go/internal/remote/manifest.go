// Package remote parses the NAS server's read-only streaming output.
// Streaming parsers scan one line at a time and retain decoded protocol state.
package remote

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"

	"nasbox-go/internal/causal"
	"nasbox-go/internal/classify"
	"nasbox-go/internal/paths"
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
		path, state, err := parseManifestRow(line, false)
		if err != nil {
			return nil, err
		}
		result[path] = state
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

// parseManifestRow decodes one full-manifest row. Delta rows use exactly the
// same encoding and state representation, including tombstones whose event
// timestamp is stored in field six rather than in the mtime field. Full
// exports retain compatibility with legacy rows whose event field was blank;
// delta V1 requires the canonical server row.
func parseManifestRow(line []byte, requireEventTimestamp bool) (string, classify.RemoteState, error) {
	fields := bytes.Split(line, []byte{'\t'})
	if len(fields) < 6 {
		return "", classify.RemoteState{}, errors.New("manifest: campi insufficienti")
	}
	path := decodeField(fields[0])
	if err := paths.ValidateRelative(path); err != nil {
		return "", classify.RemoteState{}, fmt.Errorf("manifest: percorso non valido %q: %w", path, err)
	}
	digest := string(fields[1])
	if digest != "" && !validManifestDigest(digest) {
		return "", classify.RemoteState{}, fmt.Errorf("manifest: digest non valido per %q", path)
	}
	size, err := strconv.ParseInt(string(fields[2]), 10, 64)
	if err != nil {
		return "", classify.RemoteState{}, fmt.Errorf("manifest: dimensione non valida per %q: %w", path, err)
	}
	mtimeNS, err := strconv.ParseInt(string(fields[3]), 10, 64)
	if err != nil {
		return "", classify.RemoteState{}, fmt.Errorf("manifest: mtime non valido per %q: %w", path, err)
	}
	eventSeconds := int64(0)
	if len(fields[5]) != 0 || requireEventTimestamp {
		eventSeconds, err = strconv.ParseInt(string(fields[5]), 10, 64)
		if err != nil {
			return "", classify.RemoteState{}, fmt.Errorf("manifest: timestamp evento non valido per %q: %w", path, err)
		}
	}
	causalText := ""
	if len(fields) >= 7 {
		causalText = string(fields[6])
		if causalText != "" {
			if !isASCII(causalText) {
				return "", classify.RemoteState{}, fmt.Errorf("manifest: versione causale non valida per %q", path)
			}
			if _, ok := causal.Parse(causalText); !ok {
				return "", classify.RemoteState{}, fmt.Errorf("manifest: versione causale non valida per %q", path)
			}
		}
	}

	state := classify.RemoteState{
		Kind: "FILE", Digest: digest, Size: size, MTimeNS: mtimeNS, Causal: causalText,
	}
	if digest == "" {
		if eventSeconds > math.MaxInt64/1_000_000_000 || eventSeconds < math.MinInt64/1_000_000_000 {
			return "", classify.RemoteState{}, fmt.Errorf("manifest: timestamp tombstone fuori intervallo per %q", path)
		}
		state.Kind = "TOMBSTONE"
		state.Size = 0
		state.MTimeNS = eventSeconds * 1_000_000_000
	}
	return path, state, nil
}

func validManifestDigest(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, char := range value {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return false
		}
	}
	return true
}

func isASCII(value string) bool {
	for index := 0; index < len(value); index++ {
		if value[index] >= 0x80 {
			return false
		}
	}
	return true
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
