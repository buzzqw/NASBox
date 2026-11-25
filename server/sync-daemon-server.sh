#!/usr/bin/env bash
#
# sync-daemon-server -- self-contained NAS-side history/trash retention daemon.
#
# Runs on the NAS (Synology, QNAP, or any Linux box acting as the rsync target
# for the NASBox client) as a single always-running process with its own
# internal timer -- no cron / Task Scheduler involved. The periodic pruning
# is application logic, not something delegated to the OS scheduler.
#
# Everything this script needs -- config, logs, PID file -- lives next to it
# in its own folder, so the whole thing can be copied onto a data volume and
# keeps working across DSM/QTS firmware updates (which do not guarantee /etc
# or /root survive).
#
# There is exactly one synced tree under SHARE_ROOT (the NASBox folder, root
# to root against every client's local_root) -- this script prunes ONE trash
# dir, SHARE_ROOT/.sync-trash, not a per-subfolder "share" list. (Versions
# before 3.0.0 supported multiple named shares under SHARE_ROOT, each pruned
# independently; that model was retired client-side too -- see MANUALE.md §8.)
#
# Usage:
#   sync-daemon-server.sh --start              Start the daemon in the background
#   sync-daemon-server.sh --stop               Stop it
#   sync-daemon-server.sh --restart            Stop + start
#   sync-daemon-server.sh --status             Is it running? + trash/retention info
#   sync-daemon-server.sh --run-foreground      Run the loop in this terminal (debug/supervisor use)
#   sync-daemon-server.sh --run-once [--dry-run]  One pruning pass, then exit
#   sync-daemon-server.sh --metrics              Read-only NAS load metrics
#   sync-daemon-server.sh --wait-for-revision REVISION TIMEOUT
#                                                   Wait for manifest.revision
#   sync-daemon-server.sh -c FILE               Use an alternate config file
#   sync-daemon-server.sh --help
#
set -uo pipefail

VERSION="3.32.2"
SCRIPT_PATH="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/$(basename "${BASH_SOURCE[0]}")"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

CONFIG_FILE="$SCRIPT_DIR/server.conf"
STATE_DIR="$SCRIPT_DIR/state"
LOG_FILE="$STATE_DIR/server.log"
EVENTS_FILE="$STATE_DIR/events.jsonl"
PID_FILE="$STATE_DIR/daemon.pid"
DAEMON_LOCK_FILE="$STATE_DIR/daemon.lock"
STAMP_FILE="$STATE_DIR/last-run"
TRASH_WATERMARK_FILE="$STATE_DIR/trash-watermark"
SYNC_LOCK_FILE="$STATE_DIR/sync-transfer.lock"
SYNC_LOCK_OWNER_FILE="$STATE_DIR/sync-transfer.lock.owner"
TRANSFER_QUEUE_DIR="$STATE_DIR/transfer-queue"
TRANSFER_QUEUE_LOCK_FILE="$STATE_DIR/transfer-queue.lock"
JOURNAL_FILE="$STATE_DIR/transfer-journal.tsv"
MANIFEST_FILE="$STATE_DIR/manifest.tsv"
JOURNAL_LOCK_FILE="$STATE_DIR/transfer-journal.lock"
MANIFEST_REVISION_FILE="$STATE_DIR/manifest.revision"
MANIFEST_SWEEP_FILE="$STATE_DIR/manifest-sweep.stamp"
# Single snapshot (revision + rendered rows) used by incremental exports.
# Written atomically and only by --manifest-export-delta, never by the plain
# read-only --manifest-export.
MANIFEST_SNAPSHOT_FILE="$STATE_DIR/manifest.snapshot"
TRANSACTION_DIR="$STATE_DIR/transactions"
STAGING_DIRNAME=".nasbox-staging"
CHANGE_WAIT_MAX_TIMEOUT_SECONDS=3600

# How many sha256sum/stat lookups cmd_file_states runs concurrently -- see
# that function for why. Left empty on purpose: detect_capabilities() derives it
# from the CPU count of this NAS (one job per core, at most 8), and server.conf
# can override it. A fixed 8 was either too little for a 16-core NAS or too much
# for a 2-core one with a single disk.
FILE_STATES_PARALLELISM=""

# --- capabilities ---
# What this machine can do, detected once and published to the clients. The
# clients adapt instead of assuming: a NAS without the change feed polls, a
# single-core NAS hashes one file at a time, a NAS with solid-state disks is not
# asked to rest them. Every value has a safe fallback, so an old client that
# ignores this block keeps working exactly as before.
NAS_CPU_COUNT=""
NAS_MEM_TOTAL_MB=""
NAS_DISK_ROTATIONAL=""
NAS_RSYNC_VERSION=""
NAS_RSYNC_PATH=""
NAS_RSYNC_SYSTEM_VERSION=""
NAS_FS_TYPE=""
CAPABILITIES_LIST="change_feed,causal_versions,v3_rename,batch_delete,quiet_lock,empty_dir_prune,capabilities_v1"

# 1 = rotating, 0 = solid state, empty = unknown. Derived from the device that
# holds the share; when the volume is a mapper/LVM device (Synology) the first
# physical disk is used, which is what a one-disk NAS has anyway.
disk_rotational_for() {
    local path="$1" device base parent block
    device=$(df -P "$path" 2>/dev/null | awk 'NR == 2 {print $1}')
    if [[ -n "$device" ]]; then
        base=$(basename "$(readlink -f "$device" 2>/dev/null || printf '%s' "$device")")
        if [[ -r "/sys/block/$base/queue/rotational" ]]; then
            cat "/sys/block/$base/queue/rotational"
            return 0
        fi
        for parent in /sys/block/*/"$base"; do
            [[ -e "$parent" ]] || continue
            block=$(basename "$(dirname "$parent")")
            if [[ -r "/sys/block/$block/queue/rotational" ]]; then
                cat "/sys/block/$block/queue/rotational"
                return 0
            fi
        done
    fi
    for parent in /sys/block/sd*/queue/rotational; do
        [[ -r "$parent" ]] && { cat "$parent"; return 0; }
    done
    return 0
}

detect_capabilities() {
    NAS_CPU_COUNT=$(nproc 2>/dev/null || echo 1)
    [[ "$NAS_CPU_COUNT" =~ ^[1-9][0-9]*$ ]] || NAS_CPU_COUNT=1
    local mem_kb=""
    mem_kb=$(awk '/^MemTotal:/ {print $2; exit}' /proc/meminfo 2>/dev/null || true)
    if [[ "$mem_kb" =~ ^[0-9]+$ ]]; then
        NAS_MEM_TOTAL_MB=$((mem_kb / 1024))
    else
        NAS_MEM_TOTAL_MB=0
    fi
    NAS_DISK_ROTATIONAL=$(disk_rotational_for "$SHARE_ROOT" || true)
    # "3.5.2-g41bbd895" -> "3.5.2": clients compare plain versions.
    NAS_RSYNC_SYSTEM_VERSION=$(rsync --version 2>/dev/null | head -1 | awk '{print $3}' \
        | sed 's/[^0-9.].*$//')
    NAS_RSYNC_VERSION="$NAS_RSYNC_SYSTEM_VERSION"
    # A private, newer rsync (tools/install_server_rsync.sh) wins when it runs.
    # The system one stays untouched -- it is what DSM and other services use.
    # The client asks for it with --rsync-path, so nothing has to be installed.
    local candidate="$SCRIPT_DIR/bin/nasbox-rsync" private_version="" private_output=""
    NAS_RSYNC_PATH=""
    if [[ -x "$candidate" ]]; then
        # Exit status matters: a wrapper that prints a banner and then dies must
        # not be advertised, or every client transfer would fail.
        if private_output=$("$candidate" --version 2>/dev/null); then
            private_version=$(printf '%s\n' "$private_output" | head -1 | awk '{print $3}' \
                | sed 's/[^0-9.].*$//')
        fi
        if [[ -n "$private_version" ]]; then
            NAS_RSYNC_PATH="$candidate"
            NAS_RSYNC_VERSION="$private_version"
            case ",$CAPABILITIES_LIST," in
                *,private_rsync,*) ;;
                *) CAPABILITIES_LIST="$CAPABILITIES_LIST,private_rsync" ;;
            esac
        fi
    fi
    # SHARE_ROOT is normally a folder inside a volume (not a mount point), so the
    # filesystem comes from the device df reports, not from matching the path.
    local mount_point=""
    mount_point=$(df -P "$SHARE_ROOT" 2>/dev/null | awk 'NR == 2 {print $6}')
    if [[ -n "$mount_point" ]]; then
        NAS_FS_TYPE=$(awk -v mp="$mount_point" '$2 == mp {print $3; exit}' /proc/mounts 2>/dev/null || true)
    fi
    if [[ -z "$NAS_FS_TYPE" ]]; then
        NAS_FS_TYPE=$(stat -f -c %T "$SHARE_ROOT" 2>/dev/null || true)
    fi
    if [[ -z "${FILE_STATES_PARALLELISM:-}" ]]; then
        FILE_STATES_PARALLELISM="$NAS_CPU_COUNT"
        ((FILE_STATES_PARALLELISM > 8)) && FILE_STATES_PARALLELISM=8
        ((FILE_STATES_PARALLELISM < 1)) && FILE_STATES_PARALLELISM=1
    fi
    return 0
}

# KEY=VALUE only (no header): --print-config embeds these lines, and a dedicated
# --capabilities query prints them with a CAPABILITIES_V1 header in front.
print_capability_values() {
    echo "CAPABILITIES=$CAPABILITIES_LIST"
    echo "CHANGE_FEED_AVAILABLE=true"
    echo "CAUSAL_VERSIONS_AVAILABLE=true"
    echo "BROWSE_RENAME_PROTOCOLS=BROWSE_RENAME_V1,BROWSE_RENAME_V2,BROWSE_RENAME_V3"
    echo "CPU_COUNT=$NAS_CPU_COUNT"
    echo "MEM_TOTAL_MB=$NAS_MEM_TOTAL_MB"
    echo "DISK_ROTATIONAL=$NAS_DISK_ROTATIONAL"
    echo "RSYNC_VERSION=$NAS_RSYNC_VERSION"
    echo "RSYNC_PATH=$NAS_RSYNC_PATH"
    echo "RSYNC_SYSTEM_VERSION=$NAS_RSYNC_SYSTEM_VERSION"
    echo "FS_TYPE=$NAS_FS_TYPE"
    echo "FILE_STATES_PARALLELISM=$FILE_STATES_PARALLELISM"
}

cmd_capabilities() {
    echo "CAPABILITIES_V1"
    echo "SERVER_VERSION=$VERSION"
    print_capability_values
}

TRASH_DIRNAME=".sync-trash"
REPOSITORY_MARKER_NAME=".nasbox-root"
DRY_RUN="false"

# Even when the retention watermark says nothing can be old enough yet, re-scan
# the trash at least this often. It bounds the cost of an anomalous entry (a
# client clock far behind, or a file placed in the trash by hand) to one extra
# scan a week instead of never -- while letting a normal trash that is far from
# its cutoff skip weeks of the expensive walk.
PRUNE_MAX_SKIP_SECONDS=604800

# Tombstone cleanup rewrites the whole manifest. Doing it on every read would
# turn a read-only export back into a write storm, so the sweep runs at most
# once a day even when the journal is empty.
MANIFEST_SWEEP_MAX_SKIP_SECONDS=86400

# --- logging ---

test_failpoint() {
    # Crash injection is opt-in and only used by isolated subprocess tests.
    if [[ -n "${NASBOX_TEST_FAILPOINT:-}" && "${NASBOX_TEST_FAILPOINT}" == "$1" ]]; then
        kill -KILL "$$"
    fi
}

# Non-fatal counterpart: isolated tests can force one operation (typically a
# revision bump) to fail and verify that journal and revision roll back together.
test_failpoint_return() {
    [[ -n "${NASBOX_TEST_FAILPOINT:-}" && "${NASBOX_TEST_FAILPOINT}" == "$1" ]]
}

log() {
    local level="$1"; shift
    local msg="$*"
    local ts
    ts=$(date '+%Y-%m-%d %H:%M:%S')
    mkdir -p "$STATE_DIR"
    if [[ -f "$LOG_FILE" ]]; then
        local size
        size=$(wc -c < "$LOG_FILE" 2>/dev/null || echo 0)
        if (( size > ${LOG_MAX_BYTES:-5242880} )); then
            mv -f "$LOG_FILE" "$LOG_FILE.1" 2>/dev/null || true
        fi
    fi
    printf '[%s] [%s] %s\n' "$ts" "$level" "$msg" | tee -a "$LOG_FILE" >&2
}

# JSON strings must escape every C0 control character and DEL, not just the few
# with a short escape: a newline, TAB or ESC in a filename previously produced an
# invalid events.jsonl line. Multi-byte UTF-8 bytes are copied unchanged.
json_escape_string() {
    local value="$1" out="" char code i
    for ((i = 0; i < ${#value}; i++)); do
        char="${value:i:1}"
        case "$char" in
            \\) out+='\\' ;;
            '"') out+='\"' ;;
            $'\n') out+='\n' ;;
            $'\r') out+='\r' ;;
            $'\t') out+='\t' ;;
            $'\b') out+='\b' ;;
            $'\f') out+='\f' ;;
            *)
                printf -v code '%d' "'$char"
                if (( code < 32 || code == 127 )); then
                    printf -v code '%04x' "$code"
                    out+="\\u$code"
                else
                    out+="$char"
                fi
                ;;
        esac
    done
    printf '%s' "$out"
}

log_event() {
    local action="$1" path_="$2" detail="${3:-}"
    mkdir -p "$STATE_DIR"
    local ts
    ts=$(date '+%s')
    printf '{"ts": %s, "action": "%s", "path": "%s", "detail": "%s"}\n' \
        "$ts" "$(json_escape_string "$action")" "$(json_escape_string "$path_")" \
        "$(json_escape_string "$detail")" >> "$EVENTS_FILE"
}

# --- config ---

load_config() {
    if [[ ! -f "$CONFIG_FILE" ]]; then
        if [[ -f "$SCRIPT_DIR/server.conf.example" ]]; then
            cp "$SCRIPT_DIR/server.conf.example" "$CONFIG_FILE"
            log INFO "Nessun server.conf trovato: creato da server.conf.example ($CONFIG_FILE)"
        else
            log ERROR "Config non trovato: $CONFIG_FILE"
            exit 1
        fi
    fi
    # Parse only the supported KEY=VALUE entries. This file is often edited by
    # an administrator, but must never become executable shell code when this
    # daemon is started by systemd as root.
    SHARE_ROOT=""
    RETENTION_DAYS=30
    TOMBSTONE_TTL_DAYS=0
    # Kept for compatibility with older server.conf files. A live flock is
    # released automatically when its SSH session closes, so age-based killing
    # is intentionally no longer used.
    SYNC_LOCK_MAX_AGE_MINUTES=0
    CHECK_INTERVAL_MINUTES=60
    PRUNE_MAX_FILES_PER_PASS=500
    TRASH_MAX_MB=0
    TRASH_MIN_KEEP_DAYS=7
    STAGING_RETENTION_HOURS=168
    PRUNE_MAX_STAGING_PER_PASS=20
    LOG_MAX_BYTES=5242880
    JOURNAL_MAX_BYTES=10485760
    JOURNAL_MAX_LINES=50000
    LOG_RETENTION_DAYS=30
    EVENTS_MAX_BYTES=5242880
    STATE_TEMP_MAX_AGE_HOURS=24
    # Empty = derive from the CPU count in detect_capabilities().
    FILE_STATES_PARALLELISM=""
    local key value
    while IFS='=' read -r key value || [[ -n "$key" ]]; do
        case "$key" in
            ""|\#*) ;;
            SHARE_ROOT) SHARE_ROOT="$value" ;;
            RETENTION_DAYS) RETENTION_DAYS="$value" ;;
            TOMBSTONE_TTL_DAYS) TOMBSTONE_TTL_DAYS="$value" ;;
            SYNC_LOCK_MAX_AGE_MINUTES) SYNC_LOCK_MAX_AGE_MINUTES="$value" ;;
            CHECK_INTERVAL_MINUTES) CHECK_INTERVAL_MINUTES="$value" ;;
            PRUNE_MAX_FILES_PER_PASS) PRUNE_MAX_FILES_PER_PASS="$value" ;;
            TRASH_MAX_MB) value="${value%%#*}"; TRASH_MAX_MB="${value//[[:space:]]/}" ;;
            TRASH_MIN_KEEP_DAYS) value="${value%%#*}"; TRASH_MIN_KEEP_DAYS="${value//[[:space:]]/}" ;;
            STAGING_RETENTION_HOURS) STAGING_RETENTION_HOURS="$value" ;;
            PRUNE_MAX_STAGING_PER_PASS) PRUNE_MAX_STAGING_PER_PASS="$value" ;;
            LOG_MAX_BYTES)
                # Numeric values may carry an explanatory inline comment in
                # server.conf.example. Remove it and every surrounding space
                # before validating the value.
                LOG_MAX_BYTES="${value%%#*}"
                LOG_MAX_BYTES="${LOG_MAX_BYTES//[[:space:]]/}"
                ;;
            JOURNAL_MAX_BYTES)
                JOURNAL_MAX_BYTES="${value%%#*}"
                JOURNAL_MAX_BYTES="${JOURNAL_MAX_BYTES//[[:space:]]/}"
                ;;
            JOURNAL_MAX_LINES)
                JOURNAL_MAX_LINES="${value%%#*}"
                JOURNAL_MAX_LINES="${JOURNAL_MAX_LINES//[[:space:]]/}"
                ;;
            LOG_RETENTION_DAYS)
                LOG_RETENTION_DAYS="${value%%#*}"
                LOG_RETENTION_DAYS="${LOG_RETENTION_DAYS//[[:space:]]/}"
                ;;
            EVENTS_MAX_BYTES)
                EVENTS_MAX_BYTES="${value%%#*}"
                EVENTS_MAX_BYTES="${EVENTS_MAX_BYTES//[[:space:]]/}"
                ;;
            STATE_TEMP_MAX_AGE_HOURS)
                STATE_TEMP_MAX_AGE_HOURS="${value%%#*}"
                STATE_TEMP_MAX_AGE_HOURS="${STATE_TEMP_MAX_AGE_HOURS//[[:space:]]/}"
                ;;
            FILE_STATES_PARALLELISM)
                FILE_STATES_PARALLELISM="${value%%#*}"
                FILE_STATES_PARALLELISM="${FILE_STATES_PARALLELISM//[[:space:]]/}"
                ;;
            *) log ERROR "Chiave non supportata in $CONFIG_FILE: $key"; exit 1 ;;
        esac
    done < "$CONFIG_FILE"

    [[ -n "$SHARE_ROOT" ]] || { log ERROR "SHARE_ROOT non impostato in $CONFIG_FILE"; exit 1; }
    [[ "$RETENTION_DAYS" =~ ^[0-9]+$ ]] || { log ERROR "RETENTION_DAYS deve essere un intero >= 0"; exit 1; }
    [[ "$TRASH_MAX_MB" =~ ^[0-9]+$ ]] || { log ERROR "TRASH_MAX_MB deve essere un intero >= 0"; exit 1; }
    [[ "$TRASH_MIN_KEEP_DAYS" =~ ^[0-9]+$ ]] || { log ERROR "TRASH_MIN_KEEP_DAYS deve essere un intero >= 0"; exit 1; }
    [[ "$TOMBSTONE_TTL_DAYS" =~ ^[0-9]+$ ]] || { log ERROR "TOMBSTONE_TTL_DAYS deve essere un intero >= 0"; exit 1; }
    [[ "$SYNC_LOCK_MAX_AGE_MINUTES" =~ ^[0-9]+$ ]] || { log ERROR "SYNC_LOCK_MAX_AGE_MINUTES deve essere un intero >= 0"; exit 1; }
    [[ "$CHECK_INTERVAL_MINUTES" =~ ^[1-9][0-9]*$ ]] || { log ERROR "CHECK_INTERVAL_MINUTES deve essere un intero >= 1"; exit 1; }
    [[ "$PRUNE_MAX_FILES_PER_PASS" =~ ^[1-9][0-9]*$ ]] || { log ERROR "PRUNE_MAX_FILES_PER_PASS deve essere un intero >= 1"; exit 1; }
    [[ "$STAGING_RETENTION_HOURS" =~ ^[1-9][0-9]*$ ]] || { log ERROR "STAGING_RETENTION_HOURS deve essere un intero >= 1"; exit 1; }
    [[ "$PRUNE_MAX_STAGING_PER_PASS" =~ ^[1-9][0-9]*$ ]] || { log ERROR "PRUNE_MAX_STAGING_PER_PASS deve essere un intero >= 1"; exit 1; }
    [[ "$LOG_MAX_BYTES" =~ ^[1-9][0-9]*$ ]] || { log ERROR "LOG_MAX_BYTES deve essere un intero >= 1"; exit 1; }
    [[ "$JOURNAL_MAX_BYTES" =~ ^[1-9][0-9]*$ ]] || { log ERROR "JOURNAL_MAX_BYTES deve essere un intero >= 1"; exit 1; }
    [[ "$JOURNAL_MAX_LINES" =~ ^[1-9][0-9]*$ ]] || { log ERROR "JOURNAL_MAX_LINES deve essere un intero >= 1"; exit 1; }
    [[ "$LOG_RETENTION_DAYS" =~ ^[0-9]+$ ]] || { log ERROR "LOG_RETENTION_DAYS deve essere un intero >= 0"; exit 1; }
    [[ "$EVENTS_MAX_BYTES" =~ ^[1-9][0-9]*$ ]] || { log ERROR "EVENTS_MAX_BYTES deve essere un intero >= 1"; exit 1; }
    [[ "$STATE_TEMP_MAX_AGE_HOURS" =~ ^[0-9]+$ ]] || { log ERROR "STATE_TEMP_MAX_AGE_HOURS deve essere un intero >= 0"; exit 1; }
    if [[ -n "$FILE_STATES_PARALLELISM" ]]; then
        [[ "$FILE_STATES_PARALLELISM" =~ ^[1-9][0-9]*$ && "$FILE_STATES_PARALLELISM" -le 64 ]] \
            || { log ERROR "FILE_STATES_PARALLELISM deve essere un intero tra 1 e 64"; exit 1; }
    fi

    if [[ ! -d "$SHARE_ROOT" ]]; then
        log ERROR "SHARE_ROOT non esiste: $SHARE_ROOT"
        exit 1
    fi
}

ensure_journal_files() {
    mkdir -p "$STATE_DIR"
    if [[ ! -f "$JOURNAL_FILE" ]]; then
        printf 'NASBOX_JOURNAL_V1\n' > "$JOURNAL_FILE"
    fi
    if [[ ! -f "$MANIFEST_FILE" ]]; then
        printf 'NASBOX_MANIFEST_V1\n' > "$MANIFEST_FILE"
    fi
    if [[ ! -f "$MANIFEST_REVISION_FILE" ]]; then
        printf '0\n' > "$MANIFEST_REVISION_FILE"
    fi
}

write_move_transaction() {
    local file="$1" kind="$2" txid="$3" stage="$4" device="$5" run_ts="$6"
    local relative="$7" target="$8" backup="$9" paths_file="${10:-}" protocol="${11:-STAGING_PUBLISH_V1}" tmp
    tmp="${file}.tmp.$$"
    printf '%s\0%s\0%s\0%s\0%s\0%s\0%s\0%s\0%s\0%s\0%s\0' \
        "$kind" "$txid" "$stage" "$device" "$run_ts" "$relative" "$target" "$backup" \
        "$paths_file" "$protocol" > "$tmp" && mv -f "$tmp" "$file"
}

begin_move_transaction() {
    local kind="$1" txid="$2" device="$3" run_ts="$4" relative="$5" target="$6" backup="$7" paths_file="${8:-}" protocol="${9:-STAGING_PUBLISH_V1}"
    mkdir -p "$TRANSACTION_DIR" || return 1
    write_move_transaction "$TRANSACTION_DIR/$txid.txn" "$kind" "$txid" STARTED \
        "$device" "$run_ts" "$relative" "$target" "$backup" "$paths_file" "$protocol"
}

advance_move_transaction() {
    local file="$1" stage="$2"
    local kind txid old_stage device run_ts relative target backup paths_file protocol
    exec 3< "$file" || return 1
    IFS= read -r -d '' kind <&3 || { exec 3<&-; return 1; }
    IFS= read -r -d '' txid <&3 || { exec 3<&-; return 1; }
    IFS= read -r -d '' old_stage <&3 || { exec 3<&-; return 1; }
    IFS= read -r -d '' device <&3 || { exec 3<&-; return 1; }
    IFS= read -r -d '' run_ts <&3 || { exec 3<&-; return 1; }
    IFS= read -r -d '' relative <&3 || { exec 3<&-; return 1; }
    IFS= read -r -d '' target <&3 || { exec 3<&-; return 1; }
    IFS= read -r -d '' backup <&3 || { exec 3<&-; return 1; }
    IFS= read -r -d '' paths_file <&3 || paths_file=""
    IFS= read -r -d '' protocol <&3 || protocol="STAGING_PUBLISH_V1"
    exec 3<&-
    write_move_transaction "$file" "$kind" "$txid" "$stage" "$device" "$run_ts" \
        "$relative" "$target" "$backup" "$paths_file" "$protocol"
}

