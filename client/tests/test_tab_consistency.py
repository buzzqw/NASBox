"""Uniform tab chrome: no horizontal jump when switching scrollable tabs."""
from __future__ import annotations

from PyQt6.QtCore import Qt
from PyQt6.QtWidgets import QScrollArea, QWidget

from sync_client.gui.main_window import uniform_scrollbars


def test_uniform_scrollbars_reserve_the_gutter(qtbot):
    host = QWidget()
    qtbot.addWidget(host)
    areas = [QScrollArea(host), QScrollArea(host)]
    uniform_scrollbars(host)
    for area in areas:
        assert area.verticalScrollBarPolicy() == Qt.ScrollBarPolicy.ScrollBarAlwaysOn
        assert area.horizontalScrollBarPolicy() == Qt.ScrollBarPolicy.ScrollBarAlwaysOff
