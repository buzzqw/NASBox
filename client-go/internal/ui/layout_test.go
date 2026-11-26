package ui

import (
	"bytes"
	"nasbox-go/internal/update"
	"os"
	"strings"
	"testing"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"nasbox-go/internal/conflicts"
	"nasbox-go/internal/remote"
)

func walkUI(root fyne.CanvasObject, visit func(fyne.CanvasObject)) {
	if root == nil {
		return
	}
	visit(root)
	switch value := root.(type) {
	case *fyne.Container:
		for _, child := range value.Objects {
			walkUI(child, visit)
		}
	case *container.AppTabs:
		for _, item := range value.Items {
			walkUI(item.Content, visit)
		}
	case *container.Scroll:
		walkUI(value.Content, visit)
	case *listRowObject:
		walkUI(value.content, visit)
	case *hintButton:
		walkUI(&value.Button, visit)
	case *hintArea:
		walkUI(value.Widget, visit)
	case *widthWidget:
		walkUI(value.content, visit)
	case *surfacePanel:
		walkUI(value.content, visit)
	case *tapCard:
		walkUI(value.content, visit)
	}
}

func findUI[T fyne.CanvasObject](root fyne.CanvasObject) []T {
	result := []T{}
	walkUI(root, func(object fyne.CanvasObject) {
		if match, ok := object.(T); ok {
			result = append(result, match)
		}
	})
	return result
}

func TestNavigationSelectionHasBackgroundBarAndMarker(t *testing.T) {
	c := newTestClient(t)
	c.showSection(sectionFiles)
	selected := c.navs[sectionFiles]
	if selected.bg.FillColor != c.tint(theme.ColorNameSelection) {
		t.Fatal("selected navigation background is not visible")
	}
	if selected.accent.FillColor != c.tint(theme.ColorNamePrimary) {
		t.Fatal("selected navigation accent bar is not visible")
	}
	if !selected.selected || !selected.text.TextStyle.Bold || selected.text.Color != c.tint(theme.ColorNamePrimary) {
		t.Fatal("selected item must be bold and use the accent colour")
	}
	foundBackground, foundAccent := false, false
	for _, object := range test.WidgetRenderer(selected).Objects() {
		foundBackground = foundBackground || object == selected.bg
		foundAccent = foundAccent || object == selected.accent
	}
	if !foundBackground || !foundAccent {
		t.Fatal("navigation visuals are not part of the renderer")
	}
	if c.navs[sectionStatus].bg.FillColor != colorTransparent {
		t.Fatal("inactive navigation item still has a selection background")
	}
}

func TestHeaderStatusIsSingleLine(t *testing.T) {
	c := newTestClient(t)
	c.applyStatusUI(statusBlocked, strings.Repeat("motivo molto lungo ", 20))
	if c.headerStatus.Truncation != fyne.TextTruncateEllipsis {
		t.Fatal("sidebar status may wrap into a vertical strip")
	}
	if c.statusDot.FillColor != c.tint(theme.ColorNameError) {
		t.Fatal("status dot must use the semantic colour")
	}
	if c.bannerTitle == c.bannerReason || c.bannerReason == c.bannerNext || c.bannerTitle == c.bannerNext {
		t.Fatal("banner state, reason and next action must be separate labels")
	}
	if size := c.window.Content().MinSize(); size.Width > 1120 || size.Height > 760 {
		t.Fatalf("minimum size %v exceeds 1120x760", size)
	}
}

func TestActivityRecentTransfersAndLogAreIndependent(t *testing.T) {
	c := newTestClient(t)
	c.appendLog("technical-only")
	c.appendActivity("recent-only")
	c.appendTransfer("transfer-only")
	if len(c.logLines) != 1 || len(c.activityLines) != 1 || len(c.transferLines) != 1 {
		t.Fatalf("lists are not independent: log=%d recent=%d transfers=%d",
			len(c.logLines), len(c.activityLines), len(c.transferLines))
	}
	if !strings.Contains(c.logLines[0], "technical-only") || !strings.Contains(c.activityLines[0], "recent-only") ||
		!strings.Contains(c.transferLines[0], "transfer-only") {
		t.Fatal("an event leaked into a different activity list")
	}
}

