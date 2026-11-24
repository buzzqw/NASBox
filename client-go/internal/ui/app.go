// Package ui is the native Fyne client. It supports both X11 and Wayland.
package ui

import (
	"context"
	_ "embed"
	"fmt"
	"os"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"

	"nasbox-go/internal/config"
	"nasbox-go/internal/conflicts"
	"nasbox-go/internal/engine"
	"nasbox-go/internal/history"
	"nasbox-go/internal/i18n"
	"nasbox-go/internal/integrity"
	"nasbox-go/internal/mirror"
	"nasbox-go/internal/remote"
	"nasbox-go/internal/selective"
	"nasbox-go/internal/state"
	"nasbox-go/internal/transfer"
	"nasbox-go/internal/update"
	"nasbox-go/internal/version"
)

//go:embed icon.png
var iconBytes []byte

type clientApp struct {
	cfg       *config.Config
	store     *state.Store
	client    *remote.Client
	transfers *transfer.Transfer
	runner    *engine.Runner
	cancel    context.CancelFunc

	assurance  *widget.Label
	logList    *widget.List
	logLines   []string
	confList   *widget.List
	confGroups []conflicts.Group
	histList   *widget.List
	versions   []history.Version
	nasLabel   *widget.Label
	pauseBtn   *widget.Button

	browsePath    string
	browseEntries []remote.BrowseEntry
	browseList    *widget.List
	mirrorLabel   *widget.Label
	selectiveList *widget.List
	selectiveDirs []string
	metricsLabel  *widget.Label
	devicesLabel  *widget.Label

	fyneApp          fyne.App
	window           fyne.Window
	selectedConflict string
	selectedVersion  string
	selectedBrowse   string
}

