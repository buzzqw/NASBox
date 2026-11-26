package ui

import (
	_ "embed"
	"time"

	"fyne.io/fyne/v2"
)

// Icon variants match the legacy PyQt6 client: blue rounded box with two
// concentric outlines and a semantic badge. Generated as PNG assets, embedded
// in the binary, so there are no runtime dependencies.
var (
	//go:embed icons/synced.png
	iconSyncedBytes []byte
	//go:embed icons/syncing.png
	iconSyncingBytes []byte
	//go:embed icons/syncing_alt.png
	iconSyncingAltBytes []byte
	//go:embed icons/paused.png
	iconPausedBytes []byte
	//go:embed icons/error.png
	iconErrorBytes []byte
	//go:embed icons/unconfigured.png
	iconUnconfiguredBytes []byte
)

var trayIcons = map[string]fyne.Resource{
	"synced":       fyne.NewStaticResource("synced.png", iconSyncedBytes),
	"syncing":      fyne.NewStaticResource("syncing.png", iconSyncingBytes),
	"syncing_alt":  fyne.NewStaticResource("syncing_alt.png", iconSyncingAltBytes),
	"paused":       fyne.NewStaticResource("paused.png", iconPausedBytes),
	"error":        fyne.NewStaticResource("error.png", iconErrorBytes),
	"unconfigured": fyne.NewStaticResource("unconfigured.png", iconUnconfiguredBytes),
}

func trayIcon(state string) fyne.Resource {
	if resource, ok := trayIcons[state]; ok {
		return resource
	}
	return trayIcons["unconfigured"]
}

// setTrayIcon updates both the window and the system tray icon.
func (c *clientApp) setTrayIcon(state string) {
	if state == c.trayIconState {
		return // same icon: re-setting it makes some trays flicker
	}
	c.trayIconState = state
	resource := trayIcon(state)
	c.fyneApp.SetIcon(resource)
	if tray, ok := c.fyneApp.(interface{ SetSystemTrayIcon(fyne.Resource) }); ok {
		tray.SetSystemTrayIcon(resource)
	}
}

// animateTray alternates two frames while a sync is active, matching the
// legacy client's animate_sync_icon setting.
func (c *clientApp) animateTray(active func() bool) {
	if !c.cfg.Bool("animate_sync_icon") {
		return
	}
	go func() {
		toggle := false
		for {
			if !active() {
				toggle = false
				time.Sleep(700 * time.Millisecond)
				continue
			}
			state := "syncing"
			if toggle {
				state = "syncing_alt"
			}
			toggle = !toggle
			c.setTrayIcon(state)
			time.Sleep(700 * time.Millisecond)
		}
	}()
}
