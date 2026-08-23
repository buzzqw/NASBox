"""Background scanner that feeds the Trasferimenti tab's queue preview.

Runs on its own QThread, decoupled from SyncEngine's push/pull loop: a big
real transfer no longer delays the queue view (which used to go stale and
contradict the live speed readout), and a slow/large preview can no longer
delay a push or pull that's ready to go.

Woken right after SyncEngine finishes a push/pull round, so the queue
reflects real activity promptly. The normal preview uses the NAS journal-backed
manifest and the local watcher; an rsync dry-run remains only as a compatibility
fallback for an older or temporarily unavailable server protocol.

Doesn't resolve its own NAS connection -- SyncEngine already does that on its
own cadence (HOST_RECHECK_SECONDS in engine.py), and connecting this worker's
set_connection() to its connection_changed signal reuses that instead of two
threads independently opening probe sockets to the same host.
"""
from __future__ import annotations

import subprocess
import threading
from pathlib import Path

from PyQt6.QtCore import QThread, pyqtSignal

from . import paths, rsync_ops
from .config import Config
from .lock_coordinator import LockCoordinator
from .reconcile import Action, RemoteKind, plan_path
from .sync_state import SyncStateStore
from .watcher import WatcherHandle

FALLBACK_INTERVAL_SECONDS = 60


