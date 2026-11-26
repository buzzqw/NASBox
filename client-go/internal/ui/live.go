package ui

import (
	"fmt"
	"nasbox-go/internal/osx"
	"path/filepath"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"nasbox-go/internal/transfer"
)

// recentItem is one file moved by NASBox, shown in "Attività recente", in
// the Transfers tab and in the tray.
type recentItem struct {
	Path string
	Kind string // up | down | del | conflict
	At   time.Time
}

const maxRecent = 200

// recentKinds maps persistent event actions (shared with the Python client)
// to recent-item kinds.
var recentKinds = map[string]string{
	"UPLOAD": "up", "DOWNLOAD": "down",
	"DELETE_REMOTE": "del", "DELETE_LOCAL": "del",
	"CONFLICT": "conflict", "CONFLICT_AUTO": "conflict",
}

// seedRecent loads the recent file history from events.jsonl, so the list
// survives restarts and includes what the Python client did on this PC.
func (c *clientApp) seedRecent() {
	if c.logger == nil {
		return
	}
	entries, err := c.logger.Recent(2000)
	if err != nil {
		return
	}
	items := make([]recentItem, 0, maxRecent)
	for index := len(entries) - 1; index >= 0 && len(items) < maxRecent; index-- {
		entry := entries[index]
		kind, ok := recentKinds[entry.Action]
		if !ok || entry.Path == "" || entry.Path == "-" {
			continue
		}
		items = append(items, recentItem{Path: entry.Path, Kind: kind, At: entry.Timestamp})
		if c.lastSyncAt.IsZero() {
			c.lastSyncAt = entry.Timestamp
		}
	}
	fyne.Do(func() {
		c.recent = items
		c.renderRecent()
		c.refreshTransfersPane()
		c.refreshTray()
	})
}

// addRecent records a transferred file. It is called once per file from the
// goroutine that reads rsync's output, so it must stay cheap: files are queued
// and flushed a few times per second, off that goroutine. Persisting every
// file synchronously and rebuilding the widgets for each one made the screen
// lag far behind rsync (and slowed rsync down) during large downloads.
func (c *clientApp) addRecent(item recentItem) {
	c.recentMu.Lock()
	c.recentPending = append(c.recentPending, item)
	schedule := !c.recentFlushing
	c.recentFlushing = true
	c.recentMu.Unlock()
	if schedule {
		time.AfterFunc(250*time.Millisecond, c.flushRecent)
	}
}

func (c *clientApp) flushRecent() {
	c.recentMu.Lock()
	pending := c.recentPending
	c.recentPending = nil
	c.recentFlushing = false
	c.recentMu.Unlock()
	if len(pending) == 0 {
		return
	}
	if c.logger != nil {
		for _, item := range pending {
			if action := map[string]string{"up": "UPLOAD", "down": "DOWNLOAD"}[item.Kind]; action != "" {
				_ = c.logger.Event(action, item.Path, "")
			}
		}
	}
	fyne.Do(func() {
		fresh := make([]recentItem, 0, len(pending)+len(c.recent))
		for index := len(pending) - 1; index >= 0; index-- { // newest first
			fresh = append(fresh, pending[index])
		}
		c.recent = append(fresh, c.recent...)
		if len(c.recent) > maxRecent {
			c.recent = c.recent[:maxRecent]
		}
		c.renderRecent()
		c.refreshTransfersPane()
		c.refreshTray()
	})
}

func recentIcon(kind string) fyne.Resource {
	switch kind {
	case "up":
		return theme.UploadIcon()
	case "down":
		return theme.DownloadIcon()
	case "del":
		return theme.DeleteIcon()
	case "conflict":
		return theme.WarningIcon()
	}
	return theme.FileIcon()
}

func recentVerb(kind string) string {
	switch kind {
	case "up":
		return tr("ui.recent.uploaded", nil)
	case "down":
		return tr("ui.recent.downloaded", nil)
	case "del":
		return tr("ui.recent.deleted", nil)
	case "conflict":
		return tr("ui.recent.conflict", nil)
	}
	return ""
}