// Run starts the native client. It blocks until the window/tray exits.
func Run(cfg *config.Config, store *state.Store) {
	selectBackend(cfg)
	fyneApp := app.NewWithID("it.nasbox.client")
	fyneApp.SetIcon(fyne.NewStaticResource("icon.png", iconBytes))
	window := fyneApp.NewWindow(fmt.Sprintf("%s %s", version.AppName, version.AppVersion))
	window.Resize(fyne.NewSize(1024, 720))

	client := remote.New(cfg)
	transfers := transfer.New(cfg, client)
	ctx, cancel := context.WithCancel(context.Background())
	c := &clientApp{
		cfg: cfg, store: store, client: client, transfers: transfers,
		cancel:    cancel,
		assurance: widget.NewLabel("Avvio…"),
		runner:    engine.NewRunner(cfg, store, client, transfers),
		fyneApp:   fyneApp, window: window,
	}
	c.assurance.Wrapping = fyne.TextWrapWord
	c.logList = widget.NewList(
		func() int { return len(c.logLines) },
		func() fyne.CanvasObject { return widget.NewLabel("") },
		func(id widget.ListItemID, object fyne.CanvasObject) { object.(*widget.Label).SetText(c.logLines[id]) },
	)
	c.confList = widget.NewList(
		func() int { return len(c.confGroups) },
		func() fyne.CanvasObject { return widget.NewLabel("") },
		func(id widget.ListItemID, object fyne.CanvasObject) {
			group := c.confGroups[id]
			object.(*widget.Label).SetText(fmt.Sprintf("%s  (%d versioni)", group.Original, len(group.Candidates)))
		},
	)
	c.confList.OnSelected = func(id widget.ListItemID) {
		if id < len(c.confGroups) {
			c.selectedConflict = c.confGroups[id].Candidates[0]
		}
	}
	c.histList = widget.NewList(
		func() int { return len(c.versions) },
		func() fyne.CanvasObject { return widget.NewLabel("") },
		func(id widget.ListItemID, object fyne.CanvasObject) {
			v := c.versions[id]
			object.(*widget.Label).SetText(fmt.Sprintf("%s  —  %s", v.Original, v.Timestamp))
		},
	)
	c.histList.OnSelected = func(id widget.ListItemID) {
		if id < len(c.versions) {
			c.selectedVersion = c.versions[id].TrashPath
		}
	}
	c.nasLabel = widget.NewLabel("Stato NAS: sconosciuto")
	c.metricsLabel = widget.NewLabel("Metriche: in attesa…")
	c.devicesLabel = widget.NewLabel("Dispositivi: in attesa…")
	c.mirrorLabel = widget.NewLabel("Cartelle esterne: nessuna")
	c.browseList = widget.NewList(
		func() int { return len(c.browseEntries) },
		func() fyne.CanvasObject { return widget.NewLabel("") },
		func(id widget.ListItemID, object fyne.CanvasObject) {
			entry := c.browseEntries[id]
			object.(*widget.Label).SetText(fmt.Sprintf("%s   %s   %d byte", entry.Name, entry.Kind, entry.Size))
		},
	)
	c.browseList.OnSelected = func(id widget.ListItemID) {
		if id < len(c.browseEntries) {
			c.selectedBrowse = c.browseEntries[id].Name
		}
	}
	c.selectiveList = widget.NewList(
		func() int { return len(c.selectiveDirs) },
		func() fyne.CanvasObject { return widget.NewLabel("") },
		func(id widget.ListItemID, object fyne.CanvasObject) {
			dir := c.selectiveDirs[id]
			prefix := "☑ "
			if selective.Excluded(c.cfg, dir) {
				prefix = "☐ "
			}
			object.(*widget.Label).SetText(prefix + dir)
		},
	)
	c.selectiveList.OnSelected = func(id widget.ListItemID) {
		if id >= len(c.selectiveDirs) {
			return
		}
		dir := c.selectiveDirs[id]
		excluded := []string{}
		for _, text := range selective.ExcludedPaths(c.cfg) {
			if text != dir {
				excluded = append(excluded, text)
			}
		}
		if !selective.Excluded(c.cfg, dir) {
			excluded = append(excluded, dir)
		}
		selective.SetExcluded(c.cfg, excluded)
		c.selectiveList.Refresh()
		c.appendLog("Selezione sincronizzazione aggiornata")
	}

	c.pauseBtn = widget.NewButton("Pausa", c.onPause)
	statusTab := container.NewVBox(
		widget.NewLabel("Garanzia di sincronizzazione"), c.assurance,
		widget.NewButton("Sincronizza adesso", c.onSync),
		c.pauseBtn,
		widget.NewButton("Anteprima modifiche", c.onPreview),
		widget.NewButton("Verifica integrità", c.onAudit),
		widget.NewButton("Annulla eliminazioni recenti", c.onUndo),
	)
	transfersTab := container.NewBorder(
		widget.NewLabel("Attività di trasferimento"), nil, nil, nil, c.logList,
	)
	conflictsTab := container.NewBorder(
		widget.NewLabel("Conflitti rilevati"),
		widget.NewButton("Risolvi con la versione selezionata", c.onResolveConflict),
		nil, nil, c.confList,
	)
	historyTab := container.NewBorder(
		widget.NewLabel("Storico locale (cestino)"),
		widget.NewButton("Ripristina versione selezionata", c.onRestoreVersion),
		nil, nil, c.histList,
	)
	monitorTab := container.NewVBox(c.nasLabel, c.metricsLabel, c.devicesLabel)
	browseTab := container.NewBorder(
		container.NewVBox(
			widget.NewButton("↑ Su", c.onBrowseUp),
			widget.NewButton("Elimina selezionato", c.onBrowseDelete),
			widget.NewButton("Rinomina selezionato", c.onBrowseRename),
		),
		nil, nil, nil, c.browseList,
	)
	mirrorTab := container.NewVBox(
		c.mirrorLabel,
		widget.NewButton("Sincronizza cartelle esterne", c.onMirrorSync),
	)
	selectiveTab := container.NewBorder(
		widget.NewLabel("Seleziona le cartelle da sincronizzare (clic per escludere/includere)"),
		nil, nil, nil, c.selectiveList,
	)
	settingsTab := container.NewVBox(
		widget.NewLabel(fmt.Sprintf("Configurazione: %s", cfg.Path)),
		widget.NewLabel(fmt.Sprintf("Cartella locale: %s", cfg.LocalRoot())),
		widget.NewLabel(fmt.Sprintf("Backend display: %s", displayBackend(cfg))),
		widget.NewLabel(fmt.Sprintf("Lingua: %s (il cambio richiede il riavvio)", i18n.Language)),
		container.NewHBox(
			widget.NewButton("Italiano", func() { c.setLanguage("it") }),
			widget.NewButton("English", func() { c.setLanguage("en") }),
		),
		widget.NewLabel(fmt.Sprintf("Versione Go: %s", version.AppVersion)),
		widget.NewLabel(fmt.Sprintf("Banda: %d kbps", cfg.Int("bandwidth_kbps"))),
		widget.NewButton("Controlla aggiornamenti Go", c.onCheckUpdate),
	)

	tabs := container.NewAppTabs(
		container.NewTabItem("Stato", statusTab),
		container.NewTabItem("Trasferimenti", transfersTab),
		container.NewTabItem("Log", container.NewBorder(widget.NewLabel("Log eventi"), nil, nil, nil, c.logList)),
		container.NewTabItem("Conflitti", conflictsTab),
		container.NewTabItem("Storico", historyTab),
		container.NewTabItem("Sfoglia NAS", browseTab),
		container.NewTabItem("Cartelle esterne", mirrorTab),
		container.NewTabItem("Selettivo", selectiveTab),
		container.NewTabItem("Monitor NAS", monitorTab),
		container.NewTabItem("Impostazioni", settingsTab),
	)
	window.SetContent(tabs)

	tray := fyne.NewMenu("NASBox",
		fyne.NewMenuItem("Mostra", func() { window.Show() }),
		fyne.NewMenuItem("Sincronizza", c.onSync),
		fyne.NewMenuItem("Pausa/Riprendi", c.onPause),
		fyne.NewMenuItem("Annulla eliminazioni", c.onUndo),
		fyne.NewMenuItemSeparator(),
		fyne.NewMenuItem("Esci", func() { c.cancel(); fyneApp.Quit() }),
	)
	if trayApp, ok := fyneApp.(interface{ SetSystemTrayMenu(*fyne.Menu) }); ok {
		trayApp.SetSystemTrayMenu(tray)
	}
	window.SetCloseIntercept(func() { window.Hide() })

	go c.consumeEvents()
	go func() { _ = c.runner.Run(ctx) }()
	go func() {
		values, err := c.client.PrintConfig()
		if err != nil {
			return
		}
		fyne.Do(func() {
			c.nasLabel.SetText(fmt.Sprintf("NAS %s — server %s, repository %s",
				c.cfg.String("nas_lan"), values["VERSION"], values["REPOSITORY_ID"]))
		})
	}()
	c.refreshConflicts()
	c.refreshHistory()
	c.refreshBrowse()
	c.refreshSelective()
	c.refreshMirrorLabel()
	go c.refreshMonitor()
	c.appendLog("Client Go avviato (" + version.AppVersion + ")")
	window.ShowAndRun()
	c.cancel()
}

