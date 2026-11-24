// Package ui is the native Fyne client. It supports both X11 and Wayland
// through Fyne's backend selection (FYNE_BACKEND=x11|wayland, auto-detected
// from WAYLAND_DISPLAY when the config leaves it on "auto").
package ui

import (
	_ "embed"
	"fmt"
	"os"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"

	"nasbox-go/internal/config"
	"nasbox-go/internal/engine"
	"nasbox-go/internal/i18n"
	"nasbox-go/internal/remote"
	"nasbox-go/internal/state"
	"nasbox-go/internal/transfer"
	"nasbox-go/internal/version"
)

//go:embed icon.png
var iconBytes []byte

type clientApp struct {
	cfg       *config.Config
	store     *state.Store
	client    *remote.Client
	transfers *transfer.Transfer
	status    *widget.Label
	log       *widget.List
	lines     []string
	fyneApp   fyne.App
	window    fyne.Window
}

// Run starts the native client. It blocks until the window/tray exits.
func Run(cfg *config.Config, store *state.Store) {
	selectBackend(cfg)
	fyneApp := app.NewWithID("it.nasbox.client")
	fyneApp.SetIcon(fyne.NewStaticResource("icon.png", iconBytes))
	window := fyneApp.NewWindow(fmt.Sprintf("%s %s", version.AppName, version.AppVersion))
	window.Resize(fyne.NewSize(900, 640))

	c := &clientApp{
		cfg: cfg, store: store, client: remote.New(cfg),
		status:  widget.NewLabel("Pronto."),
		fyneApp: fyneApp, window: window,
	}
	c.transfers = transfer.New(cfg, c.client)
	c.status.Wrapping = fyne.TextWrapWord
	c.log = widget.NewList(
		func() int { return len(c.lines) },
		func() fyne.CanvasObject { return widget.NewLabel("") },
		func(id widget.ListItemID, object fyne.CanvasObject) {
			object.(*widget.Label).SetText(c.lines[id])
		},
	)

	statusTab := container.NewVBox(
		widget.NewLabel("Stato sincronizzazione"), c.status,
		widget.NewButton("Sincronizza adesso", c.onSync),
		widget.NewButton("Anteprima modifiche", c.onPreview),
		widget.NewButton("Annulla eliminazioni recenti", c.onUndo),
	)
	logTab := container.NewBorder(widget.NewLabel("Log"), nil, nil, nil, c.log)
	settingsTab := container.NewVBox(
		widget.NewLabel(fmt.Sprintf("Configurazione: %s", cfg.Path)),
		widget.NewLabel(fmt.Sprintf("Cartella locale: %s", cfg.LocalRoot())),
		widget.NewLabel(fmt.Sprintf("Lingua: %s", i18n.Language)),
	)

	tabs := container.NewAppTabs(
		container.NewTabItem("Stato", statusTab),
		container.NewTabItem("Log", logTab),
		container.NewTabItem("Impostazioni", settingsTab),
	)
	window.SetContent(tabs)

	tray := fyne.NewMenu("NASBox",
		fyne.NewMenuItem("Mostra", func() { window.Show() }),
		fyne.NewMenuItem("Sincronizza", c.onSync),
		fyne.NewMenuItem("Annulla eliminazioni", c.onUndo),
		fyne.NewMenuItemSeparator(),
		fyne.NewMenuItem("Esci", func() { fyneApp.Quit() }),
	)
	if trayApp, ok := fyneApp.(interface{ SetSystemTrayMenu(*fyne.Menu) }); ok {
		trayApp.SetSystemTrayMenu(tray)
	}

	window.SetCloseIntercept(func() { window.Hide() })
	c.appendLog("Client Go avviato (" + version.AppVersion + ")")
	window.ShowAndRun()
}

// selectBackend must run before app.NewWithID.
func selectBackend(cfg *config.Config) {
	choice := strings.ToLower(cfg.String("go_display_backend"))
	if choice == "" || choice == "auto" {
		if os.Getenv("WAYLAND_DISPLAY") != "" {
			choice = "wayland"
		} else {
			choice = "x11"
		}
	}
	if choice == "x11" || choice == "wayland" {
		_ = os.Setenv("FYNE_BACKEND", choice)
	}
}

func (c *clientApp) appendLine(line string) {
	c.lines = append([]string{time.Now().Format("15:04:05") + "  " + line}, c.lines...)
	if len(c.lines) > 300 {
		c.lines = c.lines[:300]
	}
}

func (c *clientApp) appendLog(line string) {
	fyne.Do(func() {
		c.appendLine(line)
		c.log.Refresh()
		c.status.SetText(line)
	})
}

func (c *clientApp) onSync() {
	go func() {
		result, err := engine.SyncOnce(c.cfg, c.store, c.client, c.transfers)
		if err != nil {
			c.appendLog("Errore sync: " + err.Error())
			return
		}
		if result.Blocked != "" {
			c.appendLog("Bloccato: " + result.Blocked)
			return
		}
		c.appendLog(fmt.Sprintf("Sync: %d caricati, %d cancellati", result.Uploaded, result.Deleted))
	}()
}

func (c *clientApp) onUndo() {
	go func() {
		entries, err := c.store.RecentUndo(c.cfg.RepositoryID(), 24, 500)
		if err != nil || len(entries) == 0 {
			c.appendLog("Nessuna eliminazione recente da annullare")
			return
		}
		requests := make([]remote.RestoreRequest, 0, len(entries))
		batchByPath := map[string]string{}
		for _, item := range entries {
			requests = append(requests, remote.RestoreRequest{Path: item.Path, VersionTS: item.VersionTS})
			batchByPath[item.Path] = item.BatchID
		}
		statuses, err := c.client.RestoreBatch(requests, c.store.DeviceID())
		if err != nil {
			c.appendLog("Errore undo: " + err.Error())
			return
		}
		restored := 0
		for path, status := range statuses {
			if status == "RESTORED" {
				_ = c.store.MarkUndoApplied(c.cfg.RepositoryID(), batchByPath[path], []string{path})
				restored++
			}
		}
		c.appendLog(fmt.Sprintf("Recuperati %d file", restored))
	}()
}

func (c *clientApp) onPreview() {
	go func() {
		raw, err := c.client.ManifestExport("")
		if err != nil {
			c.appendLog("Errore anteprima: " + err.Error())
			return
		}
		manifest, err := remote.ParseManifest(raw)
		if err != nil {
			c.appendLog("Errore manifest: " + err.Error())
			return
		}
		report, err := engine.Preview(c.cfg, c.store, manifest)
		if err != nil {
			c.appendLog("Errore anteprima: " + err.Error())
			return
		}
		c.appendLog("Anteprima: " + report.Summary())
	}()
}
