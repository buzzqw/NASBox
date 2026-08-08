"""Client-side trash/version-history management.

Every push/pull uses rsync --backup-dir + --suffix, so an overwritten or
deleted file lands in .sync-trash as e.g.
"pippo.txt-2025-05-01--10-31-57-123456Z" -- the original name plus the exact
UTC moment it was superseded, right there in the filename. No timestamped
subfolders to dig through.

Authoritative retention *on the NAS* is enforced by the separate, self-contained
server package (see server/sync-daemon-server.sh) since it must keep working
even when no client is running. From here we only manage the local trash and,
best-effort, can ask the NAS to run its own prune over SSH.
"""
from __future__ import annotations

import re
import shutil
import time
from dataclasses import dataclass
from datetime import datetime, timezone
from pathlib import Path

from . import paths, rsync_ops
from .config import Config
from .logger import EventLogger
from .rsync_ops import RUN_TS_FORMAT, UTC_RUN_TS_FORMAT, NasConnection

SUFFIX_RE = re.compile(
    r"-(\d{4}-\d{2}-\d{2}--\d{2}-\d{2}-\d{2}(?:-\d{6}Z)?)$"
)


@dataclass
class TrashVersion:
    relative_path: str   # original file path relative to the NASBox root (suffix stripped)
    timestamp: str        # e.g. "2025-05-01--10-31-57-123456Z"
    trash_path: Path      # the actual versioned file on disk, e.g. .../pippo.txt-2025-05-01--10-31-57
    age_days: float
    size: int = 0          # bytes, straight from the trashed file itself


@dataclass
class RemoteTrashVersion:
    relative_path: str
    timestamp: str
    remote_trash_relative_path: str
    age_days: float
    size: int = 0


def _parse_suffix(name: str) -> tuple[str, float] | None:
    m = SUFFIX_RE.search(name)
    if not m:
        return None
    ts_str = m.group(1)
    try:
        if ts_str.endswith("Z"):
            epoch = datetime.strptime(ts_str, UTC_RUN_TS_FORMAT).replace(tzinfo=timezone.utc).timestamp()
        else:
            # Backward compatibility for versions created before UTC suffixes.
            epoch = time.mktime(time.strptime(ts_str, RUN_TS_FORMAT))
    except ValueError:
        return None
    return ts_str, epoch


def list_local_versions() -> list[TrashVersion]:
    root = paths.local_trash_dir()
    if not root.exists():
        return []
    out: list[TrashVersion] = []
    now = time.time()
    for file_path in root.rglob("*"):
        if not file_path.is_file():
            continue
        parsed = _parse_suffix(file_path.name)
        if parsed is None:
            continue
        ts_str, epoch = parsed
        original_name = file_path.name[: -(len(ts_str) + 1)]  # strip trailing "-<timestamp>"
        rel_dir = file_path.parent.relative_to(root)
        relative_path = str(rel_dir / original_name) if str(rel_dir) != "." else original_name
        try:
            size = file_path.stat().st_size
        except OSError:
            size = 0
        out.append(TrashVersion(
            relative_path=relative_path,
            timestamp=ts_str, trash_path=file_path,
            age_days=(now - epoch) / 86400.0,
            size=size,
        ))
    return sorted(out, key=lambda v: v.age_days)


def _prune_empty_dirs(root: Path) -> None:
    if not root.exists():
        return
    for dirpath in sorted((p for p in root.rglob("*") if p.is_dir()), key=lambda p: len(p.parts), reverse=True):
        try:
            dirpath.rmdir()  # only succeeds if empty
        except OSError:
            pass


def prune_local(cfg: Config, logger: EventLogger) -> int:
    """Delete local trash versions older than retention_days_local. Returns count removed."""
    retention_days = float(cfg.get("retention_days_local") or 0)
    if retention_days <= 0:
        return 0
    removed = 0
    for version in list_local_versions():
        if version.age_days > retention_days:
            try:
                version.trash_path.unlink()
                removed += 1
                logger.log(
                    "PRUNE_LOCAL_TRASH", version.relative_path,
                    detail=f"versione {version.timestamp}, età={version.age_days:.1f}g > retention={retention_days:.0f}g",
                )
            except OSError as exc:
                logger.log("ERROR", version.relative_path, detail=f"prune failed: {exc}")
    _prune_empty_dirs(paths.local_trash_dir())
    return removed


def restore_version(version: TrashVersion, destination_root: str) -> bool:
    """Copy one historical version back to its original location (overwriting the current file)."""
    if not version.trash_path.exists():
        return False
    dst = Path(destination_root) / version.relative_path
    dst.parent.mkdir(parents=True, exist_ok=True)
    shutil.copy2(version.trash_path, dst)
    return True


