from __future__ import annotations

from unittest.mock import MagicMock, patch
import pytest
from PyQt6.QtWidgets import QMainWindow, QTabWidget

from sync_client.config import Config
from sync_client.gui.metrics_tab import MetricsTab
from tests.support import ClientEnvironment


class MockWindow(QMainWindow):
    def __init__(self):
        super().__init__()
        self._minimized = False
        self._visible = True

    def isMinimized(self) -> bool:
        return self._minimized

    def isVisible(self) -> bool:
        return self._visible


@pytest.fixture
def metrics_setup(qapp):
    with ClientEnvironment():
        cfg = Config()
        engine = MagicMock()
        engine.connection = MagicMock()
        window = MockWindow()
        tab = MetricsTab(cfg, engine, parent=window)
        tab._connected = True
        return window, tab


def test_metrics_disabled_when_minimized(metrics_setup):
    window, tab = metrics_setup

    # When window is visible and not minimized, set_active(True) starts timer
    window._minimized = False
    window._visible = True
    tab.set_active(True)
    assert tab._active is True
    assert tab._timer.isActive()

    # When window becomes minimized, set_active(False) stops timer
    window._minimized = True
    tab.set_active(False)
    assert tab._active is False
    assert not tab._timer.isActive()

    # Attempting set_active(True) while minimized must not activate timer
    tab.set_active(True)
    assert not tab._timer.isActive()

    # Calling _refresh_metrics directly while minimized does nothing
    tab._collector.collect = MagicMock()
    tab._refresh_metrics()
    tab._collector.collect.assert_not_called()


def test_metrics_disabled_when_hidden(metrics_setup):
    window, tab = metrics_setup

    window._minimized = False
    window._visible = False
    tab.set_active(True)
    assert not tab._timer.isActive()

    tab._collector.collect = MagicMock()
    tab._refresh_metrics()
    tab._collector.collect.assert_not_called()


def test_main_window_update_metrics_tab_active_logic(qapp):
    """Verify that _update_metrics_tab_active correctly coordinates tab index and window state."""
    window = MockWindow()
    tabs = QTabWidget()
    window.tabs = tabs

    # Add dummy tabs: 0 = Status, 1 = Metrics
    tab0 = QMainWindow()
    tab1 = QMainWindow()
    tabs.addTab(tab0, "Status")
    tabs.addTab(tab1, "Metrics")

    metrics_tab = MagicMock()
    window.metrics_tab = metrics_tab

    from sync_client.gui.main_window import MainWindow
    update_fn = MainWindow._update_metrics_tab_active.__get__(window, MockWindow)

    # Tab 0 selected, window visible and not minimized -> False
    tabs.setCurrentIndex(0)
    window._visible = True
    window._minimized = False
    update_fn()
    metrics_tab.set_active.assert_called_with(False)

    # Tab 1 (Metrics) selected, window visible and not minimized -> True
    tabs.setCurrentIndex(1)
    update_fn()
    metrics_tab.set_active.assert_called_with(True)

    # Tab 1 selected, but window is minimized -> False
    window._minimized = True
    update_fn()
    metrics_tab.set_active.assert_called_with(False)

    # Window restored from minimized -> True
    window._minimized = False
    update_fn()
    metrics_tab.set_active.assert_called_with(True)

    # Window hidden to tray -> False
    window._visible = False
    update_fn()
    metrics_tab.set_active.assert_called_with(False)
