package remote

import "testing"

func TestParseManifestHandlesFileAndTombstone(t *testing.T) {
	data := []byte("NASBOX_MANIFEST_V1\n" +
		"cartella/a.txt\tabc\t10\t1700000000000000000\t7701c755\t1700000000\t7701c755:1\n" +
		"cartella/gone.txt\t\t0\t0\t7701c755\t1700000000\t\n" +
		"percent%25%09tab.txt\tdef\t3\t1700000000000000000\t7701c755\t1700000000\t\n")
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
	if _, ok := entries["percent%\ttab.txt"]; !ok {
		t.Fatalf("escaping not decoded: %v", entries)
	}
}

func TestParseConfig(t *testing.T) {
	values := ParseConfig([]byte("VERSION=3.25.0\nREPOSITORY_READY=true\n# comment\n"))
	if values["VERSION"] != "3.25.0" || values["REPOSITORY_READY"] != "true" {
		t.Fatalf("bad config: %v", values)
	}
}