func TestBrowsePathAndSelectionState(t *testing.T) {
	c := newTestClient(t)
	c.browsePath = "Documenti"
	if got := c.joinBrowsePath("Progetti"); got != "Documenti/Progetti" {
		t.Fatalf("joined path = %q", got)
	}
	c.updateBreadcrumbs()
	buttons := findUI[*widget.Button](c.breadcrumbs)
	if len(buttons) != 2 || buttons[0].Text != "/" || buttons[1].Text != "Documenti" {
		t.Fatalf("breadcrumb buttons = %#v", buttons)
	}
	c.selectedBrowse = ""
	c.updateBrowseSelection()
	if !c.browseDeleteBtn.Disabled() || !c.browseRenameBtn.Disabled() || !c.browseDownloadBtn.Disabled() {
		t.Fatal("file actions enabled without a selection")
	}
	c.selectedBrowse = "preventivo.txt"
	c.updateBrowseSelection()
	if c.browseDeleteBtn.Disabled() || c.browseRenameBtn.Disabled() || c.browseDownloadBtn.Disabled() {
		t.Fatal("file actions not enabled after selecting a file")
	}
}

func TestConflictChoiceIncludesOriginalAndAlternatives(t *testing.T) {
	c := newTestClient(t)
	c.selectConflictGroup(conflicts.Group{
		Original:   "documento.txt",
		Candidates: []string{"documento.txt (conflitto da PC-134 2026-11-24--10-30-00-000000Z)"},
	})
	if len(c.confChoices) != 2 || c.confChoices[0] != "documento.txt" {
		t.Fatalf("conflict choices = %#v", c.confChoices)
	}
	if c.selectedConflict != "documento.txt" || !c.keepBothBtn.Disabled() {
		t.Fatal("original should be selected and Keep both disabled")
	}
	c.confChoiceList.OnSelected(1)
	if c.selectedConflict != c.confChoices[1] || c.keepBothBtn.Disabled() {
		t.Fatal("alternative selection did not enable Keep both")
	}
}

func TestMetricsAndAssignedDeviceNameAreReadable(t *testing.T) {
	c := newTestClient(t)
	c.updateMetricsUI(map[string]string{
		"COLLECTED_AT_EPOCH": "1792910400", "UPTIME_SECONDS": "90061",
		"LOAD_1": "0.24", "LOAD_5": "0.31", "LOAD_15": "0.28",
		"CPU_TOTAL_TICKS": "100", "CPU_IDLE_TICKS": "40", "CPU_IOWAIT_TICKS": "5",
		"MEM_TOTAL_BYTES": "4294967296", "MEM_AVAILABLE_BYTES": "3221225472",
		"SWAP_TOTAL_BYTES": "0", "SWAP_FREE_BYTES": "0",
		"DISK_TOTAL_BYTES": "500000000000", "DISK_USED_BYTES": "315000000000",
		"DISK_USAGE_PERCENT": "63", "DISK_AVAILABLE_BYTES": "214748364800",
		"DISK_READ_BYTES": "2000", "DISK_WRITE_BYTES": "3000",
		"NET_RX_BYTES": "4000", "NET_TX_BYTES": "5000",
		"NET_RX_PACKETS": "40", "NET_TX_PACKETS": "50", "NET_INTERFACE_COUNT": "2",
		"NASBOX_LOCK_HELD":  "false",
		"NASBOX_LOCK_PHASE": "idle", "NASBOX_QUEUE_COUNT": "0",
	})
	if !strings.Contains(c.diskValue.Text, "63%") || !strings.Contains(c.diskValue.Text, "200.0 GB") {
		t.Fatalf("disk metric is not human readable: %q", c.diskValue.Text)
	}
	if c.diskBar.Value != 0.63 {
		t.Fatalf("disk progress = %v", c.diskBar.Value)
	}
	c.updateDevicesUI([]string{"pc-122|1792910400|42|ACTIVE", "pc-134|1792906800|17|"})
	_ = c.cfg.Set("device_labels", map[string]any{
		"pc-122": map[string]any{"name": "PC Principale", "ip": "192.168.1.122"},
	}, false)
	if got := c.deviceName(c.deviceEntries[0]); got != "PC Principale" {
		t.Fatalf("assigned device name = %q", got)
	}
	if !strings.Contains(c.deviceSubtitle(c.deviceEntries[0]), "192.168.1.122") {
		t.Fatalf("assigned IP missing: %q", c.deviceSubtitle(c.deviceEntries[0]))
	}
	c.deviceList.OnSelected(0)
	if c.selectedDevice != "pc-122" {
		t.Fatalf("selected device ID = %q", c.selectedDevice)
	}
}

