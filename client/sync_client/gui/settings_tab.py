from __future__ import annotations

from PyQt6.QtCore import Qt, QUrl
from PyQt6.QtGui import QDesktopServices
from PyQt6.QtWidgets import (
    QAbstractItemView, QBoxLayout, QCheckBox, QComboBox, QDialog, QDialogButtonBox, QFormLayout,
    QGroupBox, QHBoxLayout, QLabel, QLineEdit, QListWidget, QListWidgetItem,
    QMessageBox, QPushButton, QScrollArea, QSpinBox, QVBoxLayout, QWidget,
)

from pathlib import Path

from .. import paths, rsync_ops, trash
from ..config import Config
from ..engine import SyncEngine
from ..i18n import t
from ..version import APP_NAME, APP_VERSION
from ..repository_safety import RepositorySafetyError, bind_repository
from .async_utils import run_in_background

LANGUAGE_OPTIONS = [
    ("auto", "settings.language_auto"),
    ("it", "settings.language_it"),
    ("en", "settings.language_en"),
]

TRAY_CLICK_OPTIONS = [
    ("menu", "settings.tray_click_menu"),
    ("window", "settings.tray_click_window"),
]


class SettingsTab(QWidget):
    """Everything you set up once and rarely touch again: how to reach the
    NAS, bandwidth caps, sync cadence, and which patterns to leave out of
    sync entirely. Kept apart from the Stato tab on purpose -- that one is
    for glancing at, this one is for configuring."""

    def __init__(self, cfg: Config, engine: SyncEngine, parent=None) -> None:
        super().__init__(parent)
        self.cfg = cfg
        self.engine = engine
        self._detect_busy = False
        self._restart_busy = False
        self._verify_keys_busy = False
        self._conflict_files: list[Path] = []
        self._dirty = False
        self._single_column = False

        # This tab stacks a lot of group boxes -- without a scroll area, Qt's
        # layout system reports their combined minimum height as *this tab's*
        # minimum height, which propagates all the way up to the main window
        # and makes it impossible to shrink below "tall enough to show every
        # field at once". A scroll area's own minimum height is small
        # regardless of what's inside it, so the window stays freely
        # resizable and this tab scrolls internally instead.
        outer = QVBoxLayout(self)
        outer.setContentsMargins(0, 0, 0, 0)
        scroll = QScrollArea()
        scroll.setWidgetResizable(True)
        scroll.setFrameShape(QScrollArea.Shape.NoFrame)
        outer.addWidget(scroll)

        container = QWidget()
        scroll.setWidget(container)
        root = QVBoxLayout(container)

        # Two columns instead of one long stack: NAS connection is the one
        # genuinely long group (10+ fields), so it gets a column to itself:
        # everything else is short enough to stack in the other one without
        # either column towering over the other, and neither column forces a
        # QLineEdit to stretch edge-to-edge across the whole window for a
        # value like a port number.
        columns = QHBoxLayout()
        self._columns = columns
        left_col = QVBoxLayout()
        right_col = QVBoxLayout()
        columns.addLayout(left_col, 1)
        columns.addLayout(right_col, 1)
        root.addLayout(columns)

        # --- NAS connection ---
        nas_box = QGroupBox(t("settings.nas_box_title"))
        nas_form = QFormLayout(nas_box)
        self.nas_lan = QLineEdit(str(cfg.get("nas_lan") or ""))
        self.nas_lan.setPlaceholderText(t("settings.nas_lan_placeholder"))
        self.nas_lan.setToolTip(t("settings.nas_lan_tooltip"))
        nas_form.addRow(t("settings.nas_lan_label"), self.nas_lan)
        self.nas_wan = QLineEdit(str(cfg.get("nas_wan") or ""))
        self.nas_wan.setPlaceholderText(t("settings.nas_wan_placeholder"))
        self.nas_wan.setToolTip(t("settings.nas_wan_tooltip"))
        nas_form.addRow(t("settings.nas_wan_label"), self.nas_wan)
        self.nas_user = QLineEdit(str(cfg.get("nas_user") or ""))
        self.nas_user.setToolTip(t("settings.nas_user_tooltip"))
        nas_form.addRow(t("settings.nas_user_label"), self.nas_user)
        self.ssh_port = QSpinBox()
        self.ssh_port.setRange(1, 65535)
        self.ssh_port.setValue(int(cfg.get("ssh_port") or 22))
        self.ssh_port.setToolTip(t("settings.ssh_port_tooltip"))
        nas_form.addRow(t("settings.ssh_port_label"), self.ssh_port)
        verify_keys_btn = QPushButton(t("settings.verify_keys_btn"))
        verify_keys_btn.setToolTip(t("settings.verify_keys_tooltip"))
        verify_keys_btn.clicked.connect(self._verify_host_keys)
        nas_form.addRow(verify_keys_btn)

        jump_note = QLabel(t("settings.jump_note"))
        jump_note.setWordWrap(True)
        nas_form.addRow(jump_note)
        self.jump_host = QLineEdit(str(cfg.get("jump_host") or ""))
        self.jump_host.setPlaceholderText(t("settings.jump_host_placeholder"))
        self.jump_host.setToolTip(t("settings.jump_note"))
        nas_form.addRow(t("settings.jump_host_label"), self.jump_host)
        self.jump_port = QSpinBox()
        self.jump_port.setRange(1, 65535)
        self.jump_port.setValue(int(cfg.get("jump_port") or 22))
        self.jump_port.setToolTip(t("settings.jump_note"))
        nas_form.addRow(t("settings.jump_port_label"), self.jump_port)
        self.jump_user = QLineEdit(str(cfg.get("jump_user") or ""))
        self.jump_user.setPlaceholderText(t("settings.jump_user_placeholder"))
        self.jump_user.setToolTip(t("settings.jump_note"))
        nas_form.addRow(t("settings.jump_user_label"), self.jump_user)

        script_row = QHBoxLayout()
        self.remote_server_script = QLineEdit(str(cfg.get("remote_server_script") or ""))
        self.remote_server_script.setPlaceholderText(t("settings.remote_script_placeholder"))
        self.remote_server_script.setToolTip(t("settings.remote_script_tooltip"))
        self.restart_daemon_btn = QPushButton(t("settings.restart_daemon_btn"))
        self.restart_daemon_btn.setToolTip(t("settings.restart_daemon_tooltip"))
        self.restart_daemon_btn.clicked.connect(self._restart_daemon)
        script_row.addWidget(self.remote_server_script)
        script_row.addWidget(self.restart_daemon_btn)
        nas_form.addRow(t("settings.remote_script_label"), script_row)

        prefix_row = QHBoxLayout()
        self.remote_prefix = QLineEdit(str(cfg.get("remote_prefix") or ""))
        self.remote_prefix.setPlaceholderText(t("settings.remote_prefix_placeholder"))
        self.detect_btn = QPushButton(t("settings.detect_btn"))
        self.detect_btn.setToolTip(t("settings.detect_note"))
        self.detect_btn.clicked.connect(self._detect_from_nas)
        prefix_row.addWidget(self.remote_prefix)
        prefix_row.addWidget(self.detect_btn)
        nas_form.addRow(t("settings.remote_prefix_label"), prefix_row)
        detect_note = QLabel(t("settings.detect_note"))
        detect_note.setWordWrap(True)
        nas_form.addRow(detect_note)

        self.delete_enabled = QCheckBox(t("settings.delete_enabled_checkbox"))
        self.delete_enabled.setChecked(bool(cfg.get("delete_enabled")))
        self.delete_enabled.setToolTip(t("settings.delete_enabled_tooltip"))
        nas_form.addRow(self.delete_enabled)
        self.max_delete_spin = QSpinBox()
        self.max_delete_spin.setRange(1, 1_000_000)
        self.max_delete_spin.setValue(int(cfg.get("max_delete_files") or 1000))
        self.max_delete_spin.setToolTip(t("settings.max_delete_tooltip"))
        nas_form.addRow(t("settings.max_delete_label"), self.max_delete_spin)
        left_col.addWidget(nas_box)

        # --- bandwidth ---
        bw_box = QGroupBox(t("settings.bandwidth_title"))
        bw_layout = QHBoxLayout(bw_box)
        bw_layout.addWidget(QLabel(t("settings.upload_label")))
        self.bw_upload_spin = QSpinBox()
        self.bw_upload_spin.setRange(0, 1_000_000)
        self.bw_upload_spin.setSingleStep(64)
        self.bw_upload_spin.setValue(int(cfg.get("bandwidth_upload_kbps") or 0))
        self.bw_upload_spin.setToolTip(t("settings.bandwidth_tooltip"))
        bw_layout.addWidget(self.bw_upload_spin)
        bw_layout.addWidget(QLabel(t("settings.download_label")))
        self.bw_download_spin = QSpinBox()
        self.bw_download_spin.setRange(0, 1_000_000)
        self.bw_download_spin.setSingleStep(64)
        self.bw_download_spin.setValue(int(cfg.get("bandwidth_download_kbps") or 0))
        self.bw_download_spin.setToolTip(t("settings.bandwidth_tooltip"))
        bw_layout.addWidget(self.bw_download_spin)
        right_col.addWidget(bw_box)

        # --- sync cadence ---
        cadence_box = QGroupBox(t("settings.cadence_title"))
        cadence_form = QFormLayout(cadence_box)
        self.poll_spin = QSpinBox()
        self.poll_spin.setRange(5, 3600)
        self.poll_spin.setSuffix(" s")
        self.poll_spin.setValue(int(cfg.get("poll_interval") or 60))
        self.poll_spin.setToolTip(t("settings.cadence_note"))
        cadence_form.addRow(t("settings.poll_label"), self.poll_spin)
        cadence_note = QLabel(t("settings.cadence_note"))
        cadence_note.setWordWrap(True)
        cadence_form.addRow(cadence_note)
        right_col.addWidget(cadence_box)

        # --- sync feedback ---
        feedback_box = QGroupBox(t("settings.feedback_title"))
        feedback_layout = QVBoxLayout(feedback_box)
        self.notify_sync_checkbox = QCheckBox(t("settings.notify_sync_checkbox"))
        self.notify_sync_checkbox.setChecked(bool(cfg.get("notify_sync_completion")))
        self.notify_sync_checkbox.setToolTip(t("settings.notify_sync_tooltip"))
        feedback_layout.addWidget(self.notify_sync_checkbox)
        self.animate_sync_checkbox = QCheckBox(t("settings.animate_sync_checkbox"))
        self.animate_sync_checkbox.setChecked(bool(cfg.get("animate_sync_icon")))
        self.animate_sync_checkbox.setToolTip(t("settings.animate_sync_tooltip"))
        feedback_layout.addWidget(self.animate_sync_checkbox)
        tray_click_row = QHBoxLayout()
        tray_click_row.addWidget(QLabel(t("settings.tray_click_label")))
        self.tray_click_combo = QComboBox()
        current_tray_click = cfg.get("tray_single_click", "menu")
        for index, (value, label_key) in enumerate(TRAY_CLICK_OPTIONS):
            self.tray_click_combo.addItem(t(label_key), userData=value)
            if value == current_tray_click:
                self.tray_click_combo.setCurrentIndex(index)
        self.tray_click_combo.setToolTip(t("settings.tray_click_tooltip"))
        tray_click_row.addWidget(self.tray_click_combo, 1)
        feedback_layout.addLayout(tray_click_row)
        feedback_note = QLabel(t("settings.notify_sync_note"))
        feedback_note.setWordWrap(True)
        feedback_layout.addWidget(feedback_note)
        right_col.addWidget(feedback_box)

        # --- language ---
        language_box = QGroupBox(t("settings.language_title"))
        language_form = QFormLayout(language_box)
        self.language_combo = QComboBox()
        current_lang = cfg.language()
        for index, (code, label_key) in enumerate(LANGUAGE_OPTIONS):
            self.language_combo.addItem(t(label_key), userData=code)
            if code == current_lang:
                self.language_combo.setCurrentIndex(index)
        self.language_combo.setToolTip(t("settings.language_note"))
        language_form.addRow(t("settings.language_label"), self.language_combo)
        language_note = QLabel(t("settings.language_note"))
        language_note.setWordWrap(True)
        language_form.addRow(language_note)
        right_col.addWidget(language_box)

        # --- info ---
        info_box = QGroupBox(t("settings.info_title"))
        info_form = QFormLayout(info_box)
        info_form.addRow(t("settings.info_version_label"), QLabel(f"{APP_NAME} {APP_VERSION}"))
        info_form.addRow(t("settings.info_min_server_label"), QLabel(rsync_ops.EXPECTED_SERVER_VERSION))
        config_path_label = QLabel(str(paths.config_file()))
        config_path_label.setTextInteractionFlags(Qt.TextInteractionFlag.TextSelectableByMouse)
        config_path_label.setWordWrap(True)
        info_form.addRow(t("settings.info_config_label"), config_path_label)
        log_path_label = QLabel(str(paths.log_file()))
        log_path_label.setTextInteractionFlags(Qt.TextInteractionFlag.TextSelectableByMouse)
        log_path_label.setWordWrap(True)
        info_form.addRow(t("settings.info_log_label"), log_path_label)
        right_col.addWidget(info_box)

        # --- maintenance ---
        maint_box = QGroupBox(t("settings.maint_title"))
        maint_layout = QVBoxLayout(maint_box)
        maint_note = QLabel(t("settings.maint_conflict_note"))
        maint_note.setWordWrap(True)
        maint_layout.addWidget(maint_note)
        self.conflict_count_label = QLabel(t("settings.maint_conflict_count_unknown"))
        self.conflict_count_label.setWordWrap(True)
        maint_layout.addWidget(self.conflict_count_label)
        conflict_row = QHBoxLayout()
        self.maint_scan_btn = QPushButton(t("settings.maint_scan_btn"))
        self.maint_scan_btn.setToolTip(t("settings.maint_scan_tooltip"))
        self.maint_scan_btn.clicked.connect(self._scan_conflicts)
        conflict_row.addWidget(self.maint_scan_btn)
        self.maint_delete_btn = QPushButton(t("settings.maint_review_btn"))
        self.maint_delete_btn.setToolTip(t("settings.maint_delete_tooltip"))
        self.maint_delete_btn.setEnabled(False)
        self.maint_delete_btn.clicked.connect(self._delete_conflicts)
        conflict_row.addWidget(self.maint_delete_btn)
        conflict_row.addStretch(1)
        maint_layout.addLayout(conflict_row)
        right_col.addWidget(maint_box)
        # --- selective sync (full width: the list benefits from it, and it
        #     doesn't pair well height-wise with anything above) ---
        exclude_box = QGroupBox(t("settings.exclude_title"))
        exclude_layout = QVBoxLayout(exclude_box)
        exclude_note = QLabel(t("settings.exclude_note"))
        exclude_note.setWordWrap(True)
        exclude_layout.addWidget(exclude_note)
        self.exclude_list = QListWidget()
        self.exclude_list.setMaximumHeight(120)
        self.exclude_list.setToolTip(t("settings.exclude_note"))
        self.exclude_list.addItems(cfg.exclude_patterns())
        exclude_layout.addWidget(self.exclude_list)
        exclude_add_row = QHBoxLayout()
        self.exclude_input = QLineEdit()
        self.exclude_input.setPlaceholderText(t("settings.exclude_hint"))
        self.exclude_input.setToolTip(t("settings.exclude_input_tooltip"))
        self.exclude_input.returnPressed.connect(self._add_exclude)
        exclude_add_row.addWidget(self.exclude_input)
        add_exclude_btn = QPushButton(t("settings.exclude_add_btn"))
        add_exclude_btn.setToolTip(t("settings.exclude_add_tooltip"))
        add_exclude_btn.clicked.connect(self._add_exclude)
        exclude_add_row.addWidget(add_exclude_btn)
        self.remove_exclude_btn = QPushButton(t("settings.exclude_remove_btn"))
        self.remove_exclude_btn.setToolTip(t("settings.exclude_remove_tooltip"))
        self.remove_exclude_btn.setEnabled(False)
        self.remove_exclude_btn.clicked.connect(self._remove_exclude)
        exclude_add_row.addWidget(self.remove_exclude_btn)
        exclude_layout.addLayout(exclude_add_row)

        # Read-only, separate from the editable list above: this one isn't a
        # pattern the user typed in, it's computed automatically from "Script
        # server sul NAS" (see rsync_ops.server_package_excluded_path) and
        # can't be removed here -- shown so "where did my server folder go,
        # why isn't it in my synced NASBox folder" has a visible answer
        # instead of being an invisible rule nobody can see is active.
        self.auto_exclude_label = QLabel()
        self.auto_exclude_label.setWordWrap(True)
        self.auto_exclude_label.setTextInteractionFlags(Qt.TextInteractionFlag.TextSelectableByMouse)
        exclude_layout.addWidget(self.auto_exclude_label)
        self._refresh_auto_exclude_label()

        root.addWidget(exclude_box)

        save_row = QHBoxLayout()
        self.save_feedback = QLabel(t("settings.saved_feedback"))
        self.save_feedback.setObjectName("settingsSaveFeedback")
        self.save_feedback.setWordWrap(True)
        save_row.addWidget(self.save_feedback, 1)
        self.save_btn = QPushButton(t("settings.save_all_btn"))
        self.save_btn.setObjectName("primaryButton")
        self.save_btn.setToolTip(t("settings.save_all_tooltip"))
        self.save_btn.clicked.connect(self._save_settings)
        self.save_btn.setEnabled(False)
        save_row.addWidget(self.save_btn)
        outer.addLayout(save_row)

        root.addStretch(1)
        self._connect_dirty_signals()
        self._update_column_layout(self.width())

    # --- actions ---

    def _connect_dirty_signals(self) -> None:
        for edit in (
            self.nas_lan, self.nas_wan, self.nas_user, self.jump_host,
            self.jump_user, self.remote_server_script, self.remote_prefix,
        ):
            edit.textChanged.connect(self._mark_dirty)
        for spin in (
            self.ssh_port, self.jump_port, self.max_delete_spin,
            self.bw_upload_spin, self.bw_download_spin, self.poll_spin,
        ):
            spin.valueChanged.connect(self._mark_dirty)
        for checkbox in (
            self.delete_enabled, self.notify_sync_checkbox, self.animate_sync_checkbox,
        ):
            checkbox.toggled.connect(self._mark_dirty)
        self.language_combo.currentIndexChanged.connect(self._mark_dirty)
        self.tray_click_combo.currentIndexChanged.connect(self._mark_dirty)
        self.exclude_list.itemSelectionChanged.connect(
            lambda: self.remove_exclude_btn.setEnabled(self.exclude_list.currentRow() >= 0)
        )

    def _mark_dirty(self, *_args) -> None:
        self._dirty = True
        self.save_btn.setEnabled(True)
        self.save_feedback.setText(t("settings.dirty_feedback"))

    def _apply_bandwidth(self) -> None:
        self.cfg.set("bandwidth_upload_kbps", self.bw_upload_spin.value(), persist=False)
        self.cfg.set("bandwidth_download_kbps", self.bw_download_spin.value(), persist=False)

    def _apply_poll_interval(self) -> None:
        self.cfg.set("poll_interval", self.poll_spin.value(), persist=False)

    def _add_exclude(self) -> None:
        pattern = self.exclude_input.text().strip()
        if not pattern:
            return
        patterns = [self.exclude_list.item(i).text() for i in range(self.exclude_list.count())]
        if pattern in patterns:
            self.exclude_input.clear()
            self.exclude_list.setCurrentRow(patterns.index(pattern))
            return
        self.exclude_list.addItem(pattern)
        self.exclude_list.setCurrentRow(self.exclude_list.count() - 1)
        self.exclude_input.clear()
        self._mark_dirty()

    def _remove_exclude(self) -> None:
        row = self.exclude_list.currentRow()
        if row < 0:
            return
        self.exclude_list.takeItem(row)
        if self.exclude_list.count():
            self.exclude_list.setCurrentRow(min(row, self.exclude_list.count() - 1))
        self._mark_dirty()

    def _save_settings(self) -> None:
        self.cfg.set("nas_lan", self.nas_lan.text().strip(), persist=False)
        self.cfg.set("nas_wan", self.nas_wan.text().strip(), persist=False)
        self.cfg.set("nas_user", self.nas_user.text().strip(), persist=False)
        self.cfg.set("ssh_port", self.ssh_port.value(), persist=False)
        self.cfg.set("jump_host", self.jump_host.text().strip(), persist=False)
        self.cfg.set("jump_port", self.jump_port.value(), persist=False)
        self.cfg.set("jump_user", self.jump_user.text().strip(), persist=False)
        self.cfg.set("remote_server_script", self.remote_server_script.text().strip(), persist=False)
        self.cfg.set("remote_prefix", self.remote_prefix.text().strip(), persist=False)
        self.cfg.set("delete_enabled", self.delete_enabled.isChecked(), persist=False)
        self.cfg.set("max_delete_files", self.max_delete_spin.value(), persist=False)
        self._apply_bandwidth()
        self._apply_poll_interval()
        self.cfg.set("notify_sync_completion", self.notify_sync_checkbox.isChecked(), persist=False)
        self.cfg.set("animate_sync_icon", self.animate_sync_checkbox.isChecked(), persist=False)
        self.cfg.set("tray_single_click", self.tray_click_combo.currentData() or "menu", persist=False)
        code = self.language_combo.currentData()
        if code:
            self.cfg.set("language", code, persist=False)
        patterns = [self.exclude_list.item(i).text() for i in range(self.exclude_list.count())]
        self.cfg.set("exclude_patterns", patterns, persist=False)
        self.cfg.save()
        self.engine.wake()
        self._refresh_auto_exclude_label()
        self._dirty = False
        self.save_btn.setEnabled(False)
        self.save_feedback.setText(t("settings.saved_feedback"))

    def _verify_host_keys(self) -> None:
        if self._verify_keys_busy:
            return
        hosts = []
        lan = self.nas_lan.text().strip()
        wan = self.nas_wan.text().strip()
        jump = self.jump_host.text().strip()
        if lan:
            hosts.append(("NAS LAN", lan, self.ssh_port.value()))
        if wan and wan != lan:
            hosts.append(("NAS WAN", wan, self.ssh_port.value()))
        if jump:
            hosts.append(("bastione", jump, self.jump_port.value()))
        if not hosts:
            QMessageBox.warning(self, t("settings.verify_keys_title"), t("settings.verify_keys_no_host"))
            return

        self._save_settings()
        self._verify_keys_busy = True
        run_in_background(
            self, "_verify_keys_call",
            lambda: [(label, host, port, rsync_ops.host_key_fingerprints(host, port)) for label, host, port in hosts],
            self._on_verify_host_keys_done,
        )

    def _on_verify_host_keys_done(self, result, exc: Exception | None) -> None:
        self._verify_keys_busy = False
        if exc is not None:
            QMessageBox.warning(self, t("settings.verify_keys_title"), str(exc))
            return
        known_hosts = str(paths.ssh_known_hosts_file())
        all_pinned = True
        for label, host, port, fingerprints in result:
            if not fingerprints:
                all_pinned = False
                QMessageBox.warning(
                    self, t("settings.verify_keys_title"),
                    t("settings.verify_keys_no_result", label=label, host=host),
                )
                continue
            body = t("settings.verify_keys_body", label=label, host=host, fingerprints="\n".join(fingerprints))
            if QMessageBox.question(
                self, t("settings.verify_keys_title"), body,
            ) != QMessageBox.StandardButton.Yes:
                all_pinned = False
                continue
            ok, detail = rsync_ops.pin_host_key(host, port, known_hosts, fingerprints[0])
            if not ok:
                all_pinned = False
                QMessageBox.warning(self, t("settings.verify_keys_title"), t("settings.verify_keys_failed", detail=detail))

        self.cfg.set("ssh_known_hosts", known_hosts, persist=False)
        self.cfg.set("ssh_host_key_pinned", all_pinned)
        if all_pinned:
            QMessageBox.information(self, t("settings.verify_keys_title"), t("settings.verify_keys_done"))
        else:
            QMessageBox.information(self, t("settings.verify_keys_title"), t("settings.verify_keys_partial"))
        self.engine.wake()

    def _refresh_auto_exclude_label(self) -> None:
        excluded = rsync_ops.server_package_excluded_path(self.cfg)
        if excluded:
            self.auto_exclude_label.setText(t("settings.auto_exclude_active", path=excluded))
        else:
            self.auto_exclude_label.setText(t("settings.auto_exclude_none"))

    def _detect_from_nas(self) -> None:
        if self._detect_busy:
            return  # already running -- ignore a repeat click instead of overlapping calls
        self._save_settings()  # use whatever is currently typed, not the last-saved values

        # Everything below is SSH: resolve_connection alone can take ~7s
        # (probing LAN/WAN/bastion in turn), discover_remote_scripts up to
        # 20s, fetch_remote_config up to 30s -- run inline on the GUI thread,
        # that's up to ~57s with the window completely unresponsive. The
        # worker below does all of it off-thread and returns a plain dict;
        # _on_detect_done applies it to the widgets back on the GUI thread.
        #
        # Deliberately NOT setEnabled(False): disabling a focused button
        # makes Qt hand keyboard focus to whatever's next in tab order,
        # which reads as the UI randomly jumping around. A busy flag guards
        # against a repeat click without stealing focus.
        script_path_hint = self.remote_server_script.text().strip()
        self._detect_busy = True
        self.detect_btn.setText(t("settings.detect_running"))
        run_in_background(
            self, "_detect_call",
            lambda: _detect_worker(self.cfg, script_path_hint),
            self._on_detect_done,
        )

    def _on_detect_done(self, result: dict, exc: Exception | None) -> None:
        self._detect_busy = False
        self.detect_btn.setText(t("settings.detect_btn"))
        if exc is not None:
            QMessageBox.warning(self, t("settings.detect_failed_title"), str(exc))
            return

        if not result["connected"]:
            QMessageBox.warning(self, t("settings.nas_unreachable_title"), t("settings.nas_unreachable_body"))
            return
        if result.get("daemon_not_found"):
            QMessageBox.warning(self, t("settings.daemon_not_found_title"), t("settings.daemon_not_found_body"))
            return

        script_path = result["script_path"]
        multiple_found = result.get("multiple_found")
        if multiple_found:
            QMessageBox.warning(
                self, t("settings.multiple_instances_title"),
                t("settings.multiple_instances_body", list="\n".join(multiple_found), first=multiple_found[0]),
            )
        if script_path:
            self.remote_server_script.setText(script_path)
            self.cfg.set("remote_server_script", script_path, persist=False)
            self._refresh_auto_exclude_label()

        if not result["fetch_ok"]:
            QMessageBox.warning(self, t("settings.detect_failed_title"), result["err"])
            return

        values = result["values"]
        share_root = values.get("SHARE_ROOT", "")
        retention = values.get("RETENTION_DAYS", "")
        running = values.get("running", False)
        repository_id = values.get("REPOSITORY_ID", "")
        repository_ready = values.get("REPOSITORY_READY", "").lower() == "true"
        journal_ready = values.get("JOURNAL_READY", "").lower() == "true"

        if share_root:
            self.remote_prefix.setText(share_root)
            self.cfg.set("remote_prefix", share_root, persist=False)
            self._refresh_auto_exclude_label()
        if retention:
            try:
                self.cfg.set("retention_days_remote", int(retention), persist=False)
            except ValueError:
                pass
        if repository_id:
            self.cfg.set("repository_id", repository_id, persist=False)
        self.cfg.set("remote_repository_ready", repository_ready, persist=False)
        self.cfg.set("remote_journal_ready", journal_ready, persist=False)
        if repository_ready and repository_id and self.cfg.local_root():
            try:
                bind_repository(self.cfg.local_root(), repository_id)
            except RepositorySafetyError as exc:
                QMessageBox.warning(
                    self,
                    "Verifica repository NASBox",
                    f"Il NAS è stato riconosciuto, ma la cartella locale non è stata associata:\n\n{exc}\n\n"
                    "Le cancellazioni resteranno bloccate finché la cartella non sarà verificata.",
                )
        state_dir = values.get("STATE_DIR", "")
        if state_dir:
            self.cfg.set("server_state_dir_remote", state_dir, persist=False)
        self.cfg.set("server_lock_file_remote", values.get("SYNC_LOCK_FILE", ""), persist=False)
        self._save_settings()

        status_line = t("settings.daemon_active") if running else t("settings.daemon_inactive")
        version = values.get("VERSION", "")
        version_line = ""
        if version and rsync_ops.server_is_outdated(version):
            version_line = t("settings.outdated_version_note", version=version, expected=rsync_ops.EXPECTED_SERVER_VERSION)
        QMessageBox.information(
            self, t("settings.detect_done_title"),
            t(
                "settings.detect_done_body",
                status_line=status_line,
                share_root=share_root or t("settings.not_set_placeholder"),
                retention=t("settings.retention_never") if retention == "0" else t("settings.retention_days", days=retention or "?"),
                version_line=version_line,
            ),
        )

    def resizeEvent(self, event) -> None:
        super().resizeEvent(event)
        self._update_column_layout(event.size().width())

    def _update_column_layout(self, width: int) -> None:
        single_column = width < 900
        if single_column == self._single_column:
            return
        self._single_column = single_column
        direction = (
            QBoxLayout.Direction.TopToBottom if single_column
            else QBoxLayout.Direction.LeftToRight
        )
        self._columns.setDirection(direction)

    def _restart_daemon(self) -> None:
        if self._restart_busy:
            return  # already running -- ignore a repeat click instead of overlapping calls
        script_path = self.remote_server_script.text().strip()
        if not script_path:
            QMessageBox.warning(self, t("settings.script_not_configured_title"), t("settings.script_not_configured_body"))
            return

        if QMessageBox.question(
            self, t("settings.restart_confirm_title"),
            t("settings.restart_confirm_body", script_path=script_path),
        ) != QMessageBox.StandardButton.Yes:
            return

        self._save_settings()
        # Not setEnabled(False) -- see _detect_from_nas's comment: it steals
        # keyboard focus to the next tab-order widget the instant the button
        # holding it gets disabled, which looks like a random UI glitch.
        self._restart_busy = True
        self.restart_daemon_btn.setText(t("settings.restart_running"))
        run_in_background(
            self, "_restart_call",
            lambda: _restart_worker(self.cfg, script_path),
            self._on_restart_done,
        )

    def _on_restart_done(self, result: dict, exc: Exception | None) -> None:
        self._restart_busy = False
        self.restart_daemon_btn.setText(t("settings.restart_daemon_btn"))
        if exc is not None:
            QMessageBox.warning(self, t("settings.restart_failed_title"), str(exc))
            return
        if not result["connected"]:
            QMessageBox.warning(self, t("settings.nas_unreachable_title"), t("settings.nas_unreachable_body"))
            return
        detail = result["detail"]
        if result["ok"]:
            QMessageBox.information(self, t("settings.daemon_restarted_title"), detail or t("settings.daemon_restarted_default"))
        else:
            QMessageBox.warning(self, t("settings.restart_failed_title"), detail or t("settings.restart_failed_default"))

    # --- conflict cleanup ---

    _CONFLICT_TAG = " (conflitto da "

    def _scan_conflicts(self) -> None:
        local_root = self.cfg.local_root()
        if not local_root:
            self.conflict_count_label.setText(t("settings.maint_conflict_no_folder"))
            return
        self.maint_scan_btn.setEnabled(False)
        self.conflict_count_label.setText(t("settings.maint_conflict_scanning"))
        run_in_background(
            self, "_conflict_scan_call",
            lambda: _conflict_scan_worker(local_root),
            self._on_conflict_scan_done,
        )

    def _on_conflict_scan_done(self, result, exc: Exception | None) -> None:
        self.maint_scan_btn.setEnabled(True)
        if exc is not None:
            self.conflict_count_label.setText(t("settings.maint_conflict_error", detail=str(exc)))
            return
        count = len(result)
        self._conflict_files = result
        if count == 0:
            self.conflict_count_label.setText(t("settings.maint_conflict_count_none"))
            self.maint_delete_btn.setEnabled(False)
        else:
            self.conflict_count_label.setText(t("settings.maint_conflict_count", count=count))
            self.maint_delete_btn.setEnabled(True)

    def _delete_conflicts(self) -> None:
        count = len(self._conflict_files)
        local_root = self.cfg.local_root()
        if count == 0 or not local_root:
            return
        dialog = ConflictReviewDialog(self._conflict_files, local_root, self)
        if dialog.exec() != QDialog.DialogCode.Accepted:
            return
        selected = dialog.selected_files()
        if not selected:
            return
        moved = 0
        failed: list[str] = []
        for file_path in selected:
            try:
                relative = str(file_path.relative_to(local_root))
            except ValueError:
                relative = str(file_path)
            if trash.move_to_local_trash(file_path, local_root):
                moved += 1
            else:
                failed.append(relative)
        if failed:
            QMessageBox.warning(
                self,
                t("settings.maint_delete_partial_title"),
                t("settings.maint_delete_partial_body", moved=moved, failed=len(failed), names="\n".join(failed[:10])),
            )
        else:
            QMessageBox.information(
                self, t("settings.maint_delete_done_title"), t("settings.maint_delete_done_body", count=moved),
            )
        self._conflict_files = [path for path in self._conflict_files if path.exists()]
        self._on_conflict_scan_done(self._conflict_files, None)


