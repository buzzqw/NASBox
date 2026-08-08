#!/usr/bin/env bash
# Installer/wizard for the sync-daemon-server package (persistent daemon, no
# cron). Bilingual (it/en) -- see msg() below.
#
# Deliberately does NOT copy files elsewhere: this whole folder is the
# package. Run it from wherever you extracted/copied it -- ideally a
# persistent data volume (e.g. /volume1/NASBox/sync-daemon-server/ on
# Synology, /share/.../sync-daemon-server/ on QNAP), since /etc and /root are
# not guaranteed to survive firmware updates on these boxes.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
MAIN="$SCRIPT_DIR/sync-daemon-server.sh"

RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[0;33m'; BLUE='\033[0;34m'; NC='\033[0m'
ok()  { printf "${GREEN}  OK${NC}  %s\n" "$*"; }
warn(){ printf "${YELLOW} WARN${NC} %s\n" "$*"; }
err() { printf "${RED}FAIL${NC} %s\n" "$*"; }
info(){ printf "${BLUE} INFO${NC} %s\n" "$*"; }

# --- language: guess from the environment, let the user override ---
case "${LC_ALL:-${LC_MESSAGES:-${LANG:-}}}" in
    it*|IT*) LANG_CODE="it" ;;
    *)       LANG_CODE="en" ;;
esac
read -r -p "Lingua / Language [it/en] ($LANG_CODE): " lang_choice
case "$lang_choice" in
    it|IT) LANG_CODE="it" ;;
    en|EN) LANG_CODE="en" ;;
esac

