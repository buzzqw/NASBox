package ui

import (
	"os"
	"path/filepath"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"nasbox-go/internal/integration"
	"nasbox-go/internal/ipc"
)

// startDesktopIntegration serves the local control socket (file-manager
// menus, "nasbox-go show", a second launch) and opens a queued request.
func (c *clientApp) startDesktopIntegration() func() {
	stop, err := ipc.Serve(func(command, argument string) string {
		switch command {
		case "show":
			fyne.Do(c.showWindow)
			return "ok"
		case "versions":
			fyne.Do(func() {
				c.showWindow()
				c.showFileVersions(argument)
			})
			return "ok"
		case "sync":
			c.onSync()
			return "ok"
		case "status":
			return c.trayStatusText()
		}
		return "comando sconosciuto"
	})
	if err != nil {
		c.appendLog("controllo locale non disponibile: " + err.Error())
		stop = func() {}
	}
	if pending := strings.TrimSpace(os.Getenv("NASBOX_OPEN_VERSIONS")); pending != "" {
		_ = os.Unsetenv("NASBOX_OPEN_VERSIONS")
		fyne.Do(func() {
			c.showWindow()
			c.showFileVersions(pending)
		})
	}
	return stop
}

// emblemsEnabled: on by default, "go_emblems": false turns them off.
func (c *clientApp) emblemsEnabled() bool {
	value, ok := c.cfg.Get("go_emblems").(bool)
	return !ok || value
}

// updateFolderEmblem shows the sync state on the NASBox folder itself, in
// the file manager (only when the state changes; gio runs in background).
func (c *clientApp) updateFolderEmblem(level statusLevel) {
	if !c.emblemsEnabled() || c.cfg.LocalRoot() == "" {
		return
	}
	emblem := ""
	switch level {
	case statusOK:
		emblem = integration.EmblemSynced
	case statusProgress:
		emblem = integration.EmblemSyncing
	case statusAttention, statusBlocked, statusOffline:
		emblem = integration.EmblemProblem
	default:
		return
	}
	c.emblemMu.Lock()
	changed := emblem != c.lastEmblem
	c.lastEmblem = emblem
	c.emblemMu.Unlock()
	if changed {
		root := c.cfg.LocalRoot()
		go integration.SetEmblem(root, emblem)
	}
}

// markConflictCopies puts the "important" emblem on conflict copies so they
// stand out in the file manager.
func (c *clientApp) markConflictCopies(paths []string) {
	if !c.emblemsEnabled() {
		return
	}
	root := c.cfg.LocalRoot()
	c.emblemMu.Lock()
	if c.marked == nil {
		c.marked = map[string]bool{}
	}
	todo := []string{}
	for _, relative := range paths {
		if !c.marked[relative] {
			c.marked[relative] = true
			todo = append(todo, filepath.Join(root, filepath.FromSlash(relative)))
		}
	}
	c.emblemMu.Unlock()
	if len(todo) > 0 {
		go func() {
			for _, path := range todo {
				integration.SetEmblem(path, integration.EmblemProblem)
			}
		}()
	}
}

// settingsDesktop is the "Integrazione con il desktop" group in Settings.
func (c *clientApp) settingsDesktop() fyne.CanvasObject {
	state := widget.NewLabel("")
	state.Wrapping = fyne.TextWrapWord
	var toggle *widget.Button
	refresh := func() {
		if integration.Installed() {
			state.SetText(tr("ui.desktop.menu_on", nil))
			toggle.SetText(tr("ui.desktop.menu_remove", nil))
			toggle.Importance = widget.MediumImportance
		} else {
			state.SetText(tr("ui.desktop.menu_off", nil))
			toggle.SetText(tr("ui.desktop.menu_add", nil))
			toggle.Importance = widget.HighImportance
		}
		toggle.Refresh()
	}
	toggle = widget.NewButtonWithIcon("", theme.FolderOpenIcon(), func() {
		var result integration.Result
		var err error
		if integration.Installed() {
			result, err = integration.Remove()
		} else {
			binary, exeErr := os.Executable()
			if exeErr != nil {
				err = exeErr
			} else {
				result, err = integration.Install(binary)
			}
		}
		if err != nil {
			c.appendAction(tr("ui.desktop.failed", map[string]any{"detail": err.Error()}))
		} else if len(result.Changed) > 0 {
			c.appendAction(tr("ui.desktop.done", map[string]any{"list": strings.Join(result.Changed, ", ")}))
		}
		refresh()
	})
	refresh()
	emblems := widget.NewCheck("", func(on bool) {
		c.persistSetting("go_emblems", on)
		if on {
			c.emblemMu.Lock()
			c.lastEmblem = ""
			c.emblemMu.Unlock()
			c.updateFolderEmblem(c.level)
		} else {
			go integration.SetEmblem(c.cfg.LocalRoot(), "")
		}
	})
	emblems.SetChecked(c.emblemsEnabled())
	return c.listGroup(tr("ui.desktop.title", nil), tr("ui.desktop.subtitle", nil),
		container.NewBorder(nil, nil, nil, toggle, state),
		container.NewBorder(nil, nil, emblems, nil, c.note(tr("ui.desktop.emblems", nil))))
}
