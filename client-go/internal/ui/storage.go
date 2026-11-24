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

	"nasbox-go/internal/storage"
)

// buildStoragePane is the NAS -> Storage tab: largest folders, duplicate
// groups and local trash usage, all computed on the PC from the local tree.
func (c *clientApp) buildStoragePane() fyne.CanvasObject {
	c.storageStatus = widget.NewLabel(tr("storage.status_idle", nil))
	c.storageStatus.Wrapping = fyne.TextWrapWord
	c.storageLast = widget.NewLabel(tr("storage.never_updated", nil))
	c.storageLast.Wrapping = fyne.TextWrapWord
	c.storageFolders = widget.NewLabel(tr("storage.empty_folders", nil))
	c.storageFolders.Wrapping = fyne.TextWrapWord
	c.storageDuplicates = widget.NewLabel(tr("storage.empty_duplicates", nil))
	c.storageDuplicates.Wrapping = fyne.TextWrapWord
	c.storageTrash = widget.NewLabel(tr("storage.empty_trash", nil))
	c.storageTrash.Wrapping = fyne.TextWrapWord
	c.storageProgress = widget.NewProgressBar()
	c.storageProgress.SetValue(0)
	c.storageAnalyzeBtn = newHintButton(tr("storage.analyze_btn", nil), theme.ViewRefreshIcon(),
		tr("storage.title", nil), widget.HighImportance, c.onAnalyzeStorage, c.setHint).Button
	c.storageCleanupBtn = newHintButton(tr("storage.cleanup_btn", nil), theme.DeleteIcon(),
		tr("storage.cleanup_tooltip", nil), widget.MediumImportance, c.onCleanupDuplicates, c.setHint).Button
	c.storageCleanupBtn.Disable()

	return container.NewVScroll(container.NewVBox(
		c.sectionTitle(tr("storage.title", nil), tr("storage.details_title", nil)),
		c.surface(container.NewVBox(
			c.storageStatus,
			c.storageLast,
			c.storageProgress,
			container.NewHBox(c.storageAnalyzeBtn, c.storageCleanupBtn),
		), ""),
		c.surface(container.NewVBox(
			widget.NewLabelWithStyle(tr("storage.folders_title", nil), fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
			c.storageFolders,
		), ""),
		c.surface(container.NewVBox(
			widget.NewLabelWithStyle(tr("storage.duplicates_title", nil), fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
			c.storageDuplicates,
		), ""),
		c.surface(container.NewVBox(
			widget.NewLabelWithStyle(tr("storage.trash_title", nil), fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
			c.storageTrash,
		), ""),
	))
}

func (c *clientApp) onAnalyzeStorage() {
	if c.cfg.LocalRoot() == "" {
		if c.storageStatus != nil {
			c.storageStatus.SetText(tr("storage.error", map[string]any{"detail": tr("storage.missing_root", nil)}))
		}
		return
	}
	if c.storageBusy {
		return
	}
	c.storageBusy = true
	if c.storageAnalyzeBtn != nil {
		c.storageAnalyzeBtn.Disable()
	}
	if c.storageCleanupBtn != nil {
		c.storageCleanupBtn.Disable()
	}
	if c.storageStatus != nil {
		c.storageStatus.SetText(tr("storage.analyzing", nil))
	}
	if c.storageProgress != nil {
		c.storageProgress.SetValue(0)
	}
	root := c.cfg.LocalRoot()
	retention := 0.0
	if value, ok := c.cfg.Get("retention_days_local").(float64); ok {
		retention = value
	}
	go func() {
		insights := storage.CollectRoot(root, retention, func(examined int) {
			fyne.Do(func() {
				if c.storageStatus != nil {
					c.storageStatus.SetText(tr("storage.progress", map[string]any{"count": examined}))
				}
			})
		})
		fyne.Do(func() { c.applyStorageInsights(insights) })
	}()
}

func (c *clientApp) applyStorageInsights(insights storage.Insights) {
	c.storageBusy = false
	c.storageInsights = insights
	if c.storageAnalyzeBtn != nil {
		c.storageAnalyzeBtn.Enable()
	}
	if c.storageProgress != nil {
		c.storageProgress.SetValue(1)
	}
	if c.storageStatus != nil {
		c.storageStatus.SetText(tr("storage.status_done", map[string]any{"files": insights.ScannedFiles}))
	}
	if c.storageLast != nil {
		c.storageLast.SetText(tr("storage.last_updated", map[string]any{
			"time": insights.CollectedAt.Format("15:04:05"),
		}))
	}
	if insights.ScanErrors > 0 && c.storageStatus != nil {
		c.storageStatus.SetText(c.storageStatus.Text + " " +
			tr("storage.scan_errors", map[string]any{"count": insights.ScanErrors}))
	}
	c.renderStorageFolders(insights)
	c.renderStorageDuplicates(insights)
	c.renderStorageTrash(insights)
	if c.storageCleanupBtn != nil {
		if len(insights.Duplicates) > 0 {
			c.storageCleanupBtn.Enable()
		} else {
			c.storageCleanupBtn.Disable()
		}
	}
}

func (c *clientApp) renderStorageFolders(insights storage.Insights) {
	if c.storageFolders == nil {
		return
	}
	if len(insights.TopFolders) == 0 {
		c.storageFolders.SetText(tr("storage.empty_folders", nil))
		return
	}
	lines := make([]string, 0, len(insights.TopFolders))
	for _, folder := range insights.TopFolders {
		lines = append(lines, tr("storage.folder_row", map[string]any{
			"path": folder.Path, "size": humanBytes(folder.SizeBytes), "count": folder.FileCount,
		}))
	}
	c.storageFolders.SetText(strings.Join(lines, "\n"))
}

func (c *clientApp) renderStorageDuplicates(insights storage.Insights) {
	if c.storageDuplicates == nil {
		return
	}
	if len(insights.Duplicates) == 0 {
		c.storageDuplicates.SetText(tr("storage.empty_duplicates", nil))
		return
	}
	lines := []string{
		tr("storage.duplicate_summary", map[string]any{
			"groups": len(insights.Duplicates), "wasted": humanBytes(insights.DuplicateWastedBytes),
			"candidates": insights.DuplicateCandidates,
		}),
	}
	if insights.CandidatesLimitReached {
		lines = append(lines, tr("storage.limited_notice", nil))
	}
	shown := insights.Duplicates
	if len(shown) > 10 {
		shown = shown[:10]
	}
	for _, group := range shown {
		lines = append(lines, tr("storage.duplicate_group", map[string]any{
			"size": humanBytes(group.SizeBytes), "count": len(group.Paths),
		}))
		paths := group.Paths
		if len(paths) > 8 {
			paths = paths[:8]
		}
		for _, path := range paths {
			lines = append(lines, tr("storage.duplicate_path", map[string]any{"path": path}))
		}
		if len(group.Paths) > len(paths) {
			lines = append(lines, tr("storage.duplicate_more", map[string]any{
				"count": len(group.Paths) - len(paths),
			}))
		}
	}
	if len(insights.Duplicates) > len(shown) {
		lines = append(lines, tr("storage.duplicate_more_groups", map[string]any{
			"count": len(insights.Duplicates) - len(shown),
		}))
	}
	c.storageDuplicates.SetText(strings.Join(lines, "\n"))
}

func (c *clientApp) renderStorageTrash(insights storage.Insights) {
	if c.storageTrash == nil {
		return
	}
	trash := insights.Trash
	if trash.VersionCount == 0 {
		c.storageTrash.SetText(tr("storage.empty_trash", nil))
		return
	}
	c.storageTrash.SetText(tr("storage.trash_summary", map[string]any{
		"count": trash.VersionCount, "size": humanBytes(trash.TotalBytes),
		"expired": trash.ExpiredCount, "expired_size": humanBytes(trash.ExpiredBytes),
		"days": fmt.Sprintf("%.0f", trash.RetentionDays),
	}))
}

// ---------- duplicate cleanup ----------

type duplicateRow struct {
	group int
	path  string
	keep  bool
	head  bool
}

func (c *clientApp) onCleanupDuplicates() {
	groups := c.storageInsights.Duplicates
	if len(groups) == 0 {
		c.appendAction(tr("dupclean.empty", nil))
		return
	}
	rows := make([]duplicateRow, 0)
	for groupIndex, group := range groups {
		if len(group.Paths) < 2 {
			continue
		}
		rows = append(rows, duplicateRow{group: groupIndex, head: true})
		rows = append(rows, duplicateRow{group: groupIndex, path: group.Paths[0], keep: true})
		for _, path := range group.Paths[1:] {
			rows = append(rows, duplicateRow{group: groupIndex, path: path})
		}
	}
	selected := map[string]bool{}
	intro := widget.NewLabel(tr("dupclean.intro", nil))
	intro.Wrapping = fyne.TextWrapWord
	reclaim := widget.NewLabel(tr("dupclean.reclaim", map[string]any{"size": humanBytes(0)}))
	updateReclaim := func() {
		total := int64(0)
		for _, path := range selectedPathList(selected) {
			for _, group := range groups {
				for _, candidate := range group.Paths[1:] {
					if candidate == path {
						total += group.SizeBytes
					}
				}
			}
		}
		reclaim.SetText(tr("dupclean.reclaim", map[string]any{"size": humanBytes(total)}))
	}

	list := widget.NewList(
		func() int { return len(rows) },
		func() fyne.CanvasObject {
			return container.NewHBox(widget.NewCheck("", nil), widget.NewLabel(""))
		},
		func(id widget.ListItemID, object fyne.CanvasObject) {
			row := object.(*fyne.Container)
			check, _ := row.Objects[0].(*widget.Check)
			label, _ := row.Objects[1].(*widget.Label)
			if check == nil || label == nil || id >= len(rows) {
				return
			}
			item := rows[id]
			label.Wrapping = fyne.TextTruncate
			check.OnChanged = nil
			switch {
			case item.head:
				group := groups[item.group]
				check.Hide()
				label.TextStyle = fyne.TextStyle{Bold: true}
				label.SetText(tr("dupclean.group", map[string]any{
					"index": item.group + 1, "size": humanBytes(group.SizeBytes), "count": len(group.Paths),
				}))
			case item.keep:
				check.Hide()
				label.TextStyle = fyne.TextStyle{}
				label.SetText(tr("dupclean.keep_path", map[string]any{"path": item.path}))
			default:
				path := item.path
				check.Show()
				label.TextStyle = fyne.TextStyle{}
				label.SetText(path)
				check.SetChecked(selected[path])
				check.OnChanged = func(value bool) {
					if value {
						selected[path] = true
					} else {
						delete(selected, path)
					}
					updateReclaim()
				}
			}
			label.Refresh()
		},
	)
	selectAll := widget.NewButton(tr("dupclean.select_all", nil), func() {
		for _, row := range rows {
			if !row.head && !row.keep {
				selected[row.path] = true
			}
		}
		updateReclaim()
		list.Refresh()
	})
	selectNone := widget.NewButton(tr("dupclean.select_none", nil), func() {
		selected = map[string]bool{}
		updateReclaim()
		list.Refresh()
	})
	trashButton := widget.NewButtonWithIcon(tr("dupclean.trash_button", nil), theme.DeleteIcon(), nil)
	trashButton.Importance = widget.DangerImportance

	content := container.NewBorder(
		container.NewVBox(intro, reclaim),
		container.NewVBox(container.NewHBox(selectAll, selectNone, trashButton)),
		nil, nil, container.NewGridWrap(fyne.NewSize(640, 340), list),
	)
	dialogWindow := dialog.NewCustom(tr("dupclean.title", nil), tr("dupclean.cancel", nil), content, c.window)
	trashButton.OnTapped = func() {
		paths := selectedPathList(selected)
		if len(paths) == 0 {
			dialog.ShowInformation(tr("dupclean.no_selection_title", nil),
				tr("dupclean.no_selection_body", nil), c.window)
			return
		}
		total := int64(0)
		for _, path := range paths {
			for _, group := range groups {
				for _, candidate := range group.Paths[1:] {
					if candidate == path {
						total += group.SizeBytes
					}
				}
			}
		}
		message := tr("dupclean.confirm_body", map[string]any{
			"count": len(paths), "size": humanBytes(total),
		})
		dialog.ShowConfirm(tr("dupclean.confirm_title", nil), message, func(ok bool) {
			if !ok {
				return
			}
			dialogWindow.Hide()
			go c.runDuplicateCleanup(groups, paths)
		}, c.window)
	}
	dialogWindow.Resize(fyne.NewSize(700, 560))
	dialogWindow.Show()
}

func selectedPathList(selected map[string]bool) []string {
	paths := make([]string, 0, len(selected))
	for path := range selected {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	return paths
}

func (c *clientApp) runDuplicateCleanup(groups []storage.DuplicateGroup, paths []string) {
	result := storage.RemoveDuplicates(c.cfg.LocalRoot(), groups, paths)
	if len(result.Errors) > 0 {
		c.appendLog(tr("dupclean.error_body", map[string]any{
			"error": strings.Join(result.Errors, "; "),
		}))
	}
	fyne.Do(func() {
		if len(result.Moved) > 0 {
			c.appendAction(tr("dupclean.result_body", map[string]any{
				"moved": len(result.Moved), "errors": len(result.Errors),
			}))
		}
		c.onAnalyzeStorage()
	})
}
