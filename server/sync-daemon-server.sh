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
#   sync-daemon-server.sh -c FILE               Use an alternate config file
#   sync-daemon-server.sh --help
#
set -uo pipefail

VERSION="3.12.0"
SCRIPT_PATH="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/$(basename "${BASH_SOURCE[0]}")"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

CONFIG_FILE="$SCRIPT_DIR/server.conf"
STATE_DIR="$SCRIPT_DIR/state"
LOG_FILE="$STATE_DIR/server.log"
EVENTS_FILE="$STATE_DIR/events.jsonl"
PID_FILE="$STATE_DIR/daemon.pid"
DAEMON_LOCK_FILE="$STATE_DIR/daemon.lock"
STAMP_FILE="$STATE_DIR/last-run"
SYNC_LOCK_FILE="$STATE_DIR/sync-transfer.lock"
SYNC_LOCK_OWNER_FILE="$STATE_DIR/sync-transfer.lock.owner"
JOURNAL_FILE="$STATE_DIR/transfer-journal.tsv"
MANIFEST_FILE="$STATE_DIR/manifest.tsv"
JOURNAL_LOCK_FILE="$STATE_DIR/transfer-journal.lock"
MANIFEST_REVISION_FILE="$STATE_DIR/manifest.revision"

# How many sha256sum/stat lookups cmd_file_states runs concurrently -- see
# that function for why. 8 is a conservative default: enough to stop a single
# subprocess-per-file from being the bottleneck on a large batch, low enough
# to not thrash a weak NAS CPU or a spinning disk's seek time with too much
# concurrent I/O.
FILE_STATES_PARALLELISM=8

TRASH_DIRNAME=".sync-trash"
REPOSITORY_MARKER_NAME=".nasbox-root"
DRY_RUN="false"

# --- logging ---

