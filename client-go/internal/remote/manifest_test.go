package remote

import (
	"strings"
	"testing"

	"nasbox-go/internal/classify"
)

func TestParseManifestHandlesFileAndTombstone(t *testing.T) {
	digestA := strings.Repeat("a", 64)
	digestB := strings.Repeat("b", 64)
	data := []byte("NASBOX_MANIFEST_V1\n" +
		"cartella/a.txt\t" + digestA + "\t10\t1700000000000000000\t7701c755\t1700000000\t7701c755:1\n" +
		"cartella/gone.txt\t\t0\t0\t7701c755\t1700000000\t\n" +
		"percent%25%09tab.txt\t" + digestB + "\t3\t1700000000000000000\t7701c755\t1700000000\t\n")
	entries, err := ParseManifest(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(entries))
	}
	if entries["cartella/a.txt"].Kind != "FILE" || entries["cartella/a.txt"].Size != 10 {
		t.Fatalf("bad FILE entry: %+v", entries["cartella/a.txt"])
	}
	if entries["cartella/gone.txt"].Kind != "TOMBSTONE" {
		t.Fatalf("bad TOMBSTONE entry: %+v", entries["cartella/gone.txt"])
	}
	if entries["cartella/gone.txt"].MTimeNS != 1700000000*1_000_000_000 {
		t.Fatalf("tombstone event timestamp not decoded: %+v", entries["cartella/gone.txt"])
	}
	if _, ok := entries["percent%\ttab.txt"]; !ok {
		t.Fatalf("escaping not decoded: %v", entries)
	}
}

func TestParseManifestAllowsBlankEventTimestamp(t *testing.T) {
	digest := strings.Repeat("a", 64)
	data := []byte("NASBOX_MANIFEST_V1\n" +
		"legacy.txt\t" + digest + "\t5\t1000\t\t\t\n")
	entries, err := ParseManifest(data)
	if err != nil {
		t.Fatal(err)
	}
	want := classify.RemoteState{Kind: "FILE", Digest: digest, Size: 5, MTimeNS: 1000}
	if got := entries["legacy.txt"]; got != want {
		t.Fatalf("legacy row = %+v, want %+v", got, want)
	}
}

func TestParseConfig(t *testing.T) {
	values := ParseConfig([]byte("VERSION=3.25.0\nREPOSITORY_READY=true\n# comment\n"))
	if values["VERSION"] != "3.25.0" || values["REPOSITORY_READY"] != "true" {
		t.Fatalf("bad config: %v", values)
	}
}
