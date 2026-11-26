package ui

import (
	"image/color"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"nasbox-go/internal/version"
)

// ---------------------------------------------------------------------------
// Navigation item: icon + label, hover and selected states, optional badge.
// A dedicated widget (not a restyled Button) so the whole row is clickable and
// the label is never clipped or shifted.
// ---------------------------------------------------------------------------

type navButton struct {
	widget.BaseWidget
	app      *clientApp
	section  sectionID
	label    string
	icon     fyne.Resource
	selected bool
	hovered  bool
	badge    string

	bg        *canvas.Rectangle
	accent    *canvas.Rectangle
	text      *canvas.Text
	iconView  *widget.Icon
	badgeBG   *canvas.Rectangle
	badgeText *canvas.Text
}

func (c *clientApp) newNavButton(id sectionID, label string, icon fyne.Resource) *navButton {
	n := &navButton{app: c, section: id, label: label, icon: icon}
	n.bg = canvas.NewRectangle(colorTransparent)
	n.bg.CornerRadius = 8
	n.accent = canvas.NewRectangle(colorTransparent)
	n.accent.CornerRadius = 2
	n.text = canvas.NewText(label, c.tint(theme.ColorNameForeground))
	n.text.TextSize = 14
	n.iconView = widget.NewIcon(icon)
	n.badgeBG = canvas.NewRectangle(colorTransparent)
	n.badgeBG.CornerRadius = 9
	n.badgeText = canvas.NewText("", color.White)
	n.badgeText.TextSize = 11
	n.badgeText.TextStyle = fyne.TextStyle{Bold: true}
	n.badgeText.Alignment = fyne.TextAlignCenter
	n.ExtendBaseWidget(n)
	return n
}

func (n *navButton) Tapped(*fyne.PointEvent) {
	if n.app != nil && n.app.setHint != nil {
		n.app.setHint("")
	}
	if n.app != nil {
		n.app.showSection(n.section)
	}
}

func (n *navButton) MouseIn(event *desktop.MouseEvent) {
	n.hovered = true
	n.Refresh()
	if n.app != nil && n.app.setHint != nil {
		tipPointer = event.AbsolutePosition
		n.app.setHint(tr("ui.tip.nav_"+navKey(n.section), nil))
	}
}
func (n *navButton) MouseMoved(event *desktop.MouseEvent) { tipPointer = event.AbsolutePosition }
func (n *navButton) MouseOut() {
	n.hovered = false
	n.Refresh()
	if n.app != nil && n.app.setHint != nil {
		n.app.setHint("")
	}
}

func navKey(id sectionID) string {
	switch id {
	case sectionActivity:
		return "activity"
	case sectionFiles:
		return "files"
	case sectionNAS:
		return "nas"
	case sectionSettings:
		return "settings"
	}
	return "status"
}
func (n *navButton) Cursor() desktop.Cursor { return desktop.PointerCursor }

func (n *navButton) setSelected(selected bool) {
	n.selected = selected
	n.Refresh()
}

func (n *navButton) setBadge(text string) {
	n.badge = text
	n.Refresh()
}

func (n *navButton) CreateRenderer() fyne.WidgetRenderer {
	r := &navRenderer{n: n}
	r.Refresh()
	return r
}

type navRenderer struct{ n *navButton }

func (r *navRenderer) Objects() []fyne.CanvasObject {
	n := r.n
	return []fyne.CanvasObject{n.bg, n.accent, n.iconView, n.text, n.badgeBG, n.badgeText}
}

func (r *navRenderer) MinSize() fyne.Size {
	return fyne.NewSize(180, 40)
}

