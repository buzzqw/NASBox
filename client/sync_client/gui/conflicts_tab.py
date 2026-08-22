from __future__ import annotations

from pathlib import Path

from PyQt6.QtCore import Qt, QUrl
from PyQt6.QtGui import QColor, QDesktopServices
from PyQt6.QtWidgets import (
    QComboBox, QGroupBox, QHBoxLayout, QHeaderView, QLabel, QMessageBox, QPushButton,
    QTreeWidget, QTreeWidgetItem, QVBoxLayout, QWidget,
)

from .. import conflicts
from ..config import Config
from ..engine import SyncEngine
from ..i18n import t
from .async_utils import run_in_background


GROUP_ROLE = Qt.ItemDataRole.UserRole


class ConflictsTab(QWidget):
    def __init__(self, cfg: Config, engine: SyncEngine, parent=None) -> None:
        super().__init__(parent)
        self.cfg = cfg
        self.engine = engine
        self._groups: list[conflicts.ConflictGroup] = []
        self._busy = False

        root = QVBoxLayout(self)
        notice = QLabel(t("conflicts.notice"))
        notice.setWordWrap(True)
        notice.setObjectName("conflictNotice")
        root.addWidget(notice)

        action_row = QHBoxLayout()
        self.count_label = QLabel(t("conflicts.count_unknown"))
        action_row.addWidget(self.count_label)
        action_row.addStretch(1)
        self.refresh_btn = QPushButton(t("conflicts.refresh_btn"))
        self.refresh_btn.clicked.connect(self.refresh)
        action_row.addWidget(self.refresh_btn)
        root.addLayout(action_row)

        box = QGroupBox(t("conflicts.list_title"))
        box_layout = QVBoxLayout(box)
        self.tree = QTreeWidget()
        self.tree.setColumnCount(3)
        self.tree.setHeaderLabels([
            t("conflicts.col_original"),
            t("conflicts.col_alternatives"),
            t("conflicts.col_status"),
        ])
        self.tree.header().setStretchLastSection(False)
        self.tree.header().setSectionResizeMode(0, QHeaderView.ResizeMode.Stretch)
        self.tree.header().setSectionResizeMode(1, QHeaderView.ResizeMode.ResizeToContents)
        self.tree.header().setSectionResizeMode(2, QHeaderView.ResizeMode.ResizeToContents)
        self.tree.itemSelectionChanged.connect(self._selection_changed)
        box_layout.addWidget(self.tree)

        choose_row = QHBoxLayout()
        choose_row.addWidget(QLabel(t("conflicts.keep_label")))
        self.candidate_combo = QComboBox()
        self.candidate_combo.currentIndexChanged.connect(lambda _index: self._update_buttons())
        choose_row.addWidget(self.candidate_combo, 1)
        self.open_btn = QPushButton(t("conflicts.open_btn"))
        self.open_btn.clicked.connect(self._open_selected)
        choose_row.addWidget(self.open_btn)
        self.keep_btn = QPushButton(t("conflicts.keep_btn"))
        self.keep_btn.setObjectName("primaryButton")
        self.keep_btn.clicked.connect(self._keep_selected)
        choose_row.addWidget(self.keep_btn)
        box_layout.addLayout(choose_row)
        root.addWidget(box, 1)
        self._update_buttons()
        self.refresh()

    def refresh(self) -> None:
        local_root = self.cfg.local_root()
        if not local_root:
            self._groups = []
            self._populate()
            self.count_label.setText(t("conflicts.no_folder"))
            return
        if self._busy:
            return
        self._busy = True
        self.refresh_btn.setEnabled(False)
        self.count_label.setText(t("conflicts.scanning"))
        run_in_background(
            self, "_conflicts_scan_call",
            lambda: conflicts.scan_conflict_groups(local_root),
            self._on_scan_done,
        )

    def showEvent(self, event) -> None:
        super().showEvent(event)
        self.refresh()

    def _on_scan_done(self, result, exc: Exception | None) -> None:
        self._busy = False
        self.refresh_btn.setEnabled(True)
        if exc is not None:
            self.count_label.setText(t("conflicts.scan_error", detail=str(exc)))
            return
        self._groups = result
        self._populate()
        self.count_label.setText(
            t("conflicts.count", groups=len(result), files=sum(len(group.candidates) for group in result))
            if result else t("conflicts.none")
        )

    def _populate(self) -> None:
        self.tree.clear()
        for group in self._groups:
            relative = self._relative(group.original)
            original_state = t("conflicts.original_present") if group.original.exists() else t("conflicts.original_missing")
            item = QTreeWidgetItem([relative, str(len(group.candidates)), original_state])
            item.setData(0, GROUP_ROLE, group)
            for column in range(3):
                item.setBackground(column, QColor("#fff3cd"))
            self.tree.addTopLevelItem(item)
        if self.tree.topLevelItemCount():
            self.tree.setCurrentItem(self.tree.topLevelItem(0))
        else:
            self.candidate_combo.clear()
        self._update_buttons()

    def _selection_changed(self) -> None:
        self.candidate_combo.clear()
        group = self._selected_group()
        if group is None:
            self._update_buttons()
            return
        if group.original.exists():
            self.candidate_combo.addItem(
                t("conflicts.original_option"), group.original,
            )
        for path in group.candidates:
            self.candidate_combo.addItem(self._relative(path), path)
        self._update_buttons()

    def _selected_group(self) -> conflicts.ConflictGroup | None:
        item = self.tree.currentItem()
        return item.data(0, GROUP_ROLE) if item is not None else None

    def _selected_candidate(self) -> Path | None:
        value = self.candidate_combo.currentData()
        return value if isinstance(value, Path) else None

    def _update_buttons(self) -> None:
        selected = self._selected_candidate() is not None
        self.open_btn.setEnabled(selected)
        self.keep_btn.setEnabled(selected and not self._busy)

    def _open_selected(self) -> None:
        path = self._selected_candidate()
        if path is not None:
            QDesktopServices.openUrl(QUrl.fromLocalFile(str(path)))

    def _keep_selected(self) -> None:
        group = self._selected_group()
        chosen = self._selected_candidate()
        local_root = self.cfg.local_root()
        if group is None or chosen is None or not local_root:
            return
        if QMessageBox.question(
            self, t("conflicts.confirm_title"),
            t("conflicts.confirm_body", path=self._relative(chosen)),
        ) != QMessageBox.StandardButton.Yes:
            return
        ok, detail = conflicts.resolve_conflict(group, chosen, local_root)
        if not ok:
            QMessageBox.warning(self, t("conflicts.resolve_failed_title"), detail)
            return
        QMessageBox.information(self, t("conflicts.resolve_done_title"), t("conflicts.resolve_done_body"))
        self.refresh()

    def _relative(self, path: Path) -> str:
        try:
            return str(path.relative_to(self.cfg.local_root()))
        except ValueError:
            return str(path)