func TestSettingsChoicesUseDropdowns(t *testing.T) {
	c := newTestClient(t)
	general := findUI[*widget.Select](c.settingsAppearance())
	if len(general) != 3 {
		t.Fatalf("general dropdown count = %d, want language, theme and backend on one row", len(general))
	}
	advanced := findUI[*widget.Select](c.settingsAdvanced())
	if len(advanced) < 1 {
		t.Fatalf("advanced dropdown count = %d, want the tray choice", len(advanced))
	}
	scheduleAdd := false
	for _, button := range findUI[*widget.Button](c.settingsAdvanced()) {
		if button.Text == tr("settings.schedule_add_btn", nil) {
			scheduleAdd = true
		}
	}
	if !scheduleAdd {
		t.Fatal("scheduled pause editor is missing from advanced settings")
	}
	buttons := findUI[*widget.Button](c.settingsAppearance())
	for _, button := range buttons {
		if button.Text == "Italiano" || button.Text == "English" || button.Text == "Chiaro" || button.Text == "Scuro" {
			t.Fatalf("choice still rendered as a button: %q", button.Text)
		}
	}
}

func TestBuildingSettingsDoesNotWriteConfig(t *testing.T) {
	c := newTestClient(t)
	before := c.cfg.String("go_theme")
	_ = c.settingsView()
	if got := c.cfg.String("go_theme"); got != before {
		t.Fatalf("building settings changed theme from %q to %q", before, got)
	}
	if len(c.logLines) != 0 || len(c.activityLines) != 0 {
		t.Fatal("building settings emitted action feedback")
	}
}

func TestDestructiveActionsOpenConfirmation(t *testing.T) {
	c := newTestClient(t)
	for name, action := range map[string]func(){
		"undo":     c.onUndo,
		"prune":    c.onPrune,
		"mass del": c.onConfirmDelete,
		// Installing an update always goes through the release-notes dialog
		// (it is shown after the NAS check, here directly).
		"update": func() {
			c.appendLog("update")
			c.showUpdateDialog(&update.Manifest{Version: "9.9.9", Notes: "note"})
		},
		"remote":     c.onRemoteRestore,
		"add mirror": c.onAddMirror,
	} {
		t.Run(name, func(t *testing.T) {
			before := len(c.logLines)
			action()
			if c.window.Canvas().Overlays().Top() == nil {
				t.Fatal("destructive action did not ask for confirmation")
			}
			if len(c.logLines) == before {
				t.Fatal("action did not update the footer/log")
			}
			if overlay, ok := c.window.Canvas().Overlays().Top().(interface{ Hide() }); ok {
				overlay.Hide()
			}
		})
	}
}

func TestTrayAnimationAssetsArePreserved(t *testing.T) {
	if len(trayIcons) != 6 {
		t.Fatalf("tray icon count = %d", len(trayIcons))
	}
	if bytes.Equal(iconSyncingBytes, iconSyncingAltBytes) {
		t.Fatal("tray animation frames are identical")
	}
	if iconStateForLevel(statusProgress) != "syncing" || iconStateForLevel(statusOK) != "synced" {
		t.Fatal("semantic tray icon mapping changed")
	}
}

func TestCustomRowsAndWidthConstraintsAreRenderable(t *testing.T) {
	c := newTestClient(t)
	item := c.browseList.CreateItem()
	widgetObject, ok := item.(fyne.Widget)
	if !ok {
		t.Fatal("custom list row is not a Fyne widget and will not be painted")
	}
	if renderer := test.WidgetRenderer(widgetObject); len(renderer.Objects()) == 0 {
		t.Fatal("custom list row renderer has no children")
	}
	label := widget.NewLabel("visible")
	constrained, ok := minWidth(label, 120).(fyne.Widget)
	if !ok || len(test.WidgetRenderer(constrained).Objects()) != 1 {
		t.Fatal("width-constrained settings/header content is not renderable")
	}
}

