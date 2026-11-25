package ui

import (
	"fmt"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"nasbox-go/internal/schedule"
)

var weekdayKeys = []string{
	"dialogs.schedule.day_mon", "dialogs.schedule.day_tue", "dialogs.schedule.day_wed",
	"dialogs.schedule.day_thu", "dialogs.schedule.day_fri", "dialogs.schedule.day_sat",
	"dialogs.schedule.day_sun",
}

func scheduleSummary(window schedule.Window) string {
	names := make([]string, 0, len(window.Days))
	for _, day := range window.Days {
		if day >= 0 && day < len(weekdayKeys) {
			names = append(names, tr(weekdayKeys[day], nil))
		}
	}
	return fmt.Sprintf("%s · %s → %s", strings.Join(names, ", "), window.Start, window.End)
}

// scheduleSetting edits the recurring pause windows. Multiple windows are
// supported exactly like the Python client: add, edit and remove persist
// immediately and the runner evaluates the same days/overnight semantics.
func (c *clientApp) scheduleSetting() fyne.CanvasObject {
	windows := schedule.FromConfig(c.cfg.Get("pause_schedule"))
	selected := -1
	summary := widget.NewLabel(tr("ui.settings.schedule_none", nil))
	summary.Wrapping = fyne.TextWrapWord
	list := widget.NewList(
		func() int { return len(windows) },
		func() fyne.CanvasObject {
			label := widget.NewLabel("")
			label.Wrapping = fyne.TextTruncate
			return label
		},
		func(id widget.ListItemID, object fyne.CanvasObject) {
			if int(id) < len(windows) {
				object.(*widget.Label).SetText(scheduleSummary(windows[id]))
			}
		},
	)
	refresh := func() {
		if len(windows) == 0 {
			summary.SetText(tr("ui.settings.schedule_none", nil))
		} else {
			lines := make([]string, 0, len(windows))
			for _, window := range windows {
				lines = append(lines, "• "+scheduleSummary(window))
			}
			summary.SetText(strings.Join(lines, "\n"))
		}
		list.Refresh()
	}
	save := func() {
		c.persistSetting("pause_schedule", schedule.ToConfig(windows))
	}
	list.OnSelected = func(id widget.ListItemID) {
		selected = int(id)
	}
	add := newHintButton(tr("settings.schedule_add_btn", nil), theme.ContentAddIcon(),
		tr("settings.schedule_add_tooltip", nil), widget.MediumImportance, func() {
			c.openScheduleEditor(nil, func(window schedule.Window) {
				windows = append(windows, window)
				save()
				refresh()
			})
		}, c.setHint)
	edit := newHintButton(tr("settings.schedule_edit_btn", nil), theme.DocumentCreateIcon(),
		tr("settings.schedule_edit_tooltip", nil), widget.LowImportance, func() {
			if selected < 0 || selected >= len(windows) {
				c.appendLog(tr("ui.settings.schedule_select", nil))
				return
			}
			index := selected
			c.openScheduleEditor(&windows[index], func(window schedule.Window) {
				windows[index] = window
				save()
				refresh()
			})
		}, c.setHint)
	remove := newHintButton(tr("settings.schedule_remove_btn", nil), theme.DeleteIcon(),
		tr("settings.schedule_remove_tooltip", nil), widget.DangerImportance, func() {
			if selected < 0 || selected >= len(windows) {
				c.appendLog(tr("ui.settings.schedule_select", nil))
				return
			}
			index := selected
			windows = append(windows[:index], windows[index+1:]...)
			selected = -1
			save()
			refresh()
		}, c.setHint)
	refresh()

	return c.settingCell(tr("settings.schedule_title", nil), container.NewVBox(
		c.note(tr("settings.schedule_note", nil)),
		summary,
		container.NewGridWrap(fyne.NewSize(1, 110), list),
		container.NewHBox(add, edit, remove),
	), "pause_schedule")
}

// openScheduleEditor edits one window. A nil initial value creates a new one.
func (c *clientApp) openScheduleEditor(initial *schedule.Window, onSave func(schedule.Window)) {
	days := map[int]bool{1: true}
	start, end := "09:00", "18:00"
	if initial != nil {
		days = map[int]bool{}
		for _, day := range initial.Days {
			days[day] = true
		}
		start, end = initial.Start, initial.End
	}
	checks := make([]*widget.Check, 0, 7)
	for index, key := range weekdayKeys {
		day := index
		check := widget.NewCheck(tr(key, nil), func(value bool) { days[day] = value })
		check.SetChecked(days[day])
		checks = append(checks, check)
	}
	startSelect := widget.NewSelect(scheduleTimes(start), nil)
	startSelect.SetSelected(start)
	endSelect := widget.NewSelect(scheduleTimes(end), nil)
	endSelect.SetSelected(end)

	content := container.NewVBox(
		c.note(tr("dialogs.schedule.intro", nil)),
		widget.NewLabel(tr("dialogs.schedule.days_label", nil)),
		container.NewGridWithColumns(4, checksToObjects(checks)...),
		container.NewBorder(nil, nil, widget.NewLabel(tr("dialogs.schedule.start_label", nil)), nil, startSelect),
		container.NewBorder(nil, nil, widget.NewLabel(tr("dialogs.schedule.end_label", nil)), nil, endSelect),
		c.note(tr("dialogs.schedule.overnight_hint", nil)),
	)
	editor := dialog.NewCustom(tr("dialogs.schedule.title", nil), tr("dupclean.cancel", nil), content, c.window)
	saveButton := newHintButton(tr("ui.settings.save_schedule", nil), theme.ConfirmIcon(),
		tr("ui.settings.save_schedule_hint", nil), widget.HighImportance, func() {
			selectedDays := []int{}
			for day := 0; day < 7; day++ {
				if days[day] {
					selectedDays = append(selectedDays, day)
				}
			}
			if len(selectedDays) == 0 {
				dialog.ShowInformation(tr("dialogs.schedule.no_days_title", nil),
					tr("dialogs.schedule.no_days_body", nil), c.window)
				return
			}
			if startSelect.Selected == endSelect.Selected {
				dialog.ShowInformation(tr("dialogs.schedule.same_time_title", nil),
					tr("dialogs.schedule.same_time_body", nil), c.window)
				return
			}
			if _, err := time.Parse("15:04", startSelect.Selected); err != nil {
				c.appendLog(tr("ui.settings.invalid_time", nil))
				return
			}
			if _, err := time.Parse("15:04", endSelect.Selected); err != nil {
				c.appendLog(tr("ui.settings.invalid_time", nil))
				return
			}
			window, ok := schedule.Normalize(map[string]any{
				"days": intListToAny(selectedDays), "start": startSelect.Selected, "end": endSelect.Selected,
			})
			if !ok {
				c.appendLog(tr("ui.settings.invalid_time", nil))
				return
			}
			editor.Hide()
			onSave(window)
			c.settingSaved(tr("settings.schedule_title", nil), scheduleSummary(window))
		}, c.setHint)
	editor.SetOnClosed(nil)
	// NewCustom has no footer; add the save action to the content so the
	// dialog keeps Fyne's standard dismiss button too.
	content.Add(saveButton)
	editor.Resize(fyne.NewSize(560, 420))
	editor.Show()
}

func intListToAny(values []int) []any {
	out := make([]any, len(values))
	for index, value := range values {
		out[index] = value
	}
	return out
}

func checksToObjects(checks []*widget.Check) []fyne.CanvasObject {
	out := make([]fyne.CanvasObject, len(checks))
	for index, check := range checks {
		out[index] = check
	}
	return out
}
