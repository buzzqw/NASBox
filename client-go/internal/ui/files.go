package ui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"nasbox-go/internal/conflicts"
	"nasbox-go/internal/history"
)

func (c *clientApp) buildBrowsePane() fyne.CanvasObject {
	c.browseList = newRowList(
		func() int { return len(c.browseEntries) }, 280, 120,
		func(id int, row *listRow) {
			entry := c.browseEntries[id]
			row.icon.SetResource(theme.FileIcon())
			if entry.Kind == "DIR" {
				row.icon.SetResource(theme.FolderIcon())
				row.name.SetText(entry.Name + "/")
				row.meta.SetText(tr("ui.file.folder", nil))
				return
			}
			row.name.SetText(entry.Name)
			row.meta.SetText(humanBytes(entry.Size))
		},
	)
	c.browseList.OnSelected = func(id widget.ListItemID) {
		if int(id) >= len(c.browseEntries) {
			return
		}
		entry := c.browseEntries[id]
		if entry.Kind == "DIR" {
			c.browseTo(c.joinBrowsePath(entry.Name))
			return
		}
		c.selectedBrowse = entry.Name
		c.updateBrowseSelection()
	}
	c.browsePathLabel = widget.NewLabel(tr("browse.path_label", map[string]any{"path": "/"}))
	c.browsePathLabel.Wrapping = fyne.TextTruncate
	c.breadcrumbs = container.NewHScroll(container.NewHBox())
	c.browseSelectedLabel = widget.NewLabel(tr("ui.file.none_selected", nil))
	c.browseSelectedLabel.Wrapping = fyne.TextTruncate
	c.downloadPathEntry = widget.NewEntry()
	c.downloadPathEntry.SetPlaceHolder(tr("ui.file.download_path_hint", nil))
	c.downloadPathEntry.OnSubmitted = func(string) { c.onDownloadPath() }

	c.browseDeleteBtn = plainButton(tr("browse.delete_btn", nil), theme.DeleteIcon(),
		tr("browse.delete_tooltip", nil), widget.DangerImportance, c.onBrowseDelete, c.setHint)
	c.browseRenameBtn = plainButton(tr("browse.rename_btn", nil), theme.DocumentCreateIcon(),
		tr("ui.file.rename_hint", nil), widget.LowImportance, c.onBrowseRename, c.setHint)
	c.browseDownloadBtn = plainButton(tr("ui.action.download", nil), theme.DownloadIcon(),
		tr("browse.download_tooltip", nil), widget.MediumImportance, c.onBrowseDownload, c.setHint)
	c.browseVersionsBtn = plainButton(tr("ui.versions.button", nil), theme.HistoryIcon(),
		tr("ui.versions.hint", nil), widget.LowImportance, func() {
			if c.selectedBrowse != "" {
				c.showFileVersions(c.joinBrowsePath(c.selectedBrowse))
			}
		}, c.setHint)
	c.browseUpBtn = plainButton(tr("browse.up_btn", nil), theme.NavigateBackIcon(),
		tr("ui.file.up_hint", nil), widget.LowImportance, c.onBrowseUp, c.setHint)

	header := container.NewVBox(
		c.sectionTitle("", tr("ui.file.browse_note", nil)),
		c.browsePathLabel,
		c.breadcrumbs,
		c.browseSelectedLabel,
		container.NewHBox(
			c.browseUpBtn,
			newHintButton(tr("browse.refresh_btn", nil), theme.ViewRefreshIcon(),
				tr("ui.file.refresh_hint", nil), widget.LowImportance, func() {
					c.appendAction(tr("browse.refresh_btn", nil))
					c.refreshBrowse()
				}, c.setHint),
			c.browseDeleteBtn,
			c.browseRenameBtn,
			c.browseVersionsBtn,
			c.browseDownloadBtn,
		),
		container.NewBorder(nil, nil,
			widget.NewLabel(tr("ui.file.download_path", nil)),
			newHintButton(tr("ui.action.download_path_short", nil), theme.DownloadIcon(),
				tr("ui.file.download_path_hint", nil), widget.HighImportance, c.onDownloadPath, c.setHint),
			c.downloadPathEntry,
		),
	)
	c.updateBreadcrumbs()
	c.updateBrowseSelection()
	return container.NewBorder(header, nil, nil, nil, c.browseList)
}

