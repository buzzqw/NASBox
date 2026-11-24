package ui

import (
	"image"
	"image/color"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
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

// TestMain owns the native event loop because Fyne requires Run on the main
// goroutine. It is active only for the opt-in screenshot process.
func TestMain(m *testing.M) {
	if os.Getenv("NASBOX_UI_SCREENSHOTS") != "1" {
		os.Exit(m.Run())
	}
	_ = os.Setenv("FYNE_BACKEND", "x11")
	configHome, err := os.MkdirTemp("/tmp/opencode", "nasbox-ui-config-")
	if err != nil {
		panic(err)
	}
	stateHome, err := os.MkdirTemp("/tmp/opencode", "nasbox-ui-state-")
	if err != nil {
		panic(err)
	}
	_ = os.Setenv("XDG_CONFIG_HOME", configHome)
	_ = os.Setenv("XDG_STATE_HOME", stateHome)
	a := app.NewWithID("it.nasbox.client.ui-screenshot")
	result := make(chan int, 1)
	go func() { result <- m.Run() }()
	a.Run()
	_ = os.RemoveAll(configHome)
	_ = os.RemoveAll(stateHome)
	os.Exit(<-result)
}

// TestScreenshots is an opt-in real X11/Wayland smoke test. It never starts the
// runner or performs network I/O. Run it with:
//
//	xvfb-run -a env NASBOX_UI_SCREENSHOTS=1 FYNE_SCALE=1 \
//	  go test ./internal/ui -run TestScreenshots -v
func TestScreenshots(t *testing.T) {
	if os.Getenv("NASBOX_UI_SCREENSHOTS") != "1" {
		t.Skip("set NASBOX_UI_SCREENSHOTS=1 under Xvfb to render screenshots")
	}
	_ = os.Setenv("FYNE_BACKEND", "x11")
	i18n.Language = "it"
	t.Cleanup(func() { i18n.Language = "it" })
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
	a := fyne.CurrentApp()
	var window fyne.Window
	fyne.DoAndWait(func() {
		window = a.NewWindow("NASBox UI smoke")
		window.Resize(fyne.NewSize(1180, 800))
	})
	t.Cleanup(func() { fyne.DoAndWait(a.Quit) })
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
	fyne.DoAndWait(func() {
		window.SetContent(c.buildRoot())
		c.showSection(sectionStatus)
		c.applyStatusUI(statusOK, "Tutto sincronizzato. Nessuna modifica in sospeso.")
		window.Show()
	})
	c.updateDevicesUI([]string{
		"pc-122|1792910400|42|ACTIVE",
		"pc-134|1792906800|17|",
	})
	c.updateMetricsUI(map[string]string{
		"VERSION": "3.23.0", "DISK_USAGE_PERCENT": "63", "DISK_AVAILABLE_BYTES": "214748364800",
		"LOAD_1": "0.24", "LOAD_5": "0.31", "LOAD_15": "0.28", "MEM_AVAILABLE_BYTES": "3221225472",
		"NASBOX_LOCK_HELD": "0", "NASBOX_LOCK_PHASE": "idle", "NASBOX_QUEUE_COUNT": "0",
	})
	time.Sleep(500 * time.Millisecond)

	capture := func(path string) {
		t.Helper()
		output, err := exec.Command("import", "-window", "root", path).CombinedOutput()
		if err != nil {
			t.Fatalf("capture %s: %v: %s", path, err, output)
		}
		info, err := os.Stat(path)
		if err != nil || info.Size() == 0 {
			t.Fatalf("screenshot missing or empty: %s (%v)", path, err)
		}
	}

	capture("/tmp/opencode/ui-agent-status.png")
	fyne.DoAndWait(func() { c.navigate(sectionSettings, 0) })
	time.Sleep(300 * time.Millisecond)
	capture("/tmp/opencode/ui-agent-settings.png")
	fyne.DoAndWait(func() { c.navigate(sectionFiles, 0) })
	time.Sleep(300 * time.Millisecond)
	capture("/tmp/opencode/ui-agent-files.png")
	fyne.DoAndWait(func() { c.navigate(sectionNAS, 0) })
	time.Sleep(300 * time.Millisecond)
	capture("/tmp/opencode/ui-agent-nas.png")
	fyne.DoAndWait(func() { c.navigate(sectionActivity, 0) })
	time.Sleep(250 * time.Millisecond)
	capture("/tmp/opencode/ui-agent-activity.png")
	fyne.DoAndWait(func() { c.navigate(sectionFiles, 1) })
	time.Sleep(250 * time.Millisecond)
	capture("/tmp/opencode/ui-agent-conflicts.png")
	fyne.DoAndWait(func() { c.navigate(sectionFiles, 2) })
	time.Sleep(250 * time.Millisecond)
	capture("/tmp/opencode/ui-agent-history.png")
	fyne.DoAndWait(func() { c.navigate(sectionNAS, 1) })
	time.Sleep(250 * time.Millisecond)
	capture("/tmp/opencode/ui-agent-devices.png")
	fyne.DoAndWait(func() { c.navigate(sectionSettings, 4) })
	time.Sleep(250 * time.Millisecond)
	capture("/tmp/opencode/ui-agent-advanced.png")
	fyne.DoAndWait(func() {
		c.setTheme("dark")
		c.showSection(sectionStatus)
	})
	time.Sleep(300 * time.Millisecond)
	capture("/tmp/opencode/ui-agent-status-dark.png")

	// Reject blank/zero-sized captures: X11 GL surfaces can otherwise produce
	// a valid PNG containing only the background colour.
	for _, path := range []string{
		"/tmp/opencode/ui-agent-status.png", "/tmp/opencode/ui-agent-settings.png",
		"/tmp/opencode/ui-agent-files.png", "/tmp/opencode/ui-agent-nas.png",
		"/tmp/opencode/ui-agent-activity.png", "/tmp/opencode/ui-agent-conflicts.png",
		"/tmp/opencode/ui-agent-history.png", "/tmp/opencode/ui-agent-devices.png",
		"/tmp/opencode/ui-agent-advanced.png", "/tmp/opencode/ui-agent-status-dark.png",
	} {
		file, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		img, _, err := image.Decode(file)
		_ = file.Close()
		if err != nil {
			t.Fatalf("decode %s: %v", path, err)
		}
		if img.Bounds().Dx() < 900 || img.Bounds().Dy() < 600 {
			t.Fatalf("unexpected screenshot dimensions for %s: %v", path, img.Bounds())
		}
		colours := map[color.RGBA]struct{}{}
		for y := 0; y < img.Bounds().Dy(); y += 8 {
			for x := 0; x < img.Bounds().Dx(); x += 8 {
				r, g, b, a := img.At(x, y).RGBA()
				colours[color.RGBA{uint8(r >> 8), uint8(g >> 8), uint8(b >> 8), uint8(a >> 8)}] = struct{}{}
			}
		}
		if len(colours) < 12 {
			t.Fatalf("screenshot looks blank (%d sampled colours): %s", len(colours), path)
		}
	}

}
