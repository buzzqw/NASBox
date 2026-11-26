package ui

import (
	"fmt"
	"nasbox-go/internal/osx"
	"strings"
	"time"

	"fyne.io/fyne/v2"

	"nasbox-go/internal/version"
)

// The tray menu is deliberately flat. Many desktops (XFCE's status notifier
// plugin, waybar and others) draw dbusmenu submenus empty, so "Pause" and
// "Recent files" looked blank; top-level items work everywhere. The menu is
// rebuilt only when its content changes, and at most every trayMinInterval:
// every rebuild re-registers all items with the system tray.
const trayMinInterval = 2 * time.Second

func (c *clientApp) installTray() {
	c.trayMenu = fyne.NewMenu(version.AppName)
	c.trayMenu.Items = c.buildTrayItems()
	c.traySig = c.traySignature()
	c.trayBuilt = time.Now()
	if trayApp, ok := c.fyneApp.(interface{ SetSystemTrayMenu(*fyne.Menu) }); ok {
		trayApp.SetSystemTrayMenu(c.trayMenu)
	}
}

func disabledItem(label string) *fyne.MenuItem {
	item := fyne.NewMenuItem(label, nil)
	item.Disabled = true
	return item
}

func (c *clientApp) isPausedNow() bool { return c.runner.IsPaused() || c.pausedForNow() }

// buildTrayItems lists the whole menu: state, daily actions, recent files,
// pause, bandwidth limit, conflicts and the app entries.
func (c *clientApp) buildTrayItems() []*fyne.MenuItem {
	items := []*fyne.MenuItem{
		disabledItem(c.trayStatusText()),
		fyne.NewMenuItemSeparator(),
		fyne.NewMenuItem(tr("ui.tray.show", nil), c.showWindow),
		fyne.NewMenuItem(tr("tray.open_folder", nil), func() {
			if root := c.cfg.LocalRoot(); root != "" {
				_ = osx.Open(root)
			}
		}),
		fyne.NewMenuItem(lt("Cerca un file…", "Search a file…"), func() { c.showWindow(); c.focusSearch() }),
		fyne.NewMenuItemSeparator(),
		disabledItem(tr("ui.tray.recent", nil)),
	}
	shown := 0
	for _, item := range c.recent {
		if shown >= trayRecentMax {
			break
		}
		name, _ := splitPath(item.Path)
		target := item.Path
		items = append(items, fyne.NewMenuItem("   "+recentArrow(item.Kind)+" "+name, func() {
			c.openContainingFolder(target)
		}))
		shown++
	}
	if shown == 0 {
		items = append(items, disabledItem("   "+tr("ui.home.recent_empty_short", nil)))
	}
	items = append(items, fyne.NewMenuItemSeparator(), fyne.NewMenuItem(tr("ui.tray.sync", nil), c.onSync))

	if c.isPausedNow() {
		items = append(items, fyne.NewMenuItem("▶ "+tr("status.resume_btn", nil), c.onPause))
	} else {
		items = append(items, disabledItem(tr("ui.tray.pause_menu", nil)+":"))
		for _, preset := range pausePresets() {
			if preset.seconds != 15*60 && preset.seconds != 60*60 && preset.seconds != 4*60*60 {
				continue
			}
			seconds, label := preset.seconds, preset.label
			items = append(items, fyne.NewMenuItem("   ⏸ "+label, func() { c.pauseFor(seconds, label) }))
		}
		items = append(items,
			fyne.NewMenuItem("   ⏸ "+tr("ui.tray.pause_tomorrow", nil), func() {
				now := time.Now()
				tomorrow := time.Date(now.Year(), now.Month(), now.Day()+1, 8, 0, 0, 0, now.Location())
				c.pauseFor(int64(tomorrow.Sub(now).Seconds()), tr("ui.tray.pause_tomorrow", nil))
			}),
			fyne.NewMenuItem("   ⏸ "+lt("Finché non riprendo", "Until I resume"), c.onPause),
		)
	}

	current, uniform := c.currentBandwidth()
	header := lt("Velocità massima", "Maximum speed") + ": " + c.bandwidthSummary()
	items = append(items, fyne.NewMenuItemSeparator(), disabledItem(header))
	for _, preset := range trayBandwidthPresets() {
		kbps := preset.kbps
		item := fyne.NewMenuItem("   "+preset.label, func() { c.setBandwidth(kbps) })
		item.Checked = uniform && current == kbps
		items = append(items, item)
	}

	conflicts := fyne.NewMenuItem(tr("ui.tray.no_conflicts", nil), nil)
	if count := len(c.confGroups); count > 0 {
		conflicts = fyne.NewMenuItem(tr("ui.tray.conflicts", map[string]any{"count": count}),
			func() { c.showWindow(); c.navigate(sectionFiles, 1) })
	} else {
		conflicts.Disabled = true
	}
	return append(items,
		fyne.NewMenuItemSeparator(),
		conflicts,
		fyne.NewMenuItem(tr("main_window.tab_settings", nil), func() { c.showWindow(); c.navigate(sectionSettings, 0) }),
		fyne.NewMenuItem(tr("ui.action.check_update", nil), c.onCheckUpdate),
		fyne.NewMenuItemSeparator(),
		fyne.NewMenuItem(tr("ui.tray.quit", nil), func() { c.cancel(); c.fyneApp.Quit() }),
	)
}