func (c *clientApp) buildConflictPane() fyne.CanvasObject {
	c.confList = newRowList(
		func() int { return len(c.confGroups) }, 200, 40,
		func(id int, row *listRow) {
			group := c.confGroups[id]
			row.icon.SetResource(theme.WarningIcon())
			row.name.SetText(group.Original)
			row.meta.SetText(fmt.Sprint(len(group.Candidates)))
		},
	)
	c.confList.OnSelected = func(id widget.ListItemID) {
		if int(id) < len(c.confGroups) {
			c.selectConflictGroup(c.confGroups[id])
		}
	}
	c.confChoiceList = newRowList(
		func() int { return len(c.confChoices) }, 180, 160,
		func(id int, row *listRow) {
			path := c.confChoices[id]
			row.icon.SetResource(theme.FileIcon())
			row.name.SetText(path)
			if id == 0 {
				row.meta.SetText(tr("conflicts.original_option", nil))
			} else {
				row.meta.SetText(c.conflictVersionMeta(path))
			}
		},
	)
	c.confChoiceList.OnSelected = func(id widget.ListItemID) {
		if int(id) < len(c.confChoices) {
			c.selectedConflict = c.confChoices[id]
			c.updateConflictSelection()
		}
	}
	c.confDetail = widget.NewLabel(tr("conflicts.no_selection", nil))
	c.confDetail.Wrapping = fyne.TextWrapWord
	c.resolveBtn = plainButton(tr("ui.action.resolve", nil), theme.ConfirmIcon(),
		tr("conflicts.confirm_title", nil), widget.HighImportance, c.onResolveConflict, c.setHint)
	c.keepBothBtn = plainButton(tr("ui.action.keep_both", nil), theme.ContentCopyIcon(),
		tr("conflicts.keep_both_tooltip", nil), widget.MediumImportance, c.onKeepBoth, c.setHint)

	groups := container.NewBorder(
		container.NewVBox(c.sectionTitle(tr("conflicts.list_title", nil), tr("conflicts.notice", nil))),
		nil, nil, nil, c.confList,
	)
	choices := container.NewBorder(
		container.NewVBox(
			c.sectionTitle(tr("ui.conflict.choose_title", nil), tr("ui.conflict.choose_note", nil)),
			c.confDetail,
		),
		nil, nil, nil, c.confChoiceList,
	)
	actions := container.NewHBox(
		newHintButton(tr("conflicts.refresh_btn", nil), theme.ViewRefreshIcon(),
			tr("ui.file.refresh_hint", nil), widget.LowImportance, func() {
				c.appendAction(tr("conflicts.refresh_btn", nil))
				c.refreshConflicts()
			}, c.setHint),
		c.resolveBtn,
		c.keepBothBtn,
	)
	c.updateConflictSelection()
	return container.NewBorder(nil, actions, nil, nil, container.NewGridWithColumns(2, groups, choices))
}