func TestBrowseFolderRowUsesFolderIcon(t *testing.T) {
	c := newTestClient(t)
	c.browseEntries = []remote.BrowseEntry{{Name: "Archivio", Kind: "DIR"}}
	item := c.browseList.CreateItem()
	c.browseList.UpdateItem(0, item)
	row := findListRow(item)
	if row == nil || row.icon.Resource == nil || row.icon.Resource.Name() != theme.FolderIcon().Name() {
		t.Fatal("folder row did not render the folder icon")
	}
}

func TestSettingsDoNotExposeRawJSON(t *testing.T) {
	c := newTestClient(t)
	for _, label := range findUI[*widget.Label](c.settingsView()) {
		text := strings.TrimSpace(label.Text)
		if strings.HasPrefix(text, "{") && strings.HasSuffix(text, "}") {
			t.Fatalf("raw JSON shown in settings: %s", text)
		}
	}
}

func TestBackendSelectionSupportsX11AndWayland(t *testing.T) {
	t.Setenv("FYNE_BACKEND", "")
	c := newTestClient(t)
	_ = c.cfg.Set("go_display_backend", "x11", false)
	selectBackend(c.cfg)
	if got := os.Getenv("FYNE_BACKEND"); got != "x11" {
		t.Fatalf("FYNE_BACKEND = %q, want x11", got)
	}
	_ = c.cfg.Set("go_display_backend", "wayland", false)
	selectBackend(c.cfg)
	if got := os.Getenv("FYNE_BACKEND"); got != "wayland" {
		t.Fatalf("FYNE_BACKEND = %q, want wayland", got)
	}
	_ = c.cfg.Set("go_display_backend", "auto", false)
	t.Setenv("WAYLAND_DISPLAY", "wayland-0")
	selectBackend(c.cfg)
	if got := os.Getenv("FYNE_BACKEND"); got != "wayland" || displayBackend(c.cfg) != "wayland (auto)" {
		t.Fatalf("auto backend did not select Wayland: %q / %q", got, displayBackend(c.cfg))
	}
}

func TestSimpleModeHidesTechnicalAreasAndLinksRevealThem(t *testing.T) {
	c := newTestClient(t)
	c.setAdvanced(false, false)
	if c.navs[sectionActivity].Visible() || c.navs[sectionNAS].Visible() {
		t.Fatal("in modalità semplice Attività e NAS non devono comparire nella barra")
	}
	if !c.navs[sectionStatus].Visible() || !c.navs[sectionFiles].Visible() || !c.navs[sectionSettings].Visible() {
		t.Fatal("Home, File e Impostazioni devono restare sempre visibili")
	}
	for _, object := range c.advancedHome {
		if object.Visible() {
			t.Fatal("contatori e strumenti tecnici nascosti nella Home semplice")
		}
	}
	// Un rimando interno (es. il banner verso il log) non deve finire nel vuoto.
	c.navigate(sectionActivity, 3)
	if !c.navs[sectionActivity].Visible() || c.active != sectionActivity {
		t.Fatal("un rimando a un'area tecnica deve mostrarla")
	}
	c.setAdvanced(false, false)
	if c.active != sectionStatus {
		t.Fatal("nascondendo gli strumenti avanzati si torna alla Home")
	}
}

func TestSettingsShowOnlyEssentialTabsInSimpleViewAndLinksFindAdvancedOnes(t *testing.T) {
	c := newTestClient(t)
	c.setAdvanced(false, false)
	if got := len(c.settingsTabs.Items); got != 3 {
		t.Fatalf("vista semplice: attese 3 schede (Generale, Cartelle, Informazioni), trovate %d", got)
	}
	c.setAdvanced(true, false)
	if got := len(c.settingsTabs.Items); got != 6 {
		t.Fatalf("vista avanzata: attese 6 schede, trovate %d", got)
	}
	c.setAdvanced(false, false)
	// Un rimando alla scheda Integrità (indice 2 nella lista completa) la mostra.
	c.navigate(sectionSettings, 2)
	if !c.advancedOn || c.settingsTabs.Selected() != c.settingsAll[2] {
		t.Fatal("il rimando deve rivelare e selezionare la scheda Integrità")
	}
	// Un rimando a una scheda sempre visibile non cambia la vista.
	c.setAdvanced(false, false)
	c.navigate(sectionSettings, 3)
	if c.advancedOn || c.settingsTabs.Selected() != c.settingsAll[3] {
		t.Fatal("la scheda Cartelle è sempre visibile")
	}
}
