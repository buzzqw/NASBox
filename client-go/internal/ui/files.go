package ui

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"nasbox-go/internal/conflicts"
	"nasbox-go/internal/history"
	"nasbox-go/internal/remote"
)

// Sort modes of the NAS browser (stored in config key browse_sort).
const (
	sortFoldersFirst = "folders"
	sortFilesFirst   = "files"
	sortNameAsc      = "name"
	sortNameDesc     = "name_desc"
	sortSize         = "size"
	sortNewest       = "newest"
)

func browseSortOptions() []settingOption {
	return []settingOption{
		{value: sortFoldersFirst, label: lt("Cartelle prima", "Folders first")},
		{value: sortFilesFirst, label: lt("File prima", "Files first")},
		{value: sortNameAsc, label: lt("Nome A → Z", "Name A → Z")},
		{value: sortNameDesc, label: lt("Nome Z → A", "Name Z → A")},
		{value: sortSize, label: lt("Dimensione (grandi prima)", "Size (largest first)")},
		{value: sortNewest, label: lt("Data (recenti prima)", "Date (newest first)")},
	}
}

// sortBrowse orders a folder listing. Names compare case-insensitively with
// the original spelling as tie-breaker, so the order is stable.
func sortBrowse(entries []remote.BrowseEntry, mode string) []remote.BrowseEntry {
	sorted := append([]remote.BrowseEntry(nil), entries...)
	isDir := func(e remote.BrowseEntry) bool { return e.Kind == "DIR" }
	byName := func(a, b remote.BrowseEntry) bool {
		la, lb := strings.ToLower(a.Name), strings.ToLower(b.Name)
		if la != lb {
			return la < lb
		}
		return a.Name < b.Name
	}
	sort.SliceStable(sorted, func(i, j int) bool {
		a, b := sorted[i], sorted[j]
		switch mode {
		case sortFilesFirst:
			if isDir(a) != isDir(b) {
				return !isDir(a)
			}
		case sortNameAsc:
			return byName(a, b)
		case sortNameDesc:
			return byName(b, a)
		case sortSize:
			if isDir(a) != isDir(b) {
				return isDir(a)
			}
			if a.Size != b.Size {
				return a.Size > b.Size
			}
		case sortNewest:
			if a.MTime != b.MTime {
				return a.MTime > b.MTime
			}
		default: // folders first
			if isDir(a) != isDir(b) {
				return isDir(a)
			}
		}
		return byName(a, b)
	})
	return sorted
}

// browseFileStatus is the per-file indication shown in the NAS browser. It is
// deliberately conservative: without downloading a digest for every row, the
// local copy is compared by presence and size. Conflict and an in-flight
// transfer take precedence because they are authoritative.
type browseFileStatus struct {
	Label   string
	Hint    string
	Warning bool
}

func (c *clientApp) browseConflictStatus(relative string) bool {
	for _, group := range c.confGroups {
		if group.Original == relative {
			return true
		}
		for _, candidate := range group.Candidates {
			if candidate == relative {
				return true
			}
		}
	}
	return false
}

func (c *clientApp) browseTransferStatus(relative string) string {
	c.progressMu.Lock()
	progress := c.progress
	c.progressMu.Unlock()
	if progress.Finished || progress.Total == 0 || progress.File == "" || progress.File != relative {
		return ""
	}
	if progress.Upload {
		return tr("ui.file.status_uploading", nil)
	}
	return tr("ui.file.status_downloading", nil)
}

func (c *clientApp) browseFileStatus(relative string, entry remote.BrowseEntry) browseFileStatus {
	if entry.Kind != "FILE" {
		return browseFileStatus{}
	}
	if c.browseConflictStatus(relative) {
		return browseFileStatus{
			Label:   tr("ui.file.status_conflict", nil),
			Hint:    tr("ui.file.status_conflict_hint", nil),
			Warning: true,
		}
	}
	if active := c.browseTransferStatus(relative); active != "" {
		return browseFileStatus{Label: active, Hint: tr("ui.file.status_transfer_hint", nil)}
	}
	root := strings.TrimSpace(c.cfg.LocalRoot())
	if root == "" {
		return browseFileStatus{Label: tr("ui.file.status_remote_only", nil), Hint: tr("ui.file.status_remote_hint", nil)}
	}
	info, err := os.Stat(filepath.Join(root, filepath.FromSlash(relative)))
	if err != nil || !info.Mode().IsRegular() {
		return browseFileStatus{Label: tr("ui.file.status_remote_only", nil), Hint: tr("ui.file.status_remote_hint", nil)}
	}
	if info.Size() != entry.Size {
		return browseFileStatus{
			Label:   tr("ui.file.status_not_synced", nil),
			Hint:    tr("ui.file.status_not_synced_hint", nil),
			Warning: true,
		}
	}
	if c.ghost {
		return browseFileStatus{Label: tr("ui.file.status_available", nil), Hint: tr("ui.file.status_available_hint", nil)}
	}
	return browseFileStatus{Label: tr("ui.file.status_synced", nil), Hint: tr("ui.file.status_synced_hint", nil)}
}