func displayBackend(cfg *config.Config) string {
	choice := strings.ToLower(cfg.String("go_display_backend"))
	if choice == "" || choice == "auto" {
		if os.Getenv("WAYLAND_DISPLAY") != "" {
			return "wayland (auto)"
		}
		return "x11 (auto)"
	}
	return choice
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
	c.logLines = append([]string{time.Now().Format("15:04:05") + "  " + line}, c.logLines...)
	if len(c.logLines) > 300 {
		c.logLines = c.logLines[:300]
	}
}

func (c *clientApp) appendLog(line string) {
	fyne.Do(func() {
		c.appendLine(line)
		c.logList.Refresh()
		c.assurance.SetText(line)
	})
}

func (c *clientApp) consumeEvents() {
	for event := range c.runner.Events() {
		message := event.Message
		switch event.Kind {
		case "sync":
			c.appendLog("Sincronizzazione completata:" + message)
			c.refreshConflicts()
			c.refreshHistory()
		case "blocked":
			c.appendLog("Bloccato: " + message)
		case "error":
			c.appendLog("Errore: " + message)
		default:
			c.appendLog(message)
		}
	}
}

func (c *clientApp) refreshConflicts() {
	groups, err := conflicts.Scan(c.cfg.LocalRoot(), nil)
	if err != nil {
		return
	}
	fyne.Do(func() {
		c.confGroups = groups
		c.confList.Refresh()
	})
}

