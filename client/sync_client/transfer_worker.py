"""Shared machinery for PushWorker and PullWorker: running one tracked rsync
transfer, forwarding its live progress/completions as signals, and letting
stop()/cancel_current_transfer() interrupt it -- the plumbing both directions
need identically. Kept as a shared base rather than duplicated so the two
schedulers (debounce-driven push vs poll_interval-driven pull, each with its
own retry/cancellation logic) can differ without this part drifting between
two copies.
"""
from __future__ import annotations

import threading
import time
from typing import Callable, Optional

from PyQt6.QtCore import QThread, pyqtSignal

from . import rsync_ops
from .config import Config
from .logger import EventLogger

SPEED_SIGNAL_MIN_INTERVAL = 0.2  # don't emit transfer_speed faster than this, however chatty rsync gets
CANCEL_CHECK_INTERVAL = 0.1  # how often the watchdog below polls cancel_check(), independent of rsync's own output


class TransferWorker(QThread):
    log_event = pyqtSignal(str, str, str)              # action, path, detail
    transfer_waiting_for_lock = pyqtSignal(str)         # upload | download
    transfer_preparing = pyqtSignal(str)                # upload | download, reconciliation/preflight
    transfer_phase = pyqtSignal(str, str, int, int)     # direction, phase, completed, total
    transfer_lock_unavailable = pyqtSignal(str, str)    # upload | download, detail
    transfer_started = pyqtSignal(str)                  # upload | download
    transfer_item_started = pyqtSignal(str, str, int)  # direction, path, size
    transfer_item_progress = pyqtSignal(str, str, int)  # direction, path, percent
    transfer_item_done = pyqtSignal(str, str)           # direction, path
    transfer_speed = pyqtSignal(str, float, int)        # direction, bytes_per_sec, percent
    transfer_finished = pyqtSignal(str, bool)            # upload | download, success

    def __init__(self, cfg: Config, logger: EventLogger, transfer_active: threading.Event | None = None) -> None:
        super().__init__()
        self.cfg = cfg
        self.logger = logger
        self._transfer_active = transfer_active
        self._stop_flag = threading.Event()
        self._cancel_requested = threading.Event()
        self._conn: rsync_ops.NasConnection | None = None
        self._current_proc = None  # the in-flight rsync Popen, if any -- lets stop()/cancel_current_transfer() interrupt it
        # Relative paths THIS transfer itself wrote to the local filesystem (a pull's
        # own "download"/"delete_local" items -- a push's items never touch local
        # disk, so this stays empty and unused for PushWorker). Reset at the start of
        # each transfer. See PullWorker's cancel_check for why this matters: a pull
        # writing files locally re-triggers the local watcher's own detection
        # (inotify fires on the pull's own writes exactly like it does on a real user
        # edit), so "the folder is dirty" alone can't be trusted as "something
        # external changed" while a pull with real work to do is in flight -- a
        # dirty path this transfer itself just wrote doesn't count as external.
        self._self_written_paths: set[str] = set()
        self._self_written_lock = threading.RLock()

    def _reset_self_written_paths(self) -> None:
        with self._self_written_lock:
            self._self_written_paths.clear()

    def _mark_self_written(self, path: str) -> None:
        with self._self_written_lock:
            self._self_written_paths.add(path)

    def _self_written_snapshot(self) -> set[str]:
        with self._self_written_lock:
            return set(self._self_written_paths)

    # --- external controls (mirrors the old SyncEngine API so callers don't change) ---

    def set_connection(self, conn) -> None:
        self._conn = conn

    def stop(self) -> None:
        self._stop_flag.set()
        self._cancel_requested.set()
        self._wake_now()
        proc = self._current_proc
        if proc is not None and proc.poll() is None:
            try:
                proc.terminate()
            except OSError:
                pass
        self.wait(10_000)
        if self.isRunning() and proc is not None and proc.poll() is None:
            try:
                proc.kill()
            except OSError:
                pass
            self.wait(5_000)

    def cancel_current_transfer(self) -> None:
        self._cancel_requested.set()
        proc = self._current_proc
        if proc is not None and proc.poll() is None:
            try:
                proc.terminate()
            except OSError:
                pass

    def _wake_now(self) -> None:
        """Subclasses override with their own wake mechanism (a threading.Event) so
        stop() doesn't have to wait out a full tick before noticing the stop flag."""

    # --- shared transfer execution ---

    def _run_transfer_tracked(
        self, transfer_fn, run_ts: str,
        cancel_check: Optional[Callable[[], bool]] = None,
        reset_written_paths: bool = True,
        emit_lifecycle: bool = True,
        **transfer_kwargs,
    ) -> "rsync_ops.TransferResult":
        """emit_lifecycle=False skips transfer_started/transfer_finished for this
        call -- used by PushWorker when chunking one logical push into several
        calls (see push_worker.py's PUSH_CHUNK_SIZE), so the GUI sees one
        started/finished pair for the whole chunked operation instead of one
        per chunk. Without this, a chunk with nothing to upload (pure deletes,
        or every path already resolved by ADOPT) never calls transfer_fn at all,
        so transfer_started never fires for it -- and since TransfersTab only
        clears its "waiting for the NAS lock" state on transfer_started/
        transfer_finished, a run of several such chunks in a row left the GUI
        stuck showing "NAS busy with another sync" indefinitely, even though
        real work (deletes, remote-state checks) was actively happening.

        cancel_check, if given, is polled by a dedicated watchdog thread every
        CANCEL_CHECK_INTERVAL and terminates the transfer early if it returns True --
        used by PullWorker to abort a pull the instant the NASBox folder becomes
        locally dirty, instead of letting it complete against a now-stale view of
        "what's on the NAS". This used to be checked inline in the on_progress
        callback instead of on a separate timer -- found (against a real NAS, not
        just synthetic tests) to be unreliable for a pull with little to transfer:
        --delete-after defers all deletions to the very end, and rsync's
        --info=progress2 output can be sparse-to-nonexistent when there's not much
        file content actually moving, so a fast pull that's mostly-just-deletions
        could run to completion without on_progress ever firing at all -- no
        progress lines, no chance to react. A watchdog on its own timer reacts
        within CANCEL_CHECK_INTERVAL regardless of how chatty (or silent) rsync is."""
        direction = (
            "upload"
            if transfer_fn in (rsync_ops.push, rsync_ops.push_to_staging)
            else "download"
        )
        last_speed_emit = [0.0]
        last_item_progress_emit = [0.0]
        last_item_progress_key: list[tuple[str, str] | None] = [None]
        started = [False]
        if reset_written_paths:
            self._reset_self_written_paths()
        # A cancel belongs to one rsync invocation. Keep it observable while
        # that process is being reaped, then allow the next retry to start fresh.
        self._cancel_requested.clear()
        watchdog_stop = threading.Event()

        def _cancel_check() -> bool:
            return (
                self._stop_flag.is_set()
                or self._cancel_requested.is_set()
                or (cancel_check is not None and cancel_check())
            )

        def _watchdog() -> None:
            while not watchdog_stop.wait(CANCEL_CHECK_INTERVAL):
                if _cancel_check():
                    proc = self._current_proc
                    if proc is not None and proc.poll() is None:
                        try:
                            proc.terminate()
                        except OSError:
                            pass
                    return

        def _on_start(proc) -> None:
            self._current_proc = proc
            started[0] = True
            if emit_lifecycle:
                self.transfer_started.emit(direction)
            if cancel_check is not None:
                threading.Thread(target=_watchdog, daemon=True).start()

        def _on_item(item: "rsync_ops.TransferItem") -> None:
            self._on_item_complete(item)

        def _on_item_started(item: "rsync_ops.TransferItem") -> None:
            if item.direction in ("download", "delete_local"):
                # The watcher can observe rsync's local write before rsync emits
                # the completion marker. Mark it now so PullWorker does not
                # cancel its own transfer and retry the same file forever.
                self._mark_self_written(item.path)
            self.transfer_item_started.emit(item.direction, item.path, item.size)

        def _on_item_progress(item: "rsync_ops.TransferItem", percent: int) -> None:
            # Keep progress responsive without flooding the GUI for large files.
            now = time.time()
            key = (item.direction, item.path)
            if (
                key != last_item_progress_key[0]
                or percent >= 100
                or now - last_item_progress_emit[0] >= SPEED_SIGNAL_MIN_INTERVAL
            ):
                last_item_progress_emit[0] = now
                last_item_progress_key[0] = key
                self.transfer_item_progress.emit(item.direction, item.path, percent)

        def _on_progress(percent: int, speed: float) -> None:
            # rsync can report progress many times a second; emitting a cross-thread
            # Qt signal for every single one floods the GUI event queue and pegs the
            # CPU on nothing but repainting a speed label. A live-enough readout
            # doesn't need every sample, so drop the ones that arrive too close together.
            now = time.time()
            if now - last_speed_emit[0] < SPEED_SIGNAL_MIN_INTERVAL:
                return
            last_speed_emit[0] = now
            self.transfer_speed.emit(direction, speed, percent)

        result = None
        if self._transfer_active is not None:
            self._transfer_active.set()
        try:
            result = transfer_fn(
                self.cfg, self._conn, run_ts,
                on_item=_on_item, on_item_started=_on_item_started,
                on_item_progress=_on_item_progress,
                on_progress=_on_progress, on_start=_on_start,
                cancel_check=_cancel_check,
                **transfer_kwargs,
            )
            return result
        finally:
            if self._transfer_active is not None:
                self._transfer_active.clear()
            watchdog_stop.set()
            self._current_proc = None
            self.transfer_speed.emit(direction, 0.0, 100)  # idle marker: resets the GUI label
            if started[0] and emit_lifecycle:
                self.transfer_finished.emit(direction, bool(result and result.ok))

    def _on_item_complete(self, item: "rsync_ops.TransferItem") -> None:
        if item.direction in ("download", "delete_local"):
            self._mark_self_written(item.path)
        action_map = {
            "upload": "UPLOAD",
            "download": "DOWNLOAD",
            "delete_local": "DELETE_LOCAL",
            "delete_remote": "DELETE_REMOTE",
            "rename_remote": "RENAME_REMOTE",
        }
        action = action_map.get(item.direction, item.direction.upper())
        detail = f"{item.size} byte" if item.size else ""
        self._log(action, item.path, detail)
        self.transfer_item_done.emit(item.direction, item.path)

    def _log(self, action: str, path_: str, detail: str = "") -> None:
        self.logger.log(action, path_, detail)
        self.log_event.emit(action, path_, detail)

    def _record_lock_owner(self, error: rsync_ops.RemoteLockBusy) -> None:
        """Persist owner metadata returned by the lock attempt itself."""
        self.cfg.set("server_lock_owner_id", error.owner_id)
        self.cfg.set("server_lock_owner_host", error.owner_host)
        self.cfg.set("server_lock_started_at", error.started_at)
