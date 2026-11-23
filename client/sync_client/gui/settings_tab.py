from __future__ import annotations

import json
import re
import zipfile
from datetime import datetime

from PyQt6.QtCore import Qt, QUrl
from PyQt6.QtGui import QDesktopServices, QKeySequence, QShortcut
from PyQt6.QtWidgets import (
    QAbstractItemView, QApplication, QBoxLayout, QCheckBox, QComboBox, QDialog,
    QDialogButtonBox, QFileDialog, QFormLayout, QGroupBox, QHBoxLayout, QLabel,
    QLineEdit, QListWidget, QListWidgetItem, QMessageBox, QPushButton, QScrollArea,
    QSpinBox, QVBoxLayout, QWidget,
)

from pathlib import Path

from .. import paths, rsync_ops, trash
from ..config import Config, CONFLICT_POLICY_OPTIONS, SYNC_MODE_OPTIONS, normalize_schedule
from ..engine import SyncEngine
from ..i18n import t
from ..version import APP_NAME, APP_VERSION
from ..repository_safety import RepositorySafetyError, bind_repository
from .async_utils import run_in_background
from .dialogs import ScheduleWindowDialog, SelectiveSyncDialog, WEEKDAY_KEYS
from .style import apply_style

LANGUAGE_OPTIONS = [
    ("auto", "settings.language_auto"),
    ("it", "settings.language_it"),
    ("en", "settings.language_en"),
]

THEME_OPTIONS = [
    ("system", "settings.theme_system"),
    ("light", "settings.theme_light"),
    ("dark", "settings.theme_dark"),
]

TRAY_CLICK_OPTIONS = [
    ("menu", "settings.tray_click_menu"),
    ("window", "settings.tray_click_window"),
]

COMPRESSION_OPTIONS = [
    ("auto", "settings.compression_auto"),
    ("on", "settings.compression_on"),
    ("off", "settings.compression_off"),
]

# One-click bundles for the churn NASBox sees most often. These are ordinary
# exclude patterns: adding a profile is visible in the list, removable, and
# saved exactly like a manually typed one.
EXCLUSION_PROFILES = [
    ("latex", "settings.profile_latex", [
        "*.aux", "*.log", "*.idx", "*.ind", "*.ilg", "*.toc", "*.out",
        "*.fls", "*.fdb_latexmk", "*.synctex.gz", "*.lot", "*.lof",
        "*.bbl", "*.blg", "*.xdv", "*.nav", "*.snm", "*.vrb", "*.bcf",
        "*.run.xml",
    ]),
    ("dev", "settings.profile_dev", [
        ".git/", "node_modules/", ".venv/", "venv/", "__pycache__/",
        "build/", "dist/", "target/", ".gradle/", ".idea/", ".vscode/",
        ".next/", ".cache/", "*.pyc", "*.class", "*.o", "*.obj",
    ]),
    ("office", "settings.profile_office", [
        ".~lock.*", "~$*", "*.tmp.*", "*.part", "*.crdownload",
    ]),
    ("os", "settings.profile_os", [
        ".DS_Store", "Thumbs.db", "desktop.ini", "@eaDir/",
    ]),
]

# Dynamic-property marker used to tint fields that match the settings search.
_SEARCH_MATCH_PROPERTY = "settingsSearchMatch"
_SEARCH_MATCH_STYLE = (
    'QWidget[settingsSearchMatch="true"] {'
    ' background-color: #fff2cc; border-radius: 3px; }'
)

# A bandwidth cap is only a few digits: keep both spins (and their labels)
# compact and in a single row instead of letting them span the whole column.
_BW_FIELD_MAX_WIDTH = 100

