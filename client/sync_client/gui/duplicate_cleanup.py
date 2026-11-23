"""Guided cleanup of the duplicate files reported by Storage insights.

``DuplicateGroup.paths`` are relative to ``cfg.local_root()``.  Removals always
go through :func:`sync_client.trash.move_to_local_trash` -- this module never
unlinks a file directly -- and the first path of each group (the "Mantieni"
entry) is only ever shown, never offered for removal.

The translations live here instead of in ``i18n.py`` so this self-contained
feature can be dropped into an existing 1.29.0 client without touching shared
files.  They are registered into the central table at import time, which makes
plain ``t("dupclean.*")`` lookups work everywhere.
"""
from __future__ import annotations

import shutil
from dataclasses import dataclass, field
from pathlib import Path
from typing import Sequence

from PyQt6.QtCore import Qt
from PyQt6.QtWidgets import (
    QDialog, QHBoxLayout, QLabel, QMessageBox, QPushButton, QTreeWidget,
    QTreeWidgetItem, QVBoxLayout,
)

from .. import i18n, trash
from ..config import Config
from ..i18n import t
from ..metrics_worker import DuplicateGroup
from .async_utils import run_in_background
from .format_utils import human_size

#: Keys owned by this module. ``i18n.py`` is deliberately left untouched.
DUP_CLEAN_STRINGS: dict[str, dict[str, str]] = {
    "dupclean.title": {
        "it": "Pulizia file duplicati",
        "en": "Duplicate file cleanup",
    },
    "dupclean.intro": {
        "it": "Seleziona i duplicati da spostare nel cestino. Per ogni gruppo il primo file (\"Mantieni\") resta intatto.",
        "en": "Select the duplicates to move to trash. The first file of each group (\"Keep\") is always left untouched.",
    },
    "dupclean.group": {
        "it": "Gruppo {index}: {size} × {count} file",
        "en": "Group {index}: {size} × {count} files",
    },
    "dupclean.keep_path": {
        "it": "Mantieni: {path}",
        "en": "Keep: {path}",
    },
    "dupclean.keep_tooltip": {
        "it": "Questo file viene sempre mantenuto.",
        "en": "This file is always kept.",
    },
    "dupclean.reclaim": {
        "it": "Spazio recuperabile: {size}",
        "en": "Reclaimable space: {size}",
    },
    "dupclean.select_all": {
        "it": "Seleziona tutti",
        "en": "Select all",
    },
    "dupclean.select_none": {
        "it": "Nessuno",
        "en": "None",
    },
    "dupclean.trash_button": {
        "it": "Sposta nel cestino",
        "en": "Move to trash",
    },
    "dupclean.cancel": {
        "it": "Annulla",
        "en": "Cancel",
    },
    "dupclean.empty": {
        "it": "Nessun file duplicato da pulire.",
        "en": "No duplicate files to clean up.",
    },
    "dupclean.no_selection_title": {
        "it": "Nessuna selezione",
        "en": "Nothing selected",
    },
    "dupclean.no_selection_body": {
        "it": "Seleziona almeno un file da spostare nel cestino.",
        "en": "Select at least one file to move to trash.",
    },
    "dupclean.confirm_title": {
        "it": "Conferma pulizia duplicati",
        "en": "Confirm duplicate cleanup",
    },
    "dupclean.confirm_body": {
        "it": "Spostare {count} file nel cestino?\n\nSpazio recuperabile: {size}.\nI file \"Mantieni\" non verranno toccati.",
        "en": "Move {count} files to trash?\n\nReclaimable space: {size}.\nThe \"Keep\" files will not be touched.",
    },
    "dupclean.running": {
        "it": "Spostamento nel cestino…",
        "en": "Moving to trash…",
    },
    "dupclean.result_title": {
        "it": "Pulizia completata",
        "en": "Cleanup finished",
    },
    "dupclean.result_body": {
        "it": "{moved} file spostati nel cestino, {errors} errori.",
        "en": "{moved} files moved to trash, {errors} errors.",
    },
    "dupclean.error_title": {
        "it": "Errore durante la pulizia",
        "en": "Cleanup error",
    },
    "dupclean.error_body": {
        "it": "Impossibile completare la pulizia: {error}",
        "en": "Could not complete the cleanup: {error}",
    },
    "dupclean.error_outside_root": {
        "it": "Percorso fuori dalla cartella NASBox, ignorato: {path}",
        "en": "Path outside the NASBox folder, skipped: {path}",
    },
    "dupclean.error_missing": {
        "it": "File non trovato: {path}",
        "en": "File not found: {path}",
    },
    "dupclean.error_move": {
        "it": "Impossibile spostare nel cestino: {path}",
        "en": "Could not move to trash: {path}",
    },
    "dupclean.error_keep": {
        "it": "Il file \"Mantieni\" non può essere rimosso: {path}",
        "en": "The \"Keep\" file cannot be removed: {path}",
    },
}