def delete_version(version: TrashVersion) -> bool:
    """Permanently removes one historical version from local trash, regardless
    of its age. This is deliberately separate from prune_local(): that one
    only ever removes versions older than retention_days_local, on purpose
    (retention is a safety net, not a "make it disappear on demand" button)
    -- but a user who wants a specific file or a whole folder's history gone
    right now, e.g. to reclaim space, has no way to say so through retention
    alone. Irreversible; the caller is expected to have already confirmed
    with the user (see history_tab.py)."""
    try:
        version.trash_path.unlink()
    except OSError:
        return False
    _prune_empty_dirs(paths.local_trash_dir())
    return True


def list_remote_versions(cfg: Config, conn: NasConnection) -> tuple[bool, list[RemoteTrashVersion], str]:
    script_path = cfg.get("remote_server_script")
    if not script_path:
        return False, [], "script server sul NAS non configurato"
    ok, output, error = rsync_ops.run_remote_script_bytes(cfg, conn, script_path, ["--history-list"])
    if not ok:
        return False, [], error or "impossibile leggere lo storico sul NAS"
    fields = output.split(b"\0")
    if not fields or fields[0] != b"HISTORY_V1":
        return False, [], "risposta dello storico NAS non riconosciuta: aggiorna il pacchetto server"
    versions: list[RemoteTrashVersion] = []
    now = time.time()
    for index in range(1, len(fields) - 4, 4):
        relative, timestamp, remote_path, size = (field.decode(errors="surrogateescape") for field in fields[index:index + 4])
        parsed = _parse_suffix(f"x-{timestamp}")
        if parsed is None or not relative or not remote_path:
            continue
        try:
            size_value = int(size)
        except ValueError:
            size_value = 0
        versions.append(RemoteTrashVersion(relative, timestamp, remote_path, (now - parsed[1]) / 86400.0, size_value))
    return True, sorted(versions, key=lambda version: version.age_days), ""


def fetch_remote_diagnostics(cfg: Config, conn: NasConnection) -> tuple[bool, dict[str, str], str]:
    script_path = cfg.get("remote_server_script")
    if not script_path:
        return False, {}, "script server sul NAS non configurato"
    ok, output, error = rsync_ops.run_remote_script(cfg, conn, script_path, ["--diagnostics"], timeout=120)
    if not ok:
        return False, {}, error or "impossibile leggere la diagnostica NAS"
    values: dict[str, str] = {}
    for line in output.splitlines():
        key, separator, value = line.partition("=")
        if separator:
            values[key] = value
    return True, values, ""


def trigger_remote_prune(cfg: Config, conn: NasConnection, logger: EventLogger) -> tuple[bool, str]:
    """Best-effort: ask the NAS-side server package to run its own prune now over SSH.

    Requires `remote_server_script` to be set in the client config, pointing at the
    installed path of sync-daemon-server.sh on the NAS (e.g. /volume1/NASBox/sync-daemon-server/sync-daemon-server.sh).
    """
    script_path = cfg.get("remote_server_script")
    if not script_path:
        return False, "remote_server_script non configurato: installa il pacchetto server sul NAS (vedi server/)"

    ok, stdout, stderr = rsync_ops.run_remote_script(cfg, conn, script_path, ["--run-once"], timeout=120)
    detail = (stdout + stderr).strip()
    logger.log("PRUNE_REMOTE_TRIGGER", script_path, detail=detail[:500])
    return ok, detail


def fetch_remote_config(
    cfg: Config, conn: NasConnection, script_path: str | None = None,
) -> tuple[bool, dict, str]:
    """Connect to the NAS, confirm the server daemon is up, and read its authoritative
    config (SHARE_ROOT / RETENTION_DAYS / ...) via `sync-daemon-server.sh --print-config`.

    Returns (ok, values, error_message). `values` has string keys/values as printed
    by the script, plus a bool "running" parsed from RUNNING=true/false.
    """
    script_path = script_path or cfg.get("remote_server_script")
    if not script_path:
        return False, {}, "remote_server_script non configurato: indica il percorso di sync-daemon-server.sh sul NAS"

    ok, stdout, stderr = rsync_ops.run_remote_script(cfg, conn, script_path, ["--print-config"], timeout=30)
    if not ok:
        if "sconosciuta" in stderr.lower() or "unknown option" in stderr.lower():
            return False, {}, (
                "Il pacchetto server sul NAS è una versione precedente e non supporta ancora "
                "questa funzione. Copia la cartella server/ aggiornata sul NAS e riavvia il demone."
            )
        return False, {}, (stderr or stdout or "comando remoto fallito").strip()

    values: dict = {}
    for line in stdout.splitlines():
        line = line.strip()
        if not line or "=" not in line:
            continue
        key, _, value = line.partition("=")
        values[key.strip()] = value.strip()

    values["running"] = values.get("RUNNING", "").lower() == "true"
    return True, values, ""
