from __future__ import annotations

import os
import sys
import time

from PyQt6.QtCore import Qt, QTimer, QUrl
from PyQt6.QtGui import QDesktopServices
from PyQt6.QtWidgets import (
    QGroupBox, QHBoxLayout, QLabel, QMessageBox, QPushButton, QVBoxLayout,
    QWidget,
)

from ..config import Config
from ..engine import SyncEngine
from ..scan_worker import ScanWorker
from .. import trash
from .. import rsync_ops
from ..i18n import t
from ..repository_safety import RepositorySafetyError, initialize_local_root
from ..version import APP_NAME, APP_VERSION
from .. import updater
from .dialogs import FolderSetupDialog, PauseForDialog
from .async_utils import run_in_background
from .format_utils import human_size


class StatusTab(QWidget):
    """The tab you actually look at day to day: is it working, is anything
    paused, and where's my stuff. Anything you only touch once in a while
    (NAS connection, bandwidth, retention...) lives in the Impostazioni tab
    instead -- keeping this one uncluttered is the whole point of it."""

    def __init__(self, cfg: Config, engine: SyncEngine, scan_worker: ScanWorker, parent=None) -> None:
        super().__init__(parent)
        self.cfg = cfg
        self.engine = engine
        self.scan_worker = scan_worker
        self._update_check_busy = False
        self._update_auto_checked = False
        self._update_candidate = None

        root = QVBoxLayout(self)
        root.setContentsMargins(4, 8, 4, 4)
        root.setSpacing(14)

        # --- status ---
        status_box = QGroupBox(t("status.group_title"))
        status_box.setObjectName("statusCard")
        status_layout = QVBoxLayout(status_box)
        self.status_label = QLabel(t("status.starting"))
        self.status_label.setObjectName("statusMessage")
        self.status_label.setWordWrap(True)
        self.status_label.setProperty("state", "starting")
        status_layout.addWidget(self.status_label)
        self.queue_label = QLabel(t("status.queue_unknown"))
        self.queue_label.setObjectName("statusQueue")
        status_layout.addWidget(self.queue_label)
        # Version info with click-to-check-update
        self.version_label = QLabel(f"{APP_NAME} v{APP_VERSION}")
        self.version_label.setObjectName("statusVersion")
        self.version_label.setCursor(Qt.CursorShape.PointingHandCursor)
        self.version_label.setToolTip(t("status.version_tooltip"))
        self.version_label.mousePressEvent = lambda _e: self._check_for_update()
        status_layout.addWidget(self.version_label)
        self.update_available_label = QLabel()
        self.update_available_label.setObjectName("statusUpdateAvailable")
        self.update_available_label.setWordWrap(True)
        self.update_available_label.hide()
        self.update_available_label.mousePressEvent = lambda _e: self._check_for_update()
        status_layout.addWidget(self.update_available_label)
        # Shown only while a large first-time batch is being checksummed locally
        # (see PushWorker.hash_progress) -- otherwise this phase used to look
        # frozen for as long as it took, with the rest of the status area
        # holding stale text from before the sync started.
        self.hash_progress_label = QLabel()
        self.hash_progress_label.setObjectName("statusHashProgress")
        self.hash_progress_label.hide()
        status_layout.addWidget(self.hash_progress_label)
        diagnostics_row = QHBoxLayout()
        self.diagnostics_label = QLabel(t("status.diagnostics_unknown"))
        self.diagnostics_label.setWordWrap(True)
        diagnostics_row.addWidget(self.diagnostics_label, 1)
        diagnostics_btn = QPushButton(t("status.diagnostics_refresh_btn"))
        diagnostics_btn.clicked.connect(self._refresh_diagnostics)
        diagnostics_row.addWidget(diagnostics_btn)
        status_layout.addLayout(diagnostics_row)
        integrity_row = QHBoxLayout()
        self.integrity_label = QLabel(t("status.integrity_unknown"))
        self.integrity_label.setWordWrap(True)
        integrity_row.addWidget(self.integrity_label, 1)
        integrity_btn = QPushButton(t("status.integrity_btn"))
        integrity_btn.clicked.connect(self._check_integrity)
        integrity_row.addWidget(integrity_btn)
        status_layout.addLayout(integrity_row)

        actions_row = QHBoxLayout()
        sync_now_btn = QPushButton(t("status.sync_now_btn"))
        sync_now_btn.setObjectName("primaryButton")
        sync_now_btn.setToolTip(t("status.sync_now_tooltip"))
        sync_now_btn.clicked.connect(self._sync_now)
        actions_row.addWidget(sync_now_btn)
        self.pause_btn = QPushButton(t("status.pause_btn"))
        self.pause_btn.setToolTip(t("status.pause_tooltip"))
        self.pause_btn.clicked.connect(self._toggle_pause)
        actions_row.addWidget(self.pause_btn)
        self.pause_for_btn = QPushButton(t("status.pause_for_btn"))
        self.pause_for_btn.setToolTip(t("status.pause_for_tooltip"))
        self.pause_for_btn.clicked.connect(self._pause_for)
        actions_row.addWidget(self.pause_for_btn)
        actions_row.addStretch(1)
        status_layout.addLayout(actions_row)
        self.sync_feedback_label = QLabel()
        self.sync_feedback_label.setWordWrap(True)
        self.sync_feedback_label.hide()
        status_layout.addWidget(self.sync_feedback_label)
        root.addWidget(status_box)

        # --- the NASBox folder ---
        folder_box = QGroupBox(t("status.folder_group_title"))
        folder_box.setObjectName("folderCard")
        folder_layout = QVBoxLayout(folder_box)
        self.folder_label = QLabel()
        self.folder_label.setWordWrap(True)
        folder_layout.addWidget(self.folder_label)
        folder_actions = QHBoxLayout()
        open_btn = QPushButton(t("status.open_folder_btn"))
        open_btn.setObjectName("secondaryButton")
        open_btn.setToolTip(t("status.open_folder_tooltip"))
        open_btn.clicked.connect(self._open_folder)
        folder_actions.addWidget(open_btn)
        relocate_btn = QPushButton(t("status.relocate_btn"))
        relocate_btn.setObjectName("secondaryButton")
        relocate_btn.setToolTip(t("status.relocate_tooltip"))
        relocate_btn.clicked.connect(self._relocate_folder)
        folder_actions.addWidget(relocate_btn)
        folder_actions.addStretch(1)
        folder_layout.addLayout(folder_actions)
        root.addWidget(folder_box)

        root.addStretch(1)

        self._refresh_pause_button()
        self._refresh_folder_label()

        self._timer = QTimer(self)
        self._timer.timeout.connect(self._tick_countdown)
        self._timer.start(1000)

    # --- actions ---

    def _sync_now(self) -> None:
        if self.cfg.is_paused():
            QMessageBox.information(
                self, t("status.paused_dialog_title"), t("status.paused_dialog_body"),
            )
            return
        self.engine.sync_now()
        self.scan_worker.wake()
        self.sync_feedback_label.setText(t("status.sync_requested"))
        self.sync_feedback_label.show()

    def _toggle_pause(self) -> None:
        if self.cfg.is_paused():
            self.cfg.resume()
        else:
            self.cfg.pause_indefinitely()
            self.engine.cancel_current_transfer()
        self.engine.wake()
        self._refresh_pause_button()

    def _pause_for(self) -> None:
        dlg = PauseForDialog(self)
        if dlg.exec():
            seconds = dlg.seconds()
            self.cfg.pause_for_seconds(seconds)
            self.engine.cancel_current_transfer()
            self.engine.wake()
            self._refresh_pause_button()

    def _refresh_pause_button(self) -> None:
        paused = self.cfg.is_paused()
        self.pause_btn.setText(t("status.resume_btn") if paused else t("status.pause_btn"))

    def _tick_countdown(self) -> None:
        remaining = self.cfg.pause_remaining_seconds()
        if remaining and remaining > 0:
            self._refresh_pause_button()

    def _open_folder(self) -> None:
        local_root = self.cfg.local_root()
        if not local_root:
            return
        QDesktopServices.openUrl(QUrl.fromLocalFile(local_root))

    def _relocate_folder(self) -> None:
        dlg = FolderSetupDialog(self, current=self.cfg.local_root(), relocating=True)
        if dlg.exec():
            chosen = dlg.chosen_path()
            try:
                initialize_local_root(chosen, str(self.cfg.get("repository_id") or ""))
            except RepositorySafetyError as exc:
                QMessageBox.warning(self, "Verifica cartella locale", str(exc))
                return
            self.cfg.set_local_root(chosen)
            self.engine.wake()
            self._refresh_folder_label()

    def _refresh_folder_label(self) -> None:
        local_root = self.cfg.local_root()
        self.folder_label.setText(local_root or t("status.folder_not_configured"))

    # --- signal from engine ---

    def on_status_changed(self, status: dict) -> None:
        paused = status.get("paused", False)
        remaining = status.get("pause_remaining")
        host = status.get("host")
        via_jump = status.get("via_jump", False)
        connected = status.get("connected", False)
        configured = status.get("configured", True)

        where = f"{host} {t('status.via_bastion')}" if via_jump else str(host)

        if not configured:
            text = t("status.not_configured_msg")
            state = "warning"
        elif not connected:
            text = t("status.nas_unreachable_msg")
            state = "error"
        elif paused and remaining:
            mins = int(remaining // 60)
            secs = int(remaining % 60)
            text = t("status.paused_countdown_msg", mins=f"{mins:02d}", secs=f"{secs:02d}", where=where)
            state = "paused"
        elif paused:
            text = t("status.paused_msg", where=where)
            state = "paused"
        else:
            text = t("status.active_msg", where=where)
            state = "active"

        self.status_label.setText(text)
        if self.status_label.property("state") != state:
            self.status_label.setProperty("state", state)
            self.status_label.style().unpolish(self.status_label)
            self.status_label.style().polish(self.status_label)
        self._refresh_pause_button()
        if not self._update_auto_checked and connected and configured and not paused:
            self._update_auto_checked = True
            QTimer.singleShot(10000, self._check_for_update)

    def on_hash_progress(self, done: int, total: int) -> None:
        if total <= 0 or done >= total:
            self.hash_progress_label.hide()
            return
        self.hash_progress_label.setText(t("status.hash_progress", done=done, total=total))
        self.hash_progress_label.show()

    def on_queue_updated(self, items) -> None:
        if not items:
            self.queue_label.setText(t("status.queue_empty"))
            return
        uploads = sum(1 for item in items if item.direction == "upload")
        downloads = sum(1 for item in items if item.direction == "download")
        deletes = sum(1 for item in items if item.direction.startswith("delete_"))
        self.queue_label.setText(t("status.queue_summary", uploads=uploads, downloads=downloads, deletes=deletes))

    def _refresh_diagnostics(self) -> None:
        conn = self.engine.connection
        if conn is None:
            self.diagnostics_label.setText(t("status.diagnostics_offline"))
            return
        self.diagnostics_label.setText(t("status.diagnostics_loading"))
        run_in_background(
            self, "_diagnostics_call", lambda: trash.fetch_remote_diagnostics(self.cfg, conn), self._on_diagnostics_done,
        )

    def _on_diagnostics_done(self, result, exc: Exception | None) -> None:
        if exc is not None:
            self.diagnostics_label.setText(t("status.diagnostics_failed", detail=str(exc)))
            return
        ok, values, detail = result
        if not ok:
            self.diagnostics_label.setText(t("status.diagnostics_failed", detail=detail))
            return
        try:
            free = human_size(int(values.get("SHARE_AVAILABLE_BYTES", "0")))
            trash_size = human_size(int(values.get("TRASH_DISK_BYTES", "0")))
            count = int(values.get("TRASH_FILE_COUNT", "0"))
        except ValueError:
            self.diagnostics_label.setText(t("status.diagnostics_failed", detail="risposta NAS non valida"))
            return
        self.diagnostics_label.setText(t("status.diagnostics_summary", free=free, trash=trash_size, count=count))

    def _check_integrity(self) -> None:
        conn = self.engine.connection
        if conn is None:
            self.integrity_label.setText(t("status.integrity_offline"))
            return
        self.integrity_label.setText(t("status.integrity_running"))
        run_in_background(self, "_integrity_call", lambda: rsync_ops.integrity_check(self.cfg, conn), self._on_integrity_done)

    def _on_integrity_done(self, result, exc: Exception | None) -> None:
        if exc is not None:
            self.integrity_label.setText(t("status.integrity_failed", detail=str(exc)))
            return
        paths = {item.path for item in result}
        if not paths:
            self.integrity_label.setText(t("status.integrity_ok"))
            return
        self.integrity_label.setText(t("status.integrity_different", count=len(paths)))

    # --- update check ---

    def _check_for_update(self) -> None:
        if self._update_check_busy:
            return
        self._update_check_busy = True
        self.update_available_label.setText(t("status.update_checking"))
        self.update_available_label.show()
        run_in_background(
            self, "_update_check_call",
            lambda: _update_check_worker(self.cfg),
            self._on_update_check_done,
        )

    def _on_update_check_done(self, result, exc: Exception | None) -> None:
        self._update_check_busy = False
        self._update_auto_checked = False
        self._update_candidate = None
        if exc is not None:
            self.update_available_label.setText(t("status.update_check_failed", detail=str(exc)))
            return
        candidate = result.get("candidate")
        if candidate is None:
            self.update_available_label.hide()
            return
        self._update_candidate = candidate
        self.update_available_label.setText(
            t("status.update_available", version=candidate.version)
        )
        self.update_available_label.setCursor(Qt.CursorShape.PointingHandCursor)
        self.update_available_label.mousePressEvent = lambda _e: self._install_update()
        self.update_available_label.show()

    def _install_update(self) -> None:
        candidate = getattr(self, '_update_candidate', None)
        if candidate is None:
            return
        answer = QMessageBox.question(
            self,
            t("status.update_confirm_title"),
            t("status.update_confirm_body", version=candidate.version),
            QMessageBox.StandardButton.Yes | QMessageBox.StandardButton.No,
            QMessageBox.StandardButton.Yes,
        )
        if answer != QMessageBox.StandardButton.Yes:
            return
        try:
            from pathlib import Path
            import os, sys
            source_root = candidate.materialize()
            current_root = Path(os.path.abspath(__file__)).resolve().parents[2]
            updater.install_update(source_root, current_root)
            candidate.cleanup()
            QMessageBox.information(self, t("status.update_restart_title"), t("status.update_restart_body"))
            os.execv(sys.executable, [sys.executable, str(current_root / "main.py"), *sys.argv[1:]])
        except Exception as exc:
            candidate.cleanup()
            QMessageBox.warning(self, t("status.update_failed_title"), str(exc))


def _update_check_worker(cfg: Config) -> dict:
    from pathlib import Path
    import os
    current_root = Path(os.path.abspath(__file__)).resolve().parents[2]
    candidate = updater.find_update(cfg, current_root, str(current_root / "main.py"))
    result: dict = {"candidate": None}
    if candidate is not None:
        result["candidate"] = candidate
    return result
