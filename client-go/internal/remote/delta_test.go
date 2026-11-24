package remote

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"nasbox-go/internal/classify"
)

var (
	deltaTestDigestA = strings.Repeat("a", 64)
	deltaTestDigestB = strings.Repeat("b", 64)
	deltaTestDigestC = strings.Repeat("c", 64)
	deltaTestEncoder = strings.NewReplacer(
		"%", "%25",
		"\t", "%09",
		"\n", "%0A",
		"\r", "%0D",
	)
)

func deltaTestEncodePath(path string) string {
	return deltaTestEncoder.Replace(path)
}

func deltaTestRow(path, digest string, size, mtimeNS, eventSeconds int64, causalText string) string {
	return fmt.Sprintf("%s\t%s\t%d\t%d\t100644\t%d\t%s\n",
		deltaTestEncodePath(path), digest, size, mtimeNS, eventSeconds, causalText)
}

func TestParseManifestDeltaEmpty(t *testing.T) {
	data := []byte("NASBOX_MANIFEST_DELTA_V1\n" +
		"BASE_REVISION=42\nCURRENT_REVISION=42\nCHANGED=0\nREMOVED=\n")
	delta, err := ParseManifestDelta(data)
	if err != nil {
		t.Fatal(err)
	}
	if delta.BaseRevision != 42 || delta.CurrentRevision != 42 || delta.FallbackFull {
		t.Fatalf("bad delta header: %+v", delta)
	}
	if delta.Changed == nil || len(delta.Changed) != 0 {
		t.Fatalf("expected an empty non-nil Changed map, got %#v", delta.Changed)
	}
	if delta.Removed == nil || len(delta.Removed) != 0 {
		t.Fatalf("expected an empty non-nil Removed slice, got %#v", delta.Removed)
	}

	base := map[string]classify.RemoteState{
		"keep.txt": {Kind: "FILE", Digest: deltaTestDigestA, Size: 1},
	}
	merged := MergeDelta(base, delta)
	if !reflect.DeepEqual(merged, base) {
		t.Fatalf("empty delta changed the baseline: got %v want %v", merged, base)
	}
	merged["new.txt"] = classify.RemoteState{Kind: "FILE", Digest: deltaTestDigestB}
	if _, exists := base["new.txt"]; exists {
		t.Fatal("MergeDelta aliases the base map")
	}
}

func TestParseManifestDeltaChangedRemovedAndEscaping(t *testing.T) {
	changedPath := "changed/line\nbreak\tliteral%09-%25.txt"
	removedPaths := []string{
		"gone/line\nbreak.txt",
		"gone/tab\tname.txt",
		"gone/literal%09.txt",
		"gone/carriage\rreturn.txt",
	}
	data := "NASBOX_MANIFEST_DELTA_V1\n" +
		"BASE_REVISION=10\nCURRENT_REVISION=12\nCHANGED=2\n" +
		deltaTestRow(changedPath, deltaTestDigestA, 10, 1_700_000_000_000_000_000, 1_700_000_000, "pc1:2") +
		// Non-zero size/mtime on a tombstone must be normalised just as Python does.
		deltaTestRow("deleted/file.txt", "", 99, 88, 1_700_000_001, "pc2:3") +
		"REMOVED=\n"
	for _, path := range removedPaths {
		data += deltaTestEncodePath(path) + "\n"
	}

	delta, err := ParseManifestDelta([]byte(data))
	if err != nil {
		t.Fatal(err)
	}
	if delta.BaseRevision != 10 || delta.CurrentRevision != 12 || delta.FallbackFull {
		t.Fatalf("bad delta header: %+v", delta)
	}
	wantChanged := classify.RemoteState{
		Kind: "FILE", Digest: deltaTestDigestA, Size: 10,
		MTimeNS: 1_700_000_000_000_000_000, Causal: "pc1:2",
	}
	if got := delta.Changed[changedPath]; got != wantChanged {
		t.Fatalf("changed state = %+v, want %+v", got, wantChanged)
	}
	wantTombstone := classify.RemoteState{
		Kind: "TOMBSTONE", MTimeNS: 1_700_000_001_000_000_000, Causal: "pc2:3",
	}
	if got := delta.Changed["deleted/file.txt"]; got != wantTombstone {
		t.Fatalf("tombstone state = %+v, want %+v", got, wantTombstone)
	}
	if !reflect.DeepEqual(delta.Removed, removedPaths) {
		t.Fatalf("removed paths = %#v, want %#v", delta.Removed, removedPaths)
	}

	base := map[string]classify.RemoteState{
		changedPath:        {Kind: "FILE", Digest: deltaTestDigestB, Size: 3},
		"deleted/file.txt": {Kind: "FILE", Digest: deltaTestDigestB, Size: 4},
		"untouched.txt":    {Kind: "FILE", Digest: deltaTestDigestC, Size: 5},
	}
	for _, path := range removedPaths {
		base[path] = classify.RemoteState{Kind: "FILE", Digest: deltaTestDigestC}
	}
	baseWant := make(map[string]classify.RemoteState, len(base))
	for path, state := range base {
		baseWant[path] = state
	}
	merged := MergeDelta(base, delta)
	want := map[string]classify.RemoteState{
		changedPath:        wantChanged,
		"deleted/file.txt": wantTombstone,
		"untouched.txt":    {Kind: "FILE", Digest: deltaTestDigestC, Size: 5},
	}
	if !reflect.DeepEqual(merged, want) {
		t.Fatalf("merged = %#v, want %#v", merged, want)
	}
	if !reflect.DeepEqual(base, baseWant) {
		t.Fatalf("MergeDelta mutated base: got %#v want %#v", base, baseWant)
	}
	merged[changedPath] = classify.RemoteState{Kind: "TOMBSTONE"}
	if delta.Changed[changedPath] != wantChanged {
		t.Fatal("MergeDelta result aliases delta state")
	}
}

