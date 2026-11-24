package ui

import (
	"fmt"
	"sort"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"nasbox-go/internal/classify"
)

// showPreviewDialog renders the read-only change preview computed on the PC.
// It is the structured counterpart of the Python change-preview dialog: the
// same data becomes a per-folder table, the anomalies are highlighted and the
// sample renames/deletions explain what a deletion guard is reacting to.
func (c *clientApp) showPreviewDialog(report classify.Report) {
	summary := widget.NewLabelWithStyle(report.Summary(), fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	summary.Wrapping = fyne.TextWrapWord

	sections := []fyne.CanvasObject{
		c.sectionTitle(tr("ui.preview.title", nil), tr("ui.preview.note", nil)),
		summary,
	}

	if len(report.Anomalies) > 0 {
		lines := make([]string, 0, len(report.Anomalies))
		for _, anomaly := range report.Anomalies {
			lines = append(lines, tr("ui.preview.anomaly", map[string]any{
				"directory": anomaly.Dir, "missing": anomaly.Missing, "remote": anomaly.Remote,
				"percent": fmt.Sprintf("%.0f", anomaly.Ratio()*100),
			}))
		}
		sections = append(sections,
			widget.NewLabelWithStyle(tr("ui.preview.anomalies", nil), fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
			c.note(strings.Join(lines, "\n")),
		)
	}

	// Per-folder table: only folders that lost at least one file.
	type folderRow struct {
		dir     string
		missing int
		remote  int
	}
	folders := make([]folderRow, 0, len(report.MissingByDir))
	for dir, missing := range report.MissingByDir {
		folders = append(folders, folderRow{dir: dir, missing: missing, remote: report.RemoteByDir[dir]})
	}
	sort.Slice(folders, func(i, j int) bool {
		if folders[i].missing != folders[j].missing {
			return folders[i].missing > folders[j].missing
		}
		return folders[i].dir < folders[j].dir
	})
	if len(folders) > 0 {
		shown := folders
		if len(shown) > 15 {
			shown = shown[:15]
		}
		lines := make([]string, 0, len(shown))
		for _, folder := range shown {
			lines = append(lines, tr("ui.preview.folder_row", map[string]any{
				"directory": folder.dir, "missing": folder.missing, "remote": folder.remote,
			}))
		}
		sections = append(sections,
			widget.NewLabelWithStyle(tr("ui.preview.folders", nil), fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
			c.note(strings.Join(lines, "\n")),
		)
	}

	if len(report.Renames) > 0 {
		shown := report.Renames
		if len(shown) > 10 {
			shown = shown[:10]
		}
		lines := make([]string, 0, len(shown))
		for _, rename := range shown {
			lines = append(lines, "• "+rename.Missing+" → "+rename.Local)
		}
		sections = append(sections,
			widget.NewLabelWithStyle(tr("ui.preview.renames", nil), fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
			c.note(strings.Join(lines, "\n")),
		)
	}

	if len(report.Unexplained) > 0 {
		shown := report.Unexplained
		if len(shown) > 20 {
			shown = shown[:20]
		}
		lines := make([]string, 0, len(shown))
		for _, path := range shown {
			lines = append(lines, "• "+path)
		}
		if len(report.Unexplained) > len(shown) {
			lines = append(lines, tr("ui.preview.more", map[string]any{
				"count": len(report.Unexplained) - len(shown),
			}))
		}
		sections = append(sections,
			widget.NewLabelWithStyle(tr("ui.preview.deletions", nil), fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
			c.note(strings.Join(lines, "\n")),
		)
	}

	actions := container.NewHBox(
		newHintButton(tr("ui.action.confirm_delete", nil), theme.ConfirmIcon(),
			tr("status.confirm_deletes_tooltip", nil), widget.DangerImportance, func() {
				c.onConfirmDelete()
			}, c.setHint),
		newHintButton(tr("ui.action.sync_short", nil), theme.MediaReplayIcon(),
			tr("status.sync_now_tooltip", nil), widget.MediumImportance, func() {
				c.onSync()
			}, c.setHint),
		newHintButton(tr("main_window.tab_log", nil), theme.ListIcon(),
			tr("ui.preview.open_log", nil), widget.LowImportance, func() {
				c.navigate(sectionActivity, 2)
			}, c.setHint),
	)

	content := container.NewBorder(
		nil, actions, nil, nil,
		container.NewVScroll(container.NewVBox(sections...)),
	)
	preview := dialog.NewCustom(tr("ui.preview.window_title", nil), tr("ui.action.close", nil),
		content, c.window)
	preview.Resize(fyne.NewSize(760, 560))
	preview.Show()
}