// recentGroup is one row of the home card: a single file, or a run of files
// of the same kind in the same folder (a bulk download is one line, not
// thousands).
type recentGroup struct {
	Kind   string
	Folder string
	Name   string // newest file of the group
	Path   string
	Count  int
	At     time.Time // newest
}

// groupRecent folds consecutive items (newest first) with the same kind and
// folder, and returns at most limit groups.
func groupRecent(items []recentItem, limit int) []recentGroup {
	groups := make([]recentGroup, 0, limit)
	for _, item := range items {
		name, folder := splitPath(item.Path)
		if last := len(groups) - 1; last >= 0 && groups[last].Kind == item.Kind && groups[last].Folder == folder {
			groups[last].Count++
			continue
		}
		if len(groups) == limit {
			break
		}
		groups = append(groups, recentGroup{
			Kind: item.Kind, Folder: folder, Name: name, Path: item.Path, Count: 1, At: item.At,
		})
	}
	return groups
}

// recentGroupText is "name · folder" or "1.240 file scaricati · folder".
func recentGroupText(group recentGroup) string {
	if group.Count <= 1 {
		return group.Name + "  ·  " + group.Folder
	}
	return tr("ui.recent.group_"+group.Kind, map[string]any{"count": formatCount(group.Count)}) +
		"  ·  " + group.Folder
}

// renderRecent redraws the home card: compact one-line rows, at most 7.
func (c *clientApp) renderRecent() {
	if c.recentBox == nil {
		return
	}
	c.recentBox.Objects = nil
	if len(c.recent) == 0 {
		empty := widget.NewLabel(tr("ui.home.recent_empty", nil))
		empty.Importance = widget.LowImportance
		empty.Wrapping = fyne.TextWrapWord
		c.recentBox.Add(empty)
		c.recentBox.Refresh()
		return
	}
	now := time.Now()
	for _, group := range groupRecent(c.recent, 7) {
		title := widget.NewLabel(recentGroupText(group))
		title.Truncation = fyne.TextTruncateEllipsis
		when := widget.NewLabel(relativeTime(group.At, now))
		when.Importance = widget.LowImportance
		when.SizeName = theme.SizeNameCaptionText
		target := group.Path
		open := func() { c.openContainingFolder(target) }
		tip := group.Path + "\n" + lt("Clic: apri la cartella nel file manager", "Click: open the folder in the file manager")
		if group.Count == 1 && group.Kind != "del" {
			// One file: open the file itself, like Dropbox's recent list.
			open = func() { c.openFile(target) }
			tip = group.Path + "\n" + lt("Clic: apri il file", "Click: open the file")
		}
		row := newTapRow(c, container.NewBorder(nil, nil, widget.NewIcon(recentIcon(group.Kind)), when, title), open)
		row.tip = tip
		c.recentBox.Add(row)
	}
	c.recentBox.Refresh()
	if c.homeBox != nil {
		c.homeBox.Refresh()
	}
}

// openContainingFolder opens the folder of a synced file in the desktop's
// file manager.
func (c *clientApp) openContainingFolder(relative string) {
	root := c.cfg.LocalRoot()
	if root == "" {
		return
	}
	dir := filepath.Dir(filepath.Join(root, filepath.FromSlash(relative)))
	_ = osx.Open(dir)
}

// ---------------------------------------------------------------------------
// Live progress from rsync
// ---------------------------------------------------------------------------

// installTransferObservers connects rsync progress to the UI.
func (c *clientApp) installTransferObservers() {
	if c.transfers == nil {
		return
	}
	c.syncTransfers().SetObservers(c.onTransferProgress, func(event transfer.FileEvent) {
		kind := "down"
		if event.Upload {
			kind = "up"
		}
		c.addRecent(recentItem{Path: event.Path, Kind: kind, At: event.At})
	})
}

func (c *clientApp) onTransferProgress(progress transfer.Progress) {
	now := time.Now()
	c.progressMu.Lock()
	c.progress = progress
	throttled := !progress.Finished && now.Sub(c.progressShown) < 200*time.Millisecond
	if !throttled {
		c.progressShown = now
	}
	trailing := throttled && !c.progressTrailing
	if trailing {
		c.progressTrailing = true
	}
	c.progressMu.Unlock()
	if throttled {
		// Always paint the last snapshot of a burst: dropping it left the
		// file name and counter stuck on an old value until the next update.
		if trailing {
			time.AfterFunc(220*time.Millisecond, c.renderLatestProgress)
		}
		return
	}
	fyne.Do(func() { c.renderProgress(progress) })
}