func TestParseManifestDeltaFallbackFull(t *testing.T) {
	data := []byte("NASBOX_MANIFEST_DELTA_V1\n" +
		"BASE_REVISION=5\nCURRENT_REVISION=9\nFALLBACK_FULL=1\nCHANGED=2\n" +
		deltaTestRow("live.txt", deltaTestDigestA, 7, 1_700_000_000_000_000_000, 1_700_000_000, "") +
		deltaTestRow("gone.txt", "", 0, 0, 1_700_000_001, "") +
		"REMOVED=\n")
	delta, err := ParseManifestDelta(data)
	if err != nil {
		t.Fatal(err)
	}
	if !delta.FallbackFull {
		t.Fatal("FALLBACK_FULL=1 was not decoded")
	}
	// Even a hand-built fallback carrying removals must replace, not merge.
	delta.Removed = []string{"obsolete.txt"}
	base := map[string]classify.RemoteState{
		"obsolete.txt": {Kind: "FILE", Digest: deltaTestDigestB},
		"stale.txt":    {Kind: "FILE", Digest: deltaTestDigestC},
	}
	merged := MergeDelta(base, delta)
	if len(merged) != 2 {
		t.Fatalf("fallback did not replace the baseline: %#v", merged)
	}
	if _, exists := merged["obsolete.txt"]; exists {
		t.Fatal("fallback retained a base-only path")
	}
	if _, exists := merged["stale.txt"]; exists {
		t.Fatal("fallback retained a stale base path")
	}
	if merged["gone.txt"].Kind != "TOMBSTONE" {
		t.Fatalf("fallback lost a tombstone: %#v", merged)
	}
	if len(base) != 2 {
		t.Fatal("fallback merge mutated the base map")
	}
}

