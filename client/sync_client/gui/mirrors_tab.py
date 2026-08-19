"""'Cartelle esterne' tab: manage the list of external folders that NASBox
mirrors one-way into itself, so they end up synced to the NAS and to the other
PCs too. This tab only edits the config and pokes MirrorManager -- the actual
watching/copying happens on MirrorManager's threads (mirrors.py)."""
from __future__ import annotations

import os
import time

from PyQt6.QtCore import Qt
from PyQt6.QtWidgets import (
    QFileDialog, QHBoxLayout, QHeaderView, QInputDialog, QLabel, QMessageBox,
    QPushButton, QTableWidget, QTableWidgetItem, QVBoxLayout, QWidget,
)

from ..config import Config
from ..mirrors import MirrorManager, validate_mirror
from ..i18n import t


class MirrorsTab(QWidget):
    COL_ENABLED = 0
    COL_SOURCE = 1
    COL_DEST = 2
    COL_LAST_SYNC = 3
    COL_STATUS = 4

    def __init__(self, cfg: Config, manager: MirrorManager, parent=None) -> None:
        super().__init__(parent)
        self.cfg = cfg
        self.manager = manager
        self._row_of: dict[str, int] = {}
        self._status_of: dict[str, dict] = {}

        root = QVBoxLayout(self)
        root.setContentsMargins(0, 0, 0, 0)

        note = QLabel(t("mirrors.note"))
        note.setWordWrap(True)
        root.addWidget(note)

        self.table = QTableWidget(0, 5)
        self.table.setHorizontalHeaderLabels([
            t("mirrors.col_enabled"),
            t("mirrors.col_source"),
            t("mirrors.col_dest"),
            t("mirrors.col_last_sync"),
            t("mirrors.col_status"),
        ])
        header = self.table.horizontalHeader()
        header.setSectionResizeMode(self.COL_SOURCE, QHeaderView.ResizeMode.Stretch)
        header.setSectionResizeMode(self.COL_DEST, QHeaderView.ResizeMode.Stretch)
        self.table.setSelectionBehavior(QTableWidget.SelectionBehavior.SelectRows)
        self.table.setSelectionMode(QTableWidget.SelectionMode.SingleSelection)
        self.table.verticalHeader().setVisible(False)
        self.table.setColumnWidth(self.COL_ENABLED, 70)
        self.table.setColumnWidth(self.COL_LAST_SYNC, 150)
        self.table.setColumnWidth(self.COL_STATUS, 180)
        self.table.itemChanged.connect(self._on_item_changed)
        root.addWidget(self.table, 1)

        actions = QHBoxLayout()
        add_btn = QPushButton(t("mirrors.add_btn"))
        add_btn.setToolTip(t("mirrors.add_tooltip"))
        add_btn.clicked.connect(self._add_mirror)
        actions.addWidget(add_btn)
        remove_btn = QPushButton(t("mirrors.remove_btn"))
        remove_btn.setToolTip(t("mirrors.remove_tooltip"))
        remove_btn.clicked.connect(self._remove_mirror)
        actions.addWidget(remove_btn)
        sync_btn = QPushButton(t("mirrors.sync_now_btn"))
        sync_btn.setToolTip(t("mirrors.sync_now_tooltip"))
        sync_btn.clicked.connect(self._sync_now)
        actions.addWidget(sync_btn)
        actions.addStretch(1)
        root.addLayout(actions)

        self._reload_rows()

    # --- row bookkeeping ---

    def _reload_rows(self) -> None:
        self.table.blockSignals(True)
        self.table.setRowCount(0)
        self._row_of.clear()
        for entry in self.cfg.mirrors():
            source = (entry.get("source") or "").strip()
            if not source:
                continue
            self._append_row(entry)
            if source in self._status_of:
                self._apply_status(self._status_of[source])
        self.table.blockSignals(False)

    def _append_row(self, entry: dict) -> None:
        source = entry.get("source", "")
        row = self.table.rowCount()
        self.table.insertRow(row)
        self._row_of[source] = row

        enabled_item = QTableWidgetItem()
        enabled_item.setFlags(Qt.ItemFlag.ItemIsEnabled | Qt.ItemFlag.ItemIsUserCheckable | Qt.ItemFlag.ItemIsSelectable)
        enabled_item.setCheckState(
            Qt.CheckState.Checked if entry.get("enabled", True) else Qt.CheckState.Unchecked
        )
        enabled_item.setToolTip(t("mirrors.col_enabled_tooltip"))
        self.table.setItem(row, self.COL_ENABLED, enabled_item)

        self.table.setItem(row, self.COL_SOURCE, QTableWidgetItem(source))
        dest_item = QTableWidgetItem(f"NASBox/{entry.get('dest', '')}")
        dest_item.setToolTip(t("mirrors.dest_tooltip"))
        self.table.setItem(row, self.COL_DEST, dest_item)
        last_sync = entry.get("last_sync")
        self.table.setItem(
            row, self.COL_LAST_SYNC,
            QTableWidgetItem(self._last_sync_text(last_sync)),
        )
        enabled = bool(entry.get("enabled", True))
        status_item = QTableWidgetItem(
            t("mirrors.state_disabled") if not enabled else t("mirrors.state_idle")
        )
        status_item.setToolTip("")
        self.table.setItem(row, self.COL_STATUS, status_item)

    def _source_at_row(self, row: int) -> str | None:
        for source, index in self._row_of.items():
            if index == row:
                return source
        return None

    # --- GUI actions ---

    def _add_mirror(self) -> None:
        if not self.cfg.is_configured():
            QMessageBox.information(self, t("mirrors.error_title"), t("mirrors.no_nasbox_body"))
            return
        source = QFileDialog.getExistingDirectory(
            self, t("mirrors.choose_source"), os.path.expanduser("~"),
        )
        if not source:
            return
        default_dest = os.path.basename(os.path.normpath(source)).strip() or "copia"
        dest, ok = QInputDialog.getText(
            self, t("mirrors.add_title"),
            t("mirrors.dest_prompt"), text=default_dest,
        )
        if not ok:
            return

        ok, error = validate_mirror(self.cfg, source, dest)
        if not ok:
            QMessageBox.warning(self, t("mirrors.error_title"), error)
            return

        mirrors = self.cfg.mirrors()
        mirrors.append({
            "source": source,
            "dest": dest.strip("/"),
            "enabled": True,
            "last_sync": None,
            "error": "",
        })
        self.cfg.set_mirrors(mirrors)
        self._append_row(mirrors[-1])
        self.manager.refresh()

    def _remove_mirror(self) -> None:
        row = self.table.currentRow()
        source = self._source_at_row(row)
        if source is None:
            return
        if QMessageBox.question(
            self, t("mirrors.remove_confirm_title"),
            t("mirrors.remove_confirm_body", source=source),
        ) != QMessageBox.StandardButton.Yes:
            return
        mirrors = [m for m in self.cfg.mirrors() if m.get("source") != source]
        self.cfg.set_mirrors(mirrors)
        self._status_of.pop(source, None)
        self._reload_rows()
        self.manager.refresh()

    def _sync_now(self) -> None:
        row = self.table.currentRow()
        source = self._source_at_row(row)
        if source is None:
            return
        self.manager.sync_now(source)

    def _on_item_changed(self, item: QTableWidgetItem) -> None:
        if item.column() != self.COL_ENABLED:
            return
        source = self._source_at_row(item.row())
        if source is None:
            return
        enabled = item.checkState() == Qt.CheckState.Checked
        self.cfg.update_mirror(source, enabled=enabled)
        if not enabled:
            status = self._status_of.get(source)
            if status and status["state"] not in ("disabled", "error"):
                self._apply_status(dict(status, state="disabled", message=t("mirrors.state_disabled")))
        self.manager.refresh()

    # --- live status from MirrorManager ---

    def on_status_changed(self, status: dict) -> None:
        source = status.get("source")
        if not source:
            return
        self._status_of[source] = dict(status)
        self._apply_status(status)

    def _apply_status(self, status: dict) -> None:
        source = status.get("source", "")
        self._status_of[source] = dict(status)
        row = self._row_of.get(source)
        if row is None:
            return
        state = status.get("state")
        message = status.get("message") or ""
        error = status.get("error") or ""
        if state == "error":
            text = error or t("mirrors.state_error")
        elif state == "syncing":
            text = t("mirrors.state_syncing")
        elif state == "disabled":
            text = t("mirrors.state_disabled")
        elif state == "unconfigured":
            text = t("mirrors.state_unconfigured")
        else:
            text = t("mirrors.state_idle")
        status_item = self.table.item(row, self.COL_STATUS)
        if status_item is not None:
            status_item.setText(text)
            status_item.setToolTip(message or error or "")
        last_sync = status.get("last_sync")
        last_item = self.table.item(row, self.COL_LAST_SYNC)
        if last_item is not None:
            last_item.setText(self._last_sync_text(last_sync))

    # --- helpers ---

    @staticmethod
    def _last_sync_text(last_sync) -> str:
        if not last_sync:
            return t("mirrors.last_sync_never")
        return time.strftime("%Y-%m-%d %H:%M:%S", time.localtime(float(last_sync)))
