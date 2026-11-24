package main

import (
	"strings"
	"testing"
	"time"

	"nasbox-go/internal/diagnostics"
)

func TestSummarizeErrorsSkipsOldAndCollapsesRepeats(t *testing.T) {
	now := time.Now()
	entries := []diagnostics.Entry{
		{Timestamp: now.Add(-48 * time.Hour), Level: "ERROR", Action: "ERROR", Detail: "legacy python failure"},
		{Timestamp: now.Add(-3 * time.Minute), Level: "ERROR", Action: "ERROR", Detail: "tick failed"},
		{Timestamp: now.Add(-2 * time.Minute), Level: "INFO", Action: "SYNC", Detail: "ok"},
		{Timestamp: now.Add(-2 * time.Minute), Level: "ERROR", Action: "ERROR", Detail: "tick  failed"},
		{Timestamp: now.Add(-1 * time.Minute), Level: "ERROR", Action: "PUSH", Path: "a.txt", Detail: "rsync 23"},
	}
	got := summarizeErrors(entries, now.Add(-24*time.Hour), 10)
	if len(got) != 2 {
		t.Fatalf("want 2 distinct recent errors, got %d: %+v", len(got), got)
	}
	if got[0].Action != "PUSH" {
		t.Fatalf("newest first, got %+v", got[0])
	}
	if !strings.HasSuffix(got[1].Detail, "(×2)") {
		t.Fatalf("repeats must be counted, got %q", got[1].Detail)
	}
	for _, entry := range got {
		if strings.Contains(entry.Detail, "legacy") {
			t.Fatal("errors older than the window must be hidden")
		}
	}
}