func TestParseAndMergeManifestDeltaMatchesFullExport(t *testing.T) {
	baseData := []byte("NASBOX_MANIFEST_V1\n" +
		deltaTestRow("same.txt", deltaTestDigestA, 1, 1_700_000_000_000_000_000, 1_700_000_000, "pc1:1") +
		deltaTestRow("deleted.txt", "", 0, 0, 1_700_000_001, "pc1:1") +
		deltaTestRow("gone.txt", deltaTestDigestB, 2, 1_700_000_000_000_000_000, 1_700_000_000, "pc1:1") +
		deltaTestRow("untouched.txt", deltaTestDigestC, 3, 1_700_000_000_000_000_000, 1_700_000_000, "pc1:1"))
	base, err := ParseManifest(baseData)
	if err != nil {
		t.Fatal(err)
	}
	deltaData := []byte("NASBOX_MANIFEST_DELTA_V1\n" +
		"BASE_REVISION=1\nCURRENT_REVISION=2\nCHANGED=3\n" +
		deltaTestRow("same.txt", deltaTestDigestB, 4, 1_700_000_002_000_000_000, 1_700_000_002, "pc1:2") +
		deltaTestRow("deleted.txt", "", 0, 0, 1_700_000_003, "pc1:2") +
		deltaTestRow("new.txt", deltaTestDigestC, 5, 1_700_000_002_000_000_000, 1_700_000_002, "pc2:1") +
		"REMOVED=\ngone.txt\n")
	delta, err := ParseManifestDelta(deltaData)
	if err != nil {
		t.Fatal(err)
	}
	currentData := []byte("NASBOX_MANIFEST_V1\n" +
		deltaTestRow("same.txt", deltaTestDigestB, 4, 1_700_000_002_000_000_000, 1_700_000_002, "pc1:2") +
		deltaTestRow("deleted.txt", "", 0, 0, 1_700_000_003, "pc1:2") +
		deltaTestRow("new.txt", deltaTestDigestC, 5, 1_700_000_002_000_000_000, 1_700_000_002, "pc2:1") +
		deltaTestRow("untouched.txt", deltaTestDigestC, 3, 1_700_000_000_000_000_000, 1_700_000_000, "pc1:1"))
	current, err := ParseManifest(currentData)
	if err != nil {
		t.Fatal(err)
	}
	if merged := MergeDelta(base, delta); !reflect.DeepEqual(merged, current) {
		t.Fatalf("merged delta differs from full export:\n got %#v\nwant %#v", merged, current)
	}
}

func TestParseManifestDeltaRevisionAndLineEndingBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name string
		data string
		base int64
		curr int64
	}{
		{
			name: "zero",
			data: "NASBOX_MANIFEST_DELTA_V1\nBASE_REVISION=0\nCURRENT_REVISION=0\nCHANGED=0\nREMOVED=\n",
		},
		{
			name: "leading zeroes are decimal",
			data: "NASBOX_MANIFEST_DELTA_V1\nBASE_REVISION=000001\nCURRENT_REVISION=000002\nCHANGED=000\nREMOVED=\n",
			base: 1,
			curr: 2,
		},
		{
			name: "maximum server revision",
			data: "NASBOX_MANIFEST_DELTA_V1\nBASE_REVISION=999999999999999999\nCURRENT_REVISION=999999999999999999\nCHANGED=0\nREMOVED=\n",
			base: 999_999_999_999_999_999,
			curr: 999_999_999_999_999_999,
		},
		{
			name: "CRLF",
			data: "NASBOX_MANIFEST_DELTA_V1\r\nBASE_REVISION=3\r\nCURRENT_REVISION=4\r\nCHANGED=0\r\nREMOVED=\r\n",
			base: 3,
			curr: 4,
		},
		{
			name: "no final newline",
			data: "NASBOX_MANIFEST_DELTA_V1\nBASE_REVISION=3\nCURRENT_REVISION=4\nCHANGED=0\nREMOVED=",
			base: 3,
			curr: 4,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			delta, err := ParseManifestDelta([]byte(tc.data))
			if err != nil {
				t.Fatal(err)
			}
			if delta.BaseRevision != tc.base || delta.CurrentRevision != tc.curr {
				t.Fatalf("revisions = %d/%d, want %d/%d", delta.BaseRevision, delta.CurrentRevision, tc.base, tc.curr)
			}
		})
	}
}

func TestParseManifestDeltaHeaderOrderAndMarkerLikePaths(t *testing.T) {
	data := []byte("NASBOX_MANIFEST_DELTA_V1\n" +
		"CHANGED=1\nCURRENT_REVISION=2\nBASE_REVISION=1\n" +
		deltaTestRow("equals=in=path.txt", deltaTestDigestA, 1, 2, 3, "") +
		"REMOVED=\nREMOVED=\n")
	delta, err := ParseManifestDelta(data)
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := delta.Changed["equals=in=path.txt"]; !exists {
		t.Fatalf("equals in changed path was not decoded: %#v", delta.Changed)
	}
	if !reflect.DeepEqual(delta.Removed, []string{"REMOVED="}) {
		t.Fatalf("marker-like removed path was not preserved: %#v", delta.Removed)
	}
}