# A diagnostic bundle is meant to be pasted into an issue: never dump secrets
# or a full known_hosts file, even if a key happens to have an unexpected name.
_SENSITIVE_KEY_TOKENS = (
    "password", "passwd", "token", "secret", "credential", "private",
    "known_hosts", "api_key", "apikey",
)
_BUNDLE_LOG_LINES = 200
_BUNDLE_INNER_NAME = "diagnostics.txt"
_PRIVATE_KEY_LINE = re.compile(r"BEGIN [A-Z ]*PRIVATE KEY")
_SSH_KEY_BLOB = re.compile(r"\b(?:ssh-(?:rsa|ed25519)|ecdsa-sha2-\S+|sk-ssh-ed25519) [A-Za-z0-9+/=]+")
_LOG_SECRET = re.compile(r"(?i)\b(password|passwd|token|secret|api[_-]?key)\b(\s*[=:]\s*)(\S+)")


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
        self._schedule_windows = cfg.pause_schedule()
        self._selective_excludes = cfg.selective_sync_excludes()

        # Search + collapsible sections bookkeeping (see _make_section and
        # _apply_search_filter). Kept on the instance so helpers can be unit
        # tested without rebuilding the whole tab.
        self._sections: list[QGroupBox] = []
        self._section_keys: dict[int, str] = {}
        self._collapse_state: dict[QGroupBox, bool] | None = None
        self._highlighted: set[QWidget] = set()

        outer = QVBoxLayout(self)
        outer.setContentsMargins(0, 0, 0, 0)
        outer.setSpacing(0)

        # --- settings search (Ctrl+K) ---
        # The filter lives OUTSIDE the scroll area: with many collapsible
        # groups the content is still taller than the window, and having to
        # scroll back to the top to reach the search field defeated Ctrl+K.
        self._search_header = QWidget()
        self._search_header.setObjectName("settingsSearchHeader")
        header_layout = QHBoxLayout(self._search_header)
        header_layout.setContentsMargins(6, 6, 6, 2)
        header_layout.setSpacing(6)
        search_label = QLabel(t("transfers.search_label"))
        search_label.setObjectName("settingsSearchLabel")
        header_layout.addWidget(search_label)
        self.search_edit = QLineEdit()
        self.search_edit.setObjectName("settingsSearchEdit")
        self.search_edit.setClearButtonEnabled(True)
        self.search_edit.setPlaceholderText(t("settings.search_placeholder"))
        self.search_edit.setToolTip(t("settings.search_tooltip"))
        self.search_edit.setAccessibleName(t("settings.search_placeholder"))
        self.search_edit.textChanged.connect(self._apply_search_filter)
        header_layout.addWidget(self.search_edit, 1)
        outer.addWidget(self._search_header)
        self.search_shortcut = QShortcut(QKeySequence("Ctrl+K"), self)
        self.search_shortcut.activated.connect(self._focus_search)

        # This tab stacks a lot of group boxes -- without a scroll area, Qt's
        # layout system reports their combined minimum height as *this tab's*
        # minimum height, which propagates all the way up to the main window
        # and makes it impossible to shrink below "tall enough to show every
        # field at once". A scroll area's own minimum height is small
        # regardless of what's inside it, so the window stays freely
        # resizable and this tab scrolls internally instead.
        scroll = QScrollArea()
        scroll.setWidgetResizable(True)
        scroll.setFrameShape(QScrollArea.Shape.NoFrame)
        self._scroll_area = scroll
        outer.addWidget(scroll)

        container = QWidget()
        scroll.setWidget(container)
        # The highlight rules are added on top of the application stylesheet,
        # so the light/dark theme keeps styling every other widget.
        container.setStyleSheet(_SEARCH_MATCH_STYLE)
        root = QVBoxLayout(container)
        root.setContentsMargins(6, 4, 6, 4)
        root.setSpacing(4)

        # Two columns as before, but with the groups redistributed and every
        # short field capped in width: NAS + bandwidth + exclusions on the
        # left, all the remaining compact groups on the right. Every section
        # starts expanded -- there is no checkbox to click just to see a
        # setting. Keeping a QHBoxLayout of two QVBoxLayouts also lets
        # _update_column_layout stack them on a narrow window without
        # rebuilding the widget tree.
        columns = QHBoxLayout()
        self._columns = columns
        columns.setContentsMargins(0, 0, 0, 0)
        columns.setSpacing(6)
        left_col = QVBoxLayout()
        right_col = QVBoxLayout()
        columns.addLayout(left_col, 1)
        columns.addLayout(right_col, 1)
        root.addLayout(columns)

        # Remember which column each section belongs to (used by diagnostics
        # and tests); every section starts expanded, see
        # _apply_initial_collapse.
        left_sections: list[QGroupBox] = []
        right_sections: list[QGroupBox] = []

        # --- NAS connection ---
        nas_box = self._make_section("nas", t("settings.nas_box_title"))
        nas_form = QFormLayout(nas_box)
        self._tight_layout(nas_form)
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
        self._cap_field(self.ssh_port, 90)
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
        self._cap_field(self.jump_port, 90)
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

        self.sync_mode_combo = QComboBox()
        current_sync_mode = cfg.sync_mode()
        for index, (value, label_key) in enumerate(SYNC_MODE_OPTIONS):
            self.sync_mode_combo.addItem(t(label_key), userData=value)
            if value == current_sync_mode:
                self.sync_mode_combo.setCurrentIndex(index)
        self.sync_mode_combo.setToolTip(t("settings.sync_mode_note"))
        self._cap_field(self.sync_mode_combo, 200)
        nas_form.addRow(t("settings.sync_mode_label"), self.sync_mode_combo)
        sync_mode_note = QLabel(t("settings.sync_mode_note"))
        sync_mode_note.setWordWrap(True)
        nas_form.addRow(sync_mode_note)

        self.delete_enabled = QCheckBox(t("settings.delete_enabled_checkbox"))
        self.delete_enabled.setChecked(bool(cfg.get("delete_enabled")))
        self.delete_enabled.setToolTip(t("settings.delete_enabled_tooltip"))
        nas_form.addRow(self.delete_enabled)
        self.max_delete_spin = QSpinBox()
        self.max_delete_spin.setRange(1, 1_000_000)
        self.max_delete_spin.setValue(int(cfg.get("max_delete_files") or 1000))
        self.max_delete_spin.setToolTip(t("settings.max_delete_tooltip"))
        self._cap_field(self.max_delete_spin, 110)
        nas_form.addRow(t("settings.max_delete_label"), self.max_delete_spin)

        self.conflict_policy_combo = QComboBox()
        current_policy = cfg.conflict_policy()
        for index, (value, label_key) in enumerate(CONFLICT_POLICY_OPTIONS):
            self.conflict_policy_combo.addItem(t(label_key), userData=value)
            if value == current_policy:
                self.conflict_policy_combo.setCurrentIndex(index)
        self.conflict_policy_combo.setToolTip(t("settings.conflict_policy_tooltip"))
        self._cap_field(self.conflict_policy_combo, 200)
        nas_form.addRow(t("settings.conflict_policy_label"), self.conflict_policy_combo)
        conflict_policy_note = QLabel(t("settings.conflict_policy_tooltip"))
        conflict_policy_note.setWordWrap(True)
        nas_form.addRow(conflict_policy_note)
        left_col.addWidget(nas_box)
        left_sections.append(nas_box)

        # --- bandwidth (left column, under NAS; exclusions follow right below) ---
        bw_box = self._make_section("bandwidth", t("settings.bandwidth_title"))
        bw_layout = QHBoxLayout(bw_box)
        self._tight_layout(bw_layout)
        self.bw_upload_label = QLabel(t("settings.upload_label"))
        self._cap_field(self.bw_upload_label, _BW_FIELD_MAX_WIDTH)
        bw_layout.addWidget(self.bw_upload_label)
        self.bw_upload_spin = QSpinBox()
        self.bw_upload_spin.setRange(0, 1_000_000)
        self.bw_upload_spin.setSingleStep(64)
        self.bw_upload_spin.setValue(int(cfg.get("bandwidth_upload_kbps") or 0))
        self.bw_upload_spin.setToolTip(t("settings.bandwidth_tooltip"))
        self._cap_field(self.bw_upload_spin, _BW_FIELD_MAX_WIDTH)
        bw_layout.addWidget(self.bw_upload_spin)
        self.bw_download_label = QLabel(t("settings.download_label"))
        self._cap_field(self.bw_download_label, _BW_FIELD_MAX_WIDTH)
        bw_layout.addWidget(self.bw_download_label)
        self.bw_download_spin = QSpinBox()
        self.bw_download_spin.setRange(0, 1_000_000)
        self.bw_download_spin.setSingleStep(64)
        self.bw_download_spin.setValue(int(cfg.get("bandwidth_download_kbps") or 0))
        self.bw_download_spin.setToolTip(t("settings.bandwidth_tooltip"))
        self._cap_field(self.bw_download_spin, _BW_FIELD_MAX_WIDTH)
        bw_layout.addWidget(self.bw_download_spin)
        # All four controls stay packed on one row; unused space goes to the
        # stretch at the end, never to the fields themselves.
        bw_layout.addStretch(1)
        left_col.addWidget(bw_box)
        left_sections.append(bw_box)

        # --- exclusions: directly under bandwidth, same (left) column ---
        exclude_box = self._make_section("exclude", t("settings.exclude_title"))
        exclude_layout = QVBoxLayout(exclude_box)
        self._tight_layout(exclude_layout)
        exclude_note = QLabel(t("settings.exclude_note"))
        exclude_note.setWordWrap(True)
        exclude_layout.addWidget(exclude_note)
        self.exclude_list = QListWidget()
        self.exclude_list.setMaximumHeight(90)
        self.exclude_list.setToolTip(t("settings.exclude_note"))
        self.exclude_list.addItems(cfg.exclude_patterns())
        exclude_layout.addWidget(self.exclude_list)
        profile_row = QHBoxLayout()
        profile_row.addWidget(QLabel(t("settings.exclude_profile_label")))
        self.exclusion_profile_combo = QComboBox()
        for key, label_key, patterns in EXCLUSION_PROFILES:
            self.exclusion_profile_combo.addItem(t(label_key), userData=list(patterns))
        self.exclusion_profile_combo.setToolTip(t("settings.exclude_profile_tooltip"))
        self._cap_field(self.exclusion_profile_combo, 200)
        profile_row.addWidget(self.exclusion_profile_combo, 1)
        add_profile_btn = QPushButton(t("settings.exclude_profile_add"))
        add_profile_btn.setToolTip(t("settings.exclude_profile_tooltip"))
        add_profile_btn.clicked.connect(self._add_exclusion_profile)
        profile_row.addWidget(add_profile_btn)
        exclude_layout.addLayout(profile_row)
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

        selective_row = QHBoxLayout()
        self.selective_btn = QPushButton(t("settings.selective_btn"))
        self.selective_btn.setToolTip(t("settings.selective_tooltip"))
        self.selective_btn.clicked.connect(self._choose_selective_folders)
        selective_row.addWidget(self.selective_btn)
        self.selective_label = QLabel()
        self.selective_label.setWordWrap(True)
        selective_row.addWidget(self.selective_label, 1)
        exclude_layout.addLayout(selective_row)

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
        self._refresh_selective_label()
        left_col.addWidget(exclude_box)
        left_sections.append(exclude_box)

        # --- sync cadence ---
        cadence_box = self._make_section("cadence", t("settings.cadence_title"))
        cadence_form = QFormLayout(cadence_box)
        self._tight_layout(cadence_form)
        self.poll_spin = QSpinBox()
        self.poll_spin.setRange(5, 3600)
        self.poll_spin.setSuffix(" s")
        self.poll_spin.setValue(int(cfg.get("poll_interval") or 60))
        self.poll_spin.setToolTip(t("settings.cadence_note"))
        self._cap_field(self.poll_spin, 100)
        cadence_form.addRow(t("settings.poll_label"), self.poll_spin)
        self.metrics_refresh_spin = QSpinBox()
        self.metrics_refresh_spin.setRange(5, 3600)
        self.metrics_refresh_spin.setSuffix(" s")
        self.metrics_refresh_spin.setValue(int(cfg.get("metrics_refresh_seconds") or 15))
        self.metrics_refresh_spin.setToolTip(t("settings.metrics_refresh_note"))
        self._cap_field(self.metrics_refresh_spin, 100)
        cadence_form.addRow(t("settings.metrics_refresh_label"), self.metrics_refresh_spin)
        self.compression_combo = QComboBox()
        current_compression = str(cfg.get("compression_mode") or "auto")
        for index, (value, label_key) in enumerate(COMPRESSION_OPTIONS):
            self.compression_combo.addItem(t(label_key), userData=value)
            if value == current_compression:
                self.compression_combo.setCurrentIndex(index)
        self.compression_combo.setToolTip(t("settings.compression_tooltip"))
        self._cap_field(self.compression_combo, 200)
        cadence_form.addRow(t("settings.compression_label"), self.compression_combo)
        self.full_pull_spin = QSpinBox()
        self.full_pull_spin.setRange(300, 86400)
        self.full_pull_spin.setSingleStep(900)
        self.full_pull_spin.setSuffix(" s")
        self.full_pull_spin.setValue(int(cfg.get("full_pull_interval_seconds") or 7200))
        self.full_pull_spin.setToolTip(t("settings.full_pull_note"))
        self._cap_field(self.full_pull_spin, 100)
        cadence_form.addRow(t("settings.full_pull_label"), self.full_pull_spin)
        self.nas_friendly_btn = QPushButton(t("settings.nas_friendly_btn"))
        self.nas_friendly_btn.setToolTip(t("settings.nas_friendly_tooltip"))
        self.nas_friendly_btn.clicked.connect(self._apply_nas_friendly_profile)
        cadence_form.addRow(self.nas_friendly_btn)
        self.multiplexing_checkbox = QCheckBox(t("settings.multiplexing_checkbox"))
        self.multiplexing_checkbox.setChecked(bool(cfg.get("ssh_multiplexing", True)))
        self.multiplexing_checkbox.setToolTip(t("settings.multiplexing_tooltip"))
        cadence_form.addRow(self.multiplexing_checkbox)
        cadence_note = QLabel(t("settings.cadence_note"))
        cadence_note.setWordWrap(True)
        cadence_form.addRow(cadence_note)
        right_col.addWidget(cadence_box)
        right_sections.append(cadence_box)

        # --- scheduled pause ---
        schedule_box = self._make_section("schedule", t("settings.schedule_title"))
        schedule_layout = QVBoxLayout(schedule_box)
        self._tight_layout(schedule_layout)
        self.schedule_enabled = QCheckBox(t("settings.schedule_enabled_checkbox"))
        self.schedule_enabled.setChecked(bool(cfg.get("pause_schedule_enabled")))
        self.schedule_enabled.setToolTip(t("settings.schedule_enabled_tooltip"))
        schedule_layout.addWidget(self.schedule_enabled)
        self.schedule_list = QListWidget()
        self.schedule_list.setSelectionMode(QAbstractItemView.SelectionMode.SingleSelection)
        self.schedule_list.setMaximumHeight(90)
        self.schedule_list.setToolTip(t("settings.schedule_list_tooltip"))
        schedule_layout.addWidget(self.schedule_list)
        schedule_buttons = QHBoxLayout()
        schedule_add_btn = QPushButton(t("settings.schedule_add_btn"))
        schedule_add_btn.setToolTip(t("settings.schedule_add_tooltip"))
        schedule_add_btn.clicked.connect(self._add_schedule_window)
        schedule_edit_btn = QPushButton(t("settings.schedule_edit_btn"))
        schedule_edit_btn.setToolTip(t("settings.schedule_edit_tooltip"))
        schedule_edit_btn.clicked.connect(self._edit_schedule_window)
        schedule_remove_btn = QPushButton(t("settings.schedule_remove_btn"))
        schedule_remove_btn.setToolTip(t("settings.schedule_remove_tooltip"))
        schedule_remove_btn.clicked.connect(self._remove_schedule_window)
        schedule_buttons.addWidget(schedule_add_btn)
        schedule_buttons.addWidget(schedule_edit_btn)
        schedule_buttons.addWidget(schedule_remove_btn)
        schedule_buttons.addStretch(1)
        schedule_layout.addLayout(schedule_buttons)
        schedule_note = QLabel(t("settings.schedule_note"))
        schedule_note.setWordWrap(True)
        schedule_layout.addWidget(schedule_note)
        self._refresh_schedule_list()
        right_col.addWidget(schedule_box)
        right_sections.append(schedule_box)

        # --- sync feedback ---
        feedback_box = self._make_section("feedback", t("settings.feedback_title"))
        feedback_layout = QVBoxLayout(feedback_box)
        self._tight_layout(feedback_layout)
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
        self._cap_field(self.tray_click_combo, 200)
        tray_click_row.addWidget(self.tray_click_combo, 1)
        feedback_layout.addLayout(tray_click_row)
        feedback_note = QLabel(t("settings.notify_sync_note"))
        feedback_note.setWordWrap(True)
        feedback_layout.addWidget(feedback_note)
        right_col.addWidget(feedback_box)
        right_sections.append(feedback_box)

        # --- language ---
        language_box = self._make_section("language", t("settings.language_title"))
        language_form = QFormLayout(language_box)
        self._tight_layout(language_form)
        self.language_combo = QComboBox()
        current_lang = cfg.language()
        for index, (code, label_key) in enumerate(LANGUAGE_OPTIONS):
            self.language_combo.addItem(t(label_key), userData=code)
            if code == current_lang:
                self.language_combo.setCurrentIndex(index)
        self.language_combo.setToolTip(t("settings.language_note"))
        self._cap_field(self.language_combo, 200)
        language_form.addRow(t("settings.language_label"), self.language_combo)
        language_note = QLabel(t("settings.language_note"))
        language_note.setWordWrap(True)
        language_form.addRow(language_note)
        right_col.addWidget(language_box)
        right_sections.append(language_box)

        # --- theme: light stays the default so the existing look is preserved ---
        theme_box = self._make_section("theme", t("settings.theme_title"))
        theme_form = QFormLayout(theme_box)
        self._tight_layout(theme_form)
        self.theme_combo = QComboBox()
        current_theme = cfg.get("theme", "light")
        for index, (value, label_key) in enumerate(THEME_OPTIONS):
            self.theme_combo.addItem(t(label_key), userData=value)
            if value == current_theme:
                self.theme_combo.setCurrentIndex(index)
        self.theme_combo.setToolTip(t("settings.theme_note"))
        self.theme_combo.setAccessibleName(t("settings.theme_label"))
        self._cap_field(self.theme_combo, 200)
        theme_form.addRow(t("settings.theme_label"), self.theme_combo)
        theme_note = QLabel(t("settings.theme_note"))
        theme_note.setWordWrap(True)
        theme_form.addRow(theme_note)
        right_col.addWidget(theme_box)
        right_sections.append(theme_box)

        # --- info ---
        info_box = self._make_section("info", t("settings.info_title"))
        info_form = QFormLayout(info_box)
        self._tight_layout(info_form)
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
        self.export_diag_btn = QPushButton(t("settings.export_diagnostics_btn"))
        self.export_diag_btn.setToolTip(t("settings.export_diagnostics_tooltip"))
        self.export_diag_btn.clicked.connect(self._export_diagnostics)
        info_form.addRow(self.export_diag_btn)
        right_col.addWidget(info_box)
        right_sections.append(info_box)

        # --- NAS-reported values (read-only, never re-fetched here) ---
        nas_facts_box = self._make_section("nas_facts", t("settings.nas_facts_title"))
        nas_facts_form = QFormLayout(nas_facts_box)
        self._tight_layout(nas_facts_form)
        self.nas_fact_retention = QLabel()
        self.nas_fact_retention.setWordWrap(True)
        nas_facts_form.addRow(t("settings.nas_fact_retention_label"), self.nas_fact_retention)
        self.nas_fact_server_version = QLabel()
        self.nas_fact_server_version.setWordWrap(True)
        nas_facts_form.addRow(t("settings.nas_fact_server_version_label"), self.nas_fact_server_version)
        self.nas_fact_lock = QLabel()
        self.nas_fact_lock.setWordWrap(True)
        nas_facts_form.addRow(t("settings.nas_fact_lock_label"), self.nas_fact_lock)
        nas_facts_note = QLabel(t("settings.nas_facts_note"))
        nas_facts_note.setWordWrap(True)
        nas_facts_form.addRow(nas_facts_note)
        self._refresh_nas_facts()
        right_col.addWidget(nas_facts_box)
        right_sections.append(nas_facts_box)

        # --- maintenance ---
        maint_box = self._make_section("maintenance", t("settings.maint_title"))
        maint_layout = QVBoxLayout(maint_box)
        self._tight_layout(maint_layout)
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
        right_sections.append(maint_box)

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
        self._column_sections = [left_sections, right_sections]
        self._connect_dirty_signals()
        self._apply_initial_collapse()
        self._apply_search_filter("")
        self._update_column_layout(self.width())

    # --- search / collapsible sections ---

    def _make_section(self, key: str, title: str) -> QGroupBox:
        """A checkable group: the title checkbox collapses/expands its fields.

        The check state is only a view preference -- it never touches cfg, so
        collapsing a section cannot alter the value of any setting inside it.
        """
        box = QGroupBox(title)
        box.setObjectName(f"settingsSection_{key}")
        box.setCheckable(True)
        box.setChecked(True)
        box.toggled.connect(
            lambda checked, section=box: self._on_section_toggled(section, checked)
        )
        self._sections.append(box)
        self._section_keys[id(box)] = key
        return box

    @staticmethod
    def _tight_layout(layout) -> None:
        """Group boxes stay short: small contents margins and tight spacing."""
        layout.setContentsMargins(6, 6, 6, 6)
        layout.setSpacing(4)

    @staticmethod
    def _cap_field(widget: QWidget, width: int) -> None:
        """Ports, timeouts and other short values must not span a full column."""
        widget.setMaximumWidth(width)

    def _apply_initial_collapse(self) -> None:
        """No-op for collapsing: every section starts expanded and visible.

        Kept (and still called from __init__) rather than deleted so existing
        callers keep working, but the old "at most two open groups per column"
        behaviour is disabled. A user can still tick a group's checkbox by
        hand; the application itself never collapses a section on startup.
        """
        for section in self._sections:
            section.setChecked(True)
            for child in self._section_fields(section):
                child.setVisible(True)

    @staticmethod
    def _section_fields(section: QGroupBox) -> list[QWidget]:
        # Direct children only: findChildren() would also return the private
        # popups of QComboBox/QListView and showing/hiding those is not ours to do.
        return [child for child in section.children() if isinstance(child, QWidget)]

    def _on_section_toggled(self, section: QGroupBox, checked: bool) -> None:
        # QGroupBox disables its children when unchecked; hiding them as well
        # makes the group genuinely collapse (and reclaim layout space).
        for child in self._section_fields(section):
            child.setVisible(checked)

    def _focus_search(self) -> None:
        self.search_edit.setFocus(Qt.FocusReason.ShortcutFocusReason)
        self.search_edit.selectAll()

    @staticmethod
    def _widget_search_text(widget: QWidget) -> str:
        """Every bit of visible text attached to a field, for search/highlight."""
        parts: list[str] = [widget.toolTip(), widget.accessibleName()]
        text_getter = getattr(widget, "text", None)
        if callable(text_getter):
            try:
                parts.append(str(text_getter()))
            except (TypeError, ValueError):
                pass
        placeholder = getattr(widget, "placeholderText", None)
        if callable(placeholder):
            parts.append(placeholder())
        if isinstance(widget, QComboBox):
            parts.extend(
                widget.itemText(index) for index in range(widget.count())
            )
        if isinstance(widget, QGroupBox):
            parts.append(widget.title())
        return " ".join(part for part in parts if part).casefold()

    def _section_search_text(self, section: QGroupBox) -> str:
        parts = [section.title(), section.toolTip()]
        parts.extend(
            self._widget_search_text(child) for child in self._section_fields(section)
        )
        return " ".join(part for part in parts if part).casefold()

    def _set_match_highlight(self, widget: QWidget, matched: bool) -> None:
        widget.setProperty(_SEARCH_MATCH_PROPERTY, matched)
        if matched:
            self._highlighted.add(widget)
        else:
            self._highlighted.discard(widget)
        style = widget.style()
        style.unpolish(widget)
        style.polish(widget)
        widget.update()

    def _clear_highlights(self, section: QGroupBox | None = None) -> None:
        widgets = (
            list(self._highlighted)
            if section is None
            else [w for w in self._highlighted if section.isAncestorOf(w)]
        )
        for widget in widgets:
            self._set_match_highlight(widget, False)

    def _apply_search_filter(self, query: str) -> None:
        """Hide non-matching sections and tint the matching fields."""
        needle = query.strip().casefold()
        if not needle:
            if self._collapse_state is not None:
                for section, was_checked in self._collapse_state.items():
                    section.setChecked(was_checked)
                self._collapse_state = None
            for section in self._sections:
                section.setVisible(True)
            self._clear_highlights()
            return

        if self._collapse_state is None:
            # Remember the user's collapse choices so clearing the search puts
            # them back exactly as they were.
            self._collapse_state = {
                section: section.isChecked() for section in self._sections
            }
        self._clear_highlights()
        for section in self._sections:
            matched = needle in self._section_search_text(section)
            section.setVisible(matched)
            if not matched:
                continue
            if not section.isChecked():
                section.setChecked(True)  # toggled handler also reveals fields
            for child in self._section_fields(section):
                if needle in self._widget_search_text(child):
                    self._set_match_highlight(child, True)

    # --- NAS-reported values (cfg only, no network) ---

    def _nas_reported(self, value: str) -> str:
        return t("settings.nas_reported_value", value=value)

    def _refresh_nas_facts(self) -> None:
        cfg = self.cfg
        retention = cfg.get("retention_days_remote", 0)
        try:
            days = int(retention or 0)
        except (TypeError, ValueError):
            days = 0
        retention_text = (
            t("settings.retention_never") if days <= 0
            else t("settings.retention_days", days=days)
        )
        self.nas_fact_retention.setText(self._nas_reported(retention_text))

        server_version = str(cfg.get("server_version", "") or "").strip()
        if not server_version:
            version_text = t("settings.nas_not_reported")
        else:
            version_text = self._nas_reported(server_version)
            if rsync_ops.server_is_outdated(server_version):
                version_text += " " + t(
                    "settings.outdated_version_note",
                    version=server_version,
                    expected=rsync_ops.EXPECTED_SERVER_VERSION,
                )
        self.nas_fact_server_version.setText(version_text)

        if cfg.get("server_lock_held"):
            owner = str(cfg.get("server_lock_owner_id", "") or "").strip()
            host = str(cfg.get("server_lock_owner_host", "") or "").strip()
            owner_text = " / ".join(part for part in (owner, host) if part)
            if not owner_text:
                owner_text = str(cfg.get("server_lock_owner_pid", "") or "?")
            age = cfg.get("server_lock_age_seconds", 0)
            lock_text = t(
                "settings.nas_lock_held", owner=owner_text, age=str(age or 0)
            )
        else:
            lock_text = t("settings.nas_lock_free")
        self.nas_fact_lock.setText(self._nas_reported(lock_text))

    # --- diagnostic bundle ---

    @staticmethod
    def _is_sensitive_key(key: str) -> bool:
        lowered = key.casefold()
        return (
            any(token in lowered for token in _SENSITIVE_KEY_TOKENS)
            or lowered.endswith("_key")
            or lowered == "key"
        )

    @classmethod
    def _redact_text(cls, text: str) -> str:
        redacted = t("settings.export_redacted")
        result: list[str] = []
        in_private_key = False
        for line in text.splitlines():
            if _PRIVATE_KEY_LINE.search(line):
                in_private_key = True
                result.append(redacted)
                continue
            if in_private_key:
                if "PRIVATE KEY" in line:  # the -----END ...----- line
                    in_private_key = False
                result.append(redacted)
                continue
            if _SSH_KEY_BLOB.search(line):
                result.append(_SSH_KEY_BLOB.sub(redacted, line))
                continue
            result.append(
                _LOG_SECRET.sub(
                    lambda m: f"{m.group(1)}{m.group(2)}{redacted}", line
                )
            )
        return "\n".join(result)

    def _sanitize_value(self, value: object) -> object:
        """Strip secrets from nested dict/list config values too (e.g. mirrors)."""
        if isinstance(value, dict):
            return {
                str(key): (
                    t("settings.export_redacted") if self._is_sensitive_key(str(key))
                    else self._sanitize_value(item)
                )
                for key, item in value.items()
            }
        if isinstance(value, list):
            return [self._sanitize_value(item) for item in value]
        return value

    def _config_snapshot(self) -> dict[str, object]:
        """cfg as plain data, with credentials and host keys stripped out."""
        data = getattr(self.cfg, "_data", None)
        if not isinstance(data, dict):
            return {}
        snapshot: dict[str, object] = {}
        for key in sorted(data):
            if self._is_sensitive_key(str(key)):
                snapshot[str(key)] = t("settings.export_redacted")
            else:
                snapshot[str(key)] = self._sanitize_value(data[key])
        return snapshot

    def _diagnostic_bundle_text(self) -> str:
        lines = [
            t("settings.export_bundle_title"),
            "=" * 40,
            t("settings.export_generated", timestamp=datetime.now().isoformat(timespec="seconds")),
            "",
            f"[{t('settings.export_section_versions')}]",
            f"{t('settings.info_version_label')} {APP_NAME} {APP_VERSION}",
            f"{t('settings.info_min_server_label')} {rsync_ops.EXPECTED_SERVER_VERSION}",
            f"{t('settings.nas_fact_server_version_label')} "
            f"{self.nas_fact_server_version.text()}",
            "",
            f"[{t('settings.export_section_config')}]",
        ]
        snapshot = self._config_snapshot()
        for key, value in snapshot.items():
            rendered = value if isinstance(value, str) else json.dumps(
                value, ensure_ascii=False, default=str
            )
            lines.append(f"{key} = {self._redact_text(rendered)}")
        lines.extend([
            "",
            f"[{t('settings.export_section_nas_stats')}]",
        ])
        nas_keys = [
            key for key in snapshot
            if key.startswith(("server_lock_", "remote_", "journal_", "repository_", "retention_days_"))
        ]
        for key in nas_keys:
            lines.append(f"{key} = {snapshot[key]}")
        if not nas_keys:
            lines.append(t("settings.export_no_nas_stats"))

        log_path = paths.log_file()
        lines.extend(["", f"[{t('settings.export_section_log', count=_BUNDLE_LOG_LINES)}]"])
        try:
            log_lines = log_path.read_text(encoding="utf-8", errors="replace").splitlines()
        except OSError:
            log_lines = []
        if not log_lines:
            lines.append(t("settings.export_no_log"))
        else:
            # Redact the whole tail at once: a private-key block spans lines and
            # per-line redaction would lose that state.
            tail = self._redact_text("\n".join(log_lines[-_BUNDLE_LOG_LINES:]))
            lines.extend(tail.splitlines())
        return "\n".join(lines) + "\n"

    def _export_diagnostics(self) -> None:
        """Let the user pick a .txt/.zip path and write a redacted bundle there."""
        path, _selected_filter = QFileDialog.getSaveFileName(
            self,
            t("settings.export_dialog_title"),
            "",
            t("settings.export_filter"),
        )
        if not path:
            return
        target = Path(path)
        if not target.suffix:
            target = target.with_suffix(".txt")
        try:
            bundle = self._diagnostic_bundle_text()
            if target.suffix.casefold() == ".zip":
                with zipfile.ZipFile(target, "w", zipfile.ZIP_DEFLATED) as archive:
                    archive.writestr(_BUNDLE_INNER_NAME, bundle)
            else:
                target.write_text(bundle, encoding="utf-8")
        except (OSError, ValueError, zipfile.BadZipFile) as exc:
            QMessageBox.warning(
                self, t("settings.export_failed_title"),
                t("settings.export_failed_body", detail=str(exc)),
            )
            return
        QMessageBox.information(
            self, t("settings.export_done_title"),
            t("settings.export_done_body", path=str(target)),
        )

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
            self.metrics_refresh_spin, self.full_pull_spin,
        ):
            spin.valueChanged.connect(self._mark_dirty)
        for checkbox in (
            self.delete_enabled, self.notify_sync_checkbox, self.animate_sync_checkbox,
            self.multiplexing_checkbox, self.schedule_enabled,
        ):
            checkbox.toggled.connect(self._mark_dirty)
        self.language_combo.currentIndexChanged.connect(self._mark_dirty)
        self.theme_combo.currentIndexChanged.connect(self._mark_dirty)
        self.tray_click_combo.currentIndexChanged.connect(self._mark_dirty)
        self.compression_combo.currentIndexChanged.connect(self._mark_dirty)
        self.sync_mode_combo.currentIndexChanged.connect(self._mark_dirty)
        self.conflict_policy_combo.currentIndexChanged.connect(self._mark_dirty)
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
        self.cfg.set("full_pull_interval_seconds", self.full_pull_spin.value(), persist=False)

    def _apply_metrics_refresh_interval(self) -> None:
        self.cfg.set("metrics_refresh_seconds", self.metrics_refresh_spin.value(), persist=False)

    def _apply_nas_friendly_profile(self) -> None:
        """One click for a quiet NAS without giving up event-driven sync.

        The change feed still wakes the pull immediately when the NAS revision
        changes; these values only reduce idle checks and expensive full scans.
        """
        self.poll_spin.setValue(300)
        self.metrics_refresh_spin.setValue(60)
        self.full_pull_spin.setValue(21600)
        index = self.compression_combo.findData("auto")
        if index >= 0:
            self.compression_combo.setCurrentIndex(index)
        self._mark_dirty()
        self.save_feedback.setText(t("settings.nas_friendly_applied"))

    def _add_exclusion_profile(self) -> None:
        patterns = self.exclusion_profile_combo.currentData()
        if not isinstance(patterns, list) or not patterns:
            return
        existing = {self.exclude_list.item(i).text() for i in range(self.exclude_list.count())}
        added = 0
        for pattern in patterns:
            if pattern in existing:
                continue
            self.exclude_list.addItem(pattern)
            existing.add(pattern)
            added += 1
        if added:
            self._mark_dirty()
            self.save_feedback.setText(t("settings.profile_added", count=added))
        self.exclude_list.setCurrentRow(self.exclude_list.count() - 1)

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

    def _refresh_selective_label(self) -> None:
        if not self._selective_excludes:
            self.selective_label.setText(t("settings.selective_none"))
        else:
            self.selective_label.setText(
                t("settings.selective_count", count=len(self._selective_excludes))
            )

    def _choose_selective_folders(self) -> None:
        root = self.cfg.local_root()
        if not root:
            QMessageBox.information(
                self,
                t("settings.selective_needs_folder_title"),
                t("settings.selective_needs_folder_body"),
            )
            return
        dialog = SelectiveSyncDialog(self, root=root, excluded=self._selective_excludes)
        if dialog.exec():
            result = dialog.excluded_paths()
            if result is not None:
                self._selective_excludes = result
                self._refresh_selective_label()
                self._mark_dirty()

    def _schedule_summary(self, window: dict) -> str:
        days = sorted(day for day in (window.get("days") or []) if 0 <= day <= 6)
        day_names = ", ".join(t(WEEKDAY_KEYS[day]) for day in days)
        return f"{day_names}  {window.get('start')}-{window.get('end')}"

    def _refresh_schedule_list(self) -> None:
        self.schedule_list.clear()
        for window in self._schedule_windows:
            item = QListWidgetItem(self._schedule_summary(window))
            item.setData(Qt.ItemDataRole.UserRole, window)
            self.schedule_list.addItem(item)

    def _add_schedule_window(self) -> None:
        dialog = ScheduleWindowDialog(self)
        if dialog.exec():
            window = dialog.window()
            if window:
                self._schedule_windows.append(window)
                self._refresh_schedule_list()
                self._mark_dirty()

    def _edit_schedule_window(self) -> None:
        row = self.schedule_list.currentRow()
        if row < 0 or row >= len(self._schedule_windows):
            return
        dialog = ScheduleWindowDialog(self, self._schedule_windows[row])
        if dialog.exec():
            window = dialog.window()
            if window:
                self._schedule_windows[row] = window
                self._refresh_schedule_list()
                self._mark_dirty()

    def _remove_schedule_window(self) -> None:
        row = self.schedule_list.currentRow()
        if row < 0 or row >= len(self._schedule_windows):
            return
        self._schedule_windows.pop(row)
        self._refresh_schedule_list()
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
        self.cfg.set("sync_mode", self.sync_mode_combo.currentData(), persist=False)
        self.cfg.set("delete_enabled", self.delete_enabled.isChecked(), persist=False)
        self.cfg.set("max_delete_files", self.max_delete_spin.value(), persist=False)
        self.cfg.set(
            "conflict_policy",
            self.conflict_policy_combo.currentData() or "newest",
            persist=False,
        )
        self._apply_bandwidth()
        self._apply_poll_interval()
        self._apply_metrics_refresh_interval()
        self.cfg.set("notify_sync_completion", self.notify_sync_checkbox.isChecked(), persist=False)
        self.cfg.set("animate_sync_icon", self.animate_sync_checkbox.isChecked(), persist=False)
        self.cfg.set("ssh_multiplexing", self.multiplexing_checkbox.isChecked(), persist=False)
        self.cfg.set("compression_mode", self.compression_combo.currentData() or "auto", persist=False)
        self.cfg.set("tray_single_click", self.tray_click_combo.currentData() or "menu", persist=False)
        code = self.language_combo.currentData()
        if code:
            self.cfg.set("language", code, persist=False)
        theme = self.theme_combo.currentData() or "light"
        self.cfg.set("theme", theme, persist=False)
        patterns = [self.exclude_list.item(i).text() for i in range(self.exclude_list.count())]
        self.cfg.set("exclude_patterns", patterns, persist=False)
        self.cfg.set("pause_schedule_enabled", self.schedule_enabled.isChecked(), persist=False)
        self.cfg.set("pause_schedule", normalize_schedule(self._schedule_windows), persist=False)
        self.cfg.set("selective_sync_excludes", self._selective_excludes, persist=False)
        self.cfg.save()
        apply_style(QApplication.instance(), theme)
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
        version = str(values.get("VERSION", "") or "")
        # Already fetched above: keep it so the Settings tab (and the export)
        # can show the effective server version without another SSH call.
        self.cfg.set("server_version", version, persist=False)
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
        # Detection just refreshed the cfg keys we display; no new SSH call.
        self._refresh_nas_facts()

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
