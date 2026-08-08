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
    QMessageBox, QPushButton, QStyle, QTreeWidget, QTreeWidgetItem, QVBoxLayout, QWidget,
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
        self.path_label = QLabel()
        self.path_label.setObjectName("browsePath")
        path_row.addWidget(self.path_label, 1)
        refresh_btn = QPushButton(t("browse.refresh_btn"))
        refresh_btn.clicked.connect(self.refresh)
        path_row.addWidget(refresh_btn)
        root.addLayout(path_row)

        cols = [t("browse.col_name"), t("browse.col_size"), t("browse.col_modified")]
        self.tree = QTreeWidget()
        self.tree.setColumnCount(len(cols))
        self.tree.setHeaderLabels(cols)
        self.tree.header().setSectionResizeMode(0, QHeaderView.ResizeMode.Stretch)
        self.tree.setSelectionMode(QAbstractItemView.SelectionMode.ExtendedSelection)
        self.tree.itemDoubleClicked.connect(lambda item, _col: self._open_item(item))
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
        self._path = os.path.dirname(self._path)
        self.refresh()

    def _open_item(self, item: QTreeWidgetItem) -> None:
        entry: rsync_ops.BrowseEntry = item.data(0, ENTRY_ROLE)
        if entry.kind == "DIR":
            self._path = f"{self._path}/{entry.name}" if self._path else entry.name
            self.refresh()
        elif entry.kind == "FILE":
            self._download_one(entry)

    def refresh(self) -> None:
        if self._busy:
            return
        conn = self.engine.connection
        self.path_label.setText(t("browse.path_label", path=self._path or "/"))
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
        self.tree.clear()
        style = self.style()
        dir_icon = style.standardIcon(QStyle.StandardPixmap.SP_DirIcon)
        file_icon = style.standardIcon(QStyle.StandardPixmap.SP_FileIcon)
        for entry in sorted(self._entries, key=lambda e: (e.kind != "DIR", e.name.lower())):
            size_text = "" if entry.kind == "DIR" else human_size(entry.size)
            item = QTreeWidgetItem([entry.name, size_text, self._format_mtime(entry.mtime)])
            item.setIcon(0, dir_icon if entry.kind == "DIR" else file_icon)
            item.setData(0, ENTRY_ROLE, entry)
            self.tree.addTopLevelItem(item)

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

    def _selected_entries(self) -> list[rsync_ops.BrowseEntry]:
        return [item.data(0, ENTRY_ROLE) for item in self.tree.selectedItems()]

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
        retention = self.cfg.get("retention_days_remote") or 30
        names = ", ".join(e.name for e in entries)
        if QMessageBox.question(
            self, t("browse.confirm_delete_title"),
            t("browse.confirm_delete_body", names=names, days=retention),
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
