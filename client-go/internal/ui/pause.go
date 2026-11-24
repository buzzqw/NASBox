package ui

import (
	"strconv"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

type pausePreset struct {
	label   string
	seconds int64
}

func pausePresets() []pausePreset {
	return []pausePreset{
		{tr("dialogs.pause.preset_15m", nil), 15 * 60},
		{tr("dialogs.pause.preset_30m", nil), 30 * 60},
		{tr("dialogs.pause.preset_1h", nil), 60 * 60},
		{tr("dialogs.pause.preset_4h", nil), 4 * 60 * 60},
		{tr("dialogs.pause.preset_8h", nil), 8 * 60 * 60},
		{tr("dialogs.pause.preset_custom", nil), 0},
	}
}

// showPauseDialog offers the same preset pauses as the Python client. "custom"
// reads a minute count; the manual indefinite pause stays on the main button.
func (c *clientApp) showPauseDialog() {
	presets := pausePresets()
	labels := make([]string, 0, len(presets))
	for _, preset := range presets {
		labels = append(labels, preset.label)
	}
	selectWidget := widget.NewSelect(labels, nil)
	selectWidget.SetSelected(presets[0].label)
	custom := widget.NewEntry()
	custom.SetText("60")
	custom.Disable()
	selectWidget.OnChanged = func(value string) {
		if value == tr("dialogs.pause.preset_custom", nil) {
			custom.Enable()
		} else {
			custom.Disable()
		}
	}
	content := container.NewVBox(
		c.note(tr("dialogs.pause.duration_tooltip", nil)),
		container.NewBorder(nil, nil, minWidth(widget.NewLabel(tr("dialogs.pause.duration_label", nil)), 150), nil, selectWidget),
		container.NewBorder(nil, nil, minWidth(widget.NewLabel(tr("dialogs.pause.custom_minutes_label", nil)), 150), nil, custom),
	)
	pause := newHintButton(tr("tray.pause", nil), theme.MediaPauseIcon(),
		tr("status.pause_tooltip", nil), widget.HighImportance, func() {
			seconds := int64(0)
			for _, preset := range presets {
				if preset.label == selectWidget.Selected {
					seconds = preset.seconds
					break
				}
			}
			if seconds == 0 {
				minutes, err := strconv.Atoi(strings.TrimSpace(custom.Text))
				if err != nil || minutes <= 0 {
					dialog.ShowInformation(tr("ui.settings.invalid_number", nil),
						tr("dialogs.pause.custom_minutes_tooltip", nil), c.window)
					return
				}
				seconds = int64(minutes) * 60
			}
			until := time.Now().Unix() + seconds
			_ = c.cfg.Set("paused", false, true)
			_ = c.cfg.Set("pause_until", float64(until), true)
			c.runner.SetPaused(false)
			c.pauseBtnStatus.SetText(tr("status.resume_btn", nil))
			c.applyStatus(statusPaused, tr("status.sync_state_paused", nil))
			c.appendAction(tr("status.pause_btn", nil) + " · " + selectWidget.Selected)
		}, c.setHint)
	content.Add(pause)
	pauseDialog := dialog.NewCustom(tr("dialogs.pause.title", nil), tr("dupclean.cancel", nil), content, c.window)
	pauseDialog.Resize(fyne.NewSize(520, 300))
	pauseDialog.Show()
}