class ScanWorker(QThread):
    queue_updated = pyqtSignal(list)
    scan_started = pyqtSignal()
    scan_finished = pyqtSignal()

    def __init__(
        self, cfg: Config, transfer_active: threading.Event | None = None,
        sync_state: SyncStateStore | None = None,
        transfer_lock: threading.Lock | None = None,
        watchers: WatcherHandle | None = None,
        lock_coordinator: LockCoordinator | None = None,
        push_requested: threading.Event | None = None,
    ) -> None:
        super().__init__()
        self.cfg = cfg
        self._stop_flag = threading.Event()
        self._wake = threading.Event()
        self._conn: rsync_ops.NasConnection | None = None
        self._current_proc = None  # the fallback dry-run Popen, if any
        self._transfer_active = transfer_active
        self.sync_state = sync_state
        self.transfer_lock = transfer_lock
        self.watchers = watchers
        self.lock_coordinator = lock_coordinator or LockCoordinator()
        self.push_requested = push_requested
        self._manifest_revision = -1
        self._manifest_entries: dict[str, rsync_ops.RemoteState] | None = None

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
        if conn != self._conn:
            self._manifest_revision = -1
            self._manifest_entries = None
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
        if not self.lock_coordinator.can_attempt():
            return
        watcher = self.watchers.get() if self.watchers is not None else None
        if (
            (self.push_requested is not None and self.push_requested.is_set())
            or
            (watcher is not None and watcher.is_dirty())
            or (self.sync_state is not None and self.sync_state.has_pending())
        ):
            # A preview can hash thousands of local paths. Never let it acquire
            # the shared gate ahead of a real push: the latter makes the queue
            # durable and clears the dirty marker, after which this preview can
            # safely refresh from the committed state.
            return
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
            items = self._manifest_preview()
            if items is None:
                items = rsync_ops.scan(self.cfg, self._conn, on_start=_on_start)
            completed = True
        finally:
            self._current_proc = None
            if self.transfer_lock is not None:
                self.transfer_lock.release()
            if not completed:
                self.scan_finished.emit()
        self.queue_updated.emit(items)
        self.scan_finished.emit()

    def _manifest_preview(self) -> list[rsync_ops.TransferItem] | None:
        """Build the queue from journal state without a recursive NAS scan.

        ``None`` deliberately means "the manifest path is unavailable": the
        caller then uses the old dry-run fallback rather than showing an
        incomplete queue as if it were authoritative.
        """
        if self.sync_state is None or not self.cfg.get("remote_server_script"):
            return None
        watcher = self.watchers.get() if self.watchers is not None else None
        if watcher is None:
            return None

        try:
            # Preview is optional and must never queue behind another PC's long
            # transfer. Use a zero-wait NAS lock for the manifest operation;
            # the dry-run fallback below remains safe if the lock is busy.
            lock_file = self.cfg.get("server_lock_file_remote")
            if isinstance(lock_file, str) and lock_file.strip().endswith("sync-transfer.lock"):
                with rsync_ops.remote_lock(
                    self.cfg, self._conn, timeout=0,
                    owner_id=self.sync_state.device_id() if self.sync_state is not None else "preview",
                    priority=3,
                ):
                    snapshot = rsync_ops.remote_manifest_snapshot(
                        self.cfg, self._conn, self._manifest_revision,
                    )
                    self.lock_coordinator.acquired()
            else:
                # Old/partially configured clients can still show a best-effort
                # preview; real transfers are blocked by validate_transfer_safety.
                snapshot = rsync_ops.remote_manifest_snapshot(
                    self.cfg, self._conn, self._manifest_revision,
                )
        except rsync_ops.RemoteLockBusy as exc:
            self.cfg.set("server_lock_owner_id", exc.owner_id)
            self.cfg.set("server_lock_owner_host", exc.owner_host)
            self.cfg.set("server_lock_started_at", exc.started_at)
            self.lock_coordinator.defer()
            return None
        if snapshot is None:
            return None
        revision, entries = snapshot
        if entries is not None:
            self._manifest_revision = revision
            self._manifest_entries = entries
        if self._manifest_entries is None:
            return None

        dirty_paths = watcher.dirty_paths() if watcher.is_dirty() else set()
        if "" in dirty_paths:
            # The watcher could not resolve at least one event. This is still a
            # local-only sweep; it does not reintroduce a recursive NAS scan.
            dirty_paths = self.sync_state.changed_paths(self.cfg.local_root())

        baseline = self.sync_state.all_entries()
        manifest = self._manifest_entries
        candidates = {
            path for path, remote in manifest.items()
            if remote.kind == RemoteKind.TOMBSTONE
            or path not in baseline
            or baseline[path].is_tombstone
            or baseline[path].digest != remote.digest
        }
        # The manifest is a journal-backed change set, not a complete filesystem
        # inventory: older files may predate journal support. Do not interpret a
        # baseline path missing from it as a remote deletion; the regular pull
        # remains responsible for discovering such legacy/unjournalized changes.
        candidates.update(dirty_paths)
        candidates = {
            path for path in candidates
            if path
            and not rsync_ops.path_is_excluded(self.cfg, path)
            and not Path(self.cfg.local_root(), path).is_dir()
        }

        remote_states = {
            path: manifest[path] for path in candidates if path in manifest
        }
        unknown_remote = candidates - set(remote_states)
        if unknown_remote:
            try:
                lock_file = self.cfg.get("server_lock_file_remote")
                if isinstance(lock_file, str) and lock_file.strip().endswith("sync-transfer.lock"):
                    with rsync_ops.remote_lock(
                        self.cfg, self._conn, timeout=0,
                        owner_id=self.sync_state.device_id() if self.sync_state is not None else "preview",
                        priority=3,
                    ):
                        remote = rsync_ops.remote_file_states(
                            self.cfg, self._conn, unknown_remote, compact=False,
                        )
                else:
                    remote = rsync_ops.remote_file_states(
                        self.cfg, self._conn, unknown_remote, compact=False,
                    )
            except rsync_ops.RemoteLockBusy as exc:
                self.cfg.set("server_lock_owner_id", exc.owner_id)
                self.cfg.set("server_lock_owner_host", exc.owner_host)
                self.cfg.set("server_lock_started_at", exc.started_at)
                self.lock_coordinator.defer()
                return None
            if remote is None or set(remote) != unknown_remote:
                return None
            remote_states.update(remote)

        items: list[rsync_ops.TransferItem] = []
        for path in sorted(candidates):
            local_fingerprint = self.sync_state.fingerprint(Path(self.cfg.local_root(), path))
            decision = plan_path(
                baseline.get(path),
                local_fingerprint,
                remote_states.get(path, rsync_ops.RemoteState(RemoteKind.ABSENT)),
                delete_enabled=bool(self.cfg.get("delete_enabled")),
            )
            if decision.action in (Action.UPLOAD, Action.CONFLICT_LOCAL_WINS):
                items.append(rsync_ops.TransferItem(
                    "upload", path, local_fingerprint.size if local_fingerprint else 0,
                ))
            elif decision.action == Action.DELETE_REMOTE:
                items.append(rsync_ops.TransferItem("delete_remote", path))
            elif decision.action in (Action.REMOTE_WINS, Action.CONFLICT_REMOTE_WINS):
                remote = remote_states.get(path)
                if remote is not None and remote.kind == RemoteKind.FILE:
                    items.append(rsync_ops.TransferItem("download", path, remote.size))
                elif self.cfg.get("delete_enabled") and local_fingerprint is not None:
                    items.append(rsync_ops.TransferItem("delete_local", path))
        return items
