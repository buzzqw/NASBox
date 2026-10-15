"""Client configuration: NAS connection, the single NASBox folder, bandwidth,
sync mode, retention, pause state.

Persisted as JSON at $XDG_CONFIG_HOME/sync-daemon/client.json. Access is guarded by a
lock since both the GUI thread and the background SyncEngine thread read/write it.
"""
from __future__ import annotations

import json
import os
import threading
from typing import Any, Optional

from . import paths

SYNC_MODE_BIDIRECTIONAL = "bidirectional"
SYNC_MODE_PUSH_ONLY = "push_only"
SYNC_MODE_PULL_ONLY = "pull_only"
SYNC_MODE_ARCHIVE = "archive"

SYNC_MODE_OPTIONS = (
    (SYNC_MODE_BIDIRECTIONAL, "settings.sync_mode_bidirectional"),
    (SYNC_MODE_PUSH_ONLY, "settings.sync_mode_push_only"),
    (SYNC_MODE_PULL_ONLY, "settings.sync_mode_pull_only"),
    (SYNC_MODE_ARCHIVE, "settings.sync_mode_archive"),
)
SYNC_MODES = {value for value, _label in SYNC_MODE_OPTIONS}


def normalize_sync_mode(value: Any) -> str:
    """Keep old or manually edited configurations on the safe default."""
    return value if isinstance(value, str) and value in SYNC_MODES else SYNC_MODE_BIDIRECTIONAL


# --- scheduled pause ------------------------------------------------------
#
# A schedule is a list of windows, each {"days": [0..6], "start": "HH:MM",
# "end": "HH:MM"} where days uses Python's weekday() (Monday == 0) and refers to
# the day the window STARTS. A window whose end is at or before its start wraps
# past midnight (an "overnight" window such as 22:00 -> 07:00). Outside every
# window sync runs normally; inside one, the client treats the folder as paused
# -- no push, no pull, no change-feed wait, no mirror -- which also keeps the
# NAS quiet during the chosen hours.

def _hhmm_to_minutes(value: Any) -> int | None:
    if not isinstance(value, str) or ":" not in value:
        return None
    hours, _, minutes = value.partition(":")
    try:
        h, m = int(hours), int(minutes)
    except ValueError:
        return None
    if not (0 <= h <= 23 and 0 <= m <= 59):
        return None
    return h * 60 + m


def normalize_schedule_window(window: Any) -> dict | None:
    if not isinstance(window, dict):
        return None
    start = _hhmm_to_minutes(window.get("start"))
    end = _hhmm_to_minutes(window.get("end"))
    if start is None or end is None or start == end:
        return None
    raw_days = window.get("days")
    if not isinstance(raw_days, (list, tuple)):
        return None
    days = sorted({int(day) for day in raw_days if isinstance(day, int) and 0 <= day <= 6})
    if not days:
        return None
    return {
        "days": days,
        "start": f"{start // 60:02d}:{start % 60:02d}",
        "end": f"{end // 60:02d}:{end % 60:02d}",
    }


def normalize_schedule(windows: Any) -> list[dict]:
    if not isinstance(windows, (list, tuple)):
        return []
    normalized = []
    for window in windows:
        clean = normalize_schedule_window(window)
        if clean is not None:
            normalized.append(clean)
    return normalized


def schedule_window_state(
    window: dict, when: "datetime", *, want_remaining: bool = False,
) -> tuple[bool, int | None]:
    """Is this window active at `when`, and (optionally) seconds until it ends."""
    start = _hhmm_to_minutes(window.get("start"))
    end = _hhmm_to_minutes(window.get("end"))
    days = window.get("days") or []
    if start is None or end is None or start == end or not days:
        return False, None
    minutes = when.hour * 60 + when.minute
    weekday = when.weekday()
    if start < end:
        active = weekday in days and start <= minutes < end
        if not active:
            return False, None
        if not want_remaining:
            return True, None
        remaining = (end - minutes) * 60 - when.second
        return True, max(remaining, 0)
    # Overnight window: from `start` on a listed day to `end` the next morning.
    if weekday in days and minutes >= start:
        if not want_remaining:
            return True, None
        remaining = ((24 * 60 - minutes) + end) * 60 - when.second
        return True, max(remaining, 0)
    previous = (weekday - 1) % 7
    if previous in days and minutes < end:
        if not want_remaining:
            return True, None
        remaining = (end - minutes) * 60 - when.second
        return True, max(remaining, 0)
    return False, None


