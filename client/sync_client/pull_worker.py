"""Runs NAS -> local pulls on their own thread, decoupled from PushWorker (see
push_worker.py's docstring for why the split exists at all).

Also self-cancels an in-flight pull the moment the NASBox folder becomes locally
dirty for a reason *other than the pull's own writes*. A pull already checks the
folder is clean before starting (skips this round otherwise), but a pull that's
already running captured its view of "what's on the NAS" when it started -- a
local change landing mid-pull isn't accounted for. Left to run to completion
regardless, a --delete pull could delete a file the user just added (the original
bug this whole area was rewritten for), or a pull with no delete could still
re-download a file the user just removed, undoing the deletion. Aborting and
retrying on the very next tick, once it's quiet and freshly pushed, closes that
window instead of merely narrowing it to "before the pull starts" -- what the
previous, single-threaded fix could only do since push and pull shared one thread
and couldn't interrupt each other mid-flight.

The "for a reason other than the pull's own writes" part matters a lot in
practice: a pull's own downloads/local-deletions land in the same tree the local
watcher is watching, so they mark it dirty exactly like a real user edit would.
Naively cancelling on any dirty mark made every pull with real work to do
self-cancel on its own first downloaded file, every single time -- pulls could
never actually complete (found and fixed by testing this against a real NAS, not
just synthetic tests: a synthetic test using fake transfer functions that never
touch the local filesystem doesn't exercise this feedback loop at all). The fix
compares *which paths* are dirty (FolderWatcher.dirty_paths()) against the set of
paths this pull itself just wrote (TransferWorker._self_written_paths) -- only a
dirty path this pull did *not* just write counts as a genuine external change.
"""
from __future__ import annotations

import threading
import time
from pathlib import Path

from . import rsync_ops
from .config import Config
from .logger import EventLogger
from .repository_safety import RepositorySafetyError
from .reconcile import RemoteKind
from .scan_worker import ScanWorker
from .transfer_worker import TransferWorker
from .sync_state import Fingerprint, SyncStateStore
from .watcher import WatcherHandle

TICK_SECONDS = 1