func TestParseManifestDeltaLongPath(t *testing.T) {
	path := "large/" + strings.Repeat("x", 70*1024) + ".txt"
	data := []byte("NASBOX_MANIFEST_DELTA_V1\n" +
		"BASE_REVISION=1\nCURRENT_REVISION=2\nCHANGED=1\n" +
		deltaTestRow(path, deltaTestDigestA, 1, 2, 3, "") +
		"REMOVED=\n")
	delta, err := ParseManifestDelta(data)
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := delta.Changed[path]; !exists {
		t.Fatal("long changed path was not decoded")
	}
}

func TestParseManifestDeltaSixFieldRow(t *testing.T) {
	data := []byte("NASBOX_MANIFEST_DELTA_V1\n" +
		"BASE_REVISION=1\nCURRENT_REVISION=2\nCHANGED=1\n" +
		"legacy.txt\t" + deltaTestDigestA + "\t1\t2\t100644\t3\n" +
		"REMOVED=\n")
	delta, err := ParseManifestDelta(data)
	if err != nil {
		t.Fatal(err)
	}
	want := classify.RemoteState{Kind: "FILE", Digest: deltaTestDigestA, Size: 1, MTimeNS: 2}
	if got := delta.Changed["legacy.txt"]; got != want {
		t.Fatalf("legacy row = %+v, want %+v", got, want)
	}
}

