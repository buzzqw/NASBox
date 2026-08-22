"""Runs local -> NAS pushes on their own thread, decoupled from PullWorker.

Previously push and pull ran serially in one engine tick loop: a pull could take
tens of seconds on a large tree (real-world observed: ~50s on a ~108k-file NASBox
folder), and since that call blocked the single thread, any push that became ready
*during* that pull had to wait for it to finish first -- a fresh local change could
sit unpushed for the better part of a minute for no reason related to that change
itself. Running push on its own thread means it only ever waits on what it
actually needs to: the transfer lock (held only while a push or pull is actually
in flight -- there is only ever one NASBox folder, so one lock is enough).
"""
from __future__ import annotations

import threading
import time
import uuid
import stat
import os
from pathlib import Path
from typing import Iterator

from PyQt6.QtCore import pyqtSignal

from . import rsync_ops
from .config import Config
from .i18n import t
from .lock_coordinator import LockCoordinator
from .logger import EventLogger
from .repository_safety import RepositorySafetyError
from .reconcile import Action, RemoteKind, plan_path
from .scan_worker import ScanWorker
from .sync_state import Fingerprint, SyncStateStore
from .transfer_worker import TransferWorker
from .watcher import WatcherHandle

TICK_SECONDS = 1

# A large batch (e.g. every path in a folder that was just bulk-copied in,
# with nothing in the baseline yet) is processed in chunks of this many paths
# rather than as one all-or-nothing remote-state-check + rsync call. Chosen to
# be big enough that per-chunk SSH/rsync overhead stays negligible, small
# enough that the first files actually land on the NAS -- and the queue/log
# show real progress -- within moments instead of only after the *entire*
# batch has been checked against the NAS, which on a large first-time import
# is what made this look frozen for tens of minutes with zero feedback.
PUSH_CHUNK_SIZE = 100

# How often (seconds) the local-hashing-phase progress signal is allowed to
# fire -- hashing a huge batch can touch thousands of files, and a Qt signal
# crossing threads for every single one would flood the GUI event queue for
# no visible benefit (same reasoning as TransferWorker's own speed-signal
# throttling).
HASH_PROGRESS_MIN_INTERVAL = 0.2


def _chunked(items: list[str], size: int) -> Iterator[list[str]]:
    for start in range(0, len(items), size):
        yield items[start:start + size]


