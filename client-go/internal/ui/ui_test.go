package ui

import (
	"image/color"
	"testing"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/theme"

	"nasbox-go/internal/config"
	"nasbox-go/internal/engine"
	"nasbox-go/internal/remote"
	"nasbox-go/internal/state"
	"nasbox-go/internal/transfer"
)

// newTestClient builds the UI shell without starting the runner or touching
// the network. All refresh calls happen in Run(), not while building.
func newTestClient(t *testing.T) *clientApp {
	t.Helper()
	a := test.NewApp()
	t.Cleanup(a.Quit)
	t.Setenv("XDG_STATE_HOME", t.TempDir())

	cfg := config.New("")
	cfg.Set("local_root", t.TempDir(), false)
	store, err := state.Open(cfg)
	if err != nil {
		t.Fatalf("state.Open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	client := remote.New(cfg)
	c := &clientApp{
		cfg: cfg, store: store, client: client,
		transfers: transfer.New(cfg, client),
		runner:    engine.NewRunner(cfg, store, client, transfer.New(cfg, client)),
		fyneApp:   a,
		window:    a.NewWindow("test"),
		uiTheme:   newNAStheme(theme.VariantLight),
		level:     statusStarting,
		setHint:   func(string) {},
	}
	c.bannerGo = func() {}
	a.Settings().SetTheme(c.uiTheme)
	c.window.SetContent(c.buildRoot())
	return c
}

func TestShellHasFiveAreas(t *testing.T) {
	c := newTestClient(t)
	if len(c.views) != 5 {
		t.Fatalf("views = %d, want 5", len(c.views))
	}
	if len(c.navs) != 5 {
		t.Fatalf("nav buttons = %d, want 5", len(c.navs))
	}
	if c.active != sectionStatus {
		t.Fatalf("initial section = %v, want status", c.active)
	}
	if c.content.Objects[0] != c.views[sectionStatus] {
		t.Fatal("status view is not the initial content")
	}
}

func TestNavigationSwitchesAreaAndTab(t *testing.T) {
	c := newTestClient(t)
	c.showSection(sectionFiles)
	if c.active != sectionFiles {
		t.Fatalf("active section = %v, want files", c.active)
	}
	if c.content.Objects[0] != c.views[sectionFiles] {
		t.Fatal("files view was not installed")
	}
	if got := c.headerTitle.Text; got != tr("ui.nav.files", nil) {
		t.Fatalf("header title = %q, want %q", got, tr("ui.nav.files", nil))
	}
	c.navigate(sectionFiles, 2)
	if got := c.fileTabs.SelectedIndex(); got != 2 {
		t.Fatalf("selected file tab = %d, want 2", got)
	}
}

func TestEveryNavigationButtonSwitchesSection(t *testing.T) {
	c := newTestClient(t)
	for _, section := range []sectionID{
		sectionActivity, sectionFiles, sectionNAS, sectionSettings, sectionStatus,
	} {
		item := c.navs[section]
		if item == nil {
			t.Fatalf("missing nav button for %v", section)
		}
		item.Tapped(&fyne.PointEvent{Position: fyne.NewPos(10, 10)})
		if c.active != section {
			t.Fatalf("tap on %v left active=%v", section, c.active)
		}
	}
}

func TestStatusActionsAreEnabled(t *testing.T) {
	c := newTestClient(t)
	view := c.statusView()
	if view == nil {
		t.Fatal("no status view")
	}
	if c.pauseBtnStatus == nil || c.detailsBtn == nil {
		t.Fatal("status actions missing")
	}
	if c.pauseBtnStatus.Disabled() || c.detailsBtn.Button.Disabled() {
		t.Fatal("status actions are disabled")
	}
}

func TestStatusBannerSemantics(t *testing.T) {
	c := newTestClient(t)
	c.applyStatusUI(statusBlocked, "motivo blocco")
	if got := c.bannerTitle.Text; got != plainTitle(tr("ui.status.blocked", nil)) {
		t.Fatalf("blocked title = %q", got)
	}
	if c.bannerReason.Text != "motivo blocco" {
		t.Fatalf("reason = %q", c.bannerReason.Text)
	}
	if got := c.bannerBG.FillColor; got != c.bannerTint(theme.ColorNameError) {
		t.Fatalf("blocked colour = %v, want light error tint", got)
	}
	c.applyStatusUI(statusOK, "tutto bene")
	if got := c.bannerBG.FillColor; got != c.bannerTint(theme.ColorNameSuccess) {
		t.Fatalf("ok colour = %v, want light success tint", got)
	}
	if c.headerStatus.Text != plainTitle(tr("overview.banner_ok", nil)) {
		t.Fatalf("header status = %q", c.headerStatus.Text)
	}
}

func TestBannerActionNavigatesToGuidance(t *testing.T) {
	c := newTestClient(t)
	c.applyStatusUI(statusBlocked, "blocco")
	c.bannerGo()
	if c.active != sectionActivity {
		t.Fatalf("active = %v, want activity", c.active)
	}
	if got := c.activityTabs.SelectedIndex(); got != 2 {
		t.Fatalf("selected activity tab = %d, want log", got)
	}
}

func TestThemeSwitchRebuildsShell(t *testing.T) {
	c := newTestClient(t)
	c.setTheme("dark")
	if c.uiTheme.variant != theme.VariantDark {
		t.Fatal("theme variant was not switched")
	}
	if c.cfg.String("go_theme") != "dark" {
		t.Fatal("theme was not persisted")
	}
	if len(c.views) != 5 || c.active != sectionStatus {
		t.Fatal("shell was not rebuilt correctly after theme switch")
	}
	c.showSection(sectionFiles)
	if c.content.Objects[0] != c.views[sectionFiles] {
		t.Fatal("navigation broke after theme switch")
	}
}

func TestThemeAndMinSize(t *testing.T) {
	c := newTestClient(t)
	if variantFrom("dark") != theme.VariantDark || variantFrom("light") != theme.VariantLight {
		t.Fatal("variantFrom did not map light/dark")
	}
	light, dark := newNAStheme(theme.VariantLight), newNAStheme(theme.VariantDark)
	if light.Color(theme.ColorNameSuccess, theme.VariantLight) == dark.Color(theme.ColorNameSuccess, theme.VariantDark) {
		t.Fatal("light and dark success colours must differ")
	}
	if _, ok := c.tint(theme.ColorNamePrimary).(color.NRGBA); !ok {
		t.Fatal("primary colour is not an NRGBA")
	}
	size := c.window.Content().MinSize()
	if size.Width > 1120 || size.Height > 760 {
		t.Fatalf("minimum content size %v exceeds the 1120x760 target", size)
	}
	_ = fyne.NewSize(1120, 760)
}
