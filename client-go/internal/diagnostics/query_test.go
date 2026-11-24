package diagnostics

import (
	"testing"
	"time"
)

func sampleEntries() []Entry {
	now := time.Now()
	return []Entry{
		{Timestamp: now, Action: "UPLOAD", Path: "docs/a.txt", Detail: "ok"},
		{Timestamp: now, Action: "DOWNLOAD", Path: "docs/b.txt", Detail: "ok"},
		{Timestamp: now, Action: "ERROR", Path: "-", Detail: "ssh timeout"},
		{Timestamp: now, Action: "CONFLICT", Path: "docs/c.txt", Detail: "kept both"},
		{Timestamp: now, Action: "RESTORE_REMOTE_VERSION", Path: "docs/d.txt", Detail: "restored"},
		{Timestamp: now, Action: "INFO", Path: "-", Detail: "sync ok"},
	}
}

func TestFilterByGroup(t *testing.T) {
	entries := sampleEntries()
	if got := len(Filter(entries, "", "")); got != len(entries) {
		t.Fatalf("all = %d", got)
	}
	if got := len(Filter(entries, "transfers", "")); got != 2 {
		t.Fatalf("transfers = %d", got)
	}
	if got := len(Filter(entries, "errors", "")); got != 1 {
		t.Fatalf("errors = %d", got)
	}
	if got := len(Filter(entries, "safety", "")); got != 1 {
		t.Fatalf("safety = %d", got)
	}
	if got := len(Filter(entries, "history", "")); got != 1 {
		t.Fatalf("history = %d", got)
	}
}

func TestFilterByQuery(t *testing.T) {
	entries := sampleEntries()
	if got := len(Filter(entries, "", "DOCS")); got != 4 {
		t.Fatalf("docs query = %d", got)
	}
	if got := len(Filter(entries, "safety", "c.txt")); got != 1 {
		t.Fatalf("group+query = %d", got)
	}
	if got := len(Filter(entries, "", "nothing")); got != 0 {
		t.Fatalf("empty result = %d", got)
	}
}

func TestFormatEntryIsSingleLine(t *testing.T) {
	entry := Entry{Timestamp: time.Now(), Action: "UPLOAD", Path: "a.txt", Detail: "line1\nline2"}
	text := FormatEntry(entry)
	if text == "" || text[len(text)-1] == '\n' {
		t.Fatalf("format = %q", text)
	}
	for _, character := range text {
		if character == '\n' || character == '\r' {
			t.Fatalf("not single line: %q", text)
		}
	}
}
