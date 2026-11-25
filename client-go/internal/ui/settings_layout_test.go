package ui

import (
	"fmt"
	"testing"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/widget"

	"nasbox-go/internal/i18n"
)

func TestFormatCountGroupsDigits(t *testing.T) {
	old := i18n.Language
	t.Cleanup(func() { i18n.Language = old })
	i18n.Language = "it"
	for in, want := range map[int]string{0: "0", 999: "999", 1000: "1.000", 111402: "111.402", 19445: "19.445", -1234567: "-1.234.567"} {
		if got := formatCount(in); got != want {
			t.Fatalf("formatCount(%d) = %q, want %q", in, got, want)
		}
	}
	i18n.Language = "en"
	if got := formatCount(111402); got != "111,402" {
		t.Fatalf("en: %q", got)
	}
}

func TestGroupRecentFoldsRunsOfTheSameKindAndFolder(t *testing.T) {
	now := time.Now()
	var items []recentItem
	for index := 0; index < 500; index++ {
		items = append(items, recentItem{Path: fmt.Sprintf("SADE/images/%03d.png", index), Kind: "down", At: now})
	}
	items = append(items,
		recentItem{Path: "Documenti/a.txt", Kind: "up", At: now},
		recentItem{Path: "Documenti/b.txt", Kind: "up", At: now},
		recentItem{Path: "Foto/c.jpg", Kind: "down", At: now},
	)
	groups := groupRecent(items, 7)
	if len(groups) != 3 {
		t.Fatalf("groups = %+v", groups)
	}
	if groups[0].Count != 500 || groups[0].Folder != "SADE/images" || groups[0].Name != "000.png" {
		t.Fatalf("bulk group: %+v", groups[0])
	}
	if groups[1].Count != 2 || groups[2].Count != 1 {
		t.Fatalf("groups: %+v", groups)
	}
	if got := groupRecent(items, 1); len(got) != 1 {
		t.Fatalf("limit not applied: %d", len(got))
	}
}

func TestRecentGroupTextForSingleFileAndBulk(t *testing.T) {
	i18n.Language = "it"
	if got := recentGroupText(recentGroup{Kind: "up", Folder: "Musica", Name: "a.m3u", Count: 1}); got != "a.m3u  ·  Musica" {
		t.Fatalf("single: %q", got)
	}
	if got := recentGroupText(recentGroup{Kind: "down", Folder: "Foto", Name: "x.jpg", Count: 1240}); got != "1.240 file scaricati  ·  Foto" {
		t.Fatalf("bulk: %q", got)
	}
}

// Regression: the field of a setting whose help text wraps onto several lines
// used to be stretched to the height of that text (a 90 px box for "22").
func TestSettingRowKeepsFieldAtNaturalHeight(t *testing.T) {
	test.NewTempApp(t)
	c := &clientApp{uiTheme: newNAStheme(0)}
	entry := widget.NewEntry()
	entry.SetText("22")
	row := c.settingRow("Limite download (KB/s, 0 = illimitato)", compactField(entry, 160), "bandwidth_download_kbps")
	window := test.NewWindow(row)
	window.Resize(fyne.NewSize(900, 400))
	natural := widget.NewEntry().MinSize().Height
	if entry.Size().Height > natural+2 {
		t.Fatalf("entry height %.0f, natural %.0f", entry.Size().Height, natural)
	}
	if entry.Size().Width > 200 {
		t.Fatalf("a number field must stay compact, width %.0f", entry.Size().Width)
	}
}

// A burst of files is queued and flushed once: newest first, nothing lost.
func TestAddRecentCoalescesAndKeepsNewestFirst(t *testing.T) {
	test.NewTempApp(t)
	c := &clientApp{uiTheme: newNAStheme(0)}
	for index := 0; index < 50; index++ {
		c.addRecent(recentItem{Path: fmt.Sprintf("d/%02d.png", index), Kind: "down", At: time.Now()})
	}
	c.recentMu.Lock()
	queued := len(c.recentPending)
	c.recentMu.Unlock()
	if queued != 50 || len(c.recent) != 0 {
		t.Fatalf("items must be queued, not applied one by one: queued=%d applied=%d", queued, len(c.recent))
	}
	c.flushRecent()
	if len(c.recent) != 50 || c.recent[0].Path != "d/49.png" || c.recent[49].Path != "d/00.png" {
		t.Fatalf("order after flush: first=%q len=%d", c.recent[0].Path, len(c.recent))
	}
}
