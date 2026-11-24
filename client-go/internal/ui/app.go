// Package ui is the native Fyne client. It supports both X11 and Wayland.
package ui

import (
	"context"
	_ "embed"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"nasbox-go/internal/config"
	"nasbox-go/internal/conflicts"
	"nasbox-go/internal/deletesafety"
	"nasbox-go/internal/diagnostics"
	"nasbox-go/internal/engine"
	"nasbox-go/internal/history"
	"nasbox-go/internal/i18n"
	"nasbox-go/internal/integrity"
	"nasbox-go/internal/mirror"
	"nasbox-go/internal/remote"
	"nasbox-go/internal/selective"
	"nasbox-go/internal/state"
	"nasbox-go/internal/transfer"
	"nasbox-go/internal/update"
	"nasbox-go/internal/version"
)

// sectionID identifies one of the five top-level navigational areas. The old
// ten flat tabs are grouped underneath these areas as secondary tabs.
type sectionID int

const (
	sectionStatus sectionID = iota
	sectionActivity
	sectionFiles
	sectionNAS
	sectionSettings
)

// statusLevel drives the semantic colour and the suggested next action of the
// overview banner.
type statusLevel int

const (
	statusStarting statusLevel = iota
	statusOK
	statusProgress
	statusAttention
	statusBlocked
	statusOffline
	statusPaused
)

type navButton struct {
	Button     *widget.Button
	bg         *canvas.Rectangle
	accent     *canvas.Rectangle
	buttonView fyne.CanvasObject
	label      string
	section    sectionID
}

type clientApp struct {
	cfg       *config.Config
	store     *state.Store
	client    *remote.Client
	transfers *transfer.Transfer
	runner    *engine.Runner
	cancel    context.CancelFunc

	uiTheme    *nasTheme
	fyneApp    fyne.App
	window     fyne.Window
	setHint    func(string)
	syncActive atomic.Bool
	content    *fyne.Container
	views      map[sectionID]fyne.CanvasObject
	navs       map[sectionID]*navButton
	active     sectionID

	headerTitle  *widget.Label
	headerStatus *widget.Label
	hintLabel    *widget.Label
	eventLabel   *widget.Label

	bannerTitle  *widget.Label
	bannerReason *widget.Label
	bannerNext   *widget.Label
	bannerBG     *canvas.Rectangle
	bannerGo     func()
	level        statusLevel

	statTracked   *widget.Label
	statConflicts *widget.Label
	statUndo      *widget.Label
	statAudit     *widget.Label

	pauseBtn       *widget.Button
	pauseBtnStatus *widget.Button
	detailsBtn     *hintButton
	logLines       []string
	transferLines  []string
	activityLines  []string
	logList        *widget.List
	transferList   *widget.List
	activityList   *widget.List
	confGroups     []conflicts.Group
	confList       *widget.List
	confChoices    []string
	confChoiceList *widget.List
	confDetail     *widget.Label
	versions       []history.Version
	histList       *widget.List
	histDetail     *widget.Label
	historyFilter  *widget.Entry

	browsePath          string
	browsePathLabel     *widget.Label
	breadcrumbs         *container.Scroll
	browseSelectedLabel *widget.Label
	downloadPathEntry   *widget.Entry
	browseEntries       []remote.BrowseEntry
	browseList          *widget.List
	browseDeleteBtn     *widget.Button
	browseRenameBtn     *widget.Button
	browseDownloadBtn   *widget.Button
	browseUpBtn         *widget.Button
	mirrorLabel         *widget.Label
	selectiveDirs       []string
	selectiveList       *widget.List
	nasLabel            *widget.Label
	nasUpdatedLabel     *widget.Label
	diskValue           *widget.Label
	diskBar             *widget.ProgressBar
	loadValue           *widget.Label
	memoryValue         *widget.Label
	lockValue           *widget.Label
	queueValue          *widget.Label
	devicesLabel        *widget.Label
	deviceList          *widget.List
	deviceEntries       []deviceEntry
	selectedDevice      string
	selectedDeviceLabel *widget.Label
	monitorDevicesLabel *widget.Label

	resolveBtn  *widget.Button
	keepBothBtn *widget.Button
	restoreBtn  *widget.Button

	activityTabs *container.AppTabs
	fileTabs     *container.AppTabs
	nasTabs      *container.AppTabs
	settingsTabs *container.AppTabs

	selectedConflict string
	selectedGroup    string
	selectedVersion  string
	selectedBrowse   string
}

// Run starts the native client. It blocks until the window/tray exits.
func Run(cfg *config.Config, store *state.Store, loggers ...*diagnostics.Logger) {
	selectBackend(cfg)
	detectLanguage(cfg)
	fyneApp := app.NewWithID("it.nasbox.client")
	window := fyneApp.NewWindow(fmt.Sprintf("%s %s", version.AppName, version.AppVersion))
	window.Resize(fyne.NewSize(1120, 760))

	client := remote.New(cfg)
	transfers := transfer.New(cfg, client)
	ctx, cancel := context.WithCancel(context.Background())
	c := &clientApp{
		cfg: cfg, store: store, client: client, transfers: transfers,
		cancel:  cancel,
		runner:  engine.NewRunner(cfg, store, client, transfers, loggers...),
		fyneApp: fyneApp,
		window:  window,
		uiTheme: newNAStheme(variantFrom(cfg.String("go_theme"))),
		level:   statusStarting,
	}
	c.setTrayIcon("unconfigured")
	c.bannerGo = func() { c.onSync() }
	fyneApp.Settings().SetTheme(c.uiTheme)

	c.setHint = c.setHintText
	window.SetContent(c.buildRoot())
	c.showSection(sectionStatus)
	// Useful for support/diagnostics: open directly on a section.
	switch strings.ToLower(os.Getenv("NASBOX_START_SECTION")) {
	case "activity":
		c.showSection(sectionActivity)
	case "files":
		c.showSection(sectionFiles)
	case "nas":
		c.showSection(sectionNAS)
	case "settings":
		c.showSection(sectionSettings)
	}
	window.SetCloseIntercept(func() { window.Hide() })
	c.installTray()

	c.applyStatus(statusStarting, tr("status.sync_state_starting", nil))
	go c.consumeEvents()
	c.animateTray(func() bool { return c.syncActive.Load() })
	go func() { _ = c.runner.Run(ctx) }()
	go func() {
		values, err := c.client.PrintConfig()
		if err != nil {
			c.applyStatus(statusOffline, tr("status.diagnostics_offline", nil))
			return
		}
		fyne.Do(func() {
			c.nasLabel.SetText(fmt.Sprintf(tr("ui.nas.config", nil),
				c.cfg.String("nas_lan"), values["VERSION"], values["REPOSITORY_ID"]))
		})
	}()
	go c.refreshConflicts()
	go c.refreshHistory()
	go c.refreshBrowse()
	go c.refreshSelective()
	c.refreshMirrorLabel()
	c.refreshStats()
	go c.refreshMonitor()
	c.appendLog(tr("main_window.brand_tagline", nil) + " — " + version.AppVersion)
	window.ShowAndRun()
	c.cancel()
}

// detectLanguage honours the configured language; "auto" follows the session
// locale, falling back to Italian as in the legacy client.
func detectLanguage(cfg *config.Config) {
	switch strings.ToLower(cfg.String("language")) {
	case "it", "en":
		i18n.Language = strings.ToLower(cfg.String("language"))
	default:
		locale := strings.ToLower(os.Getenv("LANG"))
		if strings.HasPrefix(locale, "en") {
			i18n.Language = "en"
		} else {
			i18n.Language = "it"
		}
	}
}

// displayBackend reports the effective display backend for the About pane.
func displayBackend(cfg *config.Config) string {
	choice := strings.ToLower(cfg.String("go_display_backend"))
	if choice == "" || choice == "auto" {
		if os.Getenv("WAYLAND_DISPLAY") != "" {
			return "wayland (auto)"
		}
		return "x11 (auto)"
	}
	return choice
}

// selectBackend must run before app.NewWithID. X11 and Wayland are both
// supported by Fyne; this only selects which one to use.
func selectBackend(cfg *config.Config) {
	choice := strings.ToLower(cfg.String("go_display_backend"))
	if choice == "" || choice == "auto" {
		if os.Getenv("WAYLAND_DISPLAY") != "" {
			choice = "wayland"
		} else {
			choice = "x11"
		}
	}
	if choice == "x11" || choice == "wayland" {
		_ = os.Setenv("FYNE_BACKEND", choice)
	}
}

// ---------------------------------------------------------------------------
// Shell: header, sidebar, content holder and footer
// ---------------------------------------------------------------------------

