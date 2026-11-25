package ui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"nasbox-go/internal/diagnostics"
	"nasbox-go/internal/i18n"
	"nasbox-go/internal/remote"
)

func TestBuildLogRowsFoldsBulkTransfersAndWritesSentences(t *testing.T) {
	i18n.Language = "it"
	now := time.Date(2026, 11, 25, 12, 0, 0, 0, time.Local)
	var entries []diagnostics.Entry
	entries = append(entries, diagnostics.Entry{Timestamp: now, Level: "ERROR", Action: "ERROR", Path: "-", Detail: "ssh: exit status 1: "})
	for index := 0; index < 1200; index++ {
		entries = append(entries, diagnostics.Entry{
			Timestamp: now.Add(-time.Duration(index) * time.Second), Level: "INFO", Action: "DOWNLOAD",
			Path: fmt.Sprintf("SADE/images/%04d.png", index),
		})
	}
	entries = append(entries, diagnostics.Entry{Timestamp: now.Add(-48 * time.Hour), Level: "INFO", Action: "UPLOAD", Path: "Documenti/a.txt"})
	rows := buildLogRows(entries, now)
	if len(rows) != 3 {
		t.Fatalf("rows = %d: %+v", len(rows), rows)
	}
	if !strings.Contains(rows[0].Text, "Il NAS ha rifiutato") || strings.Contains(rows[0].Text, "DOWNLOAD") {
		t.Fatalf("error text: %q", rows[0].Text)
	}
	if rows[1].Text != "1.200 file scaricati in SADE/images" {
		t.Fatalf("bulk text: %q", rows[1].Text)
	}
	if !strings.Contains(rows[1].When, "–") {
		t.Fatalf("a bulk row shows its time span, got %q", rows[1].When)
	}
	if rows[2].Text != "Caricato sul NAS: Documenti/a.txt" || rows[2].When != "23/11 12:00" {
		t.Fatalf("upload row: %+v", rows[2])
	}
}

func TestDropNoiseHidesDiagnosticsOnly(t *testing.T) {
	entries := []diagnostics.Entry{{Action: "PERF"}, {Action: "DOWNLOAD"}, {Action: "DOCTOR"}, {Action: "ERROR"}}
	kept := dropNoise(entries)
	if len(kept) != 2 || kept[0].Action != "DOWNLOAD" || kept[1].Action != "ERROR" {
		t.Fatalf("kept %+v", kept)
	}
}

func TestLooksOfflineOnlyForConnectionProblems(t *testing.T) {
	for message, want := range map[string]bool{
		"ssh: exit status 1: ":                        false, // the NAS answered and refused
		"ssh: exit status 255: ssh: connect refused":  true,
		"ssh: Connection timed out":                   true,
		"il NAS ha rifiutato lo spostamento a → b: x": false,
		"mirror: rsync error":                         false,
	} {
		if got := looksOffline(message); got != want {
			t.Errorf("looksOffline(%q) = %v, want %v", message, got, want)
		}
	}
}

func TestSortBrowseModes(t *testing.T) {
	entries := []remote.BrowseEntry{
		{Name: "b.txt", Kind: "FILE", Size: 10, MTime: 5},
		{Name: "Zeta", Kind: "DIR"},
		{Name: "a.txt", Kind: "FILE", Size: 99, MTime: 9},
		{Name: "alfa", Kind: "DIR"},
	}
	names := func(mode string) string {
		var out []string
		for _, entry := range sortBrowse(entries, mode) {
			out = append(out, entry.Name)
		}
		return strings.Join(out, ",")
	}
	for mode, want := range map[string]string{
		sortFoldersFirst: "alfa,Zeta,a.txt,b.txt",
		sortFilesFirst:   "a.txt,b.txt,alfa,Zeta",
		sortNameAsc:      "a.txt,alfa,b.txt,Zeta",
		sortNameDesc:     "Zeta,b.txt,alfa,a.txt",
		sortSize:         "alfa,Zeta,a.txt,b.txt",
		sortNewest:       "a.txt,b.txt,alfa,Zeta",
	} {
		if got := names(mode); got != want {
			t.Errorf("%s: %s, want %s", mode, got, want)
		}
	}
	if entries[0].Name != "b.txt" {
		t.Fatal("sorting must not modify the input")
	}
}

func TestWrapTip(t *testing.T) {
	lines := wrapTip("uno due tre quattro cinque sei sette otto nove dieci undici dodici tredici quattordici", 30)
	if len(lines) < 2 {
		t.Fatalf("lines: %q", lines)
	}
	for _, line := range lines {
		if len([]rune(line)) > 34 {
			t.Fatalf("line too long: %q", line)
		}
	}
}
