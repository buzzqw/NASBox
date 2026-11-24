package ui

import (
	"fmt"
	"strconv"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"
)

func (c *clientApp) settingRow(label string, object fyne.CanvasObject) fyne.CanvasObject {
	name := widget.NewLabel(label)
	name.Wrapping = fyne.TextWrapWord
	return container.NewBorder(nil, nil, name, nil, object)
}

func (c *clientApp) stringSetting(label, key string) fyne.CanvasObject {
	entry := widget.NewEntry()
	entry.SetText(c.cfg.String(key))
	entry.OnChanged = func(value string) { c.cfg.Set(key, value, true) }
	return c.settingRow(label, entry)
}

func (c *clientApp) intSetting(label, key string) fyne.CanvasObject {
	entry := widget.NewEntry()
	entry.SetText(fmt.Sprint(c.cfg.Int(key)))
	entry.OnChanged = func(value string) {
		if parsed, err := strconv.Atoi(strings.TrimSpace(value)); err == nil {
			c.cfg.Set(key, parsed, true)
		}
	}
	return c.settingRow(label, entry)
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
	return c.settingRow(label, entry)
}

func (c *clientApp) boolSetting(label, key string) fyne.CanvasObject {
	check := widget.NewCheck("", func(value bool) { c.cfg.Set(key, value, true) })
	check.SetChecked(c.cfg.Bool(key))
	return c.settingRow(label, check)
}

func (c *clientApp) textSetting(label, key string) fyne.CanvasObject {
	entry := widget.NewMultiLineEntry()
	entry.SetText(fmt.Sprint(c.cfg.Get(key)))
	entry.Wrapping = fyne.TextWrapWord
	entry.OnChanged = func(value string) { c.cfg.Set(key, value, true) }
	return container.NewVBox(widget.NewLabel(label), entry)
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
		c.stringSetting("Modalità (bidirectional/pull_only/push_only/archive)", "sync_mode"),
		c.intSetting("Intervallo polling (s)", "poll_interval"),
		c.intSetting("Attesa change feed (s)", "change_feed_wait_seconds"),
		c.intSetting("Debounce (s)", "debounce_seconds"),
		c.floatSetting("Stabilità file (s)", "file_stability_seconds", 0.75),
		c.stringSetting("Compressione (auto/on/off)", "compression_mode"),
		c.intSetting("Pull completo ogni (s)", "full_pull_interval_seconds"),
		c.stringSetting("Politica conflitti (newest/manual)", "conflict_policy"),
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
		c.stringSetting("Click tray (menu/window)", "tray_single_click"),
		c.stringSetting("Backend display (auto/x11/wayland)", "go_display_backend"),
		c.boolSetting("Pausa programmata", "pause_schedule_enabled"),
		c.textSetting("Schedule pausa (JSON)", "pause_schedule"),
		c.stringSetting("Percorso update Python", "client_update_remote_path"),
		c.stringSetting("Percorso update Go", "client_update_remote_path_go"),
		c.stringSetting("Versione minima server", "min_server_version"),
		c.textSetting("Nomi dispositivi (JSON)", "device_labels"),
	)
}
