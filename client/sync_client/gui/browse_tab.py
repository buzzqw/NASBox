"""Remote NAS browser ("Sfoglia NAS" tab) -- a lightweight WinSCP-style view of
the NASBox tree as it actually is on the NAS right now, independent of what
this particular client has synced locally. Deliberately narrow in scope:

- Browsing and downloading are read-only against the live NAS filesystem.
- Rename and delete never touch a file directly -- both go through the NAS
  script's own browse-delete/browse-rename commands (see rsync_ops.py and
  server/sync-daemon-server.sh), which move things into the same
  .sync-trash/retention the sync engine itself uses. Nothing here is a raw
  unlink; anything removed from this tab is recoverable from the History tab
  for the same number of days as an ordinary sync-driven deletion.
- No upload and no arbitrary file creation from here -- that's what the
  regular sync engine is for. This tab manages what's already on the NAS.
"""
from __future__ import annotations

import os
from pathlib import Path

from PyQt6.QtCore import Qt
from PyQt6.QtWidgets import (
    QAbstractItemView, QFileDialog, QHBoxLayout, QHeaderView, QInputDialog, QLabel,
    QLineEdit, QMenu, QMessageBox, QPushButton, QStyle, QTreeWidget, QTreeWidgetItem,
    QVBoxLayout, QWidget,
)

from .. import rsync_ops
from ..config import Config
from ..engine import SyncEngine
from ..i18n import t
from ..logger import EventLogger
from ..sync_state import SyncStateStore
from .async_utils import run_in_background
from .format_utils import human_size

ENTRY_ROLE = 100  # Qt.ItemDataRole.UserRole -- holds a BrowseEntry on each row


