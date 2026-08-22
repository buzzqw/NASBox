"""External-folder mirrors: keep a synced copy of folders that live OUTSIDE
the NASBox tree, so whatever is in them shows up inside NASBox too -- and,
from there, on the NAS and on every other connected PC, via the client's
ordinary push (the main NASBox watcher sees the freshly written copy and
uploads it as if you had put the files there yourself).

This is deliberately a ONE-WAY mirror: the external source folder is the
authoritative copy. Every pass re-runs `rsync -a --delete source/ dest/`, so
anything that was changed or deleted on the NAS/other PCs inside the mirror's
dest folder gets overwritten or removed by the next pass. Editing those files
from another PC is pointless and the change will be lost -- the mirror always
wins because it is the only thing that feeds this folder.

Each enabled mirror is watched (inotifywait when available, mtime polling
otherwise, reusing FolderWatcher) so changes trigger a copy almost
immediately, plus a slow periodic re-sync that catches anything the watcher
missed (changes while the machine was suspended, an inotify overflow, a
program that writes a file then renames it without ever closing it...).
"""
from __future__ import annotations

import hashlib
import os
import subprocess
import threading
import time

from PyQt6.QtCore import QThread, pyqtSignal

from .config import Config
from .logger import EventLogger
from . import paths, rsync_ops
from .watcher import FolderWatcher

DEBOUNCE_SECONDS = 2        # quiet time after a detected change before mirroring
PERIODIC_SECONDS = 300      # slow catch-all re-sync (also covers the initial pass)
TICK_SECONDS = 0.5
SYNC_TIMEOUT_SECONDS = 3600

MIRROR_EXCLUDE_DIRNAMES = (".sync-trash", ".sync-partial", ".nasbox-root", "@eaDir")

RESERVED_DEST_NAMES = {".sync-trash", ".sync-partial", ".nasbox-root", ".~tmp~"}


def _stamp_path_for(source: str) -> str:
    digest = hashlib.sha1(os.fsencode(source)).hexdigest()[:16]
    return str(paths.state_dir() / f"mirror-stamp-{digest}")


def validate_mirror(cfg: Config, source: str, dest: str) -> tuple[bool, str]:
    """Return (ok, error). Kept as a module-level function so both the GUI tab
    and anything else can validate without importing Qt widgets."""
    from .i18n import t

    source = (source or "").strip()
    dest = (dest or "").strip("/").strip()

    if not source:
        return False, t("mirrors.err_source_required")
    if not dest:
        return False, t("mirrors.err_dest_required")
    if not os.path.isabs(source):
        return False, t("mirrors.err_source_absolute")
    if not os.path.isdir(source):
        return False, t("mirrors.err_source_missing", source=source)
    if not dest or dest.startswith("/") or ".." in dest.split("/") or "\\" in dest:
        return False, t("mirrors.err_dest_invalid")
    if dest in RESERVED_DEST_NAMES:
        return False, t("mirrors.err_dest_reserved")

    root = cfg.local_root().rstrip("/")
    if not root:
        return False, t("mirrors.err_nasbox_not_configured")
    normalized_source = os.path.normpath(source)
    if normalized_source == root or normalized_source.startswith(root + os.sep):
        return False, t("mirrors.err_source_inside_nasbox")

    for entry in cfg.mirrors():
        if entry.get("source") == source:
            return False, t("mirrors.err_source_taken")
        if (entry.get("dest") or "").strip("/") == dest:
            return False, t("mirrors.err_dest_taken")

    return True, ""


