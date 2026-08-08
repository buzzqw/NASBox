"""Structured event logging for the sync client.

Writes two things:
- a human-readable rotating text log (client.log), for tailing / debugging.
- a JSON-lines event log (events.jsonl), one event per transfer/delete/error,
  which the GUI Log tab reads and filters.

Recent events are also kept in an in-memory ring buffer (self._recent), loaded
once from events.jsonl at startup. tail() serves from that buffer instead of
re-reading and re-parsing the whole file from disk on every call -- it used to,
which meant every tray-menu open and every Log tab reload/search scanned the
entire (up to MAX_EVENTS-line) file.
"""
from __future__ import annotations

import json
import threading
import time
from collections import deque
from dataclasses import asdict, dataclass
from pathlib import Path
from typing import Optional

from . import paths

MAX_TEXT_LOG_BYTES = 5 * 1024 * 1024
MAX_EVENTS = 20_000  # cap on both the in-memory ring buffer and events.jsonl on disk
TRIM_CHECK_INTERVAL = 200  # only re-evaluate whether events.jsonl needs trimming every N appends
TRIM_BUFFER = 2_000  # let the on-disk file grow this far past MAX_EVENTS before rewriting it,
                      # so a long sync doesn't rewrite a ~20k-line file every TRIM_CHECK_INTERVAL appends


@dataclass
class Event:
    ts: float
    action: str      # UPLOAD | DOWNLOAD | DELETE_LOCAL | DELETE_REMOTE | CANCELLED | ERROR
    path: str
    detail: str = ""

    def human(self) -> str:
        t = time.strftime("%Y-%m-%d %H:%M:%S", time.localtime(self.ts))
        return f"[{t}] [{self.action}] {self.path} {self.detail}".rstrip()


class EventLogger:
    def __init__(self) -> None:
        self._lock = threading.Lock()
        self._recent: "deque[Event]" = deque(maxlen=MAX_EVENTS)
        self._disk_line_count = 0
        self._appends_since_trim_check = 0
        self._text_log_size = 0
        paths.ensure_dirs()
        self._load_recent()

    # --- startup load ---

    def _load_recent(self) -> None:
        events_path = paths.events_file()
        if events_path.exists():
            try:
                with events_path.open(encoding="utf-8") as f:
                    for line in f:
                        line = line.strip()
                        if not line:
                            continue
                        try:
                            self._recent.append(Event(**json.loads(line)))
                        except (json.JSONDecodeError, TypeError):
                            continue
                        self._disk_line_count += 1
            except OSError:
                pass
        try:
            self._text_log_size = paths.log_file().stat().st_size
        except OSError:
            self._text_log_size = 0

    # --- writing ---

    def log(self, action: str, path_: str, detail: str = "") -> Event:
        ev = Event(ts=time.time(), action=action, path=path_, detail=detail)
        with self._lock:
            self._append_text(ev)
            self._append_json(ev)
            self._recent.append(ev)
        return ev

    def _append_text(self, ev: Event) -> None:
        log_path = paths.log_file()
        line = ev.human() + "\n"
        if self._text_log_size > MAX_TEXT_LOG_BYTES:
            try:
                log_path.rename(log_path.with_suffix(".log.1"))
            except OSError:
                pass
            self._text_log_size = 0
        try:
            with log_path.open("a", encoding="utf-8") as f:
                f.write(line)
            self._text_log_size += len(line.encode("utf-8"))
        except OSError:
            pass

    def _append_json(self, ev: Event) -> None:
        events_path = paths.events_file()
        line = json.dumps(asdict(ev), ensure_ascii=False) + "\n"
        try:
            with events_path.open("a", encoding="utf-8") as f:
                f.write(line)
        except OSError:
            return
        self._disk_line_count += 1
        self._appends_since_trim_check += 1
        if self._appends_since_trim_check >= TRIM_CHECK_INTERVAL:
            self._appends_since_trim_check = 0
            self._maybe_trim(events_path)

    def _maybe_trim(self, events_path: Path) -> None:
        if self._disk_line_count <= MAX_EVENTS + TRIM_BUFFER:
            return
        # self._recent already holds exactly the last MAX_EVENTS entries (its
        # maxlen enforces that as we append) -- reuse it as the trimmed content
        # instead of re-reading the file just to figure out what to keep.
        try:
            paths.events_file().write_text(
                "".join(json.dumps(asdict(e), ensure_ascii=False) + "\n" for e in self._recent),
                encoding="utf-8",
            )
        except OSError:
            return
        self._disk_line_count = len(self._recent)

    # --- reading ---

    def tail(self, limit: int = 500, action_filter: Optional[str] = None) -> list[Event]:
        with self._lock:
            snapshot = list(self._recent)
        if action_filter:
            snapshot = [ev for ev in snapshot if ev.action == action_filter]
        return snapshot[-limit:]


_shared: Optional[EventLogger] = None


def shared() -> EventLogger:
    global _shared
    if _shared is None:
        _shared = EventLogger()
    return _shared