class BrowseTab(QWidget):
    def __init__(
        self, cfg: Config, engine: SyncEngine, logger: EventLogger, sync_state: SyncStateStore, parent=None,
    ) -> None:
        super().__init__(parent)
        self.cfg = cfg
        self.engine = engine
        self.logger = logger
        self.sync_state = sync_state
        self._path = ""  # relative to the NAS repository root; "" = root
        self._entries: list[rsync_ops.BrowseEntry] = []
        self._busy = False

        root = QVBoxLayout(self)

        note = QLabel(t("browse.note"))
        note.setWordWrap(True)
        root.addWidget(note)

        path_row = QHBoxLayout()
        self.up_btn = QPushButton(t("browse.up_btn"))
        self.up_btn.clicked.connect(self._go_up)
        path_row.addWidget(self.up_btn)
        self.breadcrumb_layout = QHBoxLayout()
        self.breadcrumb_layout.setContentsMargins(0, 0, 0, 0)
        path_row.addLayout(self.breadcrumb_layout, 1)
        refresh_btn = QPushButton(t("browse.refresh_btn"))
        refresh_btn.clicked.connect(self.refresh)
        path_row.addWidget(refresh_btn)
        root.addLayout(path_row)

        search_row = QHBoxLayout()
        search_row.addWidget(QLabel(t("browse.search_label")))
        self.search_edit = QLineEdit()
        self.search_edit.setPlaceholderText(t("browse.search_placeholder"))
        self.search_edit.setClearButtonEnabled(True)
        self.search_edit.textChanged.connect(self._populate)
        search_row.addWidget(self.search_edit, 1)
        root.addLayout(search_row)

        cols = [t("browse.col_name"), t("browse.col_size"), t("browse.col_modified")]
        self.tree = QTreeWidget()
        self.tree.setColumnCount(len(cols))
        self.tree.setHeaderLabels(cols)
        self.tree.header().setSectionResizeMode(0, QHeaderView.ResizeMode.Stretch)
        self.tree.setSelectionMode(QAbstractItemView.SelectionMode.ExtendedSelection)
        self.tree.itemDoubleClicked.connect(lambda item, _col: self._open_item(item))
        self.tree.setContextMenuPolicy(Qt.ContextMenuPolicy.CustomContextMenu)
        self.tree.customContextMenuRequested.connect(self._show_context_menu)
        root.addWidget(self.tree, 1)

        buttons_row = QHBoxLayout()
        self.download_btn = QPushButton(t("browse.download_btn"))
        self.download_btn.setToolTip(t("browse.download_tooltip"))
        self.download_btn.clicked.connect(self._download_selected)
        buttons_row.addWidget(self.download_btn)
        self.rename_btn = QPushButton(t("browse.rename_btn"))
        self.rename_btn.clicked.connect(self._rename_selected)
        buttons_row.addWidget(self.rename_btn)
        self.delete_btn = QPushButton(t("browse.delete_btn"))
        self.delete_btn.setToolTip(t("browse.delete_tooltip"))
        self.delete_btn.clicked.connect(self._delete_selected)
        buttons_row.addWidget(self.delete_btn)
        buttons_row.addStretch(1)
        self.status_label = QLabel()
        buttons_row.addWidget(self.status_label)
        root.addLayout(buttons_row)

        self._shown = False

    def showEvent(self, event) -> None:
        super().showEvent(event)
        if not self._shown:
            self._shown = True
            self.refresh()

    # --- navigation ---

    def _go_up(self) -> None:
        if not self._path:
            return
        self._navigate(os.path.dirname(self._path))

    def _navigate(self, path: str) -> None:
        if self._busy or path == self._path:
            return
        self._path = path
        self.tree.clearSelection()
        self.refresh()

    def _update_breadcrumbs(self) -> None:
        while self.breadcrumb_layout.count():
            item = self.breadcrumb_layout.takeAt(0)
            if item.widget() is not None:
                item.widget().deleteLater()
        parts = [part for part in self._path.split("/") if part]
        targets = [""] + ["/".join(parts[:index]) for index in range(1, len(parts) + 1)]
        labels = ["/"] + parts
        self._breadcrumb_buttons = []
        for label, target in zip(labels, targets):
            button = QPushButton(label)
            button.setFlat(True)
            button.setEnabled(not self._busy and target != self._path)
            button.clicked.connect(lambda _checked=False, value=target: self._navigate(value))
            self.breadcrumb_layout.addWidget(button)
            self._breadcrumb_buttons.append(button)
            if target != targets[-1]:
                self.breadcrumb_layout.addWidget(QLabel("/"))
        self.breadcrumb_layout.addStretch(1)

    def _open_item(self, item: QTreeWidgetItem) -> None:
        entry: rsync_ops.BrowseEntry = item.data(0, ENTRY_ROLE)
        if entry.kind == "DIR":
            self._navigate(f"{self._path}/{entry.name}" if self._path else entry.name)
        elif entry.kind == "FILE":
            self._download_one(entry)

    def refresh(self) -> None:
        if self._busy:
            return
        conn = self.engine.connection
        self._update_breadcrumbs()
        self.up_btn.setEnabled(bool(self._path))
        if conn is None:
            self.status_label.setText(t("browse.nas_unreachable"))
            self.tree.clear()
            return
        self._set_busy(t("browse.loading"))
        path = self._path
        run_in_background(
            self, "_list_call",
            lambda: rsync_ops.browse_list(self.cfg, conn, path),
            lambda result, exc: self._on_list_done(path, result, exc),
        )

    def _on_list_done(self, requested_path: str, entries, exc: Exception | None) -> None:
        self._set_busy(None)
        if requested_path != self._path:
            return  # user navigated elsewhere while this was in flight
        if exc is not None or entries is None:
            self.status_label.setText(t("browse.list_failed"))
            return
        self._entries = entries
        self.status_label.setText(t("browse.entry_count", count=len(entries)))
        self._populate()

    def _populate(self) -> None:
        selected_names = {
            entry.name for entry in self._selected_entries() if entry is not None
        }
        self.tree.clear()
        style = self.style()
        dir_icon = style.standardIcon(QStyle.StandardPixmap.SP_DirIcon)
        file_icon = style.standardIcon(QStyle.StandardPixmap.SP_FileIcon)
        needle = self.search_edit.text().strip().lower()
        entries = [entry for entry in self._entries if not needle or needle in entry.name.lower()]
        for entry in sorted(entries, key=lambda e: (e.kind != "DIR", e.name.lower())):
            size_text = "" if entry.kind == "DIR" else human_size(entry.size)
            item = QTreeWidgetItem([entry.name, size_text, self._format_mtime(entry.mtime)])
            item.setIcon(0, dir_icon if entry.kind == "DIR" else file_icon)
            item.setData(0, ENTRY_ROLE, entry)
            self.tree.addTopLevelItem(item)
            item.setSelected(entry.name in selected_names)
        if needle:
            self.status_label.setText(t("browse.filtered_count", shown=len(entries), total=len(self._entries)))
        else:
            self.status_label.setText(t("browse.entry_count", count=len(self._entries)))

    @staticmethod
    def _format_mtime(epoch_seconds: int) -> str:
        if not epoch_seconds:
            return ""
        from datetime import datetime
        try:
            return datetime.fromtimestamp(epoch_seconds).strftime("%Y-%m-%d %H:%M")
        except (OSError, OverflowError, ValueError):
            return ""

    def _set_busy(self, message: str | None) -> None:
        self._busy = message is not None
        for widget in (self.up_btn, self.download_btn, self.rename_btn, self.delete_btn, self.tree):
            widget.setEnabled(not self._busy)
        if message is not None:
            self.status_label.setText(message)
        self._update_breadcrumbs()

    def _selected_entries(self) -> list[rsync_ops.BrowseEntry]:
        return [item.data(0, ENTRY_ROLE) for item in self.tree.selectedItems()]

    def _show_context_menu(self, position) -> None:
        item = self.tree.itemAt(position)
        if item is not None and not item.isSelected():
            self.tree.clearSelection()
            item.setSelected(True)
        entries = self._selected_entries()
        if not entries:
            return
        menu = QMenu(self)
        download = menu.addAction(t("browse.download_btn"))
        download.setEnabled(any(entry.kind == "FILE" for entry in entries))
        download.triggered.connect(self._download_selected)
        rename = menu.addAction(t("browse.rename_btn"))
        rename.setEnabled(len(entries) == 1)
        rename.triggered.connect(self._rename_selected)
        delete = menu.addAction(t("browse.delete_btn"))
        delete.triggered.connect(self._delete_selected)
        menu.exec(self.tree.viewport().mapToGlobal(position))

    # --- download (read-only, no restrictions) ---

    def _download_selected(self) -> None:
        entries = [e for e in self._selected_entries() if e.kind == "FILE"]
        if not entries:
            QMessageBox.information(self, t("browse.download_btn"), t("browse.download_pick_file"))
            return
        if len(entries) == 1:
            self._download_one(entries[0])
            return
        destination_dir = QFileDialog.getExistingDirectory(self, t("browse.download_btn"))
        if not destination_dir:
            return
        conn = self.engine.connection
        if conn is None:
            QMessageBox.warning(self, t("browse.nas_unreachable_title"), t("browse.nas_unreachable"))
            return

        def download_all():
            outcomes = []
            for entry in entries:
                remote_path = f"{self._path}/{entry.name}" if self._path else entry.name
                dest = Path(destination_dir, entry.name)
                outcomes.append((entry, *rsync_ops.browse_download(self.cfg, conn, remote_path, dest)))
            return outcomes

        self._set_busy(t("browse.downloading"))
        run_in_background(self, "_download_many_call", download_all, self._on_download_many_done)

    def _on_download_many_done(self, result, exc: Exception | None) -> None:
        self._set_busy(None)
        if exc is not None:
            QMessageBox.warning(self, t("browse.download_failed_title"), str(exc))
            return
        failed = [entry.name for entry, ok, _detail in result if not ok]
        if failed:
            QMessageBox.warning(self, t("browse.download_failed_title"), t("browse.download_partial_body", names=", ".join(failed)))
        else:
            QMessageBox.information(self, t("browse.download_done_title"), t("browse.download_done_body", count=len(result)))

    def _download_one(self, entry: rsync_ops.BrowseEntry) -> None:
        conn = self.engine.connection
        if conn is None:
            QMessageBox.warning(self, t("browse.nas_unreachable_title"), t("browse.nas_unreachable"))
            return
        destination, _filter = QFileDialog.getSaveFileName(self, t("browse.download_btn"), entry.name)
        if not destination:
            return
        remote_path = f"{self._path}/{entry.name}" if self._path else entry.name
        self._set_busy(t("browse.downloading"))
        run_in_background(
            self, "_download_call",
            lambda: rsync_ops.browse_download(self.cfg, conn, remote_path, Path(destination)),
            lambda result, exc: self._on_download_done(destination, result, exc),
        )

    def _on_download_done(self, destination: str, result, exc: Exception | None) -> None:
        self._set_busy(None)
        if exc is not None:
            QMessageBox.warning(self, t("browse.download_failed_title"), str(exc))
            return
        ok, detail = result
        if ok:
            self.logger.log("BROWSE_DOWNLOAD", destination)
            QMessageBox.information(self, t("browse.download_done_title"), t("browse.download_one_done_body", path=destination))
        else:
            QMessageBox.warning(self, t("browse.download_failed_title"), detail)

    # --- rename: goes straight to the NAS, no local trash involved ---

    def _rename_selected(self) -> None:
        entries = self._selected_entries()
        if len(entries) != 1:
            QMessageBox.information(self, t("browse.rename_btn"), t("browse.rename_pick_one"))
            return
        entry = entries[0]
        conn = self.engine.connection
        if conn is None:
            QMessageBox.warning(self, t("browse.nas_unreachable_title"), t("browse.nas_unreachable"))
            return
        new_name, accepted = QInputDialog.getText(self, t("browse.rename_btn"), t("browse.rename_prompt", name=entry.name), text=entry.name)
        new_name = new_name.strip()
        if not accepted or not new_name or new_name == entry.name:
            return
        if "/" in new_name:
            QMessageBox.warning(self, t("browse.rename_btn"), t("browse.rename_invalid_name"))
            return
        src = f"{self._path}/{entry.name}" if self._path else entry.name
        dst = f"{self._path}/{new_name}" if self._path else new_name
        device_id = self.sync_state.device_id()
        self._set_busy(t("browse.renaming"))
        run_in_background(
            self, "_rename_call",
            lambda: rsync_ops.browse_rename(self.cfg, conn, src, dst, device_id),
            lambda result, exc: self._on_rename_done(entry.name, new_name, result, exc),
        )

    def _on_rename_done(self, old_name: str, new_name: str, result, exc: Exception | None) -> None:
        self._set_busy(None)
        if exc is not None:
            QMessageBox.warning(self, t("browse.rename_failed_title"), str(exc))
            return
        ok, detail = result
        if ok:
            self.logger.log("BROWSE_RENAME", f"{self._path}/{old_name}" if self._path else old_name, detail=new_name)
            self.refresh()
        else:
            QMessageBox.warning(self, t("browse.rename_failed_title"), detail)

    # --- delete: always via the NAS trash, never a raw unlink ---

    def _delete_selected(self) -> None:
        entries = self._selected_entries()
        if not entries:
            return
        conn = self.engine.connection
        if conn is None:
            QMessageBox.warning(self, t("browse.nas_unreachable_title"), t("browse.nas_unreachable"))
            return
        retention = self.cfg.get("retention_days_remote")
        retention_text = _retention_description(retention)
        names = ", ".join(e.name for e in entries)
        if QMessageBox.question(
            self, t("browse.confirm_delete_title"),
            t("browse.confirm_delete_body", names=names, retention=retention_text),
        ) != QMessageBox.StandardButton.Yes:
            return
        device_id = self.sync_state.device_id()

        def delete_all():
            outcomes = []
            for entry in entries:
                remote_path = f"{self._path}/{entry.name}" if self._path else entry.name
                outcomes.append((entry, *rsync_ops.browse_delete(self.cfg, conn, remote_path, device_id)))
            return outcomes

        self._set_busy(t("browse.deleting"))
        run_in_background(self, "_delete_call", delete_all, self._on_delete_done)

    def _on_delete_done(self, result, exc: Exception | None) -> None:
        self._set_busy(None)
        if exc is not None:
            QMessageBox.warning(self, t("browse.delete_failed_title"), str(exc))
            return
        for entry, ok, _detail in result:
            if ok:
                remote_path = f"{self._path}/{entry.name}" if self._path else entry.name
                self.logger.log("BROWSE_DELETE", remote_path)
        failed = [entry.name for entry, ok, _detail in result if not ok]
        self.refresh()
        if failed:
            QMessageBox.warning(self, t("browse.delete_failed_title"), t("browse.delete_partial_body", names=", ".join(failed)))


def _retention_description(value) -> str:
    if str(value) == "0":
        return t("browse.retention_never")
    return t("browse.retention_days", days=30 if value is None or value == "" else value)