func (c *clientApp) buildHistoryPane() fyne.CanvasObject {
	c.historyFilter = widget.NewEntry()
	c.historyFilter.SetPlaceHolder(tr("browse.search_placeholder", nil))
	c.historyFilter.OnChanged = func(string) { c.histList.Refresh() }
	c.histList = newRowList(
		func() int { return len(c.filteredVersions()) }, 280, 180,
		func(id int, row *listRow) {
			versions := c.filteredVersions()
			if id >= len(versions) {
				return
			}
			item := versions[id]
			row.icon.SetResource(theme.HistoryIcon())
			row.name.SetText(item.Original)
			row.meta.SetText(fmt.Sprintf("%s · %s", shortTimestamp(item.Timestamp), humanBytes(item.Size)))
		},
	)
	c.histList.OnSelected = func(id widget.ListItemID) {
		versions := c.filteredVersions()
		if int(id) < len(versions) {
			c.selectedVersion = versions[id].TrashPath
			c.updateHistorySelection()
		}
	}
	c.histDetail = widget.NewLabel(tr("ui.history.none_selected", nil))
	c.histDetail.Wrapping = fyne.TextWrapWord
	c.restoreBtn = plainButton(tr("ui.action.restore", nil), theme.ContentRedoIcon(),
		tr("history.restore_tooltip", nil), widget.HighImportance, c.onRestoreVersion, c.setHint)
	header := container.NewVBox(
		c.sectionTitle("", tr("ui.history.note", nil)),
		c.historyFilter,
		c.histDetail,
		container.NewHBox(
			newHintButton(tr("history.refresh_btn", nil), theme.ViewRefreshIcon(),
				tr("ui.file.refresh_hint", nil), widget.LowImportance, func() {
					c.appendAction(tr("history.refresh_btn", nil))
					c.refreshHistory()
				}, c.setHint),
			c.restoreBtn,
			newHintButton(tr("ui.versions.button_file", nil), theme.HistoryIcon(),
				tr("ui.versions.hint", nil), widget.MediumImportance, func() {
					initial := ""
					for _, version := range c.versions {
						if version.TrashPath == c.selectedVersion {
							initial = version.Original
						}
					}
					c.promptFileVersions(initial)
				}, c.setHint),
			newHintButton(tr("ui.action.remote_restore", nil), theme.ContentRedoIcon(),
				tr("ui.history.remote_hint", nil), widget.MediumImportance, c.onRemoteRestore, c.setHint),
			newHintButton(tr("ui.action.prune", nil), theme.DeleteIcon(),
				tr("ui.history.prune_hint", nil), widget.DangerImportance, c.onPrune, c.setHint),
		),
	)
	c.updateHistorySelection()
	return container.NewBorder(header, nil, nil, nil, c.histList)
}

func (c *clientApp) joinBrowsePath(name string) string {
	name = strings.Trim(strings.TrimSpace(name), "/")
	if c.browsePath == "" {
		return name
	}
	if name == "" {
		return c.browsePath
	}
	return strings.TrimRight(c.browsePath, "/") + "/" + name
}

func (c *clientApp) browseTo(path string) {
	c.browsePath = strings.Trim(strings.TrimSpace(path), "/")
	c.selectedBrowse = ""
	c.refreshBrowse()
	c.appendAction(tr("ui.file.path_opened", map[string]any{"path": valueOrDash(c.browsePath)}))
}

func (c *clientApp) updateBreadcrumbs() {
	if c.breadcrumbs == nil {
		return
	}
	objects := []fyne.CanvasObject{
		newHintButton("/", theme.HomeIcon(), tr("ui.file.root_hint", nil), widget.LowImportance,
			func() { c.browseTo("") }, c.setHint),
	}
	parts := strings.Split(strings.Trim(c.browsePath, "/"), "/")
	path := ""
	for _, part := range parts {
		if part == "" {
			continue
		}
		path = c.joinBrowsePath(part)
		segment := path
		objects = append(objects, newHintButton(part, theme.FolderIcon(), segment, widget.LowImportance,
			func() { c.browseTo(segment) }, c.setHint))
	}
	c.breadcrumbs.Content = container.NewHBox(objects...)
	c.breadcrumbs.Refresh()
	if c.browsePathLabel != nil {
		c.browsePathLabel.SetText(tr("browse.path_label", map[string]any{"path": valueOrDash(c.browsePath)}))
	}
	if c.browseUpBtn != nil {
		if c.browsePath == "" {
			c.browseUpBtn.Disable()
		} else {
			c.browseUpBtn.Enable()
		}
	}
	if c.downloadPathEntry != nil {
		c.downloadPathEntry.SetText(c.browsePath)
	}
}

