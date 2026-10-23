from __future__ import annotations

from pathlib import Path
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


def test_conflicts_tab_shows_metadata_and_diff(qtbot, tmp_path):
    with ClientEnvironment():
        cfg = Config()
        root = tmp_path / "nasbox"
        root.mkdir()
        cfg.set_local_root(str(root))
        (root / "doc.txt").write_text("one\ntwo\n", encoding="utf-8")
        (root / "doc (conflitto da dev1 abc123).txt").write_text(
            "one\nTWO\n", encoding="utf-8",
        )
        engine = MagicMock()
        tab = ConflictsTab(cfg, engine)
        qtbot.addWidget(tab)
        qtbot.waitUntil(lambda: tab.tree.topLevelItemCount() == 1, timeout=5000)
        item = tab.tree.topLevelItem(0)
        assert item.text(0) == "doc.txt"
        assert item.text(1) == "1"
        assert tab.candidate_combo.count() == 2
        assert "---" in tab.preview.toPlainText()
