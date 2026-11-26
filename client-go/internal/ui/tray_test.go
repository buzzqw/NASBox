package ui

import (
	"strings"
	"testing"
	"time"

	"fyne.io/fyne/v2"

	"nasbox-go/internal/conflicts"
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

func TestTrayMenuIsFlatCompactAndDeduplicated(t *testing.T) {
	c := newTestClient(t)
	now := time.Now()
	c.recent = []recentItem{
		{Path: "Documenti/preventivo.txt", Kind: "up", At: now},
		{Path: "Documenti/preventivo.txt", Kind: "up", At: now}, // stesso file due volte
		{Path: "Foto/mare.jpg", Kind: "down", At: now},
		{Path: "Musica/a.mp3", Kind: "up", At: now},
		{Path: "Musica/b.mp3", Kind: "up", At: now},
	}
	items := c.buildTrayItems()

	for _, item := range items {
		if item.ChildMenu != nil {
			t.Fatalf("il menu della tray deve essere piatto (sottomenu vuoti su XFCE): %q", item.Label)
		}
	}
	if n := len(trayLabels(items)); n > 17 {
		t.Fatalf("menu troppo lungo: %d voci %v", n, trayLabels(items))
	}
	count := 0
	for _, item := range items {
		if strings.Contains(item.Label, "preventivo.txt") {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("un file compare una sola volta, trovato %d", count)
	}
	for _, want := range []string{"mare.jpg", "a.mp3", "1 ora", "domani", "Altre pause", "Altri limiti"} {
		if findItem(items, want) == nil {
			t.Fatalf("voce %q assente: %v", want, trayLabels(items))
		}
	}
	if findItem(items, "b.mp3") != nil {
		t.Fatal("al massimo 3 file recenti")
	}
	for _, unwanted := range []string{"Nessun conflitto", "aggiornamenti", "Cerca un file"} {
		if findItem(items, unwanted) != nil {
			t.Fatalf("la voce %q non serve nel menu compatto", unwanted)
		}
	}
}

func TestTrayBandwidthSwitchTogglesAndRemembersLastLimit(t *testing.T) {
	c := newTestClient(t)
	toggle := findAction(c.buildTrayItems(), "Limita velocità a 1 MB/s")
	if toggle == nil || toggle.Checked {
		t.Fatal("senza limite l'interruttore propone 1 MB/s ed è spento")
	}
	toggle.Action()
	if got := c.cfg.Int(keyBandwidthTotal); got != 1024 {
		t.Fatalf("limite applicato = %d", got)
	}
	limited := findAction(c.buildTrayItems(), "Velocità limitata")
	if limited == nil || !limited.Checked {
		t.Fatal("con un limite attivo la voce è spuntata")
	}
	limited.Action()
	if c.cfg.Int(keyBandwidthTotal) != 0 {
		t.Fatal("un secondo clic toglie il limite")
	}
	c.setBandwidth(5120) // scelta fatta altrove (Home o dialogo)
	c.setBandwidth(0)
	if findAction(c.buildTrayItems(), "Limita velocità a 5 MB/s") == nil {
		t.Fatal("l'interruttore deve ricordare l'ultimo limite scelto")
	}
}

func TestTrayShowsConflictsOnlyWhenThereAreSome(t *testing.T) {
	c := newTestClient(t)
	if findItem(c.buildTrayItems(), "onflitt") != nil {
		t.Fatal("senza conflitti la voce non compare")
	}
	c.confGroups = []conflicts.Group{{Original: "a.txt"}}
	if findItem(c.buildTrayItems(), "onflitt") == nil {
		t.Fatal("con un conflitto la voce compare")
	}
}

func TestTrayRecentPlaceholderlessAndResumeWhenPaused(t *testing.T) {
	c := newTestClient(t)
	c.recent = nil
	if findItem(c.buildTrayItems(), "recenti") != nil {
		t.Fatal("senza file recenti l'intera sezione sparisce")
	}
	c.runner.SetPaused(true)
	items := c.buildTrayItems()
	if findItem(items, "Riprendi") == nil && findItem(items, "Resume") == nil {
		t.Fatalf("in pausa la tray deve offrire di riprendere: %v", trayLabels(items))
	}
	if findItem(items, "1 ora") != nil {
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