msg() {
    local key="$1"
    if [[ "$LANG_CODE" == "it" ]]; then
        case "$key" in
            banner) echo "=== Installer server NASBox ===" ;;
            package_folder) echo "Cartella pacchetto: %s" ;;
            platform_detected) echo "Piattaforma rilevata: %s" ;;
            platform_linux_generic) echo "Linux generico" ;;
            conf_created) echo "Creato server.conf da server.conf.example" ;;
            conf_exists) echo "server.conf già presente, lo aggiorno con i valori di questa configurazione guidata." ;;
            share_root_prompt) echo "Cartella NASBox su questo NAS (SHARE_ROOT) [%s]: " ;;
            dir_exists) echo "Cartella già esistente: %s" ;;
            dir_creating) echo "La cartella non esiste ancora, provo a crearla..." ;;
            dir_created) echo "Cartella creata: %s" ;;
            dir_create_failed) echo "Impossibile creare %s (permessi negati o percorso non valido qui)." ;;
            synology_folder_hint_1) echo "Comune su Synology DSM: una cartella pensata per contenere dati condivisi va" ;;
            synology_folder_hint_2) echo "spesso creata come vera 'Cartella condivisa' dall'interfaccia (permessi/ACL" ;;
            synology_folder_hint_3) echo "gestiti da DSM, non raggiungibili con un semplice mkdir via SSH). Crea NASBox da:" ;;
            synology_folder_hint_4) echo "  DSM > Pannello di controllo > Cartella condivisa > Crea" ;;
            synology_folder_hint_5) echo "poi torna qui e premi Invio per riprovare." ;;
            qnap_folder_hint_1) echo "Comune su QNAP QTS: crea la cartella condivisa dall'interfaccia, non via SSH:" ;;
            qnap_folder_hint_2) echo "  QTS > Pannello di Controllo > Privilegi > Cartelle condivise > Crea" ;;
            qnap_folder_hint_3) echo "(oppure da File Station), poi torna qui e premi Invio per riprovare." ;;
            generic_folder_hint_1) echo "Controlla i permessi della cartella padre, o rilancia questo script con un" ;;
            generic_folder_hint_2) echo "utente che abbia accesso in scrittura lì (es. sudo)." ;;
            retry_path_prompt) echo "Premi Invio per riprovare con lo stesso percorso, oppure scrivine uno diverso: " ;;
            write_verified) echo "Scrittura verificata in %s" ;;
            write_failed) echo "%s esiste ma non ci si può scrivere con l'utente corrente (%s)." ;;
            synology_perm_hint) echo "Controlla i permessi: DSM > Pannello di controllo > Cartella condivisa > [NASBox] > Modifica > Permessi." ;;
            qnap_perm_hint) echo "Controlla i permessi: QTS > Pannello di Controllo > Privilegi > Cartelle condivise > [NASBox] > Modifica." ;;
            generic_perm_hint) echo "Controlla proprietario e permessi Unix della cartella (ls -ld \"%s\")." ;;
            share_root_set) echo "SHARE_ROOT impostato a %s" ;;
            retention_prompt) echo "Retention dello storico, in giorni (0 = non eliminare mai) [%s]: " ;;
            retention_set) echo "RETENTION_DAYS impostato a %s giorni" ;;
            invalid_retention) echo "La retention deve essere un intero maggiore o uguale a 0." ;;
            unsafe_config) echo "server.conf deve essere un file normale, non un link simbolico." ;;
            using_systemd) echo "Uso systemd per l'avvio al boot." ;;
            need_root_systemd) echo "Per registrarlo come servizio di sistema serve root. Rilancia con:" ;;
            need_root_manual) echo "Per ora lo avvio manualmente (non sopravvive a un riavvio)." ;;
            systemd_installed) echo "Servizio di sistema installato e avviato: sync-daemon-server.service" ;;
            systemd_status_hint) echo "Stato:    systemctl status sync-daemon-server" ;;
            systemd_log_hint) echo "Log:      journalctl -u sync-daemon-server -f" ;;
            using_dsm_rc) echo "Uso la convenzione di avvio al boot di Synology DSM (%s)." ;;
            rc_not_writable) echo "%s non esiste o non è scrivibile con l'utente corrente -- serve root (sudo)." ;;
            rc_installed) echo "Installato %s: DSM lo esegue automaticamente, come un vero servizio di sistema." ;;
            rc_usage) echo "Uso: %s {start|stop}" ;;
            done_header) echo "=== Fatto ===" ;;
            share_root_label) echo "Cartella NASBox:     %s" ;;
            status_label) echo "Stato:               %s --status" ;;
            stop_label) echo "Ferma il demone:     %s --stop" ;;
            prune_label) echo "Pruning manuale:     %s --run-once" ;;
            dryrun_label) echo "Anteprima (no-op):   %s --run-once --dry-run" ;;
            next_step_header) echo "Ora, su ogni PC: client/install.sh -- ti chiederà l'host di questo NAS e" ;;
            next_step_body) echo "questa stessa cartella (%s) per collegarsi." ;;
            *) echo "$key" ;;
        esac
    else
        case "$key" in
            banner) echo "=== NASBox server installer ===" ;;
            package_folder) echo "Package folder: %s" ;;
            platform_detected) echo "Detected platform: %s" ;;
            platform_linux_generic) echo "Generic Linux" ;;
            conf_created) echo "Created server.conf from server.conf.example" ;;
            conf_exists) echo "server.conf already present, updating it with this guided setup's values." ;;
            share_root_prompt) echo "NASBox folder on this NAS (SHARE_ROOT) [%s]: " ;;
            dir_exists) echo "Folder already exists: %s" ;;
            dir_creating) echo "The folder doesn't exist yet, trying to create it..." ;;
            dir_created) echo "Folder created: %s" ;;
            dir_create_failed) echo "Couldn't create %s (permission denied or invalid path here)." ;;
            synology_folder_hint_1) echo "Common on Synology DSM: a folder meant to hold shared data often needs to" ;;
            synology_folder_hint_2) echo "be created as a proper 'Shared Folder' from the interface (permissions/ACLs" ;;
            synology_folder_hint_3) echo "managed by DSM, not reachable with a plain mkdir over SSH). Create NASBox from:" ;;
            synology_folder_hint_4) echo "  DSM > Control Panel > Shared Folder > Create" ;;
            synology_folder_hint_5) echo "then come back here and press Enter to retry." ;;
            qnap_folder_hint_1) echo "Common on QNAP QTS: create the shared folder from the interface, not over SSH:" ;;
            qnap_folder_hint_2) echo "  QTS > Control Panel > Privilege Settings > Shared Folders > Create" ;;
            qnap_folder_hint_3) echo "(or from File Station), then come back here and press Enter to retry." ;;
            generic_folder_hint_1) echo "Check the parent folder's permissions, or re-run this script with a" ;;
            generic_folder_hint_2) echo "user that has write access there (e.g. sudo)." ;;
            retry_path_prompt) echo "Press Enter to retry with the same path, or type a different one: " ;;
            write_verified) echo "Write access verified in %s" ;;
            write_failed) echo "%s exists but the current user (%s) can't write to it." ;;
            synology_perm_hint) echo "Check the permissions: DSM > Control Panel > Shared Folder > [NASBox] > Edit > Permissions." ;;
            qnap_perm_hint) echo "Check the permissions: QTS > Control Panel > Privilege Settings > Shared Folders > [NASBox] > Edit." ;;
            generic_perm_hint) echo "Check the folder's Unix owner and permissions (ls -ld \"%s\")." ;;
            share_root_set) echo "SHARE_ROOT set to %s" ;;
            retention_prompt) echo "History retention, in days (0 = never delete) [%s]: " ;;
            retention_set) echo "RETENTION_DAYS set to %s days" ;;
            invalid_retention) echo "Retention must be an integer greater than or equal to 0." ;;
            unsafe_config) echo "server.conf must be a regular file, not a symbolic link." ;;
            using_systemd) echo "Using systemd for boot startup." ;;
            need_root_systemd) echo "Root is required to register it as a system service. Re-run with:" ;;
            need_root_manual) echo "Starting it manually for now (won't survive a reboot)." ;;
            systemd_installed) echo "System service installed and started: sync-daemon-server.service" ;;
            systemd_status_hint) echo "Status:   systemctl status sync-daemon-server" ;;
            systemd_log_hint) echo "Log:      journalctl -u sync-daemon-server -f" ;;
            using_dsm_rc) echo "Using Synology DSM's boot-startup convention (%s)." ;;
            rc_not_writable) echo "%s doesn't exist or isn't writable by the current user -- root (sudo) is required." ;;
            rc_installed) echo "Installed %s: DSM runs it automatically, like a real system service." ;;
            rc_usage) echo "Usage: %s {start|stop}" ;;
            done_header) echo "=== Done ===" ;;
            share_root_label) echo "NASBox folder:       %s" ;;
            status_label) echo "Status:              %s --status" ;;
            stop_label) echo "Stop the daemon:     %s --stop" ;;
            prune_label) echo "Manual pruning:      %s --run-once" ;;
            dryrun_label) echo "Preview (no-op):     %s --run-once --dry-run" ;;
            next_step_header) echo "Now, on every PC: client/install.sh -- it will ask for this NAS's host and" ;;
            next_step_body) echo "this same folder (%s) to connect." ;;
            *) echo "$key" ;;
        esac
    fi
}
m() { local key="$1"; shift; printf "$(msg "$key")" "$@"; }

