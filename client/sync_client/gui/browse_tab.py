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
- Dropping files/folders from the desktop uploads them through the ordinary
  sync engine (local staging + pending queue): that keeps the shared NAS lease,
  the journal and conflict handling intact. This tab never writes a file to
  the NAS behind the engine's back. Dragging a row out offers its local path.
"""
from __future__ import annotations

import os
import shutil
from pathlib import Path

from PyQt6.QtCore import QMimeData, Qt, QUrl
from PyQt6.QtGui import QDesktopServices, QDrag
from PyQt6.QtWidgets import (
    QAbstractItemView, QApplication, QFileDialog, QHBoxLayout, QHeaderView,
    QInputDialog, QLabel, QLineEdit, QMenu, QMessageBox, QPushButton, QStyle,
    QTreeWidget, QTreeWidgetItem, QVBoxLayout, QWidget,
)

from .. import rsync_ops
from ..config import (
    SYNC_MODE_ARCHIVE, SYNC_MODE_BIDIRECTIONAL, SYNC_MODE_PULL_ONLY,
    SYNC_MODE_PUSH_ONLY, Config,
)
from ..engine import SyncEngine
from ..i18n import t
from ..logger import EventLogger
from ..sync_state import SyncStateStore
from .async_utils import run_in_background
from .format_utils import human_size

ENTRY_ROLE = 100  # Qt.ItemDataRole.UserRole -- holds a BrowseEntry on each row

# Menu labels are separate keys (rather than reusing the Settings tab's) so the
# context menu can read "Solo NAS" / "Solo locale" without the push-only suffix.
SYNC_MODE_MENU_KEYS = {
    SYNC_MODE_PULL_ONLY: "browse.menu_sync_nas_only",
    SYNC_MODE_PUSH_ONLY: "browse.menu_sync_local_only",
    SYNC_MODE_BIDIRECTIONAL: "browse.menu_sync_bidirectional",
    SYNC_MODE_ARCHIVE: "browse.menu_sync_archive",
}


class _BrowseTreeWidget(QTreeWidget):
    """The browse tree, with external drops and a local-path drag payload."""

    def __init__(self, tab: "BrowseTab") -> None:
        super().__init__(tab)
        self._tab = tab
        self.setDragEnabled(True)
        self.setAcceptDrops(True)
        self.setDragDropMode(QAbstractItemView.DragDropMode.DragDrop)
        self.setDefaultDropAction(Qt.DropAction.CopyAction)

    def startDrag(self, supportedActions) -> None:
        mime = self._tab._drag_mime_data()
        if mime is None:
            return
        drag = QDrag(self)
        drag.setMimeData(mime)
        drag.exec(supportedActions)

    def dragEnterEvent(self, event) -> None:
        if event.source() is not self and event.mimeData().hasUrls():
            event.acceptProposedAction()
        else:
            super().dragEnterEvent(event)

    def dragMoveEvent(self, event) -> None:
        if event.source() is not self and event.mimeData().hasUrls():
            event.acceptProposedAction()
        else:
            super().dragMoveEvent(event)

    def dropEvent(self, event) -> None:
        if event.source() is not self and event.mimeData().hasUrls():
            event.acceptProposedAction()
            self._tab._upload_dropped_urls([
                url.toLocalFile() for url in event.mimeData().urls()
            ])
        else:
            super().dropEvent(event)


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
        self.tree = _BrowseTreeWidget(self)
        self.tree.setColumnCount(len(cols))
        self.tree.setHeaderLabels(cols)
        self.tree.header().setSectionResizeMode(0, QHeaderView.ResizeMode.Stretch)
        self.tree.setSelectionMode(QAbstractItemView.SelectionMode.ExtendedSelection)
        self.tree.itemDoubleClicked.connect(lambda item, _col: self._open_item(item))
        self.tree.setContextMenuPolicy(Qt.ContextMenuPolicy.CustomContextMenu)
        self.tree.customContextMenuRequested.connect(self._show_context_menu)
        root.addWidget(self.tree, 1)

        # Empty state: an empty tree should suggest what to do instead of
        # looking like a broken view. Hidden whenever there is something to show.
        self.empty_label = QLabel(t("browse.empty_hint"))
        self.empty_label.setObjectName("browseEmptyHint")
        self.empty_label.setAlignment(Qt.AlignmentFlag.AlignCenter)
        self.empty_label.setWordWrap(True)
        root.addWidget(self.empty_label, 1)
        self.empty_label.hide()

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

    # --- path helpers (shared by the menu, the drag payload and uploads) ---

    def _relative_path_for_name(self, name: str) -> str:
        return f"{self._path}/{name}" if self._path else name

    def _entry_relative_path(self, entry: rsync_ops.BrowseEntry) -> str:
        return self._relative_path_for_name(entry.name)

    def _entry_local_path(self, entry: rsync_ops.BrowseEntry) -> Path:
        return Path(self.cfg.local_root(), self._entry_relative_path(entry))

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
            self._update_empty_state()
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
        self._update_empty_state()

    def _update_empty_state(self) -> None:
        """Swap the tree for a useful hint when there is nothing to display."""
        if self.tree.topLevelItemCount():
            self.tree.show()
            self.empty_label.hide()
            return
        filtered = bool(self.search_edit.text().strip())
        self.empty_label.setText(
            t("browse.empty_filtered_hint") if filtered else t("browse.empty_hint")
        )
        self.tree.hide()
        self.empty_label.show()

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
            self.empty_label.hide()  # "Caricamento…" is the status label's job
        else:
            self._update_empty_state()
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

        copy_remote = menu.addAction(t("browse.menu_copy_remote_path"))
        copy_remote.triggered.connect(self._copy_remote_paths)
        copy_local = menu.addAction(t("browse.menu_copy_local_path"))
        copy_local.triggered.connect(self._copy_local_paths)
        open_local = menu.addAction(t("browse.menu_open_local_folder"))
        open_local.setEnabled(len(entries) == 1)
        open_local.triggered.connect(self._open_local_folder)
        menu.addSeparator()

        has_dirs = any(entry.kind == "DIR" for entry in entries)
        exclude = menu.addAction(t("browse.menu_exclude_folder"))
        exclude.setEnabled(has_dirs)
        exclude.triggered.connect(lambda _checked=False: self._set_folder_excluded(True))
        include = menu.addAction(t("browse.menu_include_folder"))
        include.setEnabled(has_dirs)
        include.triggered.connect(lambda _checked=False: self._set_folder_excluded(False))
        menu.addSeparator()

        for mode, key in SYNC_MODE_MENU_KEYS.items():
            action = menu.addAction(t(key))
            action.triggered.connect(
                lambda _checked=False, value=mode: self._set_sync_mode(value)
            )
        menu.addSeparator()

        mirror = menu.addAction(t("browse.menu_create_mirror"))
        mirror.setEnabled(len(entries) == 1 and entries[0].kind == "DIR")
        mirror.triggered.connect(self._create_mirror_from_folder)
        menu.addSeparator()

        download = menu.addAction(t("browse.download_btn"))
        download.setEnabled(any(entry.kind == "FILE" for entry in entries))
        download.triggered.connect(self._download_selected)
        rename = menu.addAction(t("browse.rename_btn"))
        rename.setEnabled(len(entries) == 1)
        rename.triggered.connect(self._rename_selected)
        delete = menu.addAction(t("browse.delete_btn"))
        delete.triggered.connect(self._delete_selected)
        menu.exec(self.tree.viewport().mapToGlobal(position))

    # --- context-menu actions ------------------------------------------------

    def _copy_remote_paths(self) -> None:
        paths = [self._entry_relative_path(entry) for entry in self._selected_entries()]
        QApplication.clipboard().setText("\n".join(paths))
        self.status_label.setText(t("browse.path_copied", path=", ".join(paths)))

    def _copy_local_paths(self) -> None:
        paths = [str(self._entry_local_path(entry)) for entry in self._selected_entries()]
        QApplication.clipboard().setText("\n".join(paths))
        self.status_label.setText(t("browse.local_path_copied", path=", ".join(paths)))

    def _open_local_folder(self) -> None:
        entries = self._selected_entries()
        if len(entries) != 1:
            return
        path = self._entry_local_path(entries[0])
        target = path if path.is_dir() else path.parent
        if not target.is_dir():
            QMessageBox.warning(
                self, t("browse.open_local_failed_title"),
                t("browse.open_local_failed_body", path=str(target)),
            )
            return
        QDesktopServices.openUrl(QUrl.fromLocalFile(str(target)))

    def _set_folder_excluded(self, exclude: bool) -> None:
        """Toggle anchored selective-sync excludes for the selected folders."""
        paths = [
            self._entry_relative_path(entry)
            for entry in self._selected_entries() if entry.kind == "DIR"
        ]
        if not paths:
            return
        current = self.cfg.selective_sync_excludes()
        if exclude:
            updated = current + [path for path in paths if path not in current]
        else:
            updated = [
                path for path in current
                if not any(path == item or path.startswith(item + "/") for item in paths)
            ]
        self.cfg.set_selective_sync_excludes(updated)
        self.engine.wake()
        self.status_label.setText(
            t("browse.exclude_done" if exclude else "browse.include_done", path=", ".join(paths))
        )
        self.logger.log(
            "BROWSE_EXCLUDE" if exclude else "BROWSE_INCLUDE", ", ".join(paths)
        )

    def _set_sync_mode(self, mode: str) -> None:
        self.cfg.set_sync_mode(mode)
        self.engine.wake()
        self.status_label.setText(
            t("browse.sync_mode_changed", mode=t(SYNC_MODE_MENU_KEYS[mode]))
        )
        self.logger.log("BROWSE_SYNC_MODE", mode)

    def _create_mirror_from_folder(self) -> None:
        """Register the selected remote folder as a one-way mirror destination.

        The source must live outside the NASBox root: that is what makes the
        folder authoritative for its own subtree (pull skips it, push re-asserts
        the source's contents). The user supplies it in a small dialog.
        """
        entries = [entry for entry in self._selected_entries() if entry.kind == "DIR"]
        if len(entries) != 1:
            return
        dest = self._entry_relative_path(entries[0])
        default_source = os.path.join(os.path.expanduser("~"), entries[0].name)
        source, accepted = QInputDialog.getText(
            self, t("browse.menu_create_mirror"),
            t("browse.mirror_source_prompt"), text=default_source,
        )
        source = source.strip()
        if not accepted or not source:
            return
        from ..mirrors import validate_mirror

        ok, error = validate_mirror(self.cfg, source, dest)
        if not ok:
            QMessageBox.warning(self, t("browse.mirror_error_title"), error)
            return
        mirrors = self.cfg.mirrors()
        mirrors.append({
            "source": source,
            "dest": dest,
            "enabled": True,
            "last_sync": None,
            "error": "",
        })
        self.cfg.set_mirrors(mirrors)
        self.logger.log("BROWSE_MIRROR_CREATE", dest, detail=source)
        self.status_label.setText(t("browse.mirror_created", dest=dest))

    # --- drag & drop ---------------------------------------------------------

    def _drag_mime_data(self) -> QMimeData | None:
        """Text/uri-list of the corresponding local files, when they exist.

        The plain-text fallback always carries the local path even when the
        file has not been downloaded yet, so dragging a row out is still useful.
        """
        entries = self._selected_entries()
        if not entries:
            return None
        paths = [str(self._entry_local_path(entry)) for entry in entries]
        mime = QMimeData()
        urls = [QUrl.fromLocalFile(path) for path in paths if Path(path).exists()]
        if urls:
            mime.setUrls(urls)
        mime.setText("\n".join(paths))
        return mime

    def _upload_dropped_urls(self, urls: list[str]) -> None:
        """Stage dropped files in the local NASBox folder and let sync push them.

        Using the engine's own queue (rather than a raw rsync from this tab)
        keeps the shared NAS lock, preflight, journal and conflict handling in
        one place -- the same guarantees a normal local file edit gets.
        """
        if self._busy:
            return
        root = self.cfg.local_root()
        if not root:
            QMessageBox.warning(
                self, t("browse.upload_failed_title"), t("browse.upload_not_configured")
            )
            return
        sources: list[Path] = []
        for value in urls:
            path = Path(value)
            if path.name and path.exists() and (path.is_file() or path.is_dir()):
                sources.append(path)
        if not sources:
            return

        destinations = [
            Path(root, self._relative_path_for_name(source.name)) for source in sources
        ]
        # Guard against dragging a file from the NASBox folder back into itself
        # (and against copying a folder into its own subtree, which would
        # recurse forever).
        pairs = []
        for source, destination in zip(sources, destinations):
            source_real = source.resolve()
            destination_real = destination.resolve()
            if source_real == destination_real:
                continue
            try:
                inside_source = os.path.commonpath(
                    (str(destination_real), str(source_real))
                ) == str(source_real)
            except ValueError:
                inside_source = False
            if source.is_dir() and inside_source:
                continue
            pairs.append((source, destination))
        if not pairs:
            return

        names = ", ".join(source.name for source, _dest in pairs)
        has_collision = any(destination.exists() for _source, destination in pairs)
        body_key = "browse.upload_overwrite_body" if has_collision else "browse.upload_confirm_body"
        if QMessageBox.question(
            self, t("browse.upload_confirm_title"),
            t(body_key, names=names, path=self._path or "/"),
        ) != QMessageBox.StandardButton.Yes:
            return
        try:
            for source, destination in pairs:
                if source.is_dir():
                    shutil.copytree(source, destination, dirs_exist_ok=True)
                else:
                    destination.parent.mkdir(parents=True, exist_ok=True)
                    shutil.copy2(source, destination)
        except OSError as exc:
            QMessageBox.warning(self, t("browse.upload_failed_title"), str(exc))
            return

        relative_paths = [
            self._relative_path_for_name(source.name) for source, _dest in pairs
        ]
        self.sync_state.mark_pending(relative_paths)
        self.engine.wake()
        self.logger.log("BROWSE_UPLOAD_QUEUED", ", ".join(relative_paths))
        QMessageBox.information(
            self, t("browse.upload_queued_title"),
            t("browse.upload_queued_body", count=len(pairs)),
        )
        # No refresh here: the NAS does not have the files yet. The engine's
        # successful upload is reported through the normal transfer/history UI.

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

        # One lease for the whole selection: per-file leases were slow and let
        # another client interleave between two entries of the same batch.
        by_path = {
            (f"{self._path}/{entry.name}" if self._path else entry.name): entry
            for entry in entries
        }

        def delete_all():
            outcomes_by_path = rsync_ops.browse_delete_many(
                self.cfg, conn, list(by_path), device_id,
            )
            return [
                (by_path[path], ok, detail)
                for path, ok, detail in outcomes_by_path
            ]

        self._set_busy(t("browse.deleting"))
        run_in_background(self, "_delete_call", delete_all, self._on_delete_done)

    def _on_delete_done(self, result, exc: Exception | None) -> None:
        self._set_busy(None)
        if exc is not None:
            QMessageBox.warning(self, t("browse.delete_failed_title"), str(exc))
            return
        deleted = 0
        for entry, ok, _detail in result:
            if ok:
                remote_path = f"{self._path}/{entry.name}" if self._path else entry.name
                self.logger.log("BROWSE_DELETE", remote_path)
                deleted += 1
        if deleted:
            from .. import delete_safety

            delete_safety.shared().record(self.cfg, deleted, "browse")
            # The NAS trash now holds the authoritative version. Mark the paths
            # pending so the next reconciliation updates the local baseline
            # instead of treating a stale fingerprint as a local edit.
            deleted_paths = [
                f"{self._path}/{entry.name}" if self._path else entry.name
                for entry, ok, _detail in result if ok
            ]
            self.sync_state.mark_pending(deleted_paths)
            self.engine.wake()
        failed = [entry.name for entry, ok, _detail in result if not ok]
        self.refresh()
        if failed:
            QMessageBox.warning(self, t("browse.delete_failed_title"), t("browse.delete_partial_body", names=", ".join(failed)))


def _retention_description(value) -> str:
    if str(value) == "0":
        return t("browse.retention_never")
    return t("browse.retention_days", days=30 if value is None or value == "" else value)
