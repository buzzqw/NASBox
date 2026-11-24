package ui

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"

	"nasbox-go/internal/i18n"
)

type helpText struct{ it, en string }

var settingHelp = map[string]helpText{
	"nas_lan":                     {"Indirizzo IP o hostname del NAS nella rete locale. È la destinazione predefinita di SSH e rsync.", "LAN IP or hostname of the NAS. Default target for SSH and rsync."},
	"nas_wan":                     {"Indirizzo pubblico del NAS, usato quando non sei in LAN. Lascia vuoto se non serve.", "Public address of the NAS when outside the LAN. Leave empty if unused."},
	"jump_host":                   {"Bastione SSH: un server raggiungibile da internet che può a sua volta raggiungere il NAS. Le connessioni passano da qui.", "SSH jump host: a publicly reachable server that can reach the NAS. Connections are proxied through it."},
	"jump_port":                   {"Porta SSH del bastione.", "SSH port of the jump host."},
	"jump_user":                   {"Utente SSH sul bastione.", "SSH user on the jump host."},
	"remote_prefix":               {"Cartella sul NAS che viene sincronizzata con la cartella locale.", "Folder on the NAS that mirrors the local folder."},
	"delete_sentinels":            {"Percorsi di file che devono esistere in locale. Se mancano, il client si rifiuta di cancellare: serve a rilevare un volume non montato o la cartella sbagliata. Separa più percorsi con una virgola.", "Local files that must exist. If missing, destructive sync is refused (unmounted volume / wrong folder). Separate multiple paths with a comma."},
	"sync_mode":                   {"bidirectional: invia e riceve. pull_only: scarica solo dal NAS. push_only: invia solo al NAS. archive: invia senza mai cancellare nulla sul NAS.", "bidirectional: send and receive. pull_only: download only. push_only: upload only. archive: upload without ever deleting on the NAS."},
	"compression_mode":            {"auto disattiva la compressione in LAN; on la forza; off la disattiva sempre.", "auto disables compression on LAN; on forces it; off disables it."},
	"conflict_policy":             {"newest: vince la versione più recente e那 perdente finisce nello storico. manual: crea una copia conflitto da risolvere a mano.", "newest: the newer version wins and the loser goes to history. manual: creates a conflict copy to resolve by hand."},
	"pause_schedule":              {"Intervallo orario in cui la sincronizzazione resta in pausa.", "Daily time window during which synchronization pauses."},
	"device_labels":               {"Nomi leggibili associati agli ID dispositivo del journal NAS. Si configurano dal tab NAS → Dispositivi.", "Friendly names for NAS journal device IDs. Configure them in the NAS → Devices tab."},
	"delete_enabled":              {"Se disattivato, il client non cancellerà mai file sul NAS.", "When off, the client will never delete files on the NAS."},
	"max_delete_files":            {"Blocco di sicurezza: oltre questo numero di cancellazioni in un batch l'operazione viene rifiutata.", "Safety stop: batches deleting more than this many files are refused."},
	"max_delete_files_per_window": {"Limite cumulativo di cancellazioni in un'ora, anche spezzate in più batch.", "Cumulative deletion limit per hour, even across multiple batches."},
	"integrity_check_sample_size": {"Quanti file confrontare per ogni verifica periodica dei checksum. 0 = tutti i file (costoso).", "How many files to compare in each periodic checksum check. 0 = all files (expensive)."},
	"bandwidth_kbps":              {"Velocità massima in kilobyte al secondo. 0 = senza limite.", "Maximum speed in KB/s. 0 = unlimited."},
	"min_server_version":          {"Versione minima dello script server richiesta dal client.", "Minimum NAS server script version required by this client."},
	"go_display_backend":          {"Backend grafico: auto sceglie Wayland se disponibile, altrimenti X11.", "Display backend: auto picks Wayland when available, otherwise X11."},
	"tray_single_click":           {"Cosa fa un clic sull'icona nella barra di sistema: apre il menu o la finestra.", "What a single click on the tray icon does: open the menu or the window."},
	"retention_days_local":        {"Quanti giorni conservare le versioni nel cestino locale.", "How many days to keep versions in the local trash."},
	"retention_days_remote":       {"Quanti giorni conservare le versioni nel cestino sul NAS.", "How many days to keep versions in the NAS trash."},
}

func (c *clientApp) helpButton(key string) fyne.CanvasObject {
	info := settingHelp[key]
	button := widget.NewButton("?", func() {
		text := info.it
		if i18n.Language == "en" || strings.ToLower(i18n.Language) == "en" {
			text = info.en
		}
		if text == "" {
			text = "Nessuna descrizione disponibile per questa impostazione."
		}
		dialog.ShowInformation("Informazioni", text, c.window)
	})
	button.Importance = widget.LowImportance
	return withHint(button, "Che cosa significa questa impostazione?", c.setHint)
}