write_config_value() {
    local key="$1" value="$2" tmp
    tmp=$(mktemp "$SCRIPT_DIR/.server.conf.XXXXXX")
    chmod 600 "$tmp"
    grep -vE "^${key}=" "$SCRIPT_DIR/server.conf" > "$tmp" || true
    printf '%s=%s\n' "$key" "$value" >> "$tmp"
    mv -f "$tmp" "$SCRIPT_DIR/server.conf"
}

echo ""
echo "$(msg banner)"
echo "$(m package_folder "$SCRIPT_DIR")"
echo ""

chmod +x "$MAIN"

# --- 1) which NAS platform is this, so the rest of the wizard can give
#        advice/defaults that actually match how this box works ---
detect_platform() {
    if [[ -f /etc/synoinfo.conf ]] || command -v synogetkeyvalue &>/dev/null || [[ -d /usr/syno ]]; then
        echo "synology"
    elif [[ -f /etc/config/qpkg.conf ]] || command -v getcfg &>/dev/null || [[ -d /share ]]; then
        echo "qnap"
    else
        echo "linux"
    fi
}
PLATFORM="$(detect_platform)"
case "$PLATFORM" in
    synology) PLATFORM_LABEL="Synology DSM"; DEFAULT_SHARE_ROOT="/volume1/NASBox" ;;
    qnap)     PLATFORM_LABEL="QNAP QTS"; DEFAULT_SHARE_ROOT="/share/CACHEDEV1_DATA/NASBox" ;;
    *)        PLATFORM_LABEL="$(msg platform_linux_generic)"; DEFAULT_SHARE_ROOT="/srv/NASBox" ;;
esac
info "$(m platform_detected "$PLATFORM_LABEL")"

if [[ -e "$SCRIPT_DIR/server.conf" && ( -L "$SCRIPT_DIR/server.conf" || ! -f "$SCRIPT_DIR/server.conf" ) ]]; then
    err "$(msg unsafe_config)"
    exit 1