class PushWorker(TransferWorker):
    # Emitted while local and remote preflight checks run, before any transfer
    # has started: (files_checked_so_far, total_to_check). Lets the GUI show
    # real progress during what used to be a long, silent wait.
    hash_progress = pyqtSignal(int, int)

    # Emitted once, right before the chunk loop starts: the true size of this
    # tick's whole batch. TransfersTab's own queue "total" otherwise comes
    # from ScanWorker's separate dry-run preview -- but ScanWorker only ever
    # tries a *non-blocking* acquire of the same transfer_lock this push is
    # holding for the entire (potentially many-chunk, many-minute) operation,
    # so it can never refresh while a big push is running. Without this
    # signal, TransfersTab's fallback logic (total = max(total, done)) simply
    # degrades to "total always equals done" -- a progress readout that's
    # always "N of N" and climbs with every completed chunk, which is exactly
    # as useless as showing nothing. This signal gives it the real number
    # up front, which this worker already knows before touching the network.
    batch_size_known = pyqtSignal(int)
    queue_items_known = pyqtSignal(list)

    def __init__(
        self, cfg: Config, logger: EventLogger, watchers: WatcherHandle,
        transfer_lock: threading.Lock, sync_state: SyncStateStore,
        scan_worker: ScanWorker | None = None,
        transfer_active: threading.Event | None = None,
        lock_coordinator: LockCoordinator | None = None,
    ) -> None:
        super().__init__(cfg, logger, transfer_active=transfer_active)
        self.watchers = watchers
        self.transfer_lock = transfer_lock
        self.sync_state = sync_state
        self._scan_worker = scan_worker
        self.lock_coordinator = lock_coordinator or LockCoordinator()
        self._wake = threading.Event()
        self._force_sync = threading.Event()
        self._last_hash_progress_emit = 0.0

    def _wake_now(self) -> None:
        self._wake.set()

    def wake(self) -> None:
        """Ask the worker to re-evaluate immediately instead of waiting for the next tick."""
        self._wake.set()

    def request_full_sync(self) -> None:
        """Run rsync even if the file watcher has no outstanding event.

        The transfer itself still determines which files differ. This avoids a
        costly full local checksum walk merely because the user requested a
        manual sync from the UI.
        """
        self._force_sync.set()
        watcher = self.watchers.get()
        if watcher is not None:
            # This also prevents PullWorker from winning the scheduling race.
            # The mark is safe: unknown changes are resolved from the baseline,
            # never by running a destructive full-tree push.
            watcher.mark_dirty()
        self._wake.set()

    def run(self) -> None:
        while not self._stop_flag.is_set():
            try:
                self._tick()
            except Exception as exc:  # keep the loop alive no matter what
                self._log("ERROR", "-", detail=f"push tick failed: {exc}")
            self._wake.wait(TICK_SECONDS)
            self._wake.clear()

    def _tick(self) -> None:
        if self._conn is None or self.cfg.is_paused() or not self.cfg.is_configured():
            return
        if not self.lock_coordinator.can_attempt():
            return
        if self._stop_flag.is_set() or self.cfg.is_paused():
            return
        debounce = float(self.cfg.get("debounce_seconds") or 2)
        watcher = self.watchers.get()
        force_sync = self._force_sync.is_set()
        watcher_dirty = bool(watcher and watcher.is_dirty())
        if watcher_dirty:
            dirty_paths = watcher.consume_paths_if_ready(debounce)
            if dirty_paths is None:
                return  # preserve known local changes once their debounce has elapsed
        else:
            dirty_paths = set()
        try:
            pending_paths = set(self.sync_state.pending_paths())
        except (AttributeError, TypeError):
            # Keep lightweight test doubles and older embedded callers compatible.
            pending_paths = set()
        dirty_paths.update(pending_paths)
        if not dirty_paths and not force_sync:
            return
        if dirty_paths:
            if "" in dirty_paths:
                dirty_paths = self.sync_state.changed_paths(self.cfg.local_root())
            dirty_paths = {
                path for path in dirty_paths
                if path and not rsync_ops.path_is_excluded(self.cfg, path)
            }
            self.sync_state.mark_pending(dirty_paths)

        # Resolve local paths before any NAS preflight. Watchers also report
        # directory-only events (notably Git maintenance under a synced
        # checkout). They are not transfer items, but must not remain in the
        # durable queue and retrigger a lock attempt every tick.
        resolved_paths = self._resolve_paths(dirty_paths, force_sync)
        stale_pending: set[str] = set()
        for path in pending_paths - resolved_paths:
            if rsync_ops.path_is_excluded(self.cfg, path):
                stale_pending.add(path)
                continue
            try:
                mode = Path(self.cfg.local_root(), path).lstat().st_mode
            except OSError:
                continue
            if not stat.S_ISREG(mode):
                stale_pending.add(path)
        if stale_pending:
            self.sync_state.clear_pending(stale_pending)
        if not resolved_paths:
            self._force_sync.clear()
            return
        try:
            stability_interval = max(0.0, min(float(self.cfg.get("file_stability_seconds") or 0), 10.0))
        except (TypeError, ValueError):
            stability_interval = 0.75
        try:
            stable_paths, unstable_paths = self.sync_state.stable_paths(
                self.cfg.local_root(), resolved_paths, stability_interval,
            )
        except (AttributeError, TypeError):
            stable_paths, unstable_paths = resolved_paths, set()
        if unstable_paths:
            self.sync_state.record_pending_attempt(unstable_paths, "file ancora in scrittura o instabile")
            if watcher is not None:
                for path in unstable_paths:
                    watcher.mark_dirty(path)
        resolved_paths = stable_paths
        if not resolved_paths:
            self._force_sync.clear()
            return

        # Blocks only if a pull is currently running -- there's only ever one
        # NASBox folder, so this is the only pair that can conflict.
        self.transfer_preparing.emit("upload")
        with self.transfer_lock:
            try:
                self.transfer_waiting_for_lock.emit("upload")
                with rsync_ops.remote_lock(
                    self.cfg, self._conn, on_start=self._set_current_process,
                    owner_id=self.sync_state.device_id(), priority=0,
                ):
                    self.lock_coordinator.acquired()
                    if self._stop_flag.is_set():
                        self.transfer_finished.emit("upload", False)
                        return
                    pending_ok, pending_detail = rsync_ops.retry_pending_journal(self.cfg, self._conn)
                    if not pending_ok:
                        self.cfg.set("journal_error", pending_detail or "journal non raggiungibile")
                        if watcher is not None:
                            watcher.mark_dirty()
                        self._force_sync.set()
                        self.sync_state.record_pending_attempt(dirty_paths, pending_detail or "journal non raggiungibile")
                        self._log("JOURNAL_BLOCK", "-", pending_detail or "journal non raggiungibile")
                        self.transfer_finished.emit("upload", False)
                        return
                    self.cfg.set("journal_error", "")
                    try:
                        rsync_ops.validate_transfer_safety(
                            self.cfg, self._conn,
                            destructive=bool(self.cfg.get("delete_enabled")),
                            direction="upload",
                        )
                    except RepositorySafetyError as exc:
                        if watcher is not None:
                            watcher.mark_dirty()
                        self._force_sync.set()
                        self._log("SAFETY_BLOCK", "-", str(exc))
                        self.transfer_finished.emit("upload", False)
                        return
                    if not rsync_ops.ensure_remote_dir(self.cfg, self._conn):
                        self._log("ERROR", "-", "impossibile creare la cartella remota")
                        if watcher is not None:
                            watcher.mark_dirty()  # consume_if_ready already cleared the retry trigger
                        self.transfer_finished.emit("upload", False)
                        return
                try:
                    max_deletes = int(self.cfg.get("max_delete_files") or 1000)
                except (TypeError, ValueError):
                    max_deletes = 1000

                ordered_paths = sorted(resolved_paths)
                chunks = list(_chunked(ordered_paths, PUSH_CHUNK_SIZE)) if ordered_paths else [[]]
                has_work = bool(ordered_paths)
                if has_work:
                    self.batch_size_known.emit(len(ordered_paths))
                    self.transfer_started.emit("upload")
                overall_ok = True
                deletes_committed = 0
                remote_progress_done = 0
                for chunk_index, chunk_paths in enumerate(chunks):
                    chunk_ok, deleted_count, delete_limit_hit = self._run_chunk(
                        chunk_index, chunk_paths, remote_progress_done, len(ordered_paths),
                        max_deletes, deletes_committed, watcher,
                    )
                    deletes_committed += deleted_count
                    remote_progress_done += len(chunk_paths)
                    if self._scan_worker:
                        self._scan_worker.wake()
                    if delete_limit_hit:
                        raise rsync_ops.RemoteLockError(
                            f"operazione bloccata: limite di sicurezza {max_deletes} cancellazioni raggiunto "
                            f"(già applicate {deletes_committed})"
                        )
                    if not chunk_ok:
                        overall_ok = False
                        break

                if overall_ok:
                    self._force_sync.clear()
                else:
                    if watcher is not None:
                        watcher.mark_dirty()
                    self._force_sync.set()
                # The preparation phase is a logical transfer even when every
                # path is adopted or the plan turns out empty.
                self.transfer_finished.emit("upload", overall_ok)
            except rsync_ops.RemoteLockBusy as exc:
                self._record_lock_owner(exc)
                if watcher is not None:
                    watcher.mark_dirty()
                self._force_sync.set()
                retry_after = self.lock_coordinator.defer()
                detail = f"{t('lock.busy_retry')} Nuovo tentativo tra {retry_after}s."
                self.sync_state.record_pending_attempt(dirty_paths, detail)
                self.transfer_lock_unavailable.emit("upload", detail)
                self.transfer_finished.emit("upload", False)
                self._log("LOCK_DEFERRED", "-", detail)
            except rsync_ops.RemoteLockError as exc:
                if watcher is not None:
                    watcher.mark_dirty()
                self._force_sync.set()
                detail = t("lock.acquire_failed", detail=str(exc))
                self.transfer_lock_unavailable.emit("upload", detail)
                self.transfer_finished.emit("upload", False)
                self._log("ERROR", "-", detail)
            except Exception:
                if watcher is not None:
                    watcher.mark_dirty()
                self._force_sync.set()
                self.transfer_finished.emit("upload", False)
                raise

    def _run_chunk(
        self, chunk_index: int, chunk_paths: list[str], remote_progress_done: int,
        remote_progress_total: int, max_deletes: int, deletes_committed: int, watcher,
    ) -> tuple[bool, int, bool]:
        """Process one chunk under a short-lived NAS lock.

        The lock is deliberately not held across the complete import. Every
        decision is rebuilt from live remote state and the delete operation has
        a baseline precondition, so yielding between chunks is safe and prevents
        one PC from monopolizing the repository for an unbounded time.
        """
        chunk_set = set(chunk_paths)
        self.transfer_waiting_for_lock.emit("upload")
        with rsync_ops.remote_lock(
            self.cfg, self._conn, on_start=self._set_current_process,
            owner_id=self.sync_state.device_id(), priority=0,
        ):
            if self._stop_flag.is_set():
                return False, 0, False
            chunk_run_ts = rsync_ops.new_run_ts()
            upload_paths, delete_requests, adopted_paths = self._build_plan(
                chunk_set,
                remote_progress_offset=remote_progress_done,
                remote_progress_total=remote_progress_total,
                compact_remote_manifest=chunk_index == 0,
            )
            if deletes_committed + len(delete_requests) > max_deletes:
                return False, 0, True

            planned_items = [
                rsync_ops.TransferItem("upload", path, self._local_file_size(path))
                for path in sorted(upload_paths)
            ]
            planned_items.extend(
                rsync_ops.TransferItem("delete_remote", path)
                for path, _digest, _mtime in sorted(delete_requests)
            )
            if planned_items:
                self.queue_items_known.emit(planned_items)

            if upload_paths:
                push_result = self._run_transfer_tracked(
                    rsync_ops.push, chunk_run_ts, paths=upload_paths,
                    emit_lifecycle=False,
                )
            else:
                push_result = rsync_ops.TransferResult(True, [])
            if push_result.ok:
                delete_result = rsync_ops.checked_delete_remote(
                    self.cfg, self._conn, delete_requests, chunk_run_ts,
                    self.sync_state.device_id(),
                )
            else:
                delete_result = rsync_ops.CheckedDeleteResult(False, [], set(), set())

            for item in delete_result.items:
                self._on_item_complete(item)
            for path in sorted(delete_result.stale_paths):
                self._log(
                    "STALE_DELETE", path,
                    "cancellazione ignorata: sul NAS esiste una versione successiva",
                )
            chunk_result = rsync_ops.TransferResult(
                push_result.ok and delete_result.ok,
                [*push_result.items, *delete_result.items],
                push_result.raw_error or delete_result.raw_error,
            )
            self._report_failure(chunk_result)
            if not chunk_result.ok:
                return False, len(delete_result.completed_paths), False

            journal_items = [item for item in chunk_result.items if item.direction == "upload"]
            authoritative = self._authoritative_fingerprints(
                journal_items, adopted_paths, delete_result.completed_paths, watcher,
            )
            journal_ok, journal_detail = rsync_ops.append_remote_journal(
                self.cfg, self._conn, self.sync_state.device_id(), journal_items,
                authoritative,
            )
            if journal_ok:
                self.cfg.set("journal_error", "")
                self.sync_state.record_fingerprints(authoritative)
                clearable = set(chunk_paths)
                for path, expected in authoritative.items():
                    current = self.sync_state.fingerprint(Path(self.cfg.local_root(), path))
                    if current != expected:
                        # A new local edit landed during this chunk. Keep its
                        # durable queue entry even if the watcher misses it.
                        clearable.discard(path)
                self.sync_state.clear_pending(clearable)
                return True, len(delete_result.completed_paths), False

            payload, payload_error = rsync_ops.build_remote_journal_payload(
                self.cfg, self.sync_state.device_id(), journal_items, authoritative,
            )
            if payload:
                rsync_ops.save_pending_journal(payload)
            detail = journal_detail or payload_error or "journal NAS non aggiornato"
            self.cfg.set("journal_error", detail)
            self._log("JOURNAL_ERROR", "-", detail)
            # Stop immediately. A single pending payload cannot safely hold two
            # independent failed chunks, so continuing would lose retry data.
            return False, len(delete_result.completed_paths), False

    def _authoritative_fingerprints(
        self, uploaded_items: list[rsync_ops.TransferItem], adopted_paths: set[str],
        deleted_paths: set[str], watcher,
    ) -> dict[str, Fingerprint | None]:
        uploaded_paths = {item.path for item in uploaded_items}
        remote = rsync_ops.remote_file_states(
            self.cfg, self._conn, uploaded_paths, compact=False,
        )
        if remote is None or set(remote) != uploaded_paths:
            raise rsync_ops.RemoteLockError("impossibile confermare i file appena caricati sul NAS")

        authoritative: dict[str, Fingerprint | None] = {path: None for path in deleted_paths}
        for path in adopted_paths:
            authoritative[path] = self.sync_state.fingerprint(Path(self.cfg.local_root(), path))
        for path in uploaded_paths:
            state = remote[path]
            if state.kind != RemoteKind.FILE:
                raise rsync_ops.RemoteLockError(f"il file appena caricato non risulta presente sul NAS: {path}")
            remote_fp = Fingerprint(state.digest, state.size, state.mtime_ns)
            local_fp = self.sync_state.fingerprint(Path(self.cfg.local_root(), path))
            if local_fp is not None and local_fp.digest == remote_fp.digest:
                authoritative[path] = local_fp
            else:
                authoritative[path] = remote_fp
                if watcher is not None:
                    watcher.mark_dirty(path)
        return authoritative

    def _on_hash_progress(self, done: int, total: int) -> None:
        now = time.time()
        if done < total and now - self._last_hash_progress_emit < HASH_PROGRESS_MIN_INTERVAL:
            return  # always let the final (done == total) call through, throttle the rest
        self._last_hash_progress_emit = now
        self.hash_progress.emit(done, total)

    def _resolve_paths(self, dirty_paths: set[str], force_sync: bool) -> set[str]:
        paths = set(dirty_paths)
        if force_sync or "" in paths:
            paths.discard("")
            paths.update(self.sync_state.changed_paths(self.cfg.local_root(), on_progress=self._on_hash_progress))
        resolved: set[str] = set()
        for path in paths:
            if not path or rsync_ops.path_is_excluded(self.cfg, path):
                continue
            local_path = Path(self.cfg.local_root(), path)
            try:
                mode = local_path.lstat().st_mode
            except FileNotFoundError:
                resolved.add(path)  # a real deletion candidate
                continue
            except OSError as exc:
                raise rsync_ops.RemoteLockError(f"impossibile leggere {path}: {exc}") from exc
            if stat.S_ISREG(mode):
                resolved.add(path)
            elif not stat.S_ISDIR(mode):
                self._log("UNSUPPORTED", path, "symlink o file speciale ignorato")
        return resolved

    def _local_file_size(self, relative_path: str) -> int:
        try:
            local_path = Path(self.cfg.local_root(), relative_path)
            info = local_path.lstat()
        except OSError:
            return 0
        return info.st_size if stat.S_ISREG(info.st_mode) else 0

    def _build_plan(
        self, relative_paths: set[str],
        remote_progress_offset: int = 0, remote_progress_total: int = 0,
        compact_remote_manifest: bool = True,
    ) -> tuple[set[str], list[tuple[str, str, int]], set[str]]:
        if not relative_paths:
            return set(), [], set()
        remote_progress = None
        if remote_progress_total:
            self._on_hash_progress(remote_progress_offset, remote_progress_total)
            remote_progress = lambda done, total: self._on_hash_progress(
                remote_progress_offset + done, remote_progress_total,
            )
        remote_states = rsync_ops.remote_file_states(
            self.cfg, self._conn, relative_paths,
            compact=compact_remote_manifest, on_progress=remote_progress,
        )
        if remote_states is None or set(remote_states) != relative_paths:
            raise rsync_ops.RemoteLockError("impossibile verificare in batch lo stato dei file sul NAS")

        uploads: set[str] = set()
        deletes: list[tuple[str, str, int]] = []
        adopted: set[str] = set()
        for relative_path in sorted(relative_paths):
            local_fp = self.sync_state.fingerprint(Path(self.cfg.local_root(), relative_path))
            baseline = self.sync_state.get(relative_path)
            remote = remote_states[relative_path]
            decision = plan_path(
                baseline, local_fp, remote,
                delete_enabled=bool(self.cfg.get("delete_enabled")),
            )
            if decision.action == Action.UPLOAD:
                uploads.add(relative_path)
            elif decision.action == Action.DELETE_REMOTE:
                assert baseline is not None and not baseline.is_tombstone
                deletes.append((relative_path, baseline.digest, baseline.mtime_ns // 1_000_000_000))
            elif decision.action == Action.ADOPT:
                adopted.add(relative_path)
            elif decision.action == Action.CONFLICT_LOCAL_WINS:
                conflict_path = self._conflict_path(relative_path)
                if not rsync_ops.copy_remote_file(self.cfg, self._conn, relative_path, conflict_path):
                    raise rsync_ops.RemoteLockError("impossibile conservare la versione NAS in conflitto")
                self._log("CONFLICT", relative_path, f"versione NAS salvata come {conflict_path}")
                uploads.add(relative_path)
            elif decision.action == Action.CONFLICT_REMOTE_WINS:
                if local_fp is not None:
                    conflict_path = self._conflict_path(relative_path)
                    ok, detail = rsync_ops.upload_conflict_copy(
                        self.cfg, self._conn, relative_path, conflict_path,
                    )
                    if not ok:
                        raise rsync_ops.RemoteLockError(detail or "impossibile conservare la versione locale in conflitto")
                    self._log("CONFLICT", relative_path, f"versione locale salvata sul NAS come {conflict_path}")
            elif decision.action == Action.REMOTE_WINS:
                if local_fp is None and baseline is not None and not baseline.is_tombstone:
                    self._log("STALE_DELETE", relative_path, decision.detail)
            elif decision.action == Action.BLOCK:
                raise rsync_ops.RemoteLockError(f"{relative_path}: {decision.detail}")
        return uploads, deletes, adopted

    def _set_current_process(self, proc) -> None:
        # The SSH session holding the NAS lock must be interruptible on exit,
        # just like the later rsync process. Otherwise a contended lock can
        # hold application shutdown until RemoteLock's timeout expires.
        self._current_proc = proc

    def _conflict_path(self, relative_path: str) -> str:
        path = Path(relative_path)
        token = uuid.uuid4().hex[:8]
        tag = f" (conflitto da {self.sync_state.device_id()} {token})"
        suffix = path.suffix
        max_name_bytes = 240  # below the common NAME_MAX=255 on NAS filesystems
        if len(os.fsencode(tag + suffix)) >= max_name_bytes:
            suffix = ""
            stem = path.name
        else:
            stem = path.stem
        budget = max_name_bytes - len(os.fsencode(tag + suffix))
        while stem and len(os.fsencode(stem)) > budget:
            stem = stem[:-1]
        name = f"{stem or 'file'}{tag}{suffix}"
        return str(path.with_name(name))

    def _report_failure(self, result: "rsync_ops.TransferResult") -> None:
        if result.ok:
            return
        if self._stop_flag.is_set() or self.cfg.is_paused():
            # Almost certainly our own cancel_current_transfer() (pause/quit) having
            # just SIGTERM'd this transfer, not a real rsync failure -- rsync's own
            # exit code isn't a reliable way to tell the two apart (it can catch the
            # signal and exit with an ordinary-looking positive code), so we go by
            # our own state instead.
            self._log("CANCELLED", "-", "trasferimento interrotto (pausa o chiusura in corso)")
            return
        self._log("ERROR", "-", result.raw_error or "trasferimento fallito")