recover_pending_transactions_unlocked() {
    mkdir -p "$TRANSACTION_DIR" || return 1
    local file kind txid stage device run_ts relative target backup paths_file protocol
    local rec_path rec_digest rec_size rec_mtime
    shopt -s nullglob
    for file in "$TRANSACTION_DIR"/*.txn; do
        exec 3< "$file"
        IFS= read -r -d '' kind <&3 || { exec 3<&-; continue; }
        IFS= read -r -d '' txid <&3 || { exec 3<&-; continue; }
        IFS= read -r -d '' stage <&3 || { exec 3<&-; continue; }
        IFS= read -r -d '' device <&3 || { exec 3<&-; continue; }
        IFS= read -r -d '' run_ts <&3 || { exec 3<&-; continue; }
        IFS= read -r -d '' relative <&3 || { exec 3<&-; continue; }
        IFS= read -r -d '' target <&3 || { exec 3<&-; continue; }
        IFS= read -r -d '' backup <&3 || { exec 3<&-; continue; }
        IFS= read -r -d '' paths_file <&3 || paths_file=""
        IFS= read -r -d '' protocol <&3 || protocol="STAGING_PUBLISH_V1"
        exec 3<&-

        case "$kind" in
            DELETE)
                if grep -Fq $'COMMIT\t'"$txid" "$JOURNAL_FILE" 2>/dev/null; then
                    # A crash after journal append but before the transaction
                    # marker was removed. The extra revision is harmless and
                    # makes the manifest change visible to waiting clients.
                    bump_manifest_revision_unlocked || return 1
                    rm -f "$file"
                    continue
                fi
                if [[ "$stage" == "STARTED" && -e "$target" ]]; then
                    rm -f "$file"
                    continue
                fi
                if [[ ! -e "$target" && ! -L "$target" && -e "$backup" ]]; then
                    if append_delete_journal_unlocked "$relative" "$device" "$txid"; then
                        rm -f "$file"
                    fi
                    continue
                fi
                if [[ -e "$target" || -L "$target" ]] && [[ ! -e "$backup" ]]; then
                    rm -f "$file"
                    continue
                fi
                log ERROR "recovery transazione ambigua: $txid ($relative)"
                ;;
            BROWSE_DELETE|BROWSE_RENAME)
                if [[ -z "$paths_file" || ! -f "$paths_file" ]]; then
                    log ERROR "recovery transazione senza elenco percorsi: $txid ($relative)"
                    continue
                fi
                if grep -Fq $'COMMIT\t'"$txid" "$JOURNAL_FILE" 2>/dev/null; then
                    rm -f "$file" "$paths_file"
                    continue
                fi
                if [[ "$kind" == "BROWSE_RENAME" && ( "$protocol" == "BROWSE_RENAME_V2" || "$protocol" == "BROWSE_RENAME_V3" ) ]]; then
                    # V2's paths file contains source/destination metadata. A
                    # crash after mv must complete both journal sides; it must
                    # never be downgraded to a source-only tombstone.
                    if [[ -e "$target" || -L "$target" ]] && [[ ! -e "$backup" && ! -L "$backup" ]]; then
                        rm -f "$file" "$paths_file"
                        continue
                    fi
                    if [[ ! -e "$target" && ! -L "$target" && ( -e "$backup" || -L "$backup" ) ]]; then
                        if append_rename_journal_batch_unlocked "$paths_file" "$device" "$txid"; then
                            rm -f "$file" "$paths_file"
                        fi
                        continue
                    fi
                    log ERROR "recovery rename transazione ambigua: $txid ($relative)"
                    continue
                fi
                if [[ "$stage" == "STARTED" && ( -e "$target" || -L "$target" ) ]]; then
                    rm -f "$file" "$paths_file"
                    continue
                fi
                if [[ ! -e "$target" && ! -L "$target" && ( -e "$backup" || -L "$backup" ) ]]; then
                    if append_delete_journal_batch_unlocked "$paths_file" "$device" "$txid"; then
                        rm -f "$file" "$paths_file"
                    fi
                    continue
                fi
                if [[ ( -e "$target" || -L "$target" ) && ! -e "$backup" && ! -L "$backup" ]]; then
                    rm -f "$file" "$paths_file"
                    continue
                fi
                log ERROR "recovery transazione ambigua: $txid ($relative)"
                ;;
            RESTORE)
                if [[ -z "$paths_file" || ! -f "$paths_file" ]]; then
                    log ERROR "recovery restore senza payload: $txid ($relative)"
                    continue
                fi
                if grep -Fq $'COMMIT\t'"$txid" "$JOURNAL_FILE" 2>/dev/null; then
                    # The PUT is already durable; a crash may have skipped only
                    # the revision bump or the marker cleanup.
                    bump_manifest_revision_unlocked || return 1
                    rm -f "$file" "$paths_file"
                    continue
                fi
                rec_path="" rec_digest="" rec_size="" rec_mtime=""
                {
                    IFS= read -r -d '' rec_path || :
                    IFS= read -r -d '' rec_digest || :
                    IFS= read -r -d '' rec_size || :
                    IFS= read -r -d '' rec_mtime || :
                } < "$paths_file"
                [[ "$rec_path" == "$relative" && "$rec_digest" =~ ^[0-9a-f]{64}$ && \
                   "$rec_size" =~ ^[0-9]+$ && "$rec_mtime" =~ ^[0-9]+$ ]] || {
                    log ERROR "recovery restore payload non valido: $txid ($relative)"
                    continue
                }
                if [[ -e "$target" || -L "$target" ]]; then
                    if publish_file_matches "$target" "$rec_digest" "$rec_size" "$rec_mtime"; then
                        # The move completed (or a previous attempt had already
                        # restored those bytes): finish the journal side with the
                        # original txid so a repeated recovery stays idempotent.
                        if append_put_journal_batch_unlocked "$paths_file" "$device" "$txid" "$protocol"; then
                            rm -f "$file" "$paths_file"
                        fi
                        continue
                    fi
                    log ERROR "recovery restore target non verificabile: $txid ($relative)"
                    continue
                fi
                if [[ -e "$backup" || -L "$backup" ]]; then
                    # The version is still in the trash: no data changed yet, so
                    # dropping the marker is safe and idempotent.
                    rm -f "$file" "$paths_file"
                    continue
                fi
                log ERROR "recovery restore transazione ambigua: $txid ($relative)"
                ;;
            PUBLISH)
                if [[ -z "$paths_file" || ! -f "$paths_file" ]]; then
                    log ERROR "recovery publish senza metadati: $txid"
                    continue
                fi
                if ! sync_transfer_lock_is_held; then
                    log ERROR "recovery publish senza lock globale client: $txid"
                    return 1
                fi
                if grep -Fq $'COMMIT\t'"$txid" "$JOURNAL_FILE" 2>/dev/null; then
                    bump_manifest_revision_unlocked || return 1
                    rm -f "$file" "$paths_file"
                    continue
                fi
                if recover_publish_transaction_unlocked "$file" "$device" "$target" "$paths_file" "$txid" "$protocol"; then
                    rm -f "$file" "$paths_file"
                fi
                ;;
            *)
                log ERROR "recovery transazione non supportata: $txid ($kind)"
                ;;
        esac
    done
    shopt -u nullglob
}

# A journal record must be durable before the revision that advertises it, and
# the revision's atomic rename must itself be durable (directory entry included)
# before callers report success. `sync FILE` is absent on some NAS firmwares, so
# a targeted flush falls back to a full sync there.
durable_sync_file() {
    local file="$1"
    if ! sync "$file" 2>/dev/null; then
        sync || return 1
    fi
    sync "${file%/*}" 2>/dev/null || true
}

# Restore a journal snapshot and flush it, so a rolled-back transaction can
# never become visible again after a power loss.
rollback_journal_backup() {
    cp "$1" "$JOURNAL_FILE" 2>/dev/null || return 1
    durable_sync_file "$JOURNAL_FILE" || true
}

bump_manifest_revision_unlocked() {
    local revision tmp
    test_failpoint_return "bump_revision_failure" && return 1
    revision=$(cat "$MANIFEST_REVISION_FILE" 2>/dev/null || echo 0)
    [[ "$revision" =~ ^[0-9]+$ ]] || revision=0
    revision=$((revision + 1))
    tmp="${MANIFEST_REVISION_FILE}.tmp.$$"
    printf '%s\n' "$revision" > "$tmp" || { rm -f "$tmp"; return 1; }
    mv -f "$tmp" "$MANIFEST_REVISION_FILE" || { rm -f "$tmp"; return 1; }
    durable_sync_file "$MANIFEST_REVISION_FILE"
}

# Encode the only characters that can break the line-oriented journal format.
# NUL cannot occur in a Unix filename; all other bytes remain unchanged.
encode_journal_field() {
    local value="$1"
    value="${value//%/%25}"
    value="${value//$'\t'/%09}"
    value="${value//$'\n'/%0A}"
    value="${value//$'\r'/%0D}"
    printf '%s' "$value"
}

manifest_sweep_due() {
    local now last=0
    now=$(date '+%s')
    if [[ -f "$MANIFEST_SWEEP_FILE" ]]; then
        last=$(<"$MANIFEST_SWEEP_FILE") || last=0
    fi
    [[ "$last" =~ ^[0-9]+$ ]] || last=0
    (( now - last >= MANIFEST_SWEEP_MAX_SKIP_SECONDS ))
}

mark_manifest_sweep() {
    mkdir -p "$STATE_DIR" 2>/dev/null || true
    date '+%s' > "$MANIFEST_SWEEP_FILE" 2>/dev/null || true
}

# Render the authoritative union of manifest + journal on stdout, without
# writing anything. This is what readers use: --manifest-export used to call
# the compactor and rewrote the whole manifest on every read, which is
# needless NAS CPU/disk churn for every client change notification.
render_manifest_unlocked() {
    local now_epoch tombstone_cutoff_seconds
    now_epoch=$(date '+%s')
    tombstone_cutoff_seconds=$(( TOMBSTONE_TTL_DAYS * 86400 ))
    awk -F '\t' -v now="$now_epoch" -v cutoff="$tombstone_cutoff_seconds" '
        FILENAME == ARGV[1] {
            if (FNR == 1 && $0 == "NASBOX_MANIFEST_V1") next
            if (NF >= 6) state[$1] = $0
            next
        }
        FILENAME == ARGV[2] {
            if (FNR == 1 && $0 == "NASBOX_JOURNAL_V1") next
            if ($1 == "BEGIN") {
                tx = $2
                pending_count = 0
                next
            }
            if (($1 == "PUT" || $1 == "DELETE") && tx != "") {
                pending[++pending_count] = $0
                next
            }
            if ($1 == "COMMIT" && tx != "" && $2 == tx) {
                for (i = 1; i <= pending_count; i++) {
                    split(pending[i], fields, FS)
                    if (fields[1] == "DELETE") {
                        state[fields[2]] = fields[2] FS "" FS "0" FS "0" FS fields[6] FS fields[7] FS fields[8]
                    } else if (fields[1] == "PUT" && length(fields[2]) > 0) {
                        state[fields[2]] = fields[2] FS fields[3] FS fields[4] FS fields[5] FS fields[6] FS fields[7] FS fields[8]
                    }
                }
                tx = ""
                pending_count = 0
                next
            }
        }
        END {
            print "NASBOX_MANIFEST_V1"
            for (path in state) {
                if (cutoff == 0) { print state[path]; continue }
                split(state[path], f, FS)
                if (f[2] == "" && f[3] == "0") {
                    age = now - int(f[6])
                    if (age > cutoff) continue
                }
                print state[path]
            }
        }
    ' "$MANIFEST_FILE" "$JOURNAL_FILE"
}

compact_manifest_unlocked() {
    ensure_journal_files
    local journal_lines tmp
    journal_lines=$(wc -l < "$JOURNAL_FILE" 2>/dev/null || echo 0)
    if (( journal_lines <= 1 )); then
        # Sweep old tombstones at most once a day even when no transaction
        # arrived. Without the watermark this was a full manifest rewrite on
        # every single read.
        if (( TOMBSTONE_TTL_DAYS > 0 )) && manifest_sweep_due; then
            tmp="${MANIFEST_FILE}.sweep.$$"
            if ! render_manifest_unlocked > "$tmp"; then
                rm -f "$tmp"
                return 1
            fi
            mv -f "$tmp" "$MANIFEST_FILE" || { rm -f "$tmp"; return 1; }
            mark_manifest_sweep
        fi
        return 0
    fi
    tmp="${MANIFEST_FILE}.tmp.$$"
    if ! render_manifest_unlocked > "$tmp"; then
        rm -f "$tmp"
        return 1
    fi
    mv -f "$tmp" "$MANIFEST_FILE" || { rm -f "$tmp"; return 1; }
    printf 'NASBOX_JOURNAL_V1\n' > "${JOURNAL_FILE}.tmp" || { rm -f "${JOURNAL_FILE}.tmp"; return 1; }
    mv -f "${JOURNAL_FILE}.tmp" "$JOURNAL_FILE"
    mark_manifest_sweep
}

cmd_journal_append() {
    ensure_journal_files
    local actual_repository_id payload_repository_id=""
    actual_repository_id=$(read_repository_id 2>/dev/null) || {
        echo "journal: repository marker assente o non valido" >&2
        return 1
    }
    local magic txid device timestamp count action path digest size mtime causal server_timestamp
    local tmp="${JOURNAL_FILE}.append.$$"
    IFS= read -r -d '' magic || { echo "journal: intestazione mancante" >&2; return 1; }
    if [[ "$magic" == "JOURNAL_V2" || "$magic" == "JOURNAL_V3" ]]; then
        IFS= read -r -d '' payload_repository_id || return 1
        [[ "$payload_repository_id" == "$actual_repository_id" ]] || {
            echo "journal: repository non corrispondente" >&2
            return 1
        }
    elif [[ "$magic" != "JOURNAL_V1" ]]; then
        echo "journal: protocollo non riconosciuto" >&2
        return 1
    fi
    IFS= read -r -d '' txid || return 1
    IFS= read -r -d '' device || return 1
    IFS= read -r -d '' timestamp || return 1
    IFS= read -r -d '' count || return 1
    [[ "$txid" =~ ^[A-Za-z0-9._:-]{1,128}$ ]] || return 1
    [[ "$device" =~ ^[A-Za-z0-9._:-]{1,128}$ ]] || return 1
    [[ "$timestamp" =~ ^[0-9]+$ ]] || return 1
    [[ "$count" =~ ^[0-9]+$ && "$count" -le 100000 ]] || return 1
    require_sync_transfer_owner "$device" || return $?
    server_timestamp=$(date '+%s')

    {
        printf 'BEGIN\t%s\t%s\t%s\n' "$txid" "$server_timestamp" "$device"
        local index
        for ((index = 0; index < count; index++)); do
            IFS= read -r -d '' action || { rm -f "$tmp"; return 1; }
            IFS= read -r -d '' path || { rm -f "$tmp"; return 1; }
            IFS= read -r -d '' digest || { rm -f "$tmp"; return 1; }
            IFS= read -r -d '' size || { rm -f "$tmp"; return 1; }
            IFS= read -r -d '' mtime || { rm -f "$tmp"; return 1; }
            causal=""
            if [[ "$magic" == "JOURNAL_V3" ]]; then
                IFS= read -r -d '' causal || { rm -f "$tmp"; return 1; }
                [[ -z "$causal" || "$causal" =~ ^[A-Za-z0-9._:-]+:[1-9][0-9]*(,[A-Za-z0-9._:-]+:[1-9][0-9]*)*$ ]] || {
                    rm -f "$tmp"; return 1;
                }
            fi
            [[ "$action" == "PUT" || "$action" == "DELETE" ]] || { rm -f "$tmp"; return 1; }
            [[ -n "$path" ]] || { rm -f "$tmp"; return 1; }
            [[ -z "$digest" || "$digest" =~ ^[0-9a-f]{64}$ ]] || { rm -f "$tmp"; return 1; }
            [[ "$size" =~ ^[0-9]+$ && "$mtime" =~ ^[0-9]+$ ]] || { rm -f "$tmp"; return 1; }
            if [[ "$magic" == "JOURNAL_V3" ]]; then
                printf '%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n' \
                    "$action" "$(encode_journal_field "$path")" "$digest" "$size" "$mtime" \
                    "$(encode_journal_field "$device")" "$server_timestamp" "$causal"
            else
                printf '%s\t%s\t%s\t%s\t%s\t%s\t%s\n' \
                    "$action" "$(encode_journal_field "$path")" "$digest" "$size" "$mtime" \
                    "$(encode_journal_field "$device")" "$server_timestamp"
            fi
        done
        printf 'COMMIT\t%s\n' "$txid"
    } > "$tmp" || { rm -f "$tmp"; return 1; }

    if ! command -v flock >/dev/null 2>&1; then
        rm -f "$tmp"
        echo "journal: flock non disponibile" >&2
        return 1
    fi
    exec 9>>"$JOURNAL_LOCK_FILE"
    if ! flock -x 9; then
        rm -f "$tmp"
        exec 9>&-
        return 1
    fi
    recover_pending_transactions_unlocked || { flock -u 9; exec 9>&-; return 1; }
    if grep -Fq $'COMMIT\t'"$txid" "$JOURNAL_FILE" 2>/dev/null; then
        rm -f "$tmp"
        flock -u 9
        exec 9>&-
        echo "Journal già aggiornato: transazione $txid già committata."
        return 0
    fi
    # Snapshot both files before appending: a failed revision bump must not
    # leave a journal entry that no revision advertises.
    local journal_backup="${JOURNAL_FILE}.rollback.$$.$RANDOM"
    local revision_backup="${MANIFEST_REVISION_FILE}.rollback.$$.$RANDOM"
    local old_revision
    old_revision=$(cat "$MANIFEST_REVISION_FILE" 2>/dev/null || echo 0)
    cp "$JOURNAL_FILE" "$journal_backup" || {
        rm -f "$tmp" "$journal_backup"; flock -u 9; exec 9>&-; return 1;
    }
    local append_status=0
    cat "$tmp" >> "$JOURNAL_FILE" || append_status=1
    rm -f "$tmp"
    if (( append_status != 0 )); then
        rollback_journal_backup "$journal_backup" || true
        rm -f "$journal_backup"
        flock -u 9
        exec 9>&-
        return 1
    fi
    # Flush the journal before publishing the revision that advertises it.
    if ! durable_sync_file "$JOURNAL_FILE"; then
        rollback_journal_backup "$journal_backup" || true
        rm -f "$journal_backup"
        flock -u 9
        exec 9>&-
        return 1
    fi
    if ! bump_manifest_revision_unlocked; then
        rollback_journal_backup "$journal_backup" || true
        printf '%s\n' "$old_revision" > "$revision_backup" && \
            mv -f "$revision_backup" "$MANIFEST_REVISION_FILE"
        durable_sync_file "$MANIFEST_REVISION_FILE" || true
        rm -f "$journal_backup" "$revision_backup"
        flock -u 9
        exec 9>&-
        return 1
    fi
    rm -f "$journal_backup"
    local journal_size
    journal_size=$(wc -c < "$JOURNAL_FILE" 2>/dev/null || echo 0)
    if (( journal_size > JOURNAL_MAX_BYTES )); then
        compact_manifest_unlocked || { log ERROR "compattazione journal fallita"; flock -u 9; exec 9>&-; return 1; }
    fi
    flock -u 9
    exec 9>&-
    echo "Journal aggiornato: $count record (transazione $txid)."
}

cmd_journal_compact() {
    ensure_journal_files
    if ! command -v flock >/dev/null 2>&1; then
        echo "Compattazione impossibile: flock non disponibile." >&2
        return 1
    fi
    exec 9>>"$JOURNAL_LOCK_FILE"
    flock -x 9 || { exec 9>&-; return 1; }
    recover_pending_transactions_unlocked || { flock -u 9; exec 9>&-; return 1; }
    compact_manifest_unlocked
    local status=$?
    flock -u 9
    exec 9>&-
    return "$status"
}

cmd_manifest_export() {
    ensure_journal_files
    local journal_lines lock_held="false"
    # A shared lock keeps the manifest+journal pair consistent without blocking
    # other readers or rewriting anything. Writers still take the exclusive
    # side, so an export cannot observe half an append.
    if command -v flock >/dev/null 2>&1; then
        # No "2>/dev/null" on the exec line: it would silence this command.
        exec 9>>"$JOURNAL_LOCK_FILE"
        if flock -s 9 2>/dev/null; then
            lock_held="true"
        fi
    fi
    journal_lines=$(wc -l < "$JOURNAL_FILE" 2>/dev/null || echo 0)
    # Rendered into a private file, not a shell variable: an 11 MB string in
    # bash is copied twice and cost seconds of CPU on a small NAS.
    local rendered_file
    rendered_file=$(mktemp "${STATE_DIR}/.manifest-export.$$.XXXXXX" 2>/dev/null) || {
        [[ "$lock_held" == "true" ]] && flock -u 9
        exec 9>&-
        return 1
    }
    if (( journal_lines <= 1 )); then
        cp "$MANIFEST_FILE" "$rendered_file" || {
            rm -f "$rendered_file"
            [[ "$lock_held" == "true" ]] && flock -u 9
            exec 9>&-
            return 1
        }
    elif ! render_manifest_unlocked > "$rendered_file"; then
        rm -f "$rendered_file"
        [[ "$lock_held" == "true" ]] && flock -u 9
        exec 9>&-
        return 1
    fi
    # Release before streaming over SSH: appends must not wait for the network.
    [[ "$lock_held" == "true" ]] && flock -u 9
    exec 9>&-
    cat "$rendered_file"
    rm -f "$rendered_file"
}

# Rendered (manifest + journal) state of the current revision. Stored in a
# global because Bash 4.3 has no namerefs; callers hold the journal lock.
RENDERED_MANIFEST=""
read_rendered_manifest_unlocked() {
    local journal_lines
    journal_lines=$(wc -l < "$JOURNAL_FILE" 2>/dev/null || echo 0)
    if (( journal_lines <= 1 )); then
        RENDERED_MANIFEST=$(<"$MANIFEST_FILE")
    else
        RENDERED_MANIFEST=$(render_manifest_unlocked) || return 1
    fi
    return 0
}

# Print the revision stored in the snapshot, or fail if it is missing/corrupt.
snapshot_revision() {
    [[ -f "$MANIFEST_SNAPSHOT_FILE" ]] || return 1
    local header rev
    IFS= read -r header < "$MANIFEST_SNAPSHOT_FILE" || return 1
    [[ "$header" == "NASBOX_MANIFEST_SNAPSHOT_V1" ]] || return 1
    rev=$(sed -n '2s/^REVISION=//p' "$MANIFEST_SNAPSHOT_FILE")
    [[ "$rev" =~ ^[0-9]{1,18}$ ]] || return 1
    printf '%s\n' "$((10#$rev))"
}

# Replace the snapshot in one atomic step while holding the journal lock.
write_manifest_snapshot_unlocked() {
    local revision="$1" rendered="$2" tmp
    mkdir -p "$STATE_DIR" || return 1
    tmp="${MANIFEST_SNAPSHOT_FILE}.tmp.$$"
    {
        printf 'NASBOX_MANIFEST_SNAPSHOT_V1\n'
        printf 'REVISION=%s\n' "$revision"
        printf '%s\n' "$rendered" | awk 'NR == 1 && $0 == "NASBOX_MANIFEST_V1" { next } { print }'
    } > "$tmp" || { rm -f "$tmp"; return 1; }
    mv -f "$tmp" "$MANIFEST_SNAPSHOT_FILE" || { rm -f "$tmp"; return 1; }
}

# Same snapshot, written from the rendered file instead of a shell variable:
# the row set is already on disk, so only the two header lines are prepended.
write_manifest_snapshot_from_file_unlocked() {
    local revision="$1" source="$2" tmp
    [[ -f "$source" ]] || return 1
    mkdir -p "$STATE_DIR" || return 1
    tmp="${MANIFEST_SNAPSHOT_FILE}.tmp.$$"
    {
        printf 'NASBOX_MANIFEST_SNAPSHOT_V1\n'
        printf 'REVISION=%s\n' "$revision"
        if [[ "$(head -1 "$source")" == "NASBOX_MANIFEST_V1" ]]; then
            tail -n +2 "$source"
        else
            cat "$source"
        fi
    } > "$tmp" || { rm -f "$tmp"; return 1; }
    mv -f "$tmp" "$MANIFEST_SNAPSHOT_FILE" || { rm -f "$tmp"; return 1; }
}

print_empty_manifest_delta() {
    printf 'NASBOX_MANIFEST_DELTA_V1\nBASE_REVISION=%s\nCURRENT_REVISION=%s\nCHANGED=0\nREMOVED=\n' "$1" "$2"
}

# Machine-readable incremental manifest export.
#
# Output:
#   NASBOX_MANIFEST_DELTA_V1
#   BASE_REVISION=<since>
#   CURRENT_REVISION=<rev>
#   [FALLBACK_FULL=1]        <-- only when no snapshot for <since> is available
#   CHANGED=<n>
#   <n rows, byte-identical to the full manifest rows, escaped path included>
#   REMOVED=
#   <removed paths, one per line, same escaping as the manifest path field>
#
# A "state" is encoded exactly as in a full manifest row: a normal file has a
# 64-hex digest, a tombstone has an empty digest and zero size/mtime. Paths
# that disappeared from the current snapshot altogether are listed after the
# REMOVED= marker (the chosen alternative to a synthetic "D" TSV column).
# With FALLBACK_FULL=1 the client must replace its whole state with the rows.
cmd_manifest_export_delta() {
    local requested="${1:-}" current base_rev="" use_snapshot="false"
    local lock_held="false" status=0 changed_count=0 removed_count=0
    local workdir="" changed_file="" removed_file="" counts_file="" base_file="" current_file=""
    local rendered=""

    if [[ ! "$requested" =~ ^[0-9]{1,18}$ ]]; then
        printf 'manifest export delta: since_revision non valido\n' >&2
        return 75
    fi
    requested=$((10#$requested))

    read_manifest_revision || {
        printf 'manifest export delta: manifest.revision non valido\n' >&2
        return 75
    }
    current="$MANIFEST_REVISION"
    if (( requested > current )); then
        printf 'manifest export delta: since_revision %s successivo alla revisione corrente %s\n' "$requested" "$current" >&2
        return 75
    fi
    # Nothing changed: do not touch the journal, the tree or any state file.
    if (( requested == current )); then
        print_empty_manifest_delta "$requested" "$current"
        return 0
    fi

    ensure_journal_files
    if command -v flock >/dev/null 2>&1; then
        exec 9>>"$JOURNAL_LOCK_FILE" 2>/dev/null || true
        if flock -s 9 2>/dev/null; then
            lock_held="true"
        fi
    fi

    if ! read_manifest_revision; then
        printf 'manifest export delta: manifest.revision non valido\n' >&2
        status=75
    else
        current="$MANIFEST_REVISION"
        if (( requested > current )); then
            printf 'manifest export delta: since_revision %s successivo alla revisione corrente %s\n' "$requested" "$current" >&2
            status=75
        elif (( requested == current )); then
            print_empty_manifest_delta "$requested" "$current"
        else
            base_rev=$(snapshot_revision 2>/dev/null) || base_rev=""
            if [[ -n "$base_rev" && "$base_rev" == "$requested" ]]; then
                use_snapshot="true"
            fi

            workdir=$(mktemp -d "${STATE_DIR}/.manifest-delta.$$.XXXXXX" 2>/dev/null) || status=1
            if (( status == 0 )); then
                changed_file="$workdir/changed"
                removed_file="$workdir/removed"
                counts_file="$workdir/counts"
                base_file="$workdir/base"
                current_file="$workdir/current"
                if ! : > "$changed_file" || ! : > "$removed_file" || ! : > "$counts_file"; then
                    status=1
                fi
            fi
            # Stream the rendered manifest into the file the diff reads. Keeping
            # the same 11 MB in a shell variable cost about 5 s of CPU per export
            # on this NAS (bash copies it twice), and a delta is requested on
            # every pass that sees a remote change: it was the real limit of
            # real-time sync, far more than the transfer itself.
            if (( status == 0 )) && ! render_manifest_unlocked > "$current_file"; then
                status=1
            fi
            if (( status == 0 )); then
                if [[ "$use_snapshot" == "true" ]]; then
                    awk 'NR > 2 { print }' "$MANIFEST_SNAPSHOT_FILE" > "$base_file" || status=1
                else
                    : > "$base_file" || status=1
                fi
            fi
            if (( status == 0 )); then
                awk -F '\t' \
                    -v changed_file="$changed_file" \
                    -v removed_file="$removed_file" \
                    -v counts_file="$counts_file" '
                    FILENAME == ARGV[1] {
                        if (NF < 6) next
                        base[$1] = $0
                        base_order[++base_count] = $1
                        next
                    }
                    FILENAME == ARGV[2] {
                        if (NF < 6) next
                        current[$1] = $0
                        if (!($1 in base) || base[$1] != $0) {
                            print $0 > changed_file
                            changed++
                        }
                        next
                    }
                    END {
                        for (i = 1; i <= base_count; i++) {
                            path = base_order[i]
                            if (!(path in current)) {
                                print path > removed_file
                                removed++
                            }
                        }
                        printf "%d %d\n", changed + 0, removed + 0 > counts_file
                    }
                ' "$base_file" "$current_file" > /dev/null || status=1
            fi
            if (( status == 0 )); then
                read -r changed_count removed_count < "$counts_file" || status=1
                [[ "$changed_count" =~ ^[0-9]+$ && "$removed_count" =~ ^[0-9]+$ ]] || status=1
            fi
            if (( status == 0 )); then
                # The snapshot we just used (or the full export we just sent)
                # becomes the base for the next request carrying this revision.
                if [[ "$lock_held" == "true" ]] && ! flock -x 9; then
                    status=1
                fi
                if (( status == 0 )); then
                    write_manifest_snapshot_from_file_unlocked "$current" "$current_file" || status=1
                fi
            fi
            if (( status == 0 )); then
                if [[ "$use_snapshot" == "true" ]]; then
                    printf 'NASBOX_MANIFEST_DELTA_V1\nBASE_REVISION=%s\nCURRENT_REVISION=%s\nCHANGED=%s\n' "$requested" "$current" "$changed_count"
                else
                    printf 'NASBOX_MANIFEST_DELTA_V1\nBASE_REVISION=%s\nCURRENT_REVISION=%s\nFALLBACK_FULL=1\nCHANGED=%s\n' "$requested" "$current" "$changed_count"
                fi
                cat "$changed_file"
                printf 'REMOVED=\n'
                if [[ "$use_snapshot" == "true" ]]; then
                    cat "$removed_file"
                fi
            fi
            [[ -n "$workdir" ]] && rm -rf "$workdir"
        fi
    fi

    [[ "$lock_held" == "true" ]] && flock -u 9
    exec 9>&-
    return "$status"
}

cmd_manifest_get() {
    local path="${1:-}" encoded journal_lines
    [[ -n "$path" && "$path" != /* && "/$path/" != */../* ]] || {
        echo "MANIFEST_INVALID" >&2
        return 1
    }
    encoded="$(encode_journal_field "$path")"
    ensure_journal_files
    local lock_held="false"
    if command -v flock >/dev/null 2>&1; then
        exec 9>>"$JOURNAL_LOCK_FILE" 2>/dev/null || true
        if flock -s 9 2>/dev/null; then
            lock_held="true"
        fi
    fi
    journal_lines=$(wc -l < "$JOURNAL_FILE" 2>/dev/null || echo 0)
    if (( journal_lines <= 1 )); then
        cat "$MANIFEST_FILE"
    else
        render_manifest_unlocked || { [[ "$lock_held" == "true" ]] && flock -u 9; exec 9>&-; return 1; }
    fi | awk -F '\t' -v wanted="$encoded" '
        FNR == 1 && $0 == "NASBOX_MANIFEST_V1" { next }
        $1 == wanted { print "MANIFEST_HIT\t" $0; found = 1; exit }
        END { if (!found) print "MANIFEST_MISS" }
    '
    [[ "$lock_held" == "true" ]] && flock -u 9
    exec 9>&-
}

valid_sync_relative_path() {
    local path="$1" part current remaining
    [[ -n "$path" && "$path" != /* && "/$path/" != */../* ]] || return 1
    case "/$path/" in
        */.sync-trash/*|*/.sync-partial/*|*/.nasbox-staging/*|*/.nasbox-root/*|*/@eaDir/*) return 1 ;;
    esac
    current="$SHARE_ROOT"
    remaining="$path"
    while [[ "$remaining" == */* ]]; do
        part="${remaining%%/*}"
        remaining="${remaining#*/}"
        [[ -n "$part" ]] || continue
        current="$current/$part"
        [[ ! -L "$current" ]] || return 1
    done
    return 0
}