func (c *clientApp) settingRow(label string, object fyne.CanvasObject, helpKey string) fyne.CanvasObject {
	name := widget.NewLabel(label)
	name.Wrapping = fyne.TextWrapWord
	left := container.NewVBox(name)
	if info, ok := settingHelp[helpKey]; ok {
		text := info.it
		if strings.EqualFold(i18n.Language, "en") {
			text = info.en
		}
		help := widget.NewLabel(text)
		help.Wrapping = fyne.TextWrapWord
		help.TextStyle = fyne.TextStyle{Italic: true}
		left.Add(help)
	}
	// Fixed-width left column: the explanation lives under the label and never
	// collapses to one character per line.
	leftWrap := container.NewGridWrap(fyne.NewSize(320, 84), left)
	return container.NewBorder(nil, nil, leftWrap, nil, object)
}

func (c *clientApp) stringSetting(label, key string) fyne.CanvasObject {
	entry := widget.NewEntry()
	entry.SetText(c.cfg.String(key))
	entry.OnChanged = func(value string) { c.cfg.Set(key, value, true) }
	return c.settingRow(label, entry, key)
}

func (c *clientApp) intSetting(label, key string) fyne.CanvasObject {
	entry := widget.NewEntry()
	entry.SetText(fmt.Sprint(c.cfg.Int(key)))
	entry.OnChanged = func(value string) {
		if parsed, err := strconv.Atoi(strings.TrimSpace(value)); err == nil {
			c.cfg.Set(key, parsed, true)
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
			c.cfg.Set(key, parsed, true)
		}
	}
	return c.settingRow(label, entry, key)
}

func (c *clientApp) choiceSetting(label, key string, options []string) fyne.CanvasObject {
	selectWidget := widget.NewSelect(options, func(value string) { c.cfg.Set(key, value, true) })
	current := c.cfg.String(key)
	for _, option := range options {
		if strings.EqualFold(option, current) {
			current = option
			break
		}
	}
	selectWidget.SetSelected(current)
	return c.settingRow(label, selectWidget, key)
}

func (c *clientApp) boolSetting(label, key string) fyne.CanvasObject {
	check := widget.NewCheck("", func(value bool) { c.cfg.Set(key, value, true) })
	check.SetChecked(c.cfg.Bool(key))
	return c.settingRow(label, check, key)
}

func (c *clientApp) scheduleSetting() fyne.CanvasObject {
	start, end := "09:00", "18:00"
	var entries []map[string]any
	if raw, err := json.Marshal(c.cfg.Get("pause_schedule")); err == nil {
		_ = json.Unmarshal(raw, &entries)
	}
	if len(entries) > 0 {
		start = fmt.Sprint(entries[0]["start"])
		end = fmt.Sprint(entries[0]["end"])
	}
	startEntry := widget.NewEntry()
	startEntry.SetText(start)
	endEntry := widget.NewEntry()
	endEntry.SetText(end)
	save := widget.NewButton("Salva fascia oraria", func() {
		c.cfg.Set("pause_schedule", []any{map[string]any{
			"start": strings.TrimSpace(startEntry.Text), "end": strings.TrimSpace(endEntry.Text),
		}}, true)
		c.appendLog("Fascia di pausa aggiornata")
	})
	return c.settingRow("Fascia pausa (HH:MM)", container.NewHBox(startEntry, endEntry, save), "pause_schedule")
}

func (c *clientApp) deviceLabelsSetting() fyne.CanvasObject {
	label := widget.NewLabel("I nomi dei dispositivi si assegnano dal tab NAS → Dispositivi.")
	label.Wrapping = fyne.TextWrapWord
	button := widget.NewButton("Apri Dispositivi", func() { c.navigate(sectionNAS, 1) })
	return c.settingRow("Nomi dispositivi", container.NewVBox(label, button), "device_labels")
}

func (c *clientApp) settingsConnection() fyne.CanvasObject {
	return container.NewVBox(
		c.sectionTitle("Connessione NAS e bastione", "Stessi campi del client Python: configurazione e tunnel SSH"),
		c.stringSetting("NAS LAN (host/IP)", "nas_lan"),
		c.stringSetting("NAS WAN (host/IP)", "nas_wan"),
		c.stringSetting("Utente SSH", "nas_user"),
		c.intSetting("Porta SSH", "ssh_port"),
		c.stringSetting("Jump host (bastione)", "jump_host"),
		c.intSetting("Porta jump host", "jump_port"),
		c.stringSetting("Utente jump host", "jump_user"),
		c.stringSetting("Prefisso remoto", "remote_prefix"),
		c.stringSetting("Script server NAS", "remote_server_script"),
		c.stringSetting("Known hosts", "ssh_known_hosts"),
	)
}

