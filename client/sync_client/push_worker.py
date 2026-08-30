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
import hashlib
from dataclasses import dataclass
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
from .sync_state import CausalVersion, Fingerprint, SyncStateStore
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

# A path with no live local baseline can only be a new upload or an out-of-band
# NAS change. New uploads use the non-overwriting staging protocol, so they can
# be grouped more aggressively without making an existing-path transfer hold
# the lease for longer. If the NAS reports an existing path, _run_chunk falls
# back to PUSH_CHUNK_SIZE before taking the conservative direct path.
STAGING_CHUNK_SIZE = 500

# How often (seconds) the local-hashing-phase progress signal is allowed to
# fire -- hashing a huge batch can touch thousands of files, and a Qt signal
# crossing threads for every single one would flood the GUI event queue for
# no visible benefit (same reasoning as TransferWorker's own speed-signal
# throttling).
HASH_PROGRESS_MIN_INTERVAL = 0.2


@dataclass(frozen=True)
class _RenamePlan:
    source_path: str
    destination_path: str
    kind: str
    entries: tuple[tuple[str, str, Fingerprint, Fingerprint], ...]
    source_causals: tuple[tuple[str, CausalVersion | None], ...]
    expected_mtime_ns: int = 0

    @property
    def paths(self) -> set[str]:
        return {
            path
            for source, destination, _baseline, _local in self.entries
            for path in (source, destination)
        }

    @property
    def size(self) -> int:
        return sum(local.size for _source, _destination, _baseline, local in self.entries)


def _chunked(items: list[str], size: int) -> Iterator[list[str]]:
    for start in range(0, len(items), size):
        yield items[start:start + size]