func (c *clientApp) renderLatestProgress() {
	c.progressMu.Lock()
	progress := c.progress
	c.progressTrailing = false
	c.progressShown = time.Now()
	c.progressMu.Unlock()
	fyne.Do(func() { c.renderProgress(progress) })
}

// transferView is the live transfer block: title (direction, files done of
// total), percentage, current file, bar and details. The home page and the
// Activity > Transfers tab each show one, fed by the same snapshot.
type transferView struct {
	icon    *widget.Icon
	title   *widget.Label
	percent *widget.Label
	file    *widget.Label
	bar     *widget.ProgressBar
	meta    *widget.Label
	content fyne.CanvasObject
}

func newTransferView() *transferView {
	v := &transferView{
		icon:    widget.NewIcon(theme.DownloadIcon()),
		title:   widget.NewLabelWithStyle(tr("ui.transfer.idle", nil), fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		percent: widget.NewLabelWithStyle("", fyne.TextAlignTrailing, fyne.TextStyle{Bold: true}),
		file:    widget.NewLabel(""),
		bar:     widget.NewProgressBar(),
		meta:    widget.NewLabel(""),
	}
	v.file.Truncation = fyne.TextTruncateEllipsis
	v.bar.TextFormatter = func() string { return "" }
	v.meta.Importance = widget.LowImportance
	v.meta.SizeName = theme.SizeNameCaptionText
	v.content = container.NewVBox(
		container.NewBorder(nil, nil, v.icon, v.percent, v.title),
		v.file,
		v.bar,
		v.meta,
	)
	return v
}

// apply paints one snapshot.
func (v *transferView) apply(progress transfer.Progress) {
	direction := tr("ui.transfer.downloading", nil)
	icon := theme.DownloadIcon()
	if progress.Upload {
		direction = tr("ui.transfer.uploading", nil)
		icon = theme.UploadIcon()
	}
	v.icon.SetResource(icon)
	// Files already completed: it only moves when a file really finishes.
	v.title.SetText(tr("ui.transfer.title", map[string]any{
		"direction": direction, "current": formatCount(progress.Done), "total": formatCount(progress.Total),
	}))
	v.percent.SetText(fmt.Sprintf("%d%%", progress.Percent))
	if progress.File != "" {
		v.file.SetText(progress.File)
	}
	v.bar.SetValue(float64(progress.Percent) / 100)
	v.meta.SetText(progressMeta(progress))
}

func (c *clientApp) renderProgress(progress transfer.Progress) {
	if c.homeTransfer == nil {
		return
	}
	if progress.Finished {
		if progress.Err == nil {
			c.lastSyncAt = time.Now()
		}
		c.updateLastSync()
		c.homeTransfer.bar.SetValue(1)
		go func() {
			time.Sleep(1500 * time.Millisecond)
			fyne.Do(func() {
				c.progressMu.Lock()
				still := c.progress.Finished
				c.progressMu.Unlock()
				if still {
					c.setTransferCardVisible(false)
					c.refreshTransfersPane()
					c.refreshTray()
				}
			})
		}()
		return
	}
	c.homeTransfer.apply(progress)
	if c.tabTransfer != nil {
		c.tabTransfer.apply(progress)
		c.setTabTransferActive(true)
	}
	c.setTransferCardVisible(true)
	if c.lastSyncLabel != nil {
		c.lastSyncLabel.SetText(progressSummary(progress))
	}
	c.refreshTray()
}

// progressSummary is the short "58% · 16.5 MB/s" shown in the sidebar.
func progressSummary(progress transfer.Progress) string {
	text := fmt.Sprintf("%d%%", progress.Percent)
	if progress.Rate != "" {
		text += " · " + formatRate(progress.Rate)
	}
	return text
}

func progressMeta(progress transfer.Progress) string {
	parts := []string{}
	if progress.TotalBytes > 0 {
		parts = append(parts, tr("ui.transfer.bytes_of", map[string]any{
			"done": humanBytes(progress.Bytes), "total": humanBytes(progress.TotalBytes),
		}))
	} else if progress.Bytes > 0 {
		parts = append(parts, tr("ui.transfer.bytes_done", map[string]any{"done": humanBytes(progress.Bytes)}))
	}
	if progress.Rate != "" {
		parts = append(parts, formatRate(progress.Rate))
	}
	if progress.ETA != "" && progress.ETA != "0:00" {
		parts = append(parts, tr("ui.transfer.eta", map[string]any{"eta": progress.ETA}))
	}
	return strings.Join(parts, " · ")
}

// transferActive reports whether rsync is moving data right now.
func (c *clientApp) transferActive() (transfer.Progress, bool) {
	c.progressMu.Lock()
	defer c.progressMu.Unlock()
	return c.progress, c.progressShown.After(time.Time{}) && !c.progress.Finished && c.progress.Total > 0
}

func (c *clientApp) updateLastSync() {
	if c.lastSyncLabel == nil {
		return
	}
	if _, active := c.transferActive(); active {
		return // the sidebar shows the live percentage meanwhile
	}
	if c.lastSyncAt.IsZero() {
		c.lastSyncLabel.SetText(tr("ui.status.never_synced", nil))
		return
	}
	c.lastSyncLabel.SetText(tr("ui.status.last_sync", map[string]any{
		"when": relativeTime(c.lastSyncAt, time.Now()),
	}))
}

// ---------------------------------------------------------------------------
// Activity › Transfers: current transfer + detailed history list.
// ---------------------------------------------------------------------------

func (c *clientApp) buildTransfersPane() fyne.CanvasObject {
	c.tabTransfer = newTransferView()
	c.tabTransferIdle = widget.NewLabel(tr("ui.transfer.idle", nil))
	c.tabTransferIdle.Importance = widget.LowImportance
	c.tabTransferBox = container.NewVBox(c.tabTransferIdle)
	c.transferHistory = newRowList(
		func() int { return len(c.recent) }, 200, 170,
		func(id int, row *listRow) {
			if id >= len(c.recent) {
				return
			}
			item := c.recent[id]
			name, folder := splitPath(item.Path)
			row.icon.SetResource(recentIcon(item.Kind))
			row.name.SetText(name + "  —  " + folder)
			row.meta.SetText(recentVerb(item.Kind) + " · " + relativeTime(item.At, time.Now()))
		},
	)
	c.transferHistory.OnSelected = func(id widget.ListItemID) {
		if int(id) < len(c.recent) {
			c.openContainingFolder(c.recent[id].Path)
		}
		c.transferHistory.UnselectAll()
	}
	header := container.NewVBox(
		c.sectionTitle("", tr("ui.activity.transfers_note", nil)),
		c.surface(c.tabTransferBox, ""),
	)
	return container.NewBorder(header, nil, nil, nil, c.transferHistory)
}

// setTabTransferActive swaps the idle line for the live block. Objects are
// replaced (not Hide/Show) so the container re-lays out its new content.
func (c *clientApp) setTabTransferActive(active bool) {
	if c.tabTransferBox == nil {
		return
	}
	want := fyne.CanvasObject(c.tabTransferIdle)
	if active {
		want = c.tabTransfer.content
	}
	if len(c.tabTransferBox.Objects) == 1 && c.tabTransferBox.Objects[0] == want {
		return
	}
	c.tabTransferBox.Objects = []fyne.CanvasObject{want}
	c.tabTransferBox.Refresh()
}

func (c *clientApp) refreshTransfersPane() {
	if c.transferHistory != nil {
		c.transferHistory.Refresh()
	}
	if _, active := c.transferActive(); !active {
		c.setTabTransferActive(false)
	}
}

// formatRate turns rsync's "21.8MB/s" into "21.8 MB/s".
func formatRate(rate string) string {
	if strings.Contains(rate, " ") {
		return rate
	}
	for index, r := range rate {
		if (r < '0' || r > '9') && r != '.' && r != ',' {
			return rate[:index] + " " + rate[index:]
		}
	}
	return rate
}
