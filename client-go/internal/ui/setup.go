package ui

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"nasbox-go/internal/config"
	"nasbox-go/internal/remote"
)

// showSetupWizard builds the first-run configuration page. It mirrors the
// Python wizard's essentials: local folder, SSH endpoint, NAS detection and a
// real connection test before the sync engine starts.
func showSetupWizard(fyneApp fyne.App, window fyne.Window, cfg *config.Config, onDone func()) {
	fyneApp.Settings().SetTheme(newNAStheme(variantFrom(cfg.String("go_theme"))))

	localEntry := widget.NewEntry()
	localEntry.SetText(cfg.LocalRoot())
	hostEntry := widget.NewEntry()
	hostEntry.SetText(cfg.String("nas_lan"))
	userEntry := widget.NewEntry()
	userEntry.SetText(cfg.String("nas_user"))
	portEntry := widget.NewEntry()
	portEntry.SetText(fmt.Sprint(cfg.Int("ssh_port")))
	prefixEntry := widget.NewEntry()
	prefixEntry.SetText(cfg.String("remote_prefix"))
	scriptEntry := widget.NewEntry()
	scriptEntry.SetText(cfg.String("remote_server_script"))
	status := widget.NewLabel(tr("setup.detect_optional", nil))
	status.Wrapping = fyne.TextWrapWord

	apply := func() error {
		root := strings.TrimSpace(localEntry.Text)
		host := strings.TrimSpace(hostEntry.Text)
		user := strings.TrimSpace(userEntry.Text)
		if root == "" {
			return fmt.Errorf("%s", tr("setup.local_failed_title", nil))
		}
		if host == "" || user == "" {
			return fmt.Errorf("%s", tr("setup.connection_missing", nil))
		}
		port, err := strconv.Atoi(strings.TrimSpace(portEntry.Text))
		if err != nil || port <= 0 || port > 65535 {
			port = 22
		}
		_ = cfg.Set("local_root", root, false)
		_ = cfg.Set("nas_lan", host, false)
		_ = cfg.Set("nas_user", user, false)
		_ = cfg.Set("ssh_port", port, false)
		_ = cfg.Set("remote_prefix", strings.TrimSpace(prefixEntry.Text), false)
		_ = cfg.Set("remote_server_script", strings.TrimSpace(scriptEntry.Text), false)
		return nil
	}

	persist := func() error {
		if err := os.MkdirAll(strings.TrimSpace(localEntry.Text), 0o700); err != nil {
			return err
		}
		for _, key := range []string{
			"local_root", "nas_lan", "nas_user", "ssh_port", "remote_prefix", "remote_server_script",
		} {
			if err := cfg.Set(key, cfg.Get(key), true); err != nil {
				return err
			}
		}
		return nil
	}

	browse := widget.NewButtonWithIcon("", theme.FolderOpenIcon(), func() {
		dialog.ShowFolderOpen(func(uri fyne.ListableURI, err error) {
			if err != nil || uri == nil {
				return
			}
			localEntry.SetText(uri.Path())
		}, window)
	})

	detect := newHintButton(tr("setup.server_title", nil), theme.SearchIcon(),
		tr("setup.server_intro", nil), widget.MediumImportance, func() {
			if err := apply(); err != nil {
				status.SetText(err.Error())
				return
			}
			status.SetText(tr("setup.testing", nil))
			go func() {
				client := remote.New(cfg)
				scripts, err := client.DiscoverServerScripts()
				fyne.Do(func() {
					if err != nil {
						status.SetText(tr("setup.detect_failed", map[string]any{"detail": err.Error()}))
						return
					}
					if len(scripts) == 0 {
						status.SetText(tr("setup.detect_failed", map[string]any{
							"detail": tr("setup.server_intro", nil),
						}))
						return
					}
					scriptEntry.SetText(scripts[0])
					_ = cfg.Set("remote_server_script", scripts[0], false)
					values, err := remote.New(cfg).PrintConfig()
					if err == nil && values["SHARE_ROOT"] != "" {
						prefixEntry.SetText(values["SHARE_ROOT"])
						_ = cfg.Set("remote_prefix", values["SHARE_ROOT"], false)
					}
					status.SetText(tr("setup.detect_ok", nil))
				})
			}()
		}, func(string) {})

	test := newHintButton(tr("setup.test_connection", nil), theme.ComputerIcon(),
		tr("setup.connection_intro", nil), widget.MediumImportance, func() {
			if err := apply(); err != nil {
				status.SetText(err.Error())
				return
			}
			status.SetText(tr("setup.testing", nil))
			go func() {
				values, err := remote.New(cfg).PrintConfig()
				fyne.Do(func() {
					if err != nil {
						status.SetText(tr("setup.test_failed", map[string]any{"detail": err.Error()}))
						return
					}
					status.SetText(tr("setup.test_ok", map[string]any{"host": cfg.String("nas_lan")}) +
						" · " + tr("settings.info_version_label", nil) + " " + values["VERSION"])
				})
			}()
		}, func(string) {})

	save := newHintButton(tr("ui.setup.save", nil), theme.ConfirmIcon(),
		tr("ui.setup.save_hint", nil), widget.HighImportance, func() {
			if err := apply(); err != nil {
				status.SetText(err.Error())
				return
			}
			status.SetText(tr("setup.testing", nil))
			go func() {
				bootstrapErr := remote.New(cfg).Bootstrap()
				fyne.Do(func() {
					finish := func() {
						if err := persist(); err != nil {
							status.SetText(tr("setup.local_failed_title", nil) + ": " + err.Error())
							return
						}
						onDone()
					}
					if bootstrapErr != nil {
						dialog.ShowConfirm(tr("setup.detect_failed", map[string]any{
							"detail": bootstrapErr.Error(),
						}),
							tr("setup.detect_optional", nil), func(ok bool) {
								if ok {
									finish()
								}
							}, window)
						return
					}
					finish()
				})
			}()
		}, func(string) {})

	row := func(label string, object fyne.CanvasObject) fyne.CanvasObject {
		return container.NewBorder(nil, nil, minWidth(widget.NewLabel(label), 170), nil, object)
	}

	content := container.NewVScroll(container.NewVBox(
		widget.NewLabelWithStyle(tr("setup.title", nil), fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		cnote(tr("setup.local_intro", nil)),
		row(tr("setup.local_title", nil), container.NewBorder(nil, nil, nil, browse, localEntry)),
		container.NewVBox(
			widget.NewLabelWithStyle(tr("setup.connection_title", nil), fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
			cnote(tr("setup.connection_intro", nil)),
			row(tr("ui.setup.nas_lan", nil), hostEntry),
			row(tr("ui.setup.nas_user", nil), userEntry),
			row(tr("ui.setup.ssh_port", nil), portEntry),
		),
		container.NewVBox(
			widget.NewLabelWithStyle(tr("setup.server_title", nil), fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
			cnote(tr("setup.server_intro", nil)),
			row(tr("ui.setup.prefix", nil), prefixEntry),
			row(tr("ui.setup.script", nil), scriptEntry),
		),
		cnote(tr("setup.advanced_later", nil)),
		status,
		container.NewHBox(detect, test, save),
	))
	window.SetContent(container.NewPadded(content))
}

// cnote is the setup wizard's secondary text helper (the clientApp one is not
// available yet at this stage).
func cnote(text string) *widget.Label {
	label := widget.NewLabelWithStyle(text, fyne.TextAlignLeading, fyne.TextStyle{})
	label.Wrapping = fyne.TextWrapWord
	return label
}
