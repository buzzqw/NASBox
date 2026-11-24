package ui

import (
	"fmt"
	"path"
	"strconv"
	"strings"
	"time"
	"unicode"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

func itoa(value int) string { return strconv.Itoa(value) }

// plainTitle drops the leading emoji some status strings carry: the hero and
// the sidebar already show a coloured icon.
func plainTitle(text string) string {
	runes := []rune(strings.TrimSpace(text))
	start := 0
	for start < len(runes) && !unicode.IsLetter(runes[start]) && !unicode.IsDigit(runes[start]) {
		start++
	}
	return string(runes[start:])
}

// statusView is the home page. It answers, in this order: am I in sync? what
// is happening right now? what changed recently? is the NAS healthy?
func (c *clientApp) statusView() fyne.CanvasObject {
	c.pauseBtnStatus = plainButton(labelPause(c.runner.Paused()), theme.MediaPauseIcon(),
		tr("status.pause_tooltip", nil), widget.MediumImportance, c.onPause, c.setHint)
	c.detailsBtn = newHintButton(tr("ui.action.sync_short", nil), theme.ViewRefreshIcon(),
		tr("ui.action.details_hint", nil), widget.HighImportance, func() {
			if c.bannerGo != nil {
				c.bannerGo()
			}
		}, c.setHint)

	numberTracked, trackedTile := c.kpiTile(theme.DocumentIcon(), tr("assurance.stat_tracked", nil),
		func() { c.navigate(sectionFiles, 0) })
	numberConflicts, conflictsTile := c.kpiTile(theme.WarningIcon(), tr("assurance.stat_conflicts", nil),
		func() { c.navigate(sectionFiles, 1) })
	numberUndo, undoTile := c.kpiTile(theme.ContentUndoIcon(), tr("ui.stat.undo", nil),
		func() { c.navigate(sectionFiles, 2) })
	numberAudit, auditTile := c.kpiTile(theme.ConfirmIcon(), tr("overview.stat_audit", nil),
		func() { c.navigate(sectionSettings, 2) })
	c.statTracked, c.statConflicts, c.statUndo, c.statAudit = numberTracked, numberConflicts, numberUndo, numberAudit
	stats := container.NewGridWithColumns(4, trackedTile, conflictsTile, undoTile, auditTile)

	c.confirmDeleteBtn = plainButton(tr("ui.action.confirm_delete", nil), theme.ConfirmIcon(),
		tr("status.confirm_deletes_tooltip", nil), widget.DangerImportance, c.onConfirmDelete, c.setHint)
	c.confirmDeleteBtn.Hide()
	more := container.NewHBox(
		newHintButton(tr("ui.action.preview_short", nil), theme.VisibilityIcon(),
			tr("transfers.queue_note", nil), widget.LowImportance, c.onPreview, c.setHint),
		newHintButton(tr("ui.action.audit_short", nil), theme.ConfirmIcon(),
			tr("audit.run_now_tooltip", nil), widget.LowImportance, c.onAudit, c.setHint),
		newHintButton(tr("ui.action.undo_short", nil), theme.ContentUndoIcon(),
			tr("ui.action.undo_hint", nil), widget.LowImportance, c.onUndo, c.setHint),
		newHintButton(tr("ui.action.cancel", nil), theme.CancelIcon(),
			tr("status.cancel_tooltip", nil), widget.LowImportance, c.onCancel, c.setHint),
		layout.NewSpacer(),
		c.confirmDeleteBtn,
	)

	columns := container.NewGridWithColumns(2, c.buildRecentCard(), c.buildNASCard())
	c.homeBox = container.NewVBox(
		c.buildBanner(),
		c.buildTransferCard(),
		stats,
		columns,
		c.cardTitle(tr("ui.home.more_actions", nil)),
		more,
	)
	return container.NewPadded(c.homeBox)
}

// buildBanner is the hero: a large semantic status icon, title, reason and the
// suggested next step with its action button.
func (c *clientApp) buildBanner() fyne.CanvasObject {
	c.bannerTitle = widget.NewLabelWithStyle("", fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	c.bannerTitle.SizeName = theme.SizeNameSubHeadingText
	c.bannerReason = widget.NewLabel("")
	c.bannerReason.Wrapping = fyne.TextWrapWord
	c.bannerNext = widget.NewLabel("")
	c.bannerNext.Wrapping = fyne.TextWrapWord
	c.bannerNext.Importance = widget.LowImportance
	c.bannerBG = canvas.NewRectangle(c.bannerTint(theme.ColorNamePrimary))
	c.bannerBG.CornerRadius = 14
	c.heroCircle = canvas.NewCircle(c.tint(theme.ColorNamePrimary))
	c.heroIcon = widget.NewIcon(theme.InfoIcon())
	hero := container.NewStack(
		container.NewGridWrap(fyne.NewSize(56, 56), c.heroCircle),
		container.NewCenter(container.NewGridWrap(fyne.NewSize(30, 30), c.heroIcon)),
	)
	text := container.NewVBox(c.bannerTitle, c.bannerReason, c.bannerNext)
	action := container.NewVBox(layout.NewSpacer(), c.detailsBtn, c.pauseBtnStatus, layout.NewSpacer())
	body := container.NewBorder(nil, nil,
		container.NewPadded(container.NewCenter(hero)), container.NewPadded(action), text)
	return container.NewStack(c.bannerBG, container.NewPadded(body))
}

// heroStyle maps a status level to its icon and semantic colour.
func heroStyle(level statusLevel) (fyne.Resource, fyne.ThemeColorName) {
	switch level {
	case statusOK:
		return theme.ConfirmIcon(), theme.ColorNameSuccess
	case statusProgress:
		return theme.ViewRefreshIcon(), theme.ColorNamePrimary
	case statusAttention, statusPaused:
		if level == statusPaused {
			return theme.MediaPauseIcon(), theme.ColorNameWarning
		}
		return theme.WarningIcon(), theme.ColorNameWarning
	case statusBlocked, statusOffline:
		return theme.ErrorIcon(), theme.ColorNameError
	default:
		return theme.InfoIcon(), theme.ColorNamePrimary
	}
}

// buildTransferCard shows the live rsync progress; hidden while idle.
func (c *clientApp) buildTransferCard() fyne.CanvasObject {
	c.transferTitle = widget.NewLabelWithStyle(tr("ui.transfer.idle", nil), fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	c.transferPercent = widget.NewLabelWithStyle("", fyne.TextAlignTrailing, fyne.TextStyle{Bold: true})
	c.transferFile = widget.NewLabel("")
	c.transferFile.Truncation = fyne.TextTruncateEllipsis
	c.transferBar = widget.NewProgressBar()
	c.transferBar.TextFormatter = func() string { return "" }
	c.transferMeta = widget.NewLabel("")
	c.transferMeta.Importance = widget.LowImportance
	c.transferIcon = widget.NewIcon(theme.UploadIcon())
	card := c.surface(container.NewVBox(
		container.NewBorder(nil, nil, c.transferIcon, c.transferPercent, c.transferTitle),
		c.transferFile,
		c.transferBar,
		c.transferMeta,
	), theme.ColorNamePrimary)
	c.transferCard = container.NewVBox(card)
	c.transferCard.Hide()
	return c.transferCard
}

// setTransferCardVisible shows or hides the live card and re-lays out the
// home page: a container does not resize a child that becomes visible.
func (c *clientApp) setTransferCardVisible(visible bool) {
	if c.transferCard == nil || c.transferCard.Visible() == visible {
		return
	}
	if visible {
		c.transferCard.Show()
	} else {
		c.transferCard.Hide()
	}
	if c.homeBox != nil {
		c.homeBox.Refresh()
	}
}

// kpiTile is a clickable KPI card: icon, big number, caption.
func (c *clientApp) kpiTile(icon fyne.Resource, caption string, onTap func()) (*widget.Label, fyne.CanvasObject) {
	number := widget.NewLabelWithStyle("—", fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	number.SizeName = theme.SizeNameSubHeadingText
	label := widget.NewLabel(caption)
	label.Importance = widget.LowImportance
	label.Truncation = fyne.TextTruncateEllipsis
	content := container.NewBorder(nil, nil, nil, widget.NewIcon(theme.NewPrimaryThemedResource(icon)),
		container.NewVBox(number, label))
	return number, newTapCard(c, content, onTap)
}

func (c *clientApp) cardTitle(text string) fyne.CanvasObject {
	label := widget.NewLabelWithStyle(text, fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	return label
}

// buildRecentCard lists the latest files moved by NASBox, newest first.
func (c *clientApp) buildRecentCard() fyne.CanvasObject {
	c.recentBox = container.NewVBox()
	all := widget.NewButtonWithIcon(tr("ui.home.see_all", nil), theme.NavigateNextIcon(), func() {
		c.navigate(sectionActivity, 1)
	})
	all.Importance = widget.LowImportance
	c.renderRecent()
	return c.surface(container.NewBorder(
		container.NewBorder(nil, nil, c.cardTitle(tr("ui.home.recent_title", nil)), all),
		nil, nil, nil, c.recentBox,
	), "")
}

// buildNASCard summarises the NAS: host, disk usage, server and devices.
func (c *clientApp) buildNASCard() fyne.CanvasObject {
	c.homeNASLabel = widget.NewLabel(fmt.Sprintf("%s · %s", valueOrDash(c.cfg.String("nas_lan")),
		valueOrDash(c.cfg.String("remote_prefix"))))
	c.homeNASLabel.Truncation = fyne.TextTruncateEllipsis
	c.homeDiskLabel = widget.NewLabel(tr("status.diagnostics_loading", nil))
	c.homeDiskLabel.Importance = widget.LowImportance
	c.homeDiskBar = widget.NewProgressBar()
	c.homeDiskBar.TextFormatter = func() string { return "" }
	c.homeDevicesLabel = widget.NewLabel("")
	c.homeDevicesLabel.Importance = widget.LowImportance
	c.homeTrashLabel = widget.NewLabel("")
	c.homeTrashLabel.Importance = widget.LowImportance
	c.homeTrashLabel.Hide()
	open := widget.NewButtonWithIcon(tr("ui.home.nas_details", nil), theme.NavigateNextIcon(), func() {
		c.navigate(sectionNAS, 0)
	})
	open.Importance = widget.LowImportance
	return c.surface(container.NewVBox(
		container.NewBorder(nil, nil, c.cardTitle(tr("ui.home.nas_title", nil)), open),
		c.homeNASLabel,
		c.homeDiskBar,
		c.homeDiskLabel,
		c.homeDevicesLabel,
		c.homeTrashLabel,
	), "")
}

// ---------------------------------------------------------------------------
// tapCard: a surface that behaves like a button (hover + tap).
// ---------------------------------------------------------------------------

type tapCard struct {
	widget.BaseWidget
	plain   bool // list row: no border, hover background only
	app     *clientApp
	content fyne.CanvasObject
	onTap   func()
	bg      *canvas.Rectangle
	hovered bool
}

// newTapRow is a borderless clickable list row.
func newTapRow(c *clientApp, content fyne.CanvasObject, onTap func()) *tapCard {
	t := newTapCard(c, content, onTap)
	t.plain = true
	t.bg.FillColor = colorTransparent
	t.bg.CornerRadius = 8
	return t
}

func newTapCard(c *clientApp, content fyne.CanvasObject, onTap func()) *tapCard {
	t := &tapCard{app: c, content: content, onTap: onTap}
	t.bg = canvas.NewRectangle(c.tint(colorNameSurface))
	t.bg.CornerRadius = 12
	t.bg.StrokeColor = c.tint(theme.ColorNameSeparator)
	t.ExtendBaseWidget(t)
	return t
}

func (t *tapCard) Tapped(*fyne.PointEvent) {
	if t.onTap != nil {
		t.onTap()
	}
}
func (t *tapCard) MouseIn(*desktop.MouseEvent)    { t.hovered = true; t.Refresh() }
func (t *tapCard) MouseMoved(*desktop.MouseEvent) {}
func (t *tapCard) MouseOut()                      { t.hovered = false; t.Refresh() }
func (t *tapCard) Cursor() desktop.Cursor         { return desktop.PointerCursor }

func (t *tapCard) CreateRenderer() fyne.WidgetRenderer {
	padded := container.NewPadded(t.content)
	r := &tapCardRenderer{t: t, objects: []fyne.CanvasObject{t.bg, padded}, padded: padded}
	r.Layout(t.Size())
	return r
}

type tapCardRenderer struct {
	t       *tapCard
	objects []fyne.CanvasObject
	padded  fyne.CanvasObject
}

func (r *tapCardRenderer) Layout(size fyne.Size) {
	// A stroked, rounded rectangle smaller than its radius cannot be drawn
	// by the software painter (used for captures and tests).
	if r.t.plain || size.Width < 2*r.t.bg.CornerRadius+2 || size.Height < 2*r.t.bg.CornerRadius+2 {
		r.t.bg.StrokeWidth = 0
	} else {
		r.t.bg.StrokeWidth = 1
	}
	r.t.bg.Resize(size)
	r.padded.Resize(size)
}
func (r *tapCardRenderer) MinSize() fyne.Size           { return r.padded.MinSize() }
func (r *tapCardRenderer) Objects() []fyne.CanvasObject { return r.objects }
func (r *tapCardRenderer) Destroy()                     {}
func (r *tapCardRenderer) Refresh() {
	r.Layout(r.t.Size())
	switch {
	case r.t.plain && r.t.hovered:
		r.t.bg.FillColor = r.t.app.tint(theme.ColorNameHover)
	case r.t.plain:
		r.t.bg.FillColor = colorTransparent
	case r.t.hovered:
		r.t.bg.StrokeColor = r.t.app.tint(theme.ColorNamePrimary)
	default:
		r.t.bg.StrokeColor = r.t.app.tint(theme.ColorNameSeparator)
	}
	r.t.bg.Refresh()
	r.padded.Refresh()
}

// ---------------------------------------------------------------------------
// Relative time ("2 min fa") for recent items and the last sync.
// ---------------------------------------------------------------------------

func relativeTime(at time.Time, now time.Time) string {
	if at.IsZero() {
		return "—"
	}
	delta := now.Sub(at)
	switch {
	case delta < 45*time.Second:
		return tr("ui.time.now", nil)
	case delta < time.Hour:
		return tr("ui.time.minutes", map[string]any{"n": int(delta.Minutes() + 0.5)})
	case delta < 24*time.Hour && at.Day() == now.Day():
		return at.Format("15:04")
	case delta < 48*time.Hour:
		return tr("ui.time.yesterday", map[string]any{"time": at.Format("15:04")})
	default:
		return at.Format("02/01 15:04")
	}
}

func splitPath(relative string) (string, string) {
	dir, name := path.Split(relative)
	if dir == "" {
		return name, "NASBox"
	}
	return name, path.Clean(dir)
}
