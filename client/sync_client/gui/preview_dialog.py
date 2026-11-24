"""Finestra di anteprima delle modifiche prima di applicarle.

Tutto il calcolo è già stato fatto dal client; qui si mostra il risultato in modo
leggibile, con conteggi per cartella e le azioni che l'utente può decidere.
"""
from __future__ import annotations

from PyQt6.QtCore import pyqtSignal
from PyQt6.QtWidgets import (
    QDialog, QDialogButtonBox, QHBoxLayout, QLabel, QPushButton,
    QTableWidget, QTableWidgetItem, QVBoxLayout,
)

from ..i18n import t


class ChangePreviewDialog(QDialog):
    open_log_requested = pyqtSignal()
    confirm_requested = pyqtSignal()

    def __init__(self, data: dict, parent=None) -> None:
        super().__init__(parent)
        self.setWindowTitle(t("preview.title"))
        self.setMinimumWidth(620)
        self.setMinimumHeight(420)
        layout = QVBoxLayout(self)

        self.summary_label = QLabel(str(data.get("summary") or t("preview.empty")))
        self.summary_label.setWordWrap(True)
        layout.addWidget(self.summary_label)

        self.table = QTableWidget()
        self.table.setColumnCount(3)
        self.table.setHorizontalHeaderLabels([
            t("preview.column_dir"), t("preview.column_missing"), t("preview.column_state"),
        ])
        self.table.verticalHeader().setVisible(False)
        anomalies = {
            str(item.get("directory") or "")
            for item in (data.get("anomalies") or [])
            if isinstance(item, dict)
        }
        rows = [
            item for item in (data.get("top_dirs") or [])
            if isinstance(item, dict)
        ]
        self.table.setRowCount(len(rows))
        for index, item in enumerate(rows):
            directory = str(item.get("directory") or "")
            self.table.setItem(index, 0, QTableWidgetItem(directory))
            self.table.setItem(
                index, 1,
                QTableWidgetItem(f"{item.get('missing', 0)}/{item.get('remote', 0)}"),
            )
            self.table.setItem(index, 2, QTableWidgetItem(
                t("preview.state_anomaly") if directory in anomalies
                else t("preview.state_normal")
            ))
        self.table.resizeColumnsToContents()
        layout.addWidget(self.table)

        renames = list(data.get("renames") or [])
        deletions = list(data.get("deletions") or [])
        if renames:
            label = QLabel(t("preview.renames", items="\n".join(f"• {item}" for item in renames)))
            label.setWordWrap(True)
            layout.addWidget(label)
        if deletions:
            label = QLabel(t("preview.deletions", items="\n".join(f"• {item}" for item in deletions)))
            label.setWordWrap(True)
            layout.addWidget(label)

        actions = QHBoxLayout()
        log_btn = QPushButton(t("preview.open_log"))
        log_btn.clicked.connect(self.open_log_requested.emit)
        actions.addWidget(log_btn)
        confirm_btn = QPushButton(t("preview.confirm"))
        confirm_btn.clicked.connect(self.confirm_requested.emit)
        actions.addWidget(confirm_btn)
        actions.addStretch(1)
        layout.addLayout(actions)

        close_btn = QDialogButtonBox(QDialogButtonBox.StandardButton.Close)
        close_btn.rejected.connect(self.reject)
        layout.addWidget(close_btn)
