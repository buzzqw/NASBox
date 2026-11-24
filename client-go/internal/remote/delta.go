package remote

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"strconv"

	"nasbox-go/internal/classify"
	"nasbox-go/internal/paths"
)

const (
	manifestDeltaHeader        = "NASBOX_MANIFEST_DELTA_V1"
	manifestDeltaRemovedMarker = "REMOVED="
)

// Delta is the server's incremental manifest export. Changed contains both
// file and tombstone rows; Removed contains paths that disappeared from the
// rendered manifest altogether. FallbackFull means Changed is a complete
// replacement rather than an incremental update.
type Delta struct {
	BaseRevision    int64
	CurrentRevision int64
	FallbackFull    bool
	Changed         map[string]classify.RemoteState
	Removed         []string
}

// ParseManifestDelta decodes --manifest-export-delta output. It validates the
// framing before returning any state, so a truncated or inconsistent response
// can never be merged into an authoritative baseline.
func ParseManifestDelta(data []byte) (Delta, error) {
	scanner := bufio.NewScanner(bytes.NewReader(data))
	// Match ParseManifest: paths plus seven TSV fields may exceed 64 KiB.
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	if !scanner.Scan() {
		if err := scanner.Err(); err != nil {
			return Delta{}, fmt.Errorf("manifest delta: %w", err)
		}
		return Delta{}, errors.New("manifest delta: risposta vuota")
	}
	lineNumber := 1
	if string(scanner.Bytes()) != manifestDeltaHeader {
		return Delta{}, fmt.Errorf("manifest delta: header non valido %q", scanner.Text())
	}

	delta := Delta{
		Changed: make(map[string]classify.RemoteState),
		Removed: make([]string, 0),
	}
	seen := make(map[string]bool, 4)
	var changedCount int64
	var changedRows int64
	rowsStarted := false
	removedSeen := false
	removedPaths := make(map[string]bool)

	for scanner.Scan() {
		lineNumber++
		line := scanner.Bytes()
		if removedSeen {
			if len(line) == 0 {
				return Delta{}, fmt.Errorf("manifest delta riga %d: percorso rimosso vuoto", lineNumber)
			}
			if bytes.IndexAny(line, "\t\r\x00") >= 0 {
				return Delta{}, fmt.Errorf("manifest delta riga %d: percorso rimosso non codificato", lineNumber)
			}
			path := decodeField(line)
			if err := paths.ValidateRelative(path); err != nil {
				return Delta{}, fmt.Errorf("manifest delta riga %d: percorso rimosso non valido %q: %w", lineNumber, path, err)
			}
			if _, exists := delta.Changed[path]; exists {
				return Delta{}, fmt.Errorf("manifest delta riga %d: percorso sia cambiato sia rimosso: %q", lineNumber, path)
			}
			if removedPaths[path] {
				return Delta{}, fmt.Errorf("manifest delta riga %d: percorso rimosso duplicato: %q", lineNumber, path)
			}
			removedPaths[path] = true
			delta.Removed = append(delta.Removed, path)
			continue
		}

		if bytes.Equal(line, []byte(manifestDeltaRemovedMarker)) {
			removedSeen = true
			continue
		}
		// Every data row contains a tab. Before the first row, KEY=VALUE lines
		// are headers; accepting them in any order mirrors the Python client.
		if !rowsStarted && bytes.IndexByte(line, '\t') < 0 {
			if err := parseManifestDeltaHeader(line, &delta, &changedCount, seen); err != nil {
				return Delta{}, fmt.Errorf("manifest delta riga %d: %w", lineNumber, err)
			}
			continue
		}

		rowsStarted = true
		if seen["CHANGED"] && changedRows >= changedCount {
			return Delta{}, fmt.Errorf("manifest delta riga %d: più righe di CHANGED=%d", lineNumber, changedCount)
		}
		path, state, err := parseManifestRow(line, true)
		if err != nil {
			return Delta{}, fmt.Errorf("manifest delta riga %d: %w", lineNumber, err)
		}
		if _, exists := delta.Changed[path]; exists {
			return Delta{}, fmt.Errorf("manifest delta riga %d: percorso cambiato duplicato: %q", lineNumber, path)
		}
		delta.Changed[path] = state
		changedRows++
	}
	if err := scanner.Err(); err != nil {
		return Delta{}, fmt.Errorf("manifest delta: %w", err)
	}

	for _, required := range []string{"BASE_REVISION", "CURRENT_REVISION", "CHANGED"} {
		if !seen[required] {
			return Delta{}, fmt.Errorf("manifest delta: header %s mancante", required)
		}
	}
	if !removedSeen {
		return Delta{}, errors.New("manifest delta: marcatore REMOVED= mancante")
	}
	if changedRows != changedCount {
		return Delta{}, fmt.Errorf("manifest delta: CHANGED=%d ma ricevute %d righe", changedCount, changedRows)
	}
	if delta.CurrentRevision < delta.BaseRevision {
		return Delta{}, fmt.Errorf(
			"manifest delta: CURRENT_REVISION=%d precedente a BASE_REVISION=%d",
			delta.CurrentRevision, delta.BaseRevision,
		)
	}
	return delta, nil
}

func parseManifestDeltaHeader(
	line []byte,
	delta *Delta,
	changedCount *int64,
	seen map[string]bool,
) error {
	key, value, found := bytes.Cut(line, []byte{'='})
	if !found || len(key) == 0 {
		return errors.New("header malformato")
	}
	name := string(key)
	if seen[name] {
		return fmt.Errorf("header duplicato %s", name)
	}
	seen[name] = true

	switch name {
	case "BASE_REVISION":
		revision, err := parseManifestDeltaNumber(value, name, 18)
		if err != nil {
			return err
		}
		delta.BaseRevision = revision
	case "CURRENT_REVISION":
		revision, err := parseManifestDeltaNumber(value, name, 18)
		if err != nil {
			return err
		}
		delta.CurrentRevision = revision
	case "CHANGED":
		count, err := parseManifestDeltaNumber(value, name, 0)
		if err != nil {
			return err
		}
		*changedCount = count
	case "FALLBACK_FULL":
		if string(value) != "1" {
			return errors.New("FALLBACK_FULL deve valere 1")
		}
		delta.FallbackFull = true
	default:
		return fmt.Errorf("header sconosciuto %q", name)
	}
	return nil
}

func parseManifestDeltaNumber(value []byte, field string, maxDigits int) (int64, error) {
	if len(value) == 0 || (maxDigits > 0 && len(value) > maxDigits) {
		return 0, fmt.Errorf("%s non valido", field)
	}
	for _, char := range value {
		if char < '0' || char > '9' {
			return 0, fmt.Errorf("%s non valido", field)
		}
	}
	number, err := strconv.ParseInt(string(value), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%s non valido: %w", field, err)
	}
	return number, nil
}

// MergeDelta returns a new map without mutating base. A full fallback replaces
// the baseline completely; otherwise changed rows are applied before removed
// paths, and tombstones remain present as authoritative remote state.
func MergeDelta(base map[string]classify.RemoteState, delta Delta) map[string]classify.RemoteState {
	if delta.FallbackFull {
		merged := make(map[string]classify.RemoteState, len(delta.Changed))
		for path, state := range delta.Changed {
			merged[path] = state
		}
		return merged
	}

	merged := make(map[string]classify.RemoteState, len(base)+len(delta.Changed))
	for path, state := range base {
		merged[path] = state
	}
	for path, state := range delta.Changed {
		merged[path] = state
	}
	for _, path := range delta.Removed {
		delete(merged, path)
	}
	return merged
}
