// Package ui is the native Fyne client. It supports both X11 and Wayland.
package ui

import (
	"context"
	_ "embed"
	"fmt"
	"os"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"nasbox-go/internal/config"
	"nasbox-go/internal/conflicts"
	"nasbox-go/internal/deletesafety"
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

//go:embed icon.png
var iconBytes []byte

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
	section    sectionID
}

type clientApp struct {
	cfg       *config.Config
	store     *state.Store
	client    *remote.Client
	transfers *transfer.Transfer
	runner    *engine.Runner
	cancel    context.CancelFunc

	uiTheme *nasTheme
	fyneApp fyne.App
	window  fyne.Window
	setHint func(string)
	content *fyne.Container
	views   map[sectionID]fyne.CanvasObject
	navs    map[sectionID]*navButton
	active  sectionID

	headerTitle *widget.Label
	pillLabel   *widget.Label
	pillBG      *canvas.Rectangle
	hintLabel   *widget.Label
	eventLabel  *widget.Label

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
	logList        *widget.List
	transferList   *widget.List
	confGroups     []conflicts.Group
	confList       *widget.List
	versions       []history.Version
	histList       *widget.List

	browsePath          string
	browsePathLabel     *widget.Label
	browseEntries       []remote.BrowseEntry
	browseList          *widget.List
	mirrorLabel         *widget.Label
	selectiveDirs       []string
	selectiveList       *widget.List
	nasLabel            *widget.Label
	devicesLabel        *widget.Label
	monitorDevicesLabel *widget.Label

	resolveBtn *widget.Button
	restoreBtn *widget.Button
	deleteBtn  *widget.Button
	renameBtn  *widget.Button

	activityTabs *container.AppTabs
	fileTabs     *container.AppTabs
	nasTabs      *container.AppTabs
	settingsTabs *container.AppTabs

	selectedConflict string
	selectedVersion  string
	selectedBrowse   string
}

// Run starts the native client. It blocks until the window/tray exits.
func Run(cfg *config.Config, store *state.Store) {
	selectBackend(cfg)
	detectLanguage(cfg)
	fyneApp := app.NewWithID("it.nasbox.client")
	fyneApp.SetIcon(fyne.NewStaticResource("icon.png", iconBytes))
	window := fyneApp.NewWindow(fmt.Sprintf("%s %s", version.AppName, version.AppVersion))
	window.Resize(fyne.NewSize(1120, 760))

	client := remote.New(cfg)
	transfers := transfer.New(cfg, client)
	ctx, cancel := context.WithCancel(context.Background())
	c := &clientApp{
		cfg: cfg, store: store, client: client, transfers: transfers,
		cancel:  cancel,
		runner:  engine.NewRunner(cfg, store, client, transfers),
		fyneApp: fyneApp,
		window:  window,
		uiTheme: newNAStheme(variantFrom(cfg.String("go_theme"))),
		level:   statusStarting,
	}
	c.bannerGo = func() { c.onSync() }
	fyneApp.Settings().SetTheme(c.uiTheme)

	c.setHint = c.setHintText
	window.SetContent(c.buildRoot())
	c.showSection(sectionStatus)
	window.SetCloseIntercept(func() { window.Hide() })
	c.installTray()

	c.applyStatus(statusStarting, tr("status.sync_state_starting", nil))
	go c.consumeEvents()
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
	c.refreshConflicts()
	c.refreshHistory()
	c.refreshBrowse()
	c.refreshSelective()
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
	return container.NewBorder(header, footer, sidebar, nil, c.content)
}