class PullWorker(TransferWorker):
    def __init__(
        self, cfg: Config, logger: EventLogger, watchers: WatcherHandle,
        transfer_lock: threading.Lock, sync_state: SyncStateStore,
        scan_worker: ScanWorker | None = None,
        transfer_active: threading.Event | None = None,
    ) -> None:
        super().__init__(cfg, logger, transfer_active=transfer_active)
        self.watchers = watchers
        self.transfer_lock = transfer_lock
        self.sync_state = sync_state
        self._scan_worker = scan_worker
        self._wake = threading.Event()
        self._last_pull = 0.0
        self._last_manifest_revision = -1
        self._self_cancelled = False  # set by the cancel_check right before it terminates the process
        self._cancel_dirty_paths: set[str] = set()

    def _wake_now(self) -> None:
        self._wake.set()

    def wake(self) -> None:
        """Ask the worker to re-evaluate immediately instead of waiting for the next tick."""
        self._wake.set()

    def reset_schedule(self) -> None:
        """Forget when the folder was last pulled, so it's checked on the very next
        tick instead of waiting out poll_interval -- used by "Sincronizza tutto ora"."""
        self._last_pull = 0.0
        self._wake.set()

    def run(self) -> None:
        while not self._stop_flag.is_set():
            try:
                self._tick()
            except Exception as exc:  # keep the loop alive no matter what
                self._log("ERROR", "-", detail=f"pull tick failed: {exc}")
            self._wake.wait(TICK_SECONDS)
            self._wake.clear()

    def _tick(self) -> None:
        if self._conn is None or self.cfg.is_paused() or not self.cfg.is_configured():
            return

        poll_interval = float(self.cfg.get("poll_interval") or 60)
        now = time.time()
        if now - self._last_pull < poll_interval:
            return

        watcher = self.watchers.get()
        if watcher and watcher.is_dirty():
            # Not pushed yet -- pulling now risks deleting/overwriting it before
            # PushWorker gets to it. Leave the timer untouched so this is retried
            # on the very next tick instead of waiting out a full poll_interval.
            return

        # Blocks only if a push is currently running -- there's only ever one
        # NASBox folder, so this is the only pair that can conflict.
        self.transfer_preparing.emit("download")
        with self.transfer_lock:
            try:
                self._cancel_dirty_paths = set()
                self.transfer_waiting_for_lock.emit("download")
                with rsync_ops.remote_lock(self.cfg, self._conn, on_start=self._set_current_process):
                    if self._stop_flag.is_set():
                        self.transfer_finished.emit("download", False)
                        self._last_pull = now
                        return
                    pending_ok, pending_detail = rsync_ops.retry_pending_journal(self.cfg, self._conn)
                    if not pending_ok:
                        self.cfg.set("journal_error", pending_detail or "journal non raggiungibile")
                        self._log("JOURNAL_BLOCK", "-", pending_detail or "journal non raggiungibile")
                        self.transfer_finished.emit("download", False)
                        self._last_pull = now
                        return
                    self.cfg.set("journal_error", "")
                    try:
                        rsync_ops.validate_transfer_safety(
                            self.cfg, self._conn,
                            destructive=bool(self.cfg.get("delete_enabled")),
                            direction="download",
                        )
                    except RepositorySafetyError as exc:
                        self._log("SAFETY_BLOCK", "-", str(exc))
                        self.transfer_finished.emit("download", False)
                        self._last_pull = now
                        return
                    if not rsync_ops.ensure_remote_dir(self.cfg, self._conn):
                        self._log("ERROR", "-", "impossibile creare la cartella remota")
                        self.transfer_finished.emit("download", False)
                        self._last_pull = now  # don't hammer a broken connection every tick
                        return
                    run_ts = rsync_ops.new_run_ts()
                    self._self_cancelled = False
                    snapshot = rsync_ops.remote_manifest_snapshot(
                        self.cfg, self._conn, self._last_manifest_revision,
                    )
                    if snapshot is None:
                        raise rsync_ops.RemoteLockError("impossibile leggere la revisione del manifest NAS")
                    manifest_revision, manifest_entries = snapshot
                    checksum_paths: set[str] = set()
                    baselines = {}
                    if manifest_entries is not None:
                        baselines = self.sync_state.get_many(set(manifest_entries))
                        for path, state in manifest_entries.items():
                            if state.kind != RemoteKind.FILE or rsync_ops.path_is_excluded(self.cfg, path):
                                continue
                            baseline = baselines.get(path)
                            if baseline is None or baseline.is_tombstone or baseline.digest != state.digest:
                                checksum_paths.add(path)

                    def _cancel_check(w=watcher) -> bool:
                        if w is None or not w.is_dirty():
                            return False
                        external = {
                            path for path in w.dirty_paths()
                            if not any(
                                path == own_path
                                or path.startswith(own_path.rstrip("/") + "/")
                                for own_path in self._self_written_paths
                            )
                        }
                        # Rsync creates destination directories before the
                        # files they contain. Directory-only inotify events
                        # are therefore expected side effects of this pull,
                        # not user edits that should cancel it.
                        external = {
                            path for path in external
                            if path == "" or not Path(self.cfg.local_root(), path).is_dir()
                        }
                        if not external:
                            return False  # every dirty path so far is one this pull itself just wrote
                        self._self_cancelled = True
                        self._cancel_dirty_paths = external
                        return True

                    result = self._run_transfer_tracked(
                        rsync_ops.pull, run_ts, cancel_check=_cancel_check,
                        emit_lifecycle=False,
                    )
                    if result.ok and not self._self_cancelled and checksum_paths:
                        safe_checksum_paths: set[str] = set()
                        assert manifest_entries is not None
                        for path in checksum_paths:
                            local_fp = self.sync_state.fingerprint(Path(self.cfg.local_root(), path))
                            baseline = baselines.get(path)
                            remote_digest = manifest_entries[path].digest
                            if (
                                local_fp is not None and baseline is not None
                                and not baseline.is_tombstone
                                and local_fp.digest not in (baseline.digest, remote_digest)
                            ):
                                # A real local edit landed during the normal
                                # pull. Leave it for PushWorker's conflict plan.
                                if watcher is not None:
                                    watcher.mark_dirty(path)
                                self._self_cancelled = True
                            else:
                                safe_checksum_paths.add(path)
                        checksum_paths = safe_checksum_paths
                    if result.ok and not self._self_cancelled and checksum_paths:
                        checksum_result = self._run_transfer_tracked(
                            rsync_ops.pull, run_ts, cancel_check=_cancel_check,
                            reset_written_paths=False, emit_lifecycle=False, paths=checksum_paths,
                        )
                        result = rsync_ops.TransferResult(
                            checksum_result.ok,
                            [*result.items, *checksum_result.items],
                            checksum_result.raw_error,
                        )
                    if self._scan_worker:
                        self._scan_worker.wake()
                    if result.ok:
                        # Pulls only adopt the NAS authority; they must not create
                        # new NAS journal revisions for data already committed by
                        # another client.
                        completed_paths = {item.path for item in result.items}
                        remote = rsync_ops.remote_file_states(self.cfg, self._conn, completed_paths)
                        if remote is None or set(remote) != completed_paths:
                            raise rsync_ops.RemoteLockError(
                                "impossibile confermare lo stato NAS dopo lo scaricamento"
                            )
                        authoritative: dict[str, Fingerprint | None] = {}
                        for path in completed_paths:
                            state = remote[path]
                            if state.kind == RemoteKind.FILE:
                                remote_fp = Fingerprint(state.digest, state.size, state.mtime_ns)
                                local_fp = self.sync_state.fingerprint(Path(self.cfg.local_root(), path))
                                authoritative[path] = (
                                    local_fp if local_fp is not None and local_fp.digest == remote_fp.digest
                                    else remote_fp
                                )
                            else:
                                authoritative[path] = None
                        self.sync_state.record_fingerprints(authoritative)
                        self._last_manifest_revision = manifest_revision
            except rsync_ops.RemoteLockError as exc:
                detail = f"lock di sincronizzazione sul NAS non acquisito: {exc}"
                self.transfer_lock_unavailable.emit("download", detail)
                self.transfer_finished.emit("download", False)
                self._log("ERROR", "-", detail)
                self._last_pull = now  # throttle retries -- see PushWorker's equivalent except Exception below
                return
            except Exception:
                # Unlike every early-return above, an exception that isn't a RemoteLockError
                # would otherwise leave _last_pull untouched -- with TICK_SECONDS=1 and no
                # other throttle on this path, that's a tight ~1s retry loop on any
                # persistent failure. Mirrors PushWorker's own except Exception (which
                # throttles via its debounce path instead, since it has no poll timer).
                self._last_pull = now
                self.transfer_finished.emit("download", False)
                raise

        if self._self_cancelled:
            self.transfer_finished.emit("download", False)
            detail = "scaricamento interrotto: rilevata una modifica locale non ancora inviata, riprovo a breve"
            if self._cancel_dirty_paths:
                paths = ", ".join(sorted(self._cancel_dirty_paths)[:5])
                detail = f"{detail} ({paths})"
            self._log(
                "PULL_DEFERRED", "-",
                detail,
            )
            # don't advance the timer -- retry next tick
        else:
            self._report_failure(result)
            self._last_pull = time.time()
            self.transfer_finished.emit("download", result.ok)

    def _report_failure(self, result: "rsync_ops.TransferResult") -> None:
        if result.ok:
            return
        if self._stop_flag.is_set() or self.cfg.is_paused():
            self._log("CANCELLED", "-", "trasferimento interrotto (pausa o chiusura in corso)")
            return
        self._log("ERROR", "-", result.raw_error or "trasferimento fallito")

    def _set_current_process(self, proc) -> None:
        self._current_proc = proc
