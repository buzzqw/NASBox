"""Cumulative guard against mass deletions.

The per-batch limit in rsync_ops only sees one rsync invocation at a time: a
mistaken "delete everything" can still be applied in several smaller batches
and stay under it. This module keeps a short rolling history of how many files
this client actually deleted and requires an explicit, time-limited
confirmation before crossing the cumulative threshold.

The history lives outside the synced tree (XDG state dir) and is append-only
with a bounded size, so it survives client restarts without growing forever.
"""
from __future__ import annotations

import json
import os
import tempfile
import threading
import time
from pathlib import Path
from typing import Any

from . import paths
from .i18n import t

DEFAULT_WINDOW_SECONDS = 3600
DEFAULT_MAX_DELETES_PER_WINDOW = 1000
CONFIRMATION_SECONDS = 600
_MAX_EVENTS = 4096


def _history_file() -> Path:
    return paths.state_dir() / "delete-safety.json"


class DeleteSafety:
    def __init__(self, state_file: Path | None = None) -> None:
        self._state_file = state_file or _history_file()
        self._lock = threading.RLock()
        self._events: list[tuple[float, int, str]] = []
        self._confirmed_until = 0.0
        self._load()

    def _load(self) -> None:
        try:
            data = json.loads(self._state_file.read_text(encoding="utf-8"))
            self._events = [
                (float(item[0]), int(item[1]), str(item[2]))
                for item in data.get("events", [])[-_MAX_EVENTS:]
            ]
            self._confirmed_until = float(data.get("confirmed_until", 0.0))
        except (OSError, ValueError, TypeError, IndexError):
            self._events = []
            self._confirmed_until = 0.0

    def _save(self) -> None:
        self._state_file.parent.mkdir(parents=True, exist_ok=True)
        payload = {
            "events": [[ts, count, direction] for ts, count, direction in self._events[-_MAX_EVENTS:]],
            "confirmed_until": self._confirmed_until,
        }
        fd, temp_name = tempfile.mkstemp(
            prefix=".delete-safety-", dir=str(self._state_file.parent)
        )
        try:
            with os.fdopen(fd, "w", encoding="utf-8") as stream:
                json.dump(payload, stream, separators=(",", ":"))
            os.replace(temp_name, self._state_file)
        finally:
            if os.path.exists(temp_name):
                os.unlink(temp_name)

    def _prune(self, window: float, now: float) -> None:
        self._events = [event for event in self._events if now - event[0] <= window]

    def window_seconds(self, cfg: Any) -> float:
        try:
            return max(60.0, float(cfg.get("delete_safety_window_seconds") or DEFAULT_WINDOW_SECONDS))
        except (TypeError, ValueError):
            return float(DEFAULT_WINDOW_SECONDS)

    def max_per_window(self, cfg: Any) -> int:
        try:
            return max(1, int(cfg.get("max_delete_files_per_window") or DEFAULT_MAX_DELETES_PER_WINDOW))
        except (TypeError, ValueError):
            return DEFAULT_MAX_DELETES_PER_WINDOW

    def check(self, cfg: Any, count: int, direction: str) -> None:
        """Raise if this batch would exceed the cumulative, confirmed limit."""
        if count <= 0 or not cfg.get("delete_enabled"):
            return
        with self._lock:
            now = time.time()
            window = self.window_seconds(cfg)
            self._prune(window, now)
            total = sum(event[1] for event in self._events) + count
            limit = self.max_per_window(cfg)
            if total <= limit or now < self._confirmed_until:
                return
            from .repository_safety import RepositorySafetyError

            raise RepositorySafetyError(t(
                "safety.blocked_cumulative", count=total, limit=limit,
            ))

    def record(self, cfg: Any, count: int, direction: str) -> None:
        if count <= 0 or not cfg.get("delete_enabled"):
            return
        with self._lock:
            now = time.time()
            self._prune(self.window_seconds(cfg), now)
            self._events.append((now, int(count), direction))
            self._events = self._events[-_MAX_EVENTS:]
            self._save()

    def confirm(self, cfg: Any | None = None) -> int:
        """Allow the cumulative limit to be crossed for a short time."""
        with self._lock:
            seconds = CONFIRMATION_SECONDS
            if cfg is not None:
                try:
                    seconds = max(60, int(cfg.get("delete_confirmation_seconds") or CONFIRMATION_SECONDS))
                except (TypeError, ValueError):
                    pass
            self._confirmed_until = time.time() + seconds
            self._save()
            return int(self._confirmed_until)

    def snapshot(self, cfg: Any) -> dict[str, Any]:
        with self._lock:
            now = time.time()
            window = self.window_seconds(cfg)
            self._prune(window, now)
            used = sum(event[1] for event in self._events)
            return {
                "window_seconds": window,
                "limit": self.max_per_window(cfg),
                "used": used,
                "remaining": max(0, self.max_per_window(cfg) - used),
                "confirmed_until": self._confirmed_until,
                "confirmed": now < self._confirmed_until,
            }


_shared: DeleteSafety | None = None
_shared_lock = threading.Lock()


def shared() -> DeleteSafety:
    global _shared
    with _shared_lock:
        if _shared is None:
            _shared = DeleteSafety()
        return _shared
