package ui

import (
	"fmt"
	"strconv"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"nasbox-go/internal/i18n"
)

type helpText struct{ it, en string }

var settingHelp = map[string]helpText{
	"nas_lan":                               {"Host/IP del NAS in LAN, usato da SSH e rsync.", "LAN host/IP of the NAS, used by SSH and rsync."},
	"nas_wan":                               {"Indirizzo pubblico del NAS fuori casa. Vuoto se inutile.", "Public NAS address outside the LAN. Empty if unused."},
	"jump_host":                             {"Bastione SSH per raggiungere il NAS da internet.", "SSH jump host used to reach the NAS from internet."},
	"jump_port":                             {"Porta SSH del bastione.", "SSH port of the jump host."},
	"jump_user":                             {"Utente SSH sul bastione.", "SSH user on the jump host."},
	"remote_prefix":                         {"Cartella NAS sincronizzata con quella locale.", "NAS folder mirrored to the local one."},
	"delete_sentinels":                      {"File locali obbligatori: se mancano, blocca i delete.", "Required local files: if missing, deletes are blocked."},
	"sync_mode":                             {"bidirectional, pull_only, push_only o archive.", "bidirectional, pull_only, push_only or archive."},
	"compression_mode":                      {"auto in LAN, on sempre, off mai.", "auto on LAN, on always, off never."},
	"conflict_policy":                       {"newest vince il piu recente; manual crea una copia.", "newest keeps the newer; manual creates a conflict copy."},
	"pause_schedule":                        {"Fascia oraria giornaliera di pausa.", "Daily time window when sync pauses."},
	"device_labels":                         {"Nomi dispositivi: si assegnano dal tab NAS.", "Device names: assign them in the NAS tab."},
	"delete_enabled":                        {"Se off, nessuna cancellazione remota.", "When off, no remote deletions."},
	"delete_safety_window_seconds":          {"Finestra temporale del limite cumulativo.", "Window for the cumulative deletion limit."},
	"delete_confirmation_seconds":           {"Durata dell'autorizzazione manuale alle cancellazioni.", "Duration of manual deletion authorization."},
	"delete_dir_guard_ratio":                {"Soglia di cancellazioni che attiva la protezione cartella.", "Deletion ratio that activates the folder guard."},
	"delete_dir_guard_min_files":            {"File minimi per applicare la protezione cartella.", "Minimum files for the folder guard."},
	"delete_guard_classify":                 {"Classifica le differenze prima di applicare la guardia.", "Classify differences before applying the guard."},
	"integrity_audit_enabled":               {"Esegue una verifica all'avvio.", "Run a verification at startup."},
	"integrity_check_interval_seconds":      {"Secondi tra i controlli checksum.", "Seconds between checksum checks."},
	"integrity_startup_delay_seconds":       {"Ritardo del primo controllo checksum.", "Delay before the first checksum check."},
	"integrity_audit_interval_days":         {"Giorni tra gli audit completi.", "Days between full audits."},
	"integrity_audit_batch_size":            {"File confrontati per batch di audit.", "Files compared per audit batch."},
	"integrity_audit_startup_delay_seconds": {"Ritardo del primo audit completo.", "Delay before the first full audit."},
	"bandwidth_upload_kbps":                 {"Limite upload in KB/s; 0 = illimitato.", "Upload limit in KB/s; 0 = unlimited."},
	"bandwidth_download_kbps":               {"Limite download in KB/s; 0 = illimitato.", "Download limit in KB/s; 0 = unlimited."},
	"notify_sync_completion":                {"Mostra una notifica al termine della sincronizzazione.", "Show a notification when sync completes."},
	"animate_sync_icon":                     {"Anima l'icona tray durante il trasferimento.", "Animate the tray icon during transfers."},
	"max_delete_files":                      {"Blocco oltre N cancellazioni in un batch.", "Stop above N deletions in one batch."},
	"max_delete_files_per_window":           {"Limite cumulativo di cancellazioni in un ora.", "Cumulative deletion limit per hour."},
	"integrity_check_sample_size":           {"File per check periodico; 0 = tutti.", "Files per periodic check; 0 = all."},
	"bandwidth_kbps":                        {"Velocita max in KB/s; 0 = illimitata.", "Max speed in KB/s; 0 = unlimited."},
	"min_server_version":                    {"Versione minima richiesta dello script server.", "Minimum required NAS server script version."},
	"go_display_backend":                    {"auto sceglie Wayland, altrimenti X11.", "auto picks Wayland, otherwise X11."},
	"language":                              {"Lingua dell'interfaccia; si applica subito.", "Interface language; applied immediately."},
	"go_theme":                              {"Tema chiaro o scuro; si applica subito.", "Light or dark theme; applied immediately."},
	"shortcuts":                             {"Scorciatoie disponibili in tutta l'app.", "Shortcuts available throughout the app."},
	"ssh_port":                              {"Porta SSH del NAS.", "NAS SSH port."},
	"remote_server_script":                  {"Percorso dello script server sul NAS.", "Path of the server script on the NAS."},
	"ssh_known_hosts":                       {"File known hosts usato per verificare il NAS.", "Known hosts file used to verify the NAS."},
	"poll_interval":                         {"Secondi tra i controlli periodici.", "Seconds between periodic checks."},
	"change_feed_wait_seconds":              {"Attesa massima del change feed NAS.", "Maximum wait for the NAS change feed."},
	"debounce_seconds":                      {"Attesa che i cambiamenti locali si stabilizzino.", "Wait for local changes to settle."},
	"file_stability_seconds":                {"Attesa minima prima di considerare stabile un file.", "Minimum wait before treating a file as stable."},
	"full_pull_interval_seconds":            {"Secondi tra le riconciliazioni complete.", "Seconds between full reconciliations."},
	"conflict_storm_limit":                  {"Numero massimo di conflitti prima della protezione.", "Maximum conflicts before the guard activates."},
	"conflict_storm_window_seconds":         {"Finestra temporale del limite conflitti.", "Time window for the conflict limit."},
	"metrics_refresh_seconds":               {"Intervallo di aggiornamento delle metriche NAS.", "NAS metrics refresh interval."},
	"tray_single_click":                     {"Click tray: menu o finestra.", "Tray click: menu or window."},
	"retention_days_local":                  {"Giorni di conservazione nel cestino locale.", "Days to keep local trash versions."},
	"retention_days_remote":                 {"Giorni di conservazione sul NAS.", "Days to keep NAS trash versions."},
	"pause_schedule_enabled":                {"Attiva la fascia oraria di pausa.", "Enable the scheduled pause window."},
	"client_update_remote_path":             {"Cartella del canale di aggiornamento Python.", "Python update channel folder."},
	"client_update_remote_path_go":          {"Cartella del canale di aggiornamento Go.", "Go update channel folder."},
}