portable_file_size() {
    stat -c '%s' "$1" 2>/dev/null || stat -f '%z' "$1" 2>/dev/null
}

portable_file_mtime() {
    stat -c '%Y' "$1" 2>/dev/null || stat -f '%m' "$1" 2>/dev/null
}

portable_sha256() {
    local output
    output=$(sha256sum "$1" 2>/dev/null) || return 1
    # GNU coreutils prefixes a backslash when the filename itself needs
    # escaping (for example a newline). BusyBox does not. The digest remains
    # the following 64 hexadecimal characters in both formats.
    if [[ "$output" == \\* ]]; then
        output="${output:1}"
    fi
    output="${output:0:64}"
    [[ "$output" =~ ^[0-9a-f]{64}$ ]] || return 1
    printf '%s' "$output"
}

# Emits one path's FILE_STATES_V1 entry (path\0kind\0digest\0size\0mtime_ns\0)
# to stdout; emits nothing on failure. One background job per path, see
# cmd_file_states/FILE_STATES_PARALLELISM. ``manifest_entries`` is populated
# once by cmd_file_states and inherited by these jobs. A matching digest, size
# and mtime is an authoritative unchanged state already committed by NASBox;
# reuse its digest instead of re-reading the entire file on every batch.
_file_state_entry() {
    local path="$1" protocol="${2:-FILE_STATES_V1}" target digest size mtime mtime_ns encoded manifest_line tombstone_ts causal
    target="$SHARE_ROOT/$path"
    encoded=$(encode_journal_field "$path")
    if [[ -f "$target" && ! -L "$target" ]]; then
        size=$(portable_file_size "$target" || true)
        mtime=$(portable_file_mtime "$target" || true)
        [[ "$size" =~ ^[0-9]+$ && "$mtime" =~ ^[0-9]+$ ]] || return 1
        mtime_ns=$((mtime * 1000000000))
        manifest_line="${manifest_entries[$encoded]:-}"
        if [[ -n "$manifest_line" ]]; then
            IFS=$'\t' read -r _manifest_path manifest_digest manifest_size manifest_mtime _manifest_device _manifest_ts causal <<< "$manifest_line"
            if [[ "$manifest_digest" =~ ^[0-9a-f]{64}$ && "$manifest_size" == "$size" && "$manifest_mtime" == "$mtime_ns" ]]; then
                if [[ "$protocol" == "FILE_STATES_V2" ]]; then
                    printf '%s\0FILE\0%s\0%s\0%s\0%s\0' "$path" "$manifest_digest" "$size" "$mtime_ns" "$causal"
                else
                    printf '%s\0FILE\0%s\0%s\0%s\0' "$path" "$manifest_digest" "$size" "$mtime_ns"
                fi
                return 0
            fi
        fi
        # A missing or mismatching manifest record can be an out-of-band NAS
        # modification. Hash it rather than trusting metadata alone.
        digest=$(portable_sha256 "$target" || true)
        [[ "$digest" =~ ^[0-9a-f]{64}$ ]] || return 1
        if [[ "$protocol" == "FILE_STATES_V2" ]]; then
            printf '%s\0FILE\0%s\0%s\0%s\0\0' "$path" "$digest" "$size" "$mtime_ns"
        else
            printf '%s\0FILE\0%s\0%s\0%s\0' "$path" "$digest" "$size" "$mtime_ns"
        fi
    elif [[ -e "$target" || -L "$target" ]]; then
        if [[ "$protocol" == "FILE_STATES_V2" ]]; then
            printf '%s\0%s\0%s\0%s\0%s\0\0' "$path" "OTHER" "" "0" "0"
        else
            printf '%s\0%s\0%s\0%s\0%s\0' "$path" "OTHER" "" "0" "0"
        fi
    else
        manifest_line="${manifest_entries[$encoded]:-}"
        if [[ -n "$manifest_line" ]]; then
            tombstone_ts=$(printf '%s\n' "$manifest_line" | awk -F '\t' '{ print $6 }')
            [[ "$tombstone_ts" =~ ^[0-9]+$ ]] || tombstone_ts=$(date '+%s')
            if [[ "$protocol" == "FILE_STATES_V2" ]]; then
                causal=""
                IFS=$'\t' read -r _manifest_path _manifest_digest _manifest_size _manifest_mtime _manifest_device _manifest_ts causal <<< "$manifest_line"
                printf '%s\0%s\0%s\0%s\0%s\0%s\0' \
                    "$path" "TOMBSTONE" "" "0" "$((tombstone_ts * 1000000000))" "$causal"
            else
                printf '%s\0%s\0%s\0%s\0%s\0' \
                    "$path" "TOMBSTONE" "" "0" "$((tombstone_ts * 1000000000))"
            fi
        else
            if [[ "$protocol" == "FILE_STATES_V2" ]]; then
                printf '%s\0%s\0%s\0%s\0%s\0\0' "$path" "ABSENT" "" "0" "0"
            else
                printf '%s\0%s\0%s\0%s\0%s\0' "$path" "ABSENT" "" "0" "0"
            fi
        fi
    fi
}

cmd_file_states() {
    local compact_first="${1:-true}"
    command -v sha256sum >/dev/null 2>&1 || {
        echo "file-states: sha256sum non disponibile" >&2
        return 1
    }
    local magic count path index encoded manifest_path manifest_line protocol
    local -a paths
    local -A manifest_entries
    IFS= read -r -d '' magic || return 1
    IFS= read -r -d '' count || return 1
    [[ "$magic" == "FILE_STATES_V1" || "$magic" == "FILE_STATES_V2" ]] || return 1
    protocol="$magic"
    [[ "$count" =~ ^[0-9]+$ && "$count" -le 100000 ]] || return 1
    for ((index = 0; index < count; index++)); do
        IFS= read -r -d '' path || return 1
        valid_sync_relative_path "$path" || return 1
        paths[index]="$path"
    done

    if [[ "$compact_first" == "true" ]]; then
        cmd_journal_compact >/dev/null || return 1
    fi

    if (( count == 0 )); then
        printf '%s\0%s\0' "$protocol" "$count"
        return 0
    fi

    # Look up the requested paths in the manifest with a single awk process.
    # The previous pure-bash `while read` walked every manifest line on every
    # batch; on a weak ARM NAS that alone cost ~10s per call and it runs once per
    # sync chunk, so it dominated an active sync. awk (already required by the
    # manifest compaction) does the same lookup in well under a second. Only the
    # requested encoded paths are matched, and the matching lines are read back
    # into the associative array the per-path jobs below already use.
    local want_file match_file
    want_file="$(mktemp "$STATE_DIR/fs-want.XXXXXX")" || return 1
    match_file="$(mktemp "$STATE_DIR/fs-match.XXXXXX")" || { rm -f "$want_file"; return 1; }
    for ((index = 0; index < count; index++)); do
        printf '%s\n' "$(encode_journal_field "${paths[index]}")"
    done > "$want_file"
    if ! awk -F '\t' -v want_file="$want_file" '
        BEGIN { while ((getline wline < want_file) > 0) if (wline != "") wanted[wline] = 1 }
        FNR == 1 && $0 == "NASBOX_MANIFEST_V1" { next }
        ($1 in wanted) { print }
    ' "$MANIFEST_FILE" > "$match_file" 2>/dev/null; then
        rm -f "$want_file" "$match_file"
        return 1
    fi
    while IFS= read -r manifest_line; do
        manifest_path="${manifest_line%%$'\t'*}"
        [[ -n "$manifest_path" ]] && manifest_entries["$manifest_path"]="$manifest_line"
    done < "$match_file"
    rm -f "$want_file" "$match_file"

    # Each path's digest/size/mtime -- a full-content sha256sum for anything
    # that exists -- used to be computed one file after another; on a large
    # batch that single-file-at-a-time subprocess spawn+read+hash was the
    # dominant cost of every sync attempt (observed: well over a minute per
    # ~100 files on real NAS hardware). Running FILE_STATES_PARALLELISM of
    # these concurrently instead cuts that wall-clock time roughly by the
    # same factor. Order doesn't matter to the client (results are keyed by
    # path, see rsync_ops.remote_file_states), so each job writes to its own
    # numbered file and they're streamed out afterwards in whatever order
    # that turns out to be.
    local tmpdir running
    tmpdir="$(mktemp -d)" || return 1
    running=0
    for ((index = 0; index < count; index++)); do
        _file_state_entry "${paths[index]}" "$protocol" > "$tmpdir/$index" 2>/dev/null &
        (( ++running ))
        if (( running >= FILE_STATES_PARALLELISM )); then
            wait
            running=0
        fi
    done
    wait

    for ((index = 0; index < count; index++)); do
        if [[ ! -s "$tmpdir/$index" ]]; then
            rm -rf "$tmpdir"
            return 1
        fi
    done

    printf '%s\0%s\0' "$protocol" "$count"
    for ((index = 0; index < count; index++)); do
        cat "$tmpdir/$index"
    done
    rm -rf "$tmpdir"
}

append_delete_journal_unlocked() {
    local path="$1" device="$2" supplied_txid="${3:-}" server_timestamp txid tmp journal_backup old_revision revision_tmp
    tmp="${JOURNAL_FILE}.delete.$$.$RANDOM"
    journal_backup="${JOURNAL_FILE}.rollback.$$.$RANDOM"
    revision_tmp="${MANIFEST_REVISION_FILE}.rollback.$$.$RANDOM"
    old_revision=$(cat "$MANIFEST_REVISION_FILE" 2>/dev/null || echo 0)
    cp "$JOURNAL_FILE" "$journal_backup" || return 1
    server_timestamp=$(date '+%s')
    txid="${supplied_txid:-delete-${server_timestamp}-$$-${RANDOM}}"
    {
        printf 'BEGIN\t%s\t%s\t%s\n' "$txid" "$server_timestamp" "$device"
        printf 'DELETE\t%s\t\t0\t0\t%s\t%s\n' \
            "$(encode_journal_field "$path")" "$(encode_journal_field "$device")" "$server_timestamp"
        printf 'COMMIT\t%s\n' "$txid"
    } > "$tmp" || { rm -f "$tmp" "$journal_backup"; return 1; }
    if ! cat "$tmp" >> "$JOURNAL_FILE"; then
        rollback_journal_backup "$journal_backup" || true
        rm -f "$tmp" "$journal_backup"
        return 1
    fi
    # The journal record must be durable before the revision is published.
    if ! durable_sync_file "$JOURNAL_FILE"; then
        rollback_journal_backup "$journal_backup" || true
        rm -f "$tmp" "$journal_backup"
        return 1
    fi
    test_failpoint "journal_after_append"
    rm -f "$tmp"
    if bump_manifest_revision_unlocked; then
        rm -f "$journal_backup"
        return 0
    fi
    rollback_journal_backup "$journal_backup" || true
    printf '%s\n' "$old_revision" > "$revision_tmp" && mv -f "$revision_tmp" "$MANIFEST_REVISION_FILE"
    durable_sync_file "$MANIFEST_REVISION_FILE" || true
    rm -f "$journal_backup" "$revision_tmp"
    return 1
}

append_delete_journal_batch_unlocked() {
    local paths_file="$1" device="$2" txid="$3" path server_timestamp tmp count=0
    local journal_backup old_revision revision_tmp
    [[ -f "$paths_file" ]] || return 1
    tmp="${JOURNAL_FILE}.delete-batch.$$.$RANDOM"
    journal_backup="${JOURNAL_FILE}.rollback.$$.$RANDOM"
    revision_tmp="${MANIFEST_REVISION_FILE}.rollback.$$.$RANDOM"
    old_revision=$(cat "$MANIFEST_REVISION_FILE" 2>/dev/null || echo 0)
    cp "$JOURNAL_FILE" "$journal_backup" || return 1
    : > "$tmp" || { rm -f "$tmp" "$journal_backup"; return 1; }
    server_timestamp=$(date '+%s')
    printf 'BEGIN\t%s\t%s\t%s\n' "$txid" "$server_timestamp" "$device" >> "$tmp" || {
        rm -f "$tmp" "$journal_backup"; return 1;
    }
    while IFS= read -r -d '' path; do
        printf 'DELETE\t%s\t\t0\t0\t%s\t%s\n' \
            "$(encode_journal_field "$path")" "$(encode_journal_field "$device")" "$server_timestamp" \
            >> "$tmp" || { rm -f "$tmp" "$journal_backup"; return 1; }
        (( count++ ))
    done < "$paths_file"
    (( count == 0 )) && { rm -f "$tmp" "$journal_backup"; return 0; }
    printf 'COMMIT\t%s\n' "$txid" >> "$tmp" || { rm -f "$tmp" "$journal_backup"; return 1; }
    if ! cat "$tmp" >> "$JOURNAL_FILE"; then
        rollback_journal_backup "$journal_backup" || true
        rm -f "$tmp" "$journal_backup"
        return 1
    fi
    # The journal record must be durable before the revision is published.
    if ! durable_sync_file "$JOURNAL_FILE"; then
        rollback_journal_backup "$journal_backup" || true
        rm -f "$tmp" "$journal_backup"
        return 1
    fi
    rm -f "$tmp"
    if bump_manifest_revision_unlocked; then
        rm -f "$journal_backup"
        return 0
    fi
    rollback_journal_backup "$journal_backup" || true
    printf '%s\n' "$old_revision" > "$revision_tmp" && mv -f "$revision_tmp" "$MANIFEST_REVISION_FILE"
    durable_sync_file "$MANIFEST_REVISION_FILE" || true
    rm -f "$journal_backup" "$revision_tmp"
    return 1
}

append_rename_journal_batch_unlocked() {
    local paths_file="$1" device="$2" txid="$3" src dst digest size mtime
    local journal_backup old_revision revision_tmp tmp count=0
    [[ -f "$paths_file" ]] || return 1
    tmp="${JOURNAL_FILE}.rename-batch.$$.$RANDOM"
    journal_backup="${JOURNAL_FILE}.rollback.$$.$RANDOM"
    revision_tmp="${MANIFEST_REVISION_FILE}.rollback.$$.$RANDOM"
    old_revision=$(cat "$MANIFEST_REVISION_FILE" 2>/dev/null || echo 0)
    cp "$JOURNAL_FILE" "$journal_backup" || return 1
    : > "$tmp" || { rm -f "$journal_backup"; return 1; }
    printf 'BEGIN\t%s\t%s\t%s\n' "$txid" "$(date '+%s')" "$device" >> "$tmp" || {
        rm -f "$tmp" "$journal_backup"; return 1;
    }
    while IFS= read -r -d '' src && IFS= read -r -d '' dst && \
          IFS= read -r -d '' digest && IFS= read -r -d '' size && \
          IFS= read -r -d '' mtime; do
        printf 'DELETE\t%s\t\t0\t0\t%s\t%s\n' \
            "$(encode_journal_field "$src")" "$(encode_journal_field "$device")" "$(date '+%s')" >> "$tmp" || {
            rm -f "$tmp" "$journal_backup"; return 1;
        }
        printf 'PUT\t%s\t%s\t%s\t%s\t%s\t%s\n' \
            "$(encode_journal_field "$dst")" "$digest" "$size" "$mtime" \
            "$(encode_journal_field "$device")" "$(date '+%s')" >> "$tmp" || {
            rm -f "$tmp" "$journal_backup"; return 1;
        }
        (( count++ ))
    done < "$paths_file"
    printf 'COMMIT\t%s\n' "$txid" >> "$tmp" || { rm -f "$tmp" "$journal_backup"; return 1; }
    if ! cat "$tmp" >> "$JOURNAL_FILE"; then
        rollback_journal_backup "$journal_backup" || true
        rm -f "$tmp" "$journal_backup"
        return 1
    fi
    # The journal record must be durable before the revision is published.
    if ! durable_sync_file "$JOURNAL_FILE"; then
        rollback_journal_backup "$journal_backup" || true
        rm -f "$tmp" "$journal_backup"
        return 1
    fi
    rm -f "$tmp"
    if bump_manifest_revision_unlocked; then
        rm -f "$journal_backup"
        return 0
    fi
    rollback_journal_backup "$journal_backup" || true
    printf '%s\n' "$old_revision" > "$revision_tmp" && mv -f "$revision_tmp" "$MANIFEST_REVISION_FILE"
    durable_sync_file "$MANIFEST_REVISION_FILE" || true
    rm -f "$journal_backup" "$revision_tmp"
    return 1
}

