from __future__ import annotations

import os
from pathlib import Path

from PyQt6.QtWidgets import (
    QAbstractItemView, QComboBox, QFileDialog, QGroupBox, QHBoxLayout, QHeaderView, QLabel, QLineEdit, QMessageBox,
    QPushButton, QSpinBox, QTreeWidget, QTreeWidgetItem, QVBoxLayout, QWidget,
)

from .. import rsync_ops, trash
from ..config import Config
from ..engine import SyncEngine
from ..i18n import t
from ..logger import EventLogger
from .async_utils import run_in_background
from .format_utils import human_size

ROOT_GROUP_KEY = ""  # dirname() of a file directly in the NASBox root
VERSION_ROLE = 100  # Qt.ItemDataRole.UserRole -- holds a TrashVersion on leaf rows


class HistoryTab(QWidget):
    def __init__(self, cfg: Config, engine: SyncEngine, logger: EventLogger, parent=None) -> None:
        super().__init__(parent)
        self.cfg = cfg
        self.engine = engine
        self.logger = logger
        self._showing_remote = False
        self._remote_history_busy = False

        root = QVBoxLayout(self)

        retention_box = QGroupBox(t("history.retention_title"))
        retention_layout = QHBoxLayout(retention_box)

        retention_layout.addWidget(QLabel(t("history.local_trash_label")))
        self.local_spin = QSpinBox()
        self.local_spin.setRange(0, 3650)
        self.local_spin.setValue(int(cfg.get("retention_days_local") or 0))
        self.local_spin.setToolTip(t("history.local_trash_tooltip"))
        retention_layout.addWidget(self.local_spin)

        retention_layout.addWidget(QLabel(t("history.remote_retention_label")))
        self.remote_retention_label = QLabel(self._remote_retention_text())
        self.remote_retention_label.setStyleSheet("font-weight: bold;")
        retention_layout.addWidget(self.remote_retention_label)

        save_btn = QPushButton(t("history.save_local_btn"))
        save_btn.setToolTip(t("history.save_local_tooltip"))
        save_btn.clicked.connect(self._save_retention)
        retention_layout.addWidget(save_btn)
        root.addWidget(retention_box)

        self.source_description = QLabel(t("history.source_local_description"))
        self.source_description.setWordWrap(True)
        self.source_description.setStyleSheet("font-weight: bold;")
        root.addWidget(self.source_description)

        action_row = QHBoxLayout()
        self.prune_local_btn = QPushButton(t("history.prune_local_btn"))
        self.prune_local_btn.setToolTip(t("history.prune_local_tooltip"))
        self.prune_local_btn.clicked.connect(self._prune_local)
        action_row.addWidget(self.prune_local_btn)

        self.prune_remote_btn = QPushButton(t("history.prune_remote_btn"))
        self.prune_remote_btn.setToolTip(t("history.prune_remote_tooltip"))
        self.prune_remote_btn.clicked.connect(self._prune_remote)
        action_row.addWidget(self.prune_remote_btn)
        action_row.addStretch(1)
        root.addLayout(action_row)
        self.prune_local_status = QLabel()
        self.prune_local_status.setWordWrap(True)
        self.prune_local_status.setVisible(False)
        root.addWidget(self.prune_local_status)

        search_row = QHBoxLayout()
        search_row.addWidget(QLabel(t("history.search_label")))
        self.search_edit = QLineEdit()
        self.search_edit.setPlaceholderText(t("history.search_placeholder"))
        self.search_edit.textChanged.connect(lambda _: self._apply_filter())
        search_row.addWidget(self.search_edit)
        search_row.addWidget(QLabel(t("history.scope_label")))
        self.scope_combo = QComboBox()
        self.scope_combo.addItem(t("history.scope_local"), userData=False)
        self.scope_combo.addItem(t("history.scope_remote"), userData=True)
        self.scope_combo.currentIndexChanged.connect(self._change_scope)
        search_row.addWidget(self.scope_combo)
        self.refresh_btn = QPushButton(t("history.refresh_btn"))
        self.refresh_btn.setToolTip(t("history.refresh_tooltip"))
        self.refresh_btn.clicked.connect(self.refresh)
        search_row.addWidget(self.refresh_btn)
        root.addLayout(search_row)

        # The tree shows one source at a time: local history is read directly,
        # while NAS history is requested only when that scope is selected.
        self.scope_label = QLabel(t("history.list_scope_local"))
        self.scope_label.setObjectName("historyScope")
        self.scope_label.setWordWrap(True)
        root.addWidget(self.scope_label)

        # A tree, not a flat table: top-level rows are folders (grouped by the
        # version's containing directory), children are individual historical
        # versions of one file. Selecting a folder and restoring brings back
        # the most recent trashed version of every file in it in one go;
        # selecting a single version underneath restores just that file --
        # see _restore_selected().
        cols = [t("history.col_name"), t("history.col_version"), t("history.col_size"), t("history.col_age")]
        self.tree = QTreeWidget()
        self.tree.setColumnCount(len(cols))
        self.tree.setHeaderLabels(cols)
        self.tree.header().setSectionResizeMode(0, QHeaderView.ResizeMode.Stretch)
        self.tree.setSelectionMode(QAbstractItemView.SelectionMode.ExtendedSelection)
        # Double-click restores immediately -- a file (or a whole folder) you
        # just spotted in the list shouldn't need a select-then-find-the-button
        # round trip. The button below stays for discoverability/keyboard use.
        self.tree.itemDoubleClicked.connect(lambda item, _col: self._restore_item(item))
        root.addWidget(self.tree)

        buttons_row = QHBoxLayout()
        self.restore_btn = QPushButton(t("history.restore_btn"))
        self.restore_btn.setToolTip(t("history.restore_tooltip"))
        self.restore_btn.clicked.connect(self._restore_selected)
        buttons_row.addWidget(self.restore_btn)
        self.delete_btn = QPushButton(t("history.delete_btn"))
        self.delete_btn.setToolTip(t("history.delete_tooltip"))
        self.delete_btn.clicked.connect(self._delete_selected)
        buttons_row.addWidget(self.delete_btn)
        root.addLayout(buttons_row)

        self._versions: list[trash.TrashVersion | trash.RemoteTrashVersion] = []
        self._prune_local_busy = False
        self._prune_remote_busy = False
        self.refresh()

    def refresh(self) -> None:
        if self._showing_remote:
            self._refresh_remote()
            return
        self._versions = trash.list_local_versions()
        self._apply_filter()
        self.remote_retention_label.setText(self._remote_retention_text())

    def _change_scope(self, _index: int) -> None:
        self._showing_remote = bool(self.scope_combo.currentData())
        self.delete_btn.setVisible(not self._showing_remote)
        if self._showing_remote:
            self.source_description.setText(t("history.source_remote_description"))
            self.scope_label.setText(t("history.list_scope_remote_loading"))
            self._refresh_remote()
        else:
            self.source_description.setText(t("history.source_local_description"))
            self.scope_label.setText(t("history.list_scope_local"))
            self.refresh()

    def _refresh_remote(self) -> None:
        if self._remote_history_busy:
            return
        conn = self.engine.connection
        if conn is None:
            self.scope_label.setText(t("history.list_scope_remote_offline"))
            return
        self._remote_history_busy = True
        run_in_background(
            self, "_remote_history_call",
            lambda: trash.list_remote_versions(self.cfg, conn),
            self._on_remote_history_done,
        )

    def _on_remote_history_done(self, result, exc: Exception | None) -> None:
        self._remote_history_busy = False
        if exc is not None:
            self.scope_label.setText(t("history.list_scope_remote_failed", detail=str(exc)))
            return
        ok, versions, detail = result
        if not ok:
            self.scope_label.setText(t("history.list_scope_remote_failed", detail=detail))
            return
        self._versions = versions
        self.scope_label.setText(t("history.list_scope_remote"))
        self._apply_filter()

    # --- tree population ---

    def _apply_filter(self) -> None:
        needle = self.search_edit.text().strip().lower()
        versions = self._versions
        if needle:
            versions = [v for v in versions if needle in v.relative_path.lower()]

        groups: dict[str, list[trash.TrashVersion]] = {}
        for version in versions:
            key = os.path.dirname(version.relative_path)
            groups.setdefault(key, []).append(version)

        self.tree.clear()
        for key in sorted(groups.keys()):
            group_versions = groups[key]
            label = key if key != ROOT_GROUP_KEY else t("history.root_group_label")
            distinct_files = len({v.relative_path for v in group_versions})
            group_size = sum(v.size for v in group_versions)
            group_item = QTreeWidgetItem([
                t("history.group_label", name=label, count=distinct_files),
                "", human_size(group_size), "",
            ])
            self.tree.addTopLevelItem(group_item)
            for version in sorted(group_versions, key=lambda v: v.age_days):
                leaf = QTreeWidgetItem([
                    os.path.basename(version.relative_path),
                    version.timestamp,
                    human_size(version.size),
                    f"{version.age_days:.1f}",
                ])
                leaf.setData(0, VERSION_ROLE, version)
                group_item.addChild(leaf)
        self.tree.expandAll()

    def showEvent(self, event) -> None:
        super().showEvent(event)
        self.remote_retention_label.setText(self._remote_retention_text())

    def _remote_retention_text(self) -> str:
        value = self.cfg.get("retention_days_remote")
        if value is None or value == "":
            return t("history.remote_retention_unknown")
        if str(value) == "0":
            return t("history.remote_retention_never")
        return t("history.remote_retention_value", days=value)

    def _save_retention(self) -> None:
        self.cfg.set("retention_days_local", self.local_spin.value())
        QMessageBox.information(self, t("history.saved_title"), t("history.saved_body"))

    def _prune_local(self) -> None:
        if self._prune_local_busy:
            return
        self._prune_local_busy = True
        self.prune_local_btn.setEnabled(False)
        self.prune_local_status.setText(t("history.prune_local_scanning", count=0))
        self.prune_local_status.setVisible(True)

        def prune_and_list(progress):
            removed = trash.prune_local(self.cfg, self.logger, progress)
            return removed, trash.list_local_versions(progress)

        run_in_background(
            self, "_prune_local_call",
            prune_and_list,
            self._on_prune_local_done,
            self._on_prune_local_progress,
        )

    def _on_prune_local_progress(self, update) -> None:
        stage, current, total = update
        if stage == "scan":
            self.prune_local_status.setText(t("history.prune_local_scanning", count=current))
        elif stage == "delete":
            self.prune_local_status.setText(t("history.prune_local_deleting", current=current, total=total))
        else:
            self.prune_local_status.setText(t("history.prune_local_dirs"))

    def _on_prune_local_done(self, result, exc: Exception | None) -> None:
        self._prune_local_busy = False
        self.prune_local_btn.setEnabled(True)
        self.prune_local_status.setVisible(False)
        if exc is not None:
            QMessageBox.warning(self, t("history.prune_local_title"), str(exc))
            return
        removed, versions = result
        if not self._showing_remote:
            self._versions = versions
            self._apply_filter()
        if removed == 0:
            # Not an error -- prune_local only ever removes versions OLDER
            # than retention_days_local, on purpose (retention is a safety
            # net, not an "empty the trash" button). If everything shown is
            # more recent than that, zero removed is the correct outcome,
            # not a sign it silently failed -- say so explicitly, or it just
            # looks broken. See _delete_selected for "I want this gone now,
            # regardless of age".
            retention = int(self.cfg.get("retention_days_local") or 0)
            body = t("history.prune_local_disabled_body") if retention == 0 else t("history.prune_local_none_body", days=retention)
            QMessageBox.information(self, t("history.prune_local_title"), body)
        else:
            QMessageBox.information(self, t("history.prune_local_title"), t("history.prune_local_body", count=removed))

    def _prune_remote(self) -> None:
        if self._prune_remote_busy:
            return  # already running -- ignore a repeat click instead of overlapping calls
        conn = self.engine.connection
        if not conn:
            QMessageBox.warning(self, t("history.nas_unreachable_title"), t("history.nas_unreachable_body"))
            return
        # trigger_remote_prune shells out over SSH with a timeout up to 120s --
        # a big .sync-trash can genuinely take that long to pass over (the
        # remote script forks a couple of processes per trashed file). Running
        # it inline on this thread would freeze the whole window for as long
        # as that takes; run_in_background keeps the GUI responsive while it
        # waits, see async_utils.py.
        #
        # Deliberately NOT setEnabled(False) here: disabling a focused button
        # makes Qt immediately hand keyboard focus to the next widget in tab
        # order (the search field, in this tab) -- surprising, since nothing
        # the user did asked for that. A busy flag guards against a repeat
        # click just as well, without stealing focus.
        self._prune_remote_busy = True
        self.prune_remote_btn.setText(t("history.prune_remote_running"))
        run_in_background(
            self, "_prune_remote_call",
            lambda: trash.trigger_remote_prune(self.cfg, conn, self.logger),
            self._on_prune_remote_done,
        )

    def _on_prune_remote_done(self, result, exc: Exception | None) -> None:
        self._prune_remote_busy = False
        self.prune_remote_btn.setText(t("history.prune_remote_btn"))
        if exc is not None:
            QMessageBox.warning(self, t("history.prune_remote_failed_title"), str(exc))
            return
        ok, detail = result
        if ok:
            if self._showing_remote:
                self._refresh_remote()
            QMessageBox.information(self, t("history.prune_remote_title"), t("history.prune_remote_body"))
        else:
            QMessageBox.warning(self, t("history.prune_remote_failed_title"), detail)

    # --- restore: a single version (leaf) or a whole folder (top-level group) ---

    def _restore_selected(self) -> None:
        versions = self._selected_versions(latest_per_path=True)
        if not versions:
            return
        local_root = self.cfg.local_root()
        if not local_root:
            QMessageBox.warning(self, t("history.not_configured_title"), t("history.not_configured_body"))
            return
        if len(versions) == 1:
            self._restore_one(versions[0], local_root)
            return
        if isinstance(versions[0], trash.RemoteTrashVersion):
            self._restore_remote_many(versions, local_root)
            return
        if QMessageBox.question(
            self, t("history.confirm_restore_title"),
            t("history.confirm_restore_many_body", count=len(versions)),
        ) != QMessageBox.StandardButton.Yes:
            return
        self._restore_local_many(versions, local_root)

    def _restore_item(self, item: QTreeWidgetItem) -> None:
        local_root = self.cfg.local_root()
        if not local_root:
            QMessageBox.warning(self, t("history.not_configured_title"), t("history.not_configured_body"))
            return

        if item.parent() is None:
            self._restore_folder(item, local_root)
        else:
            self._restore_one(item.data(0, VERSION_ROLE), local_root)

    def _restore_one(self, version: "trash.TrashVersion", local_root: str) -> None:
        if isinstance(version, trash.RemoteTrashVersion):
            self._restore_remote_one(version, local_root)
            return
        if QMessageBox.question(
            self, t("history.confirm_restore_title"),
            t("history.confirm_restore_body", path=version.relative_path, timestamp=version.timestamp),
        ) != QMessageBox.StandardButton.Yes:
            return

        if trash.restore_version(version, local_root):
            QMessageBox.information(self, t("history.restore_done_title"), t("history.restore_done_body", path=version.relative_path))
        else:
            QMessageBox.warning(self, t("history.restore_failed_title"), t("history.restore_failed_body"))

    def _restore_remote_one(self, version: trash.RemoteTrashVersion, local_root: str) -> None:
        default_path = os.path.join(local_root, version.relative_path)
        destination, _filter = QFileDialog.getSaveFileName(
            self, t("history.remote_restore_picker_title"), default_path,
        )
        if not destination:
            return
        conn = self.engine.connection
        if conn is None:
            QMessageBox.warning(self, t("history.nas_unreachable_title"), t("history.nas_unreachable_body"))
            return
        run_in_background(
            self, "_restore_remote_call",
            lambda: rsync_ops.restore_remote_version(self.cfg, conn, version.remote_trash_relative_path, Path(destination)),
            lambda result, exc: self._on_restore_remote_done(version, destination, result, exc),
        )

    def _restore_remote_many(self, versions: list[trash.RemoteTrashVersion], local_root: str) -> None:
        destination_root = QFileDialog.getExistingDirectory(
            self, t("history.remote_restore_many_picker_title"), local_root,
        )
        if not destination_root:
            return
        if QMessageBox.question(
            self, t("history.confirm_restore_title"),
            t("history.confirm_restore_many_body", count=len(versions)),
        ) != QMessageBox.StandardButton.Yes:
            return
        conn = self.engine.connection
        if conn is None:
            QMessageBox.warning(self, t("history.nas_unreachable_title"), t("history.nas_unreachable_body"))
            return

        def restore_all():
            outcomes = []
            for version in versions:
                destination = Path(destination_root, version.relative_path)
                outcomes.append((version, *rsync_ops.restore_remote_version(
                    self.cfg, conn, version.remote_trash_relative_path, destination,
                )))
            return outcomes

        run_in_background(self, "_restore_remote_many_call", restore_all, self._on_restore_remote_many_done)

    def _on_restore_remote_many_done(self, result, exc: Exception | None) -> None:
        if self._showing_remote:
            self._refresh_remote()
        if exc is not None:
            QMessageBox.warning(self, t("history.restore_failed_title"), str(exc))
            return
        restored = sum(1 for _version, ok, _detail in result if ok)
        for version, ok, detail in result:
            if ok:
                self.logger.log("RESTORE_REMOTE_VERSION", version.relative_path, detail=detail)
        self._show_batch_result(restored, len(result) - restored)

    def _on_restore_remote_done(self, version, destination: str, result, exc: Exception | None) -> None:
        if self._showing_remote:
            self._refresh_remote()
        if exc is not None:
            QMessageBox.warning(self, t("history.restore_failed_title"), str(exc))
            return
        ok, detail = result
        if ok:
            self.logger.log("RESTORE_REMOTE_VERSION", version.relative_path, detail=destination)
            QMessageBox.information(self, t("history.restore_done_title"), t("history.remote_restore_done_body", path=destination))
        else:
            QMessageBox.warning(self, t("history.restore_failed_title"), detail)

    def _restore_folder(self, group_item: QTreeWidgetItem, local_root: str) -> None:
        if self._showing_remote:
            QMessageBox.information(self, t("history.remote_restore_one_title"), t("history.remote_restore_one_body"))
            return
        # One version per distinct file, the most recent one -- restoring a
        # folder means "bring back the latest known state of everything in
        # it", not literally replaying every historical version on top of
        # each other (restore_version always overwrites, so only the last
        # write per path would matter anyway).
        latest_by_path: dict[str, trash.TrashVersion] = {}
        for i in range(group_item.childCount()):
            version = group_item.child(i).data(0, VERSION_ROLE)
            current = latest_by_path.get(version.relative_path)
            if current is None or version.age_days < current.age_days:
                latest_by_path[version.relative_path] = version

        count = len(latest_by_path)
        folder_label = group_item.text(0)
        if QMessageBox.question(
            self, t("history.confirm_restore_folder_title"),
            t("history.confirm_restore_folder_body", count=count, folder=folder_label),
        ) != QMessageBox.StandardButton.Yes:
            return

        restored, failed = 0, 0
        for version in latest_by_path.values():
            if trash.restore_version(version, local_root):
                restored += 1
            else:
                failed += 1

        if failed:
            QMessageBox.warning(
                self, t("history.restore_partial_title"),
                t("history.restore_partial_body", restored=restored, failed=failed),
            )
        else:
            QMessageBox.information(self, t("history.restore_done_title"), t("history.restore_all_done_body", count=restored))

    def _restore_local_many(self, versions: list[trash.TrashVersion], local_root: str) -> None:
        restored = sum(1 for version in versions if trash.restore_version(version, local_root))
        self._show_batch_result(restored, len(versions) - restored)

    def _show_batch_result(self, restored: int, failed: int) -> None:
        if failed:
            QMessageBox.warning(
                self, t("history.restore_partial_title"),
                t("history.restore_partial_body", restored=restored, failed=failed),
            )
        else:
            QMessageBox.information(self, t("history.restore_done_title"), t("history.restore_all_done_body", count=restored))

    # --- permanent delete: a single version (leaf) or every version in a
    # folder (top-level group) -- independent of retention_days_local, for
    # "I want this gone now" instead of "gone once it's old enough". ---

    def _delete_selected(self) -> None:
        versions = self._selected_versions(latest_per_path=False)
        if not versions:
            return
        if len(versions) == 1:
            self._delete_one(versions[0])
            return
        if QMessageBox.question(
            self, t("history.confirm_delete_title"),
            t("history.confirm_delete_many_body", count=len(versions)),
        ) != QMessageBox.StandardButton.Yes:
            return
        deleted = sum(1 for version in versions if trash.delete_version(version))
        self.refresh()
        failed = len(versions) - deleted
        if failed:
            QMessageBox.warning(
                self, t("history.delete_partial_title"),
                t("history.delete_partial_body", deleted=deleted, failed=failed),
            )
        else:
            QMessageBox.information(self, t("history.delete_done_title"), t("history.delete_all_done_body", count=deleted))

    def _delete_item(self, item: QTreeWidgetItem) -> None:
        if item.parent() is None:
            self._delete_folder(item)
        else:
            self._delete_one(item.data(0, VERSION_ROLE))

    def _delete_one(self, version: "trash.TrashVersion") -> None:
        if QMessageBox.question(
            self, t("history.confirm_delete_title"),
            t("history.confirm_delete_body", path=version.relative_path, timestamp=version.timestamp),
        ) != QMessageBox.StandardButton.Yes:
            return

        if trash.delete_version(version):
            self.refresh()
        else:
            QMessageBox.warning(self, t("history.delete_failed_title"), t("history.delete_failed_body"))

    def _delete_folder(self, group_item: QTreeWidgetItem) -> None:
        # Every version in the folder, not just the latest per file (unlike
        # restore_folder) -- deleting is about reclaiming space, so a version
        # left behind because it wasn't "the latest" would defeat the point.
        versions = [group_item.child(i).data(0, VERSION_ROLE) for i in range(group_item.childCount())]
        count = len(versions)
        folder_label = group_item.text(0)
        if QMessageBox.question(
            self, t("history.confirm_delete_folder_title"),
            t("history.confirm_delete_folder_body", count=count, folder=folder_label),
        ) != QMessageBox.StandardButton.Yes:
            return

        deleted = sum(1 for v in versions if trash.delete_version(v))
        self.refresh()
        failed = count - deleted
        if failed:
            QMessageBox.warning(
                self, t("history.delete_partial_title"),
                t("history.delete_partial_body", deleted=deleted, failed=failed),
            )
        else:
            QMessageBox.information(self, t("history.delete_done_title"), t("history.delete_all_done_body", count=deleted))

    def _selected_versions(self, latest_per_path: bool) -> list[trash.TrashVersion | trash.RemoteTrashVersion]:
        """Collect leaves from selected rows, expanding selected folder groups.

        A group and one of its leaves can both be selected, so de-duplicate the
        concrete historical versions before optionally keeping only the newest
        selected version of each original file for restores.
        """
        versions: list[trash.TrashVersion | trash.RemoteTrashVersion] = []
        for item in self.tree.selectedItems():
            if item.parent() is None:
                versions.extend(item.child(index).data(0, VERSION_ROLE) for index in range(item.childCount()))
            else:
                versions.append(item.data(0, VERSION_ROLE))

        unique = {}
        for version in versions:
            key = version.trash_path if isinstance(version, trash.TrashVersion) else version.remote_trash_relative_path
            unique[key] = version
        versions = list(unique.values())
        if not latest_per_path:
            return versions

        newest_by_path = {}
        for version in versions:
            current = newest_by_path.get(version.relative_path)
            if current is None or version.age_days < current.age_days:
                newest_by_path[version.relative_path] = version
        return list(newest_by_path.values())
