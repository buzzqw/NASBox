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
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"nasbox-go/internal/history"
)

// fileVersion is one previous version of a file, from this PC's history or
// from the NAS history (.sync-trash): one timeline per file, like the
// "version history" of hosted sync services.
type fileVersion struct {
	Source string // local | nas
	When   time.Time
	Size   int64
	Local  history.Version // Source == local
	Suffix string          // Source == nas
	Trash  string          // Source == nas: path inside .sync-trash
}

// parseVersionStamp delegates to the history parser, which also understands
// the microseconds of current stamps ("...-123456Z"). The old layout here
// matched only a literal "-000000Z", so most versions had no date at all.
func parseVersionStamp(value string) time.Time {
	stamp, err := history.VersionTime(strings.TrimPrefix(value, "-"))
	if err != nil {
		return time.Time{}
	}
	return stamp
}

// versionStampFull keeps seconds and the UTC offset, unlike the compact stamp
// used in the list, so two versions taken in the same minute stay distinct.
func versionStampFull(value time.Time) string {
	if value.IsZero() {
		return "—"
	}
	return value.Local().Format("02/01/2006 15:04:05 -07:00")
}

// sizeDetail adds the exact byte count when the human-readable form rounds it.
func sizeDetail(size int64) string {
	human := humanBytes(size)
	if strings.HasSuffix(human, " B") {
		return human
	}
	return fmt.Sprintf("%s (%d B)", human, size)
}

// versionDetail describes one version fully: where the file lived, when the
// copy was taken, how big it is and where its bytes are actually stored.
func versionDetail(item fileVersion, original string) string {
	source := tr("ui.versions.on_pc", nil)
	copyPath := item.Local.TrashPath
	id := item.Local.Timestamp
	path := item.Local.Original
	if item.Source == "nas" {
		source = tr("ui.versions.on_nas", nil)
		copyPath = ".sync-trash/" + strings.TrimPrefix(filepath.ToSlash(item.Trash), "/")
		id = item.Suffix
		path = original
	}
	return strings.Join([]string{
		fmt.Sprintf("%s: %s", tr("ui.versions.detail_path", nil), valueOrDash(path)),
		fmt.Sprintf("%s: %s", tr("ui.versions.detail_when", nil), versionStampFull(item.When)),
		fmt.Sprintf("%s: %s", tr("ui.versions.detail_size", nil), sizeDetail(item.Size)),
		fmt.Sprintf("%s: %s", tr("ui.versions.detail_source", nil), source),
		fmt.Sprintf("%s: %s", tr("ui.versions.detail_copy", nil), valueOrDash(copyPath)),
		fmt.Sprintf("%s: %s", tr("ui.versions.detail_id", nil), valueOrDash(id)),
	}, "\n")
}

// loadFileVersions merges local and NAS history for one path, newest first.
func (c *clientApp) loadFileVersions(original string) ([]fileVersion, error) {
	original = strings.Trim(strings.TrimSpace(original), "/")
	versions := []fileVersion{}
	local, err := history.FindByOriginal(original)
	if err != nil {
		return nil, err
	}
	for _, item := range local {
		versions = append(versions, fileVersion{Source: "local", When: parseVersionStamp(item.Timestamp), Size: item.Size, Local: item})
	}
	remoteVersions, remoteErr := c.client.HistoryList()
	for _, item := range remoteVersions {
		if item.Original == original {
			versions = append(versions, fileVersion{Source: "nas", When: parseVersionStamp(item.Suffix),
				Size: item.Size, Suffix: item.Suffix, Trash: item.TrashRel})
		}
	}
	sort.SliceStable(versions, func(i, j int) bool { return versions[i].When.After(versions[j].When) })
	if remoteErr != nil && len(versions) == 0 {
		return nil, remoteErr
	}
	return versions, nil
}

