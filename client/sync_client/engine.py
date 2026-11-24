"""Background sync coordinator: runs in its own Qt thread so the GUI never blocks.

Owns what needs a single, consistent view: connection resolution, the NASBox
folder watcher's lifecycle (created when local_root is first set up, replaced if
it's relocated), pause state, NAS-side daemon health, and local trash pruning.

Push and pull execution themselves live on their own threads (see push_worker.py
and pull_worker.py) -- this class no longer runs any rsync itself. They used to
run serially in this same tick loop: a pull could take tens of seconds on a large
tree (real-world observed: ~50s on a ~108k-file NASBox folder), and since that
blocked this single thread, a push that became ready *during* that pull had to
wait for it to finish first, for no reason related to that push. wake()/cancel_
current_transfer() below just forward to both workers, so existing callers (the
GUI) don't need to know the split happened.

The NAS itself stays a passive rsync/SSH target plus its own always-running
retention daemon (see server/, self-timed, no cron) -- it doesn't need an
active component to "hold" the current state, since every push writes
straight into its filesystem over SSH.
"""
from __future__ import annotations

import os
import threading
import time
from typing import Optional

from PyQt6.QtCore import QThread, pyqtSignal

from . import delete_safety, integrity_audit, perf, rsync_ops, trash
from .config import Config
from .logger import EventLogger
from .pull_worker import PullWorker
from .push_worker import PushWorker
from .repository_safety import RepositorySafetyError
from .sync_state import SyncStateStore
from .transfer_scheduler import TransferScheduler
from .watcher import FolderWatcher, WatcherHandle

TICK_SECONDS = 1
HOST_RECHECK_SECONDS = 30
PRUNE_INTERVAL_SECONDS = 24 * 3600
HEALTH_CHECK_INTERVAL_SECONDS = 60  # is the NAS-side server daemon actually running?
HEALTH_CHECK_OK_INTERVAL_SECONDS = 300  # stable NAS -> much less control-plane chatter
SERVER_UPDATE_CHECK_INTERVAL_SECONDS = 300  # versioned script discovery is not urgent
VERSION_WARNING_REPEAT_SECONDS = 6 * 3600  # don't re-nag about an outdated server package more than this often
PENDING_STATUS_INTERVAL_SECONDS = 5