# A staged publish is deliberately not an rsync operation: every file is first
# made durable below SHARE_ROOT/.nasbox-staging, then moved into place only when
# its destination does not exist. The transaction marker makes a killed SSH
# command resumable without ever recording a PUT before its move succeeded.
valid_staging_directory() {
    local directory="$1" current part remaining
    [[ "$directory" == "$SHARE_ROOT/$STAGING_DIRNAME/"* && -d "$directory" && ! -L "$directory" ]] || return 1
    current="$SHARE_ROOT/$STAGING_DIRNAME"
    [[ -d "$current" && ! -L "$current" ]] || return 1
    remaining="${directory#"$current/"}"
    while [[ "$remaining" == */* ]]; do
        part="${remaining%%/*}"
        remaining="${remaining#*/}"
        [[ -n "$part" ]] || continue
        current="$current/$part"
        [[ ! -L "$current" ]] || return 1
    done
}

sync_transfer_lock_is_held() {
    exec 8>>"$SYNC_LOCK_FILE" || return 1
    if flock -n 8; then
        flock -u 8
        exec 8>&-
        return 1
    fi
    exec 8>&-
    return 0
}

sync_transfer_lock_owned_by() {
    local expected="$1" owner_id
    sync_transfer_lock_is_held || return 1
    [[ -f "$SYNC_LOCK_OWNER_FILE" ]] || return 1
    IFS='|' read -r owner_id _ < "$SYNC_LOCK_OWNER_FILE" || return 1
    [[ "$owner_id" == "$expected" ]]
}

require_sync_transfer_owner() {
    local device="$1"
    if ! sync_transfer_lock_owned_by "$device"; then
        echo "operazione mutante rifiutata: lease NAS non posseduto dal device $device" >&2
        return 75
    fi
    return 0
}

publish_file_matches() {
    local file="$1" digest="$2" size="$3" mtime_ns="$4" actual_size actual_mtime digest_actual expected_mtime
    [[ -f "$file" && ! -L "$file" ]] || return 1
    actual_size=$(portable_file_size "$file" || true)
    actual_mtime=$(portable_file_mtime "$file" || true)
    [[ "$actual_size" == "$size" && "$actual_mtime" =~ ^[0-9]+$ ]] || return 1
    # Rsync/NAS stat expose whole-second mtimes. Local Python fingerprints may
    # retain sub-second precision, so compare the portable precision here and
    # normalize it before writing the remote manifest below.
    expected_mtime=$((mtime_ns / 1000000000))
    [[ "$actual_mtime" == "$expected_mtime" ]] || return 1
    digest_actual=$(portable_sha256 "$file" || true)
    [[ "$digest_actual" == "$digest" ]]
}

append_put_journal_batch_unlocked() {
    local paths_file="$1" device="$2" txid="$3" protocol="${4:-STAGING_PUBLISH_V1}"
    local path digest size mtime causal server_timestamp tmp count=0
    local journal_backup old_revision revision_tmp
    [[ -f "$paths_file" ]] || return 1
    tmp="${JOURNAL_FILE}.put-batch.$$.$RANDOM"
    journal_backup="${JOURNAL_FILE}.rollback.$$.$RANDOM"
    revision_tmp="${MANIFEST_REVISION_FILE}.rollback.$$.$RANDOM"
    old_revision=$(cat "$MANIFEST_REVISION_FILE" 2>/dev/null || echo 0)
    cp "$JOURNAL_FILE" "$journal_backup" || return 1
    server_timestamp=$(date '+%s')
    : > "$tmp" || { rm -f "$tmp" "$journal_backup"; return 1; }
    printf 'BEGIN\t%s\t%s\t%s\n' "$txid" "$server_timestamp" "$device" >> "$tmp" || { rm -f "$tmp" "$journal_backup"; return 1; }
    while IFS= read -r -d '' path && IFS= read -r -d '' digest && IFS= read -r -d '' size && IFS= read -r -d '' mtime; do
        causal=""
        if [[ "$protocol" == "STAGING_PUBLISH_V2" ]]; then
            IFS= read -r -d '' causal || { rm -f "$tmp" "$journal_backup"; return 1; }
            [[ -z "$causal" || "$causal" =~ ^[A-Za-z0-9._:-]+:[1-9][0-9]*(,[A-Za-z0-9._:-]+:[1-9][0-9]*)*$ ]] || { rm -f "$tmp" "$journal_backup"; return 1; }
            printf 'PUT\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n' \
                "$(encode_journal_field "$path")" "$digest" "$size" "$mtime" \
                "$(encode_journal_field "$device")" "$server_timestamp" "$causal" >> "$tmp" || { rm -f "$tmp" "$journal_backup"; return 1; }
        else
            printf 'PUT\t%s\t%s\t%s\t%s\t%s\t%s\n' \
                "$(encode_journal_field "$path")" "$digest" "$size" "$mtime" \
                "$(encode_journal_field "$device")" "$server_timestamp" >> "$tmp" || { rm -f "$tmp" "$journal_backup"; return 1; }
        fi
        (( count++ ))
    done < "$paths_file"
    (( count > 0 )) || { rm -f "$tmp" "$journal_backup"; return 1; }
    printf 'COMMIT\t%s\n' "$txid" >> "$tmp" || { rm -f "$tmp" "$journal_backup"; return 1; }
    # One append, one durable flush, then the revision that advertises it.
    if ! cat "$tmp" >> "$JOURNAL_FILE"; then
        rollback_journal_backup "$journal_backup" || true
        rm -f "$tmp" "$journal_backup"
        return 1
    fi
    rm -f "$tmp"
    if ! durable_sync_file "$JOURNAL_FILE"; then
        rollback_journal_backup "$journal_backup" || true
        rm -f "$journal_backup"
        return 1
    fi
    test_failpoint "publish_after_journal_append"
    if bump_manifest_revision_unlocked; then
        rm -f "$journal_backup"
        return 0
    fi
    rollback_journal_backup "$journal_backup" || true
    printf '%s\n' "$old_revision" > "$revision_tmp" && mv -f "$revision_tmp" "$MANIFEST_REVISION_FILE"
    durable_sync_file "$MANIFEST_REVISION_FILE" || true
    rm -f "$journal_backup" "$revision_tmp"
    return 1
}

recover_publish_transaction_unlocked() {
    local txfile="$1" device="$2" staging_dir="$3" paths_file="$4" txid="$5" protocol="${6:-STAGING_PUBLISH_V1}"
    local path digest size mtime causal staged target
    valid_staging_directory "$staging_dir" || { log ERROR "recovery publish staging non valido: $txid"; return 1; }
    while IFS= read -r -d '' path && IFS= read -r -d '' digest && IFS= read -r -d '' size && IFS= read -r -d '' mtime; do
        causal=""
        if [[ "$protocol" == "STAGING_PUBLISH_V2" ]]; then
            IFS= read -r -d '' causal || return 1
        fi
        valid_sync_relative_path "$path" || { log ERROR "recovery publish percorso non valido: $txid"; return 1; }
        staged="$staging_dir/$path"
        target="$SHARE_ROOT/$path"
        if [[ -e "$target" || -L "$target" ]]; then
            # A target with the expected bytes is a completed prior move. A
            # different target is never overwritten: keep the staged source.
            if [[ -e "$staged" || -L "$staged" ]] || publish_file_matches "$target" "$digest" "$size" "$mtime"; then
                [[ ! -e "$staged" && ! -L "$staged" ]] || { log ERROR "recovery publish conflitto: $path"; return 1; }
                continue
            fi
            log ERROR "recovery publish target non verificabile: $path"
            return 1
        fi
        publish_file_matches "$staged" "$digest" "$size" "$mtime" || { log ERROR "recovery publish staging non verificabile: $path"; return 1; }
        mkdir -p "${target%/*}" || return 1
        mv "$staged" "$target" || return 1
        test_failpoint "publish_after_move"
    done < "$paths_file"
    # Persist the completed move set once, before exposing the journal commit.
    # Calling sync for every one of a 3,000-file batch serializes the NAS disk
    # and makes the short publish phase indistinguishable from a stalled sync.
    sync
    advance_move_transaction "$txfile" MOVED || return 1
    append_put_journal_batch_unlocked "$paths_file" "$device" "$txid" "$protocol"
}

cmd_staging_publish() {
    command -v flock >/dev/null 2>&1 || { echo "staging-publish: flock non disponibile" >&2; return 1; }
    command -v sha256sum >/dev/null 2>&1 || { echo "staging-publish: sha256sum non disponibile" >&2; return 1; }
    local magic staging_dir txid device count path digest size mtime causal normalized_mtime index paths_file txfile target
    local existing_targets=0 mismatched_targets=0
    local -A seen_paths expected_digests expected_sizes expected_mtimes
    IFS= read -r -d '' magic || return 1
    IFS= read -r -d '' staging_dir || return 1
    IFS= read -r -d '' txid || return 1
    IFS= read -r -d '' device || return 1
    IFS= read -r -d '' count || return 1
    [[ "$magic" == "STAGING_PUBLISH_V1" || "$magic" == "STAGING_PUBLISH_V2" ]] || return 1
    valid_staging_directory "$staging_dir" || return 1
    [[ "$txid" =~ ^[A-Za-z0-9._:-]{1,128}$ && "$device" =~ ^[A-Za-z0-9._:-]{1,128}$ ]] || return 1
    [[ "$count" =~ ^[1-9][0-9]*$ && "$count" -le 100000 ]] || return 1
    paths_file="$TRANSACTION_DIR/$txid.paths"
    txfile="$TRANSACTION_DIR/$txid.txn"
    mkdir -p "$TRANSACTION_DIR" || return 1
    [[ ! -e "$txfile" && ! -e "$paths_file" ]] || return 1
    : > "$paths_file" || return 1
    for ((index = 0; index < count; index++)); do
        IFS= read -r -d '' path && IFS= read -r -d '' digest && IFS= read -r -d '' size && IFS= read -r -d '' mtime || { rm -f "$paths_file"; return 1; }
        causal=""
        if [[ "$magic" == "STAGING_PUBLISH_V2" ]]; then
            IFS= read -r -d '' causal || { rm -f "$paths_file"; return 1; }
            [[ -z "$causal" || "$causal" =~ ^[A-Za-z0-9._:-]+:[1-9][0-9]*(,[A-Za-z0-9._:-]+:[1-9][0-9]*)*$ ]] || { rm -f "$paths_file"; return 1; }
        fi
        valid_sync_relative_path "$path" && [[ -z "${seen_paths[$path]:-}" ]] || { rm -f "$paths_file"; return 1; }
        [[ "$digest" =~ ^[0-9a-f]{64}$ && "$size" =~ ^[0-9]+$ && "$mtime" =~ ^[0-9]+$ ]] || { rm -f "$paths_file"; return 1; }
        publish_file_matches "$staging_dir/$path" "$digest" "$size" "$mtime" || { rm -f "$paths_file"; return 1; }
        normalized_mtime=$(portable_file_mtime "$staging_dir/$path" || true)
        [[ "$normalized_mtime" =~ ^[0-9]+$ ]] || { rm -f "$paths_file"; return 1; }
        seen_paths["$path"]=1
        expected_digests["$path"]="$digest"
        expected_sizes["$path"]="$size"
        expected_mtimes["$path"]="$mtime"
        printf '%s\0%s\0%s\0%s\0' "$path" "$digest" "$size" "$((normalized_mtime * 1000000000))" >> "$paths_file" || { rm -f "$paths_file"; return 1; }
        if [[ "$magic" == "STAGING_PUBLISH_V2" ]]; then
            printf '%s\0' "$causal" >> "$paths_file" || { rm -f "$paths_file"; return 1; }
        fi
    done
    # The caller's persistent SSH lease owns this lock. Refuse standalone use,
    # but do not acquire it here: doing so would deadlock a valid lease.
    if ! require_sync_transfer_owner "$device"; then
        rm -f "$paths_file"
        echo "staging-publish: lease NAS non posseduto dal device" >&2
        return 75
    fi
    exec 9>>"$JOURNAL_LOCK_FILE"
    flock -x 9 || { exec 9>&-; rm -f "$paths_file"; return 1; }
    ensure_journal_files
    recover_pending_transactions_unlocked || { flock -u 9; exec 9>&-; rm -f "$paths_file"; return 1; }
    # Check the full batch before exposing any file, so an ordinary collision
    # leaves every staged file untouched. If every target already contains the
    # expected bytes, another client won the race and this publish is complete
    # from the filesystem's point of view; discard only this now-redundant
    # private staging tree instead of reporting a permanently pending batch.
    for path in "${!seen_paths[@]}"; do
        target="$SHARE_ROOT/$path"
        if [[ -e "$target" || -L "$target" ]]; then
            (( existing_targets++ ))
            publish_file_matches "$target" "${expected_digests[$path]}" \
                "${expected_sizes[$path]}" "${expected_mtimes[$path]}" || (( mismatched_targets++ ))
        fi
    done
    if (( mismatched_targets > 0 || (existing_targets > 0 && existing_targets < count) )); then
        echo "staging-publish: destinazione gia' esistente e non equivalente" >&2
        flock -u 9
        exec 9>&-
        rm -f "$paths_file"
        return 1
    fi
    if (( existing_targets == count )); then
        if staging_is_referenced "$staging_dir" || ! rm -rf -- "$staging_dir"; then
            flock -u 9
            exec 9>&-
            rm -f "$paths_file"
            return 1
        fi
        rm -f "$paths_file"
        flock -u 9
        exec 9>&-
        printf '%s\0OK\0%s\0' "$magic" "$count"
        return 0
    fi
    begin_move_transaction PUBLISH "$txid" "$device" "$(date '+%s')" "" "$staging_dir" "" "$paths_file" "$magic" || { flock -u 9; exec 9>&-; rm -f "$paths_file"; return 1; }
    sync
    test_failpoint "publish_after_marker"
    if recover_publish_transaction_unlocked "$txfile" "$device" "$staging_dir" "$paths_file" "$txid" "$magic"; then
        rm -f "$txfile" "$paths_file"
        printf '%s\0OK\0%s\0' "$magic" "$count"
    else
        # Leave marker and staged files in place; a later locked command will
        # finish after a transient journal or filesystem failure.
        flock -u 9; exec 9>&-
        return 1
    fi
    flock -u 9
    exec 9>&-
}

# Folders left empty by a mutation go too. Clients never create directories on
# the NAS: a directory only exists there as the parent of a file, so an empty one
# is what a deletion left behind (a folder removed on a PC used to leave its
# shell in the share). rmdir refuses a folder that still holds anything -- also
# one another PC just filled -- and every caller already holds the NAS lease, so
# this cannot race a transfer. Ascending stops at the first folder that is not
# empty, and never enters the trash or the staging tree.
prune_empty_dirs_unlocked() {
    local path dir
    for path in "$@"; do
        [[ "$path" == */* ]] || continue
        dir="${path%/*}"
        while [[ -n "$dir" ]]; do
            case "$dir" in
                .|"$TRASH_DIRNAME"|"$TRASH_DIRNAME"/*|"$STAGING_DIRNAME"|"$STAGING_DIRNAME"/*) break ;;
            esac
            rmdir "$SHARE_ROOT/$dir" 2>/dev/null || break
            [[ "$dir" == */* ]] || break
            dir="${dir%/*}"
        done
    done
    return 0
}

# A file can reach the share without a journal transaction: restored from a
# backup, copied by hand, written before a crash that lost the append. The
# manifest is written only by the journal, so such a file stays invisible -- no
# client downloads it, no deletion or version is tracked, and a client starting
# from scratch never sees it.
#
# --audit-orphans lists them (read-only); --audit-adopt journals them with the
# digest and metadata they have on disk, so they become ordinary tracked files.
# Adoption moves no bytes: it only writes the transaction that should have
# happened when the file arrived.
audit_path_ignored() {
    local rel="$1" base part
    local IFS=/
    # Any component, at any depth: a nested ".sync-trash" (a history folder that
    # was itself synced once) is exactly as internal as one at the root, and the
    # clients refuse such paths -- journaling them breaks every client's export,
    # not just this file.
    for part in $rel; do
        case "$part" in
            "$TRASH_DIRNAME"|"$STAGING_DIRNAME"|.sync-partial|.nasbox-client-update| \
            .nasbox-client-update-go|@eaDir|"$REPOSITORY_MARKER_NAME")
                return 0 ;;
        esac
    done
    unset IFS
    # rsync stages new content in ".<name>.<6 random chars>" next to the target;
    # adopting one of those would journal bytes that are about to vanish.
    base="${rel##*/}"
    [[ "$base" =~ ^\..+\.[A-Za-z0-9]{6}$ ]] && return 0
    return 1
}

decode_journal_field() {
    local value="$1"
    value="${value//%09/$'\t'}"
    value="${value//%0A/$'\n'}"
    value="${value//%0D/$'\r'}"
    value="${value//%25/%}"
    printf '%s' "$value"
}

# Encoded paths on disk that no live manifest row describes, one per line.
# Everything stays encoded while comparing: encoding is what keeps a filename
# with a newline a single line, and the manifest is stored encoded too.
audit_orphan_encoded_list() {
    local encoded_disk="$1" encoded_live="$2" orphans="$3"
    render_manifest_unlocked 2>/dev/null \
        | awk -F '\t' 'NR > 1 && length($2) == 64 { print $1 }' \
        | sort > "$encoded_live" || return 1
    : > "$encoded_disk" || return 1
    local abs rel
    while IFS= read -r -d '' abs; do
        rel="${abs#"$SHARE_ROOT"/}"
        audit_path_ignored "$rel" && continue
        encode_journal_field "$rel"
        printf '\n'
    done < <(find "$SHARE_ROOT" -type f -print0 2>/dev/null) > "$encoded_disk" || return 1
    sort -o "$encoded_disk" "$encoded_disk"
    # FILENAME, not NR == FNR: with an empty live list (a fresh repository) NR
    # equals FNR on the first line of the second file and that line was lost.
    awk 'FILENAME == ARGV[1] { live[$0] = 1; next } !($0 in live) { print }' \
        "$encoded_live" "$encoded_disk" > "$orphans"
    return 0
}

# Same checks as valid_sync_relative_path, but internal folders are allowed:
# forgetting a path that the clients refuse is exactly what this is for. It still
# refuses absolute paths, ".." and an ancestor that is a symlink.
valid_forget_path() {
    local path="$1" part current remaining
    [[ -n "$path" && "$path" != /* && "/$path/" != */../* ]] || return 1
    current="$SHARE_ROOT"
    remaining="$path"
    while [[ "$remaining" == */* ]]; do
        part="${remaining%%/*}"
        remaining="${remaining#*/}"
        [[ -n "$part" ]] || continue
        current="$current/$part"
        [[ ! -L "$current" ]] || return 1
    done
    return 0
}

# Tombstone rows the manifest should never have held (a path a client refuses,
# a folder that is history and not repository content). Only the journal is
# written: nothing on disk moves, so a version in .sync-trash is untouched.
cmd_journal_forget() {
    local tmpdir records count=0 path run_ts txid
    tmpdir=$(mktemp -d "${STATE_DIR}/.journal-forget.XXXXXX") || return 1
    records="$tmpdir/paths"
    : > "$records" || { rm -rf "$tmpdir"; return 1; }
    while IFS= read -r -d '' path; do
        valid_forget_path "$path" || continue
        printf '%s\0' "$path" >> "$records" || { rm -rf "$tmpdir"; return 1; }
        (( count++ ))
    done
    if (( count == 0 )); then
        rm -rf "$tmpdir"
        printf 'FORGOTTEN=0\n'
        return 0
    fi
    run_ts=$(date -u '+%Y-%m-%d--%H-%M-%S-000000Z')
    txid="journal-forget-${run_ts}-$$"
    exec 9>>"$JOURNAL_LOCK_FILE"
    flock -x 9 || { exec 9>&-; rm -rf "$tmpdir"; return 1; }
    ensure_journal_files
    if ! append_delete_journal_batch_unlocked "$records" "maintenance" "$txid"; then
        flock -u 9; exec 9>&-; rm -rf "$tmpdir"
        printf 'FORGOTTEN=0\nERROR=journal non aggiornato\n'
        return 1
    fi
    flock -u 9; exec 9>&-
    rm -rf "$tmpdir"
    printf 'FORGOTTEN=%s\n' "$count"
    return 0
}

cmd_audit_orphans() {
    local list="${1:-}" tmpdir encoded_live encoded_disk orphans count encoded raw
    tmpdir=$(mktemp -d "${STATE_DIR}/.audit-orphans.XXXXXX") || return 1
    encoded_live="$tmpdir/live"
    encoded_disk="$tmpdir/disk"
    orphans="$tmpdir/orphans"
    if command -v flock >/dev/null 2>&1; then
        # Note: no "2>/dev/null" on an exec line -- it would silence the whole
        # shell for the rest of the command, hiding every later error.
        exec 9>>"$JOURNAL_LOCK_FILE"
        flock -s 9 2>/dev/null || true
    fi
    if ! audit_orphan_encoded_list "$encoded_disk" "$encoded_live" "$orphans"; then
        rm -rf "$tmpdir"; exec 9>&- 2>/dev/null
        printf 'audit-orphans: scansione fallita\n' >&2
        return 1
    fi
    if command -v flock >/dev/null 2>&1; then
        flock -u 9 2>/dev/null || true
        exec 9>&-
    fi
    count=$(wc -l < "$orphans" 2>/dev/null || echo 0)
    # Header terminated by NUL, then one NUL-terminated path each: the same
    # streaming shape as the other batch commands (a path may contain spaces).
    printf 'ORPHANS_V1\nCOUNT=%s\n\0' "${count//[[:space:]]/}"
    if [[ "$list" == "--list" ]]; then
        while IFS= read -r encoded; do
            [[ -n "$encoded" ]] || continue
            decode_journal_field "$encoded"
            printf '\0'
        done < "$orphans"
        printf '\0'
    fi
    rm -rf "$tmpdir"
    return 0
}

cmd_audit_adopt() {
    local tmpdir encoded_live encoded_disk orphans encoded raw digest size mtime count=0
    local record_file txid run_ts
    tmpdir=$(mktemp -d "${STATE_DIR}/.audit-adopt.XXXXXX") || return 1
    encoded_live="$tmpdir/live"
    encoded_disk="$tmpdir/disk"
    orphans="$tmpdir/orphans"
    record_file="$tmpdir/records"
    if command -v flock >/dev/null 2>&1; then
        # Note: no "2>/dev/null" on an exec line -- it would silence the whole
        # shell for the rest of the command, hiding every later error.
        exec 9>>"$JOURNAL_LOCK_FILE"
        flock -s 9 2>/dev/null || true
    fi
    if ! audit_orphan_encoded_list "$encoded_disk" "$encoded_live" "$orphans"; then
        rm -rf "$tmpdir"; exec 9>&- 2>/dev/null
        printf 'audit-adopt: scansione fallita\n' >&2
        return 1
    fi
    if command -v flock >/dev/null 2>&1; then
        flock -u 9 2>/dev/null || true
        exec 9>&-
    fi
    : > "$record_file" || { rm -rf "$tmpdir"; return 1; }
    local now
    now=$(date '+%s')
    while IFS= read -r encoded; do
        [[ -n "$encoded" ]] || continue
        raw=$(decode_journal_field "$encoded")
        # A file written in the last minutes may still be an in-flight transfer:
        # leave it for the next run instead of journaling half-written bytes.
        mtime=$(portable_file_mtime "$SHARE_ROOT/$raw" || true)
        if [[ "$mtime" =~ ^[0-9]+$ ]] && (( now - mtime < 120 )); then
            continue
        fi
        size=$(portable_file_size "$SHARE_ROOT/$raw" || true)
        digest=$(portable_sha256 "$SHARE_ROOT/$raw" || true)
        [[ "$size" =~ ^[0-9]+$ && "$digest" =~ ^[0-9a-f]{64}$ ]] || continue
        printf '%s\0%s\0%s\0%s\0' "$raw" "$digest" "$size" "$mtime" >> "$record_file" || {
            rm -rf "$tmpdir"; return 1; }
        (( count++ ))
    done < "$orphans"
    if (( count == 0 )); then
        rm -rf "$tmpdir"
        printf 'ADOPTED=0\n'
        return 0
    fi
    if [[ "$DRY_RUN" == "true" ]]; then
        rm -rf "$tmpdir"
        printf 'ADOPTED=0\nWOULD_ADOPT=%s\n' "$count"
        return 0
    fi
    run_ts=$(date -u '+%Y-%m-%d--%H-%M-%S-%6NZ' 2>/dev/null || date -u '+%Y-%m-%d--%H-%M-%S-000000Z')
    txid="audit-adopt-${run_ts}-$$"
    exec 9>>"$JOURNAL_LOCK_FILE"
    flock -x 9 || { exec 9>&-; rm -rf "$tmpdir"; return 1; }
    ensure_journal_files
    if ! append_put_journal_batch_unlocked "$record_file" "audit" "$txid"; then
        flock -u 9; exec 9>&-; rm -rf "$tmpdir"
        printf 'ADOPTED=0\nERROR=journal non aggiornato\n'
        return 1
    fi
    flock -u 9; exec 9>&-
    rm -rf "$tmpdir"
    printf 'ADOPTED=%s\n' "$count"
    return 0
}

