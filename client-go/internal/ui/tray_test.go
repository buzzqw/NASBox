package ui

import (
	"strings"
	"testing"
	"time"

	"fyne.io/fyne/v2"
)

func trayLabels(items []*fyne.MenuItem) []string {
	labels := []string{}
	for _, item := range items {
		if !item.IsSeparator {
			labels = append(labels, item.Label)
		}
	}
	return labels
}

func findItem(items []*fyne.MenuItem, fragment string) *fyne.MenuItem {
	for _, item := range items {
		if strings.Contains(item.Label, fragment) {
			return item
		}
	}
	return nil
}

// findAction returns the first clickable item (headers are disabled).
func findAction(items []*fyne.MenuItem, fragment string) *fyne.MenuItem {
	for _, item := range items {
		if !item.Disabled && strings.Contains(item.Label, fragment) {
			return item
		}
	}
	return nil
}

func TestTrayMenuIsFlatAndShowsRecentPauseAndBandwidth(t *testing.T) {
	c := newTestClient(t)
	c.recent = []recentItem{
		{Path: "Documenti/preventivo.txt", Kind: "up", At: time.Now()},
		{Path: "Foto/mare.jpg", Kind: "down", At: time.Now()},
	}
	c.setBandwidth(1024)
	items := c.buildTrayItems()

	for _, item := range items {
		if item.ChildMenu != nil {
			t.Fatalf("il menu della tray deve essere piatto (sottomenu vuoti su XFCE): %q", item.Label)
		}
	}
	for _, want := range []string{"preventivo.txt", "mare.jpg", "15 minuti", "1 ora", "4 ore", "512 KB/s", "5 MB/s"} {
		if findItem(items, want) == nil {
			t.Fatalf("voce %q assente dalla tray: %v", want, trayLabels(items))
		}
	}
	if checked := findAction(items, "1 MB/s"); checked == nil || !checked.Checked {
		t.Fatal("il limite attivo (1 MB/s) deve essere spuntato")
	}
	if other := findAction(items, "512 KB/s"); other == nil || other.Checked {
		t.Fatal("gli altri limiti non devono essere spuntati")
	}
	if findItem(items, "Nessun file recente") != nil {
		t.Fatal("con file recenti non va mostrato il segnaposto")
	}
}

func TestTrayRecentPlaceholderAndResumeWhenPaused(t *testing.T) {
	c := newTestClient(t)
	c.recent = nil
	if findItem(c.buildTrayItems(), "Nessun file recente") == nil {
		t.Fatal("senza file recenti serve il segnaposto")
	}
	c.runner.SetPaused(true)
	items := c.buildTrayItems()
	if findItem(items, "Riprendi") == nil && findItem(items, "Resume") == nil {
		t.Fatalf("in pausa la tray deve offrire di riprendere: %v", trayLabels(items))
	}
	if findItem(items, "15 minuti") != nil {
		t.Fatal("in pausa non si offrono nuove pause")
	}
}

func TestBandwidthSetsBothDirectionsAndReportsMixedLimits(t *testing.T) {
	c := newTestClient(t)
	c.setBandwidth(2048)
	for _, key := range []string{keyBandwidthTotal, keyBandwidthUpload, keyBandwidthDownload} {
		if got := c.cfg.Int(key); got != 2048 {
			t.Fatalf("%s = %d, atteso 2048", key, got)
		}
	}
	if kbps, uniform := c.currentBandwidth(); !uniform || kbps != 2048 {
		t.Fatalf("currentBandwidth = %d %v", kbps, uniform)
	}
	if got := c.bandwidthSummary(); got != "2 MB/s" {
		t.Fatalf("summary = %q", got)
	}
	_ = c.cfg.Set(keyBandwidthUpload, 100, false)
	if _, uniform := c.currentBandwidth(); uniform {
		t.Fatal("upload e download diversi non sono uniformi")
	}
	if got := c.bandwidthSummary(); !strings.Contains(got, "100 KB/s") || !strings.Contains(got, "2 MB/s") {
		t.Fatalf("summary misto = %q", got)
	}
	c.setBandwidth(0)
	if kbps, _ := c.currentBandwidth(); kbps != 0 || c.bandwidthSummary() != "illimitata" {
		t.Fatalf("illimitata: %d %q", kbps, c.bandwidthSummary())
	}
}

func TestTrayRefreshRebuildsOnlyOnChangesAndCoalesces(t *testing.T) {
	c := newTestClient(t)
	c.installTray()
	built := c.trayBuilt
	c.refreshTray() // niente di cambiato
	if !c.trayBuilt.Equal(built) {
		t.Fatal("senza cambiamenti il menu non va ricostruito")
	}
	c.recent = []recentItem{{Path: "a/nuovo.txt", Kind: "up"}}
	c.trayBuilt = time.Now() // appena ricostruito: il cambiamento va accodato
	c.refreshTray()
	if !c.trayPending {
		t.Fatal("un cambiamento ravvicinato va accodato, non perso")
	}
	c.trayBuilt = time.Now().Add(-time.Minute)
	c.trayPending = false
	c.refreshTray()
	if findItem(c.trayMenu.Items, "nuovo.txt") == nil {
		t.Fatal("dopo l'intervallo il menu deve mostrare il nuovo file")
	}
}