func (c *clientApp) settingRow(label string, object fyne.CanvasObject, helpKey string) fyne.CanvasObject {
	name := widget.NewLabelWithStyle(label, fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	name.Wrapping = fyne.TextWrapWord
	leftObjects := []fyne.CanvasObject{name}
	if info, ok := settingHelp[helpKey]; ok {
		text := info.it
		if strings.EqualFold(i18n.Language, "en") {
			text = info.en
		}
		help := c.note(text)
		leftObjects = append(leftObjects, help)
	}
	// Auto-height left column: help sits directly under its label. The old
	// fixed 320x84 cell made every setting look like a large empty box.
	left := minWidth(container.NewVBox(leftObjects...), 260)
	return container.NewBorder(nil, nil, left, nil, object)
}

func (c *clientApp) settingsGroup(title, subtitle string, rows ...fyne.CanvasObject) fyne.CanvasObject {
	return container.NewVBox(
		c.sectionTitle(title, subtitle),
		c.surface(container.NewVBox(rows...), ""),
	)
}

func (c *clientApp) persistSetting(key string, value any) bool {
	if err := c.cfg.Set(key, value, true); err != nil {
		c.appendLog(tr("ui.settings.save_failed", map[string]any{"detail": err.Error()}))
		return false
	}
	return true
}

func (c *clientApp) settingSaved(label string, value any) {
	c.appendAction(fmt.Sprintf("%s: %v", label, value))
}

func (c *clientApp) stringSetting(label, key string) fyne.CanvasObject {
	entry := widget.NewEntry()
	entry.SetText(c.cfg.String(key))
	entry.OnChanged = func(value string) { _ = c.cfg.Set(key, value, false) }
	entry.OnSubmitted = func(value string) {
		if c.persistSetting(key, strings.TrimSpace(value)) {
			c.settingSaved(label, strings.TrimSpace(value))
		}
	}
	return c.settingRow(label, entry, key)
}

func (c *clientApp) intSetting(label, key string) fyne.CanvasObject {
	entry := widget.NewEntry()
	entry.SetText(fmt.Sprint(c.cfg.Int(key)))
	entry.OnChanged = func(value string) {
		if parsed, err := strconv.Atoi(strings.TrimSpace(value)); err == nil {
			_ = c.cfg.Set(key, parsed, false)
		}
	}
	entry.OnSubmitted = func(value string) {
		parsed, err := strconv.Atoi(strings.TrimSpace(value))
		if err != nil {
			c.appendLog(tr("ui.settings.invalid_number", nil))
			return
		}
		if c.persistSetting(key, parsed) {
			c.settingSaved(label, parsed)
		}
	}
	return c.settingRow(label, entry, key)
}

func (c *clientApp) floatSetting(label, key string, fallback float64) fyne.CanvasObject {
	entry := widget.NewEntry()
	value := fallback
	if raw, ok := c.cfg.Get(key).(float64); ok {
		value = raw
	}
	entry.SetText(fmt.Sprint(value))
	entry.OnChanged = func(text string) {
		if parsed, err := strconv.ParseFloat(strings.TrimSpace(text), 64); err == nil {
			_ = c.cfg.Set(key, parsed, false)
		}
	}
	entry.OnSubmitted = func(text string) {
		parsed, err := strconv.ParseFloat(strings.TrimSpace(text), 64)
		if err != nil {
			c.appendLog(tr("ui.settings.invalid_number", nil))
			return
		}
		if c.persistSetting(key, parsed) {
			c.settingSaved(label, parsed)
		}
	}
	return c.settingRow(label, entry, key)
}

type settingOption struct {
	value string
	label string
}

func (c *clientApp) choiceSetting(label, key string, options []string) fyne.CanvasObject {
	values := make([]settingOption, 0, len(options))
	for _, option := range options {
		values = append(values, settingOption{value: option, label: option})
	}
	return c.choiceSettingLabels(label, key, values)
}

func (c *clientApp) choiceSettingLabels(label, key string, options []settingOption) fyne.CanvasObject {
	labels := make([]string, 0, len(options))
	current := c.cfg.String(key)
	selected := current
	found := false
	for _, option := range options {
		labels = append(labels, option.label)
		if option.value == current {
			selected = option.label
			found = true
		}
	}
	if !found && len(options) > 0 {
		selected = options[0].label
	}
	selectWidget := widget.NewSelect(labels, nil)
	selectWidget.SetSelected(selected)
	selectWidget.OnChanged = func(value string) {
		for _, option := range options {
			if option.label == value {
				if c.persistSetting(key, option.value) {
					c.settingSaved(label, value)
				}
				return
			}
		}
	}
	return c.settingRow(label, selectWidget, key)
}

func (c *clientApp) boolSetting(label, key string) fyne.CanvasObject {
	check := widget.NewCheck("", nil)
	check.SetChecked(c.cfg.Bool(key))
	check.OnChanged = func(value bool) {
		if c.persistSetting(key, value) {
			state := tr("ui.settings.off", nil)
			if value {
				state = tr("ui.settings.on", nil)
			}
			c.settingSaved(label, state)
		}
	}
	return c.settingRow(label, check, key)
}

func scheduleTimes(current string) []string {
	times := make([]string, 0, 48)
	for hour := 0; hour < 24; hour++ {
		times = append(times, fmt.Sprintf("%02d:00", hour), fmt.Sprintf("%02d:30", hour))
	}
	found := false
	for _, value := range times {
		if value == current {
			found = true
			break
		}
	}
	if current != "" && !found {
		times = append(times, current)
	}
	return times
}

// excludePatternsSetting edits the rsync-style patterns ignored in both
// directions. One pattern per line keeps the same format as the Python
// settings tab and the client.json array.
func (c *clientApp) excludePatternsSetting() fyne.CanvasObject {
	entry := widget.NewMultiLineEntry()
	entry.SetMinRowsVisible(6)
	entry.SetText(strings.Join(stringList(c.cfg.Get("exclude_patterns")), "\n"))
	entry.SetPlaceHolder(tr("ui.settings.exclude_placeholder", nil))
	save := newHintButton(tr("ui.settings.exclude_save", nil), theme.ConfirmIcon(),
		tr("ui.settings.exclude_save_hint", nil), widget.HighImportance, func() {
			patterns := make([]any, 0)
			for _, line := range strings.Split(entry.Text, "\n") {
				line = strings.TrimSpace(line)
				if line != "" {
					patterns = append(patterns, line)
				}
			}
			if c.persistSetting("exclude_patterns", patterns) {
				c.settingSaved(tr("ui.settings.exclude_title", nil), len(patterns))
			}
		}, c.setHint)
	return container.NewVBox(
		widget.NewLabelWithStyle(tr("ui.settings.exclude_title", nil), fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		c.note(tr("ui.settings.exclude_note", nil)),
		entry,
		save,
	)
}

func stringList(value any) []string {
	out := []string{}
	switch list := value.(type) {
	case []any:
		for _, item := range list {
			if text, ok := item.(string); ok && strings.TrimSpace(text) != "" {
				out = append(out, text)
			}
		}
	case []string:
		out = append(out, list...)
	}
	return out
}

func (c *clientApp) deviceLabelsSetting() fyne.CanvasObject {
	label := c.note(tr("ui.settings.device_labels_note", nil))
	button := newHintButton(tr("ui.settings.open_devices", nil), theme.ComputerIcon(),
		tr("ui.settings.open_devices_hint", nil), widget.MediumImportance,
		func() { c.navigate(sectionNAS, 1) }, c.setHint)
	return c.settingRow(tr("ui.settings.device_labels", nil), container.NewVBox(label, button), "device_labels")
}

func (c *clientApp) languageSetting() fyne.CanvasObject {
	selectWidget := widget.NewSelect([]string{"Italiano", "English"}, nil)
	selectWidget.SetSelected("Italiano")
	if strings.EqualFold(i18n.Language, "en") {
		selectWidget.SetSelected("English")
	}
	selectWidget.OnChanged = func(value string) {
		language := "it"
		if value == "English" {
			language = "en"
		}
		c.setLanguage(language)
	}
	return c.settingRow(tr("settings.language_title", nil), selectWidget, "language")
}

func (c *clientApp) themeSetting() fyne.CanvasObject {
	selectWidget := widget.NewSelect([]string{
		tr("settings.theme_light", nil), tr("settings.theme_dark", nil),
	}, nil)
	selectWidget.SetSelected(tr("settings.theme_light", nil))
	if variantFrom(c.cfg.String("go_theme")) == theme.VariantDark {
		selectWidget.SetSelected(tr("settings.theme_dark", nil))
	}
	selectWidget.OnChanged = func(value string) {
		if value == tr("settings.theme_dark", nil) {
			c.setTheme("dark")
		} else {
			c.setTheme("light")
		}
	}
	return c.settingRow(tr("settings.theme_title", nil), selectWidget, "go_theme")
}

func (c *clientApp) settingsAppearance() fyne.CanvasObject {
	return c.settingsGroup(tr("ui.group.general", nil), tr("ui.settings.appearance_note", nil),
		c.languageSetting(),
		c.themeSetting(),
		c.settingRow(tr("ui.settings.display_backend", nil),
			widget.NewLabel(displayBackend(c.cfg)), "go_display_backend"),
		c.settingRow(tr("ui.settings.shortcuts", nil),
			widget.NewLabel(tr("ui.settings.shortcuts_value", nil)), "shortcuts"),
	)
}

func (c *clientApp) settingsConnection() fyne.CanvasObject {
	return c.settingsGroup(tr("ui.settings.connection_title", nil), tr("ui.settings.connection_note", nil),
		c.stringSetting(tr("ui.settings.nas_lan", nil), "nas_lan"),
		c.stringSetting(tr("ui.settings.nas_wan", nil), "nas_wan"),
		c.stringSetting(tr("ui.settings.nas_user", nil), "nas_user"),
		c.intSetting(tr("ui.settings.ssh_port", nil), "ssh_port"),
		c.stringSetting(tr("ui.settings.jump_host", nil), "jump_host"),
		c.intSetting(tr("ui.settings.jump_port", nil), "jump_port"),
		c.stringSetting(tr("ui.settings.jump_user", nil), "jump_user"),
		c.stringSetting(tr("ui.settings.remote_prefix", nil), "remote_prefix"),
		c.stringSetting(tr("ui.settings.remote_script", nil), "remote_server_script"),
		c.stringSetting(tr("ui.settings.known_hosts", nil), "ssh_known_hosts"),
	)
}

func (c *clientApp) settingsSync() fyne.CanvasObject {
	return c.settingsGroup(tr("ui.settings.sync_title", nil), tr("ui.settings.sync_note", nil),
		c.choiceSettingLabels(tr("ui.settings.sync_mode", nil), "sync_mode", []settingOption{
			{value: "bidirectional", label: tr("settings.sync_mode_bidirectional", nil)},
			{value: "pull_only", label: tr("settings.sync_mode_pull_only", nil)},
			{value: "push_only", label: tr("settings.sync_mode_push_only", nil)},
			{value: "archive", label: tr("settings.sync_mode_archive", nil)},
		}),
		c.intSetting(tr("ui.settings.poll_interval", nil), "poll_interval"),
		c.intSetting(tr("ui.settings.change_feed_wait", nil), "change_feed_wait_seconds"),
		c.intSetting(tr("ui.settings.debounce", nil), "debounce_seconds"),
		c.floatSetting(tr("ui.settings.file_stability", nil), "file_stability_seconds", 0.75),
		c.choiceSettingLabels(tr("ui.settings.compression", nil), "compression_mode", []settingOption{
			{value: "auto", label: tr("ui.settings.compression_auto", nil)},
			{value: "on", label: tr("ui.settings.on", nil)},
			{value: "off", label: tr("ui.settings.off", nil)},
		}),
		c.intSetting(tr("ui.settings.full_pull", nil), "full_pull_interval_seconds"),
		c.choiceSettingLabels(tr("ui.settings.conflict_policy", nil), "conflict_policy", []settingOption{
			{value: "newest", label: tr("ui.settings.conflict_newest", nil)},
			{value: "manual", label: tr("ui.settings.conflict_manual", nil)},
		}),
		c.intSetting(tr("ui.settings.conflict_limit", nil), "conflict_storm_limit"),
		c.intSetting(tr("ui.settings.conflict_window", nil), "conflict_storm_window_seconds"),
		c.floatSetting(tr("ui.settings.metrics_interval", nil), "metrics_refresh_seconds", 60),
	)
}

func (c *clientApp) settingsSafety() fyne.CanvasObject {
	return c.settingsGroup(tr("ui.settings.safety_title", nil), tr("ui.settings.safety_note", nil),
		c.boolSetting(tr("ui.settings.delete_enabled", nil), "delete_enabled"),
		c.intSetting(tr("ui.settings.max_delete_batch", nil), "max_delete_files"),
		c.intSetting(tr("ui.settings.max_delete_window", nil), "max_delete_files_per_window"),
		c.intSetting(tr("ui.settings.delete_window", nil), "delete_safety_window_seconds"),
		c.intSetting(tr("ui.settings.delete_confirmation", nil), "delete_confirmation_seconds"),
		c.floatSetting(tr("ui.settings.delete_guard_ratio", nil), "delete_dir_guard_ratio", 0.5),
		c.intSetting(tr("ui.settings.delete_guard_min", nil), "delete_dir_guard_min_files"),
		c.stringSetting(tr("ui.settings.delete_sentinels", nil), "delete_sentinels"),
		c.boolSetting(tr("ui.settings.delete_classify", nil), "delete_guard_classify"),
	)
}

func (c *clientApp) settingsIntegrity() fyne.CanvasObject {
	return c.settingsGroup(tr("ui.settings.integrity_title", nil), tr("ui.settings.integrity_note", nil),
		c.boolSetting(tr("ui.settings.integrity_enabled", nil), "integrity_audit_enabled"),
		c.intSetting(tr("ui.settings.integrity_interval", nil), "integrity_check_interval_seconds"),
		c.intSetting(tr("ui.settings.integrity_startup_delay", nil), "integrity_startup_delay_seconds"),
		c.intSetting(tr("ui.settings.integrity_sample", nil), "integrity_check_sample_size"),
		c.intSetting(tr("ui.settings.audit_days", nil), "integrity_audit_interval_days"),
		c.intSetting(tr("ui.settings.audit_batch", nil), "integrity_audit_batch_size"),
		c.intSetting(tr("ui.settings.audit_startup_delay", nil), "integrity_audit_startup_delay_seconds"),
	)
}

func (c *clientApp) settingsAdvanced() fyne.CanvasObject {
	return c.settingsGroup(tr("ui.settings.advanced_title", nil), tr("ui.settings.advanced_note", nil),
		c.intSetting(tr("ui.settings.retention_local", nil), "retention_days_local"),
		c.intSetting(tr("ui.settings.retention_remote", nil), "retention_days_remote"),
		c.intSetting(tr("ui.settings.bandwidth_total", nil), "bandwidth_kbps"),
		c.intSetting(tr("ui.settings.bandwidth_upload", nil), "bandwidth_upload_kbps"),
		c.intSetting(tr("ui.settings.bandwidth_download", nil), "bandwidth_download_kbps"),
		c.boolSetting(tr("ui.settings.notify_sync", nil), "notify_sync_completion"),
		c.boolSetting(tr("ui.settings.animate_tray", nil), "animate_sync_icon"),
		c.choiceSettingLabels(tr("ui.settings.tray_click", nil), "tray_single_click", []settingOption{
			{value: "menu", label: tr("settings.tray_click_menu", nil)},
			{value: "window", label: tr("settings.tray_click_window", nil)},
		}),
		c.choiceSettingLabels(tr("ui.settings.backend", nil), "go_display_backend", []settingOption{
			{value: "auto", label: tr("ui.settings.backend_auto", nil)},
			{value: "x11", label: "X11"},
			{value: "wayland", label: "Wayland"},
		}),
		c.boolSetting(tr("ui.settings.pause_enabled", nil), "pause_schedule_enabled"),
		c.scheduleSetting(),
		c.stringSetting(tr("ui.settings.update_python", nil), "client_update_remote_path"),
		c.stringSetting(tr("ui.settings.update_go", nil), "client_update_remote_path_go"),
		c.stringSetting(tr("ui.settings.min_server", nil), "min_server_version"),
		c.deviceLabelsSetting(),
	)
}