func (r *navRenderer) Layout(size fyne.Size) {
	n := r.n
	n.bg.Resize(size)
	n.bg.Move(fyne.NewPos(0, 0))
	n.accent.Resize(fyne.NewSize(3, nonNegative(size.Height-16)))
	n.accent.Move(fyne.NewPos(0, 8))
	icon := float32(20)
	n.iconView.Resize(fyne.NewSize(icon, icon))
	n.iconView.Move(fyne.NewPos(14, (size.Height-icon)/2))
	textSize := n.text.MinSize()
	n.text.Move(fyne.NewPos(44, (size.Height-textSize.Height)/2))
	n.text.Resize(fyne.NewSize(nonNegative(size.Width-44-36), textSize.Height))
	badgeWidth := float32(0)
	if n.badge != "" {
		badgeWidth = n.badgeText.MinSize().Width + 12
		if badgeWidth < 20 {
			badgeWidth = 20
		}
	}
	n.badgeBG.Resize(fyne.NewSize(badgeWidth, 18))
	n.badgeBG.Move(fyne.NewPos(size.Width-badgeWidth-10, (size.Height-18)/2))
	n.badgeText.Resize(fyne.NewSize(badgeWidth, 18))
	n.badgeText.Move(fyne.NewPos(size.Width-badgeWidth-10, (size.Height-18)/2+1))
}

func nonNegative(value float32) float32 {
	if value < 0 {
		return 0
	}
	return value
}

func (r *navRenderer) Refresh() {
	n := r.n
	c := n.app
	switch {
	case n.selected:
		n.bg.FillColor = c.tint(theme.ColorNameSelection)
		n.accent.FillColor = c.tint(theme.ColorNamePrimary)
		n.text.Color = c.tint(theme.ColorNamePrimary)
		n.text.TextStyle = fyne.TextStyle{Bold: true}
		n.iconView.SetResource(theme.NewPrimaryThemedResource(n.icon))
	case n.hovered:
		n.bg.FillColor = c.tint(theme.ColorNameHover)
		n.accent.FillColor = colorTransparent
		n.text.Color = c.tint(theme.ColorNameForeground)
		n.text.TextStyle = fyne.TextStyle{}
		n.iconView.SetResource(n.icon)
	default:
		n.bg.FillColor = colorTransparent
		n.accent.FillColor = colorTransparent
		n.text.Color = c.tint(theme.ColorNameForeground)
		n.text.TextStyle = fyne.TextStyle{}
		n.iconView.SetResource(n.icon)
	}
	n.text.Text = n.label
	n.badgeText.Text = n.badge
	if n.badge != "" {
		n.badgeBG.FillColor = c.tint(theme.ColorNameError)
	} else {
		n.badgeBG.FillColor = colorTransparent
	}
	r.Layout(n.Size())
	canvas.Refresh(n)
}

func (r *navRenderer) Destroy() {}

// ---------------------------------------------------------------------------
// Sidebar: brand, navigation, live status card.
// ---------------------------------------------------------------------------

func (c *clientApp) buildSidebar() fyne.CanvasObject {
	spacer := canvas.NewRectangle(colorTransparent)
	spacer.SetMinSize(fyne.NewSize(224, 1))
	bg := canvas.NewRectangle(c.tint(colorNameSidebar))
	edge := canvas.NewRectangle(c.tint(theme.ColorNameSeparator))
	edge.SetMinSize(fyne.NewSize(1, 1))

	logo := canvas.NewImageFromResource(trayIcon("synced"))
	logo.FillMode = canvas.ImageFillContain
	logo.SetMinSize(fyne.NewSize(30, 30))
	name := canvas.NewText(version.AppName, c.tint(theme.ColorNameForeground))
	name.TextSize = 17
	name.TextStyle = fyne.TextStyle{Bold: true}
	tagline := canvas.NewText(tr("ui.brand.subtitle", nil), c.tint(colorNameMuted))
	tagline.TextSize = 11
	brand := container.NewHBox(logo, container.NewVBox(name, tagline))

	items := container.NewVBox(
		c.newNav(sectionStatus, tr("main_window.tab_status", nil), theme.HomeIcon()),
		c.newNav(sectionActivity, tr("ui.nav.activity", nil), theme.HistoryIcon()),
		c.newNav(sectionFiles, tr("ui.nav.files", nil), theme.FolderOpenIcon()),
		c.newNav(sectionNAS, tr("ui.nav.nas", nil), theme.StorageIcon()),
		c.newNav(sectionSettings, tr("main_window.tab_settings", nil), theme.SettingsIcon()),
	)

	c.navBox = items
	c.advancedToggle = widget.NewButtonWithIcon("", theme.MenuDropDownIcon(), func() {
		c.setAdvanced(!c.advancedOn, true)
	})
	c.advancedToggle.Importance = widget.LowImportance
	c.advancedToggle.Alignment = widget.ButtonAlignLeading

	body := container.NewBorder(
		container.NewVBox(container.NewPadded(brand), widget.NewSeparator()),
		container.NewVBox(c.advancedToggle, c.buildSidebarStatus()),
		nil, nil,
		container.NewVBox(items, layout.NewSpacer()),
	)
	return container.NewStack(spacer, bg,
		container.NewBorder(nil, nil, nil, edge, container.NewPadded(body)))
}