class ConflictReviewDialog(QDialog):
    def __init__(self, files: list[Path], local_root: str, parent=None) -> None:
        super().__init__(parent)
        self.setWindowTitle(t("settings.maint_review_title"))
        self.resize(650, 400)
        layout = QVBoxLayout(self)
        note = QLabel(t("settings.maint_review_note"))
        note.setWordWrap(True)
        layout.addWidget(note)
        self.list = QListWidget()
        self.list.setSelectionMode(QAbstractItemView.SelectionMode.ExtendedSelection)
        root = Path(local_root)
        for file_path in files:
            try:
                label = str(file_path.relative_to(root))
            except ValueError:
                label = str(file_path)
            item = QListWidgetItem(label)
            item.setData(Qt.ItemDataRole.UserRole, file_path)
            self.list.addItem(item)
        self.list.itemSelectionChanged.connect(self._selection_changed)
        layout.addWidget(self.list)

        buttons = QDialogButtonBox(QDialogButtonBox.StandardButton.Cancel)
        self.open_btn = buttons.addButton(t("settings.maint_open_folder_btn"), QDialogButtonBox.ButtonRole.ActionRole)
        self.move_btn = buttons.addButton(t("settings.maint_move_selected_btn"), QDialogButtonBox.ButtonRole.AcceptRole)
        self.open_btn.clicked.connect(self._open_containing_folder)
        self.move_btn.clicked.connect(self.accept)
        buttons.rejected.connect(self.reject)
        layout.addWidget(buttons)
        self._selection_changed()

    def selected_files(self) -> list[Path]:
        return [item.data(Qt.ItemDataRole.UserRole) for item in self.list.selectedItems()]

    def _selection_changed(self) -> None:
        selected = bool(self.list.selectedItems())
        self.open_btn.setEnabled(selected)
        self.move_btn.setEnabled(selected)

    def _open_containing_folder(self) -> None:
        selected = self.selected_files()
        if selected:
            QDesktopServices.openUrl(QUrl.fromLocalFile(str(selected[0].parent)))


