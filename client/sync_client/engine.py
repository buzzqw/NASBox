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

from . import rsync_ops, trash
from .config import Config
from .logger import EventLogger
from .pull_worker import PullWorker
from .push_worker import PushWorker
from .repository_safety import RepositorySafetyError
from .sync_state import SyncStateStore
from .watcher import FolderWatcher, WatcherHandle

TICK_SECONDS = 1
HOST_RECHECK_SECONDS = 30
PRUNE_INTERVAL_SECONDS = 24 * 3600
HEALTH_CHECK_INTERVAL_SECONDS = 15 * 60  # is the NAS-side server daemon actually running?
VERSION_WARNING_REPEAT_SECONDS = 6 * 3600  # don't re-nag about an outdated server package more than this often


class SyncEngine(QThread):
    log_event = pyqtSignal(str, str, str)         # action, path, detail
    status_changed = pyqtSignal(dict)
    connection_changed = pyqtSignal(object)       # rsync_ops.NasConnection | None -- lets the other
                                                   # workers reuse this instead of independently resolving their own
    server_outdated = pyqtSignal(str)             # friendly message, emitted whenever detected
    server_update_available = pyqtSignal(str, str, str)  # message, path, version
    server_restarted = pyqtSignal(str)            # friendly message -- NAS daemon was found down and (attempted to be) restarted

    def __init__(
        self, cfg: Config, logger: EventLogger, watchers: WatcherHandle,
        push_worker: Optional[PushWorker] = None, pull_worker: Optional[PullWorker] = None,
        sync_state: Optional[SyncStateStore] = None,
    ) -> None:
        super().__init__()
        self.cfg = cfg
        self.logger = logger
        self.watchers = watchers
        self._push_worker = push_worker
        self._pull_worker = pull_worker
        self.sync_state = sync_state
        self._stop_flag = threading.Event()
        self._wake = threading.Event()
        self._conn: rsync_ops.NasConnection | None = None
        self._watched_path: str | None = None  # which local_root the current FolderWatcher is bound to
        self._last_host_check = 0.0
        self._last_prune = 0.0
        self._last_health_check = 0.0
        self._last_version_warning = 0.0
        self._offered_server_update = ""

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
        if self._push_worker:
            self._push_worker.request_full_sync()
        if self._pull_worker:
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

        if self._conn is None or now - self._last_host_check >= HOST_RECHECK_SECONDS:
            self._conn = rsync_ops.resolve_connection(self.cfg)
            self._last_host_check = time.time()
            self.connection_changed.emit(self._conn)

        paused = self.cfg.is_paused()
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
            "host": self._conn.host if self._conn else None,
            "via_jump": self._conn.via_jump if self._conn else False,
            "connected": self._conn is not None,
            "configured": self.cfg.is_configured(),
            "server_lock_held": self.cfg.get("server_lock_held", False),
            "server_lock_age_seconds": self.cfg.get("server_lock_age_seconds", 0),
            "server_lock_owner_pid": self.cfg.get("server_lock_owner_pid", ""),
            "watcher_mode": watcher_mode,
            "watcher_detail": watcher_detail,
            "cycle_ts": now,
        })

        if now - self._last_prune > PRUNE_INTERVAL_SECONDS:
            self.prune_now()

        if now - self._last_health_check >= HEALTH_CHECK_INTERVAL_SECONDS:
            self._check_server_health()
            self._last_health_check = time.time()

    # --- watcher lifecycle ---

    def _reconcile_watcher(self) -> None:
        local_root = self.cfg.local_root()

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
            if has_local_content or (self.sync_state is not None and self.sync_state.has_entries()):
                watcher.mark_dirty()
            self.watchers.set(watcher)
            self._watched_path = local_root

    def _log(self, action: str, path_: str, detail: str = "") -> None:
        self.logger.log(action, path_, detail)
        self.log_event.emit(action, path_, detail)

    # --- NAS-side server daemon health (running? outdated?) ---

    def _check_server_health(self) -> None:
        if not self.cfg.get("remote_server_script") or self._conn is None:
            return  # nothing configured yet / not connected -- nothing to check

        ok, values, err = trash.fetch_remote_config(self.cfg, self._conn)
        if not ok:
            if "precedente" in err or "sconosciuta" in err.lower():
                message = (
                    f"{err}\n\nLo script server aggiornato è nella cartella server/, "
                    "accanto allo script attivo. Copialo sul NAS e riavvia sync-daemon-server.sh."
                )
                self._log("SERVER_OUTDATED", "-", message)
                self.server_outdated.emit(message)
            return  # other failures (transient connectivity, misconfigured path) stay quiet here

        state_dir = values.get("STATE_DIR", "")
        if state_dir and state_dir != self.cfg.get("server_state_dir_remote"):
            self.cfg.set("server_state_dir_remote", state_dir)
        lock_file = values.get("SYNC_LOCK_FILE", "")
        if lock_file != self.cfg.get("server_lock_file_remote"):
            self.cfg.set("server_lock_file_remote", lock_file)
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

        repository_id = values.get("REPOSITORY_ID", "")
        if repository_id != self.cfg.get("repository_id"):
            self.cfg.set("repository_id", repository_id)
        repository_ready = values.get("REPOSITORY_READY", "").lower() == "true"
        if repository_ready != self.cfg.get("remote_repository_ready"):
            self.cfg.set("remote_repository_ready", repository_ready)
        journal_ready = values.get("JOURNAL_READY", "").lower() == "true"
        if journal_ready != self.cfg.get("remote_journal_ready"):
            self.cfg.set("remote_journal_ready", journal_ready)

        if not values.get("running"):
            self._log("SERVER_DOWN", "-", "il demone server sul NAS non è attivo: avvio automatico in corso")
            self._restart_server_daemon()

        remote_version = values.get("VERSION", "")
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
            self._log("SERVER_RESTARTED", "-", message)
            self.server_restarted.emit(f"Il demone server sul NAS non era attivo: riavviato.\n\n{message}")
        else:
            message = detail or "errore sconosciuto"
            self._log("ERROR", "-", f"impossibile riavviare il demone sul NAS: {message}")
            self.server_restarted.emit(
                f"Il demone server sul NAS non è attivo e il tentativo di riavvio automatico è fallito:\n\n{message}\n\n"
                "Potrebbe servire un intervento manuale sul NAS."
            )