class MirrorWatcher(QThread):
    """Watches one external source folder and mirrors it into NASBox/<dest>.
    Owns its own FolderWatcher (so it can be stopped/restarted independently
    as the mirror list changes) and runs the rsync copies serially."""

    status_changed = pyqtSignal(dict)       # per-mirror status: see _emit_status
    log_event = pyqtSignal(str, str, str)   # action, path, detail (matches SyncEngine)

    def __init__(
        self, cfg: Config, logger: EventLogger, source: str, dest: str, root: str,
        transfer_lock: threading.Lock,
    ) -> None:
        super().__init__()
        self.cfg = cfg
        self.logger = logger
        self.source = source
        self.dest = dest
        self.root = root.rstrip("/")
        self.transfer_lock = transfer_lock
        self._stop = threading.Event()
        self._force_sync = threading.Event()
        self._last_emitted: dict | None = None
        self._current_proc = None

    def request_sync(self) -> None:
        self._force_sync.set()

    def stop(self) -> None:
        self._stop.set()
        self._force_sync.set()
        proc = self._current_proc
        if proc is not None and proc.poll() is None:
            try:
                proc.terminate()
            except OSError:
                pass
        self.wait(10_000)
        proc = self._current_proc
        if self.isRunning() and proc is not None and proc.poll() is None:
            try:
                proc.kill()
            except OSError:
                pass
            self.wait(5_000)

    def run(self) -> None:
        self._emit_status("idle", self._t("mirrors.state_idle"))
        watcher = FolderWatcher(
            self.source,
            on_error=lambda msg: self._log("ERROR", self.source, msg),
            is_excluded=lambda relative: rsync_ops.path_is_excluded(self.cfg, relative),
            stamp_path=_stamp_path_for(self.source),
        )
        watcher.start()
        try:
            while not self._stop.is_set():
                try:
                    if self._force_sync.is_set():
                        self._force_sync.clear()
                        self._sync()
                    elif not self.cfg.is_paused() and watcher.consume_if_ready(DEBOUNCE_SECONDS):
                        self._sync()
                    else:
                        self._maybe_periodic_sync()
                except Exception as exc:
                    self._set_error(str(exc))
                    self._log("ERROR", self.source, f"loop mirror fallito: {exc}")
                self._stop.wait(TICK_SECONDS)
        finally:
            watcher.stop()

    # --- internals ---

    def _maybe_periodic_sync(self) -> None:
        if self.cfg.is_paused():
            return
        entry = self.cfg.mirror_by_source(self.source)
        last_sync = (entry or {}).get("last_sync") or 0
        if time.time() - last_sync >= PERIODIC_SECONDS:
            self._sync()

    def _current_entry(self) -> dict | None:
        return self.cfg.mirror_by_source(self.source)

    def _sync(self) -> None:
        from .i18n import t

        entry = self._current_entry()
        if entry is None or not entry.get("enabled", True):
            return
        source = (entry.get("source") or "").rstrip("/")
        dest = (entry.get("dest") or "").strip("/")
        if not source or not dest:
            self._set_error(t("mirrors.err_invalid_entry"))
            return
        if not os.path.isdir(source):
            self._set_error(t("mirrors.err_source_missing", source=source))
            return
        if not self.root:
            self._set_error(t("mirrors.err_nasbox_not_configured"))
            return

        destination = f"{self.root}/{dest}"
        self._emit_status("syncing", self._t("mirrors.state_syncing"))
        cmd = [
            "rsync", "-a", "--delete-after", "--partial",
            "--partial-dir=.sync-partial", "--delay-updates", "--info=progress2",
        ]
        for pattern in self.cfg.exclude_patterns():
            pattern = pattern.strip()
            if pattern:
                cmd += ["--exclude", pattern]
        for name in MIRROR_EXCLUDE_DIRNAMES:
            cmd += ["--exclude", name]
        cmd += [f"{source}/", f"{destination}/"]

        acquired = False
        try:
            while not self._stop.is_set():
                acquired = self.transfer_lock.acquire(timeout=0.2)
                if acquired:
                    break
            if not acquired:
                return
            proc = subprocess.Popen(
                cmd, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True,
            )
            self._current_proc = proc
            try:
                stdout, stderr = proc.communicate(timeout=SYNC_TIMEOUT_SECONDS)
            except subprocess.TimeoutExpired:
                proc.terminate()
                stdout, stderr = proc.communicate(timeout=10)
                if proc.returncode is None:
                    proc.kill()
                    stdout, stderr = proc.communicate()
                raise RuntimeError("timeout sincronizzazione mirror")
        except (OSError, RuntimeError) as exc:
            self._set_error(str(exc))
            self._log("ERROR", source, f"sync mirror fallito: {exc}")
            return
        finally:
            self._current_proc = None
            if acquired:
                self.transfer_lock.release()

        if proc.returncode != 0:
            detail = (stderr or "").strip() or f"rsync terminato con codice {proc.returncode}"
            self._set_error(detail)
            self._log("ERROR", source, f"sync mirror fallito: {detail}")
            return

        self.cfg.update_mirror(source, last_sync=time.time(), error="")
        self._emit_status("idle", self._t("mirrors.state_idle"))
        self._log("MIRROR", source, f"replica aggiornata in NASBox/{dest}")

    def _set_error(self, message: str) -> None:
        self.cfg.update_mirror(self.source, error=message)
        self._emit_status("error", message)

    def _emit_status(self, state: str, message: str) -> None:
        entry = self._current_entry() or {}
        status = {
            "source": self.source,
            "dest": self.dest,
            "state": state,
            "message": message,
            "last_sync": entry.get("last_sync"),
            "error": entry.get("error") or "",
            "enabled": bool(entry.get("enabled", True)),
        }
        if status == self._last_emitted:
            return
        self._last_emitted = status
        self.status_changed.emit(status)

    def _log(self, action: str, path_: str, detail: str) -> None:
        self.logger.log(action, path_, detail)
        self.log_event.emit(action, path_, detail)

    @staticmethod
    def _t(key: str) -> str:
        # Strings stay in the client's language just like the rest of the UI.
        from .i18n import t
        return t(key)


