package remote

import "testing"

func TestParseFileStates(t *testing.T) {
	output := []byte("FILE_STATES_V1\x003\x00" +
		"a b.txt\x00FILE\x00" + "abc\x0012\x001700000000000000000\x00" +
		"gone.txt\x00TOMBSTONE\x00\x000\x001700000001000000000\x00" +
		"new.txt\x00ABSENT\x00\x000\x000\x00")
	states, err := parseFileStates(output)
	if err != nil {
		t.Fatal(err)
	}
	if states["a b.txt"].Kind != "FILE" || states["a b.txt"].Digest != "abc" || states["a b.txt"].Size != 12 {
		t.Fatalf("file: %+v", states["a b.txt"])
	}
	if states["gone.txt"].Kind != "TOMBSTONE" || states["new.txt"].Kind != "ABSENT" {
		t.Fatalf("states: %+v", states)
	}
	if _, err := parseFileStates([]byte("FILE_STATES_V1\x002\x00x\x00")); err == nil {
		t.Fatal("truncated answer must be rejected")
	}
}
