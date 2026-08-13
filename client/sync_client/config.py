"""Client configuration: NAS connection, the single NASBox folder, bandwidth,
retention, pause state.

Persisted as JSON at $XDG_CONFIG_HOME/sync-daemon/client.json. Access is guarded by a
lock since both the GUI thread and the background SyncEngine thread read/write it.
"""
from __future__ import annotations

import json
import os
import threading
from typing import Any, Optional

from . import paths

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
    "remote_server_script": "",   # path to sync-daemon-server.sh on the NAS -- lets this client
                                   # read SHARE_ROOT/RETENTION_DAYS straight from the NAS ("Rileva dal NAS")
                                   # instead of the same values being retyped by hand and drifting out of sync
    "client_update_path": "",      # optional local client-update bundle path
    "client_update_remote_path": ".nasbox-client-update",  # relative to remote_prefix by default
    "poll_interval": 60,       # seconds between pull-from-NAS cycles (discover other clients' changes)
    "debounce_seconds": 2,     # seconds of local quiet before pushing a change
    "notify_sync_completion": False,  # show a tray notification after completed file transfers
    "animate_sync_icon": False,  # animate the tray icon while files are transferred
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
    "repository_id": "",           # UUID-like ID read from the NAS-side .nasbox-root marker
    "remote_repository_ready": False,
    "remote_journal_ready": False,
    "journal_error": "",
    "ssh_known_hosts": "",          # dedicated known_hosts file after explicit host-key pinning
    "ssh_host_key_pinned": False,
    "ssh_jump_key_pinned": False,
    "paused": False,
    "pause_until": None,          # epoch seconds, or None
    "local_root": "",             # absolute path to the local NASBox folder -- "" means not set up yet
                                   # (first run shows the setup dialog). Exactly one per client: there is
                                   # no per-folder add/remove anymore -- see MANUALE.md §8.
    "exclude_patterns": [],       # rsync-style patterns (e.g. "node_modules/", "*.tmp") never synced
                                   # in either direction -- NASBox's answer to Dropbox's "selective sync",
                                   # but scoped to patterns instead of whole subfolders.
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

    def exclude_patterns(self) -> list[str]:
        with self._lock:
            return list(self._data.get("exclude_patterns", []))

    def set_exclude_patterns(self, patterns: list[str]) -> None:
        self.set("exclude_patterns", list(patterns))

    def language(self) -> str:
        with self._lock:
            return self._data.get("language") or "auto"

    def set_language(self, code: str) -> None:
        self.set("language", code)

    # --- pause helpers ---

    def is_paused(self) -> bool:
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
