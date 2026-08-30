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

    # --- dialogs.py: FirstRunSetupWizard ---
    "setup.title": {"it": "Configura NASBox su questo PC", "en": "Set up NASBox on this PC"},
    "setup.local_title": {"it": "Cartella su questo PC", "en": "Folder on this PC"},
    "setup.local_intro": {
        "it": "Scegli la cartella locale che NASBox sincronizzerà. Verrà creata solo quando completi la configurazione.",
        "en": "Choose the local folder NASBox will sync. It is created only after you finish setup.",
    },
    "setup.local_failed_title": {"it": "Cartella locale non valida", "en": "Invalid local folder"},
    "setup.connection_title": {"it": "Connessione SSH al NAS", "en": "SSH connection to the NAS"},
    "setup.connection_intro": {
        "it": "Inserisci i dati SSH del NAS. Sul NAS non serve alcuna interfaccia grafica: basta l'accesso shell SSH.",
        "en": "Enter the NAS SSH details. The NAS needs no graphical interface, only SSH shell access.",
    },
    "setup.test_connection": {"it": "Prova connessione SSH", "en": "Test SSH connection"},
    "setup.testing": {"it": "Connessione in corso...", "en": "Connecting..."},
    "setup.test_required": {"it": "Prova la connessione per continuare.", "en": "Test the connection to continue."},
    "setup.connection_missing": {"it": "Inserisci host e utente SSH.", "en": "Enter the SSH host and user."},
    "setup.test_ok": {"it": "Connessione SSH riuscita a {host}.", "en": "SSH connection to {host} succeeded."},
    "setup.test_failed": {"it": "Connessione SSH non riuscita: {detail}", "en": "SSH connection failed: {detail}"},
    "setup.advanced_later": {
        "it": "Accesso WAN e bastione sono opzionali: puoi configurarli in Impostazioni dopo questa procedura.",
        "en": "WAN access and a bastion are optional; configure them later in Settings.",
    },
    "setup.server_title": {"it": "Cartella NASBox sul NAS", "en": "NASBox folder on the NAS"},
    "setup.server_intro": {
        "it": "Rileva il demone dalla shell SSH per compilare percorso dello script e cartella remota. Se non è in esecuzione, puoi inserire i percorsi e completare comunque.",
        "en": "Detect the daemon through the SSH shell to fill the script path and remote folder. If it is not running, enter the paths and finish anyway.",
    },
    "setup.detect_optional": {
        "it": "Rilevamento consigliato ma non obbligatorio.",
        "en": "Detection is recommended but optional.",
    },
    "setup.detect_ok": {
        "it": "Configurazione server rilevata dal NAS.",
        "en": "Server configuration detected from the NAS.",
    },
    "setup.detect_failed": {"it": "Rilevamento non riuscito: {detail}", "en": "Detection failed: {detail}"},

    # --- status_tab.py ---
    "status.group_title": {"it": "Stato", "en": "Status"},
    "status.starting": {"it": "In avvio…", "en": "Starting…"},
    "status.sync_state_starting": {"it": "Sincronizzazione: stato in verifica.", "en": "Synchronization: checking status."},
    "status.sync_state_not_configured": {"it": "Sincronizzazione non disponibile: completa la configurazione.", "en": "Synchronization unavailable: complete setup."},
    "status.sync_state_offline": {"it": "Sincronizzazione non disponibile finché il NAS è offline.", "en": "Synchronization unavailable while the NAS is offline."},
    "status.sync_state_paused_timed": {"it": "Sincronizzazione in pausa temporanea.", "en": "Synchronization is temporarily paused."},
    "status.sync_state_paused": {"it": "Sincronizzazione in pausa.", "en": "Synchronization is paused."},
    "status.sync_state_ready": {"it": "Sincronizzazione pronta: monitoraggio attivo, nessun trasferimento in corso.", "en": "Synchronization ready: monitoring is active, no transfer in progress."},
    "status.sync_state_pending": {"it": "Sincronizzazione pronta: {count} in coda.", "en": "Synchronization ready: {count} queued."},
    "status.sync_state_preparing": {"it": "Preparazione {direction} in corso…", "en": "Preparing {direction}…"},
    "status.sync_state_waiting": {"it": "{direction}: in attesa del lock di sincronizzazione sul NAS.", "en": "{direction}: waiting for the NAS synchronization lock."},
    "status.sync_state_active": {"it": "{direction} in corso.", "en": "{direction} in progress."},
    "status.sync_state_file": {"it": "{direction} in corso: {path}", "en": "{direction} in progress: {path}"},
    "status.remote_lock_free": {"it": "Lock NAS: libero.", "en": "NAS lock: free."},
    "status.remote_lock_held": {
        "it": "Lock NAS: occupato da circa {age}; proprietario {owner}.",
        "en": "NAS lock: held for about {age}; owner {owner}.",
    },
    "status.pending_empty": {"it": "Coda persistente: vuota.", "en": "Persistent queue: empty."},
    "status.pending_summary": {
        "it": "Coda persistente: {count} percorsi, più vecchio da {age}; ultimo rinvio: {reason}.",
        "en": "Persistent queue: {count} paths, oldest for {age}; last defer: {reason}.",
    },
    "status.watcher_starting": {"it": "Controllo modifiche: avvio in corso…", "en": "Change detection: starting…"},
    "status.watcher_inotify": {"it": "Controllo modifiche: inotify attivo.", "en": "Change detection: inotify active."},
    "status.watcher_polling": {
        "it": "Controllo modifiche: scansione periodica ogni 2 secondi; le prestazioni possono ridursi.",
        "en": "Change detection: periodic scan every 2 seconds; performance may be reduced.",
    },
    "status.watcher_polling_detail": {
        "it": "Controllo modifiche: scansione periodica; {detail}",
        "en": "Change detection: periodic scan; {detail}",
    },
    "status.watcher_disabled": {"it": "Controllo modifiche: non attivo.", "en": "Change detection: inactive."},
    "status.sync_deferred_lock": {
        "it": "{direction} rimandato: un altro client sta usando il lock NAS; nuovo tentativo automatico.",
        "en": "{direction} deferred: another client is using the NAS lock; retrying automatically.",
    },
    "status.sync_lock_error": {
        "it": "Sincronizzazione non avviata: il lock NAS ha restituito un errore. {detail}",
        "en": "Synchronization did not start: the NAS lock returned an error. {detail}",
    },
    "status.direction_upload": {"it": "caricamento verso il NAS", "en": "upload to NAS"},
    "status.direction_download": {"it": "scaricamento dal NAS", "en": "download from NAS"},
    "status.last_sync_never": {"it": "Ultima sincronizzazione riuscita: nessun trasferimento registrato.", "en": "Last successful synchronization: no transfer recorded."},
    "status.last_sync": {"it": "Ultima sincronizzazione riuscita: {time} ({direction}).", "en": "Last successful synchronization: {time} ({direction})."},
    "status.last_problem_none": {"it": "Ultimo errore o blocco di sicurezza: nessuno registrato.", "en": "Last error or safety block: none recorded."},
    "status.last_problem": {"it": "Ultimo errore o blocco: {time} [{action}] {detail}", "en": "Last error or block: {time} [{action}] {detail}"},
    "status.problem_category_connection": {"it": "connessione SSH", "en": "SSH connection"},
    "status.problem_category_transfer": {"it": "trasferimento rsync", "en": "rsync transfer"},
    "status.problem_category_safety_block": {"it": "blocco di sicurezza", "en": "safety block"},
    "status.problem_category_journal_block": {"it": "journal", "en": "journal"},
    "status.problem_category_generic": {"it": "errore {action}", "en": "{action} error"},
    "status.problem_no_detail": {"it": "nessun dettaglio", "en": "no details"},
    "status.hash_progress": {
        "it": "Verifica file locali in corso: {done} di {total}…",
        "en": "Checking local files: {done} of {total}…",
    },
    "status.sync_now_btn": {"it": "Sincronizza tutto ora", "en": "Sync everything now"},
    "status.sync_requested": {"it": "Richiesta ricevuta: controllo delle modifiche in corso.", "en": "Request received: checking for changes."},
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
    "status.connected_msg": {"it": "Connesso al NAS: {where}", "en": "Connected to NAS: {where}"},
    "status.version_tooltip": {"it": "Clicca per cercare aggiornamenti del client.", "en": "Click to check for client updates."},
    "status.update_available": {"it": "🔄 Aggiornamento {version} disponibile — clicca per installare e riavviare.", "en": "🔄 Update {version} available — click to install and restart."},
    "status.update_checking": {"it": "Controllo aggiornamenti in corso…", "en": "Checking for updates…"},
    "status.update_check_failed": {"it": "Controllo aggiornamenti non riuscito: {detail}", "en": "Update check failed: {detail}"},
    "status.update_confirm_title": {"it": "Aggiornamento disponibile", "en": "Update available"},
    "status.update_confirm_body": {"it": "Installare la versione {version} e riavviare NASBox?", "en": "Install version {version} and restart NASBox?"},
    "status.update_restart_title": {"it": "Aggiornamento completato", "en": "Update complete"},
    "status.update_restart_body": {"it": "NASBox verrà riavviato con la nuova versione.", "en": "NASBox will restart with the new version."},
    "status.update_failed_title": {"it": "Aggiornamento fallito", "en": "Update failed"},
    "status.queue_unknown": {"it": "Prima verifica in corso: sto analizzando i file…", "en": "First check in progress: analyzing files…"},
    "status.queue_empty": {"it": "Coda corrente: nessuna operazione in attesa nell'ultima verifica.", "en": "Current queue: no operations waiting in the latest check."},
    "status.queue_offline": {"it": "Coda non disponibile: il NAS non è connesso.", "en": "Queue unavailable: the NAS is not connected."},
    "status.queue_summary": {
        "it": "In attesa: {uploads} caricamenti, {downloads} scaricamenti, {deletes} eliminazioni.",
        "en": "Waiting: {uploads} uploads, {downloads} downloads, {deletes} deletions.",
    },
    "status.diagnostics_unknown": {"it": "Diagnostica NAS non ancora caricata.", "en": "NAS diagnostics have not been loaded yet."},
    "status.diagnostics_refresh_btn": {"it": "Diagnostica NAS", "en": "NAS diagnostics"},
    "status.diagnostics_loading": {"it": "Lettura diagnostica NAS…", "en": "Reading NAS diagnostics…"},
    "status.diagnostics_offline": {"it": "Diagnostica NAS non disponibile: NAS non raggiungibile.", "en": "NAS diagnostics unavailable: NAS is unreachable."},
    "status.diagnostics_failed": {"it": "Diagnostica NAS non disponibile: {detail}", "en": "NAS diagnostics unavailable: {detail}"},
    "status.diagnostics_invalid": {"it": "risposta NAS non valida", "en": "invalid NAS response"},
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
    "status.attention_title": {"it": "Richiede attenzione", "en": "Requires attention"},
    "status.attention_action_settings": {"it": "Apri Impostazioni", "en": "Open Settings"},
    "status.attention_action_log": {"it": "Apri Log", "en": "Open Log"},
    "status.attention_action_history": {"it": "Apri Storico", "en": "Open History"},
    "status.attention_action_transfers": {"it": "Apri Trasferimenti", "en": "Open Transfers"},
    "status.config_local_folder": {"it": "cartella locale", "en": "local folder"},
    "status.config_nas_host": {"it": "host NAS", "en": "NAS host"},
    "status.config_nas_user": {"it": "utente SSH", "en": "SSH user"},
    "status.problem_config": {"it": "Configurazione incompleta: {fields}.", "en": "Setup is incomplete: {fields}."},
    "status.problem_unreachable": {"it": "Il NAS configurato non è raggiungibile. Controlla rete e parametri di connessione.", "en": "The configured NAS is unreachable. Check the network and connection settings."},
    "status.problem_host_keys": {"it": "Le chiavi host SSH configurate non sono state verificate esplicitamente.", "en": "The configured SSH host keys have not been explicitly verified."},
    "status.problem_repository": {"it": "Il repository NASBox remoto non è verificato. Rileva la configurazione dal NAS prima di sincronizzare.", "en": "The remote NASBox repository is not verified. Detect its configuration before synchronizing."},
    "status.problem_journal_unavailable": {"it": "Il journal del repository NAS non risulta verificato o disponibile.", "en": "The NAS repository journal is not verified or available."},
    "status.problem_journal": {"it": "Errore journal: {detail}", "en": "Journal error: {detail}"},
    "status.problem_recent_event": {"it": "Evento recente {action}: {detail}", "en": "Recent {action} event: {detail}"},
    "status.problem_low_space": {"it": "Spazio disponibile sul NAS basso: {free}. Controlla lo storico e libera spazio.", "en": "Low available NAS space: {free}. Review history and free space."},

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
    "settings.save_all_btn": {"it": "Salva impostazioni", "en": "Save settings"},
    "settings.save_all_tooltip": {
        "it": "Salva insieme connessione, banda, frequenza, feedback, tray, lingua, esclusioni e cancellazioni.",
        "en": "Saves connection, bandwidth, cadence, feedback, tray, language, exclusions, and deletion settings together.",
    },
    "settings.dirty_feedback": {"it": "Modifiche non ancora salvate.", "en": "Unsaved changes."},
    "settings.saved_feedback": {"it": "Tutte le impostazioni sono salvate.", "en": "All settings are saved."},

    "settings.bandwidth_title": {"it": "Limite di banda (KB/s, 0 = illimitato)", "en": "Bandwidth limit (KB/s, 0 = unlimited)"},
    "settings.bandwidth_tooltip": {"it": "0 = nessun limite.", "en": "0 = no limit."},
    "settings.upload_label": {"it": "↑ Carica:", "en": "↑ Upload:"},
    "settings.download_label": {"it": "↓ Scarica:", "en": "↓ Download:"},

    "settings.cadence_title": {"it": "Frequenza sincronizzazione", "en": "Sync frequency"},
    "settings.poll_label": {"it": "Controlla il NAS per novità ogni:", "en": "Check the NAS for updates every:"},
    "settings.cadence_note": {
        "it": "Le modifiche locali vengono inviate al NAS quasi subito (appena rilevate). "
              "Questo intervallo riguarda solo lo scaricare le modifiche fatte da altri PC.",
        "en": "Local changes are sent to the NAS almost immediately (as soon as detected). "
              "This interval only affects downloading changes made by other PCs.",
    },
    "settings.metrics_refresh_label": {"it": "Aggiorna monitor NAS ogni:", "en": "Refresh NAS monitor every:"},
    "settings.metrics_refresh_note": {
        "it": "Intervallo della dashboard read-only. Non esegue scansioni dei file.",
        "en": "Read-only dashboard interval. It does not scan files.",
    },

    "settings.feedback_title": {"it": "Feedback sincronizzazione", "en": "Sync feedback"},
    "settings.notify_sync_checkbox": {
        "it": "Mostra un popup quando una sincronizzazione completa delle modifiche",
        "en": "Show a popup when a sync completes changes",
    },
    "settings.notify_sync_tooltip": {
        "it": "Mostra una notifica nell'area di sistema dopo un trasferimento riuscito. L'animazione dell'icona resta attiva durante il trasferimento.",
        "en": "Shows a system notification after a successful transfer. The tray icon animation remains active during transfers.",
    },
    "settings.notify_sync_note": {
        "it": "Le preferenze di feedback e tray vengono applicate con 'Salva impostazioni'.",
        "en": "Feedback and tray preferences are applied with 'Save settings'.",
    },
    "settings.animate_sync_checkbox": {
        "it": "Anima l'icona nel tray durante i trasferimenti",
        "en": "Animate the tray icon during transfers",
    },
    "settings.animate_sync_tooltip": {
        "it": "Alterna il simbolo dell'icona mentre NASBox prepara o trasferisce file.",
        "en": "Alternates the tray icon symbol while NASBox prepares or transfers files.",
    },
    "settings.tray_click_label": {"it": "Un clic sull'icona:", "en": "Single-click tray icon:"},
    "settings.tray_click_menu": {"it": "Mostra il menu", "en": "Show menu"},
    "settings.tray_click_window": {"it": "Apri la finestra", "en": "Open window"},
    "settings.tray_click_tooltip": {
        "it": "Il clic destro mostra sempre il menu; il doppio clic apre sempre la finestra.",
        "en": "Right-click always shows the menu; double-click always opens the window.",
    },

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
    "settings.retention_never": {"it": "mai (nessuna eliminazione automatica)", "en": "never (no automatic deletion)"},
    "settings.retention_days": {"it": "{days} giorni", "en": "{days} days"},
    "settings.detect_done_body": {
        "it": "{status_line}.\nCartella NASBox sul NAS: {share_root}\nRetention sul NAS: {retention}{version_line}",
        "en": "{status_line}.\nNASBox folder on the NAS: {share_root}\nNAS retention: {retention}{version_line}",
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
              "\"(conflitto da ...)\" per non perdere dati. Puoi rivederli qui e spostare nello "
              "storico locale le copie che non servono.",
        "en": "When two versions of a file are modified at the same time on different PCs, "
              "NASBox keeps both: one stays in place, the other is renamed with "
              "\"(conflitto da ...)\" to avoid data loss. Review them here and move unneeded "
              "copies into local history.",
    },
    "settings.maint_conflict_count_unknown": {"it": "Clicca \"Cerca\" per trovare i file di conflitto.", "en": "Click \"Scan\" to find conflict files."},
    "settings.maint_conflict_scanning": {"it": "Scansione in corso…", "en": "Scanning…"},
    "settings.maint_conflict_error": {"it": "Errore nella scansione: {detail}", "en": "Scan error: {detail}"},
    "settings.maint_conflict_count_none": {"it": "Nessun file di conflitto trovato nella cartella NASBox.", "en": "No conflict files found in the NASBox folder."},
    "settings.maint_conflict_count": {"it": "Trovati {count} file di conflitto.", "en": "Found {count} conflict files."},
    "settings.maint_conflict_no_folder": {"it": "Cartella NASBox non configurata.", "en": "NASBox folder not set up."},
    "settings.maint_scan_btn": {"it": "Cerca conflitti", "en": "Scan for conflicts"},
    "settings.maint_scan_tooltip": {"it": "Cerca nella cartella NASBox tutti i file con \"(conflitto da ...)\" nel nome.", "en": "Scans the NASBox folder for all files with \"(conflitto da ...)\" in their name."},
    "settings.maint_review_btn": {"it": "Rivedi i conflitti…", "en": "Review conflicts…"},
    "settings.maint_delete_tooltip": {
        "it": "Mostra tutti i conflitti e consente di spostare quelli selezionati nello storico locale.",
        "en": "Shows every conflict and lets you move selected files into local history.",
    },
    "settings.maint_review_title": {"it": "Rivedi i file di conflitto", "en": "Review conflict files"},
    "settings.maint_review_note": {
        "it": "Seleziona i file da rimuovere dalla cartella NASBox. Verranno spostati nello storico locale di questo PC e resteranno ripristinabili secondo la retention locale.",
        "en": "Select files to remove from the NASBox folder. They will be moved into this PC's local history and remain restorable under local retention.",
    },
    "settings.maint_open_folder_btn": {"it": "Apri cartella", "en": "Open folder"},
    "settings.maint_move_selected_btn": {"it": "Sposta selezionati nello storico", "en": "Move selected to history"},
    "settings.maint_delete_done_title": {"it": "Conflitti spostati", "en": "Conflicts moved"},
    "settings.maint_delete_done_body": {"it": "Spostati {count} file di conflitto nello storico locale.", "en": "Moved {count} conflict files into local history."},
    "settings.maint_delete_partial_title": {"it": "Spostamento parziale", "en": "Partial move"},
    "settings.maint_delete_partial_body": {"it": "Spostati {moved} file, {failed} non spostabili:\n{names}", "en": "Moved {moved} files, {failed} could not be moved:\n{names}"},

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
    "transfers.phase_activity": {"it": "{direction}: {phase} da {seconds}s", "en": "{direction}: {phase} for {seconds}s"},
    "transfers.phase_checking": {"it": "verifica stato NAS", "en": "checking NAS state"},
    "transfers.phase_transferring": {"it": "trasferimento batch", "en": "transferring batch"},
    "transfers.phase_confirming": {"it": "conferma risultati", "en": "confirming results"},
    "transfers.phase_committing": {"it": "commit manifest e journal", "en": "committing manifest and journal"},
    "transfers.preparing": {"it": "{direction} in preparazione: sto analizzando i file e costruendo la lista ({seconds}s trascorsi).", "en": "{direction} preparing: analyzing files and building the list ({seconds}s elapsed)."},
    "transfers.queue_scan_running": {"it": "Sto verificando le differenze con il NAS ({seconds}s trascorsi)…", "en": "Checking for differences with the NAS ({seconds}s elapsed)…"},
    "transfers.lock_unavailable": {"it": "Sincronizzazione rimandata. {detail}", "en": "Synchronization deferred. {detail}"},
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
    "log.filter_item": {"it": "{category}: {action}", "en": "{category}: {action}"},
    "log.category.transfer": {"it": "Sincronizzazione", "en": "Sync"},
    "log.category.browse": {"it": "Sfoglia NAS", "en": "Browse NAS"},
    "log.category.history": {"it": "Storico", "en": "History"},
    "log.category.safety": {"it": "Sicurezza", "en": "Safety"},
    "log.category.system": {"it": "Sistema", "en": "System"},
    "log.raw_action_tooltip": {"it": "Codice azione: {action}", "en": "Action code: {action}"},
    "log.open_containing_folder": {"it": "Apri cartella contenente", "en": "Open containing folder"},
    "log.action.upload": {"it": "Caricato sul NAS", "en": "Uploaded to NAS"},
    "log.action.download": {"it": "Scaricato dal NAS", "en": "Downloaded from NAS"},
    "log.action.delete_local": {"it": "Eliminato in locale", "en": "Deleted locally"},
    "log.action.delete_remote": {"it": "Eliminato sul NAS", "en": "Deleted on NAS"},
    "log.action.browse_download": {"it": "Scaricato da Sfoglia NAS", "en": "Downloaded from Browse NAS"},
    "log.action.browse_rename": {"it": "Rinominato da Sfoglia NAS", "en": "Renamed in Browse NAS"},
    "log.action.browse_delete": {"it": "Eliminato da Sfoglia NAS", "en": "Deleted in Browse NAS"},
    "log.action.restore_remote_version": {"it": "Versione NAS ripristinata", "en": "NAS version restored"},
    "log.action.prune_local_trash": {"it": "Storico locale ripulito", "en": "Local history cleaned"},
    "log.action.prune_remote_trigger": {"it": "Pulizia storico NAS richiesta", "en": "NAS history cleanup requested"},
    "log.action.conflict": {"it": "Conflitto conservato", "en": "Conflict preserved"},
    "log.action.stale_delete": {"it": "Eliminazione obsoleta ignorata", "en": "Stale deletion ignored"},
    "log.action.safety_block": {"it": "Blocco di sicurezza", "en": "Safety block"},
    "log.action.journal_block": {"it": "Blocco del journal", "en": "Journal block"},
    "log.action.journal_error": {"it": "Errore del journal", "en": "Journal error"},
    "log.action.unsupported": {"it": "Elemento non supportato", "en": "Unsupported item"},
    "log.action.pull_deferred": {"it": "Scaricamento rimandato", "en": "Download deferred"},
    "log.action.lock_deferred": {"it": "Sincronizzazione rimandata", "en": "Synchronization deferred"},
    "log.action.cancelled": {"it": "Trasferimento annullato", "en": "Transfer cancelled"},
    "log.action.server_down": {"it": "Server NAS non attivo", "en": "NAS server stopped"},
    "log.action.server_restarted": {"it": "Server NAS riavviato", "en": "NAS server restarted"},
    "log.action.server_outdated": {"it": "Server NAS da aggiornare", "en": "NAS server outdated"},
    "log.action.server_update_available": {"it": "Aggiornamento server disponibile", "en": "Server update available"},
    "log.action.error": {"it": "Errore", "en": "Error"},

    "lock.busy_retry": {
        "it": "Un altro PC sta sincronizzando con il NAS. Nessun dato è stato perso: NASBox riproverà automaticamente.",
        "en": "Another PC is syncing with the NAS. No data was lost: NASBox will retry automatically.",
    },
    "lock.acquire_failed": {
        "it": "Il lock di sincronizzazione del NAS non è disponibile per un problema inatteso: {detail}",
        "en": "The NAS synchronization lock is unavailable because of an unexpected problem: {detail}",
    },

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
    "history.source_local_description": {"it": "Fonte: questo PC. Versioni locali protette prima di una sovrascrittura o rimosse dalla manutenzione conflitti.", "en": "Source: this PC. Local versions protected before overwrite or removed by conflict maintenance."},
    "history.source_remote_description": {"it": "Fonte: NAS. Versioni conservate dal server quando un file sul NAS viene sostituito o eliminato.", "en": "Source: NAS. Versions kept by the server when a NAS file is replaced or deleted."},
    "history.prune_local_btn": {"it": "Pulisci storico locale ora", "en": "Clean local history now"},
    "history.prune_local_tooltip": {"it": "Rimuove subito le versioni locali più vecchie della retention impostata.", "en": "Immediately removes local versions older than the retention set above."},
    "history.prune_local_scanning": {"it": "Analisi dello storico locale in corso: {count} elementi controllati...", "en": "Scanning local history: {count} items checked..."},
    "history.prune_local_deleting": {"it": "Pulizia dello storico locale: {current} di {total} versioni elaborate...", "en": "Cleaning local history: {current} of {total} versions processed..."},
    "history.prune_local_dirs": {"it": "Rimozione delle cartelle vuote dello storico locale...", "en": "Removing empty local history folders..."},
    "history.prune_remote_btn": {"it": "Chiedi al NAS di pulire ora", "en": "Ask the NAS to clean now"},
    "history.prune_remote_tooltip": {
        "it": "Chiede al demone sul NAS di pulire subito lo storico remoto secondo la retention configurata sul NAS.",
        "en": "Asks the NAS daemon to clean remote history now using the retention configured on the NAS.",
    },
    "history.prune_remote_running": {"it": "Pulizia in corso…", "en": "Cleaning…"},
    "history.search_label": {"it": "Cerca:", "en": "Search:"},
    "history.search_placeholder": {"it": "cartella o file…", "en": "folder or file…"},
    "history.scope_label": {"it": "Mostra:", "en": "Show:"},
    "history.scope_local": {"it": "Cestino di questo PC", "en": "This PC's trash"},
    "history.scope_remote": {"it": "Storico sul NAS", "en": "History on NAS"},
    "history.refresh_btn": {"it": "Aggiorna", "en": "Refresh"},
    "history.refresh_tooltip": {"it": "Ricarica lo storico dalla fonte selezionata.", "en": "Reloads history from the selected source."},
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
    "history.remote_restore_many_picker_title": {"it": "Scegli la cartella in cui salvare le versioni dal NAS", "en": "Choose where to save the NAS versions"},
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
    "history.remote_retention_never": {"it": "mai eliminare automaticamente", "en": "never auto-delete"},
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
    "history.prune_local_disabled_body": {"it": "La retention locale è 0: la pulizia automatica è disattivata e nessuna versione è stata rimossa.", "en": "Local retention is 0: automatic cleanup is disabled and no versions were removed."},
    "history.nas_unreachable_title": {"it": "NAS non raggiungibile", "en": "NAS unreachable"},
    "history.nas_unreachable_body": {"it": "Impossibile contattare il NAS in questo momento.", "en": "Couldn't reach the NAS right now."},
    "history.prune_remote_title": {"it": "Pulizia remota", "en": "Remote cleanup"},
    "history.prune_remote_body": {
        "it": "Il NAS ha eseguito la pulizia del proprio storico secondo la retention remota.",
        "en": "The NAS cleaned its history according to remote retention.",
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
    "tray.upload": {"it": "Caricamento", "en": "Upload"},
    "tray.download": {"it": "Scaricamento", "en": "Download"},
    "tray.sync_completed_title": {"it": "Sincronizzazione completata", "en": "Sync completed"},
    "tray.sync_completed_body": {
        "it": "{direction}: {count} operazioni completate.",
        "en": "{direction}: {count} operations completed.",
    },

    # --- mirrors.py / mirrors_tab.py (external folder mirrors) ---
    "mirrors.note": {
        "it": "Qui puoi elencare cartelle che stanno FUORI dalla cartella NASBox (es. la tua "
              "cartella di lavoro): NASBox ne tiene automaticamente una copia sincronizzata "
              "dentro di sé, e da lì la ritrovi sul NAS e su tutti gli altri PC collegati. "
              "⚠️ È un mirror a senso unico: la cartella sorgente è quella autorevole, le "
              "modifiche fatte altrove alla copia in NASBox vengono sovrascritte.",
        "en": "List folders that live OUTSIDE the NASBox folder (e.g. your work folder): "
              "NASBox automatically keeps a synced copy of them inside itself, so they show "
              "up on the NAS and on every other connected PC too. ⚠️ This is a one-way "
              "mirror: the source folder is authoritative, edits made elsewhere to the copy "
              "inside NASBox get overwritten.",
    },
    "mirrors.col_enabled": {"it": "Attiva", "en": "On"},
    "mirrors.col_source": {"it": "Cartella sorgente", "en": "Source folder"},
    "mirrors.col_dest": {"it": "Copia in NASBox", "en": "Copy into NASBox"},
    "mirrors.col_last_sync": {"it": "Ultima sincronizzazione", "en": "Last sync"},
    "mirrors.col_status": {"it": "Stato", "en": "Status"},
    "mirrors.col_enabled_tooltip": {
        "it": "Spunta per far sì che NASBox tenga aggiornata la copia. Togli la spunta per mettere in pausa il mirror senza rimuoverlo.",
        "en": "Check to keep the copy updated. Uncheck to pause the mirror without removing it.",
    },
    "mirrors.dest_tooltip": {
        "it": "Nome della sottocartella dentro NASBox in cui viene replicata la cartella sorgente.",
        "en": "Name of the subfolder inside NASBox where the source folder is replicated.",
    },
    "mirrors.add_btn": {"it": "Aggiungi cartella…", "en": "Add folder…"},
    "mirrors.add_tooltip": {
        "it": "Scegli una cartella esterna e un nome di destinazione dentro NASBox.",
        "en": "Pick an external folder and a destination name inside NASBox.",
    },
    "mirrors.remove_btn": {"it": "Rimuovi selezionata", "en": "Remove selected"},
    "mirrors.remove_tooltip": {
        "it": "Rimuove la cartella scelta dall'elenco. La copia già presente in NASBox non viene eliminata automaticamente.",
        "en": "Removes the selected folder from the list. The copy already inside NASBox is not deleted automatically.",
    },
    "mirrors.sync_now_btn": {"it": "Sincronizza ora", "en": "Sync now"},
    "mirrors.sync_now_tooltip": {
        "it": "Copia subito la cartella selezionata in NASBox, senza aspettare il prossimo controllo.",
        "en": "Immediately copies the selected folder into NASBox, without waiting for the next check.",
    },
    "mirrors.add_title": {"it": "Aggiungi cartella esterna", "en": "Add external folder"},
    "mirrors.choose_source": {"it": "Scegli la cartella sorgente", "en": "Choose the source folder"},
    "mirrors.dest_prompt": {
        "it": "Nome della cartella dentro NASBox in cui tenerne la copia:",
        "en": "Name of the folder inside NASBox to keep the copy in:",
    },
    "mirrors.error_title": {"it": "Cartella esterna", "en": "External folder"},
    "mirrors.no_nasbox_body": {
        "it": "Configura prima la cartella NASBox (tab Stato) per poter aggiungere cartelle esterne.",
        "en": "Set up the NASBox folder first (Status tab) before adding external folders.",
    },
    "mirrors.remove_confirm_title": {"it": "Rimuovi cartella esterna", "en": "Remove external folder"},
    "mirrors.remove_confirm_body": {
        "it": "Rimuovere '{source}' dall'elenco delle cartelle esterne?\n\nLa copia già in NASBox resta dov'è.",
        "en": "Remove '{source}' from the external folders list?\n\nThe copy already in NASBox stays where it is.",
    },
    "mirrors.state_idle": {"it": "Sincronizzato", "en": "Synced"},
    "mirrors.state_syncing": {"it": "Sincronizzazione…", "en": "Syncing…"},
    "mirrors.state_error": {"it": "Errore", "en": "Error"},
    "mirrors.state_disabled": {"it": "Disattivata", "en": "Disabled"},
    "mirrors.state_unconfigured": {"it": "NASBox non configurata", "en": "NASBox not set up"},
    "mirrors.last_sync_never": {"it": "mai", "en": "never"},
    "mirrors.err_source_required": {"it": "Indicare una cartella sorgente.", "en": "Pick a source folder."},
    "mirrors.err_dest_required": {
        "it": "Indicare il nome della cartella di destinazione dentro NASBox.",
        "en": "Pick a destination folder name inside NASBox.",
    },
    "mirrors.err_source_absolute": {
        "it": "La cartella sorgente deve essere un percorso assoluto.",
        "en": "The source folder must be an absolute path.",
    },
    "mirrors.err_source_missing": {"it": "Cartella sorgente non trovata: {source}", "en": "Source folder not found: {source}"},
    "mirrors.err_dest_invalid": {
        "it": "Il nome di destinazione deve essere un semplice nome di cartella dentro NASBox.",
        "en": "The destination name must be a simple folder name inside NASBox.",
    },
    "mirrors.err_dest_reserved": {
        "it": "Questo nome è riservato alle cartelle interne di NASBox.",
        "en": "This name is reserved for NASBox's internal folders.",
    },
    "mirrors.err_nasbox_not_configured": {
        "it": "Cartella NASBox non ancora configurata.",
        "en": "The NASBox folder has not been set up yet.",
    },
    "mirrors.err_source_inside_nasbox": {
        "it": "La cartella sorgente è già dentro la cartella NASBox (e quindi già sincronizzata).",
        "en": "The source folder is already inside the NASBox folder (and therefore already synced).",
    },
    "mirrors.err_dest_taken": {
        "it": "Un'altra cartella esterna usa già questo nome di destinazione.",
        "en": "Another external folder already uses this destination name.",
    },
    "mirrors.err_source_taken": {
        "it": "Questa cartella sorgente è già presente nell'elenco.",
        "en": "This source folder is already in the list.",
    },
    "mirrors.err_invalid_entry": {
        "it": "Sorgente o destinazione non valida.",
        "en": "Source or destination is invalid.",
    },

    # --- metrics_tab.py ---
    "metrics.status_unknown": {"it": "Metriche NAS non ancora caricate.", "en": "NAS metrics have not been loaded yet."},
    "metrics.status_loading": {"it": "Lettura carico NAS…", "en": "Reading NAS load…"},
    "metrics.status_offline": {"it": "Monitor non disponibile: NAS non raggiungibile.", "en": "Monitor unavailable: NAS is unreachable."},
    "metrics.status_unsupported": {
        "it": "Il server NAS non supporta il monitoraggio del carico. Aggiorna il pacchetto server.",
        "en": "The NAS server does not support load monitoring. Update the server package.",
    },
    "metrics.status_failed": {"it": "Monitor NAS non disponibile: {detail}", "en": "NAS monitor unavailable: {detail}"},
    "metrics.status_updated": {"it": "Aggiornato alle {time}.", "en": "Updated at {time}."},
    "metrics.sync_unknown_title": {"it": "Stato sync in attesa", "en": "Sync status pending"},
    "metrics.sync_unknown_detail": {"it": "In attesa del primo aggiornamento dal NAS.", "en": "Waiting for the first update from the NAS."},
    "metrics.sync_loading_title": {"it": "Verifica sincronizzazione", "en": "Checking synchronization"},
    "metrics.sync_loading_detail": {"it": "Lettura dello stato attuale del NAS…", "en": "Reading the current NAS status…"},
    "metrics.sync_offline_title": {"it": "NAS non raggiungibile", "en": "NAS unreachable"},
    "metrics.sync_offline_detail": {"it": "Impossibile verificare lo stato della sincronizzazione.", "en": "Unable to check synchronization status."},
    "metrics.sync_error_title": {"it": "Stato sync non disponibile", "en": "Sync status unavailable"},
    "metrics.sync_idle_title": {"it": "Nessuna sincronizzazione in corso", "en": "No synchronization in progress"},
    "metrics.sync_idle_detail": {"it": "NAS libero; nessuna operazione in coda.", "en": "NAS is free; no operation is queued."},
    "metrics.sync_busy_title": {"it": "Sincronizzazione in corso", "en": "Synchronization in progress"},
    "metrics.sync_busy_detail": {"it": "Fase: {phase}{progress}", "en": "Phase: {phase}{progress}"},
    "metrics.sync_progress": {"it": " · {done}/{total}", "en": " · {done}/{total}"},
    "metrics.sync_queue_title": {"it": "Sincronizzazione in coda", "en": "Synchronization queued"},
    "metrics.sync_queue_detail": {"it": "{count} operazioni attendono il lock NAS.", "en": "{count} operations are waiting for the NAS lock."},
    "metrics.sync_staging_title": {"it": "Pubblicazione in attesa", "en": "Publication pending"},
    "metrics.sync_staging_detail": {"it": "{count} batch staging attendono la pubblicazione.", "en": "{count} staging batches are waiting to be published."},
    "metrics.phase_checking": {"it": "controllo", "en": "checking"},
    "metrics.phase_transferring": {"it": "trasferimento", "en": "transferring"},
    "metrics.phase_confirming": {"it": "conferma", "en": "confirming"},
    "metrics.phase_unknown": {"it": "attività NAS", "en": "NAS activity"},
    "metrics.health_ok": {"it": "Carico nella norma.", "en": "Load is within normal range."},
    "metrics.health_warning": {"it": "Attenzione: {areas} sotto pressione.", "en": "Warning: {areas} under pressure."},
    "metrics.invalid_response": {"it": "risposta NAS non valida", "en": "invalid NAS response"},
    "metrics.refresh_btn": {"it": "Aggiorna", "en": "Refresh"},
    "metrics.load_title": {"it": "Carico", "en": "Load"},
    "metrics.load_label": {"it": "1 / 5 / 15 minuti", "en": "1 / 5 / 15 minutes"},
    "metrics.load_chart_title": {"it": "Carico NAS recente", "en": "Recent NAS load"},
    "metrics.cpu_title": {"it": "CPU", "en": "CPU"},
    "metrics.cpu_label": {"it": "Uso / attesa I/O", "en": "Use / I/O wait"},
    "metrics.cpu_value": {"it": "{usage}% / {iowait}%", "en": "{usage}% / {iowait}%"},
    "metrics.cpu_chart_title": {"it": "Uso CPU recente", "en": "Recent CPU use"},
    "metrics.first_sample": {"it": "calcolo in corso", "en": "calculating"},
    "metrics.memory_title": {"it": "Memoria", "en": "Memory"},
    "metrics.memory_label": {"it": "Usata / totale", "en": "Used / total"},
    "metrics.memory_value": {"it": "{used} / {total} ({percent}%)", "en": "{used} / {total} ({percent}%)"},
    "metrics.swap_title": {"it": "Swap", "en": "Swap"},
    "metrics.swap_label": {"it": "Usata / totale", "en": "Used / total"},
    "metrics.swap_value": {"it": "{used} / {total}", "en": "{used} / {total}"},
    "metrics.swap_disabled": {"it": "Non attiva", "en": "Disabled"},
    "metrics.disk_title": {"it": "Disco NASBox", "en": "NASBox disk"},
    "metrics.disk_label": {"it": "Liberi / totale", "en": "Free / total"},
    "metrics.disk_value": {"it": "{available} / {total} liberi ({percent}% usato)", "en": "{available} / {total} free ({percent}% used)"},
    "metrics.io_title": {"it": "I/O disco NAS", "en": "NAS disk I/O"},
    "metrics.io_read_label": {"it": "Lettura", "en": "Read"},
    "metrics.io_write_label": {"it": "Scrittura", "en": "Write"},
    "metrics.network_title": {"it": "Rete", "en": "Network"},
    "metrics.receive_label": {"it": "Ricevuti", "en": "Received"},
    "metrics.transmit_label": {"it": "Inviati", "en": "Sent"},
    "metrics.interfaces_label": {"it": "Interfacce", "en": "Interfaces"},
    "metrics.uptime_title": {"it": "Uptime NAS", "en": "NAS uptime"},
    "metrics.uptime_label": {"it": "Attivo da", "en": "Up for"},
    "metrics.not_available": {"it": "Non disponibile", "en": "Not available"},
    "metrics.nasbox_title": {"it": "Attivita NASBox", "en": "NASBox activity"},
    "metrics.activity_label": {"it": "Trasferimento", "en": "Transfer"},
    "metrics.activity_idle": {"it": "Nessun trasferimento", "en": "No transfer"},
    "metrics.activity_busy": {"it": "{owner}, fase {phase}{progress}", "en": "{owner}, phase {phase}{progress}"},
    "metrics.unknown_value": {"it": "sconosciuto", "en": "unknown"},
    "metrics.queue_label": {"it": "Coda NAS", "en": "NAS queue"},
    "metrics.staging_label": {"it": "Batch staging", "en": "Staging batches"},
    "metrics.metadata_label": {"it": "Journal / manifest", "en": "Journal / manifest"},
    "metrics.metadata_value": {"it": "{journal} / {manifest}", "en": "{journal} / {manifest}"},

    # --- main_window.py ---
    "main_window.brand_tagline": {
        "it": "Sincronizzazione privata, sempre sotto controllo",
        "en": "Private file sync, always in control",
    },
    "main_window.tab_status": {"it": "Stato", "en": "Status"},
    "main_window.tab_metrics": {"it": "Monitor NAS", "en": "NAS Monitor"},
    "main_window.tab_transfers": {"it": "Trasferimenti", "en": "Transfers"},
    "main_window.tab_history": {"it": "Storico / Cestino", "en": "History / Trash"},
    "main_window.tab_conflicts": {"it": "Conflitti", "en": "Conflicts"},
    "main_window.tab_browse": {"it": "Sfoglia NAS", "en": "Browse NAS"},
    "main_window.tab_log": {"it": "Log", "en": "Log"},
    "main_window.tab_mirrors": {"it": "Cartelle esterne", "en": "External folders"},
    "main_window.tab_settings": {"it": "Impostazioni", "en": "Settings"},
    "main_window.tab_status_tooltip": {"it": "Stato attuale, pausa, cartella NASBox.", "en": "Current status, pause, NASBox folder."},
    "main_window.tab_metrics_tooltip": {"it": "Carico, memoria, disco e rete del NAS.", "en": "NAS load, memory, disk, and network."},
    "main_window.tab_transfers_tooltip": {"it": "File in coda di caricamento/scaricamento in questo momento.", "en": "Files currently queued to upload/download."},
    "main_window.tab_history_tooltip": {"it": "Cestino e versioni storiche dei file, con ripristino.", "en": "Trash and historical file versions, with restore."},
    "main_window.tab_conflicts_tooltip": {"it": "Conflitti rilevati: scegli quale versione mantenere.", "en": "Detected conflicts: choose which version to keep."},
    "main_window.tab_browse_tooltip": {
        "it": "Naviga la cartella sul NAS cosi' com'e' adesso, scarica, rinomina o elimina file e cartelle.",
        "en": "Browse the NAS folder as it is right now, download, rename, or delete files and folders.",
    },
    "main_window.tab_log_tooltip": {"it": "Cronologia di ogni upload, download, cancellazione ed errore.", "en": "History of every upload, download, deletion, and error."},
    "main_window.tab_mirrors_tooltip": {
        "it": "Cartelle esterne replicate dentro NASBox: la sorgente locale resta dov'è, la copia viene sincronizzata e propagata agli altri PC.",
        "en": "External folders replicated inside NASBox: the local source stays where it is, the copy is kept in sync and propagated to the other PCs.",
    },
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

    # --- conflicts_tab.py ---
    "conflicts.notice": {
        "it": "Quando due versioni cambiano contemporaneamente NASBox conserva entrambe. "
              "Scegli qui quale versione deve restare al nome originale; quella scartata "
              "finira' nello storico locale e potra' essere ripristinata.",
        "en": "When two versions change at the same time NASBox keeps both. "
              "Choose which version should keep the original name; the discarded "
              "one goes to local history and can be restored.",
    },
    "conflicts.count_unknown": {"it": "Cerca i conflitti presenti nella cartella NASBox.", "en": "Scan the NASBox folder for conflicts."},
    "conflicts.refresh_btn": {"it": "Aggiorna", "en": "Refresh"},
    "conflicts.no_folder": {"it": "Cartella NASBox non configurata.", "en": "NASBox folder not set up."},
    "conflicts.scanning": {"it": "Scansione conflitti in corso…", "en": "Scanning for conflicts…"},
    "conflicts.scan_error": {"it": "Scansione non riuscita: {detail}", "en": "Scan failed: {detail}"},
    "conflicts.count": {"it": "{groups} gruppi, {files} versioni alternative.", "en": "{groups} groups, {files} alternative versions."},
    "conflicts.none": {"it": "Nessun conflitto da risolvere.", "en": "No conflicts to resolve."},
    "conflicts.list_title": {"it": "Conflitti da esaminare", "en": "Conflicts to review"},
    "conflicts.col_original": {"it": "Percorso originale", "en": "Original path"},
    "conflicts.col_alternatives": {"it": "Alternative", "en": "Alternatives"},
    "conflicts.col_status": {"it": "Stato originale", "en": "Original status"},
    "conflicts.keep_label": {"it": "Versione da mantenere:", "en": "Version to keep:"},
    "conflicts.open_btn": {"it": "Apri", "en": "Open"},
    "conflicts.keep_btn": {"it": "Mantieni selezionata", "en": "Keep selected"},
    "conflicts.original_present": {"it": "originale presente", "en": "original present"},
    "conflicts.original_missing": {"it": "originale mancante", "en": "original missing"},
    "conflicts.original_option": {"it": "Versione originale", "en": "Original version"},
    "conflicts.confirm_title": {"it": "Risolvi conflitto", "en": "Resolve conflict"},
    "conflicts.confirm_body": {
        "it": "Mantenere questa versione come file principale?\n\n{path}\n\n"
              "Le altre versioni verranno conservate nello storico locale.",
        "en": "Keep this version as the main file?\n\n{path}\n\n"
              "The other versions will be kept in local history.",
    },
    "conflicts.resolve_failed_title": {"it": "Conflitto non risolto", "en": "Conflict not resolved"},
    "conflicts.resolve_done_title": {"it": "Conflitto risolto", "en": "Conflict resolved"},
    "conflicts.resolve_done_body": {"it": "La versione scelta e' stata mantenuta; le altre sono nello storico locale.", "en": "The selected version was kept; the others are in local history."},
    "main_window.shutdown_waiting": {
        "it": "Arresto della sincronizzazione in corso da {seconds} secondi. Attendi…",
        "en": "Stopping synchronization for {seconds} seconds. Please wait…",
    },
    "main_window.startup_error_title": {"it": "avvio non riuscito", "en": "startup failed"},
    "main_window.version_tooltip": {"it": "Clicca per cercare aggiornamenti.", "en": "Click to check for updates."},
    "main_window.update_check_failed": {"it": "Controllo aggiornamenti fallito", "en": "Update check failed"},
    "main_window.update_check_title": {"it": "Aggiornamento client", "en": "Client update"},
    "main_window.update_none": {"it": "Nessun aggiornamento disponibile: questa è già la versione più recente.", "en": "No update available: this is already the latest version."},
    "main_window.update_available_title": {"it": "Aggiornamento disponibile", "en": "Update available"},
    "main_window.update_available_body": {"it": "La versione {version} è disponibile ({origin}).\n\nInstallarla e riavviare?", "en": "Version {version} is available ({origin}).\n\nInstall and restart?"},
    "main_window.update_restart_title": {"it": "Aggiornamento completato", "en": "Update complete"},
    "main_window.update_restart_body": {"it": "NASBox verrà riavviato con la nuova versione.", "en": "NASBox will restart with the new version."},
    "main_window.update_failed_title": {"it": "Aggiornamento fallito", "en": "Update failed"},

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
    "browse.search_label": {"it": "Cerca:", "en": "Search:"},
    "browse.search_placeholder": {"it": "nome nella cartella corrente…", "en": "name in current folder…"},
    "browse.filtered_count": {"it": "{shown} di {total} elementi", "en": "{shown} of {total} items"},
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
    "browse.retention_never": {"it": "senza eliminazione automatica", "en": "with no automatic deletion"},
    "browse.retention_days": {"it": "per {days} giorni", "en": "for {days} days"},
    "browse.confirm_delete_body": {
        "it": "{names}\n\nVerrà spostato nel cestino del NAS, recuperabile dal tab Storico "
              "{retention}. Procedere?",
        "en": "{names}\n\nThis will move into the NAS trash, recoverable from the History tab "
              "{retention}. Proceed?",
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