class SyncEngine(QThread):
    log_event = pyqtSignal(str, str, str)         # action, path, detail
    status_changed = pyqtSignal(dict)
    connection_changed = pyqtSignal(object)       # rsync_ops.NasConnection | None -- lets the other
                                                   # workers reuse this instead of independently resolving their own
    server_outdated = pyqtSignal(str)             # friendly message, emitted whenever detected
    server_update_available = pyqtSignal(str, str, str)  # message, path, version
    server_restarted = pyqtSignal(str)            # friendly message -- NAS daemon was found down and (attempted to be) restarted
    server_health_changed = pyqtSignal(str)       # unknown | ok | down | unresponsive | outdated

    def __init__(
        self, cfg: Config, logger: EventLogger, watchers: WatcherHandle,
        push_worker: Optional[PushWorker] = None, pull_worker: Optional[PullWorker] = None,
        sync_state: Optional[SyncStateStore] = None,
        transfer_scheduler: Optional[TransferScheduler] = None,
    ) -> None:
        super().__init__()
        self.cfg = cfg
        self.logger = logger
        self.watchers = watchers
        self._push_worker = push_worker
        self._pull_worker = pull_worker
        self.sync_state = sync_state
        self.transfer_scheduler = transfer_scheduler
        self._stop_flag = threading.Event()
        self._wake = threading.Event()
        self._conn: rsync_ops.NasConnection | None = None
        self._watched_path: str | None = None  # which local_root the current FolderWatcher is bound to
        self._last_host_check = 0.0
        self._last_prune = 0.0
        self._last_health_check = 0.0
        self._last_update_check = 0.0
        self._health_ok_streak = 0
        self._last_version_warning = 0.0
        self._last_pending_status = 0.0
        self._pending_summary: dict[str, object] = {}
        self._integrity_running = threading.Event()
        self._audit_running = threading.Event()
        self._audit_progress: tuple[int, int] = (0, 0)
        self._devices: list[dict] = []
        self._last_devices_refresh = 0.0
        self._last_integrity_check = 0.0
        self._started_at = time.time()
        self._assurance_cache: tuple[float, int] | None = None
        self._identity_backfilled = False
        self._offered_server_update = ""
        self._server_health = "unknown"

    # --- external controls ---

    def stop(self) -> None:
        self._stop_flag.set()
        self._wake.set()
        self.wait(10_000)
        watcher = self.watchers.get()
        if watcher is not None:
            watcher.stop()

    def wake(self) -> None:
        """Ask everything (this loop, plus push/pull) to re-evaluate immediately
        instead of waiting for their next tick."""
        self._wake.set()
        if self._push_worker:
            self._push_worker.wake()
        if self._pull_worker:
            self._pull_worker.wake()

    def sync_now(self) -> None:
        """Force both directions to run as soon as possible.

        The explicit push request deliberately does not mark the whole tree as
        a watcher change: doing so used to make PushWorker calculate a checksum
        conflict check for every known local file before rsync could start.
        """
        if self._push_worker and self.cfg.allows_push():
            self._push_worker.request_full_sync()
        if self._pull_worker and self.cfg.allows_pull():
            self._pull_worker.reset_schedule()
        self.wake()

    def cancel_current_transfer(self) -> None:
        """Interrupt whatever push/pull is mid-flight right now, if any. Pausing
        only prevents *new* transfers from starting -- without this, a transfer
        already running would keep going until it finished on its own, so clicking
        Pausa wouldn't actually pause anything in progress."""
        if self._push_worker:
            self._push_worker.cancel_current_transfer()
        if self._pull_worker:
            self._pull_worker.cancel_current_transfer()

    def _maybe_integrity_check(self, now: float, force: bool = False) -> None:
        """Periodic, read-only checksum audit of both directions.

        Runs in a separate thread and only while nothing else is transferring:
        a full-tree checksum is intentionally expensive and must not steal the
        lease from a pending push or pull.
        """
        try:
            interval = int(self.cfg.get("integrity_check_interval_seconds") or 0)
        except (TypeError, ValueError):
            interval = 0
        if interval <= 0:
            return
        # On a fresh install (or right after an update) there is no previous
        # verification timestamp: scheduling one immediately launches two
        # whole-tree checksum rsyncs while the client is still warming up.
        # Pretend the last check happened `startup_delay` ago instead, so the
        # first automatic audit is delayed. "Verifica adesso" stays immediate.
        if not force:
            if self._last_integrity_check <= 0:
                try:
                    delay = max(60, int(self.cfg.get("integrity_startup_delay_seconds") or 1800))
                except (TypeError, ValueError):
                    delay = 1800
                self._last_integrity_check = now - interval + delay
                return
            if now - self._last_integrity_check < interval:
                return
            try:
                startup_delay = max(0, int(self.cfg.get("integrity_startup_delay_seconds") or 1800))
            except (TypeError, ValueError):
                startup_delay = 1800
            if now - self._started_at < startup_delay:
                return
        if self._conn is None or self._stop_flag.is_set() or self._integrity_running.is_set():
            return
        if self.transfer_scheduler is not None:
            snapshot = self.transfer_scheduler.snapshot()
            if snapshot.get("count", 0) or snapshot.get("active_kind"):
                return
        if self.sync_state is not None:
            try:
                if self.sync_state.pending_paths():
                    return
            except (OSError, RuntimeError):
                pass
        self._last_integrity_check = now
        self._integrity_running.set()

        def run() -> None:
            try:
                mismatches = rsync_ops.integrity_check(self.cfg, self._conn)
                try:
                    sample = int(self.cfg.get("integrity_check_sample_size") or 500)
                except (TypeError, ValueError):
                    sample = 500
                checked = (
                    self.sync_state.entry_count() if self.sync_state else 0
                ) if sample <= 0 else sample
            except Exception as exc:
                self.cfg.set("last_integrity_error", str(exc), persist=True)
                self.logger.log("ERROR", "-", f"controllo integrità non riuscito: {exc}")
                return
            finally:
                self._integrity_running.clear()
            self.cfg.set("last_integrity_check_ts", time.time(), persist=True)
            self.cfg.set("last_integrity_mismatches", len(mismatches), persist=True)
            self.cfg.set("last_integrity_files", checked, persist=True)
            self.cfg.set("last_integrity_error", "", persist=True)
            self.logger.log(
                "INFO", "-",
                f"verifica integrità completata: {checked} file confrontati, "
                f"{len(mismatches)} differenze",
            )
            for item in mismatches:
                self.log_event.emit("INTEGRITY_MISMATCH", item.path, item.direction)

        threading.Thread(target=run, name="nasbox-integrity", daemon=True).start()

    def verify_integrity_now(self) -> None:
        """Run a full checksum verification on demand, ignoring the interval."""
        self._maybe_integrity_check(time.time(), force=True)

    def refresh_devices_now(self) -> None:
        if self._conn is None:
            return

        def worker() -> None:
            try:
                devices = rsync_ops.remote_devices(self.cfg, self._conn) or []
            except Exception:
                return
            self._devices = devices
            self._last_devices_refresh = time.time()

        threading.Thread(target=worker, name="nasbox-devices", daemon=True).start()

    def _maybe_refresh_devices(self, now: float) -> None:
        if self._conn is None or now - self._last_devices_refresh < 300:
            return
        self.refresh_devices_now()

    def audit_snapshot(self) -> dict:
        history = integrity_audit.load_history()
        last = history[-1] if history else None
        # Old reports may contain paths that are excluded today (the repo
        # checkout is never mirrored). Count only actionable differences so a
        # stale report cannot keep the dashboard on "attention" forever.
        real_differences = [
            item for item in (last.differences if last else [])
            if not rsync_ops.path_is_excluded(self.cfg, str(item.get("path") or ""))
        ]
        status = last.status if last else "unknown"
        if last and last.status in ("differences", "problems"):
            actionable = [
                issue for issue in last.issues
                if issue.get("severity") in ("warning", "error")
            ]
            if not real_differences and not actionable:
                status = "ok"
        return {
            "last": {
                "status": status,
                "finished_at": last.finished_at,
                "duration_seconds": last.duration_seconds,
                "files_checked": last.files_checked,
                "differences": len(real_differences),
                "issues": list(last.issues),
                "checks": dict(last.checks),
                "error": last.error,
            } if last else None,
            "history_count": len(history),
            "running": self._audit_running.is_set(),
            "progress_done": self._audit_progress[0],
            "progress_total": self._audit_progress[1],
        }

    def _fresh_manifest(self) -> dict | None:
        if self._conn is None:
            return None
        try:
            with perf.timed(
                "audit_manifest_export",
                lambda detail: self.logger.log("PERF", "-", detail),
            ):
                snapshot = rsync_ops.remote_manifest_snapshot(self.cfg, self._conn, -1)
        except Exception:
            return None
        if snapshot is None:
            return None
        revision, entries = snapshot
        if revision >= 0:
            self.cfg.set("last_manifest_revision", revision, persist=False)
        return entries

    def _audit_is_idle(self) -> bool:
        if self._conn is None or self._stop_flag.is_set() or self._audit_running.is_set():
            return False
        if self.transfer_scheduler is not None:
            snapshot = self.transfer_scheduler.snapshot()
            if snapshot.get("count", 0) or snapshot.get("active_kind"):
                return False
        if self.sync_state is not None:
            try:
                if self.sync_state.pending_paths():
                    return False
            except (OSError, RuntimeError):
                pass
        return True

    def run_full_audit(self) -> bool:
        """Run the rare, full-tree consistency audit in a background thread."""
        if not self._audit_is_idle() and not self._audit_running.is_set():
            return False
        if self._audit_running.is_set():
            return False
        self._audit_running.set()
        self._audit_progress = (0, 0)

        def worker() -> None:
            try:
                report = integrity_audit.run_audit(
                    self.cfg,
                    get_manifest=lambda: rsync_ops.cached_manifest(self.cfg, max_age_seconds=3600)
                    or self._fresh_manifest(),
                    state_store=self.sync_state,
                    progress=lambda done, total: setattr(self, "_audit_progress", (done, total)),
                    cancel=self._stop_flag.is_set,
                )
                integrity_audit.save_report(report)
                self.logger.log(
                    "INFO", "-",
                    f"audit coerenza completato: {report.files_checked} file, "
                    f"{len(report.differences)} differenze ({report.status})",
                )
            except Exception as exc:
                self.logger.log("ERROR", "-", f"audit coerenza fallito: {exc}")
            finally:
                self._audit_running.clear()
                self._audit_progress = (0, 0)

        threading.Thread(target=worker, name="nasbox-integrity-audit", daemon=True).start()
        return True

    def _maybe_full_audit(self, now: float) -> None:
        if now - self._started_at < max(300, self.cfg.get("integrity_audit_startup_delay_seconds", 1800)):
            return
        if self._audit_running.is_set() or not integrity_audit.is_due(self.cfg, now):
            return
        if self._audit_is_idle():
            self.run_full_audit()

    def confirm_deletions(self) -> int:
        """Explicitly allow a large, intentional deletion batch for a short time."""
        until = delete_safety.shared().confirm(self.cfg)
        self.logger.log("INFO", "-", "cancellazioni di massa confermate dall'utente")
        return until

    def delete_safety_snapshot(self) -> dict:
        return delete_safety.shared().snapshot(self.cfg)

    def preview_changes(self) -> dict:
        """Build an on-demand, read-only preview of what a sync would change.

        This is the same classification used by the safety guard, exposed to
        the UI so the user sees *why* a batch was blocked and which folders are
        involved before deciding. It never transfers or modifies anything.
        """
        from pathlib import Path

        from . import change_classifier

        manifest = rsync_ops.cached_manifest(self.cfg, max_age_seconds=3600)
        if manifest is None:
            if self._conn is None:
                return {"ok": False, "detail": "NAS non connesso"}
            try:
                snapshot = rsync_ops.remote_manifest_snapshot(self.cfg, self._conn, -1)
            except (OSError, RuntimeError):
                snapshot = None
            if snapshot is None or not snapshot[1]:
                return {"ok": False, "detail": "manifest NAS non disponibile"}
            manifest = snapshot[1]
        try:
            identity = self.sync_state.identity_snapshot()
        except (AttributeError, OSError, RuntimeError, TypeError):
            identity = {}
        root = Path(self.cfg.local_root())
        missing = [
            path for path, state in manifest.items()
            if state.kind == rsync_ops.RemoteKind.FILE
            and not rsync_ops.path_is_excluded(self.cfg, path)
            and not (root / path).exists()
        ]
        try:
            ratio = float(self.cfg.get("delete_dir_guard_ratio") or 0.5)
            minimum = int(self.cfg.get("delete_dir_guard_min_files") or 20)
        except (TypeError, ValueError):
            ratio, minimum = 0.5, 20
        report = change_classifier.classify(
            missing, manifest, identity, dir_ratio=ratio, min_files=minimum,
        )
        return {
            "ok": True,
            "summary": report.summary(),
            "missing_total": report.missing_total,
            "rename_count": len(report.renames),
            "unexplained_count": report.effective_deletions,
            "anomalies": [
                {
                    "directory": item.directory,
                    "missing": item.missing,
                    "remote": item.remote,
                    "ratio": round(item.ratio, 3),
                }
                for item in report.anomalies
            ],
            "top_dirs": [
                {
                    "directory": directory,
                    "missing": count,
                    "remote": report.remote_by_dir.get(directory, 0),
                }
                for directory, count in report.missing_by_dir.most_common(10)
            ],
            "renames": [
                f"{item.missing_path} → {item.local_path}"
                for item in report.renames[:5]
            ],
            "deletions": report.unexplained[:5],
        }

    def undo_deletions(self, hours: float = 24, limit: int = 500) -> dict:
        """Restore recent NAS deletions from the trash, batch by batch.

        The decision and the bookkeeping stay on the PC; the NAS only receives
        small streaming ``--restore-batch`` calls while we own the lease.
        """
        conn = self._conn
        if conn is None:
            return {"ok": False, "restored": 0, "errors": 0, "detail": "NAS non connesso"}
        try:
            entries = self.sync_state.recent_undo(hours=hours, limit=limit)
        except (OSError, RuntimeError, TypeError) as exc:
            return {"ok": False, "restored": 0, "errors": 0, "detail": str(exc)}
        if not entries:
            return {"ok": True, "restored": 0, "errors": 0, "detail": "nessuna eliminazione recente"}

        batches: dict[str, list[dict]] = {}
        for entry in entries:
            batches.setdefault(str(entry.get("batch_id") or ""), []).append(entry)
        restored = 0
        errors = 0
        device_id = self.sync_state.device_id()
        try:
            with rsync_ops.remote_lock(
                self.cfg, conn, owner_id=device_id, priority=0,
            ):
                for batch_id, items in batches.items():
                    requests = [
                        (str(item.get("path") or ""), str(item.get("version_ts") or "latest"))
                        for item in items
                    ]
                    statuses = rsync_ops.restore_batch(self.cfg, conn, requests, device_id)
                    done = [path for path, state in statuses.items() if state == "RESTORED"]
                    if done:
                        self.sync_state.mark_undo_applied(batch_id, done)
                        restored += len(done)
                    errors += sum(
                        1 for state in statuses.values()
                        if state in ("ERROR", "NO_VERSION", "EXISTS")
                    )
        except (rsync_ops.RemoteLockError, OSError, RuntimeError) as exc:
            return {"ok": False, "restored": restored, "errors": errors, "detail": str(exc)}
        self.logger.log("UNDO", "-", f"recuperati {restored} file dal cestino NAS ({errors} errori)")
        return {"ok": errors == 0, "restored": restored, "errors": errors}

    def assurance_snapshot(self) -> dict:
        """Facts that answer one question: 'is everything synced?'."""
        now = time.time()
        if self._assurance_cache is not None and now - self._assurance_cache[0] < 30:
            tracked = self._assurance_cache[1]
        else:
            try:
                tracked = self.sync_state.entry_count() if self.sync_state else 0
            except (OSError, RuntimeError):
                tracked = 0
            self._assurance_cache = (now, tracked)
        scheduler = self.transfer_scheduler.snapshot() if self.transfer_scheduler else {}
        pending = self._pending_summary or {}
        delete_snapshot = delete_safety.shared().snapshot(self.cfg)
        try:
            undo_count = int(self.sync_state.undo_count())
        except (AttributeError, OSError, RuntimeError, TypeError, ValueError):
            undo_count = 0
        result = {
            "tracked_files": tracked,
            "pending_count": int(pending.get("count", 0) or 0),
            "queue_count": int(scheduler.get("count", 0) or 0),
            "active_kind": scheduler.get("active_kind") or "",
            "last_integrity_check_ts": float(self.cfg.get("last_integrity_check_ts") or 0),
            "last_integrity_files": int(self.cfg.get("last_integrity_files") or 0),
            "last_integrity_mismatches": int(self.cfg.get("last_integrity_mismatches") or 0),
            "last_integrity_error": str(self.cfg.get("last_integrity_error") or ""),
            "integrity_sample": int(self.cfg.get("integrity_check_sample_size") or 0),
            "manifest_revision": int(self.cfg.get("last_manifest_revision") or -1),
            "server_health": self._server_health,
            "repository_ready": bool(self.cfg.get("remote_repository_ready")),
            "journal_ready": bool(self.cfg.get("remote_journal_ready")),
            "delete_used": delete_snapshot.get("used", 0),
            "delete_limit": delete_snapshot.get("limit", 0),
            "undo_count": undo_count,
        }
        return result

    def prune_now(self) -> int:
        removed = trash.prune_local(self.cfg, self.logger)
        self._last_prune = time.time()
        return removed

    @property
    def host(self) -> str | None:
        return self._conn.host if self._conn else None

    @property
    def via_jump(self) -> bool:
        return bool(self._conn and self._conn.via_jump)

    @property
    def connection(self) -> rsync_ops.NasConnection | None:
        return self._conn

    # --- main loop ---

    def run(self) -> None:
        while not self._stop_flag.is_set():
            try:
                self._tick()
            except Exception as exc:  # keep the loop alive no matter what
                self.logger.log("ERROR", "-", detail=f"tick failed: {exc}")
                self.log_event.emit("ERROR", "-", f"tick failed: {exc}")
            self._wake.wait(TICK_SECONDS)
            self._wake.clear()

    def _tick(self) -> None:
        now = time.time()
        self._reconcile_watcher()
        if not self._identity_backfilled and now - self._started_at >= 5:
            self._identity_backfilled = True
            try:
                self.sync_state.backfill_identities(
                    self.cfg.local_root(),
                    lambda path: rsync_ops.path_is_excluded(self.cfg, path),
                )
            except (AttributeError, OSError, RuntimeError, TypeError):
                pass
        self._maybe_integrity_check(now)
        self._maybe_full_audit(now)
        self._maybe_refresh_devices(now)

        if now - self._last_pending_status >= PENDING_STATUS_INTERVAL_SECONDS:
            self._pending_summary = self.sync_state.pending_summary() if self.sync_state is not None else {}
            self._last_pending_status = now

        if self._conn is None or now - self._last_host_check >= HOST_RECHECK_SECONDS:
            self._conn = rsync_ops.resolve_connection(self.cfg)
            self._last_host_check = time.time()
            self.connection_changed.emit(self._conn)
            if self._conn is None:
                self._set_server_health("unknown")

        paused = self.cfg.is_paused()
        schedule_active = self.cfg.schedule_active_at()
        schedule_remaining = self.cfg.schedule_remaining_seconds() if schedule_active else None
        watcher = self.watchers.get()
        watcher_mode, watcher_detail = "disabled", ""
        if watcher is not None:
            try:
                candidate_status = watcher.status()
                if isinstance(candidate_status, tuple) and len(candidate_status) == 2:
                    watcher_mode, watcher_detail = candidate_status
            except (AttributeError, TypeError, ValueError):
                # Keep status reporting compatible with lightweight test doubles.
                watcher_mode, watcher_detail = "starting", ""
        self.status_changed.emit({
            "paused": paused,
            "pause_remaining": self.cfg.pause_remaining_seconds(),
            "schedule_active": schedule_active,
            "schedule_remaining": schedule_remaining,
            "host": self._conn.host if self._conn else None,
            "via_jump": self._conn.via_jump if self._conn else False,
            "connected": self._conn is not None,
            "configured": self.cfg.is_configured(),
            "server_lock_held": self.cfg.get("server_lock_held", False),
            "server_lock_age_seconds": self.cfg.get("server_lock_age_seconds", 0),
            "server_lock_owner_pid": self.cfg.get("server_lock_owner_pid", ""),
            "server_lock_owner_id": self.cfg.get("server_lock_owner_id", ""),
            "server_lock_owner_host": self.cfg.get("server_lock_owner_host", ""),
            "watcher_mode": watcher_mode,
            "watcher_detail": watcher_detail,
            "pending_summary": self._pending_summary,
            "scheduler": self.transfer_scheduler.snapshot() if self.transfer_scheduler else {},
            "server_health": self._server_health,
            "cycle_ts": now,
            "assurance": self.assurance_snapshot(),
            "audit": self.audit_snapshot(),
            "devices": list(self._devices),
        })

        if now - self._last_prune > PRUNE_INTERVAL_SECONDS:
            self.prune_now()

        health_interval = (
            HEALTH_CHECK_OK_INTERVAL_SECONDS
            if self._server_health == "ok" and self._health_ok_streak >= 2
            else HEALTH_CHECK_INTERVAL_SECONDS
        )
        if now - self._last_health_check >= health_interval:
            self._check_server_health()
            self._last_health_check = time.time()

    # --- watcher lifecycle ---

    def _reconcile_watcher(self) -> None:
        local_root = self.cfg.local_root()

        if not self.cfg.allows_push():
            old = self.watchers.get()
            if old is not None:
                old.stop()
            self.watchers.set(None)
            self._watched_path = None
            return

        if local_root != self._watched_path:
            old = self.watchers.get()
            if old is not None:
                old.stop()
            self.watchers.set(None)
            self._watched_path = None

        if local_root and self.watchers.get() is None:
            watcher = FolderWatcher(
                local_root,
                on_error=lambda msg: self._log("ERROR", "-", msg),
                is_excluded=lambda relative: rsync_ops.path_is_excluded(self.cfg, relative),
            )
            watcher.start()
            # A genuinely new empty folder pulls first. An empty folder with a
            # persisted baseline may instead contain offline deletions, which
            # must be reconciled rather than silently restored from the NAS.
            try:
                with os.scandir(local_root) as entries:
                    has_local_content = next(entries, None) is not None
            except OSError as exc:
                self._log("ERROR", "-", f"impossibile leggere la cartella locale: {exc}")
                has_local_content = False
            has_pending = self.sync_state is not None and self.sync_state.has_pending()
            if (
                has_local_content
                or (self.sync_state is not None and self.sync_state.has_entries())
                or has_pending
            ):
                watcher.mark_dirty()
            self.watchers.set(watcher)
            self._watched_path = local_root
            if self._push_worker is not None and (has_local_content or has_pending):
                # inotify only reports changes that happen after it starts. A
                # restart can therefore leave pre-existing local differences in
                # the preview but outside the durable push queue. Keep an
                # explicit full reconciliation request until PushWorker has
                # resolved it, rather than relying on this one watcher event.
                self._push_worker.request_full_sync()

    def _log(self, action: str, path_: str, detail: str = "") -> None:
        self.logger.log(action, path_, detail)
        self.log_event.emit(action, path_, detail)

    # --- NAS-side server daemon health (running? outdated?) ---

    def _set_server_health(self, state: str) -> None:
        if state == "ok":
            self._health_ok_streak += 1
        else:
            self._health_ok_streak = 0
        if state == self._server_health:
            return
        self._server_health = state
        self.server_health_changed.emit(state)

    def _check_server_health(self) -> None:
        if not self.cfg.get("remote_server_script") or self._conn is None:
            self._set_server_health("unknown")
            return  # nothing configured yet / not connected -- nothing to check

        ok, values, err = trash.fetch_remote_config(self.cfg, self._conn)
        if not ok:
            if "precedente" in err or "sconosciuta" in err.lower():
                self._set_server_health("outdated")
                message = (
                    f"{err}\n\nLo script server aggiornato è nella cartella server/, "
                    "accanto allo script attivo. Copialo sul NAS e riavvia sync-daemon-server.sh."
                )
                self._log("SERVER_OUTDATED", "-", message)
                self.server_outdated.emit(message)
            else:
                self._set_server_health("unresponsive")
            return  # other failures (transient connectivity, misconfigured path) stay quiet here

        state_dir = values.get("STATE_DIR", "")
        if state_dir and state_dir != self.cfg.get("server_state_dir_remote"):
            self.cfg.set("server_state_dir_remote", state_dir)
        lock_file = values.get("SYNC_LOCK_FILE", "")
        if lock_file != self.cfg.get("server_lock_file_remote"):
            self.cfg.set("server_lock_file_remote", lock_file)
        owner_file = values.get("SYNC_LOCK_OWNER_FILE", "")
        if owner_file != self.cfg.get("server_lock_owner_file_remote"):
            self.cfg.set("server_lock_owner_file_remote", owner_file)
        lock_held = values.get("SYNC_LOCK_HELD", "").lower() == "true"
        if lock_held != self.cfg.get("server_lock_held"):
            self.cfg.set("server_lock_held", lock_held)
        try:
            lock_age = int(values.get("SYNC_LOCK_AGE_SECONDS", "0") or 0)
        except ValueError:
            lock_age = 0
        if lock_age != self.cfg.get("server_lock_age_seconds"):
            self.cfg.set("server_lock_age_seconds", lock_age)
        owner_pid = values.get("SYNC_LOCK_OWNER_PID", "")
        if owner_pid != self.cfg.get("server_lock_owner_pid"):
            self.cfg.set("server_lock_owner_pid", owner_pid)
        owner_id = values.get("SYNC_LOCK_OWNER_ID", "")
        if owner_id != self.cfg.get("server_lock_owner_id"):
            self.cfg.set("server_lock_owner_id", owner_id)
        owner_host = values.get("SYNC_LOCK_OWNER_HOST", "")
        if owner_host != self.cfg.get("server_lock_owner_host"):
            self.cfg.set("server_lock_owner_host", owner_host)
        owner_started = values.get("SYNC_LOCK_STARTED_AT", "0")
        try:
            owner_started_value = int(owner_started or 0)
        except ValueError:
            owner_started_value = 0
        if owner_started_value != self.cfg.get("server_lock_started_at"):
            self.cfg.set("server_lock_started_at", owner_started_value)

        repository_id = values.get("REPOSITORY_ID", "")
        if repository_id != self.cfg.get("repository_id"):
            self.cfg.set("repository_id", repository_id)
        repository_ready = values.get("REPOSITORY_READY", "").lower() == "true"
        if repository_ready != self.cfg.get("remote_repository_ready"):
            self.cfg.set("remote_repository_ready", repository_ready)
        journal_ready = values.get("JOURNAL_READY", "").lower() == "true"
        if journal_ready != self.cfg.get("remote_journal_ready"):
            self.cfg.set("remote_journal_ready", journal_ready)
        change_feed_available = values.get("CHANGE_FEED_AVAILABLE", "").lower() == "true"
        if change_feed_available != self.cfg.get("remote_change_feed_available"):
            self.cfg.set("remote_change_feed_available", change_feed_available)
        causal_versions_available = values.get("CAUSAL_VERSIONS_AVAILABLE", "").lower() == "true"
        if causal_versions_available != self.cfg.get("remote_causal_versions_available"):
            self.cfg.set("remote_causal_versions_available", causal_versions_available)

        if not values.get("running"):
            self._set_server_health("down")
            self._log("SERVER_DOWN", "-", "il demone server sul NAS non è attivo: avvio automatico in corso")
            self._restart_server_daemon()
        else:
            self._set_server_health("ok")

        remote_version = values.get("VERSION", "")
        versioned_update = None
        now = time.time()
        if now - self._last_update_check >= SERVER_UPDATE_CHECK_INTERVAL_SECONDS:
            self._last_update_check = now
            versioned_update = rsync_ops.discover_remote_server_update(
                self.cfg, self._conn, self.cfg.get("remote_server_script"),
            )
        if versioned_update is not None:
            versioned_path, versioned_version = versioned_update
            if (
                rsync_ops._version_tuple(versioned_version) > rsync_ops._version_tuple(remote_version or "0")
                and versioned_path != self._offered_server_update
            ):
                message = (
                    f"Sul NAS è disponibile lo script server {versioned_version}, "
                    f"mentre è attivo {remote_version or 'una versione sconosciuta'}. "
                    "Vuoi installarlo, riavviare il servizio e verificarlo?"
                )
                self._offered_server_update = versioned_path
                self._log("SERVER_UPDATE_AVAILABLE", "-", message)
                self.server_update_available.emit(message, versioned_path, versioned_version)
                return
        if remote_version and rsync_ops.server_is_outdated(remote_version):
            now = time.time()
            if now - self._last_version_warning >= VERSION_WARNING_REPEAT_SECONDS:
                message = (
                    f"Il pacchetto server sul NAS è alla versione {remote_version}, "
                    f"questo client si aspetta almeno la {rsync_ops.EXPECTED_SERVER_VERSION}. "
                    "Lo script aggiornato è nella cartella server/, accanto allo script attivo: "
                    "copialo sul NAS e riavvia sync-daemon-server.sh."
                )
                self._log("SERVER_OUTDATED", "-", message)
                self.server_outdated.emit(message)
                self._last_version_warning = now

    def update_server(self, versioned_path: str, expected_version: str) -> tuple[bool, str]:
        if self._conn is None:
            return False, "NAS non raggiungibile"
        active_script = (self.cfg.get("remote_server_script") or "").strip()
        if not active_script:
            return False, "script server sul NAS non configurato"
        return rsync_ops.update_remote_server_script(
            self.cfg, self._conn, active_script, versioned_path, expected_version,
        )

    def _restart_server_daemon(self) -> None:
        # The daemon is only responsible for NAS-side trash/retention pruning -- sync
        # itself doesn't depend on it (every push/pull writes straight over SSH) -- but
        # leaving it down means old versions on the NAS just accumulate forever, so it's
        # worth trying to self-heal rather than silently doing nothing until someone
        # notices. --start is idempotent server-side (a no-op if it's already running),
        # so this is safe to call based on a possibly-stale "not running" read.
        script_path = self.cfg.get("remote_server_script")
        self._log("SERVER_DOWN", "-", "il demone server sul NAS non risulta attivo, provo a riavviarlo")
        ok, stdout, stderr = rsync_ops.run_remote_script(self.cfg, self._conn, script_path, ["--start"], timeout=30)
        detail = (stdout + stderr).strip()
        if ok:
            message = detail or "demone riavviato con successo"
            self._set_server_health("ok")
            self._log("SERVER_RESTARTED", "-", message)
            self.server_restarted.emit(f"Il demone server sul NAS non era attivo: riavviato.\n\n{message}")
        else:
            message = detail or "errore sconosciuto"
            self._set_server_health("unresponsive")
            self._log("ERROR", "-", f"impossibile riavviare il demone sul NAS: {message}")
            self.server_restarted.emit(
                f"Il demone server sul NAS non è attivo e il tentativo di riavvio automatico è fallito:\n\n{message}\n\n"
                "Potrebbe servire un intervento manuale sul NAS."
            )
