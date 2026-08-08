"""Minimal in-house i18n: a flat key -> {"it": ..., "en": ...} dict plus a
lookup function, no Qt Linguist / gettext build step required (no pylupdate6/
lrelease dependency, nothing to compile). NASBox ships with two languages
built in; the active one is picked from config (persisted, see
Config.language()/set_language()) with a first-run guess from the OS locale.

Widgets read strings once, at construction time -- there is no live re-
translation of an already-built window. Changing the language in Impostazioni
takes effect on the next app start (see settings_tab.py's note about that).
"""
from __future__ import annotations

import locale
from typing import Any

DEFAULT_LANG = "it"
SUPPORTED = ("it", "en")

_current_lang = DEFAULT_LANG

STRINGS: dict[str, dict[str, str]] = {
    # --- dialogs.py: PauseForDialog ---
    "dialogs.pause.preset_15m": {"it": "15 minuti", "en": "15 minutes"},
    "dialogs.pause.preset_30m": {"it": "30 minuti", "en": "30 minutes"},
    "dialogs.pause.preset_1h": {"it": "1 ora", "en": "1 hour"},
    "dialogs.pause.preset_4h": {"it": "4 ore", "en": "4 hours"},
    "dialogs.pause.preset_8h": {"it": "8 ore", "en": "8 hours"},
    "dialogs.pause.preset_custom": {"it": "Personalizzato (minuti)…", "en": "Custom (minutes)…"},
    "dialogs.pause.title": {"it": "Metti in pausa la sincronizzazione", "en": "Pause syncing"},
    "dialogs.pause.duration_label": {"it": "Durata:", "en": "Duration:"},
    "dialogs.pause.custom_minutes_label": {"it": "Minuti personalizzati:", "en": "Custom minutes:"},
    "dialogs.pause.duration_tooltip": {
        "it": "Per quanto tempo mettere in pausa la sincronizzazione.",
        "en": "How long to pause syncing for.",
    },
    "dialogs.pause.custom_minutes_tooltip": {
        "it": "Disponibile solo scegliendo 'Personalizzato' sopra.",
        "en": "Only usable when 'Custom' is selected above.",
    },

    # --- dialogs.py: FolderSetupDialog ---
    "dialogs.folder.title_relocate": {"it": "Sposta la cartella NASBox", "en": "Move the NASBox folder"},
    "dialogs.folder.title_welcome": {"it": "Benvenuto in NASBox", "en": "Welcome to NASBox"},
    "dialogs.folder.intro_relocate": {
        "it": "Scegli la nuova posizione della cartella NASBox su questo PC. "
              "Importante: i file nella vecchia posizione NON vengono spostati "
              "automaticamente -- spostali tu stesso prima di confermare, oppure "
              "lascia che NASBox riscarichi tutto da capo nella nuova posizione.",
        "en": "Choose the new location of the NASBox folder on this PC. "
              "Important: files in the old location are NOT moved automatically -- "
              "move them yourself before confirming, or let NASBox re-download "
              "everything from scratch into the new location.",
    },
    "dialogs.folder.intro_welcome": {
        "it": "NASBox tiene sincronizzata un'unica cartella su questo PC con il NAS "
              "e con gli altri PC collegati: qualunque cosa ci metti dentro, la "
              "ritrovi ovunque. Scegli dove vuoi che sia su questo computer (viene "
              "creata se non esiste già).",
        "en": "NASBox keeps a single folder on this PC synced with the NAS and "
              "every other connected PC: whatever you put in it shows up "
              "everywhere. Choose where you want it on this computer (it's "
              "created if it doesn't already exist).",
    },
    "dialogs.folder.browse_btn": {"it": "Sfoglia…", "en": "Browse…"},
    "dialogs.folder.browse_dialog_title": {"it": "Scegli o crea la cartella NASBox", "en": "Choose or create the NASBox folder"},
    "dialogs.folder.missing_path_title": {"it": "Percorso mancante", "en": "Missing path"},
    "dialogs.folder.missing_path_body": {"it": "Indica dove vuoi mettere la cartella NASBox.", "en": "Tell NASBox where to put the folder."},
    "dialogs.folder.mkdir_failed_title": {"it": "Impossibile creare la cartella", "en": "Couldn't create the folder"},
    "dialogs.folder.path_tooltip": {
        "it": "Percorso completo sul disco di questo PC. Viene creato se non esiste.",
        "en": "Full path on this PC's disk. Created if it doesn't exist yet.",
    },
    "dialogs.folder.browse_tooltip": {"it": "Scegli la cartella con il file manager.", "en": "Pick the folder using the file browser."},

    # --- status_tab.py ---
    "status.group_title": {"it": "Stato", "en": "Status"},
    "status.starting": {"it": "In avvio…", "en": "Starting…"},
    "status.hash_progress": {
        "it": "Verifica file locali in corso: {done} di {total}…",
        "en": "Checking local files: {done} of {total}…",
    },
    "status.sync_now_btn": {"it": "Sincronizza tutto ora", "en": "Sync everything now"},
    "status.pause_btn": {"it": "Pausa", "en": "Pause"},
    "status.resume_btn": {"it": "Riprendi", "en": "Resume"},
    "status.pause_for_btn": {"it": "Pausa per…", "en": "Pause for…"},
    "status.folder_group_title": {"it": "Cartella NASBox", "en": "NASBox folder"},
    "status.open_folder_btn": {"it": "Apri cartella", "en": "Open folder"},
    "status.relocate_btn": {"it": "Cambia percorso…", "en": "Change location…"},
    "status.folder_not_configured": {"it": "(non configurata)", "en": "(not set up)"},
    "status.paused_dialog_title": {"it": "In pausa", "en": "Paused"},
    "status.paused_dialog_body": {
        "it": "La sincronizzazione è in pausa: riprendila per sincronizzare.",
        "en": "Syncing is paused: resume it to sync.",
    },
    "status.not_configured_msg": {"it": "⚠️ Cartella NASBox non ancora configurata", "en": "⚠️ NASBox folder not set up yet"},
    "status.nas_unreachable_msg": {
        "it": "⚠️ NAS non raggiungibile (né direttamente né tramite il bastione)",
        "en": "⚠️ NAS unreachable (neither directly nor via the bastion)",
    },
    "status.paused_countdown_msg": {
        "it": "⏸ In pausa (riprende tra {mins}:{secs}) — connesso a {where}",
        "en": "⏸ Paused (resumes in {mins}:{secs}) — connected to {where}",
    },
    "status.paused_msg": {"it": "⏸ In pausa — connesso a {where}", "en": "⏸ Paused — connected to {where}"},
    "status.active_msg": {"it": "✅ Sincronizzazione attiva — connesso a {where}", "en": "✅ Syncing — connected to {where}"},
    "status.queue_unknown": {"it": "Prima verifica in corso: sto analizzando i file…", "en": "First check in progress: analyzing files…"},
    "status.queue_empty": {"it": "Nessuna differenza rilevata: tutti i file sono sincronizzati con il NAS.", "en": "No differences detected: all files are synced with the NAS."},
    "status.queue_summary": {
        "it": "In attesa: {uploads} caricamenti, {downloads} scaricamenti, {deletes} eliminazioni.",
        "en": "Waiting: {uploads} uploads, {downloads} downloads, {deletes} deletions.",
    },
    "status.diagnostics_unknown": {"it": "Diagnostica NAS non ancora caricata.", "en": "NAS diagnostics have not been loaded yet."},
    "status.diagnostics_refresh_btn": {"it": "Diagnostica NAS", "en": "NAS diagnostics"},
    "status.diagnostics_loading": {"it": "Lettura diagnostica NAS…", "en": "Reading NAS diagnostics…"},
    "status.diagnostics_offline": {"it": "Diagnostica NAS non disponibile: NAS non raggiungibile.", "en": "NAS diagnostics unavailable: NAS is unreachable."},
    "status.diagnostics_failed": {"it": "Diagnostica NAS non disponibile: {detail}", "en": "NAS diagnostics unavailable: {detail}"},
    "status.diagnostics_summary": {
        "it": "NAS: {free} liberi; storico {trash} in {count} versioni. Il server NAS è attivo e sta gestendo la retention.",
        "en": "NAS: {free} free; history {trash} across {count} versions. The NAS server is active and managing retention.",
    },
    "status.integrity_unknown": {"it": "Integrità non verificata in questa sessione.", "en": "Integrity has not been checked in this session."},
    "status.integrity_btn": {"it": "Verifica integrità", "en": "Check integrity"},
    "status.integrity_running": {"it": "Confronto checksum locale/NAS in corso…", "en": "Comparing local/NAS checksums…"},
    "status.integrity_offline": {"it": "Verifica integrità non disponibile: NAS non raggiungibile.", "en": "Integrity check unavailable: NAS is unreachable."},
    "status.integrity_failed": {"it": "Verifica integrità non riuscita: {detail}", "en": "Integrity check failed: {detail}"},
    "status.integrity_ok": {"it": "Integrità verificata: nessuna differenza rilevata.", "en": "Integrity verified: no differences found."},
    "status.integrity_different": {"it": "Integrità: trovate differenze in {count} file. Controlla Trasferimenti prima di intervenire.", "en": "Integrity: differences found in {count} files. Check Transfers before taking action."},
    "status.via_bastion": {"it": "(via bastione)", "en": "(via bastion)"},
    "status.sync_now_tooltip": {
        "it": "Forza subito un controllo verso il NAS, senza aspettare i tempi normali.",
        "en": "Forces a check against the NAS right now, without waiting for the usual timing.",
    },
    "status.pause_tooltip": {
        "it": "Sospende caricamenti e scaricamenti finché non riprendi. Un trasferimento in corso viene interrotto subito.",
        "en": "Suspends uploads and downloads until you resume. A transfer in progress is stopped immediately.",
    },
    "status.pause_for_tooltip": {"it": "Pausa a tempo, con ripresa automatica.", "en": "Timed pause, resumes automatically."},
    "status.open_folder_tooltip": {"it": "Apre la cartella NASBox nel file manager.", "en": "Opens the NASBox folder in the file manager."},
    "status.relocate_tooltip": {
        "it": "Cambia dove si trova la cartella NASBox su questo PC. Non sposta i file esistenti.",
        "en": "Changes where the NASBox folder lives on this PC. Doesn't move existing files.",
    },

    # --- settings_tab.py ---
    "settings.nas_box_title": {"it": "Connessione al NAS", "en": "NAS connection"},
    "settings.nas_lan_placeholder": {"it": "es. nas.local", "en": "e.g. nas.local"},
    "settings.nas_lan_label": {"it": "Host LAN:", "en": "LAN host:"},
    "settings.nas_wan_placeholder": {
        "it": "es. nas.miodominio.com (solo se il NAS è raggiungibile direttamente da fuori)",
        "en": "e.g. nas.mydomain.com (only if the NAS is directly reachable from outside)",
    },
    "settings.nas_wan_label": {"it": "Host WAN diretto (opzionale):", "en": "Direct WAN host (optional):"},
    "settings.nas_user_label": {"it": "Utente SSH sul NAS:", "en": "SSH user on the NAS:"},
    "settings.ssh_port_label": {"it": "Porta SSH sul NAS:", "en": "SSH port on the NAS:"},
    "settings.verify_keys_btn": {"it": "Verifica chiavi SSH…", "en": "Verify SSH keys…"},
    "settings.verify_keys_tooltip": {
        "it": "Mostra le impronte delle chiavi host e consente di accettarle esplicitamente.",
        "en": "Shows host-key fingerprints and lets you accept them explicitly.",
    },
    "settings.verify_keys_title": {"it": "Verifica chiavi SSH", "en": "Verify SSH keys"},
    "settings.verify_keys_no_host": {"it": "Configura almeno un host NAS prima di verificare la chiave.", "en": "Configure at least one NAS host before verifying its key."},
    "settings.verify_keys_no_result": {"it": "Nessuna chiave ricevuta per {label} ({host}).", "en": "No key received for {label} ({host})."},
    "settings.verify_keys_body": {
        "it": "Host: {label} ({host})\n\nImpronte ricevute:\n{fingerprints}\n\nConfrontale con il NAS e accettale solo se coincidono.",
        "en": "Host: {label} ({host})\n\nReceived fingerprints:\n{fingerprints}\n\nCompare them with the NAS and accept only if they match.",
    },
    "settings.verify_keys_failed": {"it": "Impossibile salvare la chiave: {detail}", "en": "Could not save the key: {detail}"},
    "settings.verify_keys_done": {"it": "Chiavi SSH memorizzate. Le prossime connessioni verificheranno l’identità del NAS.", "en": "SSH keys stored. Future connections will verify the NAS identity."},
    "settings.verify_keys_partial": {"it": "Verifica incompleta: il client continuerà in modalità prudente finché tutti gli host configurati non saranno verificati.", "en": "Verification incomplete: the client will remain cautious until all configured hosts are verified."},
    "settings.max_delete_label": {"it": "Limite cancellazioni per trasferimento:", "en": "Delete limit per transfer:"},
    "settings.max_delete_tooltip": {
        "it": "Blocca il trasferimento se propone più cancellazioni di questo numero. Aumentalo solo dopo aver verificato il volume.",
        "en": "Stops a transfer proposing more deletions than this number. Increase only after checking the volume.",
    },
    "settings.nas_lan_tooltip": {
        "it": "Indirizzo del NAS sulla rete locale, es. 192.0.2.10 o nas.local.",
        "en": "The NAS's address on your local network, e.g. 192.0.2.10 or nas.local.",
    },
    "settings.nas_wan_tooltip": {
        "it": "Solo se il NAS è raggiungibile direttamente da internet. Lascia vuoto altrimenti.",
        "en": "Only if the NAS is directly reachable from the internet. Leave blank otherwise.",
    },
    "settings.nas_user_tooltip": {"it": "Utente con cui il client si collega al NAS via SSH.", "en": "The user the client connects to the NAS as, over SSH."},
    "settings.ssh_port_tooltip": {"it": "Di solito 22, salvo configurazioni particolari sul NAS.", "en": "Usually 22, unless the NAS is configured otherwise."},
    "settings.jump_note": {
        "it": "Se il NAS non è raggiungibile direttamente da fuori casa (nessun accesso WAN diretto), "
              "configura qui una macchina 'ponte' che sia raggiungibile da internet e che veda il NAS "
              "sulla propria LAN: ci si passerà attraverso in automatico (SSH ProxyJump) solo quando "
              "l'accesso diretto al NAS non è disponibile.",
        "en": "If the NAS isn't directly reachable from outside your home (no direct WAN access), "
              "set up a 'bridge' machine here that's reachable from the internet and can see the NAS "
              "on its own LAN: it's used automatically (SSH ProxyJump) only when direct access to the "
              "NAS isn't available.",
    },
    "settings.jump_host_placeholder": {"it": "es. bastion.example.net", "en": "e.g. bastion.example.net"},
    "settings.jump_host_label": {"it": "Host bastione (ponte):", "en": "Bastion host:"},
    "settings.jump_port_label": {"it": "Porta SSH sul bastione:", "en": "SSH port on the bastion:"},
    "settings.jump_user_placeholder": {
        "it": "vuoto = usa lo stesso utente SSH del NAS",
        "en": "blank = use the same SSH user as the NAS",
    },
    "settings.jump_user_label": {"it": "Utente SSH sul bastione:", "en": "SSH user on the bastion:"},
    "settings.remote_script_placeholder": {
        "it": "es. /volume1/NASBox/sync-daemon-server/sync-daemon-server.sh",
        "en": "e.g. /volume1/NASBox/sync-daemon-server/sync-daemon-server.sh",
    },
    "settings.restart_daemon_btn": {"it": "Riavvia demone", "en": "Restart daemon"},
    "settings.restart_running": {"it": "Riavvio in corso…", "en": "Restarting…"},
    "settings.remote_script_tooltip": {
        "it": "Percorso di sync-daemon-server.sh sul NAS. Puoi lasciarlo vuoto: 'Rileva dal NAS' lo trova da solo.",
        "en": "Path to sync-daemon-server.sh on the NAS. You can leave it blank: 'Detect from NAS' finds it on its own.",
    },
    "settings.restart_daemon_tooltip": {
        "it": "Riavvia il demone sul NAS. Riguarda solo la pulizia dello storico, non la sincronizzazione dei file.",
        "en": "Restarts the daemon on the NAS. Only affects history cleanup, not file syncing.",
    },
    "settings.remote_script_label": {"it": "Script server sul NAS:", "en": "Server script on the NAS:"},
    "settings.remote_prefix_placeholder": {"it": "es. /volume1/NASBox", "en": "e.g. /volume1/NASBox"},
    "settings.detect_btn": {"it": "Rileva dal NAS", "en": "Detect from NAS"},
    "settings.detect_running": {"it": "Rilevamento in corso…", "en": "Detecting…"},
    "settings.remote_prefix_label": {"it": "Cartella NASBox sul NAS:", "en": "NASBox folder on the NAS:"},
    "settings.detect_note": {
        "it": "'Rileva dal NAS' si collega, verifica che il demone server sia attivo e legge da lì "
              "il percorso remoto e la retention -- niente da ricopiare a mano.",
        "en": "'Detect from NAS' connects, checks that the server daemon is running, and reads the "
              "remote path and retention straight from there -- nothing to copy by hand.",
    },
    "settings.delete_enabled_checkbox": {
        "it": "Propaga le cancellazioni (altrimenti solo aggiunte/modifiche)",
        "en": "Propagate deletions (otherwise additions/changes only)",
    },
    "settings.delete_enabled_tooltip": {
        "it": "Se spenta, cancellare un file su un PC NON lo cancella sul NAS o sugli altri PC (resta lì). Se accesa, le cancellazioni si propagano -- protette comunque dal cestino/storico.",
        "en": "If off, deleting a file on one PC does NOT delete it on the NAS or other PCs (it stays there). If on, deletions propagate -- still protected by the trash/history.",
    },
    "settings.save_nas_btn": {"it": "Salva connessione", "en": "Save connection"},
    "settings.save_nas_tooltip": {"it": "Salva questi valori e li applica subito, senza riavviare l'app.", "en": "Saves these values and applies them right away, no app restart needed."},

    "settings.bandwidth_title": {"it": "Limite di banda (KB/s, 0 = illimitato)", "en": "Bandwidth limit (KB/s, 0 = unlimited)"},
    "settings.bandwidth_tooltip": {"it": "0 = nessun limite.", "en": "0 = no limit."},
    "settings.apply_bandwidth_tooltip": {"it": "Applica i limiti al ciclo di sincronizzazione successivo.", "en": "Applies the limits starting with the next sync cycle."},
    "settings.upload_label": {"it": "↑ Carica:", "en": "↑ Upload:"},
    "settings.download_label": {"it": "↓ Scarica:", "en": "↓ Download:"},
    "settings.apply_btn": {"it": "Applica", "en": "Apply"},

    "settings.cadence_title": {"it": "Frequenza sincronizzazione", "en": "Sync frequency"},
    "settings.poll_label": {"it": "Controlla il NAS per novità ogni:", "en": "Check the NAS for updates every:"},
    "settings.cadence_note": {
        "it": "Le modifiche locali vengono inviate al NAS quasi subito (appena rilevate). "
              "Questo intervallo riguarda solo lo scaricare le modifiche fatte da altri PC.",
        "en": "Local changes are sent to the NAS almost immediately (as soon as detected). "
              "This interval only affects downloading changes made by other PCs.",
    },
    "settings.apply_poll_tooltip": {"it": "Applica il nuovo intervallo subito.", "en": "Applies the new interval right away."},

    "settings.exclude_title": {"it": "Escludi dalla sincronizzazione", "en": "Exclude from sync"},
    "settings.exclude_note": {
        "it": "Pattern (stile rsync) mai sincronizzati, in nessuna direzione -- utile per cache, "
              "cartelle di build o file temporanei che non hanno senso su un NAS condiviso.",
        "en": "Patterns (rsync-style) never synced, in either direction -- useful for caches, "
              "build folders, or temp files that don't belong on a shared NAS.",
    },
    "settings.exclude_hint": {"it": "es. node_modules/  *.tmp  .cache/", "en": "e.g. node_modules/  *.tmp  .cache/"},
    "settings.exclude_input_tooltip": {"it": "Scrivi un pattern e premi Invio o 'Aggiungi'.", "en": "Type a pattern and press Enter or 'Add'."},
    "settings.exclude_add_btn": {"it": "Aggiungi", "en": "Add"},
    "settings.exclude_add_tooltip": {"it": "Aggiunge il pattern scritto sopra all'elenco.", "en": "Adds the pattern typed above to the list."},
    "settings.exclude_remove_btn": {"it": "Rimuovi selezionato", "en": "Remove selected"},
    "settings.exclude_remove_tooltip": {"it": "Rimuove il pattern selezionato nell'elenco.", "en": "Removes the pattern selected in the list."},
    "settings.auto_exclude_active": {
        "it": "🔒 Esclusa automaticamente anche: {path} (cartella dello script server sul NAS, non modificabile qui).",
        "en": "🔒 Also automatically excluded: {path} (the NAS server script's own folder, not editable here).",
    },
    "settings.auto_exclude_none": {
        "it": "Nessuna cartella esclusa automaticamente: imposta 'Script server sul NAS' sopra (o usa 'Rileva dal NAS') "
              "perché il client protegga anche quella del pacchetto server, se si trova dentro la cartella NASBox.",
        "en": "No folder auto-excluded yet: set 'Server script on the NAS' above (or use 'Detect from NAS') "
              "so the client can also protect the server package's own folder, if it lives inside the NASBox folder.",
    },

    "settings.language_title": {"it": "Lingua", "en": "Language"},
    "settings.language_label": {"it": "Lingua dell'app:", "en": "App language:"},
    "settings.language_note": {
        "it": "Riavvia l'app per applicare il cambio lingua.",
        "en": "Restart the app to apply the language change.",
    },
    "settings.language_auto": {"it": "Automatica (sistema)", "en": "Automatic (system)"},
    "settings.language_it": {"it": "Italiano", "en": "Italiano"},
    "settings.language_en": {"it": "English", "en": "English"},

    "settings.info_title": {"it": "Informazioni", "en": "About"},
    "settings.info_version_label": {"it": "Versione:", "en": "Version:"},
    "settings.info_min_server_label": {"it": "Versione minima server NAS richiesta:", "en": "Minimum required NAS server version:"},
    "settings.info_config_label": {"it": "File di configurazione:", "en": "Configuration file:"},
    "settings.info_log_label": {"it": "File di log:", "en": "Log file:"},

    "settings.nas_unreachable_title": {"it": "NAS non raggiungibile", "en": "NAS unreachable"},
    "settings.nas_unreachable_body": {
        "it": "Impossibile contattare il NAS, né direttamente né tramite il bastione, con questi parametri.",
        "en": "Couldn't reach the NAS, neither directly nor via the bastion, with these parameters.",
    },
    "settings.daemon_not_found_title": {"it": "Demone non trovato", "en": "Daemon not found"},
    "settings.daemon_not_found_body": {
        "it": "Non trovo un processo sync-daemon-server.sh in esecuzione sul NAS "
              "(magari è fermo, o girava già a un percorso non standard). "
              "Indica il percorso a mano nel campo 'Script server sul NAS' e riprova.",
        "en": "Can't find a sync-daemon-server.sh process running on the NAS "
              "(maybe it's stopped, or already running from a non-standard path). "
              "Enter the path by hand in the 'Server script on the NAS' field and try again.",
    },
    "settings.multiple_instances_title": {"it": "Più istanze trovate", "en": "Multiple instances found"},
    "settings.multiple_instances_body": {
        "it": "Trovate più istanze di sync-daemon-server.sh in esecuzione sul NAS:\n\n{list}"
              "\n\nUso la prima ({first}) -- se non è quella giusta, indicala a mano "
              "nel campo 'Script server sul NAS'.",
        "en": "Found multiple sync-daemon-server.sh instances running on the NAS:\n\n{list}"
              "\n\nUsing the first one ({first}) -- if that's not the right one, set it by hand "
              "in the 'Server script on the NAS' field.",
    },
    "settings.detect_failed_title": {"it": "Rilevamento non riuscito", "en": "Detection failed"},
    "settings.detect_done_title": {"it": "Rilevamento completato", "en": "Detection complete"},
    "settings.detect_done_body": {
        "it": "{status_line}.\nCartella NASBox sul NAS: {share_root}\nRetention sul NAS: {retention} giorni{version_line}",
        "en": "{status_line}.\nNASBox folder on the NAS: {share_root}\nNAS retention: {retention} days{version_line}",
    },
    "settings.daemon_active": {"it": "✅ Demone server attivo sul NAS", "en": "✅ Server daemon active on the NAS"},
    "settings.daemon_inactive": {"it": "⚠️ Demone server NON in esecuzione sul NAS", "en": "⚠️ Server daemon NOT running on the NAS"},
    "settings.not_set_placeholder": {"it": "(non impostata)", "en": "(not set)"},
    "settings.outdated_version_note": {
        "it": "\n\n⚠️ Versione del pacchetto server: {version} (questo client si aspetta almeno "
              "la {expected}). Copia la cartella server/ aggiornata sul NAS.",
        "en": "\n\n⚠️ Server package version: {version} (this client expects at least "
              "{expected}). Copy the updated server/ folder to the NAS.",
    },
    "settings.script_not_configured_title": {"it": "Script non configurato", "en": "Script not configured"},
    "settings.script_not_configured_body": {
        "it": "Indica il percorso dello script server sul NAS, oppure usa 'Rileva dal NAS' per trovarlo.",
        "en": "Enter the path to the server script on the NAS, or use 'Detect from NAS' to find it.",
    },
    "settings.restart_confirm_title": {"it": "Conferma riavvio", "en": "Confirm restart"},
    "settings.restart_confirm_body": {
        "it": "Riavviare il demone server sul NAS ({script_path})?\n\n"
              "Il demone gestisce solo la pulizia dello storico/retention sul NAS -- "
              "la sincronizzazione dei file non dipende da lui e non verrà interrotta.",
        "en": "Restart the server daemon on the NAS ({script_path})?\n\n"
              "The daemon only handles history/retention cleanup on the NAS -- "
              "file syncing doesn't depend on it and won't be interrupted.",
    },
    "settings.daemon_restarted_title": {"it": "Demone riavviato", "en": "Daemon restarted"},
    "settings.daemon_restarted_default": {"it": "Demone riavviato con successo.", "en": "Daemon restarted successfully."},
    "settings.restart_failed_title": {"it": "Riavvio non riuscito", "en": "Restart failed"},
    "settings.restart_failed_default": {"it": "Errore sconosciuto.", "en": "Unknown error."},

    "settings.maint_title": {"it": "Manutenzione", "en": "Maintenance"},
    "settings.maint_conflict_note": {
        "it": "Quando due versioni di un file vengono modificate contemporaneamente su PC diversi, "
              "NASBox conserva entrambe: una resta al suo posto, l'altra viene rinominata con "
              "\"(conflitto da ...)\" per non perdere dati. Questi file sopravvivono finché non "
              "vengono cancellati a mano.",
        "en": "When two versions of a file are modified at the same time on different PCs, "
              "NASBox keeps both: one stays in place, the other is renamed with "
              "\"(conflitto da ...)\" to avoid data loss. These files persist until deleted by hand.",
    },
    "settings.maint_conflict_count_unknown": {"it": "Clicca \"Cerca\" per trovare i file di conflitto.", "en": "Click \"Scan\" to find conflict files."},
    "settings.maint_conflict_count_none": {"it": "Nessun file di conflitto trovato nella cartella NASBox.", "en": "No conflict files found in the NASBox folder."},
    "settings.maint_conflict_count": {"it": "Trovati {count} file di conflitto.", "en": "Found {count} conflict files."},
    "settings.maint_conflict_no_folder": {"it": "Cartella NASBox non configurata.", "en": "NASBox folder not set up."},
    "settings.maint_scan_btn": {"it": "Cerca conflitti", "en": "Scan for conflicts"},
    "settings.maint_scan_tooltip": {"it": "Cerca nella cartella NASBox tutti i file con \"(conflitto da ...)\" nel nome.", "en": "Scans the NASBox folder for all files with \"(conflitto da ...)\" in their name."},
    "settings.maint_delete_btn": {"it": "Elimina i conflitti trovati", "en": "Delete found conflicts"},
    "settings.maint_delete_tooltip": {
        "it": "Elimina definitivamente i file trovati dalla scansione. Verifica prima di procedere: l'operazione non è reversibile.",
        "en": "Permanently deletes the files found by the scan. Verify before proceeding: this cannot be undone.",
    },
    "settings.maint_delete_confirm_title": {"it": "⚠️ Eliminare i file di conflitto?", "en": "⚠️ Delete conflict files?"},
    "settings.maint_delete_confirm_body": {
        "it": "Stai per eliminare definitivamente {count} file di conflitto dalla cartella NASBox.\n\n"
              "Prima di procedere, assicurati di aver controllato il contenuto di ogni file: "
              "potrebbero esserci modifiche che vuoi conservare. Una volta eliminati, non potranno "
              "essere recuperati (non passano dal cestino).\n\n"
              "In futuro, i conflitti verranno gestiti automaticamente con retention configurabile; "
              "per ora vanno rimossi a mano.\n\n"
              "Procedere con l'eliminazione definitiva?",
        "en": "You are about to permanently delete {count} conflict files from the NASBox folder.\n\n"
              "Before proceeding, make sure you have checked the contents of each file: "
              "they may contain changes you want to keep. Once deleted, they cannot be "
              "recovered (they don't go through the trash).\n\n"
              "In the future, conflicts will be handled automatically with configurable retention; "
              "for now they must be removed by hand.\n\n"
              "Proceed with permanent deletion?",
    },
    "settings.maint_delete_done_title": {"it": "Conflitti eliminati", "en": "Conflicts deleted"},
    "settings.maint_delete_done_body": {"it": "Eliminati {count} file di conflitto.", "en": "Deleted {count} conflict files."},
    "settings.maint_delete_partial_title": {"it": "Eliminazione parziale", "en": "Partial deletion"},
    "settings.maint_delete_partial_body": {"it": "Eliminati {deleted} file, {failed} non eliminabili:\n{names}", "en": "Deleted {deleted} files, {failed} could not be deleted:\n{names}"},

    # --- transfers_tab.py ---
    "transfers.speed_title": {"it": "Velocità", "en": "Speed"},
    "transfers.progress_idle": {"it": "Coda: -", "en": "Queue: -"},
    "transfers.progress_scan": {"it": "Coda: aggiorno l'anteprima…", "en": "Queue: updating preview…"},
    "transfers.progress_preparing": {"it": "Coda: sto preparando la lista dei file da sincronizzare…", "en": "Queue: building the list of files to sync…"},
    "transfers.progress_format": {"it": "Coda: {done}/{total} ({percent}%)", "en": "Queue: {done}/{total} ({percent}%)"},
    "transfers.preflight_progress": {
        "it": "Sto confrontando {total} file con il NAS: verificati {done} ({seconds}s trascorsi)…",
        "en": "Comparing {total} files with the NAS: {done} checked ({seconds}s elapsed)…",
    },
    "transfers.waiting": {"it": "Coda pronta: sto monitorando la cartella in tempo reale. Le modifiche locali vengono inviate subito, quelle dal NAS ogni 60 secondi.", "en": "Queue ready: monitoring the folder in real time. Local changes are sent immediately, NAS changes every 60 seconds."},
    "transfers.waiting_for_lock": {"it": "{direction} in attesa del lock NAS: verifico che nessun altro client stia sincronizzando ({seconds}s trascorsi).", "en": "{direction} waiting for the NAS lock: checking that no other client is syncing ({seconds}s elapsed)."},
    "transfers.preparing": {"it": "{direction} in preparazione: sto analizzando i file e costruendo la lista ({seconds}s trascorsi).", "en": "{direction} preparing: analyzing files and building the list ({seconds}s elapsed)."},
    "transfers.queue_scan_running": {"it": "Sto verificando le differenze con il NAS ({seconds}s trascorsi)…", "en": "Checking for differences with the NAS ({seconds}s elapsed)…"},
    "transfers.lock_unavailable": {"it": "Trasferimento rimandato: {detail}", "en": "Transfer deferred: {detail}"},
    "transfers.completed_batch": {"it": "{direction} completato: {count} operazioni eseguite. Il monitoraggio continua.", "en": "{direction} complete: {count} operations completed. Monitoring continues."},
    "transfers.failed_batch": {"it": "{direction} non completato: controlla il Log.", "en": "{direction} did not complete: check the Log."},
    "transfers.active_starting": {"it": "{direction} in corso: sto confrontando i file con il NAS; compariranno qui sotto appena rilevati ({seconds}s trascorsi).", "en": "{direction} in progress: comparing files with the NAS; they will appear below as detected ({seconds}s elapsed)."},
    "transfers.active_file": {"it": "{direction} in corso: {path} ({seconds}s trascorsi)", "en": "{direction} in progress: {path} ({seconds}s elapsed)"},
    "transfers.upload_speed_idle": {"it": "↑ Carica: -", "en": "↑ Upload: -"},
    "transfers.download_speed_idle": {"it": "↓ Scarica: -", "en": "↓ Download: -"},
    "transfers.upload_prefix": {"it": "↑ Carica", "en": "↑ Upload"},
    "transfers.download_prefix": {"it": "↓ Scarica", "en": "↓ Download"},
    "transfers.search_label": {"it": "Cerca:", "en": "Search:"},
    "transfers.search_placeholder": {"it": "file…", "en": "file…"},
    "transfers.no_items": {"it": "Nessun file in coda.", "en": "No files queued."},
    "transfers.no_items_all_synced": {"it": "Nessuna differenza rilevata con il NAS: tutti i file sono sincronizzati.", "en": "No differences detected with the NAS: all files are synced."},
    "transfers.queue_note": {
        "it": "Questa è l'anteprima della coda attuale, non lo storico. Si aggiorna durante e dopo la sincronizzazione.",
        "en": "This is the current queue preview, not history. It updates during and after syncing.",
    },
    "transfers.visible_limit": {
        "it": "Mostro i primi {shown} di {total} elementi per mantenere l'app reattiva. Usa Cerca per trovare un file specifico.",
        "en": "Showing the first {shown} of {total} items to keep the app responsive. Use Search to find a specific file.",
    },
    "transfers.summary": {
        "it": "{up} da caricare · {down} da scaricare · {delete} da eliminare",
        "en": "{up} to upload · {down} to download · {delete} to delete",
    },
    "transfers.summary_preparing": {
        "it": "Piano in preparazione: nessun file è ancora dichiarato completato.",
        "en": "Plan in preparation: no file has been declared complete yet.",
    },
    "transfers.summary_waiting_for_lock": {
        "it": "Trasferimento in attesa del lock NAS: la sincronizzazione non è conclusa.",
        "en": "Transfer waiting for the NAS lock: synchronization is not complete.",
    },
    "transfers.summary_active_no_items": {
        "it": "Trasferimento in corso: sto confrontando i file con il NAS; la lista è ancora vuota.",
        "en": "Transfer in progress: comparing files with the NAS; the list is still empty.",
    },
    "transfers.summary_scan_running": {
        "it": "Sto verificando le differenze con il NAS: la lista qui sotto potrebbe non essere completa.",
        "en": "Checking for differences with the NAS: the list below may not be complete yet.",
    },
    "transfers.col_direction": {"it": "Direzione", "en": "Direction"},
    "transfers.col_file": {"it": "File", "en": "File"},
    "transfers.col_size": {"it": "Dimensione", "en": "Size"},
    "transfers.col_state": {"it": "Stato", "en": "Status"},
    "transfers.col_progress": {"it": "Progresso", "en": "Progress"},
    "transfers.item_waiting": {"it": "In attesa", "en": "Waiting"},
    "transfers.item_active": {"it": "In corso", "en": "In progress"},
    "transfers.item_completed": {"it": "Completato", "en": "Completed"},
    "transfers.dir_upload": {"it": "↑ Carica", "en": "↑ Upload"},
    "transfers.dir_download": {"it": "↓ Scarica", "en": "↓ Download"},
    "transfers.dir_delete_remote": {"it": "🗑 Elimina sul NAS", "en": "🗑 Delete on NAS"},
    "transfers.dir_delete_local": {"it": "🗑 Elimina in locale", "en": "🗑 Delete locally"},

    # --- updater.py / main.py ---
    "updater.available_title": {"it": "Aggiornamento disponibile", "en": "Update available"},
    "updater.available_body": {
        "it": "È disponibile la versione {version} del client ({origin}).\n\nVuoi aggiornarla e riavviare NASBox?",
        "en": "Client version {version} is available ({origin}).\n\nUpdate and restart NASBox?",
    },
    "updater.restarting_title": {"it": "Aggiornamento client", "en": "Client update"},
    "updater.restarting_body": {"it": "Aggiornamento completato. Riavvio NASBox…", "en": "Update complete. Restarting NASBox…"},
    "updater.failed_title": {"it": "Aggiornamento non riuscito", "en": "Update failed"},
    "updater.failed_body": {
        "it": "Non è stato possibile aggiornare il client.\n\n{detail}\n\nAvvio comunque la versione attuale.",
        "en": "The client could not be updated.\n\n{detail}\n\nStarting the current version anyway.",
    },

    # --- log_tab.py ---
    "log.filter_label": {"it": "Filtra per azione:", "en": "Filter by action:"},
    "log.all_filter": {"it": "Tutti", "en": "All"},
    "log.search_label": {"it": "Cerca:", "en": "Search:"},
    "log.search_placeholder": {"it": "percorso o dettaglio…", "en": "path or detail…"},
    "log.filter_tooltip": {"it": "Mostra solo le righe di questo tipo di azione.", "en": "Shows only rows for this kind of action."},
    "log.search_tooltip": {"it": "Filtra per percorso file o testo del dettaglio.", "en": "Filters by file path or detail text."},
    "log.refresh_btn": {"it": "Aggiorna", "en": "Refresh"},
    "log.refresh_tooltip": {"it": "Ricarica il log da disco.", "en": "Reloads the log from disk."},
    "log.col_time": {"it": "Ora", "en": "Time"},
    "log.col_action": {"it": "Azione", "en": "Action"},
    "log.col_path": {"it": "Percorso", "en": "Path"},
    "log.col_detail": {"it": "Dettaglio", "en": "Detail"},

    # --- history_tab.py ---
    "history.retention_title": {"it": "Quanto storico conservare", "en": "How much history to keep"},
    "history.local_trash_label": {
        "it": "Cestino locale su questo PC (giorni, 0 = mai eliminare):",
        "en": "Local trash on this PC (days, 0 = never delete):",
    },
    "history.remote_retention_label": {
        "it": "Sul NAS (letta dal NAS, non modificabile qui):",
        "en": "On the NAS (read from the NAS, not editable here):",
    },
    "history.local_trash_tooltip": {"it": "0 = non eliminare mai automaticamente le versioni storiche locali.", "en": "0 = never automatically delete local historical versions."},
    "history.save_local_btn": {"it": "Salva locale", "en": "Save locally"},
    "history.save_local_tooltip": {"it": "Salva il valore di retention locale.", "en": "Saves the local retention value."},
    "history.note": {
        "it": "Non sono la stessa cosa: lo storico sul NAS conserva le versioni sovrascritte quando "
              "TU carichi una modifica (protegge gli altri PC dalle tue modifiche). Il cestino "
              "locale conserva invece la TUA versione se uno scaricamento da un altro PC stava per "
              "sovrascriverla mentre non era ancora stata caricata (protegge te da un conflitto). "
              "La retention sul NAS è di competenza del pacchetto server (sync-daemon-server), che "
              "continua a fare pulizia anche a client spenti; per aggiornarla qui usa 'Rileva dal NAS' "
              "nel tab Impostazioni -- non è un valore che si imposta in questo client.",
        "en": "These are not the same thing: history on the NAS keeps versions overwritten when "
              "YOU upload a change (protects other PCs from your changes). Local trash instead "
              "keeps YOUR version if a download from another PC was about to overwrite it while "
              "it hadn't been uploaded yet (protects you from a conflict). Retention on the NAS "
              "is handled by the server package (sync-daemon-server), which keeps cleaning up even "
              "while clients are off; to refresh it here use 'Detect from NAS' in the Impostazioni "
              "tab -- it's not a value you set in this client.",
    },
    "history.prune_local_btn": {"it": "Pulisci storico locale ora", "en": "Clean local history now"},
    "history.prune_local_tooltip": {"it": "Rimuove subito le versioni locali più vecchie della retention impostata.", "en": "Immediately removes local versions older than the retention set above."},
    "history.prune_remote_btn": {"it": "Chiedi al NAS di pulire ora", "en": "Ask the NAS to clean now"},
    "history.prune_remote_tooltip": {
        "it": "Chiede al demone sul NAS di eseguire subito un pass di pulizia sul SUO storico. "
              "Non tocca l'elenco qui sotto, che mostra solo il cestino locale su questo PC.",
        "en": "Asks the daemon on the NAS to run a cleanup pass on ITS OWN history right now. "
              "Doesn't touch the list below, which only shows the local trash on this PC.",
    },
    "history.prune_remote_running": {"it": "Pulizia in corso…", "en": "Cleaning…"},
    "history.search_label": {"it": "Cerca:", "en": "Search:"},
    "history.search_placeholder": {"it": "cartella o file…", "en": "folder or file…"},
    "history.scope_label": {"it": "Mostra:", "en": "Show:"},
    "history.scope_local": {"it": "Cestino di questo PC", "en": "This PC's trash"},
    "history.scope_remote": {"it": "Storico sul NAS", "en": "History on NAS"},
    "history.list_scope_local": {
        "it": "Elenco mostrato: cestino locale di questo PC. Lo storico sul NAS non viene elencato qui.",
        "en": "Showing: this PC's local trash. History stored on the NAS is not listed here.",
    },
    "history.list_scope_remote": {
        "it": "Elenco mostrato: storico conservato sul NAS. Il ripristino scarica una copia scelta da te.",
        "en": "Showing: history kept on the NAS. Restore downloads a copy to your chosen location.",
    },
    "history.list_scope_remote_loading": {"it": "Caricamento dello storico dal NAS…", "en": "Loading history from the NAS…"},
    "history.list_scope_remote_offline": {"it": "Storico NAS non disponibile: NAS non raggiungibile.", "en": "NAS history unavailable: NAS is unreachable."},
    "history.list_scope_remote_failed": {"it": "Impossibile leggere lo storico NAS: {detail}", "en": "Couldn't read NAS history: {detail}"},
    "history.remote_restore_picker_title": {"it": "Scegli dove salvare la versione dal NAS", "en": "Choose where to save the NAS version"},
    "history.remote_restore_done_body": {"it": "Versione dal NAS ripristinata in '{path}'.", "en": "NAS version restored to '{path}'."},
    "history.remote_restore_one_title": {"it": "Scegli un file", "en": "Choose one file"},
    "history.remote_restore_one_body": {"it": "Dallo storico NAS puoi ripristinare un file alla volta, scegliendone la destinazione.", "en": "From NAS history you can restore one file at a time to a chosen destination."},
    "history.col_name": {"it": "Nome", "en": "Name"},
    "history.col_version": {"it": "Versione (data e ora)", "en": "Version (date and time)"},
    "history.col_size": {"it": "Dimensione", "en": "Size"},
    "history.col_age": {"it": "Età (giorni)", "en": "Age (days)"},
    "history.restore_btn": {"it": "Ripristina selezionati…", "en": "Restore selected…"},
    "history.restore_tooltip": {
        "it": "Seleziona più file e cartelle con Ctrl o Maiusc. Per ogni file viene ripristinata la versione più recente selezionata. Doppio click ripristina subito una sola riga.",
        "en": "Select multiple files and folders with Ctrl or Shift. The newest selected version of each file is restored. Double-click restores one row immediately.",
    },
    "history.root_group_label": {"it": "(cartella radice)", "en": "(root folder)"},
    "history.group_label": {"it": "{name}  ({count} file)", "en": "{name}  ({count} files)"},
    "history.remote_retention_unknown": {"it": "non ancora rilevata", "en": "not detected yet"},
    "history.remote_retention_value": {"it": "{days} giorni", "en": "{days} days"},
    "history.saved_title": {"it": "Salvato", "en": "Saved"},
    "history.saved_body": {"it": "Retention locale salvata.", "en": "Local retention saved."},
    "history.prune_local_title": {"it": "Pulizia locale", "en": "Local cleanup"},
    "history.prune_local_body": {
        "it": "Rimosse {count} versioni dallo storico locale.",
        "en": "Removed {count} versions from local history.",
    },
    "history.prune_local_none_body": {
        "it": "Nessuna versione più vecchia di {days} giorni: non c'è nulla da rimuovere in base alla retention "
              "impostata. Per eliminare file specifici indipendentemente dall'età, selezionali nell'elenco e "
              "usa 'Elimina definitivamente'.",
        "en": "Nothing is older than {days} days, so there's nothing to remove under the current retention "
              "setting. To delete specific files regardless of age, select them in the list and use "
              "'Delete permanently'.",
    },
    "history.nas_unreachable_title": {"it": "NAS non raggiungibile", "en": "NAS unreachable"},
    "history.nas_unreachable_body": {"it": "Impossibile contattare il NAS in questo momento.", "en": "Couldn't reach the NAS right now."},
    "history.prune_remote_title": {"it": "Pulizia remota", "en": "Remote cleanup"},
    "history.prune_remote_body": {
        "it": "Il NAS ha eseguito la pulizia del SUO storico. L'elenco qui sotto è quello locale su questo "
              "PC e non cambia: se non è cambiato nulla è normale, non è un errore.",
        "en": "The NAS ran a cleanup of ITS OWN history. The list below is the local one on this PC and "
              "doesn't change: if nothing looks different here, that's expected, not a bug.",
    },
    "history.prune_remote_failed_title": {"it": "Pulizia remota non riuscita", "en": "Remote cleanup failed"},
    "history.not_configured_title": {"it": "Cartella NASBox non configurata", "en": "NASBox folder not set up"},
    "history.not_configured_body": {
        "it": "Configura prima la cartella NASBox (tab Stato).",
        "en": "Set up the NASBox folder first (Status tab).",
    },
    "history.confirm_restore_title": {"it": "Conferma ripristino", "en": "Confirm restore"},
    "history.confirm_restore_body": {
        "it": "Ripristinare '{path}' alla versione del {timestamp}?\nIl file attuale (se presente) verrà sovrascritto.",
        "en": "Restore '{path}' to the version from {timestamp}?\nThe current file (if any) will be overwritten.",
    },
    "history.confirm_restore_many_body": {
        "it": "Ripristinare {count} file? I file attuali (se presenti) verranno sovrascritti.",
        "en": "Restore {count} files? Current files (if any) will be overwritten.",
    },
    "history.restore_done_title": {"it": "Ripristino completato", "en": "Restore complete"},
    "history.restore_done_body": {"it": "'{path}' ripristinato.", "en": "'{path}' restored."},
    "history.restore_failed_title": {"it": "Ripristino non riuscito", "en": "Restore failed"},
    "history.restore_failed_body": {
        "it": "Il file storico non è più presente.",
        "en": "The historical file is no longer there.",
    },
    "history.confirm_restore_folder_title": {"it": "Conferma ripristino cartella", "en": "Confirm folder restore"},
    "history.confirm_restore_folder_body": {
        "it": "Ripristinare {count} file in '{folder}' alla loro versione più recente nello storico?\n"
              "I file attuali (se presenti) verranno sovrascritti.",
        "en": "Restore {count} files in '{folder}' to their most recent version in history?\n"
              "Current files (if any) will be overwritten.",
    },
    "history.restore_partial_title": {"it": "Ripristino parziale", "en": "Partial restore"},
    "history.restore_partial_body": {
        "it": "Ripristinati {restored} file, {failed} non più presenti nello storico.",
        "en": "Restored {restored} files, {failed} no longer present in history.",
    },
    "history.restore_all_done_body": {"it": "Ripristinati {count} file.", "en": "Restored {count} files."},

    "history.delete_btn": {"it": "Elimina definitivamente…", "en": "Delete permanently…"},
    "history.delete_tooltip": {
        "it": "Elimina per sempre le versioni e le cartelle selezionate dal cestino locale, indipendentemente dall'età. Usa Ctrl o Maiusc per una selezione multipla. Non recuperabile.",
        "en": "Permanently deletes selected versions and folders from the local trash, regardless of age. Use Ctrl or Shift for multiple selection. Not recoverable.",
    },
    "history.confirm_delete_title": {"it": "Conferma eliminazione definitiva", "en": "Confirm permanent deletion"},
    "history.confirm_delete_body": {
        "it": "Eliminare per sempre '{path}' (versione del {timestamp}) dallo storico locale?\n"
              "L'operazione non è reversibile.",
        "en": "Permanently delete '{path}' (version from {timestamp}) from local history?\n"
              "This cannot be undone.",
    },
    "history.confirm_delete_many_body": {
        "it": "Eliminare per sempre {count} versioni dallo storico locale? L'operazione non è reversibile.",
        "en": "Permanently delete {count} versions from local history? This cannot be undone.",
    },
    "history.delete_failed_title": {"it": "Eliminazione non riuscita", "en": "Deletion failed"},
    "history.delete_failed_body": {"it": "Il file storico non è più presente.", "en": "The historical file is no longer there."},
    "history.confirm_delete_folder_title": {"it": "Conferma eliminazione definitiva cartella", "en": "Confirm permanent folder deletion"},
    "history.confirm_delete_folder_body": {
        "it": "Eliminare per sempre TUTTE le {count} versioni storiche in '{folder}'?\n"
              "L'operazione non è reversibile.",
        "en": "Permanently delete ALL {count} historical versions in '{folder}'?\n"
              "This cannot be undone.",
    },
    "history.delete_done_title": {"it": "Eliminazione completata", "en": "Deletion complete"},
    "history.delete_all_done_body": {"it": "Eliminate {count} versioni.", "en": "Deleted {count} versions."},
    "history.delete_partial_title": {"it": "Eliminazione parziale", "en": "Partial deletion"},
    "history.delete_partial_body": {
        "it": "Eliminate {deleted} versioni, {failed} non più presenti nello storico.",
        "en": "Deleted {deleted} versions, {failed} no longer present in history.",
    },

    # --- tray.py ---
    "tray.starting": {"it": "In avvio…", "en": "Starting…"},
    "tray.no_recent_activity": {"it": "(nessuna attività recente)", "en": "(no recent activity)"},
    "tray.pending_files": {"it": "{count} file in coda", "en": "{count} files queued"},
    "tray.open_folder": {"it": "Apri cartella NASBox", "en": "Open NASBox folder"},
    "tray.recent_files_menu": {"it": "File recenti", "en": "Recent files"},
    "tray.resume": {"it": "Riprendi", "en": "Resume"},
    "tray.pause": {"it": "Pausa", "en": "Pause"},
    "tray.pause_for": {"it": "Pausa per…", "en": "Pause for…"},
    "tray.show_window": {"it": "Apri finestra principale", "en": "Open main window"},
    "tray.quit": {"it": "Esci", "en": "Quit"},
    "tray.pending_suffix": {"it": " — {count} in coda", "en": " — {count} queued"},
    "tray.conflict_title": {"it": "Conflitto di sincronizzazione", "en": "Sync conflict"},
    "tray.conflict_body": {
        "it": "Sono state mantenute entrambe le versioni di '{path}'. {detail}",
        "en": "Both versions of '{path}' were kept. {detail}",
    },
    "tray.not_configured": {"it": "⚠️ Cartella NASBox non configurata", "en": "⚠️ NASBox folder not set up"},
    "tray.nas_unreachable": {"it": "⚠️ NAS non raggiungibile", "en": "⚠️ NAS unreachable"},
    "tray.paused": {"it": "⏸ In pausa", "en": "⏸ Paused"},
    "tray.active": {"it": "✅ Attivo — {where}", "en": "✅ Active — {where}"},
    "tray.via_bastion": {"it": "(via bastione)", "en": "(via bastion)"},

    # --- main_window.py ---
    "main_window.brand_tagline": {
        "it": "Sincronizzazione privata, sempre sotto controllo",
        "en": "Private file sync, always in control",
    },
    "main_window.tab_status": {"it": "Stato", "en": "Status"},
    "main_window.tab_transfers": {"it": "Trasferimenti", "en": "Transfers"},
    "main_window.tab_history": {"it": "Storico / Cestino", "en": "History / Trash"},
    "main_window.tab_browse": {"it": "Sfoglia NAS", "en": "Browse NAS"},
    "main_window.tab_log": {"it": "Log", "en": "Log"},
    "main_window.tab_settings": {"it": "Impostazioni", "en": "Settings"},
    "main_window.tab_status_tooltip": {"it": "Stato attuale, pausa, cartella NASBox.", "en": "Current status, pause, NASBox folder."},
    "main_window.tab_transfers_tooltip": {"it": "File in coda di caricamento/scaricamento in questo momento.", "en": "Files currently queued to upload/download."},
    "main_window.tab_history_tooltip": {"it": "Cestino e versioni storiche dei file, con ripristino.", "en": "Trash and historical file versions, with restore."},
    "main_window.tab_browse_tooltip": {
        "it": "Naviga la cartella sul NAS cosi' com'e' adesso, scarica, rinomina o elimina file e cartelle.",
        "en": "Browse the NAS folder as it is right now, download, rename, or delete files and folders.",
    },
    "main_window.tab_log_tooltip": {"it": "Cronologia di ogni upload, download, cancellazione ed errore.", "en": "History of every upload, download, deletion, and error."},
    "main_window.tab_settings_tooltip": {"it": "Connessione al NAS, banda, esclusioni, lingua.", "en": "NAS connection, bandwidth, exclusions, language."},
    "main_window.server_outdated_suffix": {"it": "aggiornamento NAS richiesto", "en": "NAS update required"},
    "main_window.server_update_available_title": {"it": "Aggiornamento server NAS", "en": "NAS server update"},
    "main_window.server_restarted_suffix": {"it": "demone NAS", "en": "NAS daemon"},
    "main_window.tray_running_notice": {
        "it": "L'app continua a sincronizzare in background. Usa l'icona nel tray per riaprirla o uscire.",
        "en": "The app keeps syncing in the background. Use the tray icon to reopen it or quit.",
    },
    "main_window.shutdown_title": {"it": "Chiusura in corso", "en": "Closing"},
    "main_window.shutdown_message": {
        "it": "Arresto della sincronizzazione in corso. Attendi un momento…",
        "en": "Stopping synchronization. Please wait…",
    },
    "main_window.shutdown_waiting": {
        "it": "Arresto della sincronizzazione in corso da {seconds} secondi. Attendi…",
        "en": "Stopping synchronization for {seconds} seconds. Please wait…",
    },
    "main_window.startup_error_title": {"it": "avvio non riuscito", "en": "startup failed"},

    # --- browse_tab.py ---
    "browse.note": {
        "it": "Questa è la cartella sul NAS così com'è adesso, non necessariamente ciò che hai "
              "sincronizzato su questo PC. Rinominare ed eliminare passano dal cestino del NAS "
              "(recuperabili dal tab Storico), mai una cancellazione diretta.",
        "en": "This is the NAS folder as it is right now, not necessarily what you've synced "
              "on this PC. Rename and delete both go through the NAS trash (recoverable from "
              "the History tab), never a direct deletion.",
    },
    "browse.up_btn": {"it": "↑ Su", "en": "↑ Up"},
    "browse.refresh_btn": {"it": "Aggiorna", "en": "Refresh"},
    "browse.path_label": {"it": "Percorso: {path}", "en": "Path: {path}"},
    "browse.col_name": {"it": "Nome", "en": "Name"},
    "browse.col_size": {"it": "Dimensione", "en": "Size"},
    "browse.col_modified": {"it": "Modificato", "en": "Modified"},
    "browse.loading": {"it": "Caricamento…", "en": "Loading…"},
    "browse.entry_count": {"it": "{count} elementi", "en": "{count} items"},
    "browse.list_failed": {"it": "Impossibile leggere questa cartella sul NAS.", "en": "Couldn't read this folder on the NAS."},
    "browse.nas_unreachable": {"it": "NAS non raggiungibile al momento.", "en": "NAS not reachable right now."},
    "browse.nas_unreachable_title": {"it": "NAS non raggiungibile", "en": "NAS unreachable"},
    "browse.download_btn": {"it": "Scarica", "en": "Download"},
    "browse.download_tooltip": {"it": "Scarica il file selezionato senza toccare la sincronizzazione.", "en": "Download the selected file without touching sync state."},
    "browse.download_pick_file": {"it": "Seleziona almeno un file (non una cartella) da scaricare.", "en": "Select at least one file (not a folder) to download."},
    "browse.downloading": {"it": "Scaricamento…", "en": "Downloading…"},
    "browse.download_failed_title": {"it": "Scaricamento fallito", "en": "Download failed"},
    "browse.download_done_title": {"it": "Scaricamento completato", "en": "Download complete"},
    "browse.download_one_done_body": {"it": "Salvato in: {path}", "en": "Saved to: {path}"},
    "browse.download_done_body": {"it": "{count} file scaricati.", "en": "{count} files downloaded."},
    "browse.download_partial_body": {"it": "Scaricamento fallito per: {names}", "en": "Download failed for: {names}"},
    "browse.rename_btn": {"it": "Rinomina", "en": "Rename"},
    "browse.rename_pick_one": {"it": "Seleziona un solo elemento da rinominare.", "en": "Select exactly one item to rename."},
    "browse.rename_prompt": {"it": "Nuovo nome per \"{name}\":", "en": "New name for \"{name}\":"},
    "browse.rename_invalid_name": {"it": "Il nome non può contenere \"/\".", "en": "The name can't contain \"/\"."},
    "browse.renaming": {"it": "Rinomina in corso…", "en": "Renaming…"},
    "browse.rename_failed_title": {"it": "Rinomina fallita", "en": "Rename failed"},
    "browse.delete_btn": {"it": "Elimina", "en": "Delete"},
    "browse.delete_tooltip": {
        "it": "Sposta nel cestino del NAS (stessa retention della sincronizzazione), mai una cancellazione diretta.",
        "en": "Moves into the NAS trash (same retention as sync), never a direct deletion.",
    },
    "browse.confirm_delete_title": {"it": "Confermi l'eliminazione?", "en": "Confirm deletion?"},
    "browse.confirm_delete_body": {
        "it": "{names}\n\nVerrà spostato nel cestino del NAS, recuperabile dal tab Storico per "
              "{days} giorni. Procedere?",
        "en": "{names}\n\nThis will move into the NAS trash, recoverable from the History tab "
              "for {days} days. Proceed?",
    },
    "browse.deleting": {"it": "Eliminazione in corso…", "en": "Deleting…"},
    "browse.delete_failed_title": {"it": "Eliminazione fallita", "en": "Deletion failed"},
    "browse.delete_partial_body": {"it": "Eliminazione fallita per: {names}", "en": "Deletion failed for: {names}"},
}


def detect_system_language() -> str:
    try:
        lang = locale.getdefaultlocale()[0] or ""
    except (ValueError, TypeError):
        lang = ""
    return "it" if lang.lower().startswith("it") else "en"


def set_language(code: str) -> None:
    global _current_lang
    _current_lang = code if code in SUPPORTED else DEFAULT_LANG


def current_language() -> str:
    return _current_lang


def t(key: str, **kwargs: Any) -> str:
    entry = STRINGS.get(key)
    if entry is None:
        return key  # a missing key shows up as itself in the UI, loudly, instead of crashing
    text = entry.get(_current_lang) or entry.get(DEFAULT_LANG) or key
    if kwargs:
        try:
            return text.format(**kwargs)
        except (KeyError, IndexError):
            return text
    return text
