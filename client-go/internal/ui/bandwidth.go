package ui

import (
	"fmt"
	"strconv"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"
)

// Bandwidth limit. The engine reads three keys on every transfer
// (bandwidth_kbps as the total/fallback, plus one per direction), so changing
// them takes effect immediately. The simple control sets all three to the same
// value; the three separate fields remain in the advanced settings.
const (
	keyBandwidthTotal    = "bandwidth_kbps"
	keyBandwidthUpload   = "bandwidth_upload_kbps"
	keyBandwidthDownload = "bandwidth_download_kbps"
)

type bandwidthPreset struct {
	label string
	kbps  int
}

func bandwidthPresets() []bandwidthPreset {
	return []bandwidthPreset{
		{lt("Illimitata", "Unlimited"), 0},
		{"256 KB/s", 256},
		{"512 KB/s", 512},
		{"1 MB/s", 1024},
		{"2 MB/s", 2048},
		{"5 MB/s", 5120},
		{"10 MB/s", 10240},
	}
}

// trayBandwidthPresets is the short list for the tray menu.
func trayBandwidthPresets() []bandwidthPreset {
	return []bandwidthPreset{
		{lt("Illimitata", "Unlimited"), 0},
		{"512 KB/s", 512},
		{"1 MB/s", 1024},
		{"5 MB/s", 5120},
	}
}

func formatKbps(kbps int) string {
	if kbps <= 0 {
		return lt("illimitata", "unlimited")
	}
	if kbps >= 1024 && kbps%1024 == 0 {
		return fmt.Sprintf("%d MB/s", kbps/1024)
	}
	return fmt.Sprintf("%d KB/s", kbps)
}

// currentBandwidth returns the effective limit. uniform is false when upload
// and download differ (set by hand in the advanced settings).
func (c *clientApp) currentBandwidth() (kbps int, uniform bool) {
	total := c.cfg.Int(keyBandwidthTotal)
	up, down := c.cfg.Int(keyBandwidthUpload), c.cfg.Int(keyBandwidthDownload)
	if up <= 0 {
		up = total
	}
	if down <= 0 {
		down = total
	}
	if up == down {
		return up, true
	}
	return 0, false
}

func (c *clientApp) bandwidthSummary() string {
	kbps, uniform := c.currentBandwidth()
	if !uniform {
		up, down := c.cfg.Int(keyBandwidthUpload), c.cfg.Int(keyBandwidthDownload)
		return fmt.Sprintf("↑ %s · ↓ %s", formatKbps(up), formatKbps(down))
	}
	return formatKbps(kbps)
}

// setBandwidth applies the same limit to both directions and saves once.
func (c *clientApp) setBandwidth(kbps int) {
	if kbps < 0 {
		kbps = 0
	}
	_ = c.cfg.Set(keyBandwidthTotal, kbps, false)
	_ = c.cfg.Set(keyBandwidthUpload, kbps, false)
	if err := c.cfg.Set(keyBandwidthDownload, kbps, true); err != nil {
		c.appendLog(lt("Limite di banda non salvato: ", "Bandwidth limit not saved: ") + err.Error())
	}
	c.appendAction(lt("Velocità massima: ", "Maximum speed: ") + formatKbps(kbps))
	fyne.Do(func() {
		c.syncBandwidthSelect()
		c.refreshTray()
	})
}

// bandwidthRow is the Home control: current limit and a one-click change.
func (c *clientApp) bandwidthRow() fyne.CanvasObject {
	presets := bandwidthPresets()
	options := make([]string, 0, len(presets)+1)
	for _, preset := range presets {
		options = append(options, preset.label)
	}
	options = append(options, lt("Altro valore…", "Other value…"))
	c.bandwidthSelect = widget.NewSelect(options, func(selected string) {
		if c.bandwidthUpdating {
			return
		}
		for _, preset := range presets {
			if preset.label == selected {
				c.setBandwidth(preset.kbps)
				return
			}
		}
		c.askBandwidth()
	})
	c.syncBandwidthSelect()
	label := widget.NewLabel(lt("Velocità massima", "Maximum speed"))
	hint := widget.NewLabel(lt("vale per caricamenti e scaricamenti", "applies to uploads and downloads"))
	hint.Importance = widget.LowImportance
	return container.NewHBox(label, minWidth(c.bandwidthSelect, 180), hint)
}

// syncBandwidthSelect shows the current limit without triggering the handler.
func (c *clientApp) syncBandwidthSelect() {
	if c.bandwidthSelect == nil {
		return
	}
	c.bandwidthUpdating = true
	defer func() { c.bandwidthUpdating = false }()
	kbps, uniform := c.currentBandwidth()
	if uniform {
		for _, preset := range bandwidthPresets() {
			if preset.kbps == kbps {
				c.bandwidthSelect.SetSelected(preset.label)
				return
			}
		}
	}
	// A value outside the presets: show it as the selection text.
	c.bandwidthSelect.ClearSelected()
	c.bandwidthSelect.PlaceHolder = c.bandwidthSummary()
	c.bandwidthSelect.Refresh()
}

func (c *clientApp) askBandwidth() {
	entry := widget.NewEntry()
	entry.SetPlaceHolder("2048")
	current, _ := c.currentBandwidth()
	if current > 0 {
		entry.SetText(strconv.Itoa(current))
	}
	form := dialog.NewForm(lt("Velocità massima", "Maximum speed"), lt("Imposta", "Set"), lt("Annulla", "Cancel"),
		[]*widget.FormItem{
			widget.NewFormItem("KB/s", entry),
			widget.NewFormItem("", widget.NewLabel(lt("1024 KB/s = 1 MB/s. Vuoto o 0 = illimitata.", "1024 KB/s = 1 MB/s. Empty or 0 = unlimited."))),
		},
		func(ok bool) {
			if !ok {
				c.syncBandwidthSelect()
				return
			}
			text := strings.TrimSpace(entry.Text)
			if text == "" {
				c.setBandwidth(0)
				return
			}
			value, err := strconv.Atoi(text)
			if err != nil || value < 0 {
				dialog.ShowInformation(tr("ui.settings.invalid_number", nil), lt("Inserisci un numero intero di KB/s.", "Enter a whole number of KB/s."), c.window)
				c.syncBandwidthSelect()
				return
			}
			c.setBandwidth(value)
		}, c.window)
	form.Show()
}