def schedule_state(
    windows: Any, when: "datetime | None" = None, *, want_remaining: bool = False,
) -> tuple[bool, int | None]:
    """Whether any schedule window is active now, and the wait until the soonest end."""
    from datetime import datetime as _datetime

    moment = when or _datetime.now()
    active = False
    soonest: int | None = None
    for window in normalize_schedule(windows):
        window_active, remaining = schedule_window_state(window, moment, want_remaining=want_remaining)
        if not window_active:
            continue
        active = True
        if want_remaining and remaining is not None:
            soonest = remaining if soonest is None else min(soonest, remaining)
    return active, soonest


DEFAULTS: dict[str, Any] = {
    "nas_lan": "",
    "nas_wan": "",           # direct WAN hostname of the NAS, if it has one (leave empty if not)
    "nas_user": "",
    "ssh_port": 22,
    "jump_host": "",         # optional SSH bastion (e.g. "bastion.example.net"), used when the NAS
    "jump_port": 22,         # itself has no direct WAN reachability but this host can see it on its LAN
    "jump_user": "",         # SSH user on the bastion; falls back to nas_user if empty
    "remote_prefix": "/volume1/NASBox",  # the NAS-side folder this client mirrors -- the whole
                                          # thing, not a subfolder of it (see local_root below)
    "sync_mode": SYNC_MODE_BIDIRECTIONAL,  # bidirectional, push_only, pull_only, or archive
    "remote_server_script": "",   # path to sync-daemon-server.sh on the NAS -- lets this client
                                   # read SHARE_ROOT/RETENTION_DAYS straight from the NAS ("Rileva dal NAS")
                                   # instead of the same values being retyped by hand and drifting out of sync
    "client_update_path": "",      # optional local client-update bundle path
    "client_update_remote_path": ".nasbox-client-update",  # relative to remote_prefix by default
    "poll_interval": 60,       # legacy/fallback seconds between pull-from-NAS checks
    "change_feed_wait_seconds": 55,  # maximum read-only wait for a new NAS manifest revision
    "remote_change_feed_available": False,  # learned from the NAS server's capability dump
    "remote_causal_versions_available": False,  # optional JOURNAL_V3/manifest metadata
    "metrics_refresh_seconds": 15,  # seconds between read-only NAS load samples
    "debounce_seconds": 2,     # seconds of local quiet before pushing a change
    "file_stability_seconds": 0.75,  # one local stat interval before a queued file is eligible
    "notify_sync_completion": False,  # show a tray notification after completed file transfers
    "animate_sync_icon": False,  # animate the tray icon while files are transferred
    "tray_single_click": "menu",  # "menu" (compatibility default) or "window"
    "delete_enabled": False,
    "max_delete_files": 1000,       # safety stop for unexpectedly large delete batches
    "bandwidth_upload_kbps": 0,    # 0 = unlimited -- applies to push (this client -> NAS)
    "bandwidth_download_kbps": 0,  # 0 = unlimited -- applies to pull (NAS -> this client)
    "retention_days_local": 30,   # client-side trash retention
    "retention_days_remote": 30,  # NAS-side trash retention -- read-only here, sourced from the NAS via "Rileva dal NAS".
                                   # Matches server/sync-daemon-server.sh's own RETENTION_DAYS default so the
                                   # Settings tab doesn't show a stale number before the first NAS detection runs.
    "server_state_dir_remote": "", # NAS-absolute path to the daemon's own state dir (PID/lock/log) --
                                   # read-only here, self-reported by the daemon via --print-config so
                                    # rsync_ops can exclude it if it falls inside the synced tree
    "server_lock_file_remote": "", # authoritative NAS-side transaction lock, reported by server >= 3.2.0
    "server_lock_owner_file_remote": "",
    "server_lock_held": False,      # best-effort diagnostics from --print-config
    "server_lock_age_seconds": 0,
    "server_lock_owner_pid": "",
    "server_lock_owner_id": "",
    "server_lock_owner_host": "",
    "server_lock_started_at": 0,
    "repository_id": "",           # UUID-like ID read from the NAS-side .nasbox-root marker
    "remote_repository_ready": False,
    "remote_journal_ready": False,
    "journal_error": "",
    "ssh_known_hosts": "",          # dedicated known_hosts file after explicit host-key pinning
    "ssh_host_key_pinned": False,
    "ssh_jump_key_pinned": False,
    "ssh_multiplexing": True,       # reuse one SSH connection (ControlMaster) across the many
                                    # short remote commands of a sync -- far less handshake and,
                                    # on the NAS, far fewer sshd/shell forks. Disable only if a
                                    # specific SSH setup or bastion conflicts with it.
    "paused": False,
    "pause_until": None,          # epoch seconds, or None
    "pause_schedule_enabled": False,  # master switch for recurring pause windows
    "pause_schedule": [],         # list of {"days": [0..6], "start": "HH:MM", "end": "HH:MM"};
                                  # days is Python weekday() (Mon=0) and names the START day,
                                  # so start>=end is an overnight window (e.g. 22:00 -> 07:00)
    "local_root": "",             # absolute path to the local NASBox folder -- "" means not set up yet
                                   # (first run shows the setup dialog). Exactly one per client: there is
                                   # no per-folder add/remove anymore -- see MANUALE.md §8.
    "exclude_patterns": [],       # rsync-style patterns (e.g. "node_modules/", "*.tmp") never synced
                                   # in either direction -- NASBox's answer to Dropbox's "selective sync",
                                   # but scoped to patterns instead of whole subfolders.
    "selective_sync_excludes": [],  # exact relative subfolders (e.g. "RPG", "lavoro/archivio") kept out
                                    # of sync by the folder picker. Anchored: a name matches only that
                                    # path and its children, never a same-named folder elsewhere.
    "mirrors": [],                # external folders mirrored ONE-WAY into local_root (see mirrors.py):
                                   # each entry is {"source": str (absolute path), "dest": str (relative
                                   # subfolder name inside NASBox), "enabled": bool,
                                   # "last_sync": float|None (epoch seconds), "error": str}.
                                   # The local source is authoritative: the copy inside NASBox (and so
                                   # what ends up on the NAS and on other PCs) always matches it.
    "language": "auto",           # "auto" (guess from the OS locale), "it", or "en" -- see i18n.py.
                                   # Takes effect on next start: widgets read translated strings once,
                                   # at construction time, there's no live re-translation.
}


