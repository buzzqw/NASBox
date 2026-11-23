from __future__ import annotations

import os
import sys
import time
from datetime import datetime, timedelta

from PyQt6.QtCore import Qt, QTimer, QUrl, pyqtSignal
from PyQt6.QtGui import QDesktopServices, QKeySequence, QShortcut
from PyQt6.QtWidgets import (
    QBoxLayout, QGridLayout, QGroupBox, QHBoxLayout, QLabel, QMessageBox, QProgressBar, QPushButton,
    QSizePolicy, QScrollArea, QToolButton, QVBoxLayout, QWidget,
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
from .style import SHORTCUT_SYNC_NOW, set_status_text

UPDATE_CHECK_INITIAL_DELAY_MS = 10_000
UPDATE_CHECK_INTERVAL_MS = 60 * 60 * 1000
RECENT_PROBLEM_SECONDS = 24 * 60 * 60
# A generic transfer error is usually transient and already retried by the
# worker, so it must not keep the "Richiede attenzione" card up for a whole
# day after the sync has healed by itself. Safety and journal blocks are real
# decisions the user has to see, so they keep the long window.
RECENT_TRANSIENT_SECONDS = 15 * 60
RECENT_EVENT_SCAN_LIMIT = 20_000


def _clock_after(seconds) -> str:
    """HH:MM wall-clock time `seconds` from now, for schedule messaging."""
    try:
        moment = datetime.now() + timedelta(seconds=max(int(seconds), 0))
    except (TypeError, ValueError):
        return "--:--"
    return moment.strftime("%H:%M")
LOW_NAS_SPACE_BYTES = 5 * 1024 * 1024 * 1024
PROBLEM_ACTIONS = {"ERROR", "SAFETY_BLOCK", "JOURNAL_BLOCK"}


class StatusTab(QWidget):
    """The tab you actually look at day to day: is it working, is anything
    paused, and where's my stuff. Anything you only touch once in a while
    (NAS connection, bandwidth, retention...) lives in the Impostazioni tab
    instead -- keeping this one uncluttered is the whole point of it."""

    attention_action_requested = pyqtSignal(str)
    assurance_status = pyqtSignal(str, str)  # headline, proof

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
        # Deadline (monotonic) of the next automatic attempt after the NAS lock
        # was busy, per direction. A plain "deferred" line left the user staring
        # at an error with nothing moving; the countdown proves the client is
        # still working and that the queued files are not lost.
        self._retry_deadlines: dict[str, float] = {}
        self._batch_totals: dict[str, int] = {}
        self._batch_done: dict[str, int] = {}
        self._current_speed: float = 0.0
        self._current_percent: int = 0
        self._remote_lock_held: bool = False
        self._remote_lock_owner: str = ""
        self._remote_lock_age: str = ""
        self._last_success: tuple[float, str] | None = None
        self._last_problem: tuple[float, str, str] | None = None
        self._recent_problem_events: dict[str, tuple[float, str]] = {}
        self.attention_problems: list[tuple[str, str, str]] = []
        self._conflict_count = 0
        # True once the card rows have been stacked vertically because the
        # window became too narrow for two ~360px columns side by side.
        self._single_column = False

        if logger is not None:
            self._load_recent_events(logger.tail(limit=RECENT_EVENT_SCAN_LIMIT))

        outer = QVBoxLayout(self)
        outer.setContentsMargins(0, 0, 0, 0)
        outer.setSpacing(6)
        scroll = QScrollArea()
        scroll.setWidgetResizable(True)
        scroll.setFrameShape(QScrollArea.Shape.NoFrame)
        content = QWidget()
        scroll.setWidget(content)
        root = QVBoxLayout(content)
        root.setContentsMargins(4, 4, 4, 4)
        root.setSpacing(6)

        # --- sync assurance: answer "is everything synced?" before anything else ---
        self.assurance_box = QGroupBox(t("assurance.title"))
        self.assurance_box.setObjectName("assuranceCard")
        assurance_layout = QVBoxLayout(self.assurance_box)
        assurance_layout.setContentsMargins(6, 4, 6, 4)
        assurance_layout.setSpacing(4)
        self.assurance_headline = QLabel(t("assurance.checking"))
        self.assurance_headline.setObjectName("assuranceHeadline")
        self.assurance_headline.setAccessibleName(t("a11y.assurance_headline"))
        self.assurance_headline.setWordWrap(True)
        assurance_layout.addWidget(self.assurance_headline)
        self.assurance_proof = QLabel(t("assurance.waiting_proof"))
        self.assurance_proof.setObjectName("assuranceProof")
        self.assurance_proof.setWordWrap(True)
        assurance_layout.addWidget(self.assurance_proof)
        self.assurance_checks = QLabel("")
        self.assurance_checks.setObjectName("assuranceChecks")
        self.assurance_checks.setWordWrap(True)
        assurance_layout.addWidget(self.assurance_checks)
        self.assurance_activity = QLabel(t("assurance.activity_empty"))
        self.assurance_activity.setObjectName("assuranceActivity")
        self.assurance_activity.setWordWrap(True)
        assurance_layout.addWidget(self.assurance_activity)
        self.audit_row = QHBoxLayout()
        self.audit_label = QLabel(t("audit.never"))
        self.audit_label.setObjectName("auditSummary")
        self.audit_label.setWordWrap(True)
        self.audit_label.setAccessibleName(t("a11y.audit_summary"))
        self.audit_row.addWidget(self.audit_label, 1)
        self.audit_btn = QPushButton(t("audit.run_now"))
        self.audit_btn.setObjectName("auditRunNow")
        self.audit_btn.setToolTip(t("audit.run_now_tooltip"))
        self.audit_btn.clicked.connect(self._run_audit)
        self.audit_row.addWidget(self.audit_btn)
        assurance_layout.addLayout(self.audit_row)
        self.audit_progress = QProgressBar()
        self.audit_progress.setObjectName("auditProgress")
        self.audit_progress.setRange(0, 100)
        self.audit_progress.hide()
        assurance_layout.addWidget(self.audit_progress)
        assurance_grid = QGridLayout()
        assurance_grid.setContentsMargins(0, 0, 0, 0)
        assurance_grid.setSpacing(4)
        self.assurance_stats: dict[str, QLabel] = {}
        for index, key in enumerate(("tracked", "pending", "queue", "conflicts", "revision", "lastcheck")):
            caption = QLabel(t(f"assurance.stat_{key}"))
            value = QLabel(t("assurance.unknown_short"))
            value.setObjectName(f"assuranceStat_{key}")
            assurance_grid.addWidget(caption, index // 3, (index % 3) * 2)
            assurance_grid.addWidget(value, index // 3, (index % 3) * 2 + 1)
            self.assurance_stats[key] = value
        assurance_layout.addLayout(assurance_grid)
        self.verify_now_btn = QPushButton(t("assurance.verify_now"))
        self.verify_now_btn.setObjectName("secondaryButton")
        self.verify_now_btn.setToolTip(t("assurance.verify_now_tooltip"))
        self.verify_now_btn.clicked.connect(self._verify_now)
        assurance_layout.addWidget(self.verify_now_btn)
        # The assurance card no longer spans the tab as a fixed header: it
        # shares a half-width row with the status card, added to the scroll
        # content once status_box has been built (see _assurance_columns).
        outer.addWidget(scroll)

        # --- status ---
        status_box = QGroupBox(t("status.group_title"))
        status_box.setObjectName("statusCard")
        status_layout = QVBoxLayout(status_box)
        status_layout.setContentsMargins(6, 4, 6, 4)
        status_layout.setSpacing(4)
        self.status_label = QLabel(t("status.starting"))
        self.status_label.setObjectName("statusMessage")
        self.status_label.setAccessibleName(t("a11y.status_message"))
        self.status_label.setWordWrap(True)
        self.status_label.setProperty("state", "starting")
        status_layout.addWidget(self.status_label)
        self.sync_state_label = QLabel(t("status.sync_state_starting"))
        self.sync_state_label.setAccessibleName(t("a11y.sync_state"))
        self.sync_state_label.setWordWrap(True)
        status_layout.addWidget(self.sync_state_label)
        self.sync_progress_bar = QProgressBar()
        self.sync_progress_bar.setAccessibleName(t("a11y.sync_progress"))
        self.sync_progress_bar.setRange(0, 100)
        self.sync_progress_bar.setTextVisible(True)
        self.sync_progress_bar.hide()
        status_layout.addWidget(self.sync_progress_bar)
        # The bar above follows the current file, which on an import of
        # thousands of small files jumps 0 -> 100 for each of them and looks
        # frozen. This one moves once per completed file across the whole batch.
        self.batch_progress_bar = QProgressBar()
        self.batch_progress_bar.setAccessibleName(t("a11y.batch_progress"))
        self.batch_progress_bar.setRange(0, 100)
        self.batch_progress_bar.setTextVisible(True)
        self.batch_progress_bar.hide()
        status_layout.addWidget(self.batch_progress_bar)
        self.conflict_stat_label = QToolButton()
        self.conflict_stat_label.setObjectName("statusConflictStat")
        self.conflict_stat_label.setToolButtonStyle(Qt.ToolButtonStyle.ToolButtonTextOnly)
        self.conflict_stat_label.setAccessibleName(t("a11y.conflict_badge"))
        self.conflict_stat_label.setToolTip(t("status.conflict_stat_tooltip"))
        self.conflict_stat_label.hide()
        self.conflict_stat_label.clicked.connect(
            lambda: self.attention_action_requested.emit("conflicts")
        )
        status_layout.addWidget(self.conflict_stat_label)
        self.remote_lock_label = QLabel(t("status.remote_lock_free"))
        self.remote_lock_label.setWordWrap(True)
        status_layout.addWidget(self.remote_lock_label)
        self.devices_label = QLabel(t("status.devices_unknown"))
        self.devices_label.setObjectName("statusDevices")
        self.devices_label.setWordWrap(True)
        self.devices_label.setAccessibleName(t("a11y.devices_label"))
        self._last_devices_refresh = 0.0
        status_layout.addWidget(self.devices_label)
        self.confirm_deletes_btn = QToolButton()
        self.confirm_deletes_btn.setObjectName("confirmDeletes")
        self.confirm_deletes_btn.setText(t("status.confirm_deletes_btn"))
        self.confirm_deletes_btn.setToolTip(t("status.confirm_deletes_tooltip"))
        self.confirm_deletes_btn.setAccessibleName(t("a11y.confirm_deletes_btn"))
        self.confirm_deletes_btn.clicked.connect(self._confirm_deletions)
        status_layout.addWidget(self.confirm_deletes_btn)
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

        # Technical diagnostics are useful, but they should not dominate the
        # daily view. Keep them one click away and remember the last choice.
        self.details_toggle = QPushButton(t("status.details_show"))
        self.details_toggle.setObjectName("secondaryButton")
        self.details_toggle.setAccessibleName(t("a11y.details_toggle"))
        self.details_toggle.setToolTip(t("status.details_toggle_tooltip"))
        self.details_toggle.setCheckable(True)
        self.details_toggle.toggled.connect(self._toggle_details)
        status_layout.addWidget(self.details_toggle)
        self.details_widget = QWidget()
        details_layout = QVBoxLayout(self.details_widget)
        details_layout.setContentsMargins(0, 0, 0, 0)
        for technical in (
            self.remote_lock_label, self.watcher_label, self.queue_label, self.pending_label,
            self.last_sync_label, self.last_problem_label,
        ):
            status_layout.removeWidget(technical)
            details_layout.addWidget(technical)
        self.details_widget.hide()
        status_layout.addWidget(self.details_widget)
        # Keep the update status on the same row as the version: showing a
        # separate label here changes the card height while the check runs.
        version_row = QHBoxLayout()
        version_row.setContentsMargins(0, 0, 0, 0)
        version_row.setSpacing(4)
        self.version_label = QToolButton()
        self.version_label.setText(f"{APP_NAME} v{APP_VERSION}")
        self.version_label.setObjectName("statusVersion")
        self.version_label.setToolButtonStyle(Qt.ToolButtonStyle.ToolButtonTextOnly)
        self.version_label.setAccessibleName(t("a11y.version_badge"))
        self.version_label.setToolTip(t("status.version_tooltip"))
        self.version_label.clicked.connect(self._check_for_update)
        version_row.addWidget(self.version_label)
        self.update_available_label = QToolButton()
        self.update_available_label.setObjectName("statusUpdateAvailable")
        self.update_available_label.setToolButtonStyle(Qt.ToolButtonStyle.ToolButtonTextOnly)
        self.update_available_label.setAccessibleName(t("a11y.update_status"))
        self.update_available_label.setToolTip(t("status.version_tooltip"))
        self.update_available_label.setSizePolicy(
            QSizePolicy.Policy.Expanding, QSizePolicy.Policy.Fixed
        )
        self.update_available_label.hide()
        self.update_available_label.clicked.connect(self._check_for_client_update_clicked)
        version_row.addWidget(self.update_available_label, 1)
        status_layout.addLayout(version_row)
        # Shown only while a large first-time batch is being checksummed locally
        # (see PushWorker.hash_progress) -- otherwise this phase used to look
        # frozen for as long as it took, with the rest of the status area
        # holding stale text from before the sync started.
        self.hash_progress_label = QLabel()
        self.hash_progress_label.setObjectName("statusHashProgress")
        self.hash_progress_label.setAccessibleName(t("a11y.hash_progress"))
        self.hash_progress_label.hide()
        status_layout.addWidget(self.hash_progress_label)
        diagnostics_row = QHBoxLayout()
        diagnostics_row.setContentsMargins(0, 0, 0, 0)
        diagnostics_row.setSpacing(4)
        self.diagnostics_label = QLabel(t("status.diagnostics_unknown"))
        self.diagnostics_label.setAccessibleName(t("a11y.diagnostics_status"))
        self.diagnostics_label.setWordWrap(True)
        diagnostics_row.addWidget(self.diagnostics_label, 1)
        diagnostics_btn = QPushButton(t("status.diagnostics_refresh_btn"))
        diagnostics_btn.clicked.connect(self._refresh_diagnostics)
        diagnostics_row.addWidget(diagnostics_btn)
        status_layout.addLayout(diagnostics_row)
        integrity_row = QHBoxLayout()
        integrity_row.setContentsMargins(0, 0, 0, 0)
        integrity_row.setSpacing(4)
        self.integrity_label = QLabel(t("status.integrity_unknown"))
        self.integrity_label.setAccessibleName(t("a11y.integrity_status"))
        self.integrity_label.setWordWrap(True)
        integrity_row.addWidget(self.integrity_label, 1)
        integrity_btn = QPushButton(t("status.integrity_btn"))
        integrity_btn.clicked.connect(self._check_integrity)
        integrity_row.addWidget(integrity_btn)
        status_layout.addLayout(integrity_row)

        actions_row = QHBoxLayout()
        actions_row.setContentsMargins(0, 0, 0, 0)
        actions_row.setSpacing(4)
        self.sync_now_btn = QPushButton(t("status.sync_now_btn"))
        self.sync_now_btn.setObjectName("primaryButton")
        self.sync_now_btn.setAccessibleName(t("status.sync_now_btn"))
        self.sync_now_btn.setToolTip(t("status.sync_now_tooltip"))
        # Keyboard access for the most common action; the shortcut text is
        # included in the tooltip so it is discoverable without a menu.
        self.sync_shortcut = QShortcut(QKeySequence(SHORTCUT_SYNC_NOW), self)
        self.sync_shortcut.activated.connect(self._sync_now)
        self.sync_now_btn.setToolTip(f"{t('status.sync_now_tooltip')} ({SHORTCUT_SYNC_NOW})")
        self.sync_now_btn.clicked.connect(self._sync_now)
        actions_row.addWidget(self.sync_now_btn)
        self.pause_btn = QPushButton(t("status.pause_btn"))
        self.pause_btn.setAccessibleName(t("status.pause_btn"))
        self.pause_btn.setToolTip(t("status.pause_tooltip"))
        self.pause_btn.clicked.connect(self._toggle_pause)
        actions_row.addWidget(self.pause_btn)
        self.pause_for_btn = QPushButton(t("status.pause_for_btn"))
        self.pause_for_btn.setAccessibleName(t("status.pause_for_btn"))
        self.pause_for_btn.setToolTip(t("status.pause_for_tooltip"))
        self.pause_for_btn.clicked.connect(self._pause_for)
        actions_row.addWidget(self.pause_for_btn)
        actions_row.addStretch(1)
        status_layout.addLayout(actions_row)
        self.sync_feedback_label = QLabel()
        self.sync_feedback_label.setAccessibleName(t("a11y.sync_feedback"))
        self.sync_feedback_label.setWordWrap(True)
        self.sync_feedback_label.hide()
        status_layout.addWidget(self.sync_feedback_label)
        self.status_box = status_box

        # --- assurance + status: 50/50 row at the top of the scroll content ---
        # The "is everything synced?" answer and the live status now sit side
        # by side instead of assurance taking a full-width fixed header. Both
        # ask for ~360px so text and buttons never get squeezed; resizeEvent
        # stacks them when the window is too narrow for two columns.
        self._assurance_columns = QHBoxLayout()
        self._assurance_columns.setContentsMargins(0, 0, 0, 0)
        self._assurance_columns.setSpacing(6)
        for card in (self.assurance_box, self.status_box):
            card.setSizePolicy(QSizePolicy.Policy.Expanding, QSizePolicy.Policy.Preferred)
            card.setMinimumWidth(360)
            self._assurance_columns.addWidget(card, 1)
        root.addLayout(self._assurance_columns)

        # --- NAS status & overview ---
        nas_box = QGroupBox(t("status.nas_overview_title"))
        nas_box.setObjectName("nasStatusBox")
        nas_layout = QVBoxLayout(nas_box)
        nas_layout.setContentsMargins(6, 4, 6, 4)
        nas_layout.setSpacing(4)

        self.nas_host_label = QLabel(t("status.nas_connection_title"))
        self.nas_host_label.setAccessibleName(t("a11y.nas_connection"))
        self.nas_host_label.setWordWrap(True)
        nas_layout.addWidget(self.nas_host_label)

        self.nas_activity_label = QLabel(t("status.nas_activity_idle"))
        self.nas_activity_label.setAccessibleName(t("a11y.nas_activity"))
        self.nas_activity_label.setWordWrap(True)
        nas_layout.addWidget(self.nas_activity_label)

        self.nas_storage_label = QLabel(t("status.nas_storage_calculating"))
        self.nas_storage_label.setAccessibleName(t("a11y.nas_storage"))
        self.nas_storage_label.setWordWrap(True)
        nas_layout.addWidget(self.nas_storage_label)

        self.nas_storage_bar = QProgressBar()
        self.nas_storage_bar.setAccessibleName(t("a11y.nas_storage_bar"))
        self.nas_storage_bar.setRange(0, 100)
        self.nas_storage_bar.setTextVisible(True)
        self.nas_storage_bar.setValue(0)
        nas_layout.addWidget(self.nas_storage_bar)

        # The NAS overview used to share this row with the status card; that
        # card now sits next to the assurance card in _assurance_columns, so a
        # spacer keeps the overview at the same half-width column instead of
        # letting a single card stretch across the whole tab.
        self._status_columns = QHBoxLayout()
        self._status_columns.setContentsMargins(0, 0, 0, 0)
        self._status_columns.setSpacing(6)
        nas_box.setSizePolicy(QSizePolicy.Policy.Expanding, QSizePolicy.Policy.Preferred)
        nas_box.setMinimumWidth(360)
        self._status_columns.addWidget(nas_box, 1)
        self._status_columns.addStretch(1)
        root.addLayout(self._status_columns)

        # Hidden when healthy: day-to-day users only see this card when there is
        # a concrete problem and every row points at the existing place to fix it.
        self.attention_box = QGroupBox(t("status.attention_title"))
        self.attention_box.setObjectName("attentionCard")
        self.attention_layout = QVBoxLayout(self.attention_box)
        self.attention_layout.setContentsMargins(6, 4, 6, 4)
        self.attention_layout.setSpacing(4)

        # --- the NASBox folder ---
        folder_box = QGroupBox(t("status.folder_group_title"))
        folder_box.setObjectName("folderCard")
        folder_layout = QVBoxLayout(folder_box)
        folder_layout.setContentsMargins(6, 4, 6, 4)
        folder_layout.setSpacing(4)
        self.folder_label = QLabel()
        self.folder_label.setAccessibleName(t("a11y.folder_path"))
        self.folder_label.setWordWrap(True)
        folder_layout.addWidget(self.folder_label)
        folder_actions = QHBoxLayout()
        folder_actions.setContentsMargins(0, 0, 0, 0)
        folder_actions.setSpacing(4)
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

        # Second row: actionable problems next to the folder card. The
        # attention card hides itself when healthy, so refresh it only after
        # both cards are parented to the shared row layout.
        self._secondary_columns = QHBoxLayout()
        self._secondary_columns.setContentsMargins(0, 0, 0, 0)
        self._secondary_columns.setSpacing(6)
        for card in (self.attention_box, folder_box):
            card.setSizePolicy(QSizePolicy.Policy.Expanding, QSizePolicy.Policy.Preferred)
            card.setMinimumWidth(360)
            self._secondary_columns.addWidget(card, 1)
        root.addLayout(self._secondary_columns)
        self._refresh_attention()

        root.addStretch(1)

        self._refresh_pause_button()
        self._refresh_folder_label()

        self._timer = QTimer(self)
        self._timer.timeout.connect(self._tick_countdown)
        self._timer.start(1000)
        self._update_timer = QTimer(self)
        self._update_timer.setSingleShot(True)
        self._update_timer.timeout.connect(self._check_for_update)

    # --- layout ---

    def resizeEvent(self, event) -> None:
        super().resizeEvent(event)
        self._update_column_layout(event.size().width())

    def _update_column_layout(self, width: int) -> None:
        """Keep two columns while there is room, stack them when narrow.

        QHBoxLayout does not reflow on its own: without this the two 360px
        cards would squeeze below their minimum (or force a horizontal
        scrollbar) as soon as the window shrinks.
        """
        single_column = width < 780
        if single_column == self._single_column:
            return
        self._single_column = single_column
        direction = (
            QBoxLayout.Direction.TopToBottom if single_column
            else QBoxLayout.Direction.LeftToRight
        )
        for row in (self._assurance_columns, self._status_columns, self._secondary_columns):
            row.setDirection(direction)

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
        if self.cfg.is_manually_paused():
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

    def _toggle_details(self, visible: bool) -> None:
        self.details_widget.setVisible(visible)
        self.details_toggle.setText(
            t("status.details_hide") if visible else t("status.details_show")
        )

    def on_conflict_count(self, count: int) -> None:
        try:
            self._conflict_count = max(0, int(count))
        except (TypeError, ValueError):
            self._conflict_count = 0
        if self._conflict_count:
            self.conflict_stat_label.setText(
                t("status.conflict_stat", count=self._conflict_count)
            )
            self.conflict_stat_label.show()
        else:
            self.conflict_stat_label.hide()
        self._refresh_attention()

    def _refresh_pause_button(self) -> None:
        # The toggle reflects the *manual* pause only: a recurring schedule must
        # not make the button claim the user paused (and then "resume" nothing).
        paused = self.cfg.is_manually_paused()
        self.pause_btn.setText(t("status.resume_btn") if paused else t("status.pause_btn"))

    def _tick_countdown(self) -> None:
        remaining = self.cfg.pause_remaining_seconds()
        if remaining and remaining > 0:
            self._refresh_pause_button()
        # A pending retry countdown has to tick down on its own: without this
        # the line frozen at "riprovo tra 43s" looked exactly like a stall.
        if self._retry_deadlines:
            self._refresh_sync_state()

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
                QMessageBox.warning(self, t("status.folder_check_title"), str(exc))
                return
            self.cfg.set_local_root(chosen)
            self.engine.wake()
            self._refresh_folder_label()

    def _refresh_folder_label(self) -> None:
        local_root = self.cfg.local_root()
        self.folder_label.setText(local_root or t("status.folder_not_configured"))

    # --- signal from engine ---

    def _run_audit(self) -> None:
        self.audit_btn.setEnabled(False)
        if not self.engine.run_full_audit():
            self.audit_label.setText(t("audit.busy"))
            QTimer.singleShot(3000, lambda: self.audit_btn.setEnabled(True))
            return
        self.audit_label.setText(t("audit.running"))
        self.audit_progress.show()
        QTimer.singleShot(1000, lambda: self.audit_btn.setEnabled(True))

    def _refresh_audit(self, data: dict) -> None:
        if not isinstance(data, dict) or not data:
            return
        if data.get("running"):
            done, total = data.get("progress_done", 0), data.get("progress_total", 0)
            self.audit_progress.setRange(0, max(1, total))
            self.audit_progress.setValue(done)
            self.audit_progress.show()
            self.audit_label.setText(t("audit.running"))
            return
        self.audit_progress.hide()
        last = data.get("last")
        if not last:
            self.audit_label.setText(t("audit.never"))
            return
        when = time.strftime("%Y-%m-%d %H:%M", time.localtime(last.get("finished_at", 0)))
        if last.get("status") == "ok":
            self.audit_label.setText(t(
                "audit.ok", when=when, files=last.get("files_checked", 0),
                duration=int(last.get("duration_seconds", 0)),
            ))
        elif last.get("status") in ("differences", "problems"):
            issues = [
                str(issue.get("detail") or "")
                for issue in (last.get("issues") or [])
                if issue.get("severity") in ("warning", "error")
            ]
            detail = " · ".join(issues[:2]) or t("audit.differences", when=when, count=last.get("differences", 0))
            self.audit_label.setText(detail)
        else:
            self.audit_label.setText(t("audit.error", detail=last.get("error") or ""))

    def _verify_now(self) -> None:
        self.verify_now_btn.setEnabled(False)
        self.assurance_proof.setText(t("assurance.verifying"))
        self.engine.verify_integrity_now()
        QTimer.singleShot(1500, lambda: self.verify_now_btn.setEnabled(True))

    def _refresh_assurance(self, data: dict) -> None:
        if not isinstance(data, dict) or not data:
            return
        pending = data.get("pending_count", 0)
        queued = data.get("queue_count", 0)
        conflicts = self._conflict_count
        mismatch = data.get("last_integrity_mismatches", 0)
        checked = data.get("last_integrity_files", 0)
        last = data.get("last_integrity_check_ts", 0)
        active = data.get("active_kind") or ""
        healthy = (
            self._connected and data.get("server_health") == "ok"
            and data.get("repository_ready") and data.get("journal_ready")
        )
        clean = healthy and not pending and not queued and not conflicts and not mismatch
        if active and not clean:
            headline = t("assurance.in_progress", kind=active)
            state = "active"
        elif clean:
            headline = t("assurance.all_synced")
            state = "ok"
        elif self._connected:
            headline = t("assurance.needs_attention")
            state = "warning"
        else:
            headline = t("assurance.offline")
            state = "error"
        self.assurance_headline.setText(headline)
        self.assurance_headline.setProperty("state", state)
        self.assurance_headline.style().unpolish(self.assurance_headline)
        self.assurance_headline.style().polish(self.assurance_headline)

        if last:
            age = max(0, int(time.time() - last))
            ago = t("assurance.seconds_ago", seconds=age) if age < 120 else t(
                "assurance.minutes_ago", minutes=age // 60,
            )
            scope = t("assurance.sample_suffix") if data.get("integrity_sample", 0) else ""
            self.assurance_proof.setText(t(
                "assurance.proof", when=ago, files=f"{checked}{scope}", mismatches=mismatch,
            ))
        else:
            self.assurance_proof.setText(t("assurance.waiting_proof"))

        checks = [
            ("connected", self._connected),
            ("repository", bool(data.get("repository_ready"))),
            ("journal", bool(data.get("journal_ready"))),
            ("queue", not queued and not pending),
            ("conflicts", not conflicts),
            ("integrity", bool(last) and not mismatch),
            ("errors", not any(p[0] == "event_error" for p in self.attention_problems)),
        ]
        rows = []
        for key, ok in checks:
            rows.append(t("assurance.check_line", mark="✓" if ok else "✗", text=t(f"assurance.check_{key}")))
        self.assurance_checks.setText("\n".join(rows))

        values = {
            "tracked": f"{data.get('tracked_files', 0)}",
            "pending": f"{pending}",
            "queue": f"{queued}",
            "conflicts": f"{conflicts}",
            "revision": f"{data.get('manifest_revision', -1)}",
            "lastcheck": t("assurance.unknown_short") if not last else t(
                "assurance.minutes_ago", minutes=max(0, int((time.time() - last) // 60)),
            ),
        }
        for key, value in values.items():
            self.assurance_stats[key].setText(value)
        self.assurance_status.emit(headline, self.assurance_proof.text())

    def on_status_changed(self, status: dict) -> None:
        paused = status.get("paused", False)
        remaining = status.get("pause_remaining")
        schedule_active = status.get("schedule_active", False)
        schedule_remaining = status.get("schedule_remaining")
        manual_paused = self.cfg.is_manually_paused()
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
            self.nas_host_label.setText(t("status.nas_connection_not_configured"))
        elif not connected:
            text = t("status.nas_unreachable_msg")
            state = "error"
            self.nas_host_label.setText(t("status.nas_connection_unreachable", where=where))
        elif manual_paused and remaining:
            mins = int(remaining // 60)
            secs = int(remaining % 60)
            text = t("status.paused_countdown_msg", mins=f"{mins:02d}", secs=f"{secs:02d}", where=where)
            state = "paused"
            self.nas_host_label.setText(t(
                "status.nas_connection_paused_countdown",
                where=where, mins=f"{mins:02d}", secs=f"{secs:02d}",
            ))
        elif manual_paused:
            text = t("status.paused_msg", where=where)
            state = "paused"
            self.nas_host_label.setText(t("status.nas_connection_paused", where=where))
        elif schedule_active:
            until = _clock_after(schedule_remaining)
            text = t("status.scheduled_pause_msg", until=until, where=where)
            state = "paused"
            self.nas_host_label.setText(t("status.nas_connection_scheduled", where=where, until=until))
        else:
            text = t("status.connected_msg", where=where)
            state = "active"
            self.nas_host_label.setText(t("status.nas_connection_connected", where=where))

        set_status_text(self.status_label, state, text)
        self._refresh_pause_button()
        self._refresh_sync_state(paused, remaining, schedule_active, schedule_remaining)
        self._refresh_remote_lock(status)
        self._refresh_watcher(status)
        self._scheduler_status = status.get("scheduler") or {}
        self._refresh_queue_label()
        self._refresh_pending_summary(status)
        self._refresh_attention()
        self._refresh_assurance(status.get("assurance") or {})
        self._refresh_audit(status.get("audit") or {})
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
            self._remote_lock_held = False
            self._remote_lock_owner = ""
            self._remote_lock_age = ""
            self.remote_lock_label.setText(t("status.remote_lock_free"))
            self._refresh_nas_activity()
            return
        self._remote_lock_held = True
        try:
            age_seconds = max(0, int(status.get("server_lock_age_seconds") or 0))
        except (TypeError, ValueError):
            age_seconds = 0
        minutes, seconds = divmod(age_seconds, 60)
        age = f"{minutes}m {seconds:02d}s" if minutes else f"{seconds}s"
        owner_id = str(status.get("server_lock_owner_id") or "")
        owner_host = str(status.get("server_lock_owner_host") or "")
        owner_pid = str(status.get("server_lock_owner_pid") or "")
        owner = owner_host or owner_id or owner_pid or t("status.unknown_owner")
        if owner_host and owner_id:
            owner = f"{owner_host} / {owner_id}"
        self._remote_lock_owner = owner
        self._remote_lock_age = age
        self.remote_lock_label.setText(t("status.remote_lock_held", age=age, owner=owner))
        self._refresh_nas_activity()

    def _confirm_deletions(self) -> None:
        self.engine.confirm_deletions()
        # The confirmation alone only lifts the safety guard: without a sync
        # request the pending deletions would stay queued until the next poll.
        self.engine.sync_now()

    def _on_devices_done(self, devices, exc: Exception | None) -> None:
        if exc is not None or not devices:
            self.devices_label.setText(t("status.devices_unknown"))
            return
        now = __import__("time").time()
        parts = []
        for device in devices[:5]:
            name = device.get("host") or device.get("device")
            age = max(0, int(now - int(device.get("last_seen") or 0)))
            flag = t("status.device_active") if device.get("active") else f"{age}s"
            parts.append(f"{name} ({flag})")
        self.devices_label.setText(t("status.devices_seen", names=", ".join(parts)))

    def _refresh_nas_activity(self) -> None:
        if not self._connected:
            self.nas_activity_label.setText(t("status.nas_activity_offline"))
            self.devices_label.setText(t("status.devices_unknown"))
            return
        # Device presence is read-only and cheap, but it is still an SSH call:
        # refresh it at most once a minute and never on the GUI thread.
        now = __import__("time").time()
        if now - self._last_devices_refresh >= 60 and self.engine.connection is not None:
            self._last_devices_refresh = now
            run_in_background(
                self, "_devices_call",
                lambda: rsync_ops.remote_devices(self.cfg, self.engine.connection),
                self._on_devices_done,
            )
        pending_retries = self._pending_retries()
        if pending_retries:
            _direction, remaining = pending_retries[0]
            self.nas_activity_label.setText(
                t("status.nas_activity_waiting_retry", seconds=remaining)
                if remaining > 0 else t("status.nas_activity_queued", count=len(self._queue_items or []))
            )
            return
        if self._remote_lock_held:
            self.nas_activity_label.setText(t(
                "status.nas_activity_locked",
                owner=self._remote_lock_owner, age=self._remote_lock_age,
            ))
        elif self._transfer_phases:
            direction = next(iter(self._transfer_phases))
            dir_str = t(
                "status.nas_activity_direction_upload" if direction == "upload"
                else "status.nas_activity_direction_download"
            )
            path = self._current_paths.get(direction, "")
            fname = os.path.basename(path) if path else ""
            speed_str = f" • {human_size(self._current_speed)}/s" if self._current_speed > 0 else ""
            pct_str = f" ({self._current_percent}%)" if self._current_percent > 0 else ""
            item_str = f": {fname}" if fname else ""
            batch_str = self._batch_position(direction)
            self.nas_activity_label.setText(t(
                "status.nas_activity_transfer",
                direction=dir_str, item=item_str, batch=batch_str,
                pct=pct_str, speed=speed_str,
            ))
        elif self._queue_items:
            self.nas_activity_label.setText(
                t("status.nas_activity_queued", count=len(self._queue_items))
            )
        else:
            self.nas_activity_label.setText(t("status.nas_activity_idle_full"))

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
        reason = str(summary.get("last_reason") or t("status.pending_reason_unknown"))
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
        set_status_text(self.watcher_label, state, text)

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
            active=active or t("status.scheduler_active_none"),
            scheduler_waiting=waiting_count,
        ))

    def on_transfer_preparing(self, direction: str) -> None:
        self._transfer_phases[direction] = "preparing"
        # A new attempt started: the previous countdown is over.
        self._retry_deadlines.pop(direction, None)
        self._batch_done[direction] = 0
        self._refresh_sync_state()

    def on_transfer_waiting_for_lock(self, direction: str) -> None:
        self._transfer_phases[direction] = "waiting"
        self._refresh_sync_state()

    def on_transfer_started(self, direction: str) -> None:
        self._transfer_phases[direction] = "active"
        self._retry_deadlines.pop(direction, None)
        self.sync_feedback_label.hide()
        self._refresh_sync_state()

    def on_transfer_retry_scheduled(self, direction: str, seconds: int) -> None:
        """The NAS lock was busy: show a live countdown instead of a dead end."""
        try:
            delay = max(1, int(seconds))
        except (TypeError, ValueError):
            delay = 1
        self._retry_deadlines[direction] = time.monotonic() + delay
        self._refresh_sync_state()

    def on_batch_size_known(self, total: int) -> None:
        """Authoritative batch size from PushWorker, for "file N di M"."""
        try:
            self._batch_totals["upload"] = max(0, int(total))
        except (TypeError, ValueError):
            self._batch_totals["upload"] = 0

    def on_transfer_item_started(self, direction: str, path: str, _size: int = 0) -> None:
        transfer_direction = "upload" if direction in ("upload", "delete_remote") else "download"
        self._transfer_phases[transfer_direction] = "active"
        self._current_paths[transfer_direction] = path
        self._refresh_sync_state()

    def on_transfer_item_done(self, direction: str, path: str) -> None:
        transfer_direction = "upload" if direction in ("upload", "delete_remote", "rename_remote") else "download"
        self._batch_done[transfer_direction] = self._batch_done.get(transfer_direction, 0) + 1
        if self._queue_items is not None:
            self._queue_items = [
                item for item in self._queue_items
                if (item.direction, item.path) != (direction, path)
            ]
            self._refresh_queue_label()
        # Keep "file N di M" and the queue count moving as files complete,
        # instead of only refreshing when the next file starts.
        self._refresh_sync_state()

    def on_transfer_speed(self, direction: str, bytes_per_sec: float, percent: int) -> None:
        self._current_speed = bytes_per_sec
        self._current_percent = percent
        if percent > 0:
            self.sync_progress_bar.setValue(percent)
            self.sync_progress_bar.show()
        self._refresh_sync_state()

    def on_transfer_finished(self, direction: str, ok: bool) -> None:
        self._current_speed = 0.0
        self._current_percent = 0
        self.sync_progress_bar.setValue(0)
        self.sync_progress_bar.hide()
        self._transfer_phases.pop(direction, None)
        self._current_paths.pop(direction, None)
        if ok:
            self._last_success = (time.time(), direction)
            self.last_sync_label.setText(self._last_sync_text())
            self.sync_feedback_label.hide()
            self._retry_deadlines.pop(direction, None)
            self._batch_totals.pop(direction, None)
            self._batch_done.pop(direction, None)
        elif not self.cfg.is_paused() and self._retry_remaining(direction) is None:
            # An interrupted transfer used to leave the status card looking
            # idle ("Sincronizzazione pronta") right after an error. Say what
            # happened and, above all, what happens next.
            self.sync_feedback_label.setText(t(
                "status.transfer_interrupted",
                direction=t(f"status.direction_{direction}"),
            ))
            self.sync_feedback_label.show()
        self._refresh_sync_state()

    def _refresh_sync_state(
        self, paused: bool | None = None, remaining=None,
        schedule_active: bool | None = None, schedule_remaining=None,
    ) -> None:
        manual = self.cfg.is_manually_paused()
        if paused is None:
            paused = self.cfg.is_paused()
        if remaining is None:
            remaining = self.cfg.pause_remaining_seconds()
        if schedule_active is None:
            schedule_active = self.cfg.schedule_active_at()
            schedule_remaining = self.cfg.schedule_remaining_seconds() if schedule_active else None
        if not self._status_known:
            text = t("status.sync_state_starting")
        elif not self.cfg.is_configured():
            text = t("status.sync_state_not_configured")
        elif not self._connected:
            text = t("status.sync_state_offline")
        elif manual and remaining:
            text = t("status.sync_state_paused_timed")
        elif manual:
            text = t("status.sync_state_paused")
        elif paused and schedule_active:
            text = t("status.sync_state_scheduled", until=_clock_after(schedule_remaining))
        elif paused:
            text = t("status.sync_state_paused")
        elif self._transfer_phases:
            direction = next(iter(self._transfer_phases))
            phase = self._transfer_phases[direction]
            direction_text = t(f"status.direction_{direction}")
            path = self._current_paths.get(direction, "")
            speed_str = f" • {human_size(self._current_speed)}/s" if self._current_speed > 0 else ""
            pct_str = f" ({self._current_percent}%)" if self._current_percent > 0 else ""
            sym = "⬆️" if direction == "upload" else "⬇️"
            if path:
                fname = os.path.basename(path) or path
                text = f"{sym} {direction_text}: {fname}{self._batch_position(direction)}{pct_str}{speed_str}"
            else:
                text = f"{sym} " + t(f"status.sync_state_{phase}", direction=direction_text) + speed_str
        elif self._pending_retries():
            text = self._retry_message()
        elif self._queue_items:
            text = t("status.sync_state_pending", count=len(self._queue_items))
        else:
            text = t("status.sync_state_ready")
        self.sync_state_label.setText(text)
        self._refresh_batch_progress()
        self._refresh_nas_activity()

    def _refresh_batch_progress(self) -> None:
        direction = next(iter(self._transfer_phases), "") if self._transfer_phases else ""
        total = self._batch_totals.get(direction) or 0
        if total <= 0:
            self.batch_progress_bar.hide()
            return
        done = min(self._batch_done.get(direction, 0), total)
        self.batch_progress_bar.setValue(int(100 * done / total))
        self.batch_progress_bar.setFormat(
            t("status.batch_progress_format", done=done, total=total)
        )
        self.batch_progress_bar.show()

    # --- pending-rerun countdown (NAS lock busy) ---

    def _retry_remaining(self, direction: str) -> int | None:
        """Seconds left before the automatic retry, or None when none is pending."""
        deadline = self._retry_deadlines.get(direction)
        if deadline is None:
            return None
        remaining = deadline - time.monotonic()
        if remaining <= -2:
            # The worker should have started by now; don't keep a stale countdown.
            self._retry_deadlines.pop(direction, None)
            return None
        return max(0, int(round(remaining)))

    def _pending_retries(self) -> list[tuple[str, int]]:
        return [
            (direction, remaining)
            for direction in ("upload", "download")
            if (remaining := self._retry_remaining(direction)) is not None
        ]

    def _retry_message(self) -> str:
        direction, remaining = self._pending_retries()[0]
        owner = self._remote_lock_owner
        owner_clause = (
            t("status.retry_owner", owner=owner) if owner
            else t("status.retry_owner_unknown")
        )
        queue_count = len(self._queue_items) if self._queue_items else 0
        queue_clause = (
            t("status.retry_queue_known", count=queue_count) if queue_count
            else t("status.retry_queue_unknown")
        )
        direction_text = t(f"status.direction_{direction}")
        if remaining <= 0:
            return t(
                "status.retry_now",
                direction=direction_text, owner=owner_clause, queue=queue_clause,
            )
        return t(
            "status.retry_countdown",
            direction=direction_text, owner=owner_clause, queue=queue_clause,
            seconds=remaining,
        )

    def _batch_position(self, direction: str) -> str:
        total = self._batch_totals.get(direction) or 0
        if total <= 0:
            return ""
        done = min(self._batch_done.get(direction, 0), total)
        return t("status.batch_position", done=done, total=total)

    def on_transfer_lock_unavailable(self, direction: str, detail: str) -> None:
        self._transfer_phases.pop(direction, None)
        self._current_paths.pop(direction, None)
        normalized = str(detail).lower()
        if (
            ("altro pc" in normalized or "another pc" in normalized)
            and self._retry_remaining(direction) is not None
        ):
            # Contention is normal and already explained by the countdown line:
            # showing it as an error here is what made it look like a failure.
            self.sync_feedback_label.hide()
        else:
            self.sync_feedback_label.setText(
                t("status.sync_lock_error", detail=self._compact_detail(detail))
            )
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
        now = time.time()
        activity = {"UPLOAD": 0, "DOWNLOAD": 0, "DELETE_REMOTE": 0, "DELETE_LOCAL": 0}
        for event in events:
            if now - event.ts <= 86400 and event.action in activity:
                activity[event.action] += 1
        if any(activity.values()) and hasattr(self, "assurance_activity"):
            self.assurance_activity.setText(t(
                "assurance.activity",
                uploads=activity["UPLOAD"] + activity["DELETE_REMOTE"],
                downloads=activity["DOWNLOAD"] + activity["DELETE_LOCAL"],
                deletes=activity["DELETE_REMOTE"] + activity["DELETE_LOCAL"],
            ))
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
        if self._conflict_count:
            problems.append((
                "conflicts",
                t("status.problem_conflicts", count=self._conflict_count),
                "conflicts",
            ))
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
        transient_cutoff = time.time() - RECENT_TRANSIENT_SECONDS
        for action in ("SAFETY_BLOCK", "JOURNAL_BLOCK", "ERROR"):
            event = self._recent_problem_events.get(action)
            if event is None:
                continue
            # A plain transfer error that has not recurred for a while is no
            # longer actionable: the worker already retried and the queue moved.
            if event[0] < (transient_cutoff if action == "ERROR" else cutoff):
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
            self.nas_storage_label.setText(
                t("status.storage_error", detail=exc)
            )
            self.nas_storage_bar.hide()
            self._refresh_attention()
            return
        ok, values, detail = result
        if not ok:
            self.diagnostics_label.setText(t("status.diagnostics_failed", detail=detail))
            self.nas_storage_label.setText(t("status.storage_error_short", detail=detail))
            self.nas_storage_bar.hide()
            self._refresh_attention()
            return
        try:
            available_bytes = int(values.get("SHARE_AVAILABLE_BYTES", "0"))
            total_bytes = int(values.get("SHARE_TOTAL_BYTES", "0"))
            free = human_size(available_bytes)
            trash_size = human_size(int(values.get("TRASH_DISK_BYTES", "0")))
            count = int(values.get("TRASH_FILE_COUNT", "0"))
        except (TypeError, ValueError):
            self.diagnostics_label.setText(t("status.diagnostics_failed", detail=t("status.diagnostics_invalid")))
            self.nas_storage_label.setText(t("status.storage_invalid"))
            self.nas_storage_bar.hide()
            self._refresh_attention()
            return
        self._diagnostics_available = True
        self._nas_available_bytes = available_bytes
        self.diagnostics_label.setText(t("status.diagnostics_summary", free=free, trash=trash_size, count=count))
        if total_bytes > 0:
            total_str = human_size(total_bytes)
            pct = max(0, min(100, int((total_bytes - available_bytes) / total_bytes * 100)))
            self.nas_storage_label.setText(t(
                "status.storage_summary",
                free=free, total=total_str, pct=pct, trash=trash_size, count=count,
            ))
            self.nas_storage_bar.setValue(pct)
            self.nas_storage_bar.setFormat(t("status.storage_bar_format", free=free))
            self.nas_storage_bar.show()
        else:
            self.nas_storage_label.setText(t(
                "status.storage_summary_no_total", free=free, trash=trash_size, count=count,
            ))
            self.nas_storage_bar.hide()
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
        self.update_available_label.setToolTip(t("status.update_available_tooltip"))
        self.update_available_label.show()

    def _check_for_client_update_clicked(self) -> None:
        """Clicking the update badge installs a found update, otherwise it checks."""
        if self._update_candidate is not None:
            self._install_update()
        else:
            self._check_for_update()

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
            source_root = candidate.materialize()
            current_root = Path(os.path.abspath(__file__)).resolve().parents[2]
            updater.install_update(source_root, current_root)
            candidate.cleanup()
            QMessageBox.information(self, t("status.update_restart_title"), t("status.update_restart_body"))
            updater.restart(current_root)
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