class MirrorManager(QThread):
    """Owns the mirror list lifecycle: starts/stops a MirrorWatcher per enabled
    mirror as the config (or the NASBox root) changes, and relays their status
    updates up to the GUI."""

    status_changed = pyqtSignal(dict)
    log_event = pyqtSignal(str, str, str)

    def __init__(self, cfg: Config, logger: EventLogger, transfer_lock: threading.Lock) -> None:
        super().__init__()
        self.cfg = cfg
        self.logger = logger
        self.transfer_lock = transfer_lock
        self._stop = threading.Event()
        self._wake = threading.Event()
        self._watchers: dict[str, MirrorWatcher] = {}
        self._last_root: str | None = None
        self._disabled_emitted: set[str] = set()

    def stop(self) -> None:
        self._stop.set()
        self._wake.set()
        for watcher in list(self._watchers.values()):
            watcher.stop()
        self._watchers.clear()
        self.wait(10_000)

    def refresh(self) -> None:
        """Re-read the mirror list and the NASBox root on the next tick -- call
        after the user adds/removes/toggles a mirror in the GUI."""
        self._wake.set()

    def sync_now(self, source: str) -> None:
        watcher = self._watchers.get(source)
        if watcher is not None:
            watcher.request_sync()

    def run(self) -> None:
        while not self._stop.is_set():
            try:
                self._reconcile()
            except Exception as exc:
                self.logger.log("ERROR", "-", detail=f"mirror tick fallito: {exc}")
            self._wake.wait(TICK_SECONDS)
            self._wake.clear()

    def _reconcile(self) -> None:
        root = self.cfg.local_root().rstrip("/")
        desired: dict[str, tuple[str, str]] = {}
        for entry in self.cfg.mirrors():
            source = (entry.get("source") or "").strip()
            if not source:
                continue
            dest = (entry.get("dest") or "").strip("/")
            enabled = bool(entry.get("enabled", True))
            if enabled:
                desired[source] = (dest, root)

        # Stop watchers that were removed, disabled, or whose dest/root changed.
        for source, watcher in list(self._watchers.items()):
            spec = desired.get(source)
            if spec is None or spec != (watcher.dest, watcher.root):
                watcher.stop()
                del self._watchers[source]
                self._disabled_emitted.discard(source)

        # Start watchers for newly enabled mirrors (with a configured NASBox root).
        for source, (dest, root) in desired.items():
            if source in self._watchers:
                continue
            if not root:
                self._emit_disabled(source, dest, "mirrors.state_unconfigured")
                continue
            watcher = MirrorWatcher(
                self.cfg, self.logger, source, dest, root, self.transfer_lock,
            )
            watcher.status_changed.connect(self.status_changed)
            watcher.log_event.connect(self.log_event)
            self._watchers[source] = watcher
            self._disabled_emitted.discard(source)
            watcher.start()

        if root != self._last_root:
            self._last_root = root
            if root:
                self._wake.set()  # re-evaluate mirrors against the new root

    def _emit_disabled(self, source: str, dest: str, message_key: str) -> None:
        if source in self._disabled_emitted:
            return
        self._disabled_emitted.add(source)
        self.status_changed.emit({
            "source": source,
            "dest": dest,
            "state": "disabled",
            "message": self._t(message_key),
            "last_sync": None,
            "error": "",
            "enabled": True,
        })

    @staticmethod
    def _t(key: str) -> str:
        from .i18n import t
        return t(key)