const trayRecentMax = 5

// traySignature summarises everything the menu shows; equal signatures mean
// the system tray does not need to be touched.
func (c *clientApp) traySignature() string {
	var b strings.Builder
	b.WriteString(c.trayStatusText())
	fmt.Fprintf(&b, "|%t|%d|%s", c.isPausedNow(), len(c.confGroups), c.bandwidthSummary())
	for index, item := range c.recent {
		if index >= trayRecentMax {
			break
		}
		b.WriteString("|" + item.Kind + item.Path)
	}
	return b.String()
}

func (c *clientApp) showWindow() {
	if c.window == nil {
		return
	}
	c.window.Show()
	c.window.RequestFocus()
	if !c.windowVisible.Swap(true) {
		go c.pollMetrics() // fresh numbers when the window reopens
	}
}

// pauseFor applies a timed pause (shared by the tray and the pause dialog).
func (c *clientApp) pauseFor(seconds int64, label string) {
	until := time.Now().Unix() + seconds
	_ = c.cfg.Set("paused", false, true)
	_ = c.cfg.Set("pause_until", float64(until), true)
	c.runner.SetPaused(false)
	fyne.Do(func() {
		if c.pauseBtnStatus != nil {
			c.pauseBtnStatus.SetText(tr("status.resume_btn", nil))
		}
		if c.pauseBtn != nil {
			c.pauseBtn.SetText(tr("status.resume_btn", nil))
		}
	})
	c.applyStatus(statusPaused, tr("ui.status.paused_until", map[string]any{
		"time": time.Unix(until, 0).Format("02/01 15:04"),
	}))
	c.appendAction(tr("status.pause_btn", nil) + " · " + label)
}

// refreshTray rebuilds the menu when its content changed. Safe to call often:
// unchanged content costs nothing and rapid changes are coalesced.
func (c *clientApp) refreshTray() {
	if c.trayMenu == nil {
		return
	}
	signature := c.traySignature()
	if signature == c.traySig {
		return
	}
	if wait := trayMinInterval - time.Since(c.trayBuilt); wait > 0 {
		if !c.trayPending {
			c.trayPending = true
			time.AfterFunc(wait, func() {
				fyne.Do(func() {
					c.trayPending = false
					c.refreshTray()
				})
			})
		}
		return
	}
	c.trayMenu.Items = c.buildTrayItems()
	c.traySig = signature
	c.trayBuilt = time.Now()
	c.trayMenu.Refresh()
}

func recentArrow(kind string) string {
	switch kind {
	case "up":
		return "↑"
	case "down":
		return "↓"
	case "del":
		return "✕"
	case "conflict":
		return "⚠"
	}
	return "·"
}

func (c *clientApp) trayStatusText() string {
	if progress, active := c.transferActive(); active {
		arrow := "↓"
		if progress.Upload {
			arrow = "↑"
		}
		return fmt.Sprintf("%s %d/%d · %d%%", arrow, minInt(progress.Done+1, progress.Total), progress.Total, progress.Percent)
	}
	switch c.level {
	case statusOK:
		return "✓ " + plainTitle(tr("overview.banner_ok", nil))
	case statusProgress:
		return "⟳ " + plainTitle(tr("overview.banner_progress", nil))
	case statusPaused:
		return "⏸ " + plainTitle(tr("ui.status.paused", nil))
	case statusAttention:
		return "⚠ " + plainTitle(tr("overview.banner_warning", nil))
	case statusBlocked:
		return "⛔ " + plainTitle(tr("ui.status.blocked", nil))
	case statusOffline:
		return "⚡ " + plainTitle(tr("overview.banner_offline", nil))
	}
	return tr("status.starting", nil)
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// notify sends a desktop notification at most once per key and interval, so
// a recurring error never floods the desktop.
func (c *clientApp) notify(key, title, body string, interval time.Duration) {
	c.notifyMu.Lock()
	if c.lastNotify == nil {
		c.lastNotify = map[string]time.Time{}
	}
	if last, ok := c.lastNotify[key]; ok && time.Since(last) < interval {
		c.notifyMu.Unlock()
		return
	}
	c.lastNotify[key] = time.Now()
	c.notifyMu.Unlock()
	fyne.Do(func() {
		if app := fyne.CurrentApp(); app != nil {
			app.SendNotification(&fyne.Notification{Title: title, Content: body})
		}
	})
}
