package ui

import (
	"regexp"
	"strings"
	"testing"
	"time"

	"nasbox-go/internal/history"
	"nasbox-go/internal/i18n"
)

func TestVersionStampFullKeepsSecondsAndOffset(t *testing.T) {
	if got := versionStampFull(time.Time{}); got != "—" {
		t.Fatalf("timestamp zero atteso «—», ottenuto %q", got)
	}
	stamp := parseVersionStamp("2026-11-27--14-35-07-123456Z")
	if stamp.IsZero() {
		t.Fatal("timestamp non riconosciuto")
	}
	got := versionStampFull(stamp)
	want := regexp.MustCompile(`^\d{2}/\d{2}/\d{4} \d{2}:\d{2}:\d{2} [+-]\d{2}:\d{2}$`)
	if !want.MatchString(got) {
		t.Fatalf("data incompleta (seconds/offset mancanti): %q", got)
	}
}

func TestSizeDetailShowsExactBytes(t *testing.T) {
	if got, want := sizeDetail(2097275), "2.0 MB (2097275 B)"; got != want {
		t.Fatalf("sizeDetail = %q, atteso %q", got, want)
	}
	// Below 1 KiB humanBytes is already exact: do not repeat the byte count.
	if got, want := sizeDetail(512), "512 B"; got != want {
		t.Fatalf("sizeDetail = %q, atteso %q", got, want)
	}
}

func TestVersionDetailIncludesFullInfo(t *testing.T) {
	previous := i18n.Language
	i18n.Language = "it"
	t.Cleanup(func() { i18n.Language = previous })

	local := fileVersion{
		Source: "local",
		When:   parseVersionStamp("2026-11-27--14-35-07-123456Z"),
		Size:   4096,
		Local: history.Version{
			Original:  "Documenti/nota.txt",
			Timestamp: "2026-11-27--14-35-07-123456Z",
			TrashPath: "/state/sync-daemon/trash/Documenti/nota.txt-2026-11-27--14-35-07-123456Z",
			Size:      4096,
		},
	}
	detail := versionDetail(local, "Documenti/nota.txt")
	for _, want := range []string{
		"Percorso: Documenti/nota.txt",
		"Dimensione: 4.0 KB (4096 B)",
		"Conservata su: Su questo PC",
		"Copia nello storico: /state/sync-daemon/trash/Documenti/nota.txt-2026-11-27--14-35-07-123456Z",
		"ID versione: 2026-11-27--14-35-07-123456Z",
	} {
		if !strings.Contains(detail, want) {
			t.Errorf("dettaglio locale mancante %q in:\n%s", want, detail)
		}
	}

	remote := fileVersion{
		Source: "nas",
		When:   parseVersionStamp("2026-11-27--13-00-00"),
		Size:   100,
		Suffix: "-2026-11-27--13-00-00",
		Trash:  "Documenti/nota.txt-2026-11-27--13-00-00",
	}
	detail = versionDetail(remote, "Documenti/nota.txt")
	for _, want := range []string{
		"Percorso: Documenti/nota.txt",
		"Conservata su: Sul NAS",
		"Copia nello storico: .sync-trash/Documenti/nota.txt-2026-11-27--13-00-00",
		"ID versione: -2026-11-27--13-00-00",
	} {
		if !strings.Contains(detail, want) {
			t.Errorf("dettaglio NAS mancante %q in:\n%s", want, detail)
		}
	}
}