func (c *clientApp) settingsSync() fyne.CanvasObject {
	return container.NewVBox(
		c.sectionTitle("Sincronizzazione", "Modalità, tempi e risoluzione conflitti"),
		c.choiceSetting("Modalità", "sync_mode", []string{"bidirectional", "pull_only", "push_only", "archive"}),
		c.intSetting("Intervallo polling (s)", "poll_interval"),
		c.intSetting("Attesa change feed (s)", "change_feed_wait_seconds"),
		c.intSetting("Debounce (s)", "debounce_seconds"),
		c.floatSetting("Stabilità file (s)", "file_stability_seconds", 0.75),
		c.choiceSetting("Compressione", "compression_mode", []string{"auto", "on", "off"}),
		c.intSetting("Pull completo ogni (s)", "full_pull_interval_seconds"),
		c.choiceSetting("Politica conflitti", "conflict_policy", []string{"newest", "manual"}),
		c.intSetting("Limite conflitti", "conflict_storm_limit"),
		c.intSetting("Finestra conflitti (s)", "conflict_storm_window_seconds"),
		c.floatSetting("Intervallo salute NAS (s)", "metrics_refresh_seconds", 60),
	)
}

func (c *clientApp) settingsSafety() fyne.CanvasObject {
	return container.NewVBox(
		c.sectionTitle("Cancellazioni e sicurezza", "Guardia numerica, per-cartella e conferma manuale"),
		c.boolSetting("Abilita cancellazioni remote", "delete_enabled"),
		c.intSetting("Max file per batch", "max_delete_files"),
		c.intSetting("Max file per finestra", "max_delete_files_per_window"),
		c.intSetting("Finestra sicurezza (s)", "delete_safety_window_seconds"),
		c.intSetting("Durata conferma (s)", "delete_confirmation_seconds"),
		c.floatSetting("Soglia anomalia cartella", "delete_dir_guard_ratio", 0.5),
		c.intSetting("Min file per anomalia", "delete_dir_guard_min_files"),
		c.stringSetting("Sentinelle (separate da virgola)", "delete_sentinels"),
		c.boolSetting("Classifica le differenze", "delete_guard_classify"),
	)
}

func (c *clientApp) settingsIntegrity() fyne.CanvasObject {
	return container.NewVBox(
		c.sectionTitle("Integrità e audit", "Checksum e audit completo"),
		c.boolSetting("Verifica integrità all'avvio", "integrity_audit_enabled"),
		c.intSetting("Intervallo checksum (s)", "integrity_check_interval_seconds"),
		c.intSetting("Ritardo avvio checksum (s)", "integrity_startup_delay_seconds"),
		c.intSetting("Campione checksum", "integrity_check_sample_size"),
		c.intSetting("Audit ogni (giorni)", "integrity_audit_interval_days"),
		c.intSetting("Batch audit", "integrity_audit_batch_size"),
		c.intSetting("Ritardo avvio audit (s)", "integrity_audit_startup_delay_seconds"),
	)
}

func (c *clientApp) settingsAdvanced() fyne.CanvasObject {
	return container.NewVBox(
		c.sectionTitle("Avanzate", "Retention, banda, notifiche, pause e aggiornamenti"),
		c.intSetting("Retention locale (giorni)", "retention_days_local"),
		c.intSetting("Retention NAS (giorni)", "retention_days_remote"),
		c.intSetting("Banda totale (kbps, 0=illimitata)", "bandwidth_kbps"),
		c.intSetting("Banda upload (kbps)", "bandwidth_upload_kbps"),
		c.intSetting("Banda download (kbps)", "bandwidth_download_kbps"),
		c.boolSetting("Notifica a fine sync", "notify_sync_completion"),
		c.boolSetting("Animazione icona tray", "animate_sync_icon"),
		c.choiceSetting("Click tray", "tray_single_click", []string{"menu", "window"}),
		c.choiceSetting("Backend display", "go_display_backend", []string{"auto", "x11", "wayland"}),
		c.boolSetting("Pausa programmata", "pause_schedule_enabled"),
		c.scheduleSetting(),
		c.stringSetting("Percorso update Python", "client_update_remote_path"),
		c.stringSetting("Percorso update Go", "client_update_remote_path_go"),
		c.stringSetting("Versione minima server", "min_server_version"),
		c.deviceLabelsSetting(),
	)
}