func (c *clientApp) refreshHistory() {
	versions, err := history.List()
	if err != nil {
		return
	}
	fyne.Do(func() {
		c.versions = versions
		c.histList.Refresh()
	})
}

func (c *clientApp) onSync() { c.runner.SyncNow() }

func (c *clientApp) onPause() {
	paused := !c.runner.IsPaused()
	c.runner.SetPaused(paused)
	label := "Pausa"
	if paused {
		label = "Riprendi"
	}
	c.pauseBtn.SetText(label)
	c.appendLog(fmt.Sprintf("sincronizzazione %s", map[bool]string{true: "in pausa", false: "ripresa"}[paused]))
}

func (c *clientApp) onAudit() {
	go func() {
		raw, err := c.client.ManifestExport("")
		if err != nil {
			c.appendLog("Errore audit: " + err.Error())
			return
		}
		manifest, err := remote.ParseManifest(raw)
		if err != nil {
			c.appendLog("Errore audit: " + err.Error())
			return
		}
		report := integrity.Run(c.cfg, manifest, version.AppVersion, nil)
		c.appendLog(fmt.Sprintf("Audit: %s — %d file, %d differenze",
			report.Status, report.FilesChecked, len(report.Differences)))
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
			c.appendLog("Errore anteprima: " + err.Error())
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

func (c *clientApp) onResolveConflict() {
	if c.selectedConflict == "" {
		c.appendLog("Seleziona prima una versione")
		return
	}
	go func() {
		for _, group := range c.confGroups {
			for _, candidate := range group.Candidates {
				if candidate != c.selectedConflict {
					continue
				}
				if err := conflicts.Resolve(c.cfg.LocalRoot(), group, candidate); err != nil {
					c.appendLog("Errore risoluzione: " + err.Error())
				} else {
					c.appendLog("Conflitto risolto: " + group.Original)
					c.refreshConflicts()
				}
				return
			}
		}
	}()
}

func (c *clientApp) refreshBrowse() {
	entries, err := c.client.BrowseList(c.browsePath)
	if err != nil {
		c.appendLog("Errore sfoglia NAS: " + err.Error())
		return
	}
	fyne.Do(func() {
		c.browseEntries = entries
		c.browseList.Refresh()
	})
}

func (c *clientApp) onBrowseUp() {
	parts := strings.Split(c.browsePath, "/")
	if len(parts) > 0 {
		c.browsePath = strings.Join(parts[:len(parts)-1], "/")
	}
	c.refreshBrowse()
}

func (c *clientApp) onBrowseDelete() {
	if c.selectedBrowse == "" {
		c.appendLog("Seleziona prima un elemento")
		return
	}
	path := c.selectedBrowse
	if c.browsePath != "" {
		path = c.browsePath + "/" + path
	}
	go func() {
		status, err := c.client.BrowseDelete(path, remote.RunTimestamp(), c.store.DeviceID())
		if err != nil {
			c.appendLog("Errore eliminazione remota: " + err.Error())
			return
		}
		c.appendLog("Eliminazione remota: " + status)
		c.refreshBrowse()
	}()
}

func (c *clientApp) onBrowseRename() {
	if c.selectedBrowse == "" {
		c.appendLog("Seleziona prima un elemento")
		return
	}
	current := c.selectedBrowse
	dialog.ShowEntryDialog("Rinomina", "Nuovo nome:", func(name string) {
		if name == "" {
			return
		}
		src, dst := current, name
		if c.browsePath != "" {
			src = c.browsePath + "/" + src
			dst = c.browsePath + "/" + dst
		}
		go func() {
			status, err := c.client.BrowseRename(src, dst, remote.RunTimestamp(), c.store.DeviceID())
			if err != nil {
				c.appendLog("Errore rinomina remota: " + err.Error())
				return
			}
			c.appendLog("Rinomina remota: " + status)
			c.refreshBrowse()
		}()
	}, c.window)
}

func (c *clientApp) refreshSelective() {
	dirs := selective.ListDirectories(c.cfg.LocalRoot(), 1000)
	fyne.Do(func() {
		c.selectiveDirs = dirs
		c.selectiveList.Refresh()
	})
}

func (c *clientApp) refreshMirrorLabel() {
	mirrors := mirror.Configured(c.cfg)
	lines := []string{}
	for _, item := range mirrors {
		state := "disabilitata"
		if item.Enabled {
			state = "attiva"
		}
		lines = append(lines, fmt.Sprintf("%s → %s (%s)", item.Source, item.Dest, state))
	}
	text := "Cartelle esterne: nessuna"
	if len(lines) > 0 {
		text = "Cartelle esterne:\n" + strings.Join(lines, "\n")
	}
	fyne.Do(func() { c.mirrorLabel.SetText(text) })
}

func (c *clientApp) onMirrorSync() {
	go func() {
		count, err := mirror.SyncOnce(c.cfg, c.client)
		if err != nil {
			c.appendLog("Errore mirror: " + err.Error())
			return
		}
		c.appendLog(fmt.Sprintf("Mirror sincronizzati: %d", count))
		c.refreshMirrorLabel()
	}()
}

func (c *clientApp) refreshMonitor() {
	for {
		values, err := c.client.Metrics()
		if err == nil {
			text := fmt.Sprintf("NAS %s — server %s\n",
				c.cfg.String("nas_lan"), values["VERSION"])
			text += fmt.Sprintf("Disco: %s%% usato, disponibili %s byte\n",
				values["DISK_USAGE_PERCENT"], values["DISK_AVAILABLE_BYTES"])
			text += fmt.Sprintf("Carico: %s / %s / %s · RAM disponibile %s byte\n",
				values["LOAD_1"], values["LOAD_5"], values["LOAD_15"], values["MEM_AVAILABLE_BYTES"])
			text += fmt.Sprintf("Lock: %s (%s) · coda %s",
				values["NASBOX_LOCK_HELD"], values["NASBOX_LOCK_PHASE"], values["NASBOX_QUEUE_COUNT"])
			fyne.Do(func() { c.nasLabel.SetText(text) })
		}
		devices, err := c.client.Devices()
		if err == nil {
			fyne.Do(func() {
				c.devicesLabel.SetText("Dispositivi:\n" + strings.Join(devices, "\n"))
			})
		}
		time.Sleep(30 * time.Second)
	}
}

func (c *clientApp) setLanguage(language string) {
	i18n.Language = language
	c.cfg.Set("language", language, true)
	c.appendLog("Lingua impostata a " + language + " (riavvio richiesto per tutti i testi)")
}

func (c *clientApp) onCheckUpdate() {
	go func() {
		manifest, err := update.Newest(c.cfg, c.client)
		if err != nil {
			c.appendLog("Errore controllo aggiornamenti: " + err.Error())
			return
		}
		if update.Available(manifest) {
			c.appendLog("Aggiornamento Go disponibile: " + manifest.Version)
		} else {
			c.appendLog("Nessun aggiornamento Go disponibile")
		}
	}()
}

func (c *clientApp) onRestoreVersion() {
	if c.selectedVersion == "" {
		c.appendLog("Seleziona prima una versione")
		return
	}
	go func() {
		for _, version := range c.versions {
			if version.TrashPath != c.selectedVersion {
				continue
			}
			if err := history.Restore(version, c.cfg.LocalRoot()); err != nil {
				c.appendLog("Errore ripristino: " + err.Error())
			} else {
				c.appendLog("Ripristinato: " + version.Original)
				c.refreshHistory()
			}
			return
		}
	}()
}
