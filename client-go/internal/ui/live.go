package ui

import (
	"fmt"
	"os/exec"
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

// addRecent records a transferred file (newest first) and persists it.
func (c *clientApp) addRecent(item recentItem) {
	if c.logger != nil {
		action := map[string]string{"up": "UPLOAD", "down": "DOWNLOAD"}[item.Kind]
		if action != "" {
			_ = c.logger.Event(action, item.Path, "")
		}
	}
	fyne.Do(func() {
		c.recent = append([]recentItem{item}, c.recent...)
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

// renderRecent redraws the home card (at most 6 rows).
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
	for index, item := range c.recent {
		if index >= 5 {
			break
		}
		name, folder := splitPath(item.Path)
		title := widget.NewLabel(name)
		title.Truncation = fyne.TextTruncateEllipsis
		sub := widget.NewLabel(recentVerb(item.Kind) + " · " + folder)
		sub.Importance = widget.LowImportance
		sub.SizeName = theme.SizeNameCaptionText
		sub.Truncation = fyne.TextTruncateEllipsis
		when := widget.NewLabel(relativeTime(item.At, now))
		when.Importance = widget.LowImportance
		when.SizeName = theme.SizeNameCaptionText
		target := item.Path
		row := newTapRow(c, container.NewBorder(nil, nil, widget.NewIcon(recentIcon(item.Kind)), when,
			container.NewVBox(title, sub)), func() { c.openContainingFolder(target) })
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
	_ = exec.Command("xdg-open", dir).Start()
}

// ---------------------------------------------------------------------------
// Live progress from rsync
// ---------------------------------------------------------------------------

// installTransferObservers connects rsync progress to the UI.
func (c *clientApp) installTransferObservers() {
	if c.transfers == nil {
		return
	}
	c.transfers.SetObservers(c.onTransferProgress, func(event transfer.FileEvent) {
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
	c.progressMu.Unlock()
	if throttled {
		return
	}
	fyne.Do(func() { c.renderProgress(progress) })
}

func (c *clientApp) renderProgress(progress transfer.Progress) {
	if c.transferCard == nil {
		return
	}
	if progress.Finished {
		if progress.Err == nil {
			c.lastSyncAt = time.Now()
			c.updateLastSync()
		}
		c.transferBar.SetValue(1)
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
	direction := tr("ui.transfer.downloading", nil)
	icon := theme.DownloadIcon()
	if progress.Upload {
		direction = tr("ui.transfer.uploading", nil)
		icon = theme.UploadIcon()
	}
	total := progress.Total
	current := progress.Done + 1
	if total > 0 && current > total {
		current = total
	}
	c.transferIcon.SetResource(icon)
	c.transferTitle.SetText(tr("ui.transfer.title", map[string]any{
		"direction": direction, "current": current, "total": total,
	}))
	c.transferPercent.SetText(fmt.Sprintf("%d%%", progress.Percent))
	if progress.File != "" {
		c.transferFile.SetText(progress.File)
	}
	c.transferBar.SetValue(float64(progress.Percent) / 100)
	c.transferMeta.SetText(progressMeta(progress))
	c.setTransferCardVisible(true)
	if c.transferNowLabel != nil {
		c.transferNowLabel.SetText(c.transferTitle.Text + " · " + c.transferPercent.Text)
	}
	c.refreshTray()
}

func progressMeta(progress transfer.Progress) string {
	parts := []string{}
	if progress.Rate != "" {
		parts = append(parts, formatRate(progress.Rate))
	}
	if progress.ETA != "" && progress.ETA != "0:00" {
		parts = append(parts, tr("ui.transfer.eta", map[string]any{"eta": progress.ETA}))
	}
	if progress.Bytes > 0 {
		parts = append(parts, humanBytes(progress.Bytes))
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
	c.transferNowLabel = widget.NewLabelWithStyle(tr("ui.transfer.idle", nil), fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	c.transferHistory = newRowList(
		func() int { return len(c.recent) }, 260, 220,
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
		c.sectionTitle(tr("main_window.tab_transfers", nil), tr("ui.activity.transfers_note", nil)),
		c.surface(c.transferNowLabel, ""),
	)
	return container.NewBorder(header, nil, nil, nil, c.transferHistory)
}

func (c *clientApp) refreshTransfersPane() {
	if c.transferHistory != nil {
		c.transferHistory.Refresh()
	}
	if c.transferNowLabel != nil {
		if _, active := c.transferActive(); !active {
			c.transferNowLabel.SetText(tr("ui.transfer.idle", nil))
		}
	}
}

// formatRate turns rsync's "21.8MB/s" into "21.8 MB/s".
func formatRate(rate string) string {
	for index, r := range rate {
		if (r < '0' || r > '9') && r != '.' && r != ',' {
			return rate[:index] + " " + rate[index:]
		}
	}
	return rate
}