func (c *clientApp) browseEntryByName(name string) (remote.BrowseEntry, bool) {
	for _, entry := range c.browseEntries {
		if entry.Name == name {
			return entry, true
		}
	}
	return remote.BrowseEntry{}, false
}

func (c *clientApp) browseSortSelect() fyne.CanvasObject {
	options := browseSortOptions()
	labels := make([]string, 0, len(options))
	selected := options[0].label
	current := c.cfg.String("browse_sort")
	for _, option := range options {
		labels = append(labels, option.label)
		if option.value == current {
			selected = option.label
		}
	}
	selectWidget := widget.NewSelect(labels, nil)
	selectWidget.SetSelected(selected)
	selectWidget.OnChanged = func(value string) {
		for _, option := range options {
			if option.label == value {
				_ = c.cfg.Set("browse_sort", option.value, true)
				c.browseEntries = sortBrowse(c.browseEntries, option.value)
				if c.browseList != nil {
					c.browseList.UnselectAll()
					c.browseList.Refresh()
				}
				return
			}
		}
	}
	return selectWidget
}

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
			meta := humanBytes(entry.Size)
			if entry.MTime > 0 {
				meta += " · " + time.Unix(entry.MTime, 0).Format("02/01/2006")
			}
			status := c.browseFileStatus(c.joinBrowsePath(entry.Name), entry)
			if status.Label != "" {
				meta = status.Label + " · " + meta
			}
			row.meta.SetText(meta)
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
		c.downloadNotice = ""
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
	c.browseGoEntry = widget.NewEntry()
	c.browseGoEntry.SetPlaceHolder(lt("Vai a cartella (percorso NAS)", "Go to folder (NAS path)"))
	c.browseGoEntry.OnSubmitted = func(text string) { c.onBrowseGo(text) }

	c.browseDeleteBtn = plainButton(tr("browse.delete_btn", nil), theme.DeleteIcon(),
		tr("browse.delete_tooltip", nil), widget.DangerImportance, c.onBrowseDelete, c.setHint)
	c.browseRenameBtn = plainButton(tr("browse.rename_btn", nil), theme.DocumentCreateIcon(),
		tr("ui.file.rename_hint", nil), widget.LowImportance, c.onBrowseRename, c.setHint)
	c.browseDownloadBtn = plainButton(tr("ui.action.download", nil), theme.DownloadIcon(),
		tr("browse.download_tooltip", nil), widget.MediumImportance, c.onBrowseDownload, c.setHint)
	c.browseUploadBtn = plainButton(lt("Carica", "Upload"), theme.ContentAddIcon(),
		lt("Carica uno o più file nella cartella NAS aperta", "Upload one or more files into the open NAS folder"),
		widget.HighImportance, c.onBrowseUpload, c.setHint)
	c.browseVersionsBtn = plainButton(tr("ui.versions.button", nil), theme.HistoryIcon(),
		tr("ui.versions.hint", nil), widget.LowImportance, func() {
			if c.selectedBrowse != "" {
				c.showFileVersions(c.joinBrowsePath(c.selectedBrowse))
			}
		}, c.setHint)
	c.browseUpBtn = plainButton(tr("browse.up_btn", nil), theme.NavigateBackIcon(),
		tr("ui.file.up_hint", nil), widget.LowImportance, c.onBrowseUp, c.setHint)

	refreshBtn := newHintButton(tr("browse.refresh_btn", nil), theme.ViewRefreshIcon(),
		tr("ui.file.refresh_hint", nil), widget.LowImportance, func() {
			c.appendAction(tr("browse.refresh_btn", nil))
			c.refreshBrowse()
		}, c.setHint)
	downloadFolderBtn := newHintButton("", theme.FolderOpenIcon(),
		tr("ui.file.download_path_hint", nil), widget.LowImportance, c.onDownloadPath, c.setHint)

	// Two rows: where am I (navigation) and what can I do with the selection.
	// The text path and the "remote path" box duplicated the breadcrumbs, and
	// the folder download is now one button; the entry stays as hidden state.
	navigation := container.NewBorder(nil, nil, c.browseUpBtn,
		container.NewHBox(minWidth(c.browseSortSelect(), 200), minWidth(c.browseGoEntry, 180),
			downloadFolderBtn, refreshBtn), c.breadcrumbs)
	selection := container.NewBorder(nil, nil, nil,
		container.NewHBox(c.browseVersionsBtn, c.browseRenameBtn, c.browseDeleteBtn,
			c.browseDownloadBtn, c.browseUploadBtn),
		c.browseSelectedLabel)
	headerObjects := []fyne.CanvasObject{
		c.sectionTitle("", tr("ui.file.browse_note", nil)),
		navigation,
		selection,
	}
	if c.ghost {
		note := widget.NewLabel(lt("Modalità consultazione: il NAS non viene sincronizzato in automatico. Scarica e carica solo quello che ti serve.",
			"Consultation mode: the NAS is never synced automatically. Download and upload only what you need."))
		note.Wrapping = fyne.TextWrapWord
		note.Importance = widget.LowImportance
		headerObjects = append([]fyne.CanvasObject{note}, headerObjects...)
	}
	header := container.NewVBox(headerObjects...)
	c.updateBreadcrumbs()
	c.updateBrowseSelection()
	return container.NewBorder(header, nil, nil, nil, c.browseList)
}