def _detect_worker(cfg: Config, script_path_hint: str) -> dict:
    """Runs off the GUI thread (see _detect_from_nas) -- must not touch any
    widget, only cfg (thread-safe, see Config's own lock) and the network."""
    conn = rsync_ops.resolve_connection(cfg)
    if conn is None:
        return {"connected": False}

    found = rsync_ops.discover_remote_scripts(cfg, conn)
    candidates = []
    if script_path_hint:
        candidates.append(script_path_hint)
    candidates.extend(path for path in found if path not in candidates)
    if not candidates:
        return {"connected": True, "daemon_not_found": True}

    errors = []
    for script_path in candidates:
        # The script may have been discovered only seconds ago and is not
        # persisted until the GUI callback runs, so pass it explicitly rather
        # than reading the still-empty config value.
        ok, values, err = trash.fetch_remote_config(cfg, conn, script_path)
        if ok:
            return {
                "connected": True,
                "script_path": script_path,
                "multiple_found": found if len(found) > 1 else None,
                "fetch_ok": True,
                "values": values,
                "err": "",
            }
        errors.append(f"{script_path}: {err}")

    return {
        "connected": True,
        "script_path": candidates[0],
        "multiple_found": found if len(found) > 1 else None,
        "fetch_ok": False,
        "values": {},
        "err": "\n".join(errors) or "nessuno script server ha risposto correttamente",
    }


def _restart_worker(cfg: Config, script_path: str) -> dict:
    """Runs off the GUI thread (see _restart_daemon)."""
    conn = rsync_ops.resolve_connection(cfg)
    if conn is None:
        return {"connected": False}
    ok, stdout, stderr = rsync_ops.run_remote_script(cfg, conn, script_path, ["--restart"], timeout=30)
    return {"connected": True, "ok": ok, "detail": (stdout + stderr).strip()}


_CONFLICT_TAG = " (conflitto da "


def _conflict_scan_worker(local_root: str) -> list[Path]:
    result: list[Path] = []
    root = Path(local_root)
    for entry in root.rglob("*"):
        if entry.is_file() and _CONFLICT_TAG in entry.name:
            result.append(entry)
    return result
