"""Local change detection for the NASBox folder, so pushes happen promptly on
create/modify/delete instead of waiting for the next periodic scan. Mirrors the
legacy sync-daemon.sh approach: inotifywait when available, mtime polling
otherwise.
"""
from __future__ import annotations

import csv
import os
import re
import shutil
import subprocess
import threading
import time
from typing import Callable, Optional

from . import paths

POLL_FALLBACK_SECONDS = 2
UNKNOWN_PATH = ""  # sentinel for a dirty mark whose specific relative path couldn't be resolved
INTERNAL_PATH_PARTS = {".sync-partial", ".sync-trash", ".nasbox-root", ".~tmp~"}
RSYNC_TEMP_FILE_RE = re.compile(r"^\..+\.[A-Za-z0-9]{6}$")


def has_inotify() -> bool:
    return shutil.which("inotifywait") is not None


class FolderWatcher:
    def __init__(
        self, local_path: str,
        on_error: Optional[Callable[[str], None]] = None,
        is_excluded: Optional[Callable[[str], bool]] = None,
    ) -> None:
        self.path = local_path
        self._on_error = on_error
        self._is_excluded = is_excluded
        self.dirty_ts: float | None = None
        # Which specific relative path(s) are behind the current dirty state. Lets
        # PullWorker tell "this dirty mark is just my own in-flight pull writing a
        # file it's downloading" apart from "something genuinely external changed"
        # -- a plain dirty boolean/timestamp can't make that distinction, and a
        # pull's own downloads/deletions mark this watcher dirty exactly the same
        # way a real local edit does (see pull_worker.py's docstring).
        self._dirty_paths: set[str] = set()
        self._state_lock = threading.Lock()
        self._stop = threading.Event()
        self._proc: subprocess.Popen | None = None
        self._thread = threading.Thread(target=self._run, daemon=True)

    def start(self) -> None:
        self._thread.start()

    def stop(self) -> None:
        self._stop.set()
        if self._proc is not None:
            self._proc.terminate()
        self._thread.join(timeout=3)

    def mark_dirty(self, rel_path: Optional[str] = None) -> None:
        with self._state_lock:
            self.dirty_ts = time.time()
            self._dirty_paths.add(rel_path if rel_path is not None else UNKNOWN_PATH)

    def is_dirty(self) -> bool:
        """True from the moment a local change is detected until it's been pushed
        (consume_if_ready cleared it) -- regardless of whether the debounce window
        has elapsed yet. Used to hold off a --delete-enabled pull until any pending
        local change has been safely pushed first; see SyncEngine._tick."""
        with self._state_lock:
            return self.dirty_ts is not None

    def dirty_paths(self) -> set[str]:
        """Relative paths behind the current dirty state (UNKNOWN_PATH included if
        any dirty mark's path couldn't be resolved -- treat that conservatively,
        i.e. as possibly-external, rather than assuming it's explainable)."""
        with self._state_lock:
            return set(self._dirty_paths)

    def consume_if_ready(self, debounce_seconds: float) -> bool:
        """True (and clears the flag) once the folder has been quiet for debounce_seconds."""
        with self._state_lock:
            ts = self.dirty_ts
            if ts is None:
                return False
            if time.time() - ts >= debounce_seconds:
                self.dirty_ts = None
                self._dirty_paths.clear()
                return True
            return False

    def consume_paths_if_ready(self, debounce_seconds: float) -> set[str] | None:
        """Return the paths behind a debounced change, or None when not ready."""
        with self._state_lock:
            ts = self.dirty_ts
            if ts is None or time.time() - ts < debounce_seconds:
                return None
            paths = set(self._dirty_paths)
            self.dirty_ts = None
            self._dirty_paths.clear()
            return paths

    def _to_relative(self, abs_path: str) -> Optional[str]:
        try:
            return os.path.relpath(abs_path, self.path)
        except (ValueError, TypeError):
            return None

    def _is_internal_path(self, relative_path: Optional[str]) -> bool:
        if not relative_path:
            return False
        if any(part in INTERNAL_PATH_PARTS for part in relative_path.split(os.sep)):
            return True
        basename = relative_path.rsplit(os.sep, 1)[-1]
        if RSYNC_TEMP_FILE_RE.fullmatch(basename):
            return True
        return bool(self._is_excluded and self._is_excluded(relative_path))

    # --- internals ---

    def _run(self) -> None:
        if has_inotify():
            self._run_inotify()
        else:
            self._run_poll()

    def _run_inotify(self) -> None:
        try:
            self._proc = subprocess.Popen(
                [
                    "inotifywait", "-m", "-r", "-q",
                    "-e", "close_write", "-e", "moved_to", "-e", "moved_from",
                    "-e", "delete", "-e", "create", "-e", "modify",
                    "--csv", self.path,
                ],
                stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True,
            )
        except OSError:
            self._run_poll()
            return
        assert self._proc.stdout is not None
        for row in csv.reader(self._proc.stdout):
            if self._stop.is_set():
                break
            if len(row) < 3:
                continue
            # CSV quoting preserves commas, quotes and embedded newlines in a
            # filename; line-oriented --format output cannot do that.
            relative = self._to_relative(os.path.join(row[0], row[2]))
            if not self._is_internal_path(relative):
                self.mark_dirty(relative)

        if self._stop.is_set():
            return  # normal shutdown (stop() terminated the process), nothing more to do

        # inotifywait exited on its own -- e.g. it hit the OS's inotify watch limit on
        # a large tree, or the watched path was removed. Without falling back here, this
        # watcher's thread would just end: local changes would silently stop being
        # detected/pushed until the app is restarted, with no error anywhere.
        stderr = ""
        if self._proc.stderr is not None:
            stderr = self._proc.stderr.read().strip()
        if self._on_error:
            reason = stderr or f"inotifywait terminato inaspettatamente (uscita {self._proc.returncode})"
            self._on_error(
                f"controllo modifiche via inotify interrotto per '{self.path}', "
                f"passo al controllo periodico ogni {POLL_FALLBACK_SECONDS}s: {reason}"
            )
        self._run_poll()

    def _run_poll(self) -> None:
        stamp = paths.watch_stamp_file()
        stamp.parent.mkdir(parents=True, exist_ok=True)
        stamp.touch(exist_ok=True)
        # `find -newer` (below) only ever catches additions/modifications -- a file
        # that no longer exists can't show up as "newer than X", so a pure local
        # deletion with nothing else changing would otherwise never mark the watcher
        # dirty, and so never get pushed: the file would just sit on the NAS forever.
        # Diffing a full listing against the previous cycle's catches that (None on
        # the first cycle, so a fresh watcher doesn't treat "nothing scanned yet" as
        # "everything was just deleted").
        known_files: set[str] | None = None
        while not self._stop.is_set():
            changed_paths: set[str] = set()
            # Capture the next watermark *before* scanning. Replacing the old
            # one afterwards means a write that lands during this scan remains
            # newer than the next watermark and is caught on the next pass.
            next_stamp = stamp.with_name(f"{stamp.name}.next")
            try:
                next_stamp.touch(exist_ok=True)
            except OSError:
                next_stamp = None
            try:
                proc = subprocess.run(
                    ["find", self.path, "-newer", str(stamp), "-type", "f", "-print0"],
                    capture_output=True, timeout=10,
                )
                for raw_path in proc.stdout.split(b"\0"):
                    if not raw_path:
                        continue
                    relative = self._to_relative(os.fsdecode(raw_path))
                    if not self._is_internal_path(relative):
                        changed_paths.add(relative or UNKNOWN_PATH)
            except (subprocess.TimeoutExpired, OSError):
                pass
            if next_stamp is not None:
                try:
                    next_stamp.replace(stamp)
                except OSError:
                    pass

            try:
                proc = subprocess.run(
                    ["find", self.path, "-type", "f", "-print0"],
                    capture_output=True, timeout=10,
                )
                current_files: set[str] | None = {
                    os.fsdecode(raw_path) for raw_path in proc.stdout.split(b"\0") if raw_path
                }
            except (subprocess.TimeoutExpired, OSError):
                current_files = None

            if current_files is not None:
                if known_files is not None:
                    for abs_path in known_files - current_files:
                        relative = self._to_relative(abs_path)
                        if not self._is_internal_path(relative):
                            changed_paths.add(relative or UNKNOWN_PATH)
                known_files = current_files

            for rel_path in changed_paths:
                self.mark_dirty(rel_path)
            self._stop.wait(POLL_FALLBACK_SECONDS)


class WatcherHandle:
    """Thread-safe single slot holding the current FolderWatcher (or None), shared
    between SyncEngine (the only writer -- creates/replaces/clears it as local_root
    is set up, relocated, or cleared) and PushWorker/PullWorker (readers, running on
    their own threads). There is only ever at most one watcher now (one NASBox
    folder per client), but the handle still needs a lock: SyncEngine can swap it
    (e.g. after the user relocates the folder) while a worker thread is mid-read."""

    def __init__(self) -> None:
        self._lock = threading.Lock()
        self._watcher: Optional[FolderWatcher] = None

    def set(self, watcher: Optional["FolderWatcher"]) -> None:
        with self._lock:
            self._watcher = watcher

    def get(self) -> Optional["FolderWatcher"]:
        with self._lock:
            return self._watcher