cmd_checked_delete() {
    command -v sha256sum >/dev/null 2>&1 || {
        echo "checked-delete: sha256sum non disponibile" >&2
        return 1
    }
    command -v flock >/dev/null 2>&1 || {
        echo "checked-delete: flock non disponibile" >&2
        return 1
    }
    local magic run_ts device count path expected_digest expected_mtime index target digest mtime backup txid txfile
    local -a paths digests mtimes
    IFS= read -r -d '' magic || return 1
    IFS= read -r -d '' run_ts || return 1
    IFS= read -r -d '' device || return 1
    IFS= read -r -d '' count || return 1
    [[ "$magic" == "CHECKED_DELETE_V1" ]] || return 1
    [[ "$run_ts" =~ ^[0-9]{4}-[0-9]{2}-[0-9]{2}--[0-9]{2}-[0-9]{2}-[0-9]{2}-[0-9]{6}Z$ ]] || return 1
    [[ "$device" =~ ^[A-Za-z0-9._:-]{1,128}$ ]] || return 1
    [[ "$count" =~ ^[0-9]+$ && "$count" -le 100000 ]] || return 1
    for ((index = 0; index < count; index++)); do
        IFS= read -r -d '' path || return 1
        IFS= read -r -d '' expected_digest || return 1
        IFS= read -r -d '' expected_mtime || return 1
        valid_sync_relative_path "$path" || return 1
        [[ "$expected_digest" =~ ^[0-9a-f]{64}$ && "$expected_mtime" =~ ^[0-9]+$ ]] || return 1
        paths[index]="$path"
        digests[index]="$expected_digest"
        mtimes[index]="$expected_mtime"
    done

    require_sync_transfer_owner "$device" || return $?

    exec 9>>"$JOURNAL_LOCK_FILE"
    flock -x 9 || { exec 9>&-; return 1; }
    ensure_journal_files
    recover_pending_transactions_unlocked || { flock -u 9; exec 9>&-; return 1; }
    # One journal append and one revision bump for the whole request instead of
    # one of each per file: on a small NAS the two fsyncs per file were the
    # cost (a few files per second). Every file is still moved under its own
    # transaction record, and a crash before the journal commit is recovered per
    # file by recover_pending_transactions_unlocked, exactly as before.
    local -a status_of=() backup_of=() txfile_of=()
    local -a emptied=()
    local batch_paths batch_txid last_backup_dir=""
    batch_paths="$TRANSACTION_DIR/checked-delete-batch-${run_ts}-$$-${RANDOM}.paths"
    mkdir -p "$TRANSACTION_DIR" || { flock -u 9; exec 9>&-; return 1; }
    : > "$batch_paths" || { flock -u 9; exec 9>&-; return 1; }
    for ((index = 0; index < count; index++)); do
        path="${paths[index]}"
        target="$SHARE_ROOT/$path"
        if [[ ! -e "$target" && ! -L "$target" ]]; then
            printf '%s\0' "$path" >> "$batch_paths"
            status_of[index]=ABSENT_PENDING
            continue
        fi
        if [[ ! -f "$target" || -L "$target" ]]; then
            status_of[index]=ERROR
            continue
        fi
        digest=$(portable_sha256 "$target" || true)
        mtime=$(portable_file_mtime "$target" || true)
        if [[ "$digest" != "${digests[index]}" || "$mtime" != "${mtimes[index]}" ]]; then
            status_of[index]=STALE
            continue
        fi
        backup="$SHARE_ROOT/$TRASH_DIRNAME/$path-$run_ts"
        # mkdir only when the folder changes (files of one folder come together).
        if [[ "${backup%/*}" != "$last_backup_dir" ]]; then
            mkdir -p "${backup%/*}" || { status_of[index]=ERROR; continue; }
            last_backup_dir="${backup%/*}"
        fi
        txid="delete-move-${run_ts}-${index}-${RANDOM}"
        txfile="$TRANSACTION_DIR/$txid.txn"
        # Same record begin_move_transaction writes; the directory exists already.
        if ! write_move_transaction "$txfile" DELETE "$txid" STARTED \
                "$device" "$run_ts" "$path" "$target" "$backup" "" STAGING_PUBLISH_V1; then
            status_of[index]=ERROR
            continue
        fi
        if mv "$target" "$backup"; then
            test_failpoint "checked_delete_after_move"
            # No MOVED rewrite of the transaction record: recovery of a DELETE
            # decides from what exists on disk (target gone and backup present
            # means "moved, journal it"), never from the recorded stage.
            printf '%s\0' "$path" >> "$batch_paths"
            status_of[index]=DELETED_PENDING
            backup_of[index]="$backup"
            txfile_of[index]="$txfile"
        else
            rm -f "$txfile"
            status_of[index]=ERROR
        fi
    done
    if [[ -s "$batch_paths" ]]; then
        batch_txid="delete-batch-${run_ts}-$$-${RANDOM}"
        if append_delete_journal_batch_unlocked "$batch_paths" "$device" "$batch_txid"; then
            test_failpoint "checked_delete_after_journal"
            for ((index = 0; index < count; index++)); do
                case "${status_of[index]:-}" in
                    DELETED_PENDING)
                        rm -f "${txfile_of[index]}"; status_of[index]=DELETED
                        emptied+=("${paths[index]}") ;;
                    ABSENT_PENDING)
                        status_of[index]=ABSENT
                        emptied+=("${paths[index]}") ;;
                esac
            done
        else
            # The journal refused the batch: put every moved file back, so the
            # NAS and the manifest stay consistent, and report an error.
            for ((index = 0; index < count; index++)); do
                case "${status_of[index]:-}" in
                    DELETED_PENDING)
                        mv "${backup_of[index]}" "$SHARE_ROOT/${paths[index]}" 2>/dev/null || true
                        rm -f "${txfile_of[index]}"
                        status_of[index]=ERROR ;;
                    ABSENT_PENDING) status_of[index]=ERROR ;;
                esac
            done
        fi
    fi
    rm -f "$batch_paths"
    if [[ "${#emptied[@]}" -gt 0 ]]; then
        prune_empty_dirs_unlocked "${emptied[@]}"
    fi
    printf 'CHECKED_DELETE_V1\0%s\0' "$count"
    for ((index = 0; index < count; index++)); do
        printf '%s\0%s\0' "${paths[index]}" "${status_of[index]:-ERROR}"
    done
    flock -u 9
    exec 9>&-
}

# True when the rendered manifest already describes exactly these bytes. Restore
# uses it to recognise a previously completed attempt and stay idempotent instead
# of appending a second PUT for the same path.
manifest_path_matches() {
    local snapshot="$1" path="$2" digest="$3" size="$4" mtime_ns="$5" encoded
    encoded="$(encode_journal_field "$path")"
    awk -F '\t' -v wanted="$encoded" -v d="$digest" -v s="$size" -v m="$mtime_ns" '
        $1 == wanted && $2 == d && $3 == s && $4 == m { found = 1; exit }
        END { exit(found ? 0 : 1) }
    ' "$snapshot"
}

# Emit "<digest>\t<size>\t<mtime_ns>" for a live manifest entry, or fail for a
# missing/tombstoned one. Lets a repeated restore recognise its own target even
# after the trash version has been moved away.
manifest_path_fields() {
    local snapshot="$1" path="$2" encoded
    encoded="$(encode_journal_field "$path")"
    awk -F '\t' -v wanted="$encoded" '
        $1 == wanted && length($2) == 64 && $2 ~ /^[0-9a-f]+$/ {
            print $2 "\t" $3 "\t" $4; found = 1; exit
        }
        END { exit(found ? 0 : 1) }
    ' "$snapshot"
}

# Restore previously deleted files from .sync-trash. The client sends a small
# NUL-delimited batch; this verb only streams results and appends a PUT entry to
# the journal per restored file. No graph, no daemon, no in-memory index: the
# same streaming shape as --checked-delete, suitable for the 1 GB ARM NAS.
#
# Each file is a transaction like checked-delete: a STARTED marker is flushed to
# disk before the mv, the marker advances to MOVED afterwards, and the PUT is
# only appended once the move is durable. A crash at any point is completed (or
# discarded, when nothing moved) by recover_pending_transactions_unlocked at the
# start of the next mutating command.
cmd_restore_batch() {
    local magic device count index path version search_dir prefix candidate newest
    local target backup digest size mtime mtime_ns version_fields txid txfile paths_file moved_here manifest_snapshot
    local -a paths versions
    IFS= read -r -d '' magic || return 1
    IFS= read -r -d '' device || return 1
    IFS= read -r -d '' count || return 1
    [[ "$magic" == "RESTORE_BATCH_V1" ]] || return 1
    [[ "$device" =~ ^[A-Za-z0-9._:-]{1,128}$ ]] || return 1
    [[ "$count" =~ ^[0-9]+$ && "$count" -le 1000 ]] || return 1
    for ((index = 0; index < count; index++)); do
        IFS= read -r -d '' path || return 1
        IFS= read -r -d '' version || return 1
        valid_sync_relative_path "$path" || return 1
        [[ "$version" == "latest" || "$version" =~ ^[0-9]{4}-[0-9]{2}-[0-9]{2}--[0-9]{2}-[0-9]{2}-[0-9]{2}-[0-9]{6}Z$ ]] || return 1
        paths[index]="$path"
        versions[index]="$version"
    done

    require_sync_transfer_owner "$device" || return $?
    exec 9>>"$JOURNAL_LOCK_FILE"
    flock -x 9 || { exec 9>&-; return 1; }
    ensure_journal_files
    recover_pending_transactions_unlocked || { flock -u 9; exec 9>&-; return 1; }
    manifest_snapshot="${STATE_DIR}/restore-manifest.$$"
    if ! render_manifest_unlocked > "$manifest_snapshot"; then
        rm -f "$manifest_snapshot"
        flock -u 9
        exec 9>&-
        return 1
    fi
    printf 'RESTORE_BATCH_V1\0%s\0' "$count"
    local trash="$SHARE_ROOT/$TRASH_DIRNAME"
    for ((index = 0; index < count; index++)); do
        path="${paths[index]}"
        version="${versions[index]}"
        prefix="${path##*/}-"
        if [[ "${path%/*}" == "$path" ]]; then
            search_dir="$trash"
        else
            search_dir="$trash/${path%/*}"
        fi
        newest=""
        if [[ -d "$search_dir" ]]; then
            while IFS= read -r -d '' candidate; do
                [[ "${candidate##*/}" == "$prefix"* ]] || continue
                if [[ "$version" != "latest" && "${candidate##*/}" != "$prefix$version" ]]; then
                    continue
                fi
                if [[ -z "$newest" || "${candidate##*/}" > "${newest##*/}" ]]; then
                    newest="$candidate"
                fi
            done < <(find "$search_dir" -maxdepth 1 -type f -print0 2>/dev/null)
        fi
        target="$SHARE_ROOT/$path"
        if [[ -z "$newest" ]]; then
            # No trash version left: a repeated restore can still succeed when
            # the manifest already describes the bytes present at the target.
            if version_fields=$(manifest_path_fields "$manifest_snapshot" "$path") && \
               IFS=$'\t' read -r digest size mtime <<< "$version_fields" && \
               [[ "$digest" =~ ^[0-9a-f]{64}$ && "$size" =~ ^[0-9]+$ && "$mtime" =~ ^[0-9]+$ ]] && \
               publish_file_matches "$target" "$digest" "$size" "$mtime"; then
                printf '%s\0RESTORED\0' "$path"
            else
                printf '%s\0NO_VERSION\0' "$path"
            fi
            continue
        fi
        # Fingerprint the trash version before any move. Those bytes also
        # identify an already-completed restore of the same version.
        digest=$(portable_sha256 "$newest" || true)
        size=$(portable_file_size "$newest" || true)
        mtime=$(portable_file_mtime "$newest" || true)
        if [[ ! "$digest" =~ ^[0-9a-f]{64}$ || ! "$size" =~ ^[0-9]+$ || ! "$mtime" =~ ^[0-9]+$ ]]; then
            printf '%s\0ERROR\0' "$path"
            continue
        fi
        mtime_ns=$((mtime * 1000000000))
        if manifest_path_matches "$manifest_snapshot" "$path" "$digest" "$size" "$mtime_ns" && \
           publish_file_matches "$target" "$digest" "$size" "$mtime_ns"; then
            # Already restored and already journaled: no second PUT, no marker.
            printf '%s\0RESTORED\0' "$path"
            continue
        fi
        txid="restore-$(date +%s)-${index}-${RANDOM}"
        txfile="$TRANSACTION_DIR/$txid.txn"
        paths_file="$TRANSACTION_DIR/$txid.paths"
        if [[ -e "$txfile" || -e "$paths_file" ]]; then
            printf '%s\0ERROR\0' "$path"
            continue
        fi
        if ! printf '%s\0%s\0%s\0%s\0' "$path" "$digest" "$size" "$mtime_ns" > "$paths_file"; then
            printf '%s\0ERROR\0' "$path"
            continue
        fi
        moved_here="false"
        backup="$newest"
        if [[ -e "$target" || -L "$target" ]]; then
            if ! publish_file_matches "$target" "$digest" "$size" "$mtime_ns"; then
                rm -f "$paths_file"
                printf '%s\0EXISTS\0' "$path"
                continue
            fi
            # Same bytes already in place (a previous attempt moved them but did
            # not journal): finish the journal side, keep the trash version.
        else
            mkdir -p "${target%/*}" || { rm -f "$paths_file"; printf '%s\0ERROR\0' "$path"; continue; }
        fi
        begin_move_transaction RESTORE "$txid" "$device" "$(date '+%s')" \
            "$path" "$target" "$backup" "$paths_file" "STAGING_PUBLISH_V1" || {
            rm -f "$paths_file"
            printf '%s\0ERROR\0' "$path"
            continue
        }
        sync
        if [[ ! -e "$target" && ! -L "$target" ]]; then
            test_failpoint "restore_after_marker"
            if ! mv "$newest" "$target"; then
                rm -f "$txfile" "$paths_file"
                printf '%s\0ERROR\0' "$path"
                continue
            fi
            moved_here="true"
            test_failpoint "restore_after_move"
            if ! publish_file_matches "$target" "$digest" "$size" "$mtime_ns"; then
                mv "$target" "$newest" 2>/dev/null || true
                rm -f "$txfile" "$paths_file"
                printf '%s\0ERROR\0' "$path"
                continue
            fi
        fi
        if ! advance_move_transaction "$txfile" MOVED; then
            if [[ "$moved_here" == "true" ]]; then
                mv "$target" "$newest" 2>/dev/null || true
            fi
            rm -f "$txfile" "$paths_file"
            printf '%s\0ERROR\0' "$path"
            continue
        fi
        sync
        if append_put_journal_batch_unlocked "$paths_file" "$device" "$txid" "STAGING_PUBLISH_V1"; then
            test_failpoint "restore_after_journal"
            rm -f "$txfile" "$paths_file"
            printf '%s\0RESTORED\0' "$path"
        else
            # The append rolled back journal and revision. Undo our move only
            # when this call made it; never delete an already-existing target.
            if [[ "$moved_here" == "true" && -e "$target" && ! -e "$newest" && ! -L "$newest" ]]; then
                mv "$target" "$newest" 2>/dev/null || true
            fi
            printf '%s\0ERROR\0' "$path"
        fi
    done
    rm -f "$manifest_snapshot"
    flock -u 9
    exec 9>&-
}

# --- interactive remote browsing (client's "Sfoglia NAS" tab) ---
#
# Unlike checked_delete (used by the sync engine's own reconciliation, where a
# stale client must never delete something newer than what it last saw),
# browse-delete/browse-rename are direct results of the user looking at a
# live listing they just fetched and acting on it -- there is no baseline to
# stale-check against, since the browsed path may never have been synced by
# this client (or any client) at all. Safety instead comes from: (1) never a
# raw unlink -- everything moves into the same TRASH_DIRNAME/retention the
# sync engine already uses, recoverable for RETENTION_DAYS like any other
# version; (2) the client acquires the global sync-transfer lock before calling
# these mutations, while this command takes JOURNAL_LOCK_FILE for the journal
# transaction, so browse actions cannot interleave with an in-flight transfer.

# List the immediate children of a folder under SHARE_ROOT (not recursive --
# the GUI tab lists one level at a time, expanding lazily, so this stays fast
# even on a huge tree). PATH="" lists SHARE_ROOT itself.
cmd_browse_list() {
    local relative="${1:-}" target entry name kind size mtime
    if [[ -z "$relative" ]]; then
        target="$SHARE_ROOT"
    else
        valid_sync_relative_path "$relative" || { echo "browse-list: percorso non valido" >&2; return 1; }
        target="$SHARE_ROOT/$relative"
    fi
    [[ -d "$target" && ! -L "$target" ]] || { echo "browse-list: cartella non trovata" >&2; return 1; }

    local -a names=()
    while IFS= read -r -d '' entry; do
        name="${entry##*/}"
        case "$name" in
            "$TRASH_DIRNAME"|.sync-partial|"$STAGING_DIRNAME"|"$REPOSITORY_MARKER_NAME"|@eaDir) continue ;;
        esac
        names+=("$name")
    done < <(find "$target" -mindepth 1 -maxdepth 1 -print0 2>/dev/null)

    printf 'BROWSE_LIST_V1\0%s\0' "${#names[@]}"
    for name in "${names[@]}"; do
        entry="$target/$name"
        if [[ -L "$entry" ]]; then
            kind="OTHER"; size=0; mtime=0
        elif [[ -d "$entry" ]]; then
            kind="DIR"; size=0; mtime=$(portable_file_mtime "$entry" || echo 0)
        elif [[ -f "$entry" ]]; then
            kind="FILE"; size=$(portable_file_size "$entry" || echo 0); mtime=$(portable_file_mtime "$entry" || echo 0)
        else
            kind="OTHER"; size=0; mtime=0
        fi
        printf '%s\0%s\0%s\0%s\0' "$name" "$kind" "${size:-0}" "${mtime:-0}"
    done
}

cmd_browse_delete() {
    command -v flock >/dev/null 2>&1 || { echo "browse-delete: flock non disponibile" >&2; return 1; }
    local magic run_ts device relative target backup rel_file paths_file txid txfile
    local -a journal_paths=()
    IFS= read -r -d '' magic || return 1
    IFS= read -r -d '' run_ts || return 1
    IFS= read -r -d '' device || return 1
    IFS= read -r -d '' relative || return 1
    [[ "$magic" == "BROWSE_DELETE_V1" ]] || return 1
    [[ "$run_ts" =~ ^[0-9]{4}-[0-9]{2}-[0-9]{2}--[0-9]{2}-[0-9]{2}-[0-9]{2}-[0-9]{6}Z$ ]] || return 1
    [[ "$device" =~ ^[A-Za-z0-9._:-]{1,128}$ ]] || return 1
    require_sync_transfer_owner "$device" || {
        printf 'BROWSE_DELETE_V1\0ERROR\0lease NAS non posseduto dal device\0'
        return 75
    }
    if ! valid_sync_relative_path "$relative"; then
        printf 'BROWSE_DELETE_V1\0ERROR\0percorso non valido\0'; return 1
    fi
    target="$SHARE_ROOT/$relative"
    if [[ ! -e "$target" && ! -L "$target" ]]; then
        printf 'BROWSE_DELETE_V1\0ERROR\0percorso non trovato\0'; return 1
    fi
    backup="$SHARE_ROOT/$TRASH_DIRNAME/$relative-$run_ts"

    exec 9>>"$JOURNAL_LOCK_FILE"
    flock -x 9 || { exec 9>&-; printf 'BROWSE_DELETE_V1\0ERROR\0lock non acquisito\0'; return 1; }
    ensure_journal_files
    recover_pending_transactions_unlocked || { flock -u 9; exec 9>&-; printf 'BROWSE_DELETE_V1\0ERROR\0recupero transazioni fallito\0'; return 1; }

    # The listing can be stale. Recheck after taking the journal lock and before
    # collecting the paths that will be represented in the manifest.
    if [[ ! -e "$target" && ! -L "$target" ]]; then
        flock -u 9; exec 9>&-
        printf 'BROWSE_DELETE_V1\0ERROR\0percorso non trovato\0'
        return 1
    fi
    if [[ -d "$target" && ! -L "$target" ]]; then
        while IFS= read -r -d '' rel_file; do
            journal_paths+=("$relative/${rel_file#./}")
        done < <(cd "$target" && find . -type f -print0 2>/dev/null)
    else
        journal_paths+=("$relative")
    fi

    txid="browse-delete-${run_ts}-$$-${RANDOM}"
    txfile="$TRANSACTION_DIR/$txid.txn"
    paths_file="$TRANSACTION_DIR/$txid.paths"
    if ! mkdir -p "$TRANSACTION_DIR" "${backup%/*}" || ! : > "$paths_file"; then
        flock -u 9; exec 9>&-
        printf 'BROWSE_DELETE_V1\0ERROR\0creazione elenco percorsi fallita\0'
        return 1
    fi
    for rel_file in "${journal_paths[@]}"; do
        if ! printf '%s\0' "$rel_file" >> "$paths_file"; then
            rm -f "$paths_file"
            flock -u 9; exec 9>&-
            printf 'BROWSE_DELETE_V1\0ERROR\0scrittura elenco percorsi fallita\0'
            return 1
        fi
    done
    if ! begin_move_transaction BROWSE_DELETE "$txid" "$device" "$run_ts" "$relative" "$target" "$backup" "$paths_file"; then
        rm -f "$paths_file"
        flock -u 9; exec 9>&-
        printf 'BROWSE_DELETE_V1\0ERROR\0registrazione transazione fallita\0'
        return 1
    fi
    if ! mv "$target" "$backup"; then
        rm -f "$txfile" "$paths_file"
        flock -u 9; exec 9>&-
        printf 'BROWSE_DELETE_V1\0ERROR\0spostamento nel cestino fallito\0'
        return 1
    fi
    test_failpoint "browse_delete_after_move"
    if ! advance_move_transaction "$txfile" MOVED; then
        mv "$backup" "$target" 2>/dev/null && rm -f "$txfile" "$paths_file"
        flock -u 9; exec 9>&-
        printf 'BROWSE_DELETE_V1\0ERROR\0registrazione transazione fallita\0'
        return 1
    fi
    if append_delete_journal_batch_unlocked "$paths_file" "$device" "$txid"; then
        test_failpoint "browse_delete_after_journal"
        rm -f "$txfile" "$paths_file"
        prune_empty_dirs_unlocked "$relative"
        flock -u 9; exec 9>&-
        printf 'BROWSE_DELETE_V1\0OK\0'
    else
        mv "$backup" "$target" 2>/dev/null && rm -f "$txfile" "$paths_file"
        flock -u 9; exec 9>&-
        printf 'BROWSE_DELETE_V1\0ERROR\0journal non aggiornato, operazione annullata\0'
        return 1
    fi
}

