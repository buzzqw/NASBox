package remote

import (
	"reflect"
	"testing"
)

func TestParsePairedStatus(t *testing.T) {
	data := []byte("RESTORE_BATCH_V1\x002\x00a.txt\x00RESTORED\x00b.txt\x00NO_VERSION\x00")
	got, err := parsePairedStatus(data, "RESTORE_BATCH_V1")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"a.txt": "RESTORED", "b.txt": "NO_VERSION"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestShellQuoteKeepsSimplePathsReadable(t *testing.T) {
	if got := shellQuote("/volume1/NASBox"); got != "/volume1/NASBox" {
		t.Fatalf("simple path was quoted: %q", got)
	}
	quoted := shellQuote("a b'c")
	if quoted != `'a b'\''c'` {
		t.Fatalf("unexpected quoting: %q", quoted)
	}
}

func TestNulJoin(t *testing.T) {
	if got := string(nulJoin([][]byte{[]byte("a"), []byte("b")})); got != "a\x00b\x00" {
		t.Fatalf("bad NUL framing: %q", got)
	}
}
