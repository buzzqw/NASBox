from __future__ import annotations

from unittest.mock import MagicMock
import pytest

from sync_client.config import Config
from sync_client.gui.conflicts_tab import ConflictsTab
from tests.support import ClientEnvironment


@pytest.fixture
def conflicts_tab(tmp_path, qapp):
    with ClientEnvironment():
        cfg = Config()
        engine = MagicMock()
        tab = ConflictsTab(cfg, engine)
        return tab


def test_conflicts_tab_timer_and_log_event(conflicts_tab):
    # Verify auto-refresh timer interval is 30s
    assert conflicts_tab._auto_refresh_timer.interval() == 30000

    # Test timer is not active initially
    assert not conflicts_tab._auto_refresh_timer.isActive()

    # Triggering CONFLICT log event refreshes conflicts
    conflicts_tab.refresh = MagicMock()
    conflicts_tab._on_log_event("CONFLICT", "file.txt", "mismatch")
    conflicts_tab.refresh.assert_called_once()

    # Non-conflict log events should not trigger refresh
    conflicts_tab.refresh.reset_mock()
    conflicts_tab._on_log_event("UPLOAD", "file.txt", "done")
    conflicts_tab.refresh.assert_not_called()