func TestParseManifestDeltaRejectsMalformed(t *testing.T) {
	rowA := deltaTestRow("a.txt", deltaTestDigestA, 1, 2, 3, "pc1:1")
	rowB := deltaTestRow("b.txt", deltaTestDigestB, 1, 2, 3, "pc1:1")
	for _, tc := range []struct {
		name string
		data string
	}{
		{name: "empty"},
		{name: "wrong magic", data: "NASBOX_MANIFEST_V1\nBASE_REVISION=1\nCURRENT_REVISION=2\nCHANGED=0\nREMOVED=\n"},
		{name: "missing base", data: "NASBOX_MANIFEST_DELTA_V1\nCURRENT_REVISION=2\nCHANGED=0\nREMOVED=\n"},
		{name: "missing current", data: "NASBOX_MANIFEST_DELTA_V1\nBASE_REVISION=1\nCHANGED=0\nREMOVED=\n"},
		{name: "missing changed", data: "NASBOX_MANIFEST_DELTA_V1\nBASE_REVISION=1\nCURRENT_REVISION=2\nREMOVED=\n"},
		{name: "missing removed marker", data: "NASBOX_MANIFEST_DELTA_V1\nBASE_REVISION=1\nCURRENT_REVISION=2\nCHANGED=0\n"},
		{name: "marker before changed header", data: "NASBOX_MANIFEST_DELTA_V1\nBASE_REVISION=1\nCURRENT_REVISION=2\nREMOVED=\n"},
		{name: "malformed header", data: "NASBOX_MANIFEST_DELTA_V1\nBROKEN\n"},
		{name: "duplicate header", data: "NASBOX_MANIFEST_DELTA_V1\nBASE_REVISION=1\nBASE_REVISION=1\nCURRENT_REVISION=2\nCHANGED=0\nREMOVED=\n"},
		{name: "unknown header", data: "NASBOX_MANIFEST_DELTA_V1\nBASE_REVISION=1\nCURRENT_REVISION=2\nCHANGED=0\nUNKNOWN=1\nREMOVED=\n"},
		{name: "negative base", data: "NASBOX_MANIFEST_DELTA_V1\nBASE_REVISION=-1\nCURRENT_REVISION=2\nCHANGED=0\nREMOVED=\n"},
		{name: "signed current", data: "NASBOX_MANIFEST_DELTA_V1\nBASE_REVISION=1\nCURRENT_REVISION=+2\nCHANGED=0\nREMOVED=\n"},
		{name: "non numeric base", data: "NASBOX_MANIFEST_DELTA_V1\nBASE_REVISION=abc\nCURRENT_REVISION=2\nCHANGED=0\nREMOVED=\n"},
		{name: "empty revision", data: "NASBOX_MANIFEST_DELTA_V1\nBASE_REVISION=\nCURRENT_REVISION=2\nCHANGED=0\nREMOVED=\n"},
		{name: "revision too long", data: "NASBOX_MANIFEST_DELTA_V1\nBASE_REVISION=0000000000000000001\nCURRENT_REVISION=2\nCHANGED=0\nREMOVED=\n"},
		{name: "current precedes base", data: "NASBOX_MANIFEST_DELTA_V1\nBASE_REVISION=3\nCURRENT_REVISION=2\nCHANGED=0\nREMOVED=\n"},
		{name: "negative changed", data: "NASBOX_MANIFEST_DELTA_V1\nBASE_REVISION=1\nCURRENT_REVISION=2\nCHANGED=-1\nREMOVED=\n"},
		{name: "non numeric changed", data: "NASBOX_MANIFEST_DELTA_V1\nBASE_REVISION=1\nCURRENT_REVISION=2\nCHANGED=x\nREMOVED=\n"},
		{name: "empty changed", data: "NASBOX_MANIFEST_DELTA_V1\nBASE_REVISION=1\nCURRENT_REVISION=2\nCHANGED=\nREMOVED=\n"},
		{name: "changed overflow", data: "NASBOX_MANIFEST_DELTA_V1\nBASE_REVISION=1\nCURRENT_REVISION=2\nCHANGED=9223372036854775808\nREMOVED=\n"},
		{name: "too few changed rows", data: "NASBOX_MANIFEST_DELTA_V1\nBASE_REVISION=1\nCURRENT_REVISION=2\nCHANGED=2\n" + rowA + "REMOVED=\n"},
		{name: "too many changed rows", data: "NASBOX_MANIFEST_DELTA_V1\nBASE_REVISION=1\nCURRENT_REVISION=2\nCHANGED=1\n" + rowA + rowB + "REMOVED=\n"},
		{name: "duplicate changed path", data: "NASBOX_MANIFEST_DELTA_V1\nBASE_REVISION=1\nCURRENT_REVISION=2\nCHANGED=2\n" + rowA + rowA + "REMOVED=\n"},
		{name: "duplicate removed path", data: "NASBOX_MANIFEST_DELTA_V1\nBASE_REVISION=1\nCURRENT_REVISION=2\nCHANGED=0\nREMOVED=\na.txt\na.txt\n"},
		{name: "changed and removed overlap", data: "NASBOX_MANIFEST_DELTA_V1\nBASE_REVISION=1\nCURRENT_REVISION=2\nCHANGED=1\n" + rowA + "REMOVED=\na.txt\n"},
		{name: "fallback zero", data: "NASBOX_MANIFEST_DELTA_V1\nBASE_REVISION=1\nCURRENT_REVISION=2\nFALLBACK_FULL=0\nCHANGED=0\nREMOVED=\n"},
		{name: "fallback true", data: "NASBOX_MANIFEST_DELTA_V1\nBASE_REVISION=1\nCURRENT_REVISION=2\nFALLBACK_FULL=true\nCHANGED=0\nREMOVED=\n"},
		{name: "row with too few fields", data: "NASBOX_MANIFEST_DELTA_V1\nBASE_REVISION=1\nCURRENT_REVISION=2\nCHANGED=1\nbroken\nREMOVED=\n"},
		{name: "invalid digest", data: "NASBOX_MANIFEST_DELTA_V1\nBASE_REVISION=1\nCURRENT_REVISION=2\nCHANGED=1\na.txt\tABC\t1\t2\t100644\t3\t\nREMOVED=\n"},
		{name: "invalid size", data: "NASBOX_MANIFEST_DELTA_V1\nBASE_REVISION=1\nCURRENT_REVISION=2\nCHANGED=1\na.txt\t" + deltaTestDigestA + "\tx\t2\t100644\t3\t\nREMOVED=\n"},
		{name: "invalid mtime", data: "NASBOX_MANIFEST_DELTA_V1\nBASE_REVISION=1\nCURRENT_REVISION=2\nCHANGED=1\na.txt\t" + deltaTestDigestA + "\t1\tx\t100644\t3\t\nREMOVED=\n"},
		{name: "invalid event timestamp", data: "NASBOX_MANIFEST_DELTA_V1\nBASE_REVISION=1\nCURRENT_REVISION=2\nCHANGED=1\na.txt\t" + deltaTestDigestA + "\t1\t2\t100644\tx\t\nREMOVED=\n"},
		{name: "empty event timestamp", data: "NASBOX_MANIFEST_DELTA_V1\nBASE_REVISION=1\nCURRENT_REVISION=2\nCHANGED=1\na.txt\t" + deltaTestDigestA + "\t1\t2\t100644\t\t\nREMOVED=\n"},
		{name: "tombstone timestamp overflow", data: "NASBOX_MANIFEST_DELTA_V1\nBASE_REVISION=1\nCURRENT_REVISION=2\nCHANGED=1\na.txt\t\t0\t0\t100644\t9223372037\t\nREMOVED=\n"},
		{name: "invalid causal", data: "NASBOX_MANIFEST_DELTA_V1\nBASE_REVISION=1\nCURRENT_REVISION=2\nCHANGED=1\na.txt\t" + deltaTestDigestA + "\t1\t2\t100644\t3\tpc1:0\nREMOVED=\n"},
		{name: "non ASCII causal", data: "NASBOX_MANIFEST_DELTA_V1\nBASE_REVISION=1\nCURRENT_REVISION=2\nCHANGED=1\na.txt\t" + deltaTestDigestA + "\t1\t2\t100644\t3\tpcà:1\nREMOVED=\n"},
		{name: "invalid changed path", data: "NASBOX_MANIFEST_DELTA_V1\nBASE_REVISION=1\nCURRENT_REVISION=2\nCHANGED=1\n" + deltaTestRow("../escape.txt", deltaTestDigestA, 1, 2, 3, "") + "REMOVED=\n"},
		{name: "invalid removed path", data: "NASBOX_MANIFEST_DELTA_V1\nBASE_REVISION=1\nCURRENT_REVISION=2\nCHANGED=0\nREMOVED=\n../escape.txt\n"},
		{name: "empty removed path", data: "NASBOX_MANIFEST_DELTA_V1\nBASE_REVISION=1\nCURRENT_REVISION=2\nCHANGED=0\nREMOVED=\n\n"},
		{name: "raw tab in removed path", data: "NASBOX_MANIFEST_DELTA_V1\nBASE_REVISION=1\nCURRENT_REVISION=2\nCHANGED=0\nREMOVED=\ngone\tname.txt\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if delta, err := ParseManifestDelta([]byte(tc.data)); err == nil {
				t.Fatalf("expected an error, got %+v", delta)
			}
		})
	}
}

func TestMergeDeltaNilInputs(t *testing.T) {
	changed := map[string]classify.RemoteState{
		"a.txt": {Kind: "FILE", Digest: deltaTestDigestA, Size: 1},
	}
	got := MergeDelta(nil, Delta{Changed: changed})
	if !reflect.DeepEqual(got, changed) {
		t.Fatalf("nil base merge = %#v, want %#v", got, changed)
	}
	got["a.txt"] = classify.RemoteState{Kind: "TOMBSTONE"}
	if changed["a.txt"].Kind != "FILE" {
		t.Fatal("MergeDelta aliases the Changed map")
	}
	if got := MergeDelta(nil, Delta{}); got == nil || len(got) != 0 {
		t.Fatalf("empty merge = %#v, want a non-nil empty map", got)
	}
	if got := MergeDelta(baseWithEntry(), Delta{FallbackFull: true}); got == nil || len(got) != 0 {
		t.Fatalf("empty fallback = %#v, want a non-nil empty map", got)
	}
}

func baseWithEntry() map[string]classify.RemoteState {
	return map[string]classify.RemoteState{
		"old.txt": {Kind: "FILE", Digest: deltaTestDigestB},
	}
}