func (c *clientApp) buildHeader() fyne.CanvasObject {
	brand := widget.NewLabelWithStyle(version.AppName, fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	c.headerTitle = widget.NewLabelWithStyle(tr("main_window.tab_status", nil),
		fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	left := container.NewVBox(brand, c.headerTitle)

	c.pillLabel = widget.NewLabel("")
	c.pillBG = canvas.NewRectangle(c.tint(theme.ColorNamePrimary))
	c.pillBG.CornerRadius = 10
	pill := container.NewStack(c.pillBG, container.NewPadded(c.pillLabel))

	syncButton := newHintButton(tr("ui.action.sync_short", nil), theme.MediaReplayIcon(),
		tr("status.sync_now_tooltip", nil), widget.HighImportance, c.onSync, c.setHint)
	c.pauseBtn = newHintButton(labelPause(c.runner.IsPaused()), theme.MediaPauseIcon(),
		tr("status.pause_tooltip", nil), widget.MediumImportance, c.onPause, c.setHint).Button
	right := container.NewHBox(pill, syncButton, c.pauseBtn)
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
	lang := container.NewHBox(
		c.smallButton("IT", "Italiano", func() { c.setLanguage("it") }),
		c.smallButton("EN", "English", func() { c.setLanguage("en") }),
	)
	themeRow := container.NewHBox(
		c.smallButton(tr("settings.theme_light", nil), tr("settings.theme_title", nil), func() { c.setTheme("light") }),
		c.smallButton(tr("settings.theme_dark", nil), tr("settings.theme_title", nil), func() { c.setTheme("dark") }),
	)
	body := container.NewVBox(
		widget.NewLabelWithStyle("NASBOX", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		line,
		container.NewVBox(items...),
		layout.NewSpacer(),
		widget.NewLabelWithStyle(tr("settings.language_title", nil), fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		lang,
		widget.NewLabelWithStyle(tr("settings.theme_title", nil), fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		themeRow,
	)
	return container.NewStack(spacer, bg, container.NewPadded(body))
}

func (c *clientApp) smallButton(text, hint string, onTap func()) fyne.CanvasObject {
	return newHintButton(text, nil, hint, widget.LowImportance, onTap, c.setHint)
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
	_ = hint
	item := &navButton{
		Button: button, bg: bg, accent: accent, section: id,
	}
	// Keep the object returned as a single layout unit: the accent bar is
	// always visible when this section is active.
	item.buttonView = container.NewBorder(nil, nil, accent, nil, button)
	c.navs[id] = item
	return item
}

func (c *clientApp) buildFooter() fyne.CanvasObject {
	c.eventLabel = widget.NewLabel("")
	c.eventLabel.Wrapping = fyne.TextTruncate
	c.hintLabel = widget.NewLabelWithStyle("", fyne.TextAlignLeading, fyne.TextStyle{Italic: true})
	line := canvas.NewRectangle(c.tint(theme.ColorNameSeparator))
	line.SetMinSize(fyne.NewSize(1, 1))
	return container.NewVBox(line, container.NewHBox(c.eventLabel, layout.NewSpacer(), c.hintLabel))
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
	for section, item := range c.navs {
		if section == id {
			item.bg.FillColor = c.tint(theme.ColorNameSelection)
			item.accent.FillColor = c.tint(theme.ColorNamePrimary)
			item.Button.Importance = widget.HighImportance
		} else {
			item.bg.FillColor = colorTransparent
			item.accent.FillColor = colorTransparent
			item.Button.Importance = widget.LowImportance
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
	c.views[sectionActivity] = container.NewVScroll(c.activityView())
	c.views[sectionFiles] = container.NewVScroll(c.filesView())
	c.views[sectionNAS] = container.NewVScroll(c.nasView())
	c.views[sectionSettings] = container.NewVScroll(c.settingsView())
}

func (c *clientApp) statusView() fyne.CanvasObject {
	banner := c.buildBanner()
	c.pauseBtnStatus = newHintButton(labelPause(c.runner.IsPaused()), theme.MediaPauseIcon(),
		tr("status.pause_tooltip", nil), widget.MediumImportance, c.onPause, c.setHint).Button
	c.detailsBtn = newHintButton(tr("ui.action.details", nil), theme.InfoIcon(),
		tr("ui.action.details_hint", nil), widget.LowImportance, func() {
			if c.bannerGo != nil {
				c.bannerGo()
			}
		}, c.setHint)
	actions := container.NewGridWithColumns(3,
		newHintButton(tr("ui.action.sync_short", nil), theme.MediaReplayIcon(),
			tr("status.sync_now_tooltip", nil), widget.HighImportance, c.onSync, c.setHint),
		c.pauseBtnStatus,
		newHintButton(tr("ui.action.audit_short", nil), theme.ConfirmIcon(),
			tr("audit.run_now_tooltip", nil), widget.MediumImportance, c.onAudit, c.setHint),
		newHintButton(tr("ui.action.preview_short", nil), theme.VisibilityIcon(),
			tr("transfers.queue_note", nil), widget.MediumImportance, c.onPreview, c.setHint),
		newHintButton(tr("ui.action.undo_short", nil), theme.ContentUndoIcon(),
			tr("ui.action.undo_hint", nil), widget.MediumImportance, c.onUndo, c.setHint),
		c.detailsBtn,
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
		c.sectionTitle(tr("assurance.guidance_title", nil), ""),
		stats,
		c.surface(container.NewVBox(detail, backend), ""),
	)
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
		c.bannerNext,
	)
	return container.NewStack(c.bannerBG, container.NewPadded(content))
}

func (c *clientApp) activityView() fyne.CanvasObject {
	c.transferList = newTextList(
		func() int { return len(c.logLines) },
		func(id int, label *widget.Label) { label.SetText(c.logLines[id]) },
	)
	c.logList = newTextList(
		func() int { return len(c.logLines) },
		func(id int, label *widget.Label) { label.SetText(c.logLines[id]) },
	)
	transfers := container.NewBorder(
		container.NewVBox(
			c.sectionTitle(tr("main_window.tab_transfers", nil), tr("transfers.queue_note", nil)),
		),
		nil, nil, nil, container.NewVScroll(c.transferList),
	)
	logPane := container.NewBorder(
		container.NewVBox(
			c.sectionTitle(tr("main_window.tab_log", nil), tr("log.filter_tooltip", nil)),
		),
		nil, nil, nil, container.NewVScroll(c.logList),
	)
	c.activityTabs = container.NewAppTabs(
		container.NewTabItem(tr("main_window.tab_transfers", nil), transfers),
		container.NewTabItem(tr("main_window.tab_log", nil), logPane),
	)
	return c.activityTabs
}

func (c *clientApp) filesView() fyne.CanvasObject {
	c.confList = newTextList(
		func() int { return len(c.confGroups) },
		func(id int, label *widget.Label) {
			group := c.confGroups[id]
			label.SetText(fmt.Sprintf("%s  (%s)", group.Original,
				tr("conflicts.count", map[string]any{"groups": 1, "files": len(group.Candidates)})))
		},
	)
	c.confList.OnSelected = func(id widget.ListItemID) {
		if int(id) < len(c.confGroups) && len(c.confGroups[id].Candidates) > 0 {
			c.selectedConflict = c.confGroups[id].Candidates[0]
			if c.resolveBtn != nil {
				c.resolveBtn.Enable()
			}
		}
	}
	c.histList = newTextList(
		func() int { return len(c.versions) },
		func(id int, label *widget.Label) {
			item := c.versions[id]
			label.SetText(fmt.Sprintf("%s  —  %s", item.Original, item.Timestamp))
		},
	)
	c.histList.OnSelected = func(id widget.ListItemID) {
		if int(id) < len(c.versions) {
			c.selectedVersion = c.versions[id].TrashPath
			if c.restoreBtn != nil {
				c.restoreBtn.Enable()
			}
		}
	}
	c.browseList = newTextList(
		func() int { return len(c.browseEntries) },
		func(id int, label *widget.Label) {
			entry := c.browseEntries[id]
			label.SetText(fmt.Sprintf("%s   %s   %d B", entry.Name, entry.Kind, entry.Size))
		},
	)
	c.browseList.OnSelected = func(id widget.ListItemID) {
		if int(id) < len(c.browseEntries) {
			c.selectedBrowse = c.browseEntries[id].Name
			if c.deleteBtn != nil {
				c.deleteBtn.Enable()
			}
			if c.renameBtn != nil {
				c.renameBtn.Enable()
			}
		}
	}
	c.browsePathLabel = widget.NewLabel("")
	c.deleteBtn = newHintButton(tr("browse.delete_btn", nil), theme.DeleteIcon(),
		tr("browse.confirm_delete_title", nil), widget.DangerImportance, c.onBrowseDelete, c.setHint).Button
	c.renameBtn = newHintButton(tr("browse.rename_btn", nil), theme.DocumentCreateIcon(),
		"", widget.LowImportance, c.onBrowseRename, c.setHint).Button

	browsePane := container.NewBorder(
		container.NewVBox(
			c.sectionTitle(tr("main_window.tab_browse", nil), tr("browse.note", nil)),
			c.browsePathLabel,
			container.NewHBox(
				newHintButton(tr("browse.up_btn", nil), theme.NavigateBackIcon(),
					tr("main_window.tab_browse_tooltip", nil), widget.LowImportance, c.onBrowseUp, c.setHint),
				newHintButton(tr("browse.refresh_btn", nil), theme.ViewRefreshIcon(),
					"", widget.LowImportance, c.refreshBrowse, c.setHint),
				c.deleteBtn,
				c.renameBtn,
				newHintButton(tr("ui.action.download", nil), theme.DownloadIcon(),
					"", widget.MediumImportance, c.onBrowseDownload, c.setHint),
			),
		),
		nil, nil, nil, container.NewVScroll(c.browseList),
	)
	c.resolveBtn = newHintButton(tr("ui.action.resolve", nil), theme.ConfirmIcon(),
		tr("conflicts.confirm_title", nil), widget.HighImportance, c.onResolveConflict, c.setHint).Button
	conflictPane := container.NewBorder(
		container.NewVBox(
			c.sectionTitle(tr("conflicts.list_title", nil), tr("settings.maint_conflict_note", nil)),
		),
		container.NewHBox(
			newHintButton(tr("conflicts.refresh_btn", nil), theme.ViewRefreshIcon(),
				"", widget.LowImportance, c.refreshConflicts, c.setHint),
			c.resolveBtn,
			newHintButton(tr("ui.action.keep_both", nil), theme.ContentCopyIcon(),
				"", widget.MediumImportance, c.onKeepBoth, c.setHint),
		),
		nil, nil, container.NewVScroll(c.confList),
	)
	c.restoreBtn = newHintButton(tr("ui.action.restore", nil), theme.ContentRedoIcon(),
		tr("history.restore_tooltip", nil), widget.HighImportance, c.onRestoreVersion, c.setHint).Button
	historyPane := container.NewBorder(
		container.NewVBox(
			c.sectionTitle(tr("main_window.tab_history", nil), tr("settings.maint_delete_tooltip", nil)),
		),
		container.NewHBox(
			newHintButton(tr("history.refresh_btn", nil), theme.ViewRefreshIcon(),
				"", widget.LowImportance, c.refreshHistory, c.setHint),
			c.restoreBtn,
			newHintButton(tr("ui.action.remote_restore", nil), theme.ContentRedoIcon(),
				"", widget.MediumImportance, c.onRemoteRestore, c.setHint),
			newHintButton(tr("ui.action.prune", nil), theme.DeleteIcon(),
				"", widget.LowImportance, c.onPrune, c.setHint),
		),
		nil, nil, container.NewVScroll(c.histList),
	)
	c.fileTabs = container.NewAppTabs(
		container.NewTabItem(tr("main_window.tab_browse", nil), browsePane),
		container.NewTabItem(tr("main_window.tab_conflicts", nil), conflictPane),
		container.NewTabItem(tr("main_window.tab_history", nil), historyPane),
	)
	return c.fileTabs
}

func (c *clientApp) nasView() fyne.CanvasObject {
	c.nasLabel = widget.NewLabel(tr("status.diagnostics_loading", nil))
	c.nasLabel.Wrapping = fyne.TextWrapWord
	c.devicesLabel = widget.NewLabel(tr("devices.empty", nil))
	c.devicesLabel.Wrapping = fyne.TextWrapWord
	c.monitorDevicesLabel = widget.NewLabel(tr("devices.empty", nil))
	c.monitorDevicesLabel.Wrapping = fyne.TextWrapWord

	monitorPane := container.NewVBox(
		c.sectionTitle(tr("metrics.nasbox_title", nil), tr("settings.metrics_refresh_note", nil)),
		c.surface(container.NewVBox(widget.NewLabelWithStyle(tr("main_window.tab_metrics", nil),
			fyne.TextAlignLeading, fyne.TextStyle{Bold: true}), c.nasLabel), ""),
		c.surface(container.NewVBox(widget.NewLabelWithStyle(tr("status.nas_overview_title", nil),
			fyne.TextAlignLeading, fyne.TextStyle{Bold: true}), c.monitorDevicesLabel), ""),
		newHintButton(tr("ui.action.server_check", nil), theme.ViewRefreshIcon(),
			"", widget.MediumImportance, c.onServerCheck, c.setHint),
	)
	devicesPane := container.NewVBox(
		c.sectionTitle(tr("devices.title", nil), tr("a11y.devices_label", nil)),
		c.surface(container.NewVBox(c.devicesLabel), ""),
	)
	c.nasTabs = container.NewAppTabs(
		container.NewTabItem(tr("main_window.tab_metrics", nil), monitorPane),
		container.NewTabItem(tr("devices.title", nil), devicesPane),
	)
	return c.nasTabs
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
		c.sectionTitle(tr("ui.group.general", nil), ""),
		c.surface(container.NewVBox(
			widget.NewLabelWithStyle(tr("settings.language_title", nil), fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
			c.note(tr("settings.language_note", nil)),
			container.NewHBox(
				c.smallButton(tr("settings.language_it", nil), "", func() { c.setLanguage("it") }),
				c.smallButton(tr("settings.language_en", nil), "", func() { c.setLanguage("en") }),
			),
			widget.NewLabelWithStyle(tr("settings.theme_title", nil), fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
			c.note(tr("settings.theme_note", nil)),
			container.NewHBox(
				c.smallButton(tr("settings.theme_light", nil), "", func() { c.setTheme("light") }),
				c.smallButton(tr("settings.theme_dark", nil), "", func() { c.setTheme("dark") }),
			),
		), ""),
		c.surface(container.NewVBox(
			widget.NewLabelWithStyle(tr("settings.nas_box_title", nil), fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
			widget.NewLabel(fmt.Sprintf("%s: %d / %d kbps", tr("settings.upload_label", nil),
				c.cfg.Int("bandwidth_upload_kbps"), c.cfg.Int("bandwidth_download_kbps"))),
			c.note(fmt.Sprintf("%s: %s", tr("settings.info_config_label", nil), valueOrDash(c.cfg.Path))),
			c.note(fmt.Sprintf("%s: %s", tr("status.config_local_folder", nil), valueOrDash(c.cfg.LocalRoot()))),
			widget.NewLabel(fmt.Sprintf("%s: %s", tr("ui.settings.display", nil), displayBackend(c.cfg))),
		), ""),
	)
	folders := container.NewVBox(
		c.sectionTitle(tr("ui.group.folders", nil), tr("mirrors.authority_note", nil)),
		c.surface(container.NewVBox(
			widget.NewLabelWithStyle(tr("main_window.tab_mirrors", nil), fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
			c.mirrorLabel,
			container.NewHBox(newHintButton(tr("ui.action.sync_folders", nil), theme.ViewRefreshIcon(),
				tr("mirrors.sync_now_tooltip", nil), widget.MediumImportance, c.onMirrorSync, c.setHint)),
		), ""),
		c.surface(container.NewVBox(
			widget.NewLabelWithStyle(tr("dialogs.selective.title", nil), fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
			c.note(tr("dialogs.selective.intro", nil)),
			container.NewVScroll(c.selectiveList),
		), ""),
	)
	about := container.NewVBox(
		c.sectionTitle(tr("ui.group.about", nil), ""),
		c.surface(container.NewVBox(
			widget.NewLabel(fmt.Sprintf("%s: %s %s", tr("settings.info_version_label", nil), version.AppName, version.AppVersion)),
			widget.NewLabel(fmt.Sprintf("%s: %s", tr("settings.info_min_server_label", nil), valueOrDash(c.cfg.String("min_server_version")))),
			newHintButton(tr("ui.action.check_update", nil), theme.ViewRefreshIcon(),
				tr("main_window.version_tooltip", nil), widget.MediumImportance, c.onCheckUpdate, c.setHint),
			newHintButton(tr("ui.action.update_install", nil), theme.ViewRefreshIcon(),
				"", widget.HighImportance, c.onInstallUpdate, c.setHint),
			newHintButton(tr("ui.action.confirm_delete", nil), theme.ConfirmIcon(),
				tr("status.confirm_deletes_tooltip", nil), widget.DangerImportance, c.onConfirmDelete, c.setHint),
		), ""),
	)
	c.settingsTabs = container.NewAppTabs(
		container.NewTabItem(tr("ui.group.general", nil), container.NewVScroll(general)),
		container.NewTabItem(tr("ui.group.folders", nil), container.NewVScroll(folders)),
		container.NewTabItem(tr("ui.group.about", nil), container.NewVScroll(about)),
	)
	return c.settingsTabs
}

// ---------------------------------------------------------------------------
// Status semantics
// ---------------------------------------------------------------------------

func (c *clientApp) applyStatus(level statusLevel, reason string) {
	fyne.Do(func() { c.applyStatusUI(level, reason) })
}

func (c *clientApp) applyStatusUI(level statusLevel, reason string) {
	c.level = level
	var title, next, action string
	var colour fyne.ThemeColorName
	var goFn func()
	switch level {
	case statusOK:
		title, next, colour = tr("overview.banner_ok", nil), tr("ui.status.ok_next", nil), theme.ColorNameSuccess
		action, goFn = tr("ui.action.sync_short", nil), func() { c.onSync() }
	case statusProgress:
		title, next, colour = tr("overview.banner_progress", nil), tr("ui.status.progress_next", nil), theme.ColorNamePrimary
		action, goFn = tr("main_window.tab_transfers", nil), func() { c.navigate(sectionActivity, 0) }
	case statusAttention:
		title, next, colour = tr("overview.banner_warning", nil), tr("ui.status.attention_next", nil), theme.ColorNameWarning
		action, goFn = tr("main_window.tab_log", nil), func() { c.navigate(sectionActivity, 1) }
	case statusBlocked:
		title, next, colour = tr("ui.status.blocked", nil), tr("ui.status.blocked_next", nil), theme.ColorNameError
		action, goFn = tr("main_window.tab_log", nil), func() { c.navigate(sectionActivity, 1) }
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
	if c.detailsBtn != nil {
		// The label stays stable ("Vai ai dettagli"); the banner carries the
		// contextual reason and the next step. The hint is the actionable target.
		c.detailsBtn.hint = fmt.Sprintf("%s — %s", action, next)
	}

	c.pillBG.FillColor = c.tint(colour)
	c.pillBG.Refresh()
	c.pillLabel.SetText(title)
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
	c.logLines = append([]string{time.Now().Format("15:04:05") + "  " + line}, c.logLines...)
	if len(c.logLines) > 300 {
		c.logLines = c.logLines[:300]
	}
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
		if c.eventLabel != nil {
			c.eventLabel.SetText(line)
		}
	})
}

func (c *clientApp) setHintText(text string) {
	fyne.Do(func() {
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
			c.appendLog(reason)
			c.refreshConflicts()
			c.refreshHistory()
			c.refreshStats()
		case "status":
			c.applyStatus(statusProgress, tr("status.sync_requested", nil))
			c.appendLog(tr("status.sync_state_active", map[string]any{
				"direction": tr("status.sync_state_preparing", nil),
			}))
		case "blocked":
			c.applyStatus(statusBlocked, event.Message)
			c.appendLog(fmt.Sprintf("%s: %s", tr("safety.blocked_proposed", nil), event.Message))
		case "error":
			level := statusAttention
			if looksOffline(event.Message) {
				level = statusOffline
			}
			c.applyStatus(level, event.Message)
			c.appendLog(fmt.Sprintf("%s: %s", tr("log.action.error", nil), event.Message))
		default:
			c.appendLog(event.Message)
		}
	}
}

// ---------------------------------------------------------------------------
// Data refresh
// ---------------------------------------------------------------------------

func (c *clientApp) refreshStats() {
	tracked := c.store.EntryCount(c.cfg.RepositoryID())
	undo := c.store.UndoCount(c.cfg.RepositoryID(), 24)
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
		if c.confList != nil {
			c.confList.Refresh()
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
		if c.browseList != nil {
			c.browseList.Refresh()
		}
		if c.browsePathLabel != nil {
			c.browsePathLabel.SetText(tr("browse.path_label", map[string]any{
				"path": valueOrDash(c.browsePath),
			}))
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
		text := fmt.Sprintf(tr("ui.nas.config_short", nil), c.cfg.String("nas_lan"),
			tr("settings.info_version_label", nil), values["VERSION"]) + "\n"
		text += fmt.Sprintf("%s: %s%% · %s %s\n",
			tr("metrics.disk_title", nil), values["DISK_USAGE_PERCENT"],
			values["DISK_AVAILABLE_BYTES"], tr("metrics.disk_label", nil))
		text += fmt.Sprintf("%s: %s / %s / %s · %s %s\n",
			tr("metrics.load_title", nil), values["LOAD_1"], values["LOAD_5"], values["LOAD_15"],
			tr("metrics.memory_title", nil), values["MEM_AVAILABLE_BYTES"])
		text += fmt.Sprintf("%s: %s (%s) · %s %s",
			tr("settings.nas_fact_lock_label", nil), values["NASBOX_LOCK_HELD"],
			values["NASBOX_LOCK_PHASE"], tr("metrics.queue_label", nil), values["NASBOX_QUEUE_COUNT"])
		fyne.Do(func() {
			if c.nasLabel != nil {
				c.nasLabel.SetText(text)
			}
		})
	}
	devices, err := c.client.Devices()
	if err == nil {
		text := tr("devices.empty", nil)
		if len(devices) > 0 {
			text = strings.Join(devices, "\n")
		}
		fyne.Do(func() {
			if c.devicesLabel != nil {
				c.devicesLabel.SetText(text)
			}
			if c.monitorDevicesLabel != nil {
				c.monitorDevicesLabel.SetText(text)
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
	go func() {
		entries, err := c.store.RecentUndo(c.cfg.RepositoryID(), 24, 500)
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
				_ = c.store.MarkUndoApplied(c.cfg.RepositoryID(), batchByPath[path], []string{path})
				restored++
			}
		}
		c.appendLog(tr("history.restore_all_done_body", map[string]any{"count": restored}))
		c.refreshStats()
	}()
}

func (c *clientApp) onResolveConflict() {
	if c.selectedConflict == "" {
		c.appendLog(tr("conflicts.copy_name_none", nil))
		return
	}
	chosen := c.selectedConflict
	dialog.ShowConfirm(tr("conflicts.confirm_title", nil),
		fmt.Sprintf("%s: %s", tr("conflicts.keep_label", nil), chosen), func(ok bool) {
			if !ok {
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
	if len(parts) > 0 && parts[0] != "" {
		c.browsePath = strings.Join(parts[:len(parts)-1], "/")
	} else {
		c.browsePath = ""
	}
	c.selectedBrowse = ""
	c.refreshBrowse()
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
	dialog.ShowConfirm(tr("browse.confirm_delete_title", nil),
		tr("ui.dialog.confirm_delete_body", map[string]any{"path": path}), func(ok bool) {
			if !ok {
				return
			}
			go func() {
				status, err := c.client.BrowseDelete(path, remote.RunTimestamp(), c.store.DeviceID())
				if err != nil {
					c.appendLog(tr("browse.delete_failed_title", nil) + ": " + err.Error())
					return
				}
				c.appendLog(tr("log.action.browse_delete", nil) + ": " + status)
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
	dialog.ShowEntryDialog(tr("browse.rename_btn", nil), tr("ui.dialog.new_name", nil), func(name string) {
		if name == "" {
			return
		}
		src, dst := current, name
		if c.browsePath != "" {
			src = c.browsePath + "/" + src
			dst = c.browsePath + "/" + dst
		}
		go func() {
			status, err := c.client.BrowseRename(src, dst, remote.RunTimestamp(), c.store.DeviceID())
			if err != nil {
				c.appendLog(tr("browse.rename_failed_title", nil) + ": " + err.Error())
				return
			}
			c.appendLog(tr("log.action.browse_rename", nil) + ": " + status)
			c.refreshBrowse()
		}()
	}, c.window)
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
	var target history.Version
	for _, item := range c.versions {
		if item.TrashPath == chosen {
			target = item
			break
		}
	}
	dialog.ShowConfirm(tr("history.confirm_restore_title", nil),
		tr("history.confirm_restore_body", map[string]any{
			"path": target.Original, "timestamp": target.Timestamp,
		}), func(ok bool) {
			if !ok {
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
	c.appendLog(tr("settings.language_title", nil) + ": " + language)
	dialog.ShowInformation(tr("settings.language_title", nil),
		tr("settings.language_note", nil), c.window)
}

func (c *clientApp) onConfirmDelete() {
	until, err := deletesafety.Shared().Confirm(c.cfg)
	if err != nil {
		c.appendLog("Conferma cancellazioni non riuscita: " + err.Error())
		return
	}
	c.appendLog("Cancellazioni di massa confermate fino a " + time.Unix(int64(until), 0).Format("15:04:05"))
}

func (c *clientApp) onCancel() {
	c.runner.Cancel()
}

func (c *clientApp) onBrowseDownload() {
	if c.selectedBrowse == "" {
		c.appendLog("Seleziona prima un file dalla lista")
		return
	}
	relative := c.selectedBrowse
	if c.browsePath != "" {
		relative = c.browsePath + "/" + relative
	}
	go func() {
		if err := c.transfers.DownloadPath(relative); err != nil {
			c.appendLog("Download non riuscito: " + err.Error())
			return
		}
		c.appendLog("Scaricato: " + relative)
	}()
}

func (c *clientApp) onKeepBoth() {
	if c.selectedConflict == "" {
		c.appendLog("Seleziona prima una copia conflitto")
		return
	}
	go func() {
		for _, group := range c.confGroups {
			for _, candidate := range group.Candidates {
				if candidate != c.selectedConflict {
					continue
				}
				kept, err := conflicts.KeepBoth(c.cfg.LocalRoot(), group, candidate)
				if err != nil {
					c.appendLog("Keep both non riuscito: " + err.Error())
				} else {
					c.appendLog("Versione conservata: " + kept)
					c.selectedConflict = ""
					c.refreshConflicts()
				}
				return
			}
		}
	}()
}

func (c *clientApp) onRemoteRestore() {
	dialog.ShowEntryDialog("Ripristina versione NAS",
		"Inserisci percorso e suffisso separati da | (percorso|2026-11-24--07-00-00-000000Z)",
		func(value string) {
			parts := strings.SplitN(value, "|", 2)
			if len(parts) != 2 {
				c.appendLog("Formato non valido: percorso|suffisso")
				return
			}
			go func() {
				status, err := c.client.RestoreVersion(strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1]), c.store.DeviceID())
				if err != nil {
					c.appendLog("Ripristino remoto fallito: " + err.Error())
					return
				}
				c.appendLog("Ripristino remoto: " + status)
			}()
		}, c.window)
}

func (c *clientApp) onPrune() {
	go func() {
		removed, err := history.Prune(c.cfg.Int("retention_days_local"))
		if err != nil {
			c.appendLog("Pulizia storico fallita: " + err.Error())
			return
		}
		c.appendLog("Rimossi " + fmt.Sprint(removed) + " file dallo storico locale")
		c.refreshHistory()
	}()
}

func (c *clientApp) onServerCheck() {
	go func() {
		minimum := c.cfg.String("min_server_version")
		if minimum == "" {
			minimum = "3.16.0"
		}
		outdated, current, err := c.client.ServerOutdated(minimum)
		if err != nil {
			c.appendLog("Controllo server fallito: " + err.Error())
			return
		}
		if outdated {
			c.appendLog("Server obsoleto: " + current + " (richiesto " + minimum + ")")
		} else {
			c.appendLog("Server ok: " + current)
		}
	}()
}

func (c *clientApp) onInstallUpdate() {
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
			c.appendLog("Aggiornato a " + versionName + " — riavvia il client")
		} else {
			c.appendLog("Nessun aggiornamento disponibile")
		}
	}()
}

func (c *clientApp) setTheme(value string) {
	if c.cfg.String("go_theme") == value && variantFrom(value) == c.uiTheme.variant {
		return
	}
	c.cfg.Set("go_theme", value, true)
	c.uiTheme = newNAStheme(variantFrom(value))
	c.fyneApp.Settings().SetTheme(c.uiTheme)
	c.window.SetContent(c.buildRoot())
	c.appendLog(tr("settings.theme_title", nil) + ": " + value)
}