elif [[ ! -f "$SCRIPT_DIR/server.conf" ]]; then
    cp "$SCRIPT_DIR/server.conf.example" "$SCRIPT_DIR/server.conf"
    chmod 600 "$SCRIPT_DIR/server.conf"
    ok "$(msg conf_created)"
else
    ok "$(msg conf_exists)"
fi

# --- 2) SHARE_ROOT: verified, not just typed in and hoped for. On Synology
#        in particular, a mkdir over SSH under /volume1 often fails if the
#        folder wasn't created as a proper DSM "Shared Folder" (permissions/
#        ACLs managed by the interface, not the bare filesystem) -- better to
#        find that out here, with precise instructions, than with the daemon
#        started and the log full of "SHARE_ROOT non esiste". ---
echo ""
current=$(grep -E '^SHARE_ROOT=' "$SCRIPT_DIR/server.conf" 2>/dev/null | tail -1 | cut -d= -f2- || true)
DEFAULT_SHARE_ROOT="${current:-$DEFAULT_SHARE_ROOT}"

SHARE_ROOT=""
while true; do
    read -r -p "$(m share_root_prompt "$DEFAULT_SHARE_ROOT")" share_root_input
    share_root_input="${share_root_input:-$DEFAULT_SHARE_ROOT}"

    if [[ -d "$share_root_input" ]]; then
        ok "$(m dir_exists "$share_root_input")"
    else
        info "$(msg dir_creating)"
        if mkdir -p "$share_root_input" 2>/dev/null; then
            ok "$(m dir_created "$share_root_input")"
        else
            err "$(m dir_create_failed "$share_root_input")"
            case "$PLATFORM" in
                synology)
                    warn "$(msg synology_folder_hint_1)"
                    warn "$(msg synology_folder_hint_2)"
                    warn "$(msg synology_folder_hint_3)"
                    warn "$(msg synology_folder_hint_4)"
                    warn "$(msg synology_folder_hint_5)"
                    ;;
                qnap)
                    warn "$(msg qnap_folder_hint_1)"
                    warn "$(msg qnap_folder_hint_2)"
                    warn "$(msg qnap_folder_hint_3)"
                    ;;
                *)
                    warn "$(msg generic_folder_hint_1)"
                    warn "$(msg generic_folder_hint_2)"
                    ;;
            esac
            read -r -p "$(msg retry_path_prompt)" retry_path
            DEFAULT_SHARE_ROOT="${retry_path:-$share_root_input}"
            continue
        fi
    fi

    # A directory existing isn't the same as this user being able to write into
    # it -- a Synology shared folder created via DSM can still have ACLs that
    # don't include the SSH account, and that failure mode looks identical to
    # "everything's fine" until the first push tries to land a file here.
    test_file="$share_root_input/.nasbox-write-test"
    if touch "$test_file" 2>/dev/null; then
        rm -f "$test_file"
        ok "$(m write_verified "$share_root_input")"
        SHARE_ROOT="$share_root_input"
        break
    fi
    err "$(m write_failed "$share_root_input" "$(whoami)")"
    case "$PLATFORM" in
        synology) warn "$(msg synology_perm_hint)" ;;
        qnap)     warn "$(msg qnap_perm_hint)" ;;
        *)        warn "$(m generic_perm_hint "$share_root_input")" ;;
    esac
    read -r -p "$(msg retry_path_prompt)" retry_path
    DEFAULT_SHARE_ROOT="${retry_path:-$share_root_input}"
done

write_config_value "SHARE_ROOT" "$SHARE_ROOT"
ok "$(m share_root_set "$SHARE_ROOT")"

# The repository marker is deliberately initialized as part of the server
# installation. It protects clients from treating an unmounted NAS volume as
# an empty share, while keeping the server package self-contained.
if ! "$MAIN" --init-repository; then
    err "Impossibile inizializzare il marker del repository NASBox."
    exit 1
fi

# --- 3) retention, asked here instead of only via the raw config file, so
#        the wizard leaves a sane value set instead of whatever shipped in
#        server.conf.example ---
echo ""
current_retention=$(grep -E '^RETENTION_DAYS=' "$SCRIPT_DIR/server.conf" 2>/dev/null | tail -1 | cut -d= -f2- || true)
while true; do
    read -r -p "$(m retention_prompt "${current_retention:-90}")" retention_input
    retention_input="${retention_input:-${current_retention:-90}}"
    [[ "$retention_input" =~ ^[0-9]+$ ]] && break
    err "$(msg invalid_retention)"
