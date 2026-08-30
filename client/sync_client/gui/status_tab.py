from __future__ import annotations

import os
import sys
import time

from PyQt6.QtCore import Qt, QTimer, QUrl, pyqtSignal
from PyQt6.QtGui import QDesktopServices
from PyQt6.QtWidgets import (
    QGroupBox, QHBoxLayout, QLabel, QMessageBox, QPushButton, QSizePolicy,
    QScrollArea, QVBoxLayout, QWidget,
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

UPDATE_CHECK_INITIAL_DELAY_MS = 10_000
UPDATE_CHECK_INTERVAL_MS = 60 * 60 * 1000
RECENT_PROBLEM_SECONDS = 24 * 60 * 60
RECENT_EVENT_SCAN_LIMIT = 20_000
LOW_NAS_SPACE_BYTES = 5 * 1024 * 1024 * 1024
PROBLEM_ACTIONS = {"ERROR", "SAFETY_BLOCK", "JOURNAL_BLOCK"}


class StatusTab(QWidget):
    """The tab you actually look at day to day: is it working, is anything
    paused, and where's my stuff. Anything you only touch once in a while
    (NAS connection, bandwidth, retention...) lives in the Impostazioni tab
    instead -- keeping this one uncluttered is the whole point of it."""

    attention_action_requested = pyqtSignal(str)

    def __init__(
        self, cfg: Config, engine: SyncEngine, scan_worker: ScanWorker,
        parent=None, logger=None,
    ) -> None:
        super().__init__(parent)
        self.cfg = cfg
        self.engine = engine
        self.scan_worker = scan_worker
        self._update_check_busy = False
        self._update_auto_checked = False
        self._update_candidate = None
        self._status_known = False
        self._connected = False
        self._connected_observations = 0
        self._diagnostics_auto_loaded = False
        self._diagnostics_busy = False
        self._diagnostics_available = False
        self._nas_available_bytes: int | None = None
        self._queue_items = None
        self._scheduler_status: dict[str, object] = {}
        self._transfer_phases: dict[str, str] = {}
        self._current_paths: dict[str, str] = {}
        self._last_success: tuple[float, str] | None = None
        self._last_problem: tuple[float, str, str] | None = None
        self._recent_problem_events: dict[str, tuple[float, str]] = {}
        self.attention_problems: list[tuple[str, str, str]] = []

        if logger is not None:
            self._load_recent_events(logger.tail(limit=RECENT_EVENT_SCAN_LIMIT))

        outer = QVBoxLayout(self)
        outer.setContentsMargins(0, 0, 0, 0)
        scroll = QScrollArea()
        scroll.setWidgetResizable(True)
        scroll.setFrameShape(QScrollArea.Shape.NoFrame)
        content = QWidget()
        scroll.setWidget(content)
        outer.addWidget(scroll)
        root = QVBoxLayout(content)
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
        self.sync_state_label = QLabel(t("status.sync_state_starting"))
        self.sync_state_label.setWordWrap(True)
        status_layout.addWidget(self.sync_state_label)
        self.remote_lock_label = QLabel(t("status.remote_lock_free"))
        self.remote_lock_label.setWordWrap(True)
        status_layout.addWidget(self.remote_lock_label)
        self.watcher_label = QLabel(t("status.watcher_starting"))
        self.watcher_label.setObjectName("statusWatcher")
        self.watcher_label.setWordWrap(True)
        status_layout.addWidget(self.watcher_label)
        self.queue_label = QLabel(t("status.queue_unknown"))
        self.queue_label.setObjectName("statusQueue")
        status_layout.addWidget(self.queue_label)
        self.pending_label = QLabel(t("status.pending_empty"))
        self.pending_label.setWordWrap(True)
        status_layout.addWidget(self.pending_label)
        self.last_sync_label = QLabel(self._last_sync_text())
        self.last_sync_label.setWordWrap(True)
        status_layout.addWidget(self.last_sync_label)
        self.last_problem_label = QLabel(self._last_problem_text())
        self.last_problem_label.setWordWrap(True)
        status_layout.addWidget(self.last_problem_label)
        # Keep the update status on the same row as the version: showing a
        # separate label here changes the card height while the check runs.
        version_row = QHBoxLayout()
        self.version_label = QLabel(f"{APP_NAME} v{APP_VERSION}")
        self.version_label.setObjectName("statusVersion")
        self.version_label.setCursor(Qt.CursorShape.PointingHandCursor)
        self.version_label.setToolTip(t("status.version_tooltip"))
        self.version_label.mousePressEvent = lambda _e: self._check_for_update()
        version_row.addWidget(self.version_label)
        self.update_available_label = QLabel()
        self.update_available_label.setObjectName("statusUpdateAvailable")
        self.update_available_label.setWordWrap(False)
        self.update_available_label.setSizePolicy(QSizePolicy.Policy.Expanding, QSizePolicy.Policy.Fixed)
        self.update_available_label.hide()
        self.update_available_label.mousePressEvent = lambda _e: self._check_for_update()
        version_row.addWidget(self.update_available_label, 1)
        status_layout.addLayout(version_row)
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

        # Hidden when healthy: day-to-day users only see this card when there is
        # a concrete problem and every row points at the existing place to fix it.
        self.attention_box = QGroupBox(t("status.attention_title"))
        self.attention_box.setObjectName("attentionCard")
        self.attention_layout = QVBoxLayout(self.attention_box)
        root.addWidget(self.attention_box)
        self._refresh_attention()

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
        self._update_timer = QTimer(self)
        self._update_timer.setSingleShot(True)
        self._update_timer.timeout.connect(self._check_for_update)

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
        connection_configured = bool(
            configured and (self.cfg.get("nas_lan") or self.cfg.get("nas_wan"))
            and self.cfg.get("nas_user")
        )

        was_connected = self._connected
        self._status_known = True
        self._connected = bool(connected and connection_configured)
        if self._connected:
            self._connected_observations = self._connected_observations + 1 if was_connected else 1
        else:
            self._connected_observations = 0

        where = f"{host} {t('status.via_bastion')}" if via_jump else str(host)

        if not connection_configured:
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
            text = t("status.connected_msg", where=where)
            state = "active"

        self.status_label.setText(text)
        if self.status_label.property("state") != state:
            self.status_label.setProperty("state", state)
            self.status_label.style().unpolish(self.status_label)
            self.status_label.style().polish(self.status_label)
        self._refresh_pause_button()
        self._refresh_sync_state(paused, remaining)
        self._refresh_remote_lock(status)
        self._refresh_watcher(status)
        self._scheduler_status = status.get("scheduler") or {}
        self._refresh_queue_label()
        self._refresh_pending_summary(status)
        self._refresh_attention()
        if self._connected and not self._diagnostics_auto_loaded:
            self._diagnostics_auto_loaded = True
            self._refresh_diagnostics()
        if not self._update_auto_checked and connected and configured and not paused:
            self._update_auto_checked = True
            self._update_timer.start(UPDATE_CHECK_INITIAL_DELAY_MS)

    def on_hash_progress(self, done: int, total: int) -> None:
        if total <= 0 or done >= total:
            self.hash_progress_label.hide()
            return
        self.hash_progress_label.setText(t("status.hash_progress", done=done, total=total))
        self.hash_progress_label.show()

    def _refresh_remote_lock(self, status: dict) -> None:
        if not status.get("connected") or not status.get("server_lock_held"):
            self.remote_lock_label.setText(t("status.remote_lock_free"))
            return
        try:
            age_seconds = max(0, int(status.get("server_lock_age_seconds") or 0))
        except (TypeError, ValueError):
            age_seconds = 0
        minutes, seconds = divmod(age_seconds, 60)
        age = f"{minutes}m {seconds:02d}s" if minutes else f"{seconds}s"
        owner_id = str(status.get("server_lock_owner_id") or "")
        owner_host = str(status.get("server_lock_owner_host") or "")
        owner_pid = str(status.get("server_lock_owner_pid") or "")
        owner = owner_host or owner_id or owner_pid or "sconosciuto"
        if owner_host and owner_id:
            owner = f"{owner_host} / {owner_id}"
        self.remote_lock_label.setText(t("status.remote_lock_held", age=age, owner=owner))

    def _refresh_pending_summary(self, status: dict) -> None:
        summary = status.get("pending_summary") or {}
        try:
            count = int(summary.get("count") or 0)
            age_seconds = max(0, int(summary.get("oldest_age_seconds") or 0))
        except (AttributeError, TypeError, ValueError):
            self.pending_label.setText(t("status.pending_empty"))
            return
        if count <= 0:
            self.pending_label.setText(t("status.pending_empty"))
            return
        minutes, seconds = divmod(age_seconds, 60)
        age = f"{minutes}m {seconds:02d}s" if minutes else f"{seconds}s"
        reason = str(summary.get("last_reason") or "nessun motivo registrato")
        self.pending_label.setText(t("status.pending_summary", count=count, age=age, reason=reason))

    def _refresh_watcher(self, status: dict) -> None:
        mode = status.get("watcher_mode", "disabled")
        detail = self._compact_detail(status.get("watcher_detail", ""))
        if mode == "inotify":
            text = t("status.watcher_inotify")
            state = "active"
        elif mode == "polling":
            text = t("status.watcher_polling", detail=detail)
            state = "warning"
        elif mode == "starting":
            text = t("status.watcher_starting")
            state = "starting"
        else:
            text = t("status.watcher_disabled")
            state = "disabled"
        if detail and mode == "polling":
            text = t("status.watcher_polling_detail", detail=detail)
        self.watcher_label.setText(text)
        if self.watcher_label.property("state") != state:
            self.watcher_label.setProperty("state", state)
            self.watcher_label.style().unpolish(self.watcher_label)
            self.watcher_label.style().polish(self.watcher_label)

    def on_queue_updated(self, items) -> None:
        self._queue_items = list(items)
        self._refresh_queue_label()
        self._refresh_sync_state()

    def _refresh_queue_label(self) -> None:
        if not self._status_known or self._queue_items is None:
            self.queue_label.setText(t("status.queue_unknown"))
            return
        if not self._connected:
            self.queue_label.setText(t("status.queue_offline"))
            return
        items = self._queue_items or []
        uploads = sum(1 for item in items if item.direction in ("upload", "rename_remote"))
        downloads = sum(1 for item in items if item.direction == "download")
        deletes = sum(1 for item in items if item.direction.startswith("delete_"))
        scheduler = self._scheduler_status
        active = str(scheduler.get("active") or "")
        waiting = scheduler.get("waiting") or []
        waiting_count = len(waiting) if isinstance(waiting, list) else 0
        if not items and not active and not waiting_count:
            self.queue_label.setText(t("status.queue_empty"))
            return
        self.queue_label.setText(t(
            "status.queue_summary",
            uploads=uploads,
            downloads=downloads,
            deletes=deletes,
            active=active or "nessuna",
            scheduler_waiting=waiting_count,
        ))

    def on_transfer_preparing(self, direction: str) -> None:
        self._transfer_phases[direction] = "preparing"
        self._refresh_sync_state()

    def on_transfer_waiting_for_lock(self, direction: str) -> None:
        self._transfer_phases[direction] = "waiting"
        self._refresh_sync_state()

    def on_transfer_started(self, direction: str) -> None:
        self._transfer_phases[direction] = "active"
        self._refresh_sync_state()

    def on_transfer_item_started(self, direction: str, path: str, _size: int = 0) -> None:
        transfer_direction = "upload" if direction in ("upload", "delete_remote") else "download"
        self._transfer_phases[transfer_direction] = "active"
        self._current_paths[transfer_direction] = path
        self._refresh_sync_state()

    def on_transfer_item_done(self, direction: str, path: str) -> None:
        if self._queue_items is None:
            return
        self._queue_items = [
            item for item in self._queue_items
            if (item.direction, item.path) != (direction, path)
        ]
        self._refresh_queue_label()

    def on_transfer_finished(self, direction: str, ok: bool) -> None:
        self._transfer_phases.pop(direction, None)
        self._current_paths.pop(direction, None)
        if ok:
            self._last_success = (time.time(), direction)
            self.last_sync_label.setText(self._last_sync_text())
            self.sync_feedback_label.hide()
        self._refresh_sync_state()

    def _refresh_sync_state(self, paused: bool | None = None, remaining=None) -> None:
        if paused is None:
            paused = self.cfg.is_paused()
            remaining = self.cfg.pause_remaining_seconds()
        if not self._status_known:
            text = t("status.sync_state_starting")
        elif not self.cfg.is_configured():
            text = t("status.sync_state_not_configured")
        elif not self._connected:
            text = t("status.sync_state_offline")
        elif paused and remaining:
            text = t("status.sync_state_paused_timed")
        elif paused:
            text = t("status.sync_state_paused")
        elif self._transfer_phases:
            direction = next(iter(self._transfer_phases))
            phase = self._transfer_phases[direction]
            direction_text = t(f"status.direction_{direction}")
            path = self._current_paths.get(direction, "")
            if path:
                text = t("status.sync_state_file", direction=direction_text, path=path)
            else:
                text = t(f"status.sync_state_{phase}", direction=direction_text)
        elif self._queue_items:
            text = t("status.sync_state_pending", count=len(self._queue_items))
        else:
            text = t("status.sync_state_ready")
        self.sync_state_label.setText(text)

    def on_transfer_lock_unavailable(self, direction: str, detail: str) -> None:
        self._transfer_phases.pop(direction, None)
        self._current_paths.pop(direction, None)
        normalized = str(detail).lower()
        if "altro pc" in normalized or "another pc" in normalized:
            feedback = t(
                "status.sync_deferred_lock",
                direction=t(f"status.direction_{direction}"),
            )
        else:
            feedback = t("status.sync_lock_error", detail=self._compact_detail(detail))
        self.sync_feedback_label.setText(feedback)
        self.sync_feedback_label.show()
        self._refresh_sync_state()

    def on_log_event(self, action: str, _path: str, detail: str) -> None:
        if action not in PROBLEM_ACTIONS:
            return
        now = time.time()
        detail = detail or t("status.problem_no_detail")
        self._last_problem = (now, action, detail)
        self._recent_problem_events[action] = (now, detail)
        self.last_problem_label.setText(self._last_problem_text())
        self._refresh_attention()

    def _load_recent_events(self, events) -> None:
        for event in events:
            if event.action in ("UPLOAD", "DELETE_REMOTE"):
                candidate = (event.ts, "upload")
                if self._last_success is None or event.ts > self._last_success[0]:
                    self._last_success = candidate
            elif event.action in ("DOWNLOAD", "DELETE_LOCAL"):
                candidate = (event.ts, "download")
                if self._last_success is None or event.ts > self._last_success[0]:
                    self._last_success = candidate
            if event.action in PROBLEM_ACTIONS:
                detail = event.detail or t("status.problem_no_detail")
                self._recent_problem_events[event.action] = (event.ts, detail)
                if self._last_problem is None or event.ts > self._last_problem[0]:
                    self._last_problem = (event.ts, event.action, detail)

    def _last_sync_text(self) -> str:
        if self._last_success is None:
            return t("status.last_sync_never")
        timestamp, direction = self._last_success
        return t(
            "status.last_sync",
            time=time.strftime("%Y-%m-%d %H:%M:%S", time.localtime(timestamp)),
            direction=t(f"status.direction_{direction}"),
        )

    def _last_problem_text(self) -> str:
        if self._last_problem is None:
            return t("status.last_problem_none")
        timestamp, action, detail = self._last_problem
        return t(
            "status.last_problem",
            time=time.strftime("%Y-%m-%d %H:%M:%S", time.localtime(timestamp)),
            action=self._problem_category(action, detail),
            detail=self._compact_detail(detail),
        )

    @staticmethod
    def _problem_category(action: str, detail: str) -> str:
        if action in ("SAFETY_BLOCK", "JOURNAL_BLOCK"):
            return t(f"status.problem_category_{action.lower()}")
        normalized = str(detail).lower()
        connection_terms = (
            "ssh", "timeout", "timed out", "connection refused", "connection reset",
            "no route to host", "host key", "nome host", "connessione",
        )
        if any(term in normalized for term in connection_terms):
            return t("status.problem_category_connection")
        if action == "ERROR":
            return t("status.problem_category_transfer")
        return t("status.problem_category_generic", action=action)

    @staticmethod
    def _compact_detail(detail: str) -> str:
        compact = " ".join(str(detail).split())
        return compact if len(compact) <= 180 else compact[:177] + "..."

    def _refresh_attention(self) -> None:
        problems = self._collect_problems()
        if problems == self.attention_problems:
            self.attention_box.setVisible(bool(problems))
            return
        self.attention_problems = problems
        while self.attention_layout.count():
            item = self.attention_layout.takeAt(0)
            if item.widget() is not None:
                item.widget().deleteLater()
        for problem_id, message, action in problems:
            row = QWidget(self.attention_box)
            row.setObjectName(f"attention_{problem_id}")
            layout = QHBoxLayout(row)
            layout.setContentsMargins(0, 0, 0, 0)
            label = QLabel(message)
            label.setWordWrap(True)
            layout.addWidget(label, 1)
            button = QPushButton(t(f"status.attention_action_{action}"))
            button.clicked.connect(
                lambda _checked=False, destination=action: self.attention_action_requested.emit(destination)
            )
            layout.addWidget(button)
            self.attention_layout.addWidget(row)
        self.attention_box.setVisible(bool(problems))

    def _collect_problems(self) -> list[tuple[str, str, str]]:
        if not self._status_known:
            return []
        problems: list[tuple[str, str, str]] = []
        missing = []
        if not self.cfg.local_root():
            missing.append(t("status.config_local_folder"))
        if not (self.cfg.get("nas_lan") or self.cfg.get("nas_wan")):
            missing.append(t("status.config_nas_host"))
        if not self.cfg.get("nas_user"):
            missing.append(t("status.config_nas_user"))
        if missing:
            problems.append(("configuration", t("status.problem_config", fields=", ".join(missing)), "settings"))
        elif not self._connected:
            problems.append(("unreachable", t("status.problem_unreachable"), "settings"))

        if not missing and not self.cfg.get("ssh_host_key_pinned"):
            problems.append(("host_keys", t("status.problem_host_keys"), "settings"))

        remote_state_known = self._connected and self._connected_observations >= 2
        if remote_state_known and (
            not self.cfg.get("remote_repository_ready") or not self.cfg.get("repository_id")
        ):
            problems.append(("repository", t("status.problem_repository"), "settings"))
        repository_verified = bool(
            self.cfg.get("remote_repository_ready") and self.cfg.get("repository_id")
        )
        if remote_state_known and repository_verified and not self.cfg.get("remote_journal_ready"):
            problems.append(("journal_ready", t("status.problem_journal_unavailable"), "settings"))

        journal_error = str(self.cfg.get("journal_error") or "").strip()
        if journal_error:
            problems.append((
                "journal_error",
                t("status.problem_journal", detail=self._compact_detail(journal_error)),
                "log",
            ))

        cutoff = time.time() - RECENT_PROBLEM_SECONDS
        for action in ("SAFETY_BLOCK", "JOURNAL_BLOCK", "ERROR"):
            event = self._recent_problem_events.get(action)
            if event is None or event[0] < cutoff:
                continue
            # A persisted journal error already carries the same actionable fact.
            if action == "JOURNAL_BLOCK" and journal_error:
                continue
            problems.append((
                f"event_{action.lower()}",
                t(
                    "status.problem_recent_event",
                    action=self._problem_category(action, event[1]),
                    detail=self._compact_detail(event[1]),
                ),
                "log",
            ))

        if (
            self._diagnostics_available and self._nas_available_bytes is not None
            and self._nas_available_bytes < LOW_NAS_SPACE_BYTES
        ):
            problems.append((
                "low_space",
                t("status.problem_low_space", free=human_size(self._nas_available_bytes)),
                "history",
            ))
        return problems

    def _refresh_diagnostics(self) -> None:
        if self._diagnostics_busy:
            return
        conn = self.engine.connection
        if conn is None:
            self.diagnostics_label.setText(t("status.diagnostics_offline"))
            return
        self._diagnostics_busy = True
        self.diagnostics_label.setText(t("status.diagnostics_loading"))
        run_in_background(
            self, "_diagnostics_call", lambda: trash.fetch_remote_diagnostics(self.cfg, conn), self._on_diagnostics_done,
        )

    def _on_diagnostics_done(self, result, exc: Exception | None) -> None:
        self._diagnostics_busy = False
        self._diagnostics_available = False
        self._nas_available_bytes = None
        if exc is not None:
            self.diagnostics_label.setText(t("status.diagnostics_failed", detail=str(exc)))
            self._refresh_attention()
            return
        ok, values, detail = result
        if not ok:
            self.diagnostics_label.setText(t("status.diagnostics_failed", detail=detail))
            self._refresh_attention()
            return
        try:
            available_bytes = int(values.get("SHARE_AVAILABLE_BYTES", "0"))
            free = human_size(available_bytes)
            trash_size = human_size(int(values.get("TRASH_DISK_BYTES", "0")))
            count = int(values.get("TRASH_FILE_COUNT", "0"))
        except (TypeError, ValueError):
            self.diagnostics_label.setText(t("status.diagnostics_failed", detail=t("status.diagnostics_invalid")))
            self._refresh_attention()
            return
        self._diagnostics_available = True
        self._nas_available_bytes = available_bytes
        self.diagnostics_label.setText(t("status.diagnostics_summary", free=free, trash=trash_size, count=count))
        self._refresh_attention()

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
        self._update_timer.stop()
        self._update_auto_checked = True
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
        self._update_candidate = None
        # Automatic checks are deliberately infrequent. Manual clicks still
        # run immediately, but never cause the status loop to retry every tick.
        self._update_timer.start(UPDATE_CHECK_INTERVAL_MS)
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
