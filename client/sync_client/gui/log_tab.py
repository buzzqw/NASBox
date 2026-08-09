from __future__ import annotations

import time

from PyQt6.QtCore import QTimer
from PyQt6.QtWidgets import (
    QComboBox, QHBoxLayout, QHeaderView, QLabel, QLineEdit, QPushButton,
    QTableWidget, QTableWidgetItem, QVBoxLayout, QWidget,
)

from ..i18n import t
from ..logger import Event, EventLogger

MAX_ROWS = 2000
FLUSH_INTERVAL_MS = 250  # batch fast-arriving events instead of one table insert + repaint each
SEARCH_DEBOUNCE_MS = 300  # reload() re-scans the whole events.jsonl -- don't do that on every keystroke

# Action codes are also written verbatim into events.jsonl and shown as-is in
# the Azione/Action column -- kept untranslated on purpose so a raw grep
# through the log file matches what's on screen, regardless of app language.
ACTION_FILTERS = ["UPLOAD", "DOWNLOAD", "DELETE_LOCAL", "DELETE_REMOTE",
                   "PRUNE_LOCAL_TRASH", "PRUNE_REMOTE_TRIGGER", "CANCELLED", "PULL_DEFERRED",
                   "SERVER_DOWN", "SERVER_RESTARTED", "SERVER_OUTDATED", "CONFLICT", "ERROR"]


class LogTab(QWidget):
    def __init__(self, logger: EventLogger, parent=None) -> None:
        super().__init__(parent)
        self.logger = logger
        self._all_filter_label = t("log.all_filter")

        root = QVBoxLayout(self)

        filter_row = QHBoxLayout()
        filter_row.addWidget(QLabel(t("log.filter_label")))
        self.filter_combo = QComboBox()
        self.filter_combo.addItems([self._all_filter_label] + ACTION_FILTERS)
        self.filter_combo.setToolTip(t("log.filter_tooltip"))
        self.filter_combo.currentTextChanged.connect(lambda _: self.reload())
        filter_row.addWidget(self.filter_combo)

        filter_row.addWidget(QLabel(t("log.search_label")))
        self.search_edit = QLineEdit()
        self.search_edit.setPlaceholderText(t("log.search_placeholder"))
        self.search_edit.setToolTip(t("log.search_tooltip"))
        self.search_edit.textChanged.connect(self._on_search_changed)
        filter_row.addWidget(self.search_edit)

        filter_row.addStretch(1)
        refresh_btn = QPushButton(t("log.refresh_btn"))
        refresh_btn.setToolTip(t("log.refresh_tooltip"))
        refresh_btn.clicked.connect(self.reload)
        filter_row.addWidget(refresh_btn)
        root.addLayout(filter_row)

        cols = [t("log.col_time"), t("log.col_action"), t("log.col_path"), t("log.col_detail")]
        self.table = QTableWidget(0, len(cols))
        self.table.setHorizontalHeaderLabels(cols)
        self.table.setWordWrap(False)
        header = self.table.horizontalHeader()
        # All columns stay manually resizable. ResizeToContents, Stretch and
        # Fixed make the header ignore drag gestures, which is especially
        # frustrating for the two long-text columns.
        header.setStretchLastSection(False)
        header.setMinimumSectionSize(70)
        for section in range(len(cols)):
            header.setSectionResizeMode(section, QHeaderView.ResizeMode.Interactive)
        for section, width in enumerate((155, 105, 450, 300)):
            header.resizeSection(section, width)
        root.addWidget(self.table)

        # Incoming live events (potentially many per second during a big sync)
        # are just appended here -- cheap -- and applied to the table in one
        # batch every FLUSH_INTERVAL_MS, instead of an insertRow+repaint per event.
        self._pending: list[tuple[str, str, str, str]] = []
        self._flush_timer = QTimer(self)
        self._flush_timer.setInterval(FLUSH_INTERVAL_MS)
        self._flush_timer.timeout.connect(self._flush)
        self._flush_timer.start()

        # reload() re-reads and JSON-parses the whole events.jsonl file (up to
        # MAX_EVENTS lines) -- doing that on every keystroke while typing a search
        # term visibly lags the UI, so only fire it once typing pauses.
        self._search_timer = QTimer(self)
        self._search_timer.setSingleShot(True)
        self._search_timer.setInterval(SEARCH_DEBOUNCE_MS)
        self._search_timer.timeout.connect(self.reload)

        self.reload()

    def _on_search_changed(self, _text: str) -> None:
        self._search_timer.start()

    def _matches_search(self, path_: str, detail: str) -> bool:
        needle = self.search_edit.text().strip().lower()
        if not needle:
            return True
        return needle in path_.lower() or needle in detail.lower()

    def reload(self) -> None:
        action = self.filter_combo.currentText()
        action_filter = None if action == self._all_filter_label else action
        events = self.logger.tail(limit=MAX_ROWS, action_filter=action_filter)
        events = [ev for ev in events if self._matches_search(ev.path, ev.detail)]
        self._render(events)

    def _render(self, events: list[Event]) -> None:
        self.table.setRowCount(len(events))
        for row, ev in enumerate(reversed(events)):
            t_str = time.strftime("%Y-%m-%d %H:%M:%S", time.localtime(ev.ts))
            self.table.setItem(row, 0, QTableWidgetItem(t_str))
            self.table.setItem(row, 1, QTableWidgetItem(ev.action))
            self.table.setItem(row, 2, QTableWidgetItem(ev.path))
            self.table.setItem(row, 3, QTableWidgetItem(ev.detail))

    def on_log_event(self, action: str, path_: str, detail: str) -> None:
        current = self.filter_combo.currentText()
        if current != self._all_filter_label and current != action:
            return
        if not self._matches_search(path_, detail):
            return
        t_str = time.strftime("%Y-%m-%d %H:%M:%S")
        self._pending.append((t_str, action, path_, detail))

    def _flush(self) -> None:
        if not self._pending:
            return
        pending, self._pending = self._pending, []

        self.table.setUpdatesEnabled(False)
        try:
            for t_str, action, path_, detail in reversed(pending):
                self.table.insertRow(0)
                self.table.setItem(0, 0, QTableWidgetItem(t_str))
                self.table.setItem(0, 1, QTableWidgetItem(action))
                self.table.setItem(0, 2, QTableWidgetItem(path_))
                self.table.setItem(0, 3, QTableWidgetItem(detail))
            if self.table.rowCount() > MAX_ROWS:
                for row in range(self.table.rowCount() - 1, MAX_ROWS - 1, -1):
                    self.table.removeRow(row)
        finally:
            self.table.setUpdatesEnabled(True)
