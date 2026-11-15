from __future__ import annotations

from pathlib import Path
from unittest.mock import MagicMock
import pytest

from PyQt6.QtCore import Qt
from PyQt6.QtWidgets import QHeaderView

from sync_client.config import Config
from sync_client.gui.conflicts_tab import ConflictsTab
from tests.support import ClientEnvironment


@pytest.fixture
def conflicts_tab(tmp_path, qapp):
    with ClientEnvironment():
        cfg = Config()
        engine = MagicMock()
        tab = ConflictsTab(cfg, engine)
        yield tab


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


def test_conflicts_tree_columns_are_resizable_and_sort_by_real_size(qtbot, tmp_path):
    with ClientEnvironment():
        cfg = Config()
        root = tmp_path / "nasbox"
        root.mkdir()
        cfg.set_local_root(str(root))
        (root / "a.txt").write_text("a", encoding="utf-8")
        (root / "a (conflitto da dev1 aaa).txt").write_text("x" * 5000, encoding="utf-8")
        (root / "b.txt").write_text("b", encoding="utf-8")
        (root / "b (conflitto da dev1 bbb).txt").write_text("y" * 10, encoding="utf-8")

        tab = ConflictsTab(cfg, MagicMock())
        qtbot.addWidget(tab)
        qtbot.waitUntil(lambda: tab.tree.topLevelItemCount() == 2, timeout=5000)

        header = tab.tree.header()
        for column in range(tab.tree.columnCount()):
            assert header.sectionResizeMode(column) == QHeaderView.ResizeMode.Interactive
        assert tab.tree.isSortingEnabled()

        # "9 KB" vs "5 KB": text order would be wrong for one of the two.
        tab.tree.sortItems(2, Qt.SortOrder.AscendingOrder)
        assert [tab.tree.topLevelItem(i).text(0) for i in range(2)] == ["b.txt", "a.txt"]
        tab.tree.sortItems(2, Qt.SortOrder.DescendingOrder)
        assert [tab.tree.topLevelItem(i).text(0) for i in range(2)] == ["a.txt", "b.txt"]


def test_action_progress_updates_bar_and_counter(conflicts_tab):
    tab = conflicts_tab
    assert tab.progress_row.isHidden()

    tab._set_action_busy(True, "Risoluzione in corso…")
    assert tab.progress_row.isVisibleTo(tab)
    assert tab.progress_bar.minimum() == 0
    assert tab.progress_bar.maximum() == 0  # indeterminate while waiting

    tab._on_action_progress({"phase": "resolving", "done": 2, "total": 5, "path": "a.txt"})
    assert (tab.progress_bar.minimum(), tab.progress_bar.maximum()) == (0, 5)
    assert tab.progress_bar.value() == 2
    assert "2" in tab.progress_label.text() and "5" in tab.progress_label.text()
    assert tab.progress_label.toolTip() == "a.txt"

    tab._on_action_progress({"phase": "cleaning", "done": 4, "total": 10, "path": "a (conflict).txt"})
    assert (tab.progress_bar.minimum(), tab.progress_bar.maximum()) == (0, 10)
    assert tab.progress_bar.value() == 4

    tab._on_action_progress({"phase": "done", "done": 5, "total": 5})
    assert tab.progress_bar.value() == 5

    tab._set_action_busy(False)
    assert tab.progress_row.isHidden()


def test_resolve_everything_respects_the_search_filter(qtbot, tmp_path):
    with ClientEnvironment():
        cfg = Config()
        root = tmp_path / "nasbox"
        root.mkdir()
        cfg.set_local_root(str(root))
        for name in ("alpha.txt", "beta.txt"):
            (root / name).write_text(name, encoding="utf-8")
            (root / f"{name[:-4]} (conflitto da dev1 {name[:3]}).txt").write_text(
                f"{name}-alt", encoding="utf-8",
            )

        tab = ConflictsTab(cfg, MagicMock())
        qtbot.addWidget(tab)
        qtbot.waitUntil(lambda: tab.tree.topLevelItemCount() == 2, timeout=5000)
        assert tab.resolve_everything_btn.isEnabled()
        assert len(tab._visible_groups()) == 2

        tab.search_edit.setText("beta")
        assert [group.original.name for group in tab._visible_groups()] == ["beta.txt"]
        assert tab.resolve_everything_btn.isEnabled()