cmd_browse_rename() {
    command -v flock >/dev/null 2>&1 || { echo "browse-rename: flock non disponibile" >&2; return 1; }
    local magic run_ts device src dst src_target dst_target rel_file paths_file txid txfile
    local expected_kind expected_digest expected_size expected_mtime actual_size actual_mtime actual_digest
    local -a journal_paths=()
    local -a v3_rel=() v3_digest=() v3_size=() v3_mtime=()
    local v3_count=0 v3_index
    IFS= read -r -d '' magic || return 1
    IFS= read -r -d '' run_ts || return 1
    IFS= read -r -d '' device || return 1
    IFS= read -r -d '' src || return 1
    IFS= read -r -d '' dst || return 1
    if [[ "$magic" == "BROWSE_RENAME_V2" || "$magic" == "BROWSE_RENAME_V3" ]]; then
        IFS= read -r -d '' expected_kind || return 1
        IFS= read -r -d '' expected_digest || return 1
        IFS= read -r -d '' expected_size || return 1
        IFS= read -r -d '' expected_mtime || return 1
    fi
    if [[ "$magic" == "BROWSE_RENAME_V3" ]]; then
        # V3 moves a whole folder and carries what the client already knows
        # about every file (digest, size, mtime), so the NAS does not have to
        # read and hash each one: it only checks size and mtime.
        IFS= read -r -d '' v3_count || return 1
        [[ "$v3_count" =~ ^[0-9]+$ && "$v3_count" -le 200000 ]] || return 1
        for ((v3_index = 0; v3_index < v3_count; v3_index++)); do
            IFS= read -r -d '' rel_file || return 1
            v3_rel[v3_index]="$rel_file"
            IFS= read -r -d '' v3_digest[v3_index] || return 1
            IFS= read -r -d '' v3_size[v3_index] || return 1
            IFS= read -r -d '' v3_mtime[v3_index] || return 1
            valid_sync_relative_path "$src/${v3_rel[v3_index]}" || return 1
            [[ "${v3_digest[v3_index]}" =~ ^[0-9a-f]{64}$ && "${v3_size[v3_index]}" =~ ^[0-9]+$ \
               && "${v3_mtime[v3_index]}" =~ ^[0-9]+$ ]] || return 1
        done
        [[ "$expected_kind" == "DIR" ]] || return 1
    fi
    [[ "$magic" == "BROWSE_RENAME_V1" || "$magic" == "BROWSE_RENAME_V2" || "$magic" == "BROWSE_RENAME_V3" ]] || return 1
    [[ "$run_ts" =~ ^[0-9]{4}-[0-9]{2}-[0-9]{2}--[0-9]{2}-[0-9]{2}-[0-9]{2}-[0-9]{6}Z$ ]] || return 1
    [[ "$device" =~ ^[A-Za-z0-9._:-]{1,128}$ ]] || return 1
    if [[ "$magic" != "BROWSE_RENAME_V1" ]]; then
        [[ "$expected_kind" == "FILE" || "$expected_kind" == "DIR" ]] || return 1
        [[ "$expected_size" =~ ^[0-9]+$ && "$expected_mtime" =~ ^[0-9]+$ ]] || return 1
        if [[ "$expected_kind" == "FILE" ]]; then
            [[ "$expected_digest" =~ ^[0-9a-f]{64}$ ]] || return 1
        else
            [[ -z "$expected_digest" ]] || return 1
        fi
    fi
    require_sync_transfer_owner "$device" || {
        printf '%s\0ERROR\0lease NAS non posseduto dal device\0' "$magic"
        return 75
    }
    if ! valid_sync_relative_path "$src" || ! valid_sync_relative_path "$dst"; then
        printf '%s\0ERROR\0percorso non valido\0' "$magic"; return 1
    fi
    src_target="$SHARE_ROOT/$src"
    dst_target="$SHARE_ROOT/$dst"
    if [[ ! -e "$src_target" && ! -L "$src_target" ]]; then
        printf '%s\0ERROR\0percorso di origine non trovato\0' "$magic"; return 1
    fi
    if [[ -e "$dst_target" || -L "$dst_target" ]]; then
        printf '%s\0ERROR\0esiste gia un percorso con questo nome\0' "$magic"; return 1
    fi

    exec 9>>"$JOURNAL_LOCK_FILE"
    flock -x 9 || { exec 9>&-; printf '%s\0ERROR\0lock non acquisito\0' "$magic"; return 1; }
    ensure_journal_files
    recover_pending_transactions_unlocked || { flock -u 9; exec 9>&-; printf '%s\0ERROR\0recupero transazioni fallito\0' "$magic"; return 1; }

    if [[ ! -e "$src_target" && ! -L "$src_target" ]]; then
        flock -u 9; exec 9>&-
        printf '%s\0ERROR\0percorso di origine non trovato\0' "$magic"
        return 1
    fi
    if [[ -e "$dst_target" || -L "$dst_target" ]]; then
        flock -u 9; exec 9>&-
        printf '%s\0ERROR\0esiste gia un percorso con questo nome\0' "$magic"
        return 1
    fi

    if [[ "$magic" != "BROWSE_RENAME_V1" ]]; then
        if [[ "$expected_kind" == "FILE" ]]; then
            if [[ ! -f "$src_target" || -L "$src_target" ]]; then
                flock -u 9; exec 9>&-
                printf '%s\0ERROR\0la sorgente non e un file regolare\0' "$magic"
                return 1
            fi
            actual_size=$(portable_file_size "$src_target" || true)
            actual_mtime=$(portable_file_mtime "$src_target" || true)
            actual_digest=$(portable_sha256 "$src_target" || true)
            # The manifest may hold a sub-second mtime (a file uploaded by a
            # client keeps its local one); the NAS reads whole seconds. Compare
            # seconds, or every such file looked "changed".
            if [[ "$actual_size" != "$expected_size" || ! "$actual_mtime" =~ ^[0-9]+$ \
                  || "$actual_mtime" != "$((expected_mtime / 1000000000))" \
                  || "$actual_digest" != "$expected_digest" ]]; then
                flock -u 9; exec 9>&-
                printf '%s\0ERROR\0la sorgente e cambiata\0' "$magic"
                return 1
            fi
        elif [[ ! -d "$src_target" || -L "$src_target" ]]; then
            flock -u 9; exec 9>&-
            printf '%s\0ERROR\0la sorgente non e una directory\0' "$magic"
            return 1
        fi
    fi

    txid="browse-rename-${run_ts}-$$-${RANDOM}"
    txfile="$TRANSACTION_DIR/$txid.txn"
    paths_file="$TRANSACTION_DIR/$txid.paths"
    if ! mkdir -p "$TRANSACTION_DIR" "${dst_target%/*}" || ! : > "$paths_file"; then
        flock -u 9; exec 9>&-
        printf '%s\0ERROR\0creazione elenco percorsi fallita\0' "$magic"
        return 1
    fi
    if [[ "$magic" != "BROWSE_RENAME_V1" ]]; then
        if [[ "$expected_kind" == "FILE" ]]; then
            printf '%s\0%s\0%s\0%s\0%s\0' "$src" "$dst" "$expected_digest" \
                "$expected_size" "$expected_mtime" >> "$paths_file"
        elif [[ "$magic" == "BROWSE_RENAME_V3" ]]; then
            local found_files
            found_files=$(cd "$src_target" && find . -type f 2>/dev/null | wc -l)
            if [[ "${found_files//[[:space:]]/}" != "$v3_count" ]]; then
                rm -f "$paths_file"
                flock -u 9; exec 9>&-
                printf '%s\0ERROR\0la directory e cambiata\0' "$magic"
                return 1
            fi
            for ((v3_index = 0; v3_index < v3_count; v3_index++)); do
                local v3_file="$src_target/${v3_rel[v3_index]}"
                actual_size=$(portable_file_size "$v3_file" || true)
                actual_mtime=$(portable_file_mtime "$v3_file" || true)
                if [[ ! -f "$v3_file" || -L "$v3_file" || "$actual_size" != "${v3_size[v3_index]}" \
                      || ! "$actual_mtime" =~ ^[0-9]+$ \
                      || "$actual_mtime" != "$((v3_mtime[v3_index] / 1000000000))" ]]; then
                    rm -f "$paths_file"
                    flock -u 9; exec 9>&-
                    printf '%s\0ERROR\0la directory e cambiata\0' "$magic"
                    return 1
                fi
                printf '%s\0%s\0%s\0%s\0%s\0' "$src/${v3_rel[v3_index]}" "$dst/${v3_rel[v3_index]}" \
                    "${v3_digest[v3_index]}" "${v3_size[v3_index]}" "${v3_mtime[v3_index]}" >> "$paths_file"
            done
        else
            while IFS= read -r -d '' rel_file; do
                local source_file="$src/${rel_file#./}" destination_file="$dst/${rel_file#./}"
                actual_size=$(portable_file_size "$src_target/${rel_file#./}" || true)
                actual_mtime=$(portable_file_mtime "$src_target/${rel_file#./}" || true)
                actual_digest=$(portable_sha256 "$src_target/${rel_file#./}" || true)
                [[ "$actual_size" =~ ^[0-9]+$ && "$actual_mtime" =~ ^[0-9]+$ \
                   && "$actual_digest" =~ ^[0-9a-f]{64}$ ]] || {
                    rm -f "$paths_file"
                    flock -u 9; exec 9>&-
                    printf '%s\0ERROR\0contenuto directory non leggibile\0' "$magic"
                    return 1
                }
                printf '%s\0%s\0%s\0%s\0%s\0' "$source_file" "$destination_file" \
                    "$actual_digest" "$actual_size" "$((actual_mtime * 1000000000))" >> "$paths_file"
            done < <(cd "$src_target" && find . -type f -print0 2>/dev/null)
        fi
    else
        if [[ -d "$src_target" && ! -L "$src_target" ]]; then
            while IFS= read -r -d '' rel_file; do
                journal_paths+=("$src/${rel_file#./}")
            done < <(cd "$src_target" && find . -type f -print0 2>/dev/null)
        else
            journal_paths+=("$src")
        fi
        for rel_file in "${journal_paths[@]}"; do
            if ! printf '%s\0' "$rel_file" >> "$paths_file"; then
                rm -f "$paths_file"
                flock -u 9; exec 9>&-
                printf '%s\0ERROR\0scrittura elenco percorsi fallita\0' "$magic"
                return 1
            fi
        done
    fi
    if ! begin_move_transaction BROWSE_RENAME "$txid" "$device" "$run_ts" "$src" "$src_target" "$dst_target" "$paths_file" "$magic"; then
        rm -f "$paths_file"
        flock -u 9; exec 9>&-
        printf '%s\0ERROR\0registrazione transazione fallita\0' "$magic"
        return 1
    fi
    if ! mv "$src_target" "$dst_target"; then
        rm -f "$txfile" "$paths_file"
        flock -u 9; exec 9>&-
        printf '%s\0ERROR\0spostamento fallito\0' "$magic"
        return 1
    fi
    test_failpoint "browse_rename_after_move"
    if ! advance_move_transaction "$txfile" MOVED; then
        mv "$dst_target" "$src_target" 2>/dev/null && rm -f "$txfile" "$paths_file"
        flock -u 9; exec 9>&-
        printf '%s\0ERROR\0registrazione transazione fallita\0' "$magic"
        return 1
    fi
    if [[ "$magic" != "BROWSE_RENAME_V1" ]]; then
        append_rename_journal_batch_unlocked "$paths_file" "$device" "$txid"
    else
        append_delete_journal_batch_unlocked "$paths_file" "$device" "$txid"
    fi
    if [[ "$?" -eq 0 ]]; then
        test_failpoint "browse_rename_after_journal"
        rm -f "$txfile" "$paths_file"
        flock -u 9; exec 9>&-
        printf '%s\0OK\0' "$magic"
    else
        mv "$dst_target" "$src_target" 2>/dev/null && rm -f "$txfile" "$paths_file"
        flock -u 9; exec 9>&-
        printf '%s\0ERROR\0journal non aggiornato, operazione annullata\0' "$magic"
        return 1
    fi
}

cmd_journal_status() {
    ensure_journal_files
    printf 'JOURNAL_FILE=%s\n' "$JOURNAL_FILE"
    printf 'MANIFEST_FILE=%s\n' "$MANIFEST_FILE"
    printf 'JOURNAL_BYTES=%s\n' "$(wc -c < "$JOURNAL_FILE" 2>/dev/null || echo 0)"
    printf 'MANIFEST_BYTES=%s\n' "$(wc -c < "$MANIFEST_FILE" 2>/dev/null || echo 0)"
    printf 'JOURNAL_MAX_BYTES=%s\n' "$JOURNAL_MAX_BYTES"
    printf 'MANIFEST_REVISION=%s\n' "$(cat "$MANIFEST_REVISION_FILE" 2>/dev/null || echo 0)"
}

