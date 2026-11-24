package ui

import (
	"fmt"
	"os/exec"
	"time"

	"fyne.io/fyne/v2"

	"nasbox-go/internal/version"
)

// installTray builds the system tray menu. Its first line always states what
// NASBox is doing; the rest are the actions people use daily.
func (c *clientApp) installTray() {
	c.trayStatus = fyne.NewMenuItem(tr("status.starting", nil), nil)
	c.trayStatus.Disabled = true
	c.trayRecent = fyne.NewMenuItem(tr("ui.tray.recent", nil), nil)
	c.trayConflicts = fyne.NewMenuItem("", func() { c.showWindow(); c.navigate(sectionFiles, 1) })
	c.trayPause = fyne.NewMenuItem(tr("ui.tray.pause_menu", nil), nil)
	c.trayPause.ChildMenu = c.pauseMenu()
	c.trayMenu = fyne.NewMenu(version.AppName,
		c.trayStatus,
		fyne.NewMenuItemSeparator(),
		fyne.NewMenuItem(tr("ui.tray.show", nil), c.showWindow),
		fyne.NewMenuItem(tr("tray.open_folder", nil), func() {
			if root := c.cfg.LocalRoot(); root != "" {
				_ = exec.Command("xdg-open", root).Start()
			}
		}),
		c.trayRecent,
		fyne.NewMenuItemSeparator(),
		fyne.NewMenuItem(tr("ui.tray.sync", nil), c.onSync),
		c.trayPause,
		c.trayConflicts,
		fyne.NewMenuItemSeparator(),
		fyne.NewMenuItem(tr("ui.nav.activity", nil), func() { c.showWindow(); c.navigate(sectionActivity, 1) }),
		fyne.NewMenuItem(tr("main_window.tab_settings", nil), func() { c.showWindow(); c.navigate(sectionSettings, 0) }),
		fyne.NewMenuItem(tr("ui.action.check_update", nil), c.onCheckUpdate),
		fyne.NewMenuItemSeparator(),
		fyne.NewMenuItem(tr("ui.tray.quit", nil), func() { c.cancel(); c.fyneApp.Quit() }),
	)
	c.refreshTray()
	if trayApp, ok := c.fyneApp.(interface{ SetSystemTrayMenu(*fyne.Menu) }); ok {
		trayApp.SetSystemTrayMenu(c.trayMenu)
	}
}

func (c *clientApp) showWindow() {
	if c.window == nil {
		return
	}
	c.window.Show()
	c.window.RequestFocus()
}

// pauseMenu offers bounded pauses (like the dialog) plus resume.
func (c *clientApp) pauseMenu() *fyne.Menu {
	items := []*fyne.MenuItem{}
	for _, preset := range pausePresets() {
		if preset.seconds == 0 {
			continue
		}
		seconds := preset.seconds
		label := preset.label
		items = append(items, fyne.NewMenuItem(label, func() { c.pauseFor(seconds, label) }))
	}
	items = append(items,
		fyne.NewMenuItem(tr("ui.tray.pause_tomorrow", nil), func() {
			now := time.Now()
			tomorrow := time.Date(now.Year(), now.Month(), now.Day()+1, 8, 0, 0, 0, now.Location())
			c.pauseFor(int64(tomorrow.Sub(now).Seconds()), tr("ui.tray.pause_tomorrow", nil))
		}),
		fyne.NewMenuItemSeparator(),
		fyne.NewMenuItem(tr("status.resume_btn", nil), func() {
			if c.runner.IsPaused() || c.pausedForNow() {
				c.onPause()
			}
		}),
	)
	return fyne.NewMenu(tr("ui.tray.pause_menu", nil), items...)
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

// refreshTray updates the dynamic parts of the menu. Safe to call often.
func (c *clientApp) refreshTray() {
	if c.trayMenu == nil {
		return
	}
	status := c.trayStatusText()
	c.trayStatus.Label = status
	recent := []*fyne.MenuItem{}
	for index, item := range c.recent {
		if index >= 8 {
			break
		}
		name, _ := splitPath(item.Path)
		target := item.Path
		recent = append(recent, fyne.NewMenuItem(recentArrow(item.Kind)+" "+name, func() {
			c.openContainingFolder(target)
		}))
	}
	if len(recent) == 0 {
		empty := fyne.NewMenuItem(tr("ui.home.recent_empty_short", nil), nil)
		empty.Disabled = true
		recent = append(recent, empty)
	}
	c.trayRecent.ChildMenu = fyne.NewMenu(tr("ui.tray.recent", nil), recent...)
	if count := len(c.confGroups); count > 0 {
		c.trayConflicts.Label = tr("ui.tray.conflicts", map[string]any{"count": count})
		c.trayConflicts.Disabled = false
	} else {
		c.trayConflicts.Label = tr("ui.tray.no_conflicts", nil)
		c.trayConflicts.Disabled = true
	}
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
		return "✓ " + tr("overview.banner_ok", nil)
	case statusProgress:
		return "⟳ " + tr("overview.banner_progress", nil)
	case statusPaused:
		return "⏸ " + tr("ui.status.paused", nil)
	case statusAttention:
		return "⚠ " + tr("overview.banner_warning", nil)
	case statusBlocked:
		return "⛔ " + tr("ui.status.blocked", nil)
	case statusOffline:
		return "⚡ " + tr("overview.banner_offline", nil)
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