func (c *clientApp) buildRoot() fyne.CanvasObject {
	c.views = make(map[sectionID]fyne.CanvasObject)
	c.navs = make(map[sectionID]*navButton)
	c.content = container.NewStack()

	header := c.buildHeader()
	sidebar := c.buildSidebar()
	footer := c.buildFooter()
	c.buildSections()
	c.showSection(c.active)
	c.installShortcuts()
	return container.NewBorder(header, footer, sidebar, nil, c.content)
}

func (c *clientApp) buildHeader() fyne.CanvasObject {
	brand := widget.NewLabelWithStyle(version.AppName, fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	c.headerTitle = widget.NewLabelWithStyle(tr("main_window.tab_status", nil),
		fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	c.headerTitle.Wrapping = fyne.TextTruncate
	separator := widget.NewLabel("·")
	left := container.NewHBox(brand, separator, minWidth(c.headerTitle, 180))
	c.headerStatus = widget.NewLabelWithStyle("", fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	c.headerStatus.Wrapping = fyne.TextTruncate

	syncButton := newHintButton("", theme.MediaReplayIcon(),
		tr("status.sync_now_tooltip", nil), widget.HighImportance, c.onSync, c.setHint)
	c.pauseBtn = newHintButton("", theme.MediaPauseIcon(),
		tr("status.pause_tooltip", nil), widget.MediumImportance, c.onPause, c.setHint).Button
	right := container.NewHBox(minWidth(c.headerStatus, 220), syncButton,
		withHint(c.pauseBtn, tr("status.pause_tooltip", nil), c.setHint))
	return c.surface(container.NewBorder(nil, nil, left, right, nil), "")
}

func labelPause(paused bool) string {
	if paused {
		return tr("status.resume_btn", nil)
	}
	return tr("status.pause_btn", nil)
}

func (c *clientApp) buildSidebar() fyne.CanvasObject {
	// A transparent object fixes the rail width so the layout stays stable
	// from 900x640 up to 1280x800 and beyond.
	spacer := canvas.NewRectangle(colorTransparent)
	spacer.SetMinSize(fyne.NewSize(206, 1))
	bg := canvas.NewRectangle(c.tint(theme.ColorNameHeaderBackground))
	line := canvas.NewRectangle(c.tint(theme.ColorNameSeparator))
	line.SetMinSize(fyne.NewSize(1, 1))

	items := []fyne.CanvasObject{
		c.makeNav(sectionStatus, tr("main_window.tab_status", nil), theme.HomeIcon(),
			tr("main_window.tab_status_tooltip", nil)).buttonView,
		c.makeNav(sectionActivity, tr("ui.nav.activity", nil), theme.ListIcon(),
			tr("main_window.tab_transfers_tooltip", nil)).buttonView,
		c.makeNav(sectionFiles, tr("ui.nav.files", nil), theme.FolderOpenIcon(),
			tr("main_window.tab_browse_tooltip", nil)).buttonView,
		c.makeNav(sectionNAS, tr("ui.nav.nas", nil), theme.ComputerIcon(),
			tr("main_window.tab_metrics_tooltip", nil)).buttonView,
		c.makeNav(sectionSettings, tr("main_window.tab_settings", nil), theme.SettingsIcon(),
			tr("main_window.tab_settings_tooltip", nil)).buttonView,
	}
	body := container.NewVBox(
		widget.NewLabelWithStyle("NASBOX", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		line,
		container.NewVBox(items...),
		layout.NewSpacer(),
		c.note(tr("ui.nav.hint", nil)),
	)
	return container.NewStack(spacer, bg, container.NewPadded(body))
}

func (c *clientApp) makeNav(id sectionID, label string, icon fyne.Resource, hint string) *navButton {
	bg := canvas.NewRectangle(colorTransparent)
	bg.CornerRadius = 8
	accent := canvas.NewRectangle(colorTransparent)
	accent.SetMinSize(fyne.NewSize(4, 32))
	// Navigation uses a native Button: Fyne routes taps to it directly, without
	// a wrapper that could swallow the Tapped callback.
	button := widget.NewButtonWithIcon(label, icon, func() { c.showSection(id) })
	button.Importance = widget.LowImportance
	button.SetText(label)
	item := &navButton{
		Button: button, bg: bg, accent: accent, section: id, label: label,
	}
	// Background, accent bar and marker are all part of the same layout unit.
	// Previously the background rectangle was created but never inserted.
	item.buttonView = container.NewStack(bg, container.NewBorder(
		nil, nil, accent, nil, container.NewPadded(withHint(button, hint, c.setHint)),
	))
	c.navs[id] = item
	return item
}

func (c *clientApp) buildFooter() fyne.CanvasObject {
	c.eventLabel = widget.NewLabel("")
	c.eventLabel.Wrapping = fyne.TextTruncate
	c.hintLabel = widget.NewLabelWithStyle("", fyne.TextAlignTrailing, fyne.TextStyle{Italic: true})
	c.hintLabel.Wrapping = fyne.TextTruncate
	line := canvas.NewRectangle(c.tint(theme.ColorNameSeparator))
	line.SetMinSize(fyne.NewSize(1, 1))
	// Both footer cells stay on one line: long events are elided instead of
	// stealing width from the hint and wrapping into a vertical strip.
	return container.NewVBox(line, container.NewBorder(
		nil, nil, nil, minWidth(c.hintLabel, 300), minWidth(c.eventLabel, 540),
	))
}

func (c *clientApp) installShortcuts() {
	if c.window == nil || c.window.Canvas() == nil {
		return
	}
	canvas := c.window.Canvas()
	canvas.AddShortcut(&desktop.CustomShortcut{KeyName: fyne.KeyS, Modifier: fyne.KeyModifierControl}, func(fyne.Shortcut) { c.onSync() })
	canvas.AddShortcut(&desktop.CustomShortcut{KeyName: fyne.KeyP, Modifier: fyne.KeyModifierControl}, func(fyne.Shortcut) { c.onPause() })
	canvas.AddShortcut(&desktop.CustomShortcut{KeyName: fyne.KeyF5}, func(fyne.Shortcut) { c.refreshCurrentSection() })
	canvas.AddShortcut(&desktop.CustomShortcut{KeyName: fyne.KeyL, Modifier: fyne.KeyModifierControl}, func(fyne.Shortcut) { c.navigate(sectionActivity, 2) })
	canvas.AddShortcut(&desktop.CustomShortcut{KeyName: fyne.Key1, Modifier: fyne.KeyModifierControl}, func(fyne.Shortcut) { c.showSection(sectionStatus) })
	canvas.AddShortcut(&desktop.CustomShortcut{KeyName: fyne.Key2, Modifier: fyne.KeyModifierControl}, func(fyne.Shortcut) { c.showSection(sectionActivity) })
	canvas.AddShortcut(&desktop.CustomShortcut{KeyName: fyne.Key3, Modifier: fyne.KeyModifierControl}, func(fyne.Shortcut) { c.showSection(sectionFiles) })
	canvas.AddShortcut(&desktop.CustomShortcut{KeyName: fyne.Key4, Modifier: fyne.KeyModifierControl}, func(fyne.Shortcut) { c.showSection(sectionNAS) })
	canvas.AddShortcut(&desktop.CustomShortcut{KeyName: fyne.Key5, Modifier: fyne.KeyModifierControl}, func(fyne.Shortcut) { c.showSection(sectionSettings) })
	canvas.AddShortcut(&desktop.CustomShortcut{KeyName: fyne.KeyComma, Modifier: fyne.KeyModifierControl}, func(fyne.Shortcut) { c.navigate(sectionSettings, 0) })
}

func (c *clientApp) refreshCurrentSection() {
	switch c.active {
	case sectionActivity:
		c.appendAction(tr("ui.activity.refreshed", nil))
	case sectionFiles:
		c.appendAction(tr("browse.refresh_btn", nil))
		c.refreshConflicts()
		c.refreshHistory()
		c.refreshBrowse()
	case sectionNAS:
		c.appendAction(tr("ui.action.server_check", nil))
		c.pollMetrics()
	default:
		c.refreshStats()
		c.appendAction(tr("ui.activity.refreshed", nil))
	}
}

func (c *clientApp) installTray() {
	tray := fyne.NewMenu(version.AppName,
		fyne.NewMenuItem(tr("ui.tray.show", nil), c.window.Show),
		fyne.NewMenuItem(tr("ui.tray.sync", nil), c.onSync),
		fyne.NewMenuItem(tr("ui.tray.pause_resume", nil), c.onPause),
		fyne.NewMenuItem(tr("ui.tray.undo", nil), c.onUndo),
		fyne.NewMenuItemSeparator(),
		fyne.NewMenuItem(tr("ui.tray.quit", nil), func() { c.cancel(); c.fyneApp.Quit() }),
	)
	if trayApp, ok := c.fyneApp.(interface{ SetSystemTrayMenu(*fyne.Menu) }); ok {
		trayApp.SetSystemTrayMenu(tray)
	}
}

func (c *clientApp) sectionLabel(id sectionID) string {
	switch id {
	case sectionStatus:
		return tr("main_window.tab_status", nil)
	case sectionActivity:
		return tr("ui.nav.activity", nil)
	case sectionFiles:
		return tr("ui.nav.files", nil)
	case sectionNAS:
		return tr("ui.nav.nas", nil)
	default:
		return tr("main_window.tab_settings", nil)
	}
}

func (c *clientApp) showSection(id sectionID) {
	if c.content == nil {
		return
	}
	view, ok := c.views[id]
	if !ok {
		return
	}
	c.active = id
	c.content.Objects = []fyne.CanvasObject{view}
	c.content.Refresh()
	c.headerTitle.SetText(c.sectionLabel(id))
	if c.eventLabel != nil {
		c.eventLabel.SetText(c.sectionLabel(id))
	}
	for section, item := range c.navs {
		if section == id {
			item.bg.FillColor = c.tint(theme.ColorNameSelection)
			item.accent.FillColor = c.tint(theme.ColorNamePrimary)
			item.Button.Importance = widget.LowImportance
			item.Button.SetText("▸ " + item.label)
		} else {
			item.bg.FillColor = colorTransparent
			item.accent.FillColor = colorTransparent
			item.Button.Importance = widget.LowImportance
			item.Button.SetText(item.label)
		}
		item.bg.Refresh()
		item.accent.Refresh()
		item.Button.Refresh()
	}
}

// navigate switches area and, when requested, a secondary tab inside it.
func (c *clientApp) navigate(id sectionID, tab int) {
	c.showSection(id)
	var tabs *container.AppTabs
	switch id {
	case sectionActivity:
		tabs = c.activityTabs
	case sectionFiles:
		tabs = c.fileTabs
	case sectionNAS:
		tabs = c.nasTabs
	case sectionSettings:
		tabs = c.settingsTabs
	}
	if tabs != nil && tab >= 0 && tab < len(tabs.Items) {
		tabs.SelectIndex(tab)
	}
}

// ---------------------------------------------------------------------------
// Sections
// ---------------------------------------------------------------------------

func (c *clientApp) buildSections() {
	c.views[sectionStatus] = container.NewVScroll(c.statusView())
	c.views[sectionActivity] = c.activityView()
	c.views[sectionFiles] = c.filesView()
	c.views[sectionNAS] = container.NewVScroll(c.nasView())
	c.views[sectionSettings] = c.settingsView()
}

func (c *clientApp) statusView() fyne.CanvasObject {
	c.pauseBtnStatus = newHintButton(labelPause(c.runner.IsPaused()), theme.MediaPauseIcon(),
		tr("status.pause_tooltip", nil), widget.MediumImportance, c.onPause, c.setHint).Button
	c.detailsBtn = newHintButton(tr("ui.action.details", nil), theme.NavigateNextIcon(),
		tr("ui.action.details_hint", nil), widget.MediumImportance, func() {
			if c.bannerGo != nil {
				c.bannerGo()
			}
		}, c.setHint)
	banner := c.buildBanner()
	actions := container.NewGridWithColumns(4,
		newHintButton(tr("ui.action.sync_short", nil), theme.MediaReplayIcon(),
			tr("status.sync_now_tooltip", nil), widget.HighImportance, c.onSync, c.setHint),
		c.pauseBtnStatus,
		newHintButton(tr("ui.action.audit_short", nil), theme.ConfirmIcon(),
			tr("audit.run_now_tooltip", nil), widget.MediumImportance, c.onAudit, c.setHint),
		newHintButton(tr("ui.action.preview_short", nil), theme.VisibilityIcon(),
			tr("transfers.queue_note", nil), widget.MediumImportance, c.onPreview, c.setHint),
		newHintButton(tr("ui.action.undo_short", nil), theme.ContentUndoIcon(),
			tr("ui.action.undo_hint", nil), widget.MediumImportance, c.onUndo, c.setHint),
		newHintButton(tr("ui.action.confirm_delete", nil), theme.ConfirmIcon(),
			tr("status.confirm_deletes_tooltip", nil), widget.DangerImportance, c.onConfirmDelete, c.setHint),
		newHintButton(tr("ui.action.cancel", nil), theme.CancelIcon(),
			tr("status.cancel_tooltip", nil), widget.LowImportance, c.onCancel, c.setHint),
	)

	numberTracked, trackedTile := c.statTile("—", tr("assurance.stat_tracked", nil))
	numberConflicts, conflictsTile := c.statTile("—", tr("assurance.stat_conflicts", nil))
	numberUndo, undoTile := c.statTile("—", tr("ui.stat.undo", nil))
	numberAudit, auditTile := c.statTile("—", tr("overview.stat_audit", nil))
	c.statTracked, c.statConflicts, c.statUndo, c.statAudit = numberTracked, numberConflicts, numberUndo, numberAudit

	stats := container.NewGridWithColumns(4, trackedTile, conflictsTile, undoTile, auditTile)

	detail := widget.NewLabel(fmt.Sprintf("%s: %s\n%s: %s\n%s: %s",
		tr("status.config_nas_host", nil), valueOrDash(c.cfg.String("nas_lan")),
		tr("status.config_local_folder", nil), valueOrDash(c.cfg.LocalRoot()),
		tr("status.config_nas_user", nil), valueOrDash(c.cfg.String("nas_user")),
	))
	detail.Wrapping = fyne.TextWrapWord
	backend := c.note(fmt.Sprintf("%s: %s · %s %s",
		tr("main_window.tab_settings", nil), displayBackend(c.cfg),
		tr("settings.info_version_label", nil), version.AppVersion))

	return container.NewVBox(
		c.sectionTitle(tr("assurance.title", nil), tr("main_window.brand_tagline", nil)),
		banner,
		container.NewPadded(actions),
		stats,
		c.surface(container.NewVBox(detail, backend), ""),
	)
}

func (c *clientApp) filteredVersions() []history.Version {
	query := ""
	if c.historyFilter != nil {
		query = strings.ToLower(strings.TrimSpace(c.historyFilter.Text))
	}
	if query == "" {
		return c.versions
	}
	result := make([]history.Version, 0)
	for _, version := range c.versions {
		if strings.Contains(strings.ToLower(version.Original), query) {
			result = append(result, version)
		}
	}
	return result
}

func shortTimestamp(value string) string {
	if len(value) >= 16 {
		return value[5:10] + " " + value[11:16]
	}
	return value
}

func humanBytes(size int64) string {
	switch {
	case size >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(size)/(1<<30))
	case size >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(size)/(1<<20))
	case size >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(size)/(1<<10))
	default:
		return fmt.Sprintf("%d B", size)
	}
}

func valueOrDash(value string) string {
	if strings.TrimSpace(value) == "" {
		return "—"
	}
	return value
}

func (c *clientApp) buildBanner() fyne.CanvasObject {
	c.bannerTitle = widget.NewLabelWithStyle("", fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	c.bannerReason = widget.NewLabel("")
	c.bannerReason.Wrapping = fyne.TextWrapWord
	c.bannerNext = widget.NewLabelWithStyle("", fyne.TextAlignLeading, fyne.TextStyle{Italic: true})
	c.bannerNext.Wrapping = fyne.TextWrapWord
	c.bannerBG = canvas.NewRectangle(c.bannerTint(theme.ColorNamePrimary))
	c.bannerBG.CornerRadius = 12
	content := container.NewVBox(
		c.bannerTitle,
		c.bannerReason,
		container.NewBorder(nil, nil, nil, c.detailsBtn, c.bannerNext),
	)
	return container.NewStack(c.bannerBG, container.NewPadded(content))
}

func (c *clientApp) activityView() fyne.CanvasObject {
	c.activityList = newTextList(
		func() int { return len(c.activityLines) },
		func(id int, label *widget.Label) { label.SetText(c.activityLines[id]) },
	)
	c.transferList = newTextList(
		func() int { return len(c.transferLines) },
		func(id int, label *widget.Label) { label.SetText(c.transferLines[id]) },
	)
	c.logList = newTextList(
		func() int { return len(c.logLines) },
		func(id int, label *widget.Label) { label.SetText(c.logLines[id]) },
	)
	activityPane := container.NewBorder(
		container.NewVBox(c.sectionTitle(tr("ui.activity.recent_title", nil), tr("ui.activity.recent_note", nil))),
		nil, nil, nil, container.NewVScroll(c.activityList),
	)
	transfers := container.NewBorder(
		container.NewVBox(
			c.sectionTitle(tr("main_window.tab_transfers", nil), tr("ui.activity.transfers_note", nil)),
		),
		nil, nil, nil, container.NewVScroll(c.transferList),
	)
	logPane := container.NewBorder(
		container.NewVBox(
			c.sectionTitle(tr("main_window.tab_log", nil), tr("ui.activity.log_note", nil)),
		),
		nil, nil, nil, container.NewVScroll(c.logList),
	)
	c.activityTabs = container.NewAppTabs(
		container.NewTabItem(tr("ui.activity.recent", nil), activityPane),
		container.NewTabItem(tr("main_window.tab_transfers", nil), transfers),
		container.NewTabItem(tr("main_window.tab_log", nil), logPane),
	)
	return c.activityTabs
}

func (c *clientApp) filesView() fyne.CanvasObject {
	c.fileTabs = container.NewAppTabs(
		container.NewTabItem(tr("main_window.tab_browse", nil), c.buildBrowsePane()),
		container.NewTabItem(tr("main_window.tab_conflicts", nil), c.buildConflictPane()),
		container.NewTabItem(tr("main_window.tab_history", nil), c.buildHistoryPane()),
	)
	return c.fileTabs
}

func (c *clientApp) nasView() fyne.CanvasObject {
	c.nasLabel = widget.NewLabel(tr("status.diagnostics_loading", nil))
	c.nasLabel.Wrapping = fyne.TextWrapWord
	c.nasUpdatedLabel = widget.NewLabel("")
	c.devicesLabel = widget.NewLabel(tr("devices.empty", nil))
	c.devicesLabel.Wrapping = fyne.TextWrapWord
	c.monitorDevicesLabel = widget.NewLabel(tr("devices.empty", nil))
	c.monitorDevicesLabel.Wrapping = fyne.TextWrapWord

	c.diskValue, _ = c.statTile("—", tr("metrics.disk_title", nil))
	c.diskBar = widget.NewProgressBar()
	diskCaption := widget.NewLabelWithStyle(tr("metrics.disk_title", nil), fyne.TextAlignLeading, fyne.TextStyle{Italic: true})
	var loadTile, memoryTile, lockTile, queueTile fyne.CanvasObject
	c.loadValue, loadTile = c.statTile("—", tr("metrics.load_title", nil))
	c.memoryValue, memoryTile = c.statTile("—", tr("metrics.memory_title", nil))
	c.lockValue, lockTile = c.statTile("—", tr("settings.nas_fact_lock_label", nil))
	c.queueValue, queueTile = c.statTile("—", tr("metrics.queue_label", nil))
	metrics := container.NewGridWithColumns(2,
		c.surface(container.NewVBox(c.diskValue, diskCaption, c.diskBar), ""),
		loadTile, memoryTile, lockTile, queueTile,
	)
	monitorPane := container.NewVBox(
		c.sectionTitle(tr("metrics.nasbox_title", nil), tr("settings.metrics_refresh_note", nil)),
		c.surface(container.NewVBox(c.nasLabel, c.nasUpdatedLabel), ""),
		metrics,
		c.surface(container.NewVBox(
			widget.NewLabelWithStyle(tr("status.nas_overview_title", nil), fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
			c.monitorDevicesLabel,
		), ""),
		newHintButton(tr("ui.action.server_check", nil), theme.ViewRefreshIcon(),
			tr("ui.action.server_check_hint", nil), widget.MediumImportance, c.onServerCheck, c.setHint),
	)
	c.deviceList = newRowList(
		func() int { return len(c.deviceEntries) }, 220, 320,
		func(id int, row *listRow) {
			entry := c.deviceEntries[id]
			row.icon.SetResource(theme.ComputerIcon())
			row.name.SetText(c.deviceName(entry))
			row.meta.SetText(c.deviceSubtitle(entry))
		},
	)
	c.deviceList.OnSelected = func(id widget.ListItemID) {
		if int(id) < len(c.deviceEntries) {
			c.selectedDevice = c.deviceEntries[id].ID
			c.updateDevicesLabel()
		}
	}
	c.selectedDeviceLabel = widget.NewLabel(tr("ui.device.none_selected", nil))
	c.selectedDeviceLabel.Wrapping = fyne.TextWrapWord
	devicesPane := container.NewBorder(
		container.NewVBox(
			c.sectionTitle(tr("devices.title", nil), tr("ui.device.assign_note", nil)),
			c.selectedDeviceLabel,
			container.NewHBox(
				newHintButton(tr("ui.action.assign_device", nil), theme.DocumentCreateIcon(),
					tr("ui.action.assign_device_hint", nil), widget.MediumImportance, c.onAssignDevice, c.setHint),
				newHintButton(tr("devices.refresh_btn", nil), theme.ViewRefreshIcon(),
					tr("ui.device.refresh_hint", nil), widget.LowImportance, func() {
						c.appendAction(tr("devices.refresh_btn", nil))
						c.pollMetrics()
					}, c.setHint),
			),
		),
		nil, nil, nil, c.deviceList,
	)
	c.nasTabs = container.NewAppTabs(
		container.NewTabItem(tr("main_window.tab_metrics", nil), monitorPane),
		container.NewTabItem(tr("devices.title", nil), devicesPane),
	)
	return c.nasTabs
}

func (c *clientApp) updateDevicesLabel() {
	if c.selectedDeviceLabel == nil {
		return
	}
	if c.selectedDevice == "" {
		c.selectedDeviceLabel.SetText(tr("ui.device.none_selected", nil))
		return
	}
	c.selectedDeviceLabel.SetText(tr("ui.device.selected", map[string]any{"id": c.selectedDevice}))
}

func (c *clientApp) settingsView() fyne.CanvasObject {
	c.mirrorLabel = widget.NewLabel("")
	c.mirrorLabel.Wrapping = fyne.TextWrapWord
	c.selectiveList = newTextList(
		func() int { return len(c.selectiveDirs) },
		func(id int, label *widget.Label) {
			dir := c.selectiveDirs[id]
			prefix := "☑ "
			if selective.Excluded(c.cfg, dir) {
				prefix = "☐ "
			}
			label.SetText(prefix + dir)
		},
	)
	c.selectiveList.OnSelected = c.onToggleSelective

	general := container.NewVBox(
		c.settingsAppearance(),
		c.settingsConnection(),
		c.settingsSync(),
		c.settingsGroup(tr("ui.settings.info_title", nil), "",
			widget.NewLabel(fmt.Sprintf("%s: %s", tr("settings.info_config_label", nil), valueOrDash(c.cfg.Path))),
			widget.NewLabel(fmt.Sprintf("%s: %s", tr("status.config_local_folder", nil), valueOrDash(c.cfg.LocalRoot()))),
		),
	)
	folders := container.NewVBox(
		c.sectionTitle(tr("ui.group.folders", nil), tr("mirrors.authority_note", nil)),
		c.surface(container.NewVBox(
			widget.NewLabelWithStyle(tr("main_window.tab_mirrors", nil), fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
			c.note(tr("ui.dialog.mirror_note", nil)),
			c.mirrorLabel,
			container.NewHBox(
				newHintButton(tr("ui.action.sync_folders", nil), theme.ViewRefreshIcon(),
					tr("mirrors.sync_now_tooltip", nil), widget.MediumImportance, c.onMirrorSync, c.setHint),
				newHintButton(tr("ui.action.add_mirror", nil), theme.ContentAddIcon(),
					tr("ui.settings.add_mirror_hint", nil), widget.HighImportance, c.onAddMirror, c.setHint),
			),
		), ""),
		c.surface(container.NewVBox(
			widget.NewLabelWithStyle(tr("dialogs.selective.title", nil), fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
			c.note(tr("ui.hint.selective", nil)),
			container.NewGridWrap(fyne.NewSize(1, 150), c.selectiveList),
		), ""),
	)
	about := c.settingsGroup(tr("ui.group.about", nil), "",
		widget.NewLabel(fmt.Sprintf("%s: %s %s", tr("settings.info_version_label", nil), version.AppName, version.AppVersion)),
		widget.NewLabel(fmt.Sprintf("%s: %s", tr("settings.info_min_server_label", nil), valueOrDash(c.cfg.String("min_server_version")))),
		newHintButton(tr("ui.action.check_update", nil), theme.ViewRefreshIcon(),
			tr("main_window.version_tooltip", nil), widget.MediumImportance, c.onCheckUpdate, c.setHint),
		newHintButton(tr("ui.action.update_install", nil), theme.ViewRefreshIcon(),
			tr("ui.settings.install_update_hint", nil), widget.HighImportance, c.onInstallUpdate, c.setHint),
		newHintButton(tr("ui.action.confirm_delete", nil), theme.ConfirmIcon(),
			tr("status.confirm_deletes_tooltip", nil), widget.DangerImportance, c.onConfirmDelete, c.setHint),
	)
	c.settingsTabs = container.NewAppTabs(
		container.NewTabItem(tr("ui.group.general", nil), container.NewVScroll(general)),
		container.NewTabItem(tr("ui.settings.safety_tab", nil), container.NewVScroll(c.settingsSafety())),
		container.NewTabItem(tr("ui.settings.integrity_tab", nil), container.NewVScroll(c.settingsIntegrity())),
		container.NewTabItem(tr("ui.group.folders", nil), container.NewVScroll(folders)),
		container.NewTabItem(tr("ui.settings.advanced_tab", nil), container.NewVScroll(c.settingsAdvanced())),
		container.NewTabItem(tr("ui.group.about", nil), container.NewVScroll(about)),
	)
	if index, err := strconv.Atoi(os.Getenv("NASBOX_SETTINGS_TAB")); err == nil &&
		index >= 0 && index < len(c.settingsTabs.Items) {
		c.settingsTabs.SelectIndex(index)
	}
	return c.settingsTabs
}

// ---------------------------------------------------------------------------
// Status semantics
// ---------------------------------------------------------------------------

func (c *clientApp) applyStatus(level statusLevel, reason string) {
	fyne.Do(func() { c.applyStatusUI(level, reason) })
}

func iconStateForLevel(level statusLevel) string {
	switch level {
	case statusOK:
		return "synced"
	case statusProgress:
		return "syncing"
	case statusPaused:
		return "paused"
	case statusAttention, statusBlocked, statusOffline:
		return "error"
	default:
		return "unconfigured"
	}
}

func (c *clientApp) applyStatusUI(level statusLevel, reason string) {
	if strings.TrimSpace(reason) == "" {
		reason = tr("status.sync_state_ready", nil)
	}
	c.level = level
	c.syncActive.Store(level == statusProgress)
	if level != statusProgress {
		c.setTrayIcon(iconStateForLevel(level))
	}
	var title, next, action string
	var colour fyne.ThemeColorName
	var goFn func()
	switch level {
	case statusOK:
		title, next, colour = tr("overview.banner_ok", nil), tr("ui.status.ok_next", nil), theme.ColorNameSuccess
		action, goFn = tr("ui.action.sync_short", nil), func() { c.onSync() }
	case statusProgress:
		title, next, colour = tr("overview.banner_progress", nil), tr("ui.status.progress_next", nil), theme.ColorNamePrimary
		action, goFn = tr("main_window.tab_transfers", nil), func() { c.navigate(sectionActivity, 1) }
	case statusAttention:
		title, next, colour = tr("overview.banner_warning", nil), tr("ui.status.attention_next", nil), theme.ColorNameWarning
		action, goFn = tr("main_window.tab_log", nil), func() { c.navigate(sectionActivity, 2) }
	case statusBlocked:
		title, next, colour = tr("ui.status.blocked", nil), tr("ui.status.blocked_next", nil), theme.ColorNameError
		action, goFn = tr("main_window.tab_log", nil), func() { c.navigate(sectionActivity, 2) }
	case statusOffline:
		title, next, colour = tr("overview.banner_offline", nil), tr("overview.step_unreachable", nil), theme.ColorNameError
		action, goFn = tr("main_window.tab_settings", nil), func() { c.navigate(sectionSettings, 0) }
	case statusPaused:
		title, next, colour = tr("ui.status.paused", nil), tr("overview.step_paused", nil), theme.ColorNameWarning
		action, goFn = tr("status.resume_btn", nil), func() { c.onPause() }
	default:
		title, next, colour = tr("status.starting", nil), tr("status.sync_state_starting", nil), theme.ColorNamePrimary
		action, goFn = tr("main_window.tab_status", nil), func() { c.showSection(sectionStatus) }
	}
	c.bannerBG.FillColor = c.bannerTint(colour)
	c.bannerBG.Refresh()
	c.bannerTitle.SetText(title)
	c.bannerReason.SetText(reason)
	c.bannerNext.SetText(fmt.Sprintf("%s: %s", tr("assurance.guidance_title", nil), next))
	c.bannerGo = goFn
	if c.headerStatus != nil {
		c.headerStatus.SetText("● " + title)
	}
	if c.detailsBtn != nil {
		c.detailsBtn.SetText(action)
		c.detailsBtn.hint = fmt.Sprintf("%s — %s", action, next)
	}
}

func looksOffline(message string) bool {
	lower := strings.ToLower(message)
	for _, token := range []string{
		"offline", "unreachable", "raggiungibile", "connection refused",
		"no such host", "timeout", "ssh", "network", "rete",
	} {
		if strings.Contains(lower, token) {
			return true
		}
	}
	return false
}

func (c *clientApp) appendLine(line string) {
	stamp := time.Now().Format("15:04:05") + "  "
	entry := stamp + line
	c.logLines = append([]string{entry}, c.logLines...)
	if len(c.logLines) > 300 {
		c.logLines = c.logLines[:300]
	}
}

func (c *clientApp) prependLine(target *[]string, line string) {
	entry := time.Now().Format("15:04:05") + "  " + line
	*target = append([]string{entry}, (*target)...)
	if len(*target) > 300 {
		*target = (*target)[:300]
	}
}

func (c *clientApp) appendActivity(line string) {
	fyne.Do(func() {
		c.prependLine(&c.activityLines, line)
		if c.activityList != nil {
			c.activityList.Refresh()
		}
	})
}

func (c *clientApp) appendTransfer(line string) {
	fyne.Do(func() {
		c.prependLine(&c.transferLines, line)
		if c.transferList != nil {
			c.transferList.Refresh()
		}
	})
}

// appendAction records a user action once in the human-oriented recent list
// and once in the technical log. Transfer progress has its own list.
func (c *clientApp) appendAction(line string) {
	c.appendActivity(line)
	c.appendLog(line)
}

func (c *clientApp) appendLog(line string) {
	fyne.Do(func() {
		c.appendLine(line)
		if c.logList != nil {
			c.logList.Refresh()
		}
		if c.transferList != nil {
			c.transferList.Refresh()
		}
		if c.activityList != nil {
			c.activityList.Refresh()
		}
		if c.eventLabel != nil {
			c.eventLabel.SetText(line)
		}
	})
}

func shortHint(text string) string {
	if len(text) <= 64 {
		return text
	}
	cut := text[:64]
	if index := strings.LastIndex(cut, " "); index > 32 {
		cut = cut[:index]
	}
	return cut + "…"
}

func (c *clientApp) setHintText(text string) {
	fyne.Do(func() {
		if text == "" {
			text = tr("ui.footer.hint", nil)
		}
		text = shortHint(text)
		if c.hintLabel != nil {
			c.hintLabel.SetText(text)
		}
	})
}

func (c *clientApp) consumeEvents() {
	for event := range c.runner.Events() {
		switch event.Kind {
		case "sync":
			reason := strings.TrimSpace(event.Message)
			if reason == "" {
				reason = tr("assurance.all_synced", nil)
			}
			c.applyStatus(statusOK, reason)
			c.appendActivity(reason)
			c.appendTransfer(reason)
			c.appendLog(reason)
			c.refreshConflicts()
			c.refreshHistory()
			c.refreshStats()
		case "status":
			c.applyStatus(statusProgress, tr("status.sync_requested", nil))
			message := tr("status.sync_state_active", map[string]any{
				"direction": tr("ui.direction.sync", nil),
			})
			c.appendTransfer(message)
			c.appendLog(message)
		case "blocked":
			c.applyStatus(statusBlocked, event.Message)
			c.appendAction(event.Message)
		case "error":
			level := statusAttention
			if looksOffline(event.Message) {
				level = statusOffline
			}
			c.applyStatus(level, event.Message)
			c.appendAction(fmt.Sprintf("%s: %s", tr("log.action.error", nil), event.Message))
		default:
			c.appendLog(event.Message)
		}
	}
}

// ---------------------------------------------------------------------------
// Data refresh
// ---------------------------------------------------------------------------

func (c *clientApp) refreshStats() {
	tracked := c.store.EntryCount(c.cfg.LocalRepositoryID())
	undo := c.store.UndoCount(c.cfg.LocalRepositoryID(), 24)
	audit := tr("audit.never", nil)
	if reports := integrity.Load(); len(reports) > 0 {
		last := reports[len(reports)-1]
		audit = fmt.Sprintf("%d", len(last.Differences))
	}
	conflictCount := len(c.confGroups)
	fyne.Do(func() {
		if c.statTracked != nil {
			c.statTracked.SetText(fmt.Sprint(tracked))
		}
		if c.statConflicts != nil {
			c.statConflicts.SetText(fmt.Sprint(conflictCount))
		}
		if c.statUndo != nil {
			c.statUndo.SetText(fmt.Sprint(undo))
		}
		if c.statAudit != nil {
			c.statAudit.SetText(audit)
		}
	})
}

func (c *clientApp) refreshConflicts() {
	groups, err := conflicts.Scan(c.cfg.LocalRoot(), nil)
	if err != nil {
		return
	}
	fyne.Do(func() {
		c.confGroups = groups
		found := false
		for _, group := range groups {
			if group.Original == c.selectedGroup {
				found = true
				break
			}
		}
		if !found {
			c.selectedGroup = ""
			c.selectedConflict = ""
			c.confChoices = nil
		}
		c.updateConflictSelection()
		if c.confList != nil {
			c.confList.Refresh()
		}
		if c.confChoiceList != nil {
			c.confChoiceList.Refresh()
		}
		c.refreshStats()
	})
}

func (c *clientApp) refreshHistory() {
	versions, err := history.List()
	if err != nil {
		return
	}
	fyne.Do(func() {
		c.versions = versions
		if c.selectedVersion != "" {
			if _, ok := c.historyVersion(c.selectedVersion); !ok {
				c.selectedVersion = ""
			}
		}
		c.updateHistorySelection()
		if c.histList != nil {
			c.histList.Refresh()
		}
	})
}

func (c *clientApp) refreshBrowse() {
	entries, err := c.client.BrowseList(c.browsePath)
	if err != nil {
		c.appendLog(fmt.Sprintf("%s: %s", tr("browse.nas_unreachable_title", nil), err))
		return
	}
	fyne.Do(func() {
		c.browseEntries = entries
		if c.selectedBrowse != "" {
			found := false
			for _, entry := range entries {
				if entry.Name == c.selectedBrowse && entry.Kind != "DIR" {
					found = true
					break
				}
			}
			if !found {
				c.selectedBrowse = ""
			}
		}
		c.updateBreadcrumbs()
		c.updateBrowseSelection()
		if c.browseList != nil {
			c.browseList.Refresh()
		}
	})
}

func (c *clientApp) refreshSelective() {
	dirs := selective.ListDirectories(c.cfg.LocalRoot(), 1000)
	fyne.Do(func() {
		c.selectiveDirs = dirs
		if c.selectiveList != nil {
			c.selectiveList.Refresh()
		}
	})
}

func (c *clientApp) refreshMirrorLabel() {
	mirrors := mirror.Configured(c.cfg)
	lines := []string{}
	for _, item := range mirrors {
		state := tr("mirrors.last_sync_never", nil)
		if item.Enabled {
			state = tr("devices.active", nil)
		}
		lines = append(lines, fmt.Sprintf("%s → %s (%s)", item.Source, item.Dest, state))
	}
	text := tr("mirrors.last_sync_never", nil)
	if len(lines) > 0 {
		text = strings.Join(lines, "\n")
	}
	fyne.Do(func() {
		if c.mirrorLabel != nil {
			c.mirrorLabel.SetText(text)
		}
	})
}

func (c *clientApp) refreshMonitor() {
	for {
		c.pollMetrics()
		time.Sleep(30 * time.Second)
	}
}

func (c *clientApp) pollMetrics() {
	values, err := c.client.Metrics()
	if err == nil {
		fyne.Do(func() { c.updateMetricsUI(values) })
	} else {
		fyne.Do(func() {
			if c.nasUpdatedLabel != nil {
				c.nasUpdatedLabel.SetText(tr("ui.nas.metrics_unavailable", nil))
			}
		})
	}
	devices, err := c.client.Devices()
	if err == nil {
		fyne.Do(func() {
			c.updateDevicesUI(devices)
			if c.devicesLabel != nil {
				c.devicesLabel.SetText(tr("ui.device.assign_note", nil))
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Actions
// ---------------------------------------------------------------------------

func (c *clientApp) onSync() {
	c.applyStatus(statusProgress, tr("status.sync_requested", nil))
	c.appendLog(tr("status.sync_requested", nil))
	c.runner.SyncNow()
}

func (c *clientApp) onPause() {
	paused := !c.runner.IsPaused()
	c.runner.SetPaused(paused)
	if paused {
		c.applyStatus(statusPaused, tr("status.sync_state_paused", nil))
	} else {
		c.applyStatus(statusOK, tr("status.sync_state_ready", nil))
	}
	fyne.Do(func() {
		if c.pauseBtn != nil {
			c.pauseBtn.SetText(labelPause(paused))
		}
		if c.pauseBtnStatus != nil {
			c.pauseBtnStatus.SetText(labelPause(paused))
		}
	})
	c.appendLog(labelPause(paused))
}

func (c *clientApp) onAudit() {
	c.appendLog(tr("audit.running", nil))
	go func() {
		raw, err := c.client.ManifestExport("")
		if err != nil {
			c.appendLog(tr("audit.error", map[string]any{"detail": err.Error()}))
			return
		}
		manifest, err := remote.ParseManifest(raw)
		if err != nil {
			c.appendLog(tr("audit.error", map[string]any{"detail": err.Error()}))
			return
		}
		report := integrity.Run(c.cfg, manifest, version.AppVersion, nil)
		c.appendLog(fmt.Sprintf("%s — %d, %d", report.Status, report.FilesChecked, len(report.Differences)))
		c.refreshStats()
	}()
}

func (c *clientApp) onPreview() {
	c.appendLog(tr("transfers.queue_scan_running", map[string]any{"seconds": 0}))
	go func() {
		raw, err := c.client.ManifestExport("")
		if err != nil {
			c.appendLog(tr("audit.error", map[string]any{"detail": err.Error()}))
			return
		}
		manifest, err := remote.ParseManifest(raw)
		if err != nil {
			c.appendLog(tr("audit.error", map[string]any{"detail": err.Error()}))
			return
		}
		report, err := engine.Preview(c.cfg, c.store, manifest)
		if err != nil {
			c.appendLog(tr("audit.error", map[string]any{"detail": err.Error()}))
			return
		}
		c.appendLog(report.Summary())
	}()
}

func (c *clientApp) onUndo() {
	c.appendAction(tr("ui.action.undo", nil))
	dialog.ShowConfirm(tr("ui.confirm.undo_title", nil), tr("ui.confirm.undo_body", nil), func(ok bool) {
		if !ok {
			c.appendAction(tr("ui.action.cancelled", nil))
			return
		}
		go func() {
			entries, err := c.store.RecentUndo(c.cfg.LocalRepositoryID(), 24, 500)
			if err != nil || len(entries) == 0 {
				c.appendLog(tr("status.last_problem_none", nil))
				return
			}
			requests := make([]remote.RestoreRequest, 0, len(entries))
			batchByPath := map[string]string{}
			for _, item := range entries {
				requests = append(requests, remote.RestoreRequest{Path: item.Path, VersionTS: item.VersionTS})
				batchByPath[item.Path] = item.BatchID
			}
			statuses, err := c.client.RestoreBatch(requests, c.store.DeviceID())
			if err != nil {
				c.appendLog(tr("history.restore_failed_title", nil) + ": " + err.Error())
				return
			}
			restored := 0
			for path, status := range statuses {
				if status == "RESTORED" {
					_ = c.store.MarkUndoApplied(c.cfg.LocalRepositoryID(), batchByPath[path], []string{path})
					restored++
				}
			}
			c.appendAction(tr("history.restore_all_done_body", map[string]any{"count": restored}))
			c.refreshStats()
		}()
	}, c.window)
}

func (c *clientApp) onResolveConflict() {
	if c.selectedConflict == "" {
		c.appendLog(tr("conflicts.copy_name_none", nil))
		return
	}
	chosen := c.selectedConflict
	c.appendAction(tr("ui.action.resolve", nil) + ": " + chosen)
	dialog.ShowConfirm(tr("conflicts.confirm_title", nil),
		fmt.Sprintf("%s: %s", tr("conflicts.keep_label", nil), chosen), func(ok bool) {
			if !ok {
				c.appendAction(tr("ui.action.cancelled", nil))
				return
			}
			go func() {
				for _, group := range c.confGroups {
					for _, candidate := range group.Candidates {
						if candidate != chosen {
							continue
						}
						if err := conflicts.Resolve(c.cfg.LocalRoot(), group, candidate); err != nil {
							c.appendLog(tr("conflicts.resolve_failed_title", nil) + ": " + err.Error())
						} else {
							c.appendLog(tr("conflicts.resolve_done_title", nil) + ": " + group.Original)
							c.selectedConflict = ""
							c.refreshConflicts()
						}
						return
					}
				}
			}()
		}, c.window)
}

func (c *clientApp) onBrowseUp() {
	parts := strings.Split(strings.Trim(c.browsePath, "/"), "/")
	parent := ""
	if len(parts) > 0 && parts[0] != "" {
		parent = strings.Join(parts[:len(parts)-1], "/")
	}
	c.browseTo(parent)
}

func (c *clientApp) onBrowseDelete() {
	if c.selectedBrowse == "" {
		c.appendLog(tr("ui.hint.select_first", nil))
		return
	}
	path := c.selectedBrowse
	if c.browsePath != "" {
		path = c.browsePath + "/" + path
	}
	c.appendAction(tr("browse.delete_btn", nil) + ": " + path)
	retention := tr("browse.retention_never", nil)
	if days := c.cfg.Int("retention_days_remote"); days > 0 {
		retention = tr("browse.retention_days", map[string]any{"days": days})
	}
	dialog.ShowConfirm(tr("browse.confirm_delete_title", nil),
		tr("browse.confirm_delete_body", map[string]any{"names": path, "retention": retention}), func(ok bool) {
			if !ok {
				c.appendAction(tr("ui.action.cancelled", nil))
				return
			}
			go func() {
				status, err := c.client.BrowseDelete(path, remote.RunTimestamp(), c.store.DeviceID())
				if err != nil {
					c.appendLog(tr("browse.delete_failed_title", nil) + ": " + err.Error())
					return
				}
				c.appendAction(tr("log.action.browse_delete", nil) + ": " + status)
				c.selectedBrowse = ""
				c.refreshBrowse()
			}()
		}, c.window)
}

func (c *clientApp) onBrowseRename() {
	if c.selectedBrowse == "" {
		c.appendLog(tr("ui.hint.select_first", nil))
		return
	}
	current := c.selectedBrowse
	c.appendAction(tr("browse.rename_btn", nil) + ": " + current)
	entry := widget.NewEntry()
	entry.SetText(current)
	content := container.NewVBox(
		widget.NewLabel(tr("browse.rename_prompt", map[string]any{"name": current})),
		entry,
		c.note(tr("browse.path_label", map[string]any{"path": valueOrDash(c.browsePath)})),
	)
	confirm := dialog.NewCustomConfirm(
		tr("browse.rename_btn", nil), tr("ui.action.save", nil), tr("ui.action.cancelled", nil),
		content, func(ok bool) {
			if !ok {
				c.appendAction(tr("ui.action.cancelled", nil))
				return
			}
			if strings.TrimSpace(entry.Text) == "" {
				c.appendLog(tr("browse.rename_invalid_name", nil))
				return
			}
			dst := strings.TrimSpace(entry.Text)
			if strings.Contains(dst, "/") {
				c.appendLog(tr("browse.rename_invalid_name", nil))
				return
			}
			src, target := current, dst
			if c.browsePath != "" {
				src = c.browsePath + "/" + src
				target = c.browsePath + "/" + dst
			}
			go func() {
				status, err := c.client.BrowseRename(src, target, remote.RunTimestamp(), c.store.DeviceID())
				if err != nil {
					c.appendLog(tr("browse.rename_failed_title", nil) + ": " + err.Error())
					return
				}
				c.appendLog(tr("log.action.browse_rename", nil) + ": " + status)
				c.refreshBrowse()
			}()
		}, c.window,
	)
	confirm.Resize(fyne.NewSize(480, 190))
	confirm.Show()
}

func (c *clientApp) onToggleSelective(id widget.ListItemID) {
	if int(id) >= len(c.selectiveDirs) {
		return
	}
	dir := c.selectiveDirs[id]
	excluded := []string{}
	for _, text := range selective.ExcludedPaths(c.cfg) {
		if text != dir {
			excluded = append(excluded, text)
		}
	}
	if !selective.Excluded(c.cfg, dir) {
		excluded = append(excluded, dir)
	}
	selective.SetExcluded(c.cfg, excluded)
	c.selectiveList.Refresh()
	c.appendLog(tr("settings.selective_btn", nil) + ": " + dir)
}

func (c *clientApp) onAddMirror() {
	c.appendAction(tr("ui.action.add_mirror", nil))
	source := widget.NewEntry()
	source.SetPlaceHolder(tr("ui.hint.mirror_source", nil))
	destination := widget.NewEntry()
	destination.SetPlaceHolder(tr("ui.hint.mirror_dest", nil))
	content := container.NewVBox(
		widget.NewLabel(tr("ui.hint.mirror_source", nil)), source,
		widget.NewLabel(tr("ui.hint.mirror_dest", nil)), destination,
	)
	confirm := dialog.NewCustomConfirm(
		tr("ui.action.add_mirror", nil), tr("ui.action.save", nil), tr("ui.action.cancel_short", nil),
		content, func(ok bool) {
			if !ok {
				c.appendAction(tr("ui.action.cancelled", nil))
				return
			}
			if strings.TrimSpace(source.Text) == "" || strings.TrimSpace(destination.Text) == "" {
				c.appendLog(tr("ui.mirror.invalid", nil))
				return
			}
			values, _ := c.cfg.Get("mirrors").([]any)
			values = append(values, map[string]any{
				"source":  strings.TrimSpace(source.Text),
				"dest":    strings.TrimSpace(destination.Text),
				"enabled": true,
			})
			if !c.persistSetting("mirrors", values) {
				return
			}
			c.appendAction(tr("ui.mirror.added", map[string]any{
				"source": strings.TrimSpace(source.Text), "dest": strings.TrimSpace(destination.Text),
			}))
			c.refreshMirrorLabel()
		}, c.window,
	)
	confirm.Resize(fyne.NewSize(520, 220))
	confirm.Show()
}

func (c *clientApp) onMirrorSync() {
	c.appendLog(tr("mirrors.state_syncing", nil))
	go func() {
		count, err := mirror.SyncOnce(c.cfg, c.client)
		if err != nil {
			c.appendLog(tr("mirrors.error_title", nil) + ": " + err.Error())
			return
		}
		c.appendLog(tr("ui.log.mirror_done", map[string]any{"count": count}))
		c.refreshMirrorLabel()
	}()
}

func (c *clientApp) onCheckUpdate() {
	c.appendLog(tr("status.update_checking", nil))
	go func() {
		manifest, err := update.Newest(c.cfg, c.client)
		if err != nil {
			c.appendLog(tr("status.update_check_failed", map[string]any{"detail": err.Error()}))
			return
		}
		if update.Available(manifest) {
			c.appendLog(tr("status.update_available", map[string]any{"version": manifest.Version}))
			return
		}
		c.appendLog(tr("main_window.update_none", nil))
	}()
}

func (c *clientApp) onRestoreVersion() {
	if c.selectedVersion == "" {
		c.appendLog(tr("history.restore_failed_body", nil))
		return
	}
	chosen := c.selectedVersion
	target, ok := c.historyVersion(chosen)
	if !ok {
		c.appendLog(tr("history.restore_failed_body", nil))
		return
	}
	c.appendAction(tr("ui.action.restore", nil) + ": " + chosen)
	dialog.ShowConfirm(tr("history.confirm_restore_title", nil),
		tr("history.confirm_restore_body", map[string]any{
			"path": target.Original, "timestamp": target.Timestamp,
		}), func(ok bool) {
			if !ok {
				c.appendAction(tr("ui.action.cancelled", nil))
				return
			}
			go func() {
				if err := history.Restore(target, c.cfg.LocalRoot()); err != nil {
					c.appendLog(tr("history.restore_failed_title", nil) + ": " + err.Error())
				} else {
					c.appendLog(tr("history.restore_done_body", map[string]any{"path": target.Original}))
					c.selectedVersion = ""
					c.refreshHistory()
				}
			}()
		}, c.window)
}

func (c *clientApp) setLanguage(language string) {
	if i18n.Language == language && c.cfg.String("language") == language {
		return
	}
	i18n.Language = language
	c.cfg.Set("language", language, true)
	c.window.SetContent(c.buildRoot())
	c.showSection(c.active)
	c.appendLog(tr("settings.language_title", nil) + ": " + language)
}

func (c *clientApp) onConfirmDelete() {
	c.appendAction(tr("ui.action.confirm_delete", nil))
	dialog.ShowConfirm(tr("ui.confirm.mass_delete_title", nil), tr("ui.confirm.mass_delete_body", nil), func(ok bool) {
		if !ok {
			c.appendAction(tr("ui.action.cancelled", nil))
			return
		}
		until, err := deletesafety.Shared().Confirm(c.cfg)
		if err != nil {
			c.appendLog(tr("ui.confirm.mass_delete_failed", map[string]any{"detail": err.Error()}))
			return
		}
		c.appendAction(tr("ui.confirm.mass_delete_done", map[string]any{"time": time.Unix(int64(until), 0).Format("15:04:05")}))
	}, c.window)
}

func (c *clientApp) onCancel() {
	c.appendAction(tr("ui.action.cancel", nil))
	c.runner.Cancel()
}

func (c *clientApp) onDownloadPath() {
	if c.downloadPathEntry == nil {
		return
	}
	relative := strings.Trim(strings.TrimSpace(c.downloadPathEntry.Text), "/")
	if relative == "" {
		c.appendLog(tr("ui.file.download_empty", nil))
		return
	}
	c.startDownload(relative)
}

func (c *clientApp) onBrowseDownload() {
	if c.selectedBrowse == "" {
		c.appendLog(tr("ui.hint.select_first", nil))
		return
	}
	c.startDownload(c.joinBrowsePath(c.selectedBrowse))
}

func (c *clientApp) startDownload(relative string) {
	c.appendAction(tr("ui.file.download_started", map[string]any{"path": relative}))
	go func() {
		if err := c.transfers.DownloadPath(relative); err != nil {
			c.appendLog(tr("browse.download_failed_title", nil) + ": " + err.Error())
			return
		}
		c.appendAction(tr("ui.file.download_done", map[string]any{"path": relative}))
	}()
}

func (c *clientApp) onKeepBoth() {
	if c.selectedConflict == "" {
		c.appendLog(tr("ui.hint.select_first", nil))
		return
	}
	c.appendAction(tr("ui.action.keep_both", nil) + ": " + c.selectedConflict)
	go func() {
		for _, group := range c.confGroups {
			for _, candidate := range group.Candidates {
				if candidate != c.selectedConflict {
					continue
				}
				kept, err := conflicts.KeepBoth(c.cfg.LocalRoot(), group, candidate)
				if err != nil {
					c.appendLog(tr("ui.conflict.keep_both_failed", map[string]any{"detail": err.Error()}))
				} else {
					c.appendAction(tr("ui.conflict.keep_both_done", map[string]any{"path": kept}))
					c.selectedConflict = ""
					c.refreshConflicts()
				}
				return
			}
		}
	}()
}

func (c *clientApp) onRemoteRestore() {
	c.appendAction(tr("ui.action.remote_restore", nil))
	pathEntry := widget.NewEntry()
	pathEntry.SetPlaceHolder(tr("ui.history.remote_path_hint", nil))
	versionEntry := widget.NewEntry()
	versionEntry.SetPlaceHolder(tr("ui.history.remote_version_hint", nil))
	content := container.NewVBox(
		widget.NewLabel(tr("ui.history.remote_path", nil)), pathEntry,
		widget.NewLabel(tr("ui.history.remote_version", nil)), versionEntry,
		c.note(tr("ui.history.remote_warning", nil)),
	)
	confirm := dialog.NewCustomConfirm(
		tr("ui.action.remote_restore", nil), tr("ui.action.restore", nil), tr("ui.action.cancel_short", nil),
		content, func(ok bool) {
			if !ok {
				c.appendAction(tr("ui.action.cancelled", nil))
				return
			}
			path := strings.TrimSpace(pathEntry.Text)
			version := strings.TrimSpace(versionEntry.Text)
			if path == "" || version == "" {
				c.appendLog(tr("ui.history.remote_invalid", nil))
				return
			}
			go func() {
				status, err := c.client.RestoreVersion(path, version, c.store.DeviceID())
				if err != nil {
					c.appendLog(tr("ui.history.remote_failed", map[string]any{"detail": err.Error()}))
					return
				}
				c.appendAction(tr("ui.history.remote_done", map[string]any{"status": status}))
			}()
		}, c.window,
	)
	confirm.Resize(fyne.NewSize(520, 250))
	confirm.Show()
}

func (c *clientApp) onPrune() {
	c.appendAction(tr("ui.action.prune", nil))
	dialog.ShowConfirm(tr("ui.confirm.prune_title", nil), tr("ui.confirm.prune_body", nil), func(ok bool) {
		if !ok {
			c.appendAction(tr("ui.action.cancelled", nil))
			return
		}
		go func() {
			removed, err := history.Prune(c.cfg.Int("retention_days_local"))
			if err != nil {
				c.appendLog(tr("ui.history.prune_failed", map[string]any{"detail": err.Error()}))
				return
			}
			c.appendAction(tr("ui.history.prune_done", map[string]any{"count": removed}))
			c.refreshHistory()
		}()
	}, c.window)
}

func (c *clientApp) onAssignDevice() {
	if c.selectedDevice == "" {
		c.appendLog(tr("ui.hint.select_first", nil))
		return
	}
	name := widget.NewEntry()
	name.SetPlaceHolder(tr("ui.hint.device_name", nil))
	ip := widget.NewEntry()
	ip.SetPlaceHolder(tr("ui.hint.device_ip", nil))
	labels, _ := c.cfg.Get("device_labels").(map[string]any)
	if label, ok := labels[c.selectedDevice].(map[string]any); ok {
		name.SetText(strings.TrimSpace(fmt.Sprint(label["name"])))
		ip.SetText(strings.TrimSpace(fmt.Sprint(label["ip"])))
	}
	c.appendAction(tr("ui.action.assign_device", nil) + ": " + c.selectedDevice)
	content := container.NewVBox(
		widget.NewLabel(tr("ui.device.id", nil)+": "+c.selectedDevice),
		widget.NewLabel(tr("ui.hint.device_name", nil)), name,
		widget.NewLabel(tr("ui.hint.device_ip", nil)), ip,
	)
	confirm := dialog.NewCustomConfirm(
		tr("ui.action.assign_device", nil), tr("ui.action.save", nil), tr("ui.action.cancel_short", nil), content,
		func(ok bool) {
			if !ok {
				c.appendAction(tr("ui.action.cancelled", nil))
				return
			}
			if labels == nil {
				labels = map[string]any{}
			}
			labels[c.selectedDevice] = map[string]any{
				"name": strings.TrimSpace(name.Text),
				"ip":   strings.TrimSpace(ip.Text),
			}
			if !c.persistSetting("device_labels", labels) {
				return
			}
			c.appendAction(tr("ui.device.saved", map[string]any{"id": c.selectedDevice}))
			c.updateDevicesLabel()
			if c.deviceList != nil {
				c.deviceList.Refresh()
			}
		}, c.window,
	)
	confirm.Resize(fyne.NewSize(460, 240))
	confirm.Show()
}

func (c *clientApp) onServerCheck() {
	c.appendAction(tr("ui.action.server_check", nil))
	go func() {
		minimum := c.cfg.String("min_server_version")
		if minimum == "" {
			minimum = "3.16.0"
		}
		outdated, current, err := c.client.ServerOutdated(minimum)
		if err != nil {
			c.appendLog(tr("ui.server.check_failed", map[string]any{"detail": err.Error()}))
			return
		}
		if outdated {
			c.appendAction(tr("ui.server.outdated", map[string]any{"current": current, "minimum": minimum}))
		} else {
			c.appendAction(tr("ui.server.ok", map[string]any{"version": current}))
		}
		c.pollMetrics()
	}()
}

func (c *clientApp) onInstallUpdate() {
	c.appendAction(tr("ui.action.update_install", nil))
	dialog.ShowConfirm(tr("ui.confirm.update_title", nil), tr("ui.confirm.update_body", nil), func(ok bool) {
		if !ok {
			c.appendAction(tr("ui.action.cancelled", nil))
			return
		}
		go func() {
			binary, err := os.Executable()
			if err != nil {
				c.appendLog("Installazione aggiornamento fallita: " + err.Error())
				return
			}
			applied, versionName, err := update.Apply(c.cfg, c.client, binary)
			if err != nil {
				c.appendLog("Installazione aggiornamento fallita: " + err.Error())
				return
			}
			if applied {
				c.appendAction(tr("ui.update.installed", map[string]any{"version": versionName}))
			} else {
				c.appendAction(tr("main_window.update_none", nil))
			}
		}()
	}, c.window)
}

func (c *clientApp) setTheme(value string) {
	if c.cfg.String("go_theme") == value && variantFrom(value) == c.uiTheme.variant {
		return
	}
	if !c.persistSetting("go_theme", value) {
		return
	}
	c.uiTheme = newNAStheme(variantFrom(value))
	c.fyneApp.Settings().SetTheme(c.uiTheme)
	reason := ""
	if c.bannerReason != nil {
		reason = c.bannerReason.Text
	}
	c.window.SetContent(c.buildRoot())
	c.showSection(c.active)
	c.applyStatusUI(c.level, reason)
	c.appendLog(tr("settings.theme_title", nil) + ": " + value)
}