func (c *clientApp) updateBrowseSelection() {
	if c.browseSelectedLabel != nil {
		if c.selectedBrowse == "" {
			c.browseSelectedLabel.SetText(tr("ui.file.none_selected", nil))
		} else {
			c.browseSelectedLabel.SetText(tr("ui.file.selected", map[string]any{"name": c.selectedBrowse}))
		}
	}
	if c.browseDeleteBtn != nil {
		c.browseDeleteBtn.Disable()
		if c.selectedBrowse != "" {
			c.browseDeleteBtn.Enable()
		}
	}
	if c.browseRenameBtn != nil {
		c.browseRenameBtn.Disable()
		if c.selectedBrowse != "" {
			c.browseRenameBtn.Enable()
		}
	}
	if c.browseVersionsBtn != nil {
		c.browseVersionsBtn.Disable()
		if c.selectedBrowse != "" && !strings.HasSuffix(c.selectedBrowse, "/") {
			c.browseVersionsBtn.Enable()
		}
	}
	if c.browseDownloadBtn != nil {
		c.browseDownloadBtn.Disable()
		if c.selectedBrowse != "" {
			c.browseDownloadBtn.Enable()
		}
	}
}

func (c *clientApp) selectConflictGroup(group conflicts.Group) {
	c.selectedGroup = group.Original
	c.confChoices = append([]string{group.Original}, group.Candidates...)
	c.selectedConflict = ""
	if len(c.confChoices) > 0 {
		c.selectedConflict = c.confChoices[0]
	}
	if c.confChoiceList != nil {
		c.confChoiceList.UnselectAll()
		c.confChoiceList.Refresh()
	}
	c.updateConflictSelection()
}

func (c *clientApp) updateConflictSelection() {
	if c.confDetail != nil {
		if c.selectedConflict == "" {
			c.confDetail.SetText(tr("conflicts.no_selection", nil))
		} else {
			c.confDetail.SetText(fmt.Sprintf("%s\n%s", c.selectedConflict, c.conflictVersionMeta(c.selectedConflict)))
		}
	}
	if c.resolveBtn != nil {
		c.resolveBtn.Disable()
		if c.selectedConflict != "" {
			c.resolveBtn.Enable()
		}
	}
	if c.keepBothBtn != nil {
		c.keepBothBtn.Disable()
		if c.selectedConflict != "" && c.selectedConflict != c.selectedGroup {
			c.keepBothBtn.Enable()
		}
	}
}

func (c *clientApp) conflictVersionMeta(path string) string {
	info, err := os.Stat(filepath.Join(c.cfg.LocalRoot(), filepath.FromSlash(path)))
	if err != nil {
		return tr("conflicts.preview_missing", nil)
	}
	return fmt.Sprintf("%s · %s", humanBytes(info.Size()), info.ModTime().Format("02/01 15:04"))
}

func (c *clientApp) updateHistorySelection() {
	if c.histDetail != nil {
		if c.selectedVersion == "" {
			c.histDetail.SetText(tr("ui.history.none_selected", nil))
		} else {
			for _, item := range c.versions {
				if item.TrashPath == c.selectedVersion {
					c.histDetail.SetText(fmt.Sprintf("%s · %s · %s", item.Original,
						item.Timestamp, humanBytes(item.Size)))
					break
				}
			}
		}
	}
	if c.restoreBtn != nil {
		c.restoreBtn.Disable()
		if c.selectedVersion != "" {
			c.restoreBtn.Enable()
		}
	}
}

func (c *clientApp) historyVersion(path string) (history.Version, bool) {
	for _, item := range c.versions {
		if item.TrashPath == path {
			return item, true
		}
	}
	return history.Version{}, false
}