class Config:
    def __init__(self) -> None:
        self._lock = threading.RLock()
        self._data: dict[str, Any] = json.loads(json.dumps(DEFAULTS))
        self.load()

    # --- persistence ---

    def load(self) -> None:
        with self._lock:
            path = paths.config_file()
            if path.exists():
                try:
                    on_disk = json.loads(path.read_text())
                except (json.JSONDecodeError, OSError):
                    on_disk = {}
                merged = json.loads(json.dumps(DEFAULTS))
                merged.update(on_disk)
                self._data = merged
            else:
                self._data = json.loads(json.dumps(DEFAULTS))

    def save(self) -> None:
        with self._lock:
            paths.ensure_dirs()
            path = paths.config_file()
            tmp = path.with_suffix(".json.tmp")
            tmp.write_text(json.dumps(self._data, indent=2, ensure_ascii=False))
            try:
                os.chmod(tmp, 0o600)  # NAS host/username live here -- keep it owner-only
            except OSError:
                pass
            tmp.replace(path)

    # --- generic get/set ---

    def get(self, key: str, default: Any = None) -> Any:
        with self._lock:
            return self._data.get(key, default)

    def set(self, key: str, value: Any, persist: bool = True) -> None:
        with self._lock:
            self._data[key] = value
            if persist:
                self.save()

    # --- the NASBox folder ---

    def local_root(self) -> str:
        with self._lock:
            return self._data.get("local_root") or ""

    def is_configured(self) -> bool:
        """False before the first-run folder setup has ever completed."""
        return bool(self.local_root())

    def set_local_root(self, path: str) -> None:
        self.set("local_root", path)

    def sync_mode(self) -> str:
        with self._lock:
            return normalize_sync_mode(self._data.get("sync_mode"))

    def set_sync_mode(self, mode: str) -> None:
        self.set("sync_mode", normalize_sync_mode(mode))

    def allows_push(self) -> bool:
        return self.sync_mode() in (
            SYNC_MODE_BIDIRECTIONAL, SYNC_MODE_PUSH_ONLY, SYNC_MODE_ARCHIVE,
        )

    def allows_pull(self) -> bool:
        return self.sync_mode() in (SYNC_MODE_BIDIRECTIONAL, SYNC_MODE_PULL_ONLY)

    def allows_remote_deletions(self) -> bool:
        return bool(self.get("delete_enabled")) and self.sync_mode() != SYNC_MODE_ARCHIVE

    def exclude_patterns(self) -> list[str]:
        with self._lock:
            return list(self._data.get("exclude_patterns", []))

    def set_exclude_patterns(self, patterns: list[str]) -> None:
        self.set("exclude_patterns", list(patterns))

    def selective_sync_excludes(self) -> list[str]:
        with self._lock:
            raw = self._data.get("selective_sync_excludes", [])
        return [str(path) for path in raw] if isinstance(raw, list) else []

    def set_selective_sync_excludes(self, paths: list[str]) -> None:
        from .selective import minimize_excluded

        self.set("selective_sync_excludes", minimize_excluded(paths or []))

    # --- external folder mirrors ---

    def mirrors(self) -> list[dict]:
        with self._lock:
            return json.loads(json.dumps(self._data.get("mirrors", [])))

    def set_mirrors(self, mirrors: list[dict]) -> None:
        self.set("mirrors", list(mirrors))

    def mirror_by_source(self, source: str) -> Optional[dict]:
        for entry in self.mirrors():
            if entry.get("source") == source:
                return entry
        return None

    def update_mirror(self, source: str, **updates: Any) -> None:
        """Merge updates into the mirror entry identified by its source path.
        A no-op if that entry no longer exists (e.g. it was removed from the
        GUI while a background watcher was still finishing a transfer)."""
        mirrors = self.mirrors()
        changed = False
        for entry in mirrors:
            if entry.get("source") == source:
                entry.update(updates)
                changed = True
        if changed:
            self.set_mirrors(mirrors)

    def language(self) -> str:
        with self._lock:
            return self._data.get("language") or "auto"

    def set_language(self, code: str) -> None:
        self.set("language", code)

    # --- pause helpers ---

    def is_paused(self) -> bool:
        return self.is_manually_paused() or self.schedule_active_at()

    def is_manually_paused(self) -> bool:
        """Only the explicit pause (indefinite or a one-shot timer), not a schedule.

        The status bar and tray use this for the Pause/Resume toggle so that a
        recurring window never makes the toggle lie about what the user set.
        """
        with self._lock:
            if self._data.get("paused"):
                return True
            until = self._data.get("pause_until")
            if until is not None and until > _now():
                return True
            if until is not None and until <= _now():
                # timed pause expired -> auto clear
                self._data["pause_until"] = None
                self.save()
            return False

    # --- scheduled pause ---

    def schedule_active_at(self, when=None) -> bool:
        if not self.get("pause_schedule_enabled"):
            return False
        active, _remaining = schedule_state(self.get("pause_schedule") or [], when)
        return active

    def schedule_remaining_seconds(self, when=None):
        """Seconds until the active scheduled window ends, or None if none is active."""
        if not self.get("pause_schedule_enabled"):
            return None
        active, remaining = schedule_state(
            self.get("pause_schedule") or [], when, want_remaining=True,
        )
        return remaining if active else None

    def pause_schedule(self) -> list[dict]:
        with self._lock:
            return normalize_schedule(self._data.get("pause_schedule", []))

    def set_pause_schedule(self, windows: list[dict]) -> None:
        self.set("pause_schedule", normalize_schedule(windows))

    def pause_indefinitely(self) -> None:
        self.set("paused", True, persist=False)
        self.set("pause_until", None)

    def pause_for_seconds(self, seconds: float) -> None:
        self.set("paused", False, persist=False)
        self.set("pause_until", _now() + seconds)

    def resume(self) -> None:
        self.set("paused", False, persist=False)
        self.set("pause_until", None)

    def pause_remaining_seconds(self) -> Optional[float]:
        until = self.get("pause_until")
        if until is None:
            return None
        remaining = until - _now()
        return remaining if remaining > 0 else None


def _now() -> float:
    import time

    return time.time()


_shared: Optional[Config] = None


def shared() -> Config:
    global _shared
    if _shared is None:
        _shared = Config()
    return _shared