test_failpoint() {
    # Crash injection is opt-in and only used by isolated subprocess tests.
    if [[ -n "${NASBOX_TEST_FAILPOINT:-}" && "${NASBOX_TEST_FAILPOINT}" == "$1" ]]; then
        kill -KILL "$$"
    fi
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

log_event() {
    local action="$1" path_="$2" detail="${3:-}"
    mkdir -p "$STATE_DIR"
    local ts
    ts=$(date '+%s')
    local escaped_path="${path_//\\/\\\\}"
    escaped_path="${escaped_path//\"/\\\"}"
    escaped_path="${escaped_path//$'\n'/\\n}"
    escaped_path="${escaped_path//$'\r'/\\r}"
    escaped_path="${escaped_path//$'\t'/\\t}"
    escaped_path="${escaped_path//$'\b'/\\b}"
    escaped_path="${escaped_path//$'\f'/\\f}"
    local escaped_detail="${detail//\\/\\\\}"
    escaped_detail="${escaped_detail//\"/\\\"}"
    escaped_detail="${escaped_detail//$'\n'/\\n}"
    escaped_detail="${escaped_detail//$'\r'/\\r}"
    escaped_detail="${escaped_detail//$'\t'/\\t}"
    escaped_detail="${escaped_detail//$'\b'/\\b}"
    escaped_detail="${escaped_detail//$'\f'/\\f}"
    printf '{"ts": %s, "action": "%s", "path": "%s", "detail": "%s"}\n' \
        "$ts" "$action" "$escaped_path" "$escaped_detail" >> "$EVENTS_FILE"
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
    LOG_MAX_BYTES=5242880
    JOURNAL_MAX_BYTES=10485760
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
            *) log ERROR "Chiave non supportata in $CONFIG_FILE: $key"; exit 1 ;;
        esac
    done < "$CONFIG_FILE"

    [[ -n "$SHARE_ROOT" ]] || { log ERROR "SHARE_ROOT non impostato in $CONFIG_FILE"; exit 1; }
    [[ "$RETENTION_DAYS" =~ ^[0-9]+$ ]] || { log ERROR "RETENTION_DAYS deve essere un intero >= 0"; exit 1; }
    [[ "$TOMBSTONE_TTL_DAYS" =~ ^[0-9]+$ ]] || { log ERROR "TOMBSTONE_TTL_DAYS deve essere un intero >= 0"; exit 1; }
    [[ "$SYNC_LOCK_MAX_AGE_MINUTES" =~ ^[0-9]+$ ]] || { log ERROR "SYNC_LOCK_MAX_AGE_MINUTES deve essere un intero >= 0"; exit 1; }
    [[ "$CHECK_INTERVAL_MINUTES" =~ ^[1-9][0-9]*$ ]] || { log ERROR "CHECK_INTERVAL_MINUTES deve essere un intero >= 1"; exit 1; }
    [[ "$PRUNE_MAX_FILES_PER_PASS" =~ ^[1-9][0-9]*$ ]] || { log ERROR "PRUNE_MAX_FILES_PER_PASS deve essere un intero >= 1"; exit 1; }
    [[ "$LOG_MAX_BYTES" =~ ^[1-9][0-9]*$ ]] || { log ERROR "LOG_MAX_BYTES deve essere un intero >= 1"; exit 1; }
    [[ "$JOURNAL_MAX_BYTES" =~ ^[1-9][0-9]*$ ]] || { log ERROR "JOURNAL_MAX_BYTES deve essere un intero >= 1"; exit 1; }

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

bump_manifest_revision_unlocked() {
    local revision tmp
    revision=$(cat "$MANIFEST_REVISION_FILE" 2>/dev/null || echo 0)
    [[ "$revision" =~ ^[0-9]+$ ]] || revision=0
    revision=$((revision + 1))
    tmp="${MANIFEST_REVISION_FILE}.tmp.$$"
    printf '%s\n' "$revision" > "$tmp" && mv -f "$tmp" "$MANIFEST_REVISION_FILE"
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

compact_manifest_unlocked() {
    ensure_journal_files
    local journal_lines tombstone_cutoff_seconds now_epoch
    journal_lines=$(wc -l < "$JOURNAL_FILE" 2>/dev/null || echo 0)
    if (( journal_lines <= 1 )); then
        # Sweep old tombstones even when no new journal transaction arrived.
        if (( TOMBSTONE_TTL_DAYS > 0 )); then
            now_epoch=$(date '+%s')
            tombstone_cutoff_seconds=$(( TOMBSTONE_TTL_DAYS * 86400 ))
            local tmp_sweep="${MANIFEST_FILE}.sweep.$$"
            awk -F '\t' -v now="$now_epoch" -v cutoff="$tombstone_cutoff_seconds" '
                NR == 1 { print; next }
                {
                    if ($2 == "" && $3 == "0" && NF >= 6) {
                        age = now - int($6)
                        if (age > cutoff) next
                    }
                    print
                }
            ' "$MANIFEST_FILE" > "$tmp_sweep" && mv -f "$tmp_sweep" "$MANIFEST_FILE" || { rm -f "$tmp_sweep"; return 1; }
        fi
        return 0
    fi
    local tmp="${MANIFEST_FILE}.tmp.$$"
    now_epoch=$(date '+%s')
    tombstone_cutoff_seconds=$(( TOMBSTONE_TTL_DAYS * 86400 ))
    if ! awk -F '\t' -v now="$now_epoch" -v cutoff="$tombstone_cutoff_seconds" '
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
                        state[fields[2]] = fields[2] FS "" FS "0" FS "0" FS fields[6] FS fields[7]
                    } else if (fields[1] == "PUT" && length(fields[2]) > 0) {
                        state[fields[2]] = fields[2] FS fields[3] FS fields[4] FS fields[5] FS fields[6] FS fields[7]
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
    ' "$MANIFEST_FILE" "$JOURNAL_FILE" > "$tmp"; then
        rm -f "$tmp"
        return 1
    fi
    mv -f "$tmp" "$MANIFEST_FILE" || { rm -f "$tmp"; return 1; }
    printf 'NASBOX_JOURNAL_V1\n' > "${JOURNAL_FILE}.tmp" || { rm -f "${JOURNAL_FILE}.tmp"; return 1; }
    mv -f "${JOURNAL_FILE}.tmp" "$JOURNAL_FILE"
}

cmd_journal_append() {
    ensure_journal_files
    local actual_repository_id payload_repository_id=""
    actual_repository_id=$(read_repository_id 2>/dev/null) || {
        echo "journal: repository marker assente o non valido" >&2
        return 1
    }
    local magic txid device timestamp count action path digest size mtime server_timestamp
    local tmp="${JOURNAL_FILE}.append.$$"
    IFS= read -r -d '' magic || { echo "journal: intestazione mancante" >&2; return 1; }
    if [[ "$magic" == "JOURNAL_V2" ]]; then
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
            [[ "$action" == "PUT" || "$action" == "DELETE" ]] || { rm -f "$tmp"; return 1; }
            [[ -n "$path" ]] || { rm -f "$tmp"; return 1; }
            [[ -z "$digest" || "$digest" =~ ^[0-9a-f]{64}$ ]] || { rm -f "$tmp"; return 1; }
            [[ "$size" =~ ^[0-9]+$ && "$mtime" =~ ^[0-9]+$ ]] || { rm -f "$tmp"; return 1; }
            printf '%s\t%s\t%s\t%s\t%s\t%s\t%s\n' \
                "$action" "$(encode_journal_field "$path")" "$digest" "$size" "$mtime" \
                "$(encode_journal_field "$device")" "$server_timestamp"
        done
        printf 'COMMIT\t%s\n' "$txid"
    } > "$tmp" || { rm -f "$tmp"; return 1; }

    if ! command -v flock >/dev/null 2>&1; then
        rm -f "$tmp"
        echo "journal: flock non disponibile" >&2
        return 1
    fi
    exec 9>"$JOURNAL_LOCK_FILE"
    if ! flock -x 9; then
        rm -f "$tmp"
        exec 9>&-
        return 1
    fi
    cat "$tmp" >> "$JOURNAL_FILE"
    local append_status=$?
    rm -f "$tmp"
    if (( append_status != 0 )); then
        flock -u 9
        exec 9>&-
        return 1
    fi
    if ! bump_manifest_revision_unlocked; then
        flock -u 9
        exec 9>&-
        return 1
    fi
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
    exec 9>"$JOURNAL_LOCK_FILE"
    flock -x 9 || { exec 9>&-; return 1; }
    compact_manifest_unlocked
    local status=$?
    flock -u 9
    exec 9>&-
    return "$status"
}

cmd_manifest_export() {
    ensure_journal_files
    cmd_journal_compact >/dev/null || return 1
    cat "$MANIFEST_FILE"
}

cmd_manifest_get() {
    local path="${1:-}" encoded
    [[ -n "$path" && "$path" != /* && "/$path/" != */../* ]] || {
        echo "MANIFEST_INVALID" >&2
        return 1
    }
    encoded="$(encode_journal_field "$path")"
    cmd_journal_compact >/dev/null || return 1
    awk -F '\t' -v wanted="$encoded" '
        FNR == 1 && $0 == "NASBOX_MANIFEST_V1" { next }
        $1 == wanted { print "MANIFEST_HIT\t" $0; found = 1; exit }
        END { if (!found) print "MANIFEST_MISS" }
    ' "$MANIFEST_FILE"
}

valid_sync_relative_path() {
    local path="$1" part current remaining
    [[ -n "$path" && "$path" != /* && "/$path/" != */../* ]] || return 1
    case "/$path/" in
        */.sync-trash/*|*/.sync-partial/*|*/.nasbox-root/*|*/@eaDir/*) return 1 ;;
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
# cmd_file_states/FILE_STATES_PARALLELISM.
_file_state_entry() {
    local path="$1" target digest size mtime encoded manifest_line tombstone_ts
    target="$SHARE_ROOT/$path"
    if [[ -f "$target" && ! -L "$target" ]]; then
        digest=$(portable_sha256 "$target" || true)
        size=$(portable_file_size "$target" || true)
        mtime=$(portable_file_mtime "$target" || true)
        [[ "$digest" =~ ^[0-9a-f]{64}$ && "$size" =~ ^[0-9]+$ && "$mtime" =~ ^[0-9]+$ ]] || return 1
        printf '%s\0FILE\0%s\0%s\0%s\0' "$path" "$digest" "$size" "$((mtime * 1000000000))"
    elif [[ -e "$target" || -L "$target" ]]; then
        printf '%s\0%s\0%s\0%s\0%s\0' "$path" "OTHER" "" "0" "0"
    else
        encoded=$(encode_journal_field "$path")
        manifest_line=$(awk -F '\t' -v wanted="$encoded" '$1 == wanted { print; exit }' "$MANIFEST_FILE")
        if [[ -n "$manifest_line" ]]; then
            tombstone_ts=$(printf '%s\n' "$manifest_line" | awk -F '\t' '{ print $6 }')
            [[ "$tombstone_ts" =~ ^[0-9]+$ ]] || tombstone_ts=$(date '+%s')
            printf '%s\0%s\0%s\0%s\0%s\0' \
                "$path" "TOMBSTONE" "" "0" "$((tombstone_ts * 1000000000))"
        else
            printf '%s\0%s\0%s\0%s\0%s\0' "$path" "ABSENT" "" "0" "0"
        fi
    fi
}

cmd_file_states() {
    local compact_first="${1:-true}"
    command -v sha256sum >/dev/null 2>&1 || {
        echo "file-states: sha256sum non disponibile" >&2
        return 1
    }
    local magic count path index
    local -a paths
    IFS= read -r -d '' magic || return 1
    IFS= read -r -d '' count || return 1
    [[ "$magic" == "FILE_STATES_V1" ]] || return 1
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
        printf 'FILE_STATES_V1\0%s\0' "$count"
        return 0
    fi

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
        _file_state_entry "${paths[index]}" > "$tmpdir/$index" 2>/dev/null &
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

    printf 'FILE_STATES_V1\0%s\0' "$count"
    for ((index = 0; index < count; index++)); do
        cat "$tmpdir/$index"
    done
    rm -rf "$tmpdir"
}

append_delete_journal_unlocked() {
    local path="$1" device="$2" server_timestamp txid tmp journal_backup old_revision revision_tmp
    tmp="${JOURNAL_FILE}.delete.$$.$RANDOM"
    journal_backup="${JOURNAL_FILE}.rollback.$$.$RANDOM"
    revision_tmp="${MANIFEST_REVISION_FILE}.rollback.$$.$RANDOM"
    old_revision=$(cat "$MANIFEST_REVISION_FILE" 2>/dev/null || echo 0)
    cp "$JOURNAL_FILE" "$journal_backup" || return 1
    server_timestamp=$(date '+%s')
    txid="delete-${server_timestamp}-$$-${RANDOM}"
    {
        printf 'BEGIN\t%s\t%s\t%s\n' "$txid" "$server_timestamp" "$device"
        printf 'DELETE\t%s\t\t0\t0\t%s\t%s\n' \
            "$(encode_journal_field "$path")" "$(encode_journal_field "$device")" "$server_timestamp"
        printf 'COMMIT\t%s\n' "$txid"
    } > "$tmp" || { rm -f "$tmp" "$journal_backup"; return 1; }
    if ! cat "$tmp" >> "$JOURNAL_FILE"; then
        cp "$journal_backup" "$JOURNAL_FILE" 2>/dev/null || true
        rm -f "$tmp" "$journal_backup"
        return 1
    fi
    test_failpoint "journal_after_append"
    rm -f "$tmp"
    if bump_manifest_revision_unlocked; then
        rm -f "$journal_backup"
        return 0
    fi
    cp "$journal_backup" "$JOURNAL_FILE" 2>/dev/null || true
    printf '%s\n' "$old_revision" > "$revision_tmp" && mv -f "$revision_tmp" "$MANIFEST_REVISION_FILE"
    rm -f "$journal_backup" "$revision_tmp"
    return 1
}

append_delete_journal_batch_unlocked() {
    local paths_file="$1" device="$2" path server_timestamp txid tmp count=0
    local journal_backup old_revision revision_tmp
    tmp="${JOURNAL_FILE}.delete-batch.$$.$RANDOM"
    journal_backup="${JOURNAL_FILE}.rollback.$$.$RANDOM"
    revision_tmp="${MANIFEST_REVISION_FILE}.rollback.$$.$RANDOM"
    old_revision=$(cat "$MANIFEST_REVISION_FILE" 2>/dev/null || echo 0)
    cp "$JOURNAL_FILE" "$journal_backup" || return 1
    : > "$tmp" || { rm -f "$tmp" "$journal_backup"; return 1; }
    while IFS= read -r -d '' path; do
        server_timestamp=$(date '+%s')
        txid="delete-${server_timestamp}-$$-${RANDOM}"
        {
            printf 'BEGIN\t%s\t%s\t%s\n' "$txid" "$server_timestamp" "$device"
            printf 'DELETE\t%s\t\t0\t0\t%s\t%s\n' \
                "$(encode_journal_field "$path")" "$(encode_journal_field "$device")" "$server_timestamp"
            printf 'COMMIT\t%s\n' "$txid"
        } >> "$tmp" || { rm -f "$tmp" "$journal_backup"; return 1; }
        (( count++ ))
    done < "$paths_file"
    if (( count == 0 )); then
        rm -f "$tmp" "$journal_backup"
        return 0
    fi
    if ! cat "$tmp" >> "$JOURNAL_FILE"; then
        cp "$journal_backup" "$JOURNAL_FILE" 2>/dev/null || true
        rm -f "$tmp" "$journal_backup"
        return 1
    fi
    rm -f "$tmp"
    if bump_manifest_revision_unlocked; then
        rm -f "$journal_backup"
        return 0
    fi
    cp "$journal_backup" "$JOURNAL_FILE" 2>/dev/null || true
    printf '%s\n' "$old_revision" > "$revision_tmp" && mv -f "$revision_tmp" "$MANIFEST_REVISION_FILE"
    rm -f "$journal_backup" "$revision_tmp"
    return 1
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
    local magic run_ts device count path expected_digest expected_mtime index target digest mtime backup
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

    exec 9>"$JOURNAL_LOCK_FILE"
    flock -x 9 || { exec 9>&-; return 1; }
    ensure_journal_files
    printf 'CHECKED_DELETE_V1\0%s\0' "$count"
    for ((index = 0; index < count; index++)); do
        path="${paths[index]}"
        target="$SHARE_ROOT/$path"
        if [[ ! -e "$target" && ! -L "$target" ]]; then
            if append_delete_journal_unlocked "$path" "$device"; then
                printf '%s\0ABSENT\0' "$path"
            else
                printf '%s\0ERROR\0' "$path"
            fi
            continue
        fi
        if [[ ! -f "$target" || -L "$target" ]]; then
            printf '%s\0ERROR\0' "$path"
            continue
        fi
        digest=$(portable_sha256 "$target" || true)
        mtime=$(portable_file_mtime "$target" || true)
        if [[ "$digest" != "${digests[index]}" || "$mtime" != "${mtimes[index]}" ]]; then
            printf '%s\0STALE\0' "$path"
            continue
        fi
        backup="$SHARE_ROOT/$TRASH_DIRNAME/$path-$run_ts"
        mkdir -p "${backup%/*}" || { printf '%s\0ERROR\0' "$path"; continue; }
        if mv "$target" "$backup"; then
            test_failpoint "checked_delete_after_move"
            if append_delete_journal_unlocked "$path" "$device"; then
                test_failpoint "checked_delete_after_journal"
                printf '%s\0DELETED\0' "$path"
            else
                mv "$backup" "$target" 2>/dev/null || true
                printf '%s\0ERROR\0' "$path"
            fi
        else
            printf '%s\0ERROR\0' "$path"
        fi
    done
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
            "$TRASH_DIRNAME"|.sync-partial|"$REPOSITORY_MARKER_NAME"|@eaDir) continue ;;
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
    local magic run_ts device relative target backup rel_file paths_file
    local -a journal_paths=()
    IFS= read -r -d '' magic || return 1
    IFS= read -r -d '' run_ts || return 1
    IFS= read -r -d '' device || return 1
    IFS= read -r -d '' relative || return 1
    [[ "$magic" == "BROWSE_DELETE_V1" ]] || return 1
    [[ "$run_ts" =~ ^[0-9]{4}-[0-9]{2}-[0-9]{2}--[0-9]{2}-[0-9]{2}-[0-9]{2}-[0-9]{6}Z$ ]] || return 1
    [[ "$device" =~ ^[A-Za-z0-9._:-]{1,128}$ ]] || return 1
    if ! valid_sync_relative_path "$relative"; then
        printf 'BROWSE_DELETE_V1\0ERROR\0percorso non valido\0'; return 1
    fi
    target="$SHARE_ROOT/$relative"
    if [[ ! -e "$target" && ! -L "$target" ]]; then
        printf 'BROWSE_DELETE_V1\0ERROR\0percorso non trovato\0'; return 1
    fi
    backup="$SHARE_ROOT/$TRASH_DIRNAME/$relative-$run_ts"

    exec 9>"$JOURNAL_LOCK_FILE"
    flock -x 9 || { exec 9>&-; printf 'BROWSE_DELETE_V1\0ERROR\0lock non acquisito\0'; return 1; }
    ensure_journal_files

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

    if ! mkdir -p "${backup%/*}" || ! mv "$target" "$backup"; then
        flock -u 9; exec 9>&-
        printf 'BROWSE_DELETE_V1\0ERROR\0spostamento nel cestino fallito\0'
        return 1
    fi
    test_failpoint "browse_delete_after_move"

    paths_file="${JOURNAL_FILE}.browse-paths.$$.$RANDOM"
    : > "$paths_file"
    for rel_file in "${journal_paths[@]}"; do
        printf '%s\0' "$rel_file" >> "$paths_file"
    done
    if append_delete_journal_batch_unlocked "$paths_file" "$device"; then
        test_failpoint "browse_delete_after_journal"
        rm -f "$paths_file"
        flock -u 9; exec 9>&-
        printf 'BROWSE_DELETE_V1\0OK\0'
    else
        rm -f "$paths_file"
        mv "$backup" "$target" 2>/dev/null || true
        flock -u 9; exec 9>&-
        printf 'BROWSE_DELETE_V1\0ERROR\0journal non aggiornato, operazione annullata\0'
        return 1
    fi
}

cmd_browse_rename() {
    command -v flock >/dev/null 2>&1 || { echo "browse-rename: flock non disponibile" >&2; return 1; }
    local magic run_ts device src dst src_target dst_target rel_file paths_file
    local -a journal_paths=()
    IFS= read -r -d '' magic || return 1
    IFS= read -r -d '' run_ts || return 1
    IFS= read -r -d '' device || return 1
    IFS= read -r -d '' src || return 1
    IFS= read -r -d '' dst || return 1
    [[ "$magic" == "BROWSE_RENAME_V1" ]] || return 1
    [[ "$run_ts" =~ ^[0-9]{4}-[0-9]{2}-[0-9]{2}--[0-9]{2}-[0-9]{2}-[0-9]{2}-[0-9]{6}Z$ ]] || return 1
    [[ "$device" =~ ^[A-Za-z0-9._:-]{1,128}$ ]] || return 1
    if ! valid_sync_relative_path "$src" || ! valid_sync_relative_path "$dst"; then
        printf 'BROWSE_RENAME_V1\0ERROR\0percorso non valido\0'; return 1
    fi
    src_target="$SHARE_ROOT/$src"
    dst_target="$SHARE_ROOT/$dst"
    if [[ ! -e "$src_target" && ! -L "$src_target" ]]; then
        printf 'BROWSE_RENAME_V1\0ERROR\0percorso di origine non trovato\0'; return 1
    fi
    if [[ -e "$dst_target" || -L "$dst_target" ]]; then
        printf 'BROWSE_RENAME_V1\0ERROR\0esiste gia un percorso con questo nome\0'; return 1
    fi

    exec 9>"$JOURNAL_LOCK_FILE"
    flock -x 9 || { exec 9>&-; printf 'BROWSE_RENAME_V1\0ERROR\0lock non acquisito\0'; return 1; }
    ensure_journal_files

    if [[ ! -e "$src_target" && ! -L "$src_target" ]]; then
        flock -u 9; exec 9>&-
        printf 'BROWSE_RENAME_V1\0ERROR\0percorso di origine non trovato\0'
        return 1
    fi
    if [[ -e "$dst_target" || -L "$dst_target" ]]; then
        flock -u 9; exec 9>&-
        printf 'BROWSE_RENAME_V1\0ERROR\0esiste gia un percorso con questo nome\0'
        return 1
    fi

    # The old path(s) are gone from this location -- record it exactly like a
    # delete so other clients' manifests stop expecting it there. The new
    # location needs no journal entry of its own: it's simply a file/folder
    # any client will see as new content on its next ordinary comparison,
    # the same as content freshly pushed by any client.
    if [[ -d "$src_target" && ! -L "$src_target" ]]; then
        while IFS= read -r -d '' rel_file; do
            journal_paths+=("$src/${rel_file#./}")
        done < <(cd "$src_target" && find . -type f -print0 2>/dev/null)
    else
        journal_paths+=("$src")
    fi

    if ! mkdir -p "${dst_target%/*}" || ! mv "$src_target" "$dst_target"; then
        flock -u 9; exec 9>&-
        printf 'BROWSE_RENAME_V1\0ERROR\0spostamento fallito\0'
        return 1
    fi
    test_failpoint "browse_rename_after_move"

    paths_file="${JOURNAL_FILE}.browse-paths.$$.$RANDOM"
    : > "$paths_file"
    for rel_file in "${journal_paths[@]}"; do
        printf '%s\0' "$rel_file" >> "$paths_file"
    done
    if append_delete_journal_batch_unlocked "$paths_file" "$device"; then
        test_failpoint "browse_rename_after_journal"
        rm -f "$paths_file"
        flock -u 9; exec 9>&-
        printf 'BROWSE_RENAME_V1\0OK\0'
    else
        rm -f "$paths_file"
        mv "$dst_target" "$src_target" 2>/dev/null || true
        flock -u 9; exec 9>&-
        printf 'BROWSE_RENAME_V1\0ERROR\0journal non aggiornato, operazione annullata\0'
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
file_epoch() {
    local file="$1"
    local base="${file##*/}"
    if [[ "$base" =~ $TS_SUFFIX_REGEX ]]; then
        local ts="${BASH_REMATCH[1]}"
        local utc_suffix="${BASH_REMATCH[2]}"
        local y="${ts:0:4}" mo="${ts:5:2}" d="${ts:8:2}" h="${ts:12:2}" mi="${ts:15:2}" s="${ts:18:2}"
        local epoch
        if [[ -n "$utc_suffix" ]]; then
            epoch=$(date -u -d "${y}-${mo}-${d} ${h}:${mi}:${s}" '+%s' 2>/dev/null)
        else
            epoch=$(date -d "${y}-${mo}-${d} ${h}:${mi}:${s}" '+%s' 2>/dev/null)
        fi
        if [[ -n "$epoch" ]]; then
            echo "$epoch"
            return
        fi
    fi
    stat -c '%Y' "$file" 2>/dev/null || stat -f '%m' "$file" 2>/dev/null
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

prune_root() {
    local trash="$1"
    [[ -d "$trash" ]] || return 0

    if [[ "$RETENTION_DAYS" == "0" ]]; then
        return 0
    fi

    local now retention_seconds processed=0
    now=$(date '+%s')
    retention_seconds=$(( RETENTION_DAYS * 86400 ))

    while IFS= read -r -d '' file; do
        local epoch
        epoch="$(file_epoch "$file")"
        [[ -z "$epoch" ]] && continue
        local age_seconds=$(( now - epoch ))
        if (( age_seconds > retention_seconds )); then
            local rel="${file#"$trash"/}"
            local age_display
            age_display="$(format_age_days "$age_seconds")"
            if [[ "$DRY_RUN" == "true" ]]; then
                log INFO "[dry-run] rimuoverei versione: $rel (età ${age_display}gg > retention ${RETENTION_DAYS}gg)"
            else
                if rm -f "$file"; then
                    log INFO "Rimossa versione storica: $rel (età ${age_display}gg > retention ${RETENTION_DAYS}gg)"
                    log_event "PRUNE_REMOTE_TRASH" "$rel" "age=${age_display}d retention=${RETENTION_DAYS}d"
                else
                    log ERROR "Impossibile rimuovere versione storica: $rel"
                fi
            fi
            (( processed++ ))
            if (( processed >= PRUNE_MAX_FILES_PER_PASS )); then
                log INFO "Pass di pruning limitato a $PRUNE_MAX_FILES_PER_PASS versioni; continuera' al prossimo intervallo."
                break
            fi
        fi
    done < <(find "$trash" -type f -print0 2>/dev/null)

    # tidy up now-empty subdirectories left behind (but keep trash_dir itself)
    find "$trash" -mindepth 1 -type d -empty -delete 2>/dev/null || true

    return 0
}

run_once_pass() {
    command -v flock >/dev/null 2>&1 || {
        log ERROR "Impossibile eseguire il pruning: flock non disponibile."
        return 1
    }
    # Retention touches the same .sync-trash that rsync uses for backups. Do not
    # prune while a client owns the transaction lock; a live transfer is never
    # stale merely because it is long-running.
    exec 8>"$SYNC_LOCK_FILE" || return 1
    if ! flock -n 8; then
        log INFO "Pass di pruning rimandato: trasferimento NASBox in corso."
        exec 8>&-
        return 0
    fi
    log INFO "sync-daemon-server v$VERSION: avvio pass di pruning (SHARE_ROOT=$SHARE_ROOT, retention=${RETENTION_DAYS}gg, dry-run=$DRY_RUN)"
    local trash
    trash="$(trash_dir)"
    if [[ -d "$trash" ]]; then
        prune_root "$trash"
    else
        log INFO "Nessuno storico ancora presente ($trash non esiste)."
    fi
    date '+%s' > "$STAMP_FILE"
    log INFO "Pass completato."
    flock -u 8
    exec 8>&-
}

# --- daemon loop ---

run_foreground() {
    mkdir -p "$STATE_DIR"
    if ! command -v flock >/dev/null 2>&1; then
        log ERROR "Impossibile avviare il demone: flock non disponibile."
        return 1
    fi
    exec 9>"$DAEMON_LOCK_FILE"
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
    exec 8>"$SYNC_LOCK_FILE" || return 0
    if flock -n 8; then
        flock -u 8
        exec 8>&-
        return 0
    fi
    SYNC_LOCK_HELD="true"
    if [[ -f "$SYNC_LOCK_OWNER_FILE" ]]; then
        IFS='|' read -r SYNC_LOCK_OWNER_ID SYNC_LOCK_OWNER_HOST SYNC_LOCK_STARTED_AT < "$SYNC_LOCK_OWNER_FILE"
        [[ "$SYNC_LOCK_STARTED_AT" =~ ^[0-9]+$ ]] || SYNC_LOCK_STARTED_AT="0"
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
    read_sync_lock_diagnostics
    if [[ "$SYNC_LOCK_HELD" == "true" ]]; then
        printf '%-18s occupato (host %s, device %s, PID %s, età %s secondi)\n' \
            "Lock trasferimenti:" "${SYNC_LOCK_OWNER_HOST:-sconosciuto}" \
            "${SYNC_LOCK_OWNER_ID:-sconosciuto}" "${SYNC_LOCK_OWNER_PID:-sconosciuto}" "$SYNC_LOCK_AGE_SECONDS"
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
    echo "TOMBSTONE_TTL_DAYS=$TOMBSTONE_TTL_DAYS"
    echo "SYNC_LOCK_MAX_AGE_MINUTES=0"
    read_sync_lock_diagnostics
    echo "SYNC_LOCK_HELD=$SYNC_LOCK_HELD"
    echo "SYNC_LOCK_AGE_SECONDS=$SYNC_LOCK_AGE_SECONDS"
    echo "SYNC_LOCK_OWNER_PID=$SYNC_LOCK_OWNER_PID"
    echo "SYNC_LOCK_OWNER_ID=$SYNC_LOCK_OWNER_ID"
    echo "SYNC_LOCK_OWNER_HOST=$SYNC_LOCK_OWNER_HOST"
    echo "SYNC_LOCK_STARTED_AT=$SYNC_LOCK_STARTED_AT"
    echo "CHECK_INTERVAL_MINUTES=$CHECK_INTERVAL_MINUTES"
    echo "PRUNE_MAX_FILES_PER_PASS=$PRUNE_MAX_FILES_PER_PASS"
    echo "JOURNAL_MAX_BYTES=$JOURNAL_MAX_BYTES"
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
    local trash count disk_bytes available last_run disk_kb available_kb
    trash="$(trash_dir)"
    count=0
    disk_bytes=0
    if [[ -d "$trash" ]]; then
        count=$(find "$trash" -type f 2>/dev/null | wc -l | tr -d '[:space:]')
        disk_kb=$(du -sk "$trash" 2>/dev/null | cut -f1 || true)
        disk_bytes=$(( ${disk_kb:-0} * 1024 ))
    fi
    available_kb=$(df -Pk "$SHARE_ROOT" 2>/dev/null | awk 'NR==2 {print $4}' || true)
    available=$(( ${available_kb:-0} * 1024 ))
    last_run=$(cat "$STAMP_FILE" 2>/dev/null || echo 0)
    echo "VERSION=$VERSION"
    echo "TRASH_FILE_COUNT=$count"
    echo "TRASH_DISK_BYTES=$disk_bytes"
    echo "SHARE_AVAILABLE_BYTES=$available"
    echo "LAST_RUN_EPOCH=$last_run"
    echo "JOURNAL_BYTES=$(wc -c < "$JOURNAL_FILE" 2>/dev/null || echo 0)"
    echo "MANIFEST_BYTES=$(wc -c < "$MANIFEST_FILE" 2>/dev/null || echo 0)"
    echo "JOURNAL_MAX_BYTES=$JOURNAL_MAX_BYTES"
    if [[ -w "$STATE_DIR" ]]; then echo "STATE_WRITABLE=true"; else echo "STATE_WRITABLE=false"; fi
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
  $(basename "$0") --print-config              Dump KEY=VALUE (per l'auto-configurazione dei client)
  $(basename "$0") --init-repository           Crea il marker persistente del repository
  $(basename "$0") --journal-append             Registra una transazione (input NUL)
  $(basename "$0") --journal-compact            Compatta il journal nel manifest
  $(basename "$0") --manifest-export            Esporta il manifest corrente
  $(basename "$0") --manifest-get PATH          Legge lo stato di un percorso
  $(basename "$0") --file-states               Stato batch file/tombstone (input/output NUL)
  $(basename "$0") --checked-delete            Cancella solo la baseline attesa (input/output NUL)
  $(basename "$0") --journal-status             Stato e dimensione journal/manifest
  $(basename "$0") --history-list              Elenco NUL dello storico sul NAS (per il client)
  $(basename "$0") --diagnostics               Diagnostica KEY=VALUE del NAS (per il client)
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
        --dry-run) DRY_RUN="true" ;;
        --status) ACTION="status" ;;
        --print-config) ACTION="print_config" ;;
        --init-repository) ACTION="init_repository" ;;
        --journal-append) ACTION="journal_append" ;;
        --journal-compact) ACTION="journal_compact" ;;
        --manifest-export) ACTION="manifest_export" ;;
        --manifest-get) shift; MANIFEST_PATH="${1:-}"; ACTION="manifest_get" ;;
        --file-states) ACTION="file_states" ;;
        --file-states-current) ACTION="file_states_current" ;;
        --checked-delete) ACTION="checked_delete" ;;
        --browse-list) shift; BROWSE_PATH="${1:-}"; ACTION="browse_list" ;;
        --browse-delete) ACTION="browse_delete" ;;
        --browse-rename) ACTION="browse_rename" ;;
        --journal-status) ACTION="journal_status" ;;
        --history-list) ACTION="history_list" ;;
        --diagnostics) ACTION="diagnostics" ;;
        -c|--config) shift; CONFIG_FILE="${1:?}" ;;
        --help|-h) cmd_help; exit 0 ;;
        *) echo "Opzione sconosciuta: $1" >&2; cmd_help; exit 1 ;;
    esac
    shift