// showFileVersions opens the version history of one file.
func (c *clientApp) showFileVersions(original string) {
	original = strings.Trim(strings.TrimSpace(original), "/")
	if original == "" {
		return
	}
	status := widget.NewLabel(tr("ui.versions.loading", nil))
	status.Wrapping = fyne.TextWrapWord
	var versions []fileVersion
	selected := -1
	restore := widget.NewButtonWithIcon(tr("ui.versions.restore", nil), theme.ContentRedoIcon(), nil)
	restore.Importance = widget.HighImportance
	restore.Disable()
	// Reserve the full height from the start: Fyne does not re-layout a Border
	// when a label's text grows, so a one-line placeholder would be clipped
	// once the six detail lines arrive.
	detailHint := strings.Join([]string{
		tr("ui.versions.detail_none", nil), "", "", "", "", "", "", "",
	}, "\n")
	detail := widget.NewLabel(detailHint)
	detail.Wrapping = fyne.TextWrapWord
	detail.Importance = widget.LowImportance
	list := newRowList(func() int { return len(versions) }, 200, 340, func(id int, row *listRow) {
		if id >= len(versions) {
			return
		}
		item := versions[id]
		if item.Source == "nas" {
			row.icon.SetResource(theme.StorageIcon())
			row.name.SetText(tr("ui.versions.on_nas", nil))
		} else {
			row.icon.SetResource(theme.ComputerIcon())
			row.name.SetText(tr("ui.versions.on_pc", nil))
		}
		row.meta.SetText(fmt.Sprintf("%s · %s", versionStampFull(item.When), sizeDetail(item.Size)))
	})
	list.OnSelected = func(id widget.ListItemID) {
		selected = int(id)
		if selected >= 0 && selected < len(versions) {
			detail.SetText(versionDetail(versions[selected], original))
		}
		restore.Enable()
	}
	title := widget.NewLabelWithStyle(original, fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	title.Truncation = fyne.TextTruncateEllipsis
	note := widget.NewLabel(tr("ui.versions.note", nil))
	note.Wrapping = fyne.TextWrapWord
	note.Importance = widget.LowImportance
	content := container.NewBorder(container.NewVBox(title, note, status), detail, nil, nil, list)
	popup := dialog.NewCustom(tr("ui.versions.title", nil), tr("ui.action.cancel_short", nil),
		container.NewBorder(nil, restore, nil, nil, content), c.window)
	restore.OnTapped = func() {
		if selected < 0 || selected >= len(versions) {
			return
		}
		item := versions[selected]
		restore.Disable()
		status.SetText(tr("ui.versions.restoring", nil))
		go func() {
			err := c.restoreFileVersion(original, item)
			fyne.Do(func() {
				if err != nil {
					status.SetText(tr("ui.versions.failed", map[string]any{"detail": err.Error()}))
					restore.Enable()
					return
				}
				popup.Hide()
			})
		}()
	}
	popup.Resize(fyne.NewSize(720, 620))
	popup.Show()
	go func() {
		loaded, err := c.loadFileVersions(original)
		fyne.Do(func() {
			if err != nil {
				status.SetText(tr("ui.versions.failed", map[string]any{"detail": err.Error()}))
				return
			}
			versions = loaded
			list.Refresh()
			if selected >= len(versions) {
				selected = -1
				restore.Disable()
				detail.SetText(detailHint)
			} else if selected >= 0 {
				detail.SetText(versionDetail(versions[selected], original))
			}
			if len(versions) == 0 {
				status.SetText(tr("ui.versions.empty", nil))
			} else {
				status.SetText(tr("ui.versions.count", map[string]any{"count": len(versions)}))
			}
		})
	}()
}

// restoreFileVersion brings a version back without ever losing the current
// one: a local current file goes to the local history first; a NAS restore is
// done by the server (which keeps the replaced NAS file in its history) and
// reaches this PC with the next sync.
func (c *clientApp) restoreFileVersion(original string, item fileVersion) error {
	root := c.cfg.LocalRoot()
	switch item.Source {
	case "local":
		target := filepath.Join(root, filepath.FromSlash(original))
		if _, err := os.Lstat(target); err == nil {
			if err := history.MoveToTrash(root, original); err != nil {
				return err
			}
		}
		if err := history.Restore(item.Local, root); err != nil {
			return err
		}
	case "nas":
		target := filepath.Join(root, filepath.FromSlash(original))
		if _, err := os.Lstat(target); err == nil {
			// The file still exists: the server refuses to restore over it
			// (EXISTS). Fetch the old bytes onto the local file instead; the
			// next sync uploads them as a normal edit and the current NAS
			// version is kept in the NAS history.
			if err := history.MoveToTrash(root, original); err != nil {
				return err
			}
			if err := c.transfers.DownloadHistoryVersion(item.Trash, original); err != nil {
				return err
			}
			break
		}
		statuses, err := c.client.RestoreVersion(original, item.Suffix, c.store.DeviceID())
		if err != nil {
			return err
		}
		if statuses != "RESTORED" {
			return fmt.Errorf("%s", tr("ui.versions.server_status", map[string]any{"status": statuses}))
		}
	}
	c.appendAction(tr("ui.versions.done", map[string]any{"path": original}))
	go c.refreshHistory()
	c.runner.SyncNow()
	return nil
}

// promptFileVersions asks for a path (pre-filled) and opens its history.
func (c *clientApp) promptFileVersions(initial string) {
	entry := widget.NewEntry()
	entry.SetText(initial)
	entry.SetPlaceHolder(tr("ui.versions.path_hint", nil))
	dialog.ShowForm(tr("ui.versions.title", nil), tr("ui.versions.open", nil), tr("ui.action.cancel_short", nil),
		[]*widget.FormItem{widget.NewFormItem(tr("ui.versions.path", nil), entry)}, func(ok bool) {
			if ok {
				c.showFileVersions(entry.Text)
			}
		}, c.window)
}