class PushWorker(TransferWorker):
    # Emitted while local and remote preflight checks run, before any transfer
    # has started: (files_checked_so_far, total_to_check). Lets the GUI show
    # real progress during what used to be a long, silent wait.
    hash_progress = pyqtSignal(int, int)

    # Emitted once, right before the chunk loop starts: the true size of this
    # tick's whole batch. ScanWorker can now refresh between chunks, but its
    # preview is still only a best effort and can lag a fast initial import.
    # This signal gives TransfersTab the authoritative total up front instead
    # of degrading to a misleading "N of N" counter as chunks complete.
    batch_size_known = pyqtSignal(int)
    queue_items_known = pyqtSignal(list)

    def __init__(
        self, cfg: Config, logger: EventLogger, watchers: WatcherHandle,
        transfer_lock: threading.Lock, sync_state: SyncStateStore,
        scan_worker: ScanWorker | None = None,
        transfer_active: threading.Event | None = None,
        lock_coordinator: LockCoordinator | None = None,
        force_sync: threading.Event | None = None,
    ) -> None:
        super().__init__(cfg, logger, transfer_active=transfer_active)
        self.watchers = watchers
        self.transfer_lock = transfer_lock
        self.sync_state = sync_state
        self._scan_worker = scan_worker
        self.lock_coordinator = lock_coordinator or LockCoordinator()
        self._wake = threading.Event()
        self._force_sync = force_sync or threading.Event()
        self._last_hash_progress_emit = 0.0
        self._conflict_journal_items: list[rsync_ops.TransferItem] = []
        self._causal_journal_versions: dict[str, CausalVersion | None] = {}
        self._staging_upload_paths: set[str] = set()
        self._directory_events: set[str] = set()
        self._completed_rename_paths: set[str] = set()

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
        if (
            not self.cfg.allows_push()
            or self._conn is None
            or self.cfg.is_paused()
            or not self.cfg.is_configured()
        ):
            return
        if not self.lock_coordinator.can_attempt():
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
        full_sweep = "" in dirty_paths or self._force_sync.is_set()
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
                # Keep the full-sweep sentinel durable while changed_paths()
                # walks the tree. Otherwise ScanWorker can claim the scheduler
                # in this gap and delay the real push behind its own preview.
                self.sync_state.mark_pending({""})
                dirty_paths = self.sync_state.changed_paths(self.cfg.local_root())
            dirty_paths = {
                path for path in dirty_paths
                if path and not rsync_ops.path_is_excluded(self.cfg, path)
            }
            if dirty_paths:
                self.sync_state.mark_pending(dirty_paths)

        # Resolve local paths before any NAS preflight. Watchers also report
        # directory-only events (notably Git maintenance under a synced
        # checkout). They are not transfer items, but must not remain in the
        # durable queue and retrigger a lock attempt every tick.
        resolved_paths = self._resolve_paths(dirty_paths, force_sync)
        # A full watcher sweep starts with the empty-path sentinel. Persist the
        # real paths only after resolving that sentinel: otherwise a restart can
        # retain an empty durable queue while the preview still shows thousands
        # of uploads, leaving no reliable retry trigger.
        if resolved_paths:
            self.sync_state.mark_pending(resolved_paths)
        if "" in pending_paths or full_sweep:
            self.sync_state.clear_pending({""})
        stale_pending: set[str] = set()
        for path in (pending_paths | dirty_paths) - resolved_paths:
            if rsync_ops.path_is_excluded(self.cfg, path):
                stale_pending.add(path)
                continue
            try:
                mode = Path(self.cfg.local_root(), path).lstat().st_mode
            except OSError:
                if path in self._directory_events:
                    stale_pending.add(path)
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
                            destructive=self.cfg.allows_remote_deletions(),
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
                staging_candidates = [
                    path for path in ordered_paths if self._is_staging_candidate(path)
                ]
                staging_candidate_set = set(staging_candidates)
                conservative_paths = [
                    path for path in ordered_paths if path not in staging_candidate_set
                ]
                chunks = [
                    *_chunked(staging_candidates, STAGING_CHUNK_SIZE),
                    *_chunked(conservative_paths, PUSH_CHUNK_SIZE),
                ] if ordered_paths else [[]]
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
                    if chunk_ok and chunk_index + 1 < len(chunks):
                        self._yield_local_transfer_lock()
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

    def _yield_local_transfer_lock(self) -> None:
        """Let same-client pull, mirror and previews run between safe chunks.

        _run_chunk has already released its NAS lease and committed its journal
        transaction before this point. The following chunk rebuilds its plan
        from live NAS state, so another local operation cannot make its older
        decisions unsafe while it gets a turn.
        """
        self.transfer_lock.release()
        try:
            # Yield to a waiting thread before immediately queueing this push's
            # next chunk again. Lock acquisition still serializes filesystem work.
            time.sleep(0.01)
        finally:
            self.transfer_lock.acquire()

    def _run_chunk(
        self, chunk_index: int, chunk_paths: list[str], remote_progress_done: int,
        remote_progress_total: int, max_deletes: int, deletes_committed: int, watcher,
        *, allow_large_staging: bool = True,
    ) -> tuple[bool, int, bool]:
        """Process one chunk under a short-lived NAS lock.

        The lock is deliberately not held across the complete import. Every
        decision is rebuilt from live remote state and the delete operation has
        a baseline precondition, so yielding between chunks is safe and prevents
        one PC from monopolizing the repository for an unbounded time.
        """
        chunk_set = set(chunk_paths)
        staged_result = self._try_run_staged_chunk(
            chunk_set, remote_progress_done, remote_progress_total, chunk_index, watcher,
        )
        if staged_result is not None:
            return staged_result
        if allow_large_staging and len(chunk_set) > PUSH_CHUNK_SIZE:
            # An apparently new path exists on the NAS after all. Rebuild the
            # conservative plan in small chunks, so an out-of-band edit cannot
            # turn a large staging batch into a long lease holder.
            deleted_count = 0
            for offset, fallback_paths in enumerate(_chunked(sorted(chunk_set), PUSH_CHUNK_SIZE)):
                ok, deleted, delete_limit_hit = self._run_chunk(
                    chunk_index, fallback_paths,
                    remote_progress_done + offset * PUSH_CHUNK_SIZE,
                    remote_progress_total, max_deletes, deletes_committed + deleted_count,
                    watcher, allow_large_staging=False,
                )
                deleted_count += deleted
                if not ok or delete_limit_hit:
                    return ok, deleted_count, delete_limit_hit
            return True, deleted_count, False
        self.transfer_waiting_for_lock.emit("upload")
        with rsync_ops.remote_lock(
            self.cfg, self._conn, on_start=self._set_current_process,
            owner_id=self.sync_state.device_id(), priority=0,
        ) as lock:
            self.lock_coordinator.acquired()
            if self._stop_flag.is_set():
                return False, 0, False
            chunk_run_ts = rsync_ops.new_run_ts()
            self._conflict_journal_items = []
            self._causal_journal_versions = {}
            self.transfer_phase.emit("upload", "checking", remote_progress_done, remote_progress_total)
            lock.set_activity("checking", remote_progress_done, remote_progress_total)
            upload_paths, delete_requests, adopted_paths, remote_wins_paths, rename_plans = self._build_plan(
                chunk_set,
                remote_progress_offset=remote_progress_done,
                remote_progress_total=remote_progress_total,
                compact_remote_manifest=chunk_index == 0,
            )
            self._completed_rename_paths = set()
            rename_items, failed_rename_paths = self._run_renames(rename_plans, watcher, lock)
            if failed_rename_paths:
                # The V2 command rejects a changed source or occupied target.
                # Rebuild those paths as ordinary upload/delete work instead of
                # retrying a rename decision against a concurrent edit.
                fallback_paths = chunk_set - self._completed_rename_paths
                self._conflict_journal_items = []
                self._causal_journal_versions = {}
                upload_paths, delete_requests, adopted_paths, remote_wins_paths, _ = self._build_plan(
                    fallback_paths,
                    remote_progress_offset=remote_progress_done,
                    remote_progress_total=remote_progress_total,
                    compact_remote_manifest=False,
                    allow_renames=False,
                )
            # The live NAS state has authoritatively won over this now-missing
            # local file. Persist that local deletion as a tombstone as well as
            # clearing the pending entry: changed_paths() then stops rediscovering
            # the same absence on every fallback scan, while PullWorker still sees
            # the remote file as a change to converge.
            self.sync_state.record_fingerprints(
                {path: None for path in remote_wins_paths},
                {path: self._causal_journal_versions.get(path) for path in remote_wins_paths},
            )
            self.sync_state.clear_pending(remote_wins_paths)
            if deletes_committed + len(delete_requests) > max_deletes:
                return False, 0, True

            planned_items = [
                rsync_ops.TransferItem("upload", path, self._local_file_size(path))
                for path in sorted(upload_paths)
            ]
            planned_items.extend(rename_items)
            planned_items.extend(
                rsync_ops.TransferItem("delete_remote", path)
                for path, _digest, _mtime in sorted(delete_requests)
            )
            if planned_items:
                self.queue_items_known.emit(planned_items)
            for item in rename_items:
                self.transfer_item_started.emit(item.direction, item.path, item.size)
                self._on_item_complete(item)

            if upload_paths:
                self.transfer_phase.emit("upload", "transferring", remote_progress_done, remote_progress_total)
                lock.set_activity("transferring", remote_progress_done, remote_progress_total)
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
                [*rename_items, *push_result.items, *delete_result.items],
                push_result.raw_error or delete_result.raw_error,
            )
            self._report_failure(chunk_result)
            if not chunk_result.ok:
                return False, len(delete_result.completed_paths), False

            # A retry after a crash between rsync success and journal append sees
            # its original upload as ADOPT. Journal that confirmed remote state so
            # the manifest recovers instead of silently retaining a blind spot.
            # Conflict copies are also real NAS writes and must be visible there.
            journal_items = [item for item in chunk_result.items if item.direction == "upload"]
            # Only an adopted local file can be an upload that completed before
            # its journal write. An absent local path adopted from a NAS tombstone
            # must update the local baseline, not be falsely journaled as upload.
            journal_items.extend(
                rsync_ops.TransferItem("upload", path)
                for path in sorted(adopted_paths)
                if self.sync_state.local_fingerprint(self.cfg.local_root(), path) is not None
            )
            journal_items.extend(self._conflict_journal_items)
            remote_only_paths = {item.path for item in self._conflict_journal_items}
            self.transfer_phase.emit("upload", "confirming", remote_progress_done, remote_progress_total)
            lock.set_activity("confirming", remote_progress_done, remote_progress_total)
            authoritative = self._authoritative_fingerprints(
                journal_items, adopted_paths, delete_result.completed_paths, watcher,
                remote_only_paths=remote_only_paths,
            )
            self.transfer_phase.emit("upload", "committing", remote_progress_done, remote_progress_total)
            lock.set_activity("committing", remote_progress_done, remote_progress_total)
            journal_ok, journal_detail = rsync_ops.append_remote_journal(
                self.cfg, self._conn, self.sync_state.device_id(), journal_items,
                authoritative, self._causal_journal_versions,
            )
            if journal_ok:
                self.cfg.set("journal_error", "")
                self.sync_state.record_fingerprints({
                    path: fp for path, fp in authoritative.items()
                    if path not in remote_only_paths
                }, self._causal_journal_versions)
                clearable = set(chunk_paths) - self._completed_rename_paths
                for path, expected in authoritative.items():
                    current = self.sync_state.local_fingerprint(self.cfg.local_root(), path)
                    if current != expected:
                        # A new local edit landed during this chunk. Keep its
                        # durable queue entry even if the watcher misses it.
                        clearable.discard(path)
                self.sync_state.clear_pending(clearable)
                return True, len(delete_result.completed_paths), False

            payload, payload_error = rsync_ops.build_remote_journal_payload(
                self.cfg, self.sync_state.device_id(), journal_items, authoritative,
                self._causal_journal_versions,
            )
            if payload:
                rsync_ops.save_pending_journal(payload)
            detail = journal_detail or payload_error or "journal NAS non aggiornato"
            self.cfg.set("journal_error", detail)
            self._log("JOURNAL_ERROR", "-", detail)
            # Stop immediately. A single pending payload cannot safely hold two
            # independent failed chunks, so continuing would lose retry data.
            return False, len(delete_result.completed_paths), False

    def _try_run_staged_chunk(
        self, chunk_set: set[str], remote_progress_done: int, remote_progress_total: int,
        chunk_index: int, watcher,
    ) -> tuple[bool, int, bool] | None:
        """Stage a pure batch of brand-new files outside the NAS lease.

        Existing paths, deletes and conflicts retain the conservative direct
        path below. A staging publish never overwrites: if another client wins
        the race, the private staged files remain untouched and this batch is
        retried from a fresh plan.
        """
        self.transfer_waiting_for_lock.emit("upload")
        with rsync_ops.remote_lock(
            self.cfg, self._conn, on_start=self._set_current_process,
            owner_id=self.sync_state.device_id(), priority=0,
        ) as lock:
            self.lock_coordinator.acquired()
            self._conflict_journal_items = []
            self._causal_journal_versions = {}
            self._staging_upload_paths = set()
            self.transfer_phase.emit("upload", "checking", remote_progress_done, remote_progress_total)
            lock.set_activity("checking", remote_progress_done, remote_progress_total)
            upload_paths, delete_requests, adopted_paths, remote_wins_paths, rename_plans = self._build_plan(
                chunk_set,
                remote_progress_offset=remote_progress_done,
                remote_progress_total=remote_progress_total,
                compact_remote_manifest=chunk_index == 0,
            )
            if (
                not upload_paths or upload_paths != self._staging_upload_paths
                or delete_requests or adopted_paths or remote_wins_paths or rename_plans
            ):
                return None
            fingerprints = {
                path: self.sync_state.local_fingerprint(self.cfg.local_root(), path)
                for path in upload_paths
            }
            if any(fingerprint is None for fingerprint in fingerprints.values()):
                return None

        # The ID is content-derived so a retry after a client restart can reuse
        # the same private staging tree and resume its partial files. A changed
        # source fingerprint produces a different tree and can never publish old
        # bytes as the new version.
        transaction_id = self._staging_transaction_id(upload_paths, fingerprints)
        staging_dir = rsync_ops.create_remote_staging(self.cfg, self._conn, transaction_id)
        if staging_dir is None:
            return False, 0, False
        self.transfer_phase.emit("upload", "transferring", remote_progress_done, remote_progress_total)
        stage_result = self._run_transfer_tracked(
            rsync_ops.push_to_staging, rsync_ops.new_run_ts(),
            emit_lifecycle=False, paths=upload_paths, staging_dir=staging_dir,
        )
        self._report_failure(stage_result)
        if not stage_result.ok:
            return False, 0, False

        # The source may have changed while rsync was uploading the private
        # staging tree. Never publish bytes that no longer match the local
        # version that produced this batch; leave the fresh edit for the next
        # watcher-driven attempt.
        changed_during_stage = set()
        for path, expected in fingerprints.items():
            current = self.sync_state.local_fingerprint(self.cfg.local_root(), path)
            if current != expected:
                changed_during_stage.add(path)
        if changed_during_stage:
            for path in changed_during_stage:
                if watcher is not None:
                    watcher.mark_dirty(path)
            rsync_ops.cleanup_remote_staging(self.cfg, self._conn, staging_dir)
            self._log(
                "STAGING_DEFERRED", ", ".join(sorted(changed_during_stage)[:5]),
                "file modificato durante il caricamento staging",
            )
            return False, 0, False

        self.transfer_waiting_for_lock.emit("upload")
        with rsync_ops.remote_lock(
            self.cfg, self._conn, on_start=self._set_current_process,
            owner_id=self.sync_state.device_id(), priority=0,
        ) as lock:
            self.lock_coordinator.acquired()
            self.transfer_phase.emit("upload", "committing", remote_progress_done, remote_progress_total)
            lock.set_activity("committing", remote_progress_done, remote_progress_total)
            staged_fingerprints = {path: fp for path, fp in fingerprints.items() if fp is not None}
            published, detail = rsync_ops.publish_staging(
                self.cfg, self._conn, staging_dir, transaction_id,
                self.sync_state.device_id(), staged_fingerprints,
                {path: self._causal_journal_versions.get(path) for path in staged_fingerprints},
            )
            if not published:
                self._log("STAGING_DEFERRED", "-", detail)
                return False, 0, False
            self.sync_state.record_fingerprints(
                staged_fingerprints,
                {path: self._causal_journal_versions.get(path) for path in staged_fingerprints},
            )
            self.sync_state.clear_pending(set(staged_fingerprints))
        rsync_ops.cleanup_remote_staging(self.cfg, self._conn, staging_dir)
        return True, 0, False

    def _staging_transaction_id(
        self, paths: set[str], fingerprints: dict[str, Fingerprint | None],
    ) -> str:
        digest = hashlib.sha256()
        for path in sorted(paths):
            fingerprint = fingerprints.get(path)
            if fingerprint is None:
                continue
            digest.update(os.fsencode(path))
            digest.update(b"\0")
            digest.update(fingerprint.digest.encode("ascii"))
            digest.update(b"\0")
            digest.update(str(fingerprint.size).encode("ascii"))
            digest.update(b"\0")
            digest.update(str(fingerprint.mtime_ns).encode("ascii"))
            digest.update(b"\0")
        return f"publish-{digest.hexdigest()}"

    def _authoritative_fingerprints(
        self, uploaded_items: list[rsync_ops.TransferItem], adopted_paths: set[str],
        deleted_paths: set[str], watcher, *, remote_only_paths: set[str] | None = None,
    ) -> dict[str, Fingerprint | None]:
        remote_only_paths = remote_only_paths or set()
        uploaded_paths = {item.path for item in uploaded_items}
        remote = rsync_ops.remote_file_states(
            self.cfg, self._conn, uploaded_paths, compact=False,
        )
        if remote is None or set(remote) != uploaded_paths:
            raise rsync_ops.RemoteLockError("impossibile confermare i file appena caricati sul NAS")

        authoritative: dict[str, Fingerprint | None] = {path: None for path in deleted_paths}
        for path in adopted_paths:
            authoritative[path] = self.sync_state.local_fingerprint(self.cfg.local_root(), path)
        for path in uploaded_paths:
            state = remote[path]
            if state.kind != RemoteKind.FILE:
                raise rsync_ops.RemoteLockError(f"il file appena caricato non risulta presente sul NAS: {path}")
            remote_fp = Fingerprint(state.digest, state.size, state.mtime_ns, state.causal)
            if path in remote_only_paths:
                authoritative[path] = remote_fp
                continue
            local_fp = self.sync_state.local_fingerprint(self.cfg.local_root(), path)
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
        self._directory_events = set()
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
                # A missing path with baseline descendants is a moved/deleted
                # directory event. Queue its files, not the directory itself.
                baseline = self.sync_state.all_entries()
                if any(candidate.startswith(path.rstrip("/") + "/") for candidate in baseline):
                    self._directory_events.add(path)
                    resolved.update(
                        candidate for candidate in baseline
                        if candidate.startswith(path.rstrip("/") + "/")
                    )
                else:
                    resolved.add(path)  # a real file deletion candidate
                continue
            except OSError as exc:
                raise rsync_ops.RemoteLockError(f"impossibile leggere {path}: {exc}") from exc
            if stat.S_ISREG(mode):
                resolved.add(path)
            elif stat.S_ISDIR(mode):
                self._directory_events.add(path)
                try:
                    for child in local_path.rglob("*"):
                        if child.is_file() and not child.is_symlink():
                            child_path = str(child.relative_to(Path(self.cfg.local_root())))
                            if not rsync_ops.path_is_excluded(self.cfg, child_path):
                                resolved.add(child_path)
                except OSError as exc:
                    raise rsync_ops.RemoteLockError(f"impossibile leggere {path}: {exc}") from exc
            else:
                self._log("UNSUPPORTED", path, "symlink o file speciale ignorato")
        return resolved

    def _local_file_size(self, relative_path: str) -> int:
        try:
            local_path = Path(self.cfg.local_root(), relative_path)
            info = local_path.lstat()
        except OSError:
            return 0
        return info.st_size if stat.S_ISREG(info.st_mode) else 0

    def _is_staging_candidate(self, relative_path: str) -> bool:
        local = self.sync_state.local_fingerprint(self.cfg.local_root(), relative_path)
        baseline = self.sync_state.get(relative_path)
        if local is None or (baseline is not None and not baseline.is_tombstone):
            return False
        # Keep a possible rename source in the same reconciliation chunk. A
        # staging-only destination would otherwise be separated from the
        # missing baseline path and could only be uploaded, never moved.
        return not any(
            candidate != relative_path and not fingerprint.is_tombstone
            and fingerprint.digest == local.digest and fingerprint.size == local.size
            for candidate, fingerprint in self.sync_state.all_entries().items()
        )

    def _build_plan(
        self, relative_paths: set[str],
        remote_progress_offset: int = 0, remote_progress_total: int = 0,
        compact_remote_manifest: bool = True,
        allow_renames: bool = True,
    ) -> tuple[set[str], list[tuple[str, str, int]], set[str], set[str], list[_RenamePlan]]:
        if not relative_paths:
            return set(), [], set(), set(), []
        remote_progress = None
        if remote_progress_total:
            self._on_hash_progress(remote_progress_offset, remote_progress_total)
            remote_progress = lambda done, total: self._on_hash_progress(
                remote_progress_offset + done, remote_progress_total,
            )
        remote_query_paths = set(relative_paths)
        if allow_renames:
            remote_query_paths.update(self._directory_events)
        remote_states = rsync_ops.remote_file_states(
            self.cfg, self._conn, remote_query_paths,
            compact=compact_remote_manifest, on_progress=remote_progress,
        )
        if remote_states is None or set(remote_states) != remote_query_paths:
            raise rsync_ops.RemoteLockError("impossibile verificare in batch lo stato dei file sul NAS")

        rename_plans = self._find_rename_plans(relative_paths, remote_states) if allow_renames else []
        rename_paths = {path for plan in rename_plans for path in plan.paths}

        uploads: set[str] = set()
        deletes: list[tuple[str, str, int]] = []
        adopted: set[str] = set()
        remote_wins: set[str] = set()
        for relative_path in sorted(relative_paths - rename_paths):
            local_fp = self.sync_state.local_fingerprint(self.cfg.local_root(), relative_path)
            baseline = self.sync_state.get(relative_path)
            remote = remote_states[relative_path]
            decision = plan_path(
                baseline, local_fp, remote,
                delete_enabled=self.cfg.allows_remote_deletions(),
                local_causal=local_fp.causal if local_fp is not None else self.sync_state.local_causal(
                    self.cfg.local_root(), relative_path,
                ),
            )
            if decision.action == Action.UPLOAD:
                uploads.add(relative_path)
                self._causal_journal_versions[relative_path] = local_fp.causal if local_fp else None
                if remote.kind == RemoteKind.ABSENT and (baseline is None or baseline.is_tombstone):
                    self._staging_upload_paths.add(relative_path)
            elif decision.action == Action.DELETE_REMOTE:
                # Precondition deletion on the exact live state just observed,
                # not its older common baseline. This permits mtime-only NAS
                # changes while rejecting a concurrent content replacement.
                deletes.append((relative_path, remote.digest, remote.mtime_ns // 1_000_000_000))
                self._causal_journal_versions[relative_path] = self.sync_state.local_causal(
                    self.cfg.local_root(), relative_path,
                )
            elif decision.action == Action.ADOPT:
                adopted.add(relative_path)
            elif decision.action == Action.CONFLICT_LOCAL_WINS:
                conflict_path = self._conflict_path(relative_path)
                if not rsync_ops.copy_remote_file(self.cfg, self._conn, relative_path, conflict_path):
                    raise rsync_ops.RemoteLockError("impossibile conservare la versione NAS in conflitto")
                self._conflict_journal_items.append(rsync_ops.TransferItem("upload", conflict_path))
                self._causal_journal_versions[conflict_path] = remote.causal
                self._causal_journal_versions[relative_path] = local_fp.causal if local_fp else None
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
                    self._conflict_journal_items.append(rsync_ops.TransferItem("upload", conflict_path))
                    self._causal_journal_versions[conflict_path] = local_fp.causal
                    self._log("CONFLICT", relative_path, f"versione locale salvata sul NAS come {conflict_path}")
            elif decision.action == Action.REMOTE_WINS:
                if local_fp is None and baseline is not None and not baseline.is_tombstone:
                    self._log("STALE_DELETE", relative_path, decision.detail)
                    remote_wins.add(relative_path)
                    self._causal_journal_versions[relative_path] = remote.causal
            elif decision.action == Action.BLOCK:
                raise rsync_ops.RemoteLockError(f"{relative_path}: {decision.detail}")
        return uploads, deletes, adopted, remote_wins, rename_plans

    def _find_rename_plans(
        self, relative_paths: set[str], remote_states: dict[str, rsync_ops.RemoteState],
    ) -> list[_RenamePlan]:
        """Find only unambiguous local moves whose remote source is unchanged."""
        baseline = self.sync_state.all_entries()
        root = Path(self.cfg.local_root())
        directory_plans: list[_RenamePlan] = []
        directory_sources = {
            path for path in self._directory_events
            if not (root / path).exists()
            and any(candidate.startswith(path.rstrip("/") + "/") for candidate in baseline)
        }
        directory_destinations = {
            path for path in self._directory_events if (root / path).is_dir()
        }

        def files_under(path: str) -> set[str]:
            directory = root / path
            try:
                return {
                    str(child.relative_to(root))
                    for child in directory.rglob("*")
                    if child.is_file()
                    and not rsync_ops.path_is_excluded(self.cfg, str(child.relative_to(root)))
                }
            except OSError:
                return set()

        directory_matches: list[tuple[str, str, _RenamePlan]] = []
        for source in sorted(directory_sources):
            source_files = {
                path for path in baseline
                if path.startswith(source.rstrip("/") + "/")
                and not rsync_ops.path_is_excluded(self.cfg, path)
                and not baseline[path].is_tombstone
            }
            if not source_files:
                continue  # empty directories are not in the file manifest
            source_root_state = remote_states.get(source)
            for destination in sorted(directory_destinations):
                if destination == source or destination.startswith(source.rstrip("/") + "/"):
                    continue
                if any(
                    path.startswith(destination.rstrip("/") + "/")
                    and not rsync_ops.path_is_excluded(self.cfg, path)
                    for path in baseline
                ):
                    continue
                mapped = {
                    f"{destination}/{path[len(source) + 1:]}" for path in source_files
                }
                if files_under(destination) != mapped or source_root_state is None:
                    continue
                if source_root_state.kind != RemoteKind.OTHER or remote_states.get(destination, rsync_ops.RemoteState(RemoteKind.ABSENT)).kind not in (
                    RemoteKind.ABSENT, RemoteKind.TOMBSTONE,
                ):
                    continue
                entries: list[tuple[str, str, Fingerprint, Fingerprint]] = []
                causals: list[tuple[str, CausalVersion | None]] = []
                valid = True
                for source_file in sorted(source_files):
                    destination_file = f"{destination}/{source_file[len(source) + 1:]}"
                    old = baseline[source_file]
                    new = self.sync_state.local_fingerprint(str(root), destination_file)
                    remote = remote_states.get(source_file)
                    if new is None or remote is None or remote.kind != RemoteKind.FILE \
                            or remote.digest != old.digest or remote.size != old.size \
                            or new.digest != old.digest or new.size != old.size:
                        valid = False
                        break
                    entries.append((source_file, destination_file, old, new))
                    causals.append((source_file, self.sync_state.local_causal(str(root), source_file)))
                if valid:
                    directory_matches.append((source, destination, _RenamePlan(
                        source, destination, "DIR", tuple(entries), tuple(causals),
                    )))

        # More than one matching pair means identical content or competing
        # directory events. Refuse the optimization and let normal reconciliation
        # preserve both sides.
        if len(directory_matches) == 1:
            directory_plans.append(directory_matches[0][2])
        directory_paths = {path for plan in directory_plans for path in plan.paths}

        old_files: dict[tuple[str, int, int], list[tuple[str, Fingerprint]]] = {}
        new_files: dict[tuple[str, int, int], list[tuple[str, Fingerprint]]] = {}
        for path in relative_paths - directory_paths:
            old = baseline.get(path)
            local = self.sync_state.local_fingerprint(str(root), path)
            if old is not None and not old.is_tombstone and local is None:
                old_files.setdefault((old.digest, old.size, old.mtime_ns), []).append((path, old))
            elif old is None and local is not None:
                new_files.setdefault((local.digest, local.size, local.mtime_ns), []).append((path, local))

        file_plans: list[_RenamePlan] = []
        for key in sorted(set(old_files) & set(new_files)):
            old_matches = old_files[key]
            new_matches = new_files[key]
            if len(old_matches) != 1 or len(new_matches) != 1:
                continue
            source, old = old_matches[0]
            destination, new = new_matches[0]
            source_remote = remote_states.get(source)
            destination_remote = remote_states.get(destination)
            if (
                source_remote is None or source_remote.kind != RemoteKind.FILE
                or source_remote.digest != old.digest or source_remote.size != old.size
                or destination_remote is None
                or destination_remote.kind not in (RemoteKind.ABSENT, RemoteKind.TOMBSTONE)
            ):
                continue
            file_plans.append(_RenamePlan(
                source, destination, "FILE", ((source, destination, old, new),),
                ((source, self.sync_state.local_causal(str(root), source)),),
                source_remote.mtime_ns,
            ))
        return [*directory_plans, *file_plans]

    def _run_renames(
        self, plans: list[_RenamePlan], watcher, lease: rsync_ops.RemoteLock | None = None,
    ) -> tuple[list[rsync_ops.TransferItem], set[str]]:
        items: list[rsync_ops.TransferItem] = []
        failed: set[str] = set()
        root = self.cfg.local_root()
        for plan in plans:
            current_entries = [
                (
                    self.sync_state.local_fingerprint(root, destination),
                    self.sync_state.fingerprint(Path(root, source)),
                    expected,
                )
                for source, destination, _old, expected in plan.entries
            ]
            if any(
                source is not None or destination != expected
                for destination, source, expected in current_entries
            ):
                failed.update(plan.paths)
                if watcher is not None:
                    for path in plan.paths:
                        watcher.mark_dirty(path)
                continue
            first_old = plan.entries[0][2]
            ok, detail = rsync_ops.rename_remote(
                self.cfg, self._conn, plan.source_path, plan.destination_path,
                self.sync_state.device_id(), kind=plan.kind,
                digest=first_old.digest if plan.kind == "FILE" else "",
                size=first_old.size if plan.kind == "FILE" else 0,
                mtime_ns=plan.expected_mtime_ns,
                lease=lease,
            )
            if not ok:
                failed.update(plan.paths)
                self._log("RENAME_DEFERRED", plan.source_path, detail)
                if watcher is not None:
                    for path in plan.paths:
                        watcher.mark_dirty(path)
                continue
            baseline_updates: dict[str, Fingerprint | None] = {}
            causals: dict[str, CausalVersion | None] = dict(plan.source_causals)
            for source, destination, _old, expected in plan.entries:
                baseline_updates[source] = None
                baseline_updates[destination] = expected
            self.sync_state.record_fingerprints(baseline_updates, causals)
            for source, destination, _old, expected in plan.entries:
                source_current = self.sync_state.fingerprint(Path(root, source))
                destination_current = self.sync_state.local_fingerprint(root, destination)
                if source_current is None and destination_current == expected:
                    self.sync_state.clear_pending({source, destination})
                else:
                    if watcher is not None:
                        watcher.mark_dirty(source if source_current is not None else destination)
            item = rsync_ops.TransferItem(
                "rename_remote", plan.destination_path, plan.size,
                source_path=plan.source_path,
            )
            items.append(item)
            self._completed_rename_paths.update(plan.paths)
        return items, failed

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