done

mkdir -p "$STATE_DIR"
load_config
ensure_journal_files

case "$ACTION" in
    start) cmd_start ;;
    stop) cmd_stop ;;
    restart) cmd_restart ;;
    run_foreground) run_foreground ;;
    run_once) run_once_pass; exit $? ;;
    status) cmd_status; exit $? ;;
    print_config) cmd_print_config; exit $? ;;
    init_repository) cmd_init_repository; exit $? ;;
    journal_append) cmd_journal_append; exit $? ;;
    journal_compact) cmd_journal_compact; exit $? ;;
    manifest_export) cmd_manifest_export; exit $? ;;
    manifest_get) cmd_manifest_get "$MANIFEST_PATH"; exit $? ;;
    file_states) cmd_file_states; exit $? ;;
    file_states_current) cmd_file_states false; exit $? ;;
    checked_delete) cmd_checked_delete; exit $? ;;
    browse_list) cmd_browse_list "$BROWSE_PATH"; exit $? ;;
    browse_delete) cmd_browse_delete; exit $? ;;
    browse_rename) cmd_browse_rename; exit $? ;;
    journal_status) cmd_journal_status; exit $? ;;
    history_list) cmd_history_list; exit $? ;;
    diagnostics) cmd_diagnostics; exit $? ;;
esac