done
write_config_value "RETENTION_DAYS" "$retention_input"
ok "$(m retention_set "$retention_input")"

# --- system-level service registration (this is the primary path: the daemon
#     should start on its own at boot, managed by the OS/root, not by a user
#     remembering to run --start by hand) ---
echo ""
SYSTEMD_UNIT="/etc/systemd/system/sync-daemon-server.service"
RC_DIR="/usr/local/etc/rc.d"

if [[ -d /run/systemd/system ]] && command -v systemctl &>/dev/null; then
    info "$(msg using_systemd)"
    if [[ "${EUID:-$(id -u)}" -ne 0 ]]; then
        warn "$(msg need_root_systemd)"
        warn "  sudo $SCRIPT_DIR/install.sh"
        warn "$(msg need_root_manual)"
        "$MAIN" --restart
    else
        cat > "$SYSTEMD_UNIT" <<EOF
[Unit]
Description=sync-daemon-server -- NASBox history/trash pruning
After=network.target

[Service]
Type=simple
ExecStart="$MAIN" --run-foreground
Restart=always
RestartSec=10

[Install]
WantedBy=multi-user.target
EOF
        systemctl daemon-reload
        systemctl enable sync-daemon-server.service
        systemctl restart sync-daemon-server.service
        ok "$(msg systemd_installed)"
        info "$(msg systemd_status_hint)"
        info "$(msg systemd_log_hint)"
    fi
elif [[ "$PLATFORM" == "synology" || ( -d "$RC_DIR" && -w "$RC_DIR" ) ]]; then
    info "$(m using_dsm_rc "$RC_DIR")"
    if [[ ! -d "$RC_DIR" || ! -w "$RC_DIR" ]]; then
        warn "$(m rc_not_writable "$RC_DIR")"
        warn "$(msg need_root_manual)"
        "$MAIN" --restart
    else
        RC_SCRIPT="$RC_DIR/sync-daemon-server.sh"
        cat > "$RC_SCRIPT" <<EOF
#!/bin/sh
# Installed by sync-daemon-server/install.sh -- run by DSM itself (not by a
# user) at boot and shutdown.
case "\$1" in
    start) "$MAIN" --start ;;
    stop)  "$MAIN" --stop ;;
    *) echo "$(m rc_usage '$0')" ;;
esac
EOF
        chmod +x "$RC_SCRIPT"
        ok "$(m rc_installed "$RC_SCRIPT")"
        "$MAIN" --restart
    fi
else
    "$MAIN" --restart
    if [[ "$LANG_CODE" == "it" ]]; then
        cat <<EOF

Nessun meccanismo di avvio al boot gestito dal sistema è stato rilevato qui
automaticamente. Su QNAP QTS non esiste un hook di sistema pulito senza
impacchettare un QPKG vero e proprio, quindi va registrato a mano:

  QNAP QTS: Pannello di Controllo > Sistema > Utilità di Pianificazione >
    Script Autorun (o "Attività all'avvio"), comando: $MAIN --start

Il demone è comunque già avviato ora ed è un processo persistente e autonomo
(nessun cron: il ciclo di pruning è interno, vedi CHECK_INTERVAL_MINUTES in
server.conf); questa registrazione serve solo a farlo ripartire da solo dopo
un riavvio del NAS.
EOF
    else
        cat <<EOF

No system-managed boot-startup mechanism was auto-detected here. QNAP QTS
has no clean system hook without packaging a real QPKG, so it needs to be
registered by hand:

  QNAP QTS: Control Panel > System > Task Scheduler >
    Autorun Script (or "Startup Task"), command: $MAIN --start

The daemon is already running now regardless, as a persistent, self-
contained process (no cron: the pruning cycle is internal, see
CHECK_INTERVAL_MINUTES in server.conf); this registration only makes it
start again on its own after a NAS reboot.
EOF
    fi
fi

echo ""
echo "$(msg done_header)"
echo "$(m share_root_label "$SHARE_ROOT")"
echo "$(m status_label "$MAIN")"
echo "$(m stop_label "$MAIN")"
echo "$(m prune_label "$MAIN")"
echo "$(m dryrun_label "$MAIN")"
echo ""
echo "$(msg next_step_header)"
echo "$(m next_step_body "$SHARE_ROOT")"
