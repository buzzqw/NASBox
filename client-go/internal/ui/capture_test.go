package ui

import (
	"fmt"
	"fyne.io/fyne/v2/canvas"
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/theme"

	"nasbox-go/internal/config"
	"nasbox-go/internal/conflicts"
	"nasbox-go/internal/engine"
	"nasbox-go/internal/history"
	"nasbox-go/internal/i18n"
	"nasbox-go/internal/remote"
	"nasbox-go/internal/state"
	"nasbox-go/internal/transfer"
)

// TestCaptureSoftware renders every section with Fyne's software renderer, so
// the UI can be reviewed on machines without a display or Xvfb:
//
//	NASBOX_UI_CAPTURE=/tmp/shots go test ./internal/ui -run TestCaptureSoftware
func TestCaptureSoftware(t *testing.T) {
	dir := os.Getenv("NASBOX_UI_CAPTURE")
	if dir == "" {
		t.Skip("set NASBOX_UI_CAPTURE=<dir> to render PNG captures")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	i18n.Language = "it"
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	root := t.TempDir()
	for _, dir := range []string{"Documenti", "Documenti/Progetti", "Foto", "Musica"} {
		if err := os.MkdirAll(filepath.Join(root, filepath.FromSlash(dir)), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for _, path := range []string{
		"Documenti/Progetti/preventivo.txt",
		"Documenti/Progetti/preventivo.txt (conflitto da PC-Salotto 2026-11-24--10-30-00-000000Z)",
	} {
		if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(path)), []byte("contenuto di esempio\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	cfg := config.New("")
	cfg.Set("local_root", root, false)
	cfg.Set("go_theme", "light", false)
	cfg.Set("language", "it", false)
	store, err := state.Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	client := remote.New(cfg)
	a := test.NewTempApp(t)
	window := test.NewWindow(nil)
	window.Resize(fyne.NewSize(1180, 800))
	c := &clientApp{
		cfg: cfg, store: store, client: client,
		transfers: transfer.New(cfg, client),
		runner:    engine.NewRunner(cfg, store, client, transfer.New(cfg, client)),
		fyneApp:   a, window: window,
		uiTheme: newNAStheme(theme.VariantLight),
		level:   statusOK,
	}
	c.setHint = c.setHintText
	a.Settings().SetTheme(c.uiTheme)
	c.bannerGo = func() {}
	c.logLines = []string{
		"19:42:01  Sincronizzazione completata: 0 caricamenti, 0 download",
		"19:41:58  Controllo manifest NAS completato",
		"19:41:55  Connessione SSH stabilita",
	}
	c.activityLines = []string{
		"19:42:01  Sincronizzazione completata",
		"19:40:12  Audit completo: 4 file, 0 differenze",
		"19:38:44  Sincronizzazione richiesta",
	}
	c.transferLines = []string{
		"19:42:01  Sincronizzazione completata: nessun trasferimento",
		"19:41:58  Controllo cambiamenti remoto completato",
	}
	c.browsePath = "Documenti/Progetti"
	c.browseEntries = []remote.BrowseEntry{
		{Name: "Archivio", Kind: "DIR", Size: 0},
		{Name: "preventivo.txt", Kind: "FILE", Size: 2048},
		{Name: "note.txt", Kind: "FILE", Size: 512},
	}
	c.confGroups = []conflicts.Group{{
		Original:   "Documenti/Progetti/preventivo.txt",
		Candidates: []string{"Documenti/Progetti/preventivo.txt (conflitto da PC-Salotto 2026-11-24--10-30-00-000000Z)"},
	}}
	c.versions = []history.Version{{
		TrashPath: "/tmp/trash/Documenti/Progetti/preventivo.txt-2026-11-23--18-05-00-000000Z",
		Original:  "Documenti/Progetti/preventivo.txt",
		Timestamp: "2026-11-23--18-05-00-000000Z", Size: 1536,
	}}
	c.selectiveDirs = []string{"Documenti", "Documenti/Progetti", "Foto", "Musica"}
	window.SetContent(c.buildRoot())
	c.showSection(sectionStatus)
	c.applyStatusUI(statusOK, "Tutto sincronizzato. Nessuna modifica in sospeso.")
	c.updateDevicesUI([]string{
		"pc-122|1792910400|42|ACTIVE",
		"pc-134|1792906800|17|",
	})
	c.updateMetricsUI(map[string]string{
		"VERSION": "3.23.0", "DISK_USAGE_PERCENT": "63", "DISK_AVAILABLE_BYTES": "214748364800",
		"LOAD_1": "0.24", "LOAD_5": "0.31", "LOAD_15": "0.28", "MEM_AVAILABLE_BYTES": "3221225472",
		"NASBOX_LOCK_HELD": "false", "NASBOX_LOCK_PHASE": "idle", "NASBOX_QUEUE_COUNT": "0",
	})

	var walk func(o fyne.CanvasObject, depth int, trail string)
	walk = func(o fyne.CanvasObject, depth int, trail string) {
		if o == nil || !o.Visible() {
			return
		}
		trail = trail + fmt.Sprintf("/%T", o)
		// A visible object with a negative size means a container did not
		// re-layout a child (e.g. a card shown with Show()): it renders
		// squashed in the real app and crashes the software painter.
		if r, ok := o.(*canvas.Rectangle); ok && (r.Size().Width < 0 || r.Size().Height < 0) {
			t.Errorf("negative size %v: %s", r.Size(), trail)
		}
		switch v := o.(type) {
		case *fyne.Container:
			for _, child := range v.Objects {
				walk(child, depth+1, trail)
			}
		case fyne.Widget:
			for _, child := range test.WidgetRenderer(v).Objects() {
				walk(child, depth+1, trail)
			}
		}
	}
	save := func(name string) {
		t.Helper()
		walk(window.Content(), 0, "")
		img := window.Canvas().Capture()
		file, err := os.Create(filepath.Join(dir, name+".png"))
		if err != nil {
			t.Fatal(err)
		}
		defer file.Close()
		if err := png.Encode(file, img); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now()
	c.recent = []recentItem{
		{Path: "Documenti/Progetti/preventivo.txt", Kind: "up", At: now.Add(-1 * time.Minute)},
		{Path: "Foto/2026/mare/IMG_2041.jpg", Kind: "down", At: now.Add(-12 * time.Minute)},
		{Path: "Musica/playlist.m3u", Kind: "up", At: now.Add(-50 * time.Minute)},
		{Path: "Documenti/vecchia-bozza.odt", Kind: "del", At: now.Add(-3 * time.Hour)},
		{Path: "Documenti/Progetti/preventivo.txt", Kind: "conflict", At: now.Add(-26 * time.Hour)},
	}
	c.renderRecent()
	c.refreshTransfersPane()
	c.lastSyncAt = now.Add(-2 * time.Minute)
	c.updateLastSync()
	c.updateMetricsUI(map[string]string{
		"VERSION": "3.27.1", "COLLECTED_AT_EPOCH": "1792910400", "UPTIME_SECONDS": "354300",
		"CPU_TOTAL_TICKS": "1000000", "CPU_IDLE_TICKS": "850000", "CPU_IOWAIT_TICKS": "20000",
		"MEM_TOTAL_BYTES": "1035993088", "MEM_AVAILABLE_BYTES": "330000000",
		"SWAP_TOTAL_BYTES": "2147483648", "SWAP_FREE_BYTES": "1700000000",
		"DISK_USED_BYTES": "12788000000000", "DISK_TOTAL_BYTES": "17430000000000",
		"DISK_AVAILABLE_BYTES": "4020000000000", "DISK_USAGE_PERCENT": "77",
		"DISK_READ_BYTES": "5000000000", "DISK_WRITE_BYTES": "900000000",
		"NET_RX_BYTES": "90000000000", "NET_TX_BYTES": "70000000000",
		"NET_RX_PACKETS": "5000000", "NET_TX_PACKETS": "4000000", "NET_INTERFACE_COUNT": "2",
		"LOAD_1": "0.52", "LOAD_5": "0.50", "LOAD_15": "0.47",
		"NASBOX_LOCK_HELD": "false", "NASBOX_LOCK_PHASE": "idle", "NASBOX_QUEUE_COUNT": "0",
	})
	c.updateDevicesUI([]string{"pc-122|1792910400|42|ACTIVE", "pc-134|1792906800|17|"})
	save("01-status")
	c.applyStatusUI(statusProgress, "Sincronizzazione in corso")
	c.renderProgress(transfer.Progress{Upload: true, File: "Foto/2026/mare/VID_2044.mp4",
		Percent: 47, Bytes: 734003200, Rate: "21.8MB/s", ETA: "0:42", Done: 2, Total: 12})
	save("01b-status-transfer")
	// Bulk download: thousands of files in one folder fold into one row and
	// the progress block shows files done / bytes with one consistent source.
	bulk := make([]recentItem, 0, 1400)
	for index := 0; index < 1240; index++ {
		bulk = append(bulk, recentItem{Path: fmt.Sprintf("SADE/SADE/images/img%04d.png", 1240-index), Kind: "down", At: now})
	}
	c.recent = append(bulk, c.recent...)
	c.renderRecent()
	c.refreshTransfersPane()
	c.renderProgress(transfer.Progress{File: "SADE/SADE/images/eltab.png", Percent: 53, Bytes: 38000000000,
		TotalBytes: 71000000000, Rate: "16.5 MB/s", ETA: "16:14", Done: 10301, Total: 19445})
	save("01c-status-bulk")
	c.navigate(sectionActivity, 1)
	save("02c-transfers-live")
	c.navigate(sectionStatus, 0)
	c.applyStatusUI(statusOK, "Tutto sincronizzato. Nessuna modifica in sospeso.")
	c.progress = transfer.Progress{Finished: true}
	c.setTransferCardVisible(false)
	shots := []struct {
		name    string
		section sectionID
		tab     int
	}{
		{"02-activity", sectionActivity, 0}, {"02b-transfers", sectionActivity, 1}, {"03-files", sectionFiles, 0},
		{"04-conflicts", sectionFiles, 1}, {"05-history", sectionFiles, 2},
		{"06-nas", sectionNAS, 0}, {"07-devices", sectionNAS, 1}, {"07b-storage", sectionNAS, 2},
		{"08c-security", sectionSettings, 1}, {"08d-integrity", sectionSettings, 2},
		{"08-settings", sectionSettings, 0}, {"08b-folders", sectionSettings, 3}, {"09-advanced", sectionSettings, 4},
	}
	for _, shot := range shots {
		c.navigate(shot.section, shot.tab)
		save(shot.name)
	}
	for index := 0; index < 90; index++ {
		wave := float64(index % 30)
		c.cpuChart.add(12 + wave*1.8 + float64(index%7)*3)
		c.loadChart.add(0.3 + wave/25)
		c.netChart.add(float64((index*37)%23) / 3)
	}
	c.navigate(sectionNAS, 0)
	save("06b-nas-charts")
	c.navigate(sectionSettings, 5)
	save("09b-info-health")
	c.setTheme("dark")
	c.showSection(sectionStatus)
	save("10-status-dark")
	_ = a
}
