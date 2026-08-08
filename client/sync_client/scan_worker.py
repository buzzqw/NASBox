"""Background dry-run scanner that feeds the Trasferimenti tab's queue preview.

Runs on its own QThread, decoupled from SyncEngine's push/pull loop: a big
real transfer no longer delays the queue view (which used to go stale and
contradict the live speed readout), and a slow/large dry-run scan can no
longer delay a push or pull that's ready to go.

Woken right after SyncEngine finishes a push/pull round, so the queue
reflects real activity promptly, with a longer timer as a fallback to
notice NAS-side changes made by other clients between pulls.

Doesn't resolve its own NAS connection -- SyncEngine already does that on its
own cadence (HOST_RECHECK_SECONDS in engine.py), and connecting this worker's
set_connection() to its connection_changed signal reuses that instead of two
threads independently opening probe sockets to the same host.
"""
from __future__ import annotations

import threading
from pathlib import Path

from PyQt6.QtCore import QThread, pyqtSignal

from . import rsync_ops
from .config import Config
from .reconcile import Action, RemoteKind, plan_path
from .sync_state import SyncStateStore

FALLBACK_INTERVAL_SECONDS = 60


class ScanWorker(QThread):
    queue_updated = pyqtSignal(list)
    scan_started = pyqtSignal()
    scan_finished = pyqtSignal()

    def __init__(
        self, cfg: Config, transfer_active: threading.Event | None = None,
        sync_state: SyncStateStore | None = None,
        transfer_lock: threading.Lock | None = None,
    ) -> None:
        super().__init__()
        self.cfg = cfg
        self._stop_flag = threading.Event()
        self._wake = threading.Event()
        self._conn: rsync_ops.NasConnection | None = None
        self._current_proc = None  # the in-flight dry-run rsync Popen, if any -- lets stop() cancel it
        self._transfer_active = transfer_active
        self.sync_state = sync_state
        self.transfer_lock = transfer_lock

    def stop(self) -> None:
        self._stop_flag.set()
        self._wake.set()
        proc = self._current_proc
        if proc is not None and proc.poll() is None:
            # Without this, a dry-run scan in flight when the app quits keeps running
            # (subprocess.Popen.communicate has no external cancel of its own), blocking
            # this wait() for nothing and potentially outliving the GUI as an orphan.
            try:
                proc.terminate()
            except OSError:
                pass
            try:
                proc.wait(timeout=5)
            except (OSError, subprocess.TimeoutExpired):
                try:
                    proc.kill()
                except OSError:
                    pass
        self.wait(10_000)

    def wake(self) -> None:
        """Ask for a rescan now instead of waiting for the fallback timer."""
        self._wake.set()

    def set_connection(self, conn) -> None:
        """Slot for SyncEngine.connection_changed -- a plain attribute swap is
        thread-safe enough here (CPython GIL), no lock needed for a single
        reference read/write like this."""
        self._conn = conn

    def run(self) -> None:
        while not self._stop_flag.is_set():
            try:
                self._scan_once()
            except Exception as exc:
                import traceback
                try:
                    detail = traceback.format_exc()
                    paths.ensure_dirs()
                    with paths.log_file().open("a", encoding="utf-8") as f:
                        f.write(f"[SCAN ERROR] {exc}\n{detail}\n")
                except OSError:
                    pass
            self._wake.wait(FALLBACK_INTERVAL_SECONDS)
            self._wake.clear()

    def _scan_once(self) -> None:
        if self._conn is None or not self.cfg.is_configured():
            return  # nothing to preview until a connection is resolved and the folder is set up
        if self._transfer_active is not None and self._transfer_active.is_set():
            return  # never compete with a real transfer for NAS/CPU/IO
        lock_acquired = self.transfer_lock is None or self.transfer_lock.acquire(blocking=False)
        if not lock_acquired:
            return

        self.scan_started.emit()
        items: list[rsync_ops.TransferItem] = []
        completed = False

        def _on_start(proc) -> None:
            self._current_proc = proc

        try:
            items = rsync_ops.scan(self.cfg, self._conn, on_start=_on_start)
            if self.sync_state is not None:
                changed = {
                    path for path in self.sync_state.changed_paths(self.cfg.local_root())
                    if not rsync_ops.path_is_excluded(self.cfg, path)
                    and not Path(self.cfg.local_root(), path).is_dir()
                }
                remote = rsync_ops.remote_file_states(self.cfg, self._conn, changed)
                if remote is not None:
                    by_path = {item.path: item for item in items}
                    for path in changed:
                        local_fingerprint = self.sync_state.fingerprint(Path(self.cfg.local_root(), path))
                        decision = plan_path(
                            self.sync_state.get(path),
                            local_fingerprint,
                            remote[path],
                            delete_enabled=bool(self.cfg.get("delete_enabled")),
                        )
                        by_path.pop(path, None)
                        if decision.action in (Action.UPLOAD, Action.CONFLICT_LOCAL_WINS):
                            by_path[path] = rsync_ops.TransferItem(
                                "upload", path, local_fingerprint.size if local_fingerprint else 0,
                            )
                        elif decision.action == Action.DELETE_REMOTE:
                            by_path[path] = rsync_ops.TransferItem("delete_remote", path)
                        elif decision.action in (Action.REMOTE_WINS, Action.CONFLICT_REMOTE_WINS):
                            if remote[path].kind == RemoteKind.FILE:
                                by_path[path] = rsync_ops.TransferItem(
                                    "download", path, remote[path].size,
                                )
                            elif self.cfg.get("delete_enabled"):
                                 by_path[path] = rsync_ops.TransferItem("delete_local", path)
                    items = [by_path[path] for path in sorted(by_path)]
            completed = True
        finally:
            self._current_proc = None
            if self.transfer_lock is not None:
                self.transfer_lock.release()
            if not completed:
                self.scan_finished.emit()
        self.queue_updated.emit(items)
        self.scan_finished.emit()