func (c *clientApp) buildConflictPane() fyne.CanvasObject {
	c.confList = newRowList(
		func() int { return len(c.confGroups) }, 120, 30,
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
		func() int { return len(c.confChoices) }, 120, 110,
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
	actions := buttonRows(3,
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
	c.historyFilter.SetPlaceHolder(lt("Cerca tra i file eliminati…", "Search the deleted files…"))
	c.historyFilter.OnChanged = func(string) { c.histList.Refresh() }
	c.histList = newRowList(
		func() int { return len(c.filteredVersions()) }, 220, 250,
		func(id int, row *listRow) {
			versions := c.filteredVersions()
			if id >= len(versions) {
				return
			}
			item := versions[id]
			row.icon.SetResource(theme.HistoryIcon())
			name, folder := splitPath(item.Original)
			row.name.SetText(name + "  —  " + folder)
			kind := lt("Sostituito", "Replaced")
			if c.versionDeleted[item.Original] {
				kind = lt("Eliminato", "Deleted")
			}
			when := shortTimestamp(item.Timestamp)
			if at, err := history.VersionTime(item.Timestamp); err == nil {
				when = relativeTime(at, time.Now())
			}
			row.meta.SetText(fmt.Sprintf("%s · %s · %s", kind, when, humanBytes(item.Size)))
		},
	)
	c.histList.OnSelected = func(id widget.ListItemID) {
		versions := c.filteredVersions()
		if int(id) < len(versions) {
			c.selectedVersion = versions[id].TrashPath
			c.updateHistorySelection()
		}
	}
	c.historyMode = widget.NewSelect([]string{
		lt("File eliminati di recente", "Recently deleted files"),
		lt("Tutte le versioni precedenti", "All earlier versions"),
	}, func(string) {
		c.selectedVersion = ""
		c.updateHistorySelection()
		c.histList.UnselectAll()
		c.histList.Refresh()
	})
	c.historyMode.SetSelected(lt("File eliminati di recente", "Recently deleted files"))
	c.histDetail = widget.NewLabel(tr("ui.history.none_selected", nil))
	c.histDetail.Wrapping = fyne.TextWrapWord
	c.restoreBtn = plainButton(tr("ui.action.restore", nil), theme.ContentRedoIcon(),
		tr("history.restore_tooltip", nil), widget.HighImportance, c.onRestoreVersion, c.setHint)
	header := container.NewVBox(
		c.sectionTitle("", lt("File eliminati o sostituiti, ripristinabili in un clic. Le copie restano per il numero di giorni impostato.",
			"Deleted or replaced files, restorable in one click. Copies are kept for the number of days you set.")),
		container.NewHBox(minWidth(c.historyMode, 260)),
		c.historyFilter,
		c.histDetail,
		buttonRows(3,
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
	c.downloadNotice = ""
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
		switch {
		case c.downloadNotice != "":
			// The outcome of a download stays here: this tab does not show the
			// activity list, so a selection-only label hid the result.
			c.browseSelectedLabel.SetText(c.downloadNotice)
		case c.selectedBrowse == "":
			c.browseSelectedLabel.SetText(tr("ui.file.none_selected", nil))
		default:
			status := browseFileStatus{}
			if entry, ok := c.browseEntryByName(c.selectedBrowse); ok {
				status = c.browseFileStatus(c.joinBrowsePath(c.selectedBrowse), entry)
			}
			if status.Label != "" {
				c.browseSelectedLabel.SetText(tr("ui.file.selected_status", map[string]any{
					"name": c.selectedBrowse, "status": status.Label,
				}))
			} else {
				c.browseSelectedLabel.SetText(tr("ui.file.selected", map[string]any{"name": c.selectedBrowse}))
			}
			if status.Hint != "" && c.setHint != nil {
				c.setHint(status.Hint)
			}
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