for _key, _entry in DUP_CLEAN_STRINGS.items():
    i18n.STRINGS.setdefault(_key, dict(_entry))
del _key, _entry

_GROUP_ROLE = Qt.ItemDataRole.UserRole
_PATH_ROLE = Qt.ItemDataRole.UserRole + 1


@dataclass(frozen=True)
class CleanupTarget:
    """One removable path, together with the group it belongs to."""

    group: DuplicateGroup
    path: str


@dataclass
class CleanupResult:
    moved: list[str] = field(default_factory=list)
    errors: list[str] = field(default_factory=list)


class DuplicateCleanupDialog(QDialog):
    """Let the user pick which duplicate copies should go to the local trash.

    One checkable row per group acts as a master switch; the group's first path
    is shown as a non-checkable "Mantieni" row and the remaining paths are
    individually checkable removal candidates.
    """

    def __init__(
        self, cfg: Config, groups: Sequence[DuplicateGroup], parent=None,
    ) -> None:
        super().__init__(parent)
        self.cfg = cfg
        self.groups = tuple(groups)
        self.last_result = CleanupResult()
        self._updating = False
        self._cleanup_call = None  # keeps the QThread alive until it finishes

        self.setWindowTitle(t("dupclean.title"))
        self.setMinimumWidth(520)
        layout = QVBoxLayout(self)

        intro = QLabel(t("dupclean.intro"))
        intro.setWordWrap(True)
        layout.addWidget(intro)

        self.tree = QTreeWidget()
        self.tree.setColumnCount(1)
        self.tree.setHeaderHidden(True)
        self.tree.itemChanged.connect(self._on_item_changed)
        layout.addWidget(self.tree)

        self.reclaim_label = QLabel()
        layout.addWidget(self.reclaim_label)

        buttons = QHBoxLayout()
        self.select_all_btn = QPushButton(t("dupclean.select_all"))
        self.select_none_btn = QPushButton(t("dupclean.select_none"))
        self.trash_btn = QPushButton(t("dupclean.trash_button"))
        self.cancel_btn = QPushButton(t("dupclean.cancel"))
        for button in (
            self.select_all_btn, self.select_none_btn,
            self.trash_btn, self.cancel_btn,
        ):
            buttons.addWidget(button)
        layout.addLayout(buttons)

        self.select_all_btn.clicked.connect(self.select_all)
        self.select_none_btn.clicked.connect(self.select_none)
        self.trash_btn.clicked.connect(self._on_trash_clicked)
        self.cancel_btn.clicked.connect(self.reject)

        self._populate()

    # --- tree -------------------------------------------------------------
    def _populate(self) -> None:
        self._updating = True
        try:
            self.tree.clear()
            if not self.groups:
                empty = QTreeWidgetItem(self.tree)
                empty.setText(0, t("dupclean.empty"))
                empty.setFlags(Qt.ItemFlag.NoItemFlags)
            for index, group in enumerate(self.groups, start=1):
                group_item = QTreeWidgetItem(self.tree)
                group_item.setText(0, t(
                    "dupclean.group", index=index,
                    size=human_size(group.size_bytes), count=len(group.paths),
                ))
                group_item.setFlags(
                    group_item.flags()
                    | Qt.ItemFlag.ItemIsEnabled
                    | Qt.ItemFlag.ItemIsUserCheckable
                )
                group_item.setData(0, _GROUP_ROLE, group)
                group_item.setCheckState(0, Qt.CheckState.Unchecked)
                for path_index, relative in enumerate(group.paths):
                    child = QTreeWidgetItem(group_item)
                    if path_index == 0:
                        child.setText(0, t("dupclean.keep_path", path=relative))
                        child.setFlags(
                            child.flags() & ~Qt.ItemFlag.ItemIsUserCheckable
                        )
                        child.setToolTip(0, t("dupclean.keep_tooltip"))
                    else:
                        child.setText(0, relative)
                        child.setFlags(
                            child.flags()
                            | Qt.ItemFlag.ItemIsEnabled
                            | Qt.ItemFlag.ItemIsUserCheckable
                        )
                        child.setData(0, _PATH_ROLE, relative)
                        child.setCheckState(0, Qt.CheckState.Unchecked)
            self.tree.expandAll()
        finally:
            self._updating = False
        self._update_summary()

    @staticmethod
    def _is_removable(item: QTreeWidgetItem) -> bool:
        return bool(
            item.flags() & Qt.ItemFlag.ItemIsUserCheckable
            and item.data(0, _PATH_ROLE)
        )

    def _on_item_changed(self, item: QTreeWidgetItem, _column: int) -> None:
        if self._updating:
            return
        self._updating = True
        try:
            if item.parent() is None:
                self._check_group_children(
                    item, item.checkState(0) != Qt.CheckState.Unchecked,
                )
            elif self._is_removable(item):
                parent = item.parent()
                states = [
                    parent.child(i).checkState(0)
                    for i in range(parent.childCount())
                    if self._is_removable(parent.child(i))
                ]
                if all(state == Qt.CheckState.Checked for state in states):
                    state = Qt.CheckState.Checked
                elif all(state == Qt.CheckState.Unchecked for state in states):
                    state = Qt.CheckState.Unchecked
                else:
                    state = Qt.CheckState.PartiallyChecked
                parent.setCheckState(0, state)
        finally:
            self._updating = False
        self._update_summary()

    def _check_group_children(self, group_item: QTreeWidgetItem, checked: bool) -> None:
        state = Qt.CheckState.Checked if checked else Qt.CheckState.Unchecked
        for index in range(group_item.childCount()):
            child = group_item.child(index)
            if self._is_removable(child):
                child.setCheckState(0, state)

    def select_all(self) -> None:
        self._set_all(True)

    def select_none(self) -> None:
        self._set_all(False)

    def _set_all(self, checked: bool) -> None:
        self._updating = True
        try:
            for index in range(self.tree.topLevelItemCount()):
                group_item = self.tree.topLevelItem(index)
                if group_item.data(0, _GROUP_ROLE) is None:
                    continue
                group_item.setCheckState(
                    0,
                    Qt.CheckState.Checked if checked else Qt.CheckState.Unchecked,
                )
                self._check_group_children(group_item, checked)
        finally:
            self._updating = False
        self._update_summary()

    # --- selection / accounting ------------------------------------------
    def selected_targets(self) -> list[CleanupTarget]:
        """Removal candidates currently checked, never the "Mantieni" row."""
        targets: list[CleanupTarget] = []
        for index in range(self.tree.topLevelItemCount()):
            group_item = self.tree.topLevelItem(index)
            group = group_item.data(0, _GROUP_ROLE)
            if group is None:
                continue
            for child_index in range(group_item.childCount()):
                child = group_item.child(child_index)
                if (
                    self._is_removable(child)
                    and child.checkState(0) == Qt.CheckState.Checked
                ):
                    targets.append(CleanupTarget(group, child.data(0, _PATH_ROLE)))
        return targets

    def reclaimable_bytes(self) -> int:
        return sum(target.group.size_bytes for target in self.selected_targets())

    def _update_summary(self) -> None:
        self.reclaim_label.setText(
            t("dupclean.reclaim", size=human_size(self.reclaimable_bytes()))
        )

    # --- path safety ------------------------------------------------------
    @staticmethod
    def resolve_relative_path(relative: str, local_root: str) -> Path | None:
        """Resolve a duplicate path, rejecting anything outside ``local_root``.

        Absolute paths, ``..`` traversal and symlinks escaping the synced tree
        all return None.  Callers must only ever hand the result to the trash
        helper.
        """
        relative_path = Path(relative)
        if relative_path.is_absolute():
            return None
        root = Path(local_root).resolve()
        candidate = (root / relative_path).resolve()
        try:
            candidate.relative_to(root)
        except ValueError:
            return None
        if candidate == root:
            return None
        return candidate

    # --- action -----------------------------------------------------------
    def _on_trash_clicked(self) -> None:
        targets = self.selected_targets()
        if not targets:
            QMessageBox.information(
                self, t("dupclean.no_selection_title"),
                t("dupclean.no_selection_body"),
            )
            return
        if QMessageBox.question(
            self, t("dupclean.confirm_title"),
            t("dupclean.confirm_body", count=len(targets), size=human_size(self.reclaimable_bytes())),
        ) != QMessageBox.StandardButton.Yes:
            return

        local_root = str(Path(self.cfg.local_root()).resolve())
        self.trash_btn.setEnabled(False)
        self.trash_btn.setText(t("dupclean.running"))
        run_in_background(
            self, "_cleanup_call",
            lambda: self._move_targets_to_trash(targets, local_root),
            self._on_cleanup_done,
        )

    def _move_targets_to_trash(
        self, targets: Sequence[CleanupTarget], local_root: str,
    ) -> CleanupResult:
        """Worker (runs off the GUI thread). Returns details for the summary."""
        result = CleanupResult()
        keep_paths = {group.paths[0] for group in self.groups if group.paths}
        for target in targets:
            relative = target.path
            if relative in keep_paths:
                result.errors.append(t("dupclean.error_keep", path=relative))
                continue
            source = self.resolve_relative_path(relative, local_root)
            if source is None:
                result.errors.append(
                    t("dupclean.error_outside_root", path=relative)
                )
                continue
            if not source.is_file():
                result.errors.append(t("dupclean.error_missing", path=relative))
                continue
            try:
                moved = trash.move_to_local_trash(source, local_root)
            except (OSError, shutil.Error):
                moved = False
            if moved:
                result.moved.append(relative)
            else:
                result.errors.append(t("dupclean.error_move", path=relative))
        return result

    def _on_cleanup_done(
        self, result: CleanupResult | None, error: Exception | None,
    ) -> None:
        self.trash_btn.setEnabled(True)
        self.trash_btn.setText(t("dupclean.trash_button"))
        if error is not None or result is None:
            detail = str(error) if error is not None else ""
            self.last_result = CleanupResult()
            QMessageBox.warning(
                self, t("dupclean.error_title"),
                t("dupclean.error_body", error=detail),
            )
            return

        self.last_result = result
        moved_paths = set(result.moved)
        self._updating = True
        try:
            for index in range(self.tree.topLevelItemCount()):
                group_item = self.tree.topLevelItem(index)
                for child_index in range(group_item.childCount()):
                    child = group_item.child(child_index)
                    if child.data(0, _PATH_ROLE) in moved_paths:
                        child.setCheckState(0, Qt.CheckState.Unchecked)
                        child.setDisabled(True)
        finally:
            self._updating = False
        self._update_summary()

        QMessageBox.information(
            self, t("dupclean.result_title"),
            t("dupclean.result_body", moved=len(result.moved), errors=len(result.errors)),
        )
        if result.moved:
            self.accept()