func (c *clientApp) newNav(id sectionID, label string, icon fyne.Resource) fyne.CanvasObject {
	item := c.newNavButton(id, label, icon)
	c.navs[id] = item
	return item
}

// buildSidebarStatus is the small always-visible status card at the bottom
// of the sidebar (dot + state + last sync), like a sync client's footer.
func (c *clientApp) buildSidebarStatus() fyne.CanvasObject {
	c.statusDot = canvas.NewCircle(c.tint(theme.ColorNameDisabled))
	dot := container.NewGridWrap(fyne.NewSize(10, 10), c.statusDot)
	c.headerStatus = widget.NewLabelWithStyle("", fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	c.headerStatus.Truncation = fyne.TextTruncateEllipsis
	lastSyncText := tr("ui.status.never_synced", nil)
	if c.ghost {
		lastSyncText = lt("Modalità consultazione", "Consultation mode")
	}
	c.lastSyncLabel = widget.NewLabel(lastSyncText)
	c.lastSyncLabel.Importance = widget.LowImportance
	c.lastSyncLabel.SizeName = theme.SizeNameCaptionText
	c.lastSyncLabel.Truncation = fyne.TextTruncateEllipsis
	c.hintLabel = widget.NewLabel("")
	c.hintLabel.Importance = widget.LowImportance
	c.hintLabel.SizeName = theme.SizeNameCaptionText
	c.hintLabel.Wrapping = fyne.TextWrapWord
	versionLabel := widget.NewLabel(version.AppName + " " + version.AppVersion)
	versionLabel.Importance = widget.LowImportance
	versionLabel.SizeName = theme.SizeNameCaptionText
	card := c.surface(container.NewVBox(
		container.NewBorder(nil, nil, container.NewCenter(dot), nil, c.headerStatus),
		c.lastSyncLabel,
	), "")
	c.hintLabel.Hide() // tooltips replaced the footer hint line
	return container.NewVBox(card, versionLabel)
}

// ---------------------------------------------------------------------------
// Page header: big title + subtitle on the left, global actions on the right.
// ---------------------------------------------------------------------------

func (c *clientApp) buildHeader() fyne.CanvasObject {
	c.headerTitle = widget.NewLabelWithStyle(tr("main_window.tab_status", nil),
		fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	c.headerTitle.SizeName = theme.SizeNameSubHeadingText
	c.headerTitle.Truncation = fyne.TextTruncateEllipsis
	syncButton := newHintButton(tr("ui.action.sync_short", nil), theme.ViewRefreshIcon(),
		tr("status.sync_now_tooltip", nil), widget.HighImportance, c.onSync, c.setHint)
	c.pauseBtn = plainButton(labelPause(false), theme.MediaPauseIcon(),
		tr("status.pause_tooltip", nil), widget.MediumImportance, c.onPause, c.setHint)
	c.pauseBtn.SetText(labelPause(c.runner != nil && (c.runner.IsPaused() || c.pausedForNow())))
	searchButton := newHintButton(lt("Cerca", "Search"), theme.SearchIcon(),
		lt("Cerca un file tra tutti quelli sincronizzati (Ctrl+F)", "Search all synced files (Ctrl+F)"),
		widget.LowImportance, c.focusSearch, c.setHint)
	folderButton := newHintButton(lt("Apri cartella", "Open folder"), theme.FolderOpenIcon(),
		lt("Apri la cartella NASBox sul tuo PC", "Open the NASBox folder on your PC"),
		widget.LowImportance, c.openLocalFolder, c.setHint)
	actions := container.NewHBox(searchButton, folderButton, c.pauseBtn, syncButton)
	if c.ghost {
		// Nothing is synced in the background, so these controls would be dead.
		syncButton.Hide()
		c.pauseBtn.Hide()
	}
	line := canvas.NewRectangle(c.tint(theme.ColorNameSeparator))
	line.SetMinSize(fyne.NewSize(1, 1))
	return container.NewVBox(
		container.NewPadded(container.NewBorder(nil, nil, nil, actions, c.headerTitle)),
		line,
	)
}

func labelPause(paused bool) string {
	if paused {
		return tr("status.resume_btn", nil)
	}
	return tr("status.pause_btn", nil)
}

// sectionSubtitle explains each area in one line, under the page title.
func (c *clientApp) sectionTitleText(id sectionID) string {
	return c.sectionLabel(id)
}

// updateNavBadges shows the conflict count on "File".
func (c *clientApp) updateNavBadges() {
	if c.navs == nil {
		return
	}
	if item, ok := c.navs[sectionFiles]; ok {
		badge := ""
		if count := len(c.confGroups); count > 0 {
			badge = itoa(count)
		}
		item.setBadge(badge)
	}
}

// setAdvanced shows or hides the technical areas (Activity: queue and log;
// NAS: monitor, devices, storage) and the counters/tools on the home page. The
// simple view keeps what people use every day: Home, Files and Settings.
func (c *clientApp) setAdvanced(on, persist bool) {
	c.advancedOn = on
	if persist {
		_ = c.cfg.Set("ui_advanced", on, true)
	}
	for _, id := range []sectionID{sectionActivity, sectionNAS} {
		if item, ok := c.navs[id]; ok {
			if on {
				item.Show()
			} else {
				item.Hide()
			}
		}
	}
	for _, object := range c.advancedHome {
		if on {
			object.Show()
		} else {
			object.Hide()
		}
	}
	if c.advancedToggle != nil {
		if on {
			c.advancedToggle.SetText(lt("Strumenti avanzati", "Advanced tools"))
			c.advancedToggle.SetIcon(theme.MenuDropUpIcon())
		} else {
			c.advancedToggle.SetText(lt("Strumenti avanzati", "Advanced tools"))
			c.advancedToggle.SetIcon(theme.MenuDropDownIcon())
		}
	}
	c.applySettingsTabs()
	if c.uiReady {
		if c.navBox != nil {
			c.navBox.Refresh() // a container does not re-layout children shown later
		}
		if c.homeBox != nil {
			c.homeBox.Refresh()
		}
	}
	if !on && (c.active == sectionActivity || c.active == sectionNAS) {
		c.showSection(sectionStatus)
	}
}

func isAdvancedSection(id sectionID) bool { return id == sectionActivity || id == sectionNAS }

// simpleSettingsTabs are the positions (in the full list) shown in the simple
// view: General, Folders and mirrors, About. Safety, Integrity and Advanced
// belong to the advanced tools.
var simpleSettingsTabs = map[int]bool{0: true, 3: true, 5: true}

func (c *clientApp) settingsTabVisible(index int) bool {
	return c.advancedOn || simpleSettingsTabs[index]
}

func (c *clientApp) applySettingsTabs() {
	if c.settingsTabs == nil || len(c.settingsAll) == 0 {
		return
	}
	items := make([]*container.TabItem, 0, len(c.settingsAll))
	for index, item := range c.settingsAll {
		if c.settingsTabVisible(index) {
			items = append(items, item)
		}
	}
	selected := c.settingsTabs.Selected()
	if !c.uiReady || c.active != sectionSettings {
		// Not on screen: just change the list. It is drawn when the section is
		// shown (showSection refreshes the content), so redrawing every settings
		// pane now would only cost time.
		c.settingsTabs.Items = items
		if !containsTab(items, selected) && len(items) > 0 {
			c.settingsTabs.SelectIndex(0)
		}
		return
	}
	c.settingsTabs.SetItems(items)
	for index, item := range items {
		if item == selected {
			c.settingsTabs.SelectIndex(index)
		}
	}
}

func containsTab(items []*container.TabItem, wanted *container.TabItem) bool {
	for _, item := range items {
		if item == wanted {
			return true
		}
	}
	return false
}
