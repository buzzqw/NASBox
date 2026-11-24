package ui

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"

	"nasbox-go/internal/remote"
	"nasbox-go/internal/update"
	"nasbox-go/internal/version"
)

// autoUpdateLoop keeps the client current like the Python client: a check
// shortly after start and every 6 hours. A new version is installed only
// while no transfer is running; the updater's own state machine rolls back a
// binary that fails to start twice. Server updates are only announced: they
// affect every PC and need an explicit confirmation.
func (c *clientApp) autoUpdateLoop(ctx context.Context) {
	if strings.EqualFold(c.cfg.String("go_auto_update"), "false") {
		return
	}
	timer := time.NewTimer(2 * time.Minute)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		c.checkClientUpdate(false)
		c.checkServerUpdate(false)
		timer.Reset(6 * time.Hour)
	}
}

// checkClientUpdate looks for a newer client. Interactive checks show the
// release notes and let the user decide; automatic ones install by themselves.
func (c *clientApp) checkClientUpdate(interactive bool) {
	if interactive {
		c.appendLog(tr("status.update_checking", nil))
	}
	manifest, err := update.Newest(c.cfg, c.client)
	if err != nil {
		if interactive {
			c.appendAction(tr("status.update_check_failed", map[string]any{"detail": err.Error()}))
		}
		return
	}
	if !update.Available(manifest) {
		if interactive {
			c.appendAction(tr("main_window.update_none", nil))
		}
		return
	}
	if interactive {
		fyne.Do(func() { c.showUpdateDialog(manifest) })
		return
	}
	if _, active := c.transferActive(); active || c.level == statusProgress {
		return // try again at the next check, never in the middle of a transfer
	}
	c.installClientUpdate(manifest, false)
}

func (c *clientApp) showUpdateDialog(manifest *update.Manifest) {
	notes := manifest.Changelog
	if strings.TrimSpace(notes) == "" {
		notes = manifest.Notes
	}
	text := widget.NewRichTextFromMarkdown(notes)
	text.Wrapping = fyne.TextWrapWord
	scroll := container.NewVScroll(text)
	scroll.SetMinSize(fyne.NewSize(560, 320))
	header := widget.NewLabel(tr("ui.update.header", map[string]any{
		"current": version.AppVersion, "version": manifest.Version,
	}))
	header.Wrapping = fyne.TextWrapWord
	content := container.NewBorder(header, nil, nil, nil, scroll)
	confirm := dialog.NewCustomConfirm(tr("ui.update.title", map[string]any{"version": manifest.Version}),
		tr("ui.update.install_restart", nil), tr("ui.update.later", nil), content, func(ok bool) {
			if ok {
				go c.installClientUpdate(manifest, true)
			}
		}, c.window)
	confirm.Resize(fyne.NewSize(640, 480))
	confirm.Show()
}

// installClientUpdate applies the update and restarts into the new binary.
func (c *clientApp) installClientUpdate(manifest *update.Manifest, interactive bool) {
	binary, err := os.Executable()
	if err != nil {
		c.appendAction(tr("ui.update.failed", map[string]any{"detail": err.Error()}))
		return
	}
	applied, versionName, err := update.Apply(c.cfg, c.client, binary)
	if err != nil {
		if interactive || err != update.ErrUnmanagedBinary {
			c.appendAction(tr("ui.update.failed", map[string]any{"detail": err.Error()}))
		}
		return
	}
	if !applied {
		return
	}
	c.appendAction(tr("ui.update.installed_restart", map[string]any{"version": versionName}))
	c.notify("update:"+versionName, tr("ui.update.done_title", nil),
		tr("ui.update.installed_restart", map[string]any{"version": versionName}), 24*time.Hour)
	c.restartSelf(binary)
}

// restartSelf starts the new binary (after this process released its
// instance lock) and quits. The relaunch survives the parent's exit.
func (c *clientApp) restartSelf(binary string) {
	command := exec.Command("sh", "-c", `sleep 2; exec "$0" gui`, binary)
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	command.Stdin, command.Stdout, command.Stderr = nil, nil, nil
	if err := command.Start(); err != nil {
		c.appendAction(tr("ui.update.failed", map[string]any{"detail": err.Error()}))
		return
	}
	fyne.Do(func() {
		if c.cancel != nil {
			c.cancel()
		}
		c.fyneApp.Quit()
	})
}

// checkServerUpdate announces (automatic) or offers to install (interactive)
// a newer server script published on the NAS.
func (c *clientApp) checkServerUpdate(interactive bool) {
	values, err := c.client.PrintConfig()
	if err != nil {
		if interactive {
			c.appendAction(tr("status.diagnostics_offline", nil))
		}
		return
	}
	current := values["VERSION"]
	found, err := c.client.FindServerUpdate(current)
	if err != nil || found == nil {
		if interactive {
			c.appendAction(tr("ui.server_update.none", map[string]any{"version": current}))
		}
		return
	}
	if !interactive {
		c.notify("server-update:"+found.Version, tr("ui.server_update.title", nil),
			tr("ui.server_update.available", map[string]any{"current": current, "version": found.Version}), 24*time.Hour)
		c.appendLog(tr("ui.server_update.available", map[string]any{"current": current, "version": found.Version}))
		return
	}
	fyne.Do(func() {
		dialog.ShowConfirm(tr("ui.server_update.title", nil),
			tr("ui.server_update.confirm", map[string]any{"current": current, "version": found.Version}),
			func(ok bool) {
				if ok {
					go c.installServerUpdate(*found)
				}
			}, c.window)
	})
}

func (c *clientApp) installServerUpdate(found remote.ServerUpdate) {
	c.appendAction(tr("ui.server_update.installing", map[string]any{"version": found.Version}))
	if err := c.client.InstallServerUpdate(found); err != nil {
		c.appendAction(tr("ui.update.failed", map[string]any{"detail": err.Error()}))
		return
	}
	c.appendAction(tr("ui.server_update.done", map[string]any{"version": found.Version}))
	c.cfg.Set("min_server_version", c.cfg.String("min_server_version"), false)
	go c.refreshMonitor()
}

func (c *clientApp) onCheckServerUpdate() { go c.checkServerUpdate(true) }