MANIFEST_REVISION=""
read_manifest_revision() {
    # Sets MANIFEST_REVISION instead of printing: the only caller is a hot
    # one-second wait loop, and `$(read_manifest_revision)` would fork a
    # subshell every second for every client. `raw=$(<file)` is a bash
    # special case that reads the file in-process, with no fork either.
    local raw
    MANIFEST_REVISION=""
    if [[ ! -f "$MANIFEST_REVISION_FILE" ]]; then
        MANIFEST_REVISION="0"
        return 0
    fi
    raw=$(<"$MANIFEST_REVISION_FILE")
    # Keep the value within bash arithmetic's portable signed range. The
    # revision writer only emits decimal integers, so anything else is a
    # corrupt protocol state rather than a reason to inspect SHARE_ROOT.
    [[ "$raw" =~ ^[0-9]{1,18}$ ]] || return 1
    MANIFEST_REVISION=$((10#$raw))
    return 0
}

print_change_wait_response() {
    printf 'NASBOX_CHANGE_WAIT_V1\nRESULT=%s\nREVISION=%s\n' "$1" "$2"
}

cmd_wait_for_revision() {
    local requested="$1" timeout="$2" current started remaining event event_status
    local use_inotify="false"
    local revision_name="${MANIFEST_REVISION_FILE##*/}"

    if [[ ! "$requested" =~ ^[0-9]{1,18}$ || ! "$timeout" =~ ^[0-9]+$ ]]; then
        print_change_wait_response "ERROR" "0"
        printf 'wait revision: argomenti non validi\n' >&2
        return 2
    fi
    requested=$((10#$requested))
    if (( timeout > CHANGE_WAIT_MAX_TIMEOUT_SECONDS )); then
        print_change_wait_response "ERROR" "0"
        printf 'wait revision: timeout fuori limite\n' >&2
        return 2
    fi

    read_manifest_revision && current="$MANIFEST_REVISION" || {
        print_change_wait_response "ERROR" "0"
        printf 'wait revision: manifest.revision non valido\n' >&2
        return 1
    }
    if (( current > requested )); then
        print_change_wait_response "CHANGED" "$current"
        return 0
    fi
    if (( timeout == 0 )); then
        print_change_wait_response "TIMEOUT" "$current"
        return 0
    fi

    started=$SECONDS
    if command -v inotifywait >/dev/null 2>&1; then
        use_inotify="true"
    fi
    while :; do
        read_manifest_revision && current="$MANIFEST_REVISION" || {
            print_change_wait_response "ERROR" "0"
            printf 'wait revision: manifest.revision non valido\n' >&2
            return 1
        }
        if (( current > requested )); then
            print_change_wait_response "CHANGED" "$current"
            return 0
        fi
        remaining=$((timeout - (SECONDS - started)))
        if (( remaining <= 0 )); then
            print_change_wait_response "TIMEOUT" "$current"
            return 0
        fi

        if [[ "$use_inotify" == "true" ]]; then
            # The directory is watched because revisions are replaced
            # atomically. Filter the event name so unrelated state files never
            # turn this into a repository scan.
            event=$(inotifywait -q -t "$remaining" \
                -e close_write,moved_to,create,delete --format '%f' \
                "$STATE_DIR" 2>/dev/null)
            event_status=$?
            if (( event_status == 0 )); then
                [[ "$event" == "$revision_name" ]] || continue
                continue
            fi
            if (( event_status == 2 )); then
                read_manifest_revision && current="$MANIFEST_REVISION" || {
                    print_change_wait_response "ERROR" "0"
                    printf 'wait revision: manifest.revision non valido\n' >&2
                    return 1
                }
                if (( current > requested )); then
                    print_change_wait_response "CHANGED" "$current"
                else
                    print_change_wait_response "TIMEOUT" "$current"
                fi
                return 0
            fi
            # An installed but incompatible/broken inotifywait must not make
            # the feed unavailable: fall back to a one-second file poll.
            use_inotify="false"
        else
            sleep $((remaining < 1 ? remaining : 1))
        fi
    done
}

# --- pruning ---
#
# Each historical version is its own file, named
# "<originalname>-YYYY-MM-DD--HH-MM-SS-microsecondsZ" in UTC. Older files use
# the shorter local-time suffix and remain supported.
# (the client appends this suffix via rsync --suffix on every push/pull). This
# makes it obvious at a glance which files are historical and from when,
# without digging into per-run subfolders.

TS_SUFFIX_REGEX='-([0-9]{4}-[0-9]{2}-[0-9]{2}--[0-9]{2}-[0-9]{2}-[0-9]{2})(-([0-9]{6})Z)?$'

# Returns the epoch this trashed file's version timestamp represents (parsed
# straight from its filename suffix), falling back to its filesystem mtime
# if the name doesn't carry one. Deliberately the ONLY external process this
# forks per file (`date -d`, to parse the timestamp -- bash has no builtin
# for that): the previous version of this function also forked `basename`,
# a second `date` just to read "now", and `awk` to do the subtraction --
# 4 forks/file instead of 1. That difference is not cosmetic: a NAS with a
# large .sync-trash (tens of thousands of trashed versions is normal after
# months of syncing) was measured taking minutes for a single pruning pass
# on modest NAS-class ARM hardware, long enough to trip the client's own
# 120s SSH timeout on "Chiedi al NAS di pulire ora". `now` and RETENTION_DAYS
# are compared here as raw epoch seconds (prune_root passes `now` in once,
# not re-read per file), only converted to a "X.Y giorni" string for the log
# line where a human actually needs to read it -- via bash arithmetic, still
# no extra fork.
# Days between 1970-01-01 and the given proleptic-Gregorian civil date (UTC),
# via Howard Hinnant's days_from_civil. Pure 64-bit bash integer math, valid for
# any year a trash name can carry, and -- crucially -- no fork. The result is
# left in CIVIL_DAYS (rather than printed) so callers never need a `$(...)`
# subshell; that substitution, not the arithmetic, is what used to make a
# six-figure pruning walk fork once per file.
CIVIL_DAYS=0
civil_days_since_epoch() {
    local y=$((10#$1)) m=$((10#$2)) d=$((10#$3))
    (( m <= 2 )) && (( y-- ))
    local era yoe doy doe
    if (( y >= 0 )); then era=$(( y / 400 )); else era=$(( (y - 399) / 400 )); fi
    yoe=$(( y - era * 400 ))
    local mp=$(( m > 2 ? m - 3 : m + 9 ))
    doy=$(( (153 * mp + 2) / 5 + d - 1 ))
    doe=$(( yoe * 365 + yoe / 4 - yoe / 100 + doy ))
    CIVIL_DAYS=$(( era * 146097 + doe - 719468 ))
}

# Epoch seconds a trashed version represents, parsed from its filename suffix
# (falling back to the file mtime when the name carries no timestamp). Sets the
# global FILE_EPOCH ("" when unknown) instead of printing, so the hot pruning
# walk can call it once per file with no subshell and no `date` fork.
#
# The modern client stamps every version with a UTC "…-HHMMSSZ" suffix, so those
# names -- the overwhelming majority -- are resolved entirely in integer
# arithmetic. Only legacy names with no UTC marker (a handful, historically)
# still need the `date` built-in, which is a dependency the daemon already has.
FILE_EPOCH=""
file_epoch() {
    local file="$1"
    local base="${file##*/}"
    FILE_EPOCH=""
    if [[ "$base" =~ $TS_SUFFIX_REGEX ]]; then
        local ts="${BASH_REMATCH[1]}"
        local utc_suffix="${BASH_REMATCH[2]}"
        local y="${ts:0:4}" mo="${ts:5:2}" d="${ts:8:2}" h="${ts:12:2}" mi="${ts:15:2}" s="${ts:18:2}"
        if [[ -n "$utc_suffix" ]]; then
            civil_days_since_epoch "$y" "$mo" "$d"
            FILE_EPOCH=$(( CIVIL_DAYS * 86400 + 10#$h * 3600 + 10#$mi * 60 + 10#$s ))
            return
        fi
        local legacy
        legacy=$(date -d "${y}-${mo}-${d} ${h}:${mi}:${s}" '+%s' 2>/dev/null)
        if [[ -n "$legacy" ]]; then
            FILE_EPOCH="$legacy"
            return
        fi
    fi
    FILE_EPOCH=$(stat -c '%Y' "$file" 2>/dev/null || stat -f '%m' "$file" 2>/dev/null)
}

# "3.4" style day count from a whole-second age, computed with bash integer
# arithmetic (age_seconds * 100 / 86400 keeps 2 implied decimal places
# without a float lib or an awk/bc fork).
format_age_days() {
    local age_seconds="$1"
    local hundredths=$(( age_seconds * 100 / 86400 ))
    printf '%d.%02d' "$(( hundredths / 100 ))" "$(( hundredths % 100 ))"
}

trash_dir() {
    printf '%s/%s' "$SHARE_ROOT" "$TRASH_DIRNAME"
}

repository_marker_file() {
    printf '%s/%s' "$SHARE_ROOT" "$REPOSITORY_MARKER_NAME"
}

read_repository_id() {
    local marker first key value
    marker="$(repository_marker_file)"
    [[ ! -L "$marker" ]] || return 1
    [[ -f "$marker" ]] || return 1
    IFS= read -r first < "$marker" || return 1
    [[ "$first" == "NASBOX_REPOSITORY_V1" ]] || return 1
    while IFS='=' read -r key value || [[ -n "$key" ]]; do
        if [[ "$key" == "ID" ]]; then
            [[ "$value" =~ ^[A-Za-z0-9._:-]{16,128}$ ]] || return 1
            printf '%s' "$value"
            return 0
        fi
    done < "$marker"
    return 1
}

new_repository_id() {
    local entropy
    entropy="$(date -u '+%Y-%m-%dT%H:%M:%S.%N')-$$-${RANDOM:-0}-$(hostname 2>/dev/null || true)"
    if command -v sha256sum >/dev/null 2>&1; then
        printf '%s' "$entropy" | sha256sum | cut -d ' ' -f1
    else
        # sha256sum is preferred but not mandatory on NAS firmware images.
        printf '%s' "$entropy"
    fi
}

cmd_init_repository() {
    local marker id tmp
    if [[ "$(readlink -f "$SHARE_ROOT" 2>/dev/null || realpath "$SHARE_ROOT" 2>/dev/null || printf '%s' "$SHARE_ROOT")" == "/" ]]; then
        echo "SHARE_ROOT non puo' essere la radice del filesystem (/)." >&2
        return 1
    fi
    marker="$(repository_marker_file)"
    if id="$(read_repository_id)"; then
        echo "Repository NASBox già inizializzato (ID $id)."
        return 0
    fi
    if [[ -e "$marker" || -L "$marker" ]]; then
        echo "Marker repository non riconosciuto o corrotto: $marker" >&2
        return 1
    fi
    id="$(new_repository_id)"
    tmp="${marker}.tmp.$$"
    umask 022
    if ! printf 'NASBOX_REPOSITORY_V1\nID=%s\n' "$id" > "$tmp"; then
        rm -f "$tmp"
        echo "Impossibile creare il marker repository: $marker" >&2
        return 1
    fi
    if ! mv "$tmp" "$marker"; then
        rm -f "$tmp"
        echo "Impossibile installare il marker repository: $marker" >&2
        return 1
    fi
    echo "Repository NASBox inizializzato (ID $id)."
}

# --- trash retention watermark -------------------------------------------
#
# Every version's retention is decided from the UTC timestamp in its filename,
# and a version is only ever added with a "now-ish" timestamp. So the oldest
# version currently in the trash can only get older, never younger: until *that*
# one crosses the retention cutoff, no other version can either. Recording the
# oldest epoch seen at the last complete scan lets an hourly pass skip the whole
# metadata walk (minutes on a six-figure trash) until a scan could actually find
# something. The file holds "<oldest_epoch> <last_scan_epoch>".
read_trash_watermark() {
    TRASH_OLDEST=""
    TRASH_LAST_SCAN=""
    [[ -f "$TRASH_WATERMARK_FILE" ]] || return 1
    local oldest last
    read -r oldest last < "$TRASH_WATERMARK_FILE" 2>/dev/null || return 1
    [[ "$oldest" =~ ^[0-9]+$ && "$last" =~ ^[0-9]+$ ]] || return 1
    TRASH_OLDEST="$oldest"
    TRASH_LAST_SCAN="$last"
    return 0
}

trash_scan_due() {
    local now="$1"
    read_trash_watermark || return 0  # no/invalid watermark -> must scan
    local retention_seconds=$(( RETENTION_DAYS * 86400 ))
    (( now >= TRASH_OLDEST + retention_seconds )) && return 0
    (( now - TRASH_LAST_SCAN >= PRUNE_MAX_SKIP_SECONDS )) && return 0
    return 1
}

write_trash_watermark() {
    local oldest="$1" now="$2" tmp
    [[ "$oldest" =~ ^[0-9]+$ ]] || oldest="$now"
    tmp="${TRASH_WATERMARK_FILE}.tmp.$$"
    if printf '%s %s\n' "$oldest" "$now" > "$tmp" 2>/dev/null; then
        mv -f "$tmp" "$TRASH_WATERMARK_FILE" 2>/dev/null || rm -f "$tmp"
    fi
}

collect_prune_candidates() {
    local trash="$1" candidates="$2"
    local now retention_seconds processed=0 file epoch age_seconds
    now=$(date '+%s')
    retention_seconds=$(( RETENTION_DAYS * 86400 ))
    PRUNE_MIN_EPOCH=""
    PRUNE_CAPPED="false"

    # This metadata walk can take minutes on a large trash directory. It must
    # remain outside SYNC_LOCK_FILE; paths are checked again before deletion.
    while IFS= read -r -d '' file; do
        file_epoch "$file"
        epoch="$FILE_EPOCH"
        [[ -z "$epoch" ]] && continue
        age_seconds=$(( now - epoch ))
        if (( age_seconds > retention_seconds )); then
            printf '%s\0' "$file" >> "$candidates"
            (( processed++ ))
            if (( processed >= PRUNE_MAX_FILES_PER_PASS )); then
                log INFO "Pass di pruning limitato a $PRUNE_MAX_FILES_PER_PASS versioni; continuera' al prossimo intervallo."
                PRUNE_CAPPED="true"
                break
            fi
        else
            # Only survivors feed the watermark: the candidates above will be
            # deleted by this pass, so they must not dictate the next cutoff.
            if [[ -z "$PRUNE_MIN_EPOCH" ]] || (( epoch < PRUNE_MIN_EPOCH )); then
                PRUNE_MIN_EPOCH="$epoch"
            fi
        fi
    done < <(find "$trash" -type f -print0 2>/dev/null)
}

prune_candidates() {
    local trash="$1" candidates="$2"
    local now retention_seconds file epoch age_seconds rel age_display
    local removed=0
    now=$(date '+%s')
    retention_seconds=$(( RETENTION_DAYS * 86400 ))

    while IFS= read -r -d '' file; do
        # The unlocked scan may have raced a transfer; only remove a candidate
        # that is still old while this shared lock excludes transfers.
        [[ -f "$file" ]] || continue
        file_epoch "$file"
        epoch="$FILE_EPOCH"
        [[ -n "$epoch" ]] || continue
        age_seconds=$(( now - epoch ))
        (( age_seconds > retention_seconds )) || continue
        rel="${file#"$trash"/}"
        age_display="$(format_age_days "$age_seconds")"
        if [[ "$DRY_RUN" == "true" ]]; then
            log INFO "[dry-run] rimuoverei versione: $rel (età ${age_display}gg > retention ${RETENTION_DAYS}gg)"
            removed=$(( removed + 1 ))
        elif rm -f "$file"; then
            log INFO "Rimossa versione storica: $rel (età ${age_display}gg > retention ${RETENTION_DAYS}gg)"
            log_event "PRUNE_REMOTE_TRASH" "$rel" "age=${age_display}d retention=${RETENTION_DAYS}d"
            removed=$(( removed + 1 ))
        else
            log ERROR "Impossibile rimuovere versione storica: $rel"
        fi
    done < "$candidates"
    # Hand the count back so the caller only pays for the empty-directory sweep
    # when this pass actually emptied something.
    printf '%d' "$removed"
}

collect_empty_dirs() {
    local trash="$1" candidates="$2"
    local processed=0 directory
    # As above, scan outside the transfer lock; rmdir is performed only after
    # reacquiring it and succeeds only if the directory is still empty.
    while IFS= read -r -d '' directory; do
        printf '%s\0' "$directory" >> "$candidates"
        (( processed++ ))
        (( processed >= PRUNE_MAX_FILES_PER_PASS )) && break
    done < <(find "$trash" -mindepth 1 -depth -type d -empty -print0 2>/dev/null)
}

remove_empty_dirs() {
    local candidates="$1" directory
    while IFS= read -r -d '' directory; do
        rmdir "$directory" 2>/dev/null || true
    done < "$candidates"
}

collect_staging_candidates() {
    local staging="$1" candidates="$2"
    [[ -d "$staging" ]] || return 0
    # Directory mtime changes as files are created. A generous age threshold
    # avoids touching an active upload while still reclaiming abandoned batches.
    find "$staging" -mindepth 1 -maxdepth 1 -type d \
        -mmin "+$((STAGING_RETENTION_HOURS * 60))" -print0 2>/dev/null \
        | while IFS= read -r -d '' directory; do
            printf '%s\0' "$directory" >> "$candidates"
        done
}

staging_is_referenced() {
    local directory="$1" txfile
    shopt -s nullglob
    for txfile in "$TRANSACTION_DIR"/*.txn; do
        if grep -Fq -- "$directory" "$txfile" 2>/dev/null; then
            shopt -u nullglob
            return 0
        fi
    done
    shopt -u nullglob
    return 1
}

# --- History quota (TRASH_MAX_MB) -----------------------------------------
# Age retention alone lets .sync-trash grow without bound when many large
# files change. The quota pass removes the OLDEST versions first until the
# history fits, never touching versions younger than TRASH_MIN_KEEP_DAYS.
# The usage is recorded in TRASH_USAGE_FILE so --print-config can report it
# without walking the tree (clients show it; the NAS disks stay asleep).

trash_usage_file() { printf '%s/trash-usage' "$STATE_DIR"; }

record_trash_usage() {
    local bytes="$1" files="$2" tmp
    tmp="$(trash_usage_file).tmp.$$"
    if printf '%s %s %s\n' "$bytes" "$files" "$(date '+%s')" > "$tmp" 2>/dev/null; then
        mv -f "$tmp" "$(trash_usage_file)" 2>/dev/null || rm -f "$tmp"
    fi
}

# enforce_trash_quota <trash>: prints the number of removed versions.
enforce_trash_quota() {
    local trash="$1" listing total=0 files=0 limit keep_seconds now removed=0
    local epoch size file
    listing="$(mktemp "$STATE_DIR/quota-list.XXXXXX")" || { echo 0; return 1; }
    find "$trash" -type f -printf '%T@ %s %p\0' 2>/dev/null | sort -z -n > "$listing"
    while IFS= read -r -d '' line; do
        size="${line#* }"; size="${size%% *}"
        [[ "$size" =~ ^[0-9]+$ ]] || continue
        total=$(( total + size )); files=$(( files + 1 ))
    done < "$listing"
    limit=$(( TRASH_MAX_MB * 1048576 ))
    keep_seconds=$(( TRASH_MIN_KEEP_DAYS * 86400 ))
    now=$(date '+%s')
    if (( TRASH_MAX_MB > 0 && total > limit )); then
        exec 8>>"$SYNC_LOCK_FILE" || { rm -f "$listing"; echo 0; return 1; }
        if flock -n 8; then
            while IFS= read -r -d '' line && (( total > limit )); do
                epoch="${line%% *}"; epoch="${epoch%%.*}"
                size="${line#* }"; file="${size#* }"; size="${size%% *}"
                [[ "$epoch" =~ ^[0-9]+$ && "$size" =~ ^[0-9]+$ ]] || continue
                (( now - epoch >= keep_seconds )) || break
                [[ "$file" == "$trash"/* && -f "$file" ]] || continue
                if [[ "$DRY_RUN" == "true" ]]; then
                    log INFO "[dry-run] quota storico: rimuoverei ${file#"$trash"/}"
                elif rm -f "$file"; then
                    log_event "PRUNE_REMOTE_TRASH" "${file#"$trash"/}" "quota=${TRASH_MAX_MB}MB"
                else
                    continue
                fi
                total=$(( total - size )); files=$(( files - 1 )); removed=$(( removed + 1 ))
            done < "$listing"
            flock -u 8
            if [[ "$DRY_RUN" == "true" ]]; then
                log INFO "[dry-run] Quota storico ${TRASH_MAX_MB} MB: verrebbero rimosse $removed versioni, resterebbero $(( total / 1048576 )) MB."
            else
                log INFO "Quota storico ${TRASH_MAX_MB} MB: rimosse $removed versioni, occupati $(( total / 1048576 )) MB."
            fi
        else
            log INFO "Quota storico rimandata: trasferimento NASBox in corso."
        fi
        exec 8>&-
    fi
    rm -f "$listing"
    record_trash_usage "$total" "$files"
    echo "$removed"
}

prune_staging_candidates() {
    local candidates="$1" directory removed=0 processed=0
    while IFS= read -r -d '' directory; do
        (( processed >= PRUNE_MAX_STAGING_PER_PASS )) && break
        (( processed++ ))
        [[ "$directory" == "$SHARE_ROOT/$STAGING_DIRNAME/"* ]] || continue
        if staging_is_referenced "$directory"; then
            log INFO "Staging conservata: transazione ancora recuperabile ($directory)"
            continue
        fi
        if rm -rf -- "$directory"; then
            (( removed++ ))
        else
            log ERROR "Impossibile rimuovere staging abbandonata: $directory"
        fi
    done < "$candidates"
    (( removed > 0 )) && log INFO "Staging abbandonate rimosse: $removed"
}

maintain_state_files() {
    # Small, bounded housekeeping that runs with the daemon's normal hourly
    # pass. It never walks SHARE_ROOT: only the tiny state directory.
    local journal_bytes journal_lines events_bytes
    journal_bytes=$(wc -c < "$JOURNAL_FILE" 2>/dev/null || echo 0)
    journal_lines=$(wc -l < "$JOURNAL_FILE" 2>/dev/null || echo 0)
    if [[ "$journal_bytes" =~ ^[0-9]+$ && "$journal_lines" =~ ^[0-9]+$ ]] \
            && (( journal_bytes > JOURNAL_MAX_BYTES || journal_lines > JOURNAL_MAX_LINES )); then
        if cmd_journal_compact >/dev/null 2>&1; then
            log INFO "Manutenzione: journal compattato (${journal_bytes} byte, ${journal_lines} righe)."
        else
            log WARN "Manutenzione: compattazione journal fallita; riprovo al prossimo pass."
        fi
    fi

    if [[ -f "$EVENTS_FILE" ]]; then
        events_bytes=$(wc -c < "$EVENTS_FILE" 2>/dev/null || echo 0)
        if [[ "$events_bytes" =~ ^[0-9]+$ ]] && (( events_bytes > EVENTS_MAX_BYTES )); then
            # Single previous generation, same convention as server.log.
            mv -f "$EVENTS_FILE" "${EVENTS_FILE}.1" 2>/dev/null || true
            log INFO "Manutenzione: events.jsonl ruotato (${events_bytes} byte)."
        fi
    fi

    if (( LOG_RETENTION_DAYS > 0 )); then
        find "$STATE_DIR" -maxdepth 1 -type f \
            \( -name 'server.log.1' -o -name 'events.jsonl.1' \) \
            -mmin "+$(( LOG_RETENTION_DAYS * 1440 ))" \
            -exec rm -f -- {} + 2>/dev/null || true
    fi

    if (( STATE_TEMP_MAX_AGE_HOURS > 0 )); then
        # Leftovers from an interrupted command. Only well-known temporary
        # names are touched and only after a full day, so an in-flight command
        # can never lose its files.
        find "$STATE_DIR" -maxdepth 1 -type f \
            \( -name 'prune-files.*' -o -name 'prune-dirs.*' -o -name 'prune-staging.*' \
               -o -name 'fs-want.*' -o -name 'fs-match.*' \
               -o -name 'manifest.tsv.tmp.*' -o -name 'manifest.tsv.sweep.*' \
               -o -name 'transfer-journal.tsv.tmp' -o -name 'transfer-journal.tsv.append.*' \
               -o -name 'transfer-journal.tsv.delete.*' -o -name 'transfer-journal.tsv.rollback.*' \
               -o -name '*.rollback.*' \) \
            -mmin "+$(( STATE_TEMP_MAX_AGE_HOURS * 60 ))" \
            -exec rm -f -- {} + 2>/dev/null || true
    fi
}

run_once_pass() {
    command -v flock >/dev/null 2>&1 || {
        log ERROR "Impossibile eseguire il pruning: flock non disponibile."
        return 1
    }
    # Maintenance walks the history tree: run it at low CPU priority so DSM
    # and file sharing stay responsive (ionice is not available on DSM).
    command -v renice >/dev/null 2>&1 && renice -n 10 -p $$ >/dev/null 2>&1 || true
    log INFO "sync-daemon-server v$VERSION: avvio pass di pruning (SHARE_ROOT=$SHARE_ROOT, retention=${RETENTION_DAYS}gg, dry-run=$DRY_RUN)"
    maintain_state_files
    local trash file_candidates dir_candidates staging_candidates
    local removed=0
    trash="$(trash_dir)"
    if [[ -d "$trash" ]]; then
        if [[ "$RETENTION_DAYS" != "0" ]]; then
            local scan_now
            scan_now=$(date '+%s')
            if trash_scan_due "$scan_now"; then
                file_candidates="$(mktemp "$STATE_DIR/prune-files.XXXXXX")" || return 1
                collect_prune_candidates "$trash" "$file_candidates"
                if [[ -s "$file_candidates" ]]; then
                    exec 8>>"$SYNC_LOCK_FILE" || { rm -f "$file_candidates"; return 1; }
                    if flock -n 8; then
                        removed="$(prune_candidates "$trash" "$file_candidates")"
                        flock -u 8
                    else
                        log INFO "Pruning rimandato: trasferimento NASBox in corso."
                    fi
                    exec 8>&-
                fi
                rm -f "$file_candidates"

                # Record the oldest version seen so the next hourly passes can
                # skip the whole walk until one of them could actually be old
                # enough. Only trust it after a COMPLETE scan: a scan capped by
                # PRUNE_MAX_FILES_PER_PASS stopped early and did not see every
                # file, so its minimum is not authoritative.
                if [[ "$PRUNE_CAPPED" != "true" ]]; then
                    write_trash_watermark "$PRUNE_MIN_EPOCH" "$scan_now"
                fi

                # collect_empty_dirs is a second full traversal of the trash tree.
                # It can only find something to remove if this pass removed files,
                # so skip it entirely when nothing was pruned.
                if [[ "$removed" =~ ^[0-9]+$ ]] && (( removed > 0 )); then
                    dir_candidates="$(mktemp "$STATE_DIR/prune-dirs.XXXXXX")" || return 1
                    collect_empty_dirs "$trash" "$dir_candidates"
                    if [[ -s "$dir_candidates" ]]; then
                        exec 8>>"$SYNC_LOCK_FILE" || { rm -f "$dir_candidates"; return 1; }
                        if flock -n 8; then
                            remove_empty_dirs "$dir_candidates"
                            flock -u 8
                        else
                            log INFO "Pulizia cartelle storiche rimandata: trasferimento NASBox in corso."
                        fi
                        exec 8>&-
                    fi
                    rm -f "$dir_candidates"
                fi
            else
                log INFO "Pruning saltato: la versione piu' vecchia dello storico e' ancora entro la retention (${RETENTION_DAYS}gg)."
            fi
        fi
        # Quota and usage: one metadata walk per pass (every
        # CHECK_INTERVAL_MINUTES), never on client requests.
        enforce_trash_quota "$trash" >/dev/null
    else
        log INFO "Nessuno storico ancora presente ($trash non esiste)."
    fi
    staging_candidates="$(mktemp "$STATE_DIR/prune-staging.XXXXXX")" || return 1
    collect_staging_candidates "$SHARE_ROOT/$STAGING_DIRNAME" "$staging_candidates"
    if [[ -s "$staging_candidates" ]]; then
        exec 8>>"$SYNC_LOCK_FILE" || { rm -f "$staging_candidates"; return 1; }
        if flock -n 8; then
            prune_staging_candidates "$staging_candidates"
            flock -u 8
        else
            log INFO "Pulizia staging rimandata: trasferimento NASBox in corso."
        fi
        exec 8>&-
    fi
    rm -f "$staging_candidates"
    date '+%s' > "$STAMP_FILE"
    log INFO "Pass completato."
}

# --- daemon loop ---

run_foreground() {
    mkdir -p "$STATE_DIR"
    if ! command -v flock >/dev/null 2>&1; then
        log ERROR "Impossibile avviare il demone: flock non disponibile."
        return 1
    fi
    exec 9>>"$DAEMON_LOCK_FILE"
    if ! flock -n 9; then
        log WARN "Avvio ignorato: un'altra istanza del demone è già attiva."
        return 0
    fi
    echo $$ > "$PID_FILE"

    local interval_seconds=$(( CHECK_INTERVAL_MINUTES * 60 ))
    log INFO "sync-daemon-server v$VERSION avviato in foreground (PID $$), intervallo interno: ${CHECK_INTERVAL_MINUTES} min"

    local sleeper_pid=""
    cleanup() {
        log INFO "Arresto richiesto, chiusura in corso..."
        [[ -n "$sleeper_pid" ]] && kill "$sleeper_pid" 2>/dev/null
        rm -f "$PID_FILE"
        log INFO "Demone arrestato."
        exit 0
    }
    trap cleanup SIGTERM SIGINT SIGHUP

    while true; do
        run_once_pass
        sleep "$interval_seconds" &
        sleeper_pid=$!
        wait "$sleeper_pid"
        sleeper_pid=""
    done
}

is_running() {
    [[ -f "$PID_FILE" ]] || return 1
    local pid
    pid=$(cat "$PID_FILE" 2>/dev/null || echo "")
    [[ "$pid" =~ ^[1-9][0-9]*$ ]] || return 1
    kill -0 "$pid" 2>/dev/null || return 1

    # A PID can be reused after a crash. Verify that it is this daemon before
    # reporting it running or sending it a signal in cmd_stop.
    local command_line=""
    if [[ -r "/proc/$pid/cmdline" ]]; then
        command_line=$(tr '\0' ' ' < "/proc/$pid/cmdline" 2>/dev/null || true)
    fi
    if [[ -z "$command_line" ]]; then
        command_line=$(ps -p "$pid" -o args= 2>/dev/null || true)
    fi
    [[ "$command_line" == *"$SCRIPT_PATH"* && "$command_line" == *"--run-foreground"* ]]
}

cmd_start() {
    if is_running; then
        echo "Il demone è già in esecuzione (PID $(cat "$PID_FILE"))."
        return 0
    fi
    mkdir -p "$STATE_DIR"
    rm -f "$PID_FILE"
    # log() already appends to LOG_FILE through tee. Redirecting the daemon's
    # stderr to the same file here would write every line twice.
    nohup "$SCRIPT_PATH" -c "$CONFIG_FILE" --run-foreground >/dev/null 2>&1 < /dev/null &
    disown
    sleep 1
    if is_running; then
        echo "Demone avviato (PID $(cat "$PID_FILE"))."
    else
        echo "Avvio non riuscito: controlla $LOG_FILE"
        return 1
    fi
}

cmd_stop() {
    if ! is_running; then
        echo "Il demone non è in esecuzione."
        rm -f "$PID_FILE"
        return 0
    fi
    local pid
    pid=$(cat "$PID_FILE")
    kill -TERM "$pid" 2>/dev/null
    for ((_i = 0; _i < 20; _i++)); do
        is_running || break
        sleep 0.5
    done
    if is_running; then
        echo "Il processo $pid non risponde, invio SIGKILL."
        kill -KILL "$pid" 2>/dev/null
        rm -f "$PID_FILE"
    else
        echo "Demone arrestato."
    fi
}

cmd_restart() {
    cmd_stop
    cmd_start
}

# --- status ---

human_size() {
    du -sh "$1" 2>/dev/null | cut -f1
}

read_sync_lock_diagnostics() {
    SYNC_LOCK_HELD="false"
    SYNC_LOCK_AGE_SECONDS="0"
    SYNC_LOCK_OWNER_PID=""
    SYNC_LOCK_OWNER_ID=""
    SYNC_LOCK_OWNER_HOST=""
    SYNC_LOCK_STARTED_AT="0"
    SYNC_LOCK_PHASE=""
    SYNC_LOCK_PROGRESS_DONE="0"
    SYNC_LOCK_PROGRESS_TOTAL="0"
    exec 8>>"$SYNC_LOCK_FILE" || return 0
    if flock -n 8; then
        flock -u 8
        exec 8>&-
        return 0
    fi
    SYNC_LOCK_HELD="true"
    if [[ -f "$SYNC_LOCK_OWNER_FILE" ]]; then
        IFS='|' read -r SYNC_LOCK_OWNER_ID SYNC_LOCK_OWNER_HOST SYNC_LOCK_STARTED_AT SYNC_LOCK_PHASE SYNC_LOCK_PROGRESS_DONE SYNC_LOCK_PROGRESS_TOTAL < "$SYNC_LOCK_OWNER_FILE"
        [[ "$SYNC_LOCK_STARTED_AT" =~ ^[0-9]+$ ]] || SYNC_LOCK_STARTED_AT="0"
        [[ "$SYNC_LOCK_PROGRESS_DONE" =~ ^[0-9]+$ ]] || SYNC_LOCK_PROGRESS_DONE="0"
        [[ "$SYNC_LOCK_PROGRESS_TOTAL" =~ ^[0-9]+$ ]] || SYNC_LOCK_PROGRESS_TOTAL="0"
    fi
    while read -r pid elapsed args; do
        case "$args" in
            *"$SYNC_LOCK_FILE"*)
                SYNC_LOCK_OWNER_PID="$pid"
                [[ "$elapsed" =~ ^[0-9]+$ ]] && SYNC_LOCK_AGE_SECONDS="$elapsed"
                break
                ;;
        esac
    done < <(ps -eo pid=,etimes=,args= 2>/dev/null)
    flock -u 8
    exec 8>&-
}

transfer_queue_prune_stale() {
    local ticket priority created device pid
    mkdir -p "$TRANSFER_QUEUE_DIR" || return 1
    while IFS= read -r -d '' ticket; do
        IFS='|' read -r priority created device pid < "$ticket" || { rm -f "$ticket"; continue; }
        if [[ ! "$pid" =~ ^[0-9]+$ ]] || ! kill -0 "$pid" 2>/dev/null; then
            rm -f "$ticket"
        fi
    done < <(find "$TRANSFER_QUEUE_DIR" -maxdepth 1 -type f -name '*.ticket' -print0 2>/dev/null)
}

transfer_queue_head() {
    local ticket priority created device pid
    transfer_queue_prune_stale || return 1
    while IFS= read -r -d '' ticket; do
        IFS='|' read -r priority created device pid < "$ticket" || continue
        printf '%s\t%s\t%s\n' "$priority" "$created" "$ticket"
    done < <(find "$TRANSFER_QUEUE_DIR" -maxdepth 1 -type f -name '*.ticket' -print0 2>/dev/null) \
        | sort -n -k1,1 -k2,2 | awk -F '\t' 'NR == 1 { print $3; exit }'
}

cmd_transfer_wait() {
    command -v flock >/dev/null 2>&1 || return 1
    local magic device priority request_id owner_host queue_head now owner_payload owner_tmp phase done total
    TRANSFER_ACTIVE_TICKET=""
    TRANSFER_LEASE_HELD=false
    IFS= read -r -d '' magic || return 1
    IFS= read -r -d '' device || return 1
    IFS= read -r -d '' priority || return 1
    IFS= read -r -d '' request_id || return 1
    IFS= read -r -d '' owner_host || return 1
    [[ "$magic" == "TRANSFER_WAIT_V1" ]] || return 1
    [[ "$device" =~ ^[A-Za-z0-9._:-]{1,128}$ ]] || return 1
    [[ "$priority" =~ ^[0-9]+$ && "$priority" -le 9 ]] || return 1
    [[ "$request_id" =~ ^[A-Za-z0-9._:-]{8,128}$ ]] || return 1
    [[ "$owner_host" =~ ^[A-Za-z0-9._:-]{1,128}$ ]] || owner_host="unknown"

    mkdir -p "$TRANSFER_QUEUE_DIR" || return 1
    TRANSFER_ACTIVE_TICKET="$TRANSFER_QUEUE_DIR/$request_id.ticket"
    now=$(date '+%s')
    exec 7>>"$TRANSFER_QUEUE_LOCK_FILE"
    flock -x 7 || { exec 7>&-; return 1; }
    printf '%s|%s|%s|%s\n' "$priority" "$now" "$device" "$$" > "$TRANSFER_ACTIVE_TICKET" || {
        flock -u 7; exec 7>&-; return 1;
    }
    flock -u 7
    exec 7>&-

    cleanup_transfer_ticket() {
        rm -f "$TRANSFER_ACTIVE_TICKET"
        if [[ "$TRANSFER_LEASE_HELD" == true ]]; then
            rm -f "$SYNC_LOCK_OWNER_FILE"
        fi
    }
    trap cleanup_transfer_ticket EXIT
    trap 'exit 143' HUP INT TERM

    while :; do
        exec 7>>"$TRANSFER_QUEUE_LOCK_FILE" || return 1
        flock -x 7 || { exec 7>&-; return 1; }
        queue_head=$(transfer_queue_head || true)
        if [[ "$queue_head" == "$TRANSFER_ACTIVE_TICKET" ]]; then
            exec 8>>"$SYNC_LOCK_FILE" || { flock -u 7; exec 7>&-; return 1; }
            if flock -n 8; then
                # Recovery of a staged publish may itself move files, so do it
                # only after this SSH session owns the global transfer lease.
                exec 9>>"$JOURNAL_LOCK_FILE" || { flock -u 8; exec 8>&-; flock -u 7; exec 7>&-; return 1; }
                if ! flock -x 9 || ! recover_pending_transactions_unlocked; then
                    flock -u 9; exec 9>&-
                    flock -u 8; exec 8>&-
                    flock -u 7; exec 7>&-
                    return 1
                fi
                flock -u 9
                exec 9>&-
                rm -f "$TRANSFER_ACTIVE_TICKET"
                owner_payload="$device|$owner_host|$(date '+%s')|||0|0"
                owner_tmp="${SYNC_LOCK_OWNER_FILE}.$$"
                printf '%s\n' "$owner_payload" > "$owner_tmp" && mv -f "$owner_tmp" "$SYNC_LOCK_OWNER_FILE" || {
                    flock -u 8; exec 8>&-; flock -u 7; exec 7>&-; return 1;
                }
                TRANSFER_LEASE_HELD=true
                flock -u 7
                exec 7>&-
                printf 'NASBOX_LOCKED\n'
                # The client keeps this SSH session open for the whole lease.
                # Activity frames update only its diagnostics record; they never
                # affect the lock itself or filesystem/journal mutations.
                while IFS= read -r -d '' magic; do
                    [[ "$magic" == "ACTIVITY_V1" ]] || continue
                    IFS= read -r -d '' phase || break
                    IFS= read -r -d '' done || break
                    IFS= read -r -d '' total || break
                    [[ "$phase" =~ ^[a-z_]{1,32}$ ]] || continue
                    [[ "$done" =~ ^[0-9]+$ && "$total" =~ ^[0-9]+$ ]] || continue
                    owner_payload="$device|$owner_host|$(date '+%s')|$phase|$done|$total"
                    owner_tmp="${SYNC_LOCK_OWNER_FILE}.$$"
                    printf '%s\n' "$owner_payload" > "$owner_tmp" && mv -f "$owner_tmp" "$SYNC_LOCK_OWNER_FILE"
                done
                flock -u 8
                exec 8>&-
                return 0
            fi
            exec 8>&-
        fi
        flock -u 7
        exec 7>&-
        sleep 2
    done
}

cmd_status() {
    echo "sync-daemon-server v$VERSION"
    printf '%-18s %s\n' "Script:" "$SCRIPT_PATH"
    printf '%-18s %s\n' "Config:" "$CONFIG_FILE"
    printf '%-18s %s\n' "Cartella NASBox:" "${SHARE_ROOT:-<non caricata>}"
    if is_running; then
        printf '%-18s %s\n' "Demone:" "IN ESECUZIONE (PID $(cat "$PID_FILE"), intervallo ${CHECK_INTERVAL_MINUTES} min)"
    else
        printf '%-18s %s\n' "Demone:" "FERMO"
    fi
    if [[ -f "$STAMP_FILE" ]]; then
        local last
        last=$(date -d "@$(cat "$STAMP_FILE")" '+%Y-%m-%d %H:%M:%S' 2>/dev/null || cat "$STAMP_FILE")
        printf '%-18s %s\n' "Ultimo pass:" "$last"
    else
        printf '%-18s %s\n' "Ultimo pass:" "mai"
    fi
    local trash size
    trash="$(trash_dir)"
    if [[ -d "$trash" ]]; then
        size="$(human_size "$trash")"
    else
        size="(nessuno storico)"
    fi
    # "giorni" and "spazio" spelled out on purpose -- a bare "90g" next to a
    # disk size like "59G" reads as if both were the same unit (they aren't:
    # one's a day count, the other's an actual du -sh size) and that's
    # exactly the mix-up this used to invite.
    printf '%-18s %s giorni\n' "Retention:" "$RETENTION_DAYS"
    if (( TOMBSTONE_TTL_DAYS > 0 )); then
        printf '%-18s %s giorni\n' "Tombstone TTL:" "$TOMBSTONE_TTL_DAYS"
    fi
    printf '%-18s %s\n' "Storico (spazio):" "$size"
    printf '%-18s %s\n' "Journal (spazio):" "$(wc -c < "$JOURNAL_FILE" 2>/dev/null || echo 0) byte"
    printf '%-18s %s\n' "Manifest (spazio):" "$(wc -c < "$MANIFEST_FILE" 2>/dev/null || echo 0) byte"
    if [[ -f "$LOG_FILE" ]]; then
        printf '%-18s %s\n' "Log server (spazio):" "$(wc -c < "$LOG_FILE" 2>/dev/null || echo 0) byte"
    else
        printf '%-18s %s\n' "Log server (spazio):" "0 byte"
    fi
    if [[ -f "$EVENTS_FILE" ]]; then
        printf '%-18s %s\n' "Eventi (spazio):" "$(wc -c < "$EVENTS_FILE" 2>/dev/null || echo 0) byte"
    else
        printf '%-18s %s\n' "Eventi (spazio):" "0 byte"
    fi
    read_sync_lock_diagnostics
    if [[ "$SYNC_LOCK_HELD" == "true" ]]; then
        printf '%-18s occupato (host %s, device %s, PID %s, età %s secondi, fase %s %s/%s)\n' \
            "Lock trasferimenti:" "${SYNC_LOCK_OWNER_HOST:-sconosciuto}" \
            "${SYNC_LOCK_OWNER_ID:-sconosciuto}" "${SYNC_LOCK_OWNER_PID:-sconosciuto}" "$SYNC_LOCK_AGE_SECONDS" \
            "${SYNC_LOCK_PHASE:-sconosciuta}" "$SYNC_LOCK_PROGRESS_DONE" "$SYNC_LOCK_PROGRESS_TOTAL"
    else
        printf '%-18s libero\n' "Lock trasferimenti:"
    fi
}

cmd_print_config() {
    # Machine-readable KEY=VALUE dump for clients to auto-configure themselves
    # from this NAS's real, authoritative config -- instead of the same values
    # being retyped by hand in every client and drifting out of sync.
    echo "VERSION=$VERSION"
    if is_running; then
        echo "RUNNING=true"
    else
        echo "RUNNING=false"
    fi
    echo "SHARE_ROOT=$SHARE_ROOT"
    repository_id="$(read_repository_id 2>/dev/null || true)"
    if [[ -n "$repository_id" ]]; then
        echo "REPOSITORY_ID=$repository_id"
        echo "REPOSITORY_READY=true"
    else
        echo "REPOSITORY_ID="
        echo "REPOSITORY_READY=false"
    fi
    echo "RETENTION_DAYS=$RETENTION_DAYS"
    echo "TRASH_MAX_MB=$TRASH_MAX_MB"
    echo "TRASH_MIN_KEEP_DAYS=$TRASH_MIN_KEEP_DAYS"
    local usage_bytes="" usage_files="" usage_at=""
    if [[ -r "$(trash_usage_file)" ]]; then
        read -r usage_bytes usage_files usage_at < "$(trash_usage_file)" || true
    fi
    echo "TRASH_USED_BYTES=${usage_bytes}"
    echo "TRASH_FILES=${usage_files}"
    echo "TRASH_USAGE_AT=${usage_at}"
    echo "TOMBSTONE_TTL_DAYS=$TOMBSTONE_TTL_DAYS"
    echo "SYNC_LOCK_MAX_AGE_MINUTES=0"
    read_sync_lock_diagnostics
    echo "SYNC_LOCK_HELD=$SYNC_LOCK_HELD"
    echo "SYNC_LOCK_AGE_SECONDS=$SYNC_LOCK_AGE_SECONDS"
    echo "SYNC_LOCK_OWNER_PID=$SYNC_LOCK_OWNER_PID"
    echo "SYNC_LOCK_OWNER_ID=$SYNC_LOCK_OWNER_ID"
    echo "SYNC_LOCK_OWNER_HOST=$SYNC_LOCK_OWNER_HOST"
    echo "SYNC_LOCK_STARTED_AT=$SYNC_LOCK_STARTED_AT"
    echo "SYNC_LOCK_PHASE=$SYNC_LOCK_PHASE"
    echo "SYNC_LOCK_PROGRESS_DONE=$SYNC_LOCK_PROGRESS_DONE"
    echo "SYNC_LOCK_PROGRESS_TOTAL=$SYNC_LOCK_PROGRESS_TOTAL"
    echo "CHECK_INTERVAL_MINUTES=$CHECK_INTERVAL_MINUTES"
    echo "PRUNE_MAX_FILES_PER_PASS=$PRUNE_MAX_FILES_PER_PASS"
    echo "JOURNAL_MAX_BYTES=$JOURNAL_MAX_BYTES"
    echo "JOURNAL_MAX_LINES=$JOURNAL_MAX_LINES"
    echo "LOG_MAX_BYTES=$LOG_MAX_BYTES"
    echo "LOG_RETENTION_DAYS=$LOG_RETENTION_DAYS"
    echo "EVENTS_MAX_BYTES=$EVENTS_MAX_BYTES"
    echo "STATE_TEMP_MAX_AGE_HOURS=$STATE_TEMP_MAX_AGE_HOURS"
    print_capability_values
    if [[ -f "$JOURNAL_FILE" ]]; then echo "JOURNAL_READY=true"; else echo "JOURNAL_READY=false"; fi
    # Own runtime state dir (PID file, lock, log) -- self-reported so clients whose
    # synced tree happens to contain this install can exclude it from sync instead
    # of guessing its location. Nothing local ever corresponds to these files (they
    # only ever exist because the daemon is running here), so an ordinary push with
    # deletions enabled would otherwise delete them as a side effect, making this
    # daemon's own PID file vanish -- clients that auto-restart on "not running"
    # (--print-config's RUNNING=false) could then stack a duplicate on top of one
    # that's actually still alive.
    echo "STATE_DIR=$STATE_DIR"
    # Held by upgraded clients over a persistent SSH session while one whole
    # rsync transaction is active. Closing that session releases flock safely.
    if command -v flock >/dev/null 2>&1; then
        echo "SYNC_LOCK_FILE=$SYNC_LOCK_FILE"
        echo "SYNC_LOCK_OWNER_FILE=$SYNC_LOCK_OWNER_FILE"
        echo "SYNC_LOCK_AVAILABLE=true"
    else
        echo "SYNC_LOCK_FILE="
        echo "SYNC_LOCK_AVAILABLE=false"
    fi
    if command -v sha256sum >/dev/null 2>&1 && command -v flock >/dev/null 2>&1; then
        echo "PATH_RECONCILIATION_AVAILABLE=true"
    else
        echo "PATH_RECONCILIATION_AVAILABLE=false"
    fi
    if command -v flock >/dev/null 2>&1; then
        echo "BROWSE_AVAILABLE=true"
    else
        echo "BROWSE_AVAILABLE=false"
    fi
}

cmd_history_list() {
    # NUL-delimited protocol: history names may legitimately contain spaces,
    # tabs, or newlines, so line-oriented output is not safe here.
    local trash
    trash="$(trash_dir)"
    printf 'HISTORY_V1\0'
    [[ -d "$trash" ]] || return 0
    while IFS= read -r -d '' file; do
        local rel base suffix original_base original size
        rel="${file#"$trash"/}"
        base="${file##*/}"
        [[ "$base" =~ $TS_SUFFIX_REGEX ]] || continue
        suffix="${BASH_REMATCH[1]}${BASH_REMATCH[2]}"
        original_base="${base%"-$suffix"}"
        if [[ "$rel" == */* ]]; then
            original="${rel%/*}/$original_base"
        else
            original="$original_base"
        fi
        size=$(stat -c '%s' "$file" 2>/dev/null || stat -f '%z' "$file" 2>/dev/null || echo 0)
        printf '%s\0%s\0%s\0%s\0' "$original" "$suffix" "$rel" "$size"
    done < <(find "$trash" -type f -print0 2>/dev/null)
}

cmd_diagnostics() {
    # Kept separate from --print-config: these filesystem scans can be costly
    # on a large history and are only requested explicitly by the GUI.
    local trash count disk_bytes available last_run disk_kb available_kb total_kb used_kb total used
    trash="$(trash_dir)"
    count=0
    disk_bytes=0
    if [[ -d "$trash" ]]; then
        count=$(find "$trash" -type f 2>/dev/null | wc -l | tr -d '[:space:]')
        disk_kb=$(du -sk "$trash" 2>/dev/null | cut -f1 || true)
        disk_bytes=$(( ${disk_kb:-0} * 1024 ))
    fi
    total_kb=$(df -Pk "$SHARE_ROOT" 2>/dev/null | awk 'NR==2 {print $2}' || true)
    used_kb=$(df -Pk "$SHARE_ROOT" 2>/dev/null | awk 'NR==2 {print $3}' || true)
    available_kb=$(df -Pk "$SHARE_ROOT" 2>/dev/null | awk 'NR==2 {print $4}' || true)
    total=$(( ${total_kb:-0} * 1024 ))
    used=$(( ${used_kb:-0} * 1024 ))
    available=$(( ${available_kb:-0} * 1024 ))
    last_run=$(cat "$STAMP_FILE" 2>/dev/null || echo 0)
    echo "VERSION=$VERSION"
    echo "TRASH_FILE_COUNT=$count"
    echo "TRASH_DISK_BYTES=$disk_bytes"
    echo "SHARE_TOTAL_BYTES=$total"
    echo "SHARE_USED_BYTES=$used"
    echo "SHARE_AVAILABLE_BYTES=$available"
    echo "LAST_RUN_EPOCH=$last_run"
    echo "JOURNAL_BYTES=$(wc -c < "$JOURNAL_FILE" 2>/dev/null || echo 0)"
    echo "MANIFEST_BYTES=$(wc -c < "$MANIFEST_FILE" 2>/dev/null || echo 0)"
    echo "SERVER_LOG_BYTES=$(wc -c < "$LOG_FILE" 2>/dev/null || echo 0)"
    echo "EVENTS_BYTES=$(wc -c < "$EVENTS_FILE" 2>/dev/null || echo 0)"
    echo "JOURNAL_MAX_BYTES=$JOURNAL_MAX_BYTES"
    echo "JOURNAL_MAX_LINES=$JOURNAL_MAX_LINES"
    echo "LOG_MAX_BYTES=$LOG_MAX_BYTES"
    echo "EVENTS_MAX_BYTES=$EVENTS_MAX_BYTES"
    if [[ -w "$STATE_DIR" ]]; then echo "STATE_WRITABLE=true"; else echo "STATE_WRITABLE=false"; fi
}

# Read-only multi-device snapshot derived from committed transactions and the
# current lease owner. The journal keeps one BEGIN row per transaction with
# device id and server timestamp; that is enough to show last activity without
# adding a new coordination channel between clients.
cmd_devices() {
    local now last=0 ops=0 device host="-" seen=0 active=""
    now=$(date '+%s')
    printf 'NASBOX_DEVICES_V1\n'
    if [[ -f "$JOURNAL_FILE" ]]; then
        awk -F '\t' '
            $1 == "BEGIN" && NF >= 4 {
                device = $4
                if (!(device in last) || $3 > last[device]) last[device] = $3
                ops[device]++
            }
            END {
                for (device in last) printf "%s|%s|%d\n", device, last[device], ops[device]
            }
        ' "$JOURNAL_FILE" | sort -t'|' -k2,2nr
    fi
    if [[ -f "$SYNC_LOCK_OWNER_FILE" ]]; then
        IFS='|' read -r active host seen _ < "$SYNC_LOCK_OWNER_FILE" || true
        # Do not call sync_transfer_lock_is_held() here: it opens (and creates)
        # the lock file, while --devices must stay strictly read-only.
        if [[ -n "$active" && "$seen" =~ ^[0-9]+$ ]] && (( now - seen <= 300 )); then
            printf '%s|%s|%d|ACTIVE\n' "$active" "${host:--}" "$seen"
        fi
    fi
    return 0
}

cmd_metrics() {
    # Deliberately read only: this endpoint never walks SHARE_ROOT. The client
    # derives rates from cumulative counters across successive samples.
    local uptime load1 load5 load15 cpu_values memory_values disk_values io_values network_values
    local lock_held="false" lock_owner="" lock_host="" lock_phase="" lock_started="0"
    local lock_done="0" lock_total="0" queue_count="0" staging_count="0" journal_bytes="0" manifest_bytes="0"
    uptime=$(awk '{printf "%.0f", $1; exit}' /proc/uptime 2>/dev/null || true)
    read -r load1 load5 load15 _ < /proc/loadavg 2>/dev/null || true
    cpu_values=$(awk '$1 == "cpu" {
        total = $2 + $3 + $4 + $5 + $6 + $7 + $8 + $9
        idle = $5 + $6
        printf "%.0f %.0f %.0f", total, idle, $6
        exit
    }' /proc/stat 2>/dev/null || true)
    memory_values=$(awk '
        $1 == "MemTotal:" { total = $2 }
        $1 == "MemAvailable:" { available = $2 }
        $1 == "MemFree:" { free = $2 }
        $1 == "Buffers:" { buffers = $2 }
        $1 == "Cached:" { cached = $2 }
        $1 == "SwapTotal:" { swap_total = $2 }
        $1 == "SwapFree:" { swap_free = $2 }
        END {
            if (!available) available = free + buffers + cached
            printf "%.0f %.0f %.0f %.0f", total * 1024, available * 1024,
                swap_total * 1024, swap_free * 1024
        }
    ' /proc/meminfo 2>/dev/null || true)
    disk_values=$(df -Pk "$SHARE_ROOT" 2>/dev/null | awk 'NR == 2 {
        total = $2 * 1024; used = $3 * 1024; available = $4 * 1024
        percent = $5; sub(/%$/, "", percent)
        printf "%.0f %.0f %.0f %s", total, used, available, percent
    }' || true)
    io_values=$(awk '$3 ~ /^(sd|hd|vd|xvd|nvme|mmcblk|md|dm-)/ {
        reads += $6; writes += $10
    }
    END { printf "%.0f %.0f", reads * 512, writes * 512 }' /proc/diskstats 2>/dev/null || true)
    network_values=$(awk '
        NR > 2 {
            interface = $1
            sub(/:$/, "", interface)
            if (interface != "lo" && interface != "") {
                rx_bytes += $2; rx_packets += $3
                tx_bytes += $10; tx_packets += $11
                interfaces++
            }
        }
        END { printf "%.0f %.0f %.0f %.0f %d", rx_bytes, tx_bytes,
            rx_packets, tx_packets, interfaces }
    ' /proc/net/dev 2>/dev/null || true)

    if [[ -e "$SYNC_LOCK_FILE" ]]; then
        exec 8>>"$SYNC_LOCK_FILE" 2>/dev/null || true
        if ! flock -n 8 2>/dev/null; then
            lock_held="true"
            if [[ -f "$SYNC_LOCK_OWNER_FILE" ]]; then
                IFS='|' read -r lock_owner lock_host lock_started lock_phase lock_done lock_total _ \
                    < "$SYNC_LOCK_OWNER_FILE" || true
            fi
        else
            flock -u 8 2>/dev/null || true
        fi
        exec 8>&- 2>/dev/null || true
    fi
    if [[ -d "$TRANSFER_QUEUE_DIR" ]]; then
        queue_count=$(find "$TRANSFER_QUEUE_DIR" -maxdepth 1 -type f -name '*.ticket' 2>/dev/null | wc -l | tr -d '[:space:]')
    fi
    if [[ -d "$SHARE_ROOT/$STAGING_DIRNAME" ]]; then
        staging_count=$(find "$SHARE_ROOT/$STAGING_DIRNAME" -mindepth 1 -maxdepth 1 -type d 2>/dev/null | wc -l | tr -d '[:space:]')
    fi
    journal_bytes=$(wc -c < "$JOURNAL_FILE" 2>/dev/null || echo 0)
    manifest_bytes=$(wc -c < "$MANIFEST_FILE" 2>/dev/null || echo 0)

    # Keep every value on a simple KEY=VALUE line. Unknown future keys can be
    # ignored by older clients without making this read-only command unsafe.
    printf 'NASBOX_METRICS_V1\n'
    printf 'COLLECTED_AT_EPOCH=%s\n' "$(date '+%s')"
    printf 'UPTIME_SECONDS=%s\n' "${uptime:-0}"
    printf 'LOAD_1=%s\nLOAD_5=%s\nLOAD_15=%s\n' "${load1:-0}" "${load5:-0}" "${load15:-0}"
    read -r cpu_total_ticks cpu_idle_ticks cpu_iowait_ticks <<< "${cpu_values:-0 0 0}"
    printf 'CPU_TOTAL_TICKS=%s\nCPU_IDLE_TICKS=%s\nCPU_IOWAIT_TICKS=%s\n' \
        "${cpu_total_ticks:-0}" "${cpu_idle_ticks:-0}" "${cpu_iowait_ticks:-0}"
    read -r memory_total memory_available swap_total swap_free <<< "${memory_values:-0 0 0 0}"
    printf 'MEM_TOTAL_BYTES=%s\nMEM_AVAILABLE_BYTES=%s\n' "${memory_total:-0}" "${memory_available:-0}"
    printf 'SWAP_TOTAL_BYTES=%s\nSWAP_FREE_BYTES=%s\n' "${swap_total:-0}" "${swap_free:-0}"
    read -r disk_total disk_used disk_available disk_percent <<< "${disk_values:-0 0 0 0}"
    printf 'DISK_TOTAL_BYTES=%s\nDISK_USED_BYTES=%s\nDISK_AVAILABLE_BYTES=%s\nDISK_USAGE_PERCENT=%s\n' \
        "${disk_total:-0}" "${disk_used:-0}" "${disk_available:-0}" "${disk_percent:-0}"
    read -r disk_read_bytes disk_write_bytes <<< "${io_values:-0 0}"
    printf 'DISK_READ_BYTES=%s\nDISK_WRITE_BYTES=%s\n' "${disk_read_bytes:-0}" "${disk_write_bytes:-0}"
    read -r net_rx net_tx net_rx_packets net_tx_packets net_interfaces <<< "${network_values:-0 0 0 0 0}"
    printf 'NET_RX_BYTES=%s\nNET_TX_BYTES=%s\nNET_RX_PACKETS=%s\nNET_TX_PACKETS=%s\nNET_INTERFACE_COUNT=%s\n' \
        "${net_rx:-0}" "${net_tx:-0}" "${net_rx_packets:-0}" "${net_tx_packets:-0}" "${net_interfaces:-0}"
    printf 'NASBOX_LOCK_HELD=%s\nNASBOX_LOCK_OWNER=%s\nNASBOX_LOCK_HOST=%s\n' \
        "$lock_held" "${lock_owner:-}" "${lock_host:-}"
    printf 'NASBOX_LOCK_PHASE=%s\nNASBOX_LOCK_STARTED_AT=%s\n' \
        "${lock_phase:-}" "${lock_started:-0}"
    printf 'NASBOX_LOCK_PROGRESS_DONE=%s\nNASBOX_LOCK_PROGRESS_TOTAL=%s\n' \
        "${lock_done:-0}" "${lock_total:-0}"
    printf 'NASBOX_QUEUE_COUNT=%s\nNASBOX_STAGING_COUNT=%s\nJOURNAL_BYTES=%s\nMANIFEST_BYTES=%s\n' \
        "${queue_count:-0}" "${staging_count:-0}" "${journal_bytes:-0}" "${manifest_bytes:-0}"
}

cmd_help() {
    cat <<EOF
sync-daemon-server v$VERSION

Demone autonomo (nessun cron/Task Scheduler) per il pruning dello storico/cestino
(.sync-trash) della cartella NASBox su questo NAS. Pacchetto autocontenuto: config,
log e PID file vivono in questa stessa cartella.

Uso:
  $(basename "$0") --start                     Avvia il demone in background
  $(basename "$0") --stop                      Ferma il demone
  $(basename "$0") --restart                   Riavvia
  $(basename "$0") --status                    Stato + storico/retention + ultimo pass
  $(basename "$0") --run-foreground            Esegue il loop in questo terminale
  $(basename "$0") --run-once [--dry-run]      Un solo pass di pruning, poi esce
  $(basename "$0") --metrics                   Metriche read-only del carico NAS
  $(basename "$0") --devices                   Dispositivi visti di recente e lease attivo
  $(basename "$0") --wait-for-revision REVISION TIMEOUT
                                                   Attende solo manifest.revision
  $(basename "$0") --print-config              Dump KEY=VALUE (per l'auto-configurazione dei client)
  $(basename "$0") --capabilities              Cosa sa fare questo NAS (KEY=VALUE)
  $(basename "$0") --audit-orphans [--list]    File sul disco senza riga nel manifest
  $(basename "$0") --audit-adopt [--dry-run]   Registra nel journal quei file (digest reali)
  $(basename "$0") --journal-forget            Mette a tombstone percorsi (input NUL, disco intatto)
  $(basename "$0") --init-repository           Crea il marker persistente del repository
  $(basename "$0") --journal-append             Registra una transazione (input NUL)
  $(basename "$0") --journal-compact            Compatta il journal nel manifest
  $(basename "$0") --manifest-export [SINCE]    Esporta il manifest corrente; con SINCE, la delta
   $(basename "$0") --manifest-export-delta SINCE
                                                 Esporta solo le modifiche dalla revisione SINCE
  $(basename "$0") --manifest-get PATH          Legge lo stato di un percorso
  $(basename "$0") --file-states               Stato batch file/tombstone (input/output NUL)
   $(basename "$0") --checked-delete            Cancella solo la baseline attesa (input/output NUL)
   $(basename "$0") --restore-batch             Ripristina file dal cestino (input/output NUL)
   $(basename "$0") --staging-publish           Pubblica nuovi file staged (input/output NUL, lock client richiesto)
  $(basename "$0") --journal-status             Stato e dimensione journal/manifest
  $(basename "$0") --history-list              Elenco NUL dello storico sul NAS (per il client)
  $(basename "$0") --diagnostics               Diagnostica KEY=VALUE del NAS (per il client)
  $(basename "$0") --transfer-wait             Attende un ticket globale e mantiene il lock SSH
  $(basename "$0") -c FILE                     Usa un file di config alternativo
  $(basename "$0") --help

Config: $CONFIG_FILE
Log:    $LOG_FILE
EOF
}

# --- entry point ---

ACTION="status"

while [[ $# -gt 0 ]]; do
    case "$1" in
        --start) ACTION="start" ;;
        --stop) ACTION="stop" ;;
        --restart) ACTION="restart" ;;
        --run-foreground) ACTION="run_foreground" ;;
        --run-once) ACTION="run_once" ;;
        --metrics) ACTION="metrics" ;;
        --devices) ACTION="devices" ;;
        --wait-for-revision) shift; WAIT_REVISION="${1:-}"; shift; WAIT_TIMEOUT="${1:-}"; ACTION="wait_revision" ;;
        --dry-run) DRY_RUN="true" ;;
        --status) ACTION="status" ;;
        --print-config) ACTION="print_config" ;;
        --init-repository) ACTION="init_repository" ;;
        --journal-append) ACTION="journal_append" ;;
        --journal-compact) ACTION="journal_compact" ;;
        --manifest-export) shift; MANIFEST_SINCE="${1:-}"; ACTION="manifest_export" ;;
        --manifest-export-delta) shift; MANIFEST_SINCE="${1:-}"; ACTION="manifest_export_delta" ;;
        --manifest-get) shift; MANIFEST_PATH="${1:-}"; ACTION="manifest_get" ;;
        --file-states) ACTION="file_states" ;;
        --file-states-current) ACTION="file_states_current" ;;
        --checked-delete) ACTION="checked_delete" ;;
        --restore-batch) ACTION="restore_batch" ;;
        --staging-publish) ACTION="staging_publish" ;;
        --browse-list) shift; BROWSE_PATH="${1:-}"; ACTION="browse_list" ;;
        --browse-delete) ACTION="browse_delete" ;;
        --browse-rename) ACTION="browse_rename" ;;
        --journal-status) ACTION="journal_status" ;;
        --history-list) ACTION="history_list" ;;
        --diagnostics) ACTION="diagnostics" ;;
        --capabilities) ACTION="capabilities" ;;
        --audit-orphans) ACTION="audit_orphans" ;;
        --audit-adopt) ACTION="audit_adopt" ;;
        --journal-forget) ACTION="journal_forget" ;;
        --list) AUDIT_LIST="--list" ;;
        --transfer-wait) ACTION="transfer_wait" ;;
        -c|--config) shift; CONFIG_FILE="${1:?}" ;;
        --help|-h) cmd_help; exit 0 ;;
        *) echo "Opzione sconosciuta: $1" >&2; cmd_help; exit 1 ;;
    esac
    shift
done

if [[ "$ACTION" == "metrics" || "$ACTION" == "devices" ]]; then
    # A valid existing config is enough; unlike all daemon commands, metrics
    # must not create state/journal files as a side effect of being queried.
    [[ -f "$CONFIG_FILE" ]] || { printf 'Config non trovato: %s\n' "$CONFIG_FILE" >&2; exit 1; }
    load_config || exit $?
    if [[ "$ACTION" == "devices" ]]; then cmd_devices; else cmd_metrics; fi
    exit $?
fi

if [[ "$ACTION" == "wait_revision" ]]; then
    # This endpoint is intentionally independent from journal initialization
    # and from both locks. It only needs the configured state directory and
    # the single atomically replaced revision file.
    [[ -f "$CONFIG_FILE" ]] || { printf 'Config non trovato: %s\n' "$CONFIG_FILE" >&2; exit 1; }
    load_config || exit $?
    mkdir -p "$STATE_DIR" || exit 1
    cmd_wait_for_revision "${WAIT_REVISION:-}" "${WAIT_TIMEOUT:-}"
    exit $?
fi

mkdir -p "$STATE_DIR"
load_config
detect_capabilities
ensure_journal_files

case "$ACTION" in
    start) cmd_start ;;
    stop) cmd_stop ;;
    restart) cmd_restart ;;
    run_foreground) run_foreground ;;
    run_once) run_once_pass; exit $? ;;
    metrics) cmd_metrics; exit $? ;;
    devices) cmd_devices; exit $? ;;
    status) cmd_status; exit $? ;;
    print_config) cmd_print_config; exit $? ;;
    init_repository) cmd_init_repository; exit $? ;;
    journal_append) cmd_journal_append; exit $? ;;
    journal_compact) cmd_journal_compact; exit $? ;;
    manifest_export) if [[ -n "${MANIFEST_SINCE:-}" ]]; then cmd_manifest_export_delta "$MANIFEST_SINCE"; else cmd_manifest_export; fi; exit $? ;;
    manifest_export_delta) cmd_manifest_export_delta "${MANIFEST_SINCE:-}"; exit $? ;;
    manifest_get) cmd_manifest_get "$MANIFEST_PATH"; exit $? ;;
    file_states) cmd_file_states; exit $? ;;
    file_states_current) cmd_file_states false; exit $? ;;
    checked_delete) cmd_checked_delete; exit $? ;;
    restore_batch) cmd_restore_batch; exit $? ;;
    staging_publish) cmd_staging_publish; exit $? ;;
    browse_list) cmd_browse_list "$BROWSE_PATH"; exit $? ;;
    browse_delete) cmd_browse_delete; exit $? ;;
    browse_rename) cmd_browse_rename; exit $? ;;
    journal_status) cmd_journal_status; exit $? ;;
    history_list) cmd_history_list; exit $? ;;
    diagnostics) cmd_diagnostics; exit $? ;;
    capabilities) cmd_capabilities; exit $? ;;
    audit_orphans) cmd_audit_orphans "${AUDIT_LIST:-}"; exit $? ;;
    audit_adopt) cmd_audit_adopt; exit $? ;;
    journal_forget) cmd_journal_forget; exit $? ;;
    transfer_wait) cmd_transfer_wait; exit $? ;;
esac
