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

var versionStampLayouts = []string{"2006-01-02--15-04-05-000000Z", "2006-01-02--15-04-05"}

func parseVersionStamp(value string) time.Time {
	value = strings.TrimPrefix(value, "-")
	for _, layout := range versionStampLayouts {
		location := time.Local
		if strings.HasSuffix(layout, "Z") {
			location = time.UTC
		}
		if parsed, err := time.ParseInLocation(layout, value, location); err == nil {
			return parsed
		}
	}
	return time.Time{}
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
	list := newRowList(func() int { return len(versions) }, 220, 200, func(id int, row *listRow) {
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
		when := "—"
		if !item.When.IsZero() {
			when = item.When.Local().Format("02/01/2006 15:04")
		}
		row.meta.SetText(fmt.Sprintf("%s · %s", when, humanBytes(item.Size)))
	})
	list.OnSelected = func(id widget.ListItemID) {
		selected = int(id)
		restore.Enable()
	}
	title := widget.NewLabelWithStyle(original, fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	title.Truncation = fyne.TextTruncateEllipsis
	note := widget.NewLabel(tr("ui.versions.note", nil))
	note.Wrapping = fyne.TextWrapWord
	note.Importance = widget.LowImportance
	content := container.NewBorder(container.NewVBox(title, note, status), nil, nil, nil, list)
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
	popup.Resize(fyne.NewSize(640, 460))
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
