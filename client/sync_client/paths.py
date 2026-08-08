"""Filesystem locations used by the sync client (XDG-aware, mirrors sync-daemon.sh)."""
from __future__ import annotations

import os
from pathlib import Path


def home_dir() -> Path:
    return Path(os.environ.get("HOME", str(Path.home())))


def config_dir() -> Path:
    base = os.environ.get("XDG_CONFIG_HOME") or str(home_dir() / ".config")
    return Path(base) / "sync-daemon"


def state_dir() -> Path:
    base = os.environ.get("XDG_STATE_HOME") or str(home_dir() / ".local" / "state")
    return Path(base) / "sync-daemon"


def config_file() -> Path:
    return config_dir() / "client.json"


def log_file() -> Path:
    return state_dir() / "client.log"


def events_file() -> Path:
    """JSON-lines structured event log (uploads/downloads/deletes/errors)."""
    return state_dir() / "events.jsonl"


def local_trash_dir() -> Path:
    return state_dir() / "trash"


def watch_stamp_file() -> Path:
    return state_dir() / "watch-stamp"


def repository_marker_file() -> Path:
    """Client-owned repository/mount identity, outside the synced tree."""
    return state_dir() / "repository-marker.json"


def journal_pending_file() -> Path:
    """Binary-safe payload waiting to be acknowledged by the NAS journal."""
    return state_dir() / "journal-pending.bin"


def ssh_known_hosts_file() -> Path:
    return config_dir() / "known_hosts"


def sync_state_db_file() -> Path:
    """Persistent synchronization metadata (separate from user configuration)."""
    return state_dir() / "sync-state.sqlite3"


def ensure_dirs() -> None:
    # 0700: client.json holds the NAS host/username and this machine's own
    # filesystem layout, and state_dir's event log records every synced path --
    # nothing here is a secret exactly (auth is SSH-key based, no passwords
    # stored), but there's no reason to leave it group/world-readable either.
    # chmod runs every call (not just on first creation) so an existing
    # install picks up the tighter mode too, not only a fresh one.
    for d in (config_dir(), state_dir()):
        d.mkdir(parents=True, exist_ok=True)
        try:
            os.chmod(d, 0o700)
        except OSError:
            pass  # best-effort -- e.g. not the owner on some shared/odd filesystem
    local_trash_dir().mkdir(parents=True, exist_ok=True)

    existing_config = config_file()
    if existing_config.exists():
        try:
            os.chmod(existing_config, 0o600)  # tighten a config left over from an older, looser version
        except OSError:
            pass
