from __future__ import annotations

import os
import tempfile
import time
import unittest
from pathlib import Path
from unittest.mock import Mock, patch

os.environ.setdefault("QT_QPA_PLATFORM", "offscreen")

from PyQt6.QtCore import Qt
from PyQt6.QtWidgets import QApplication, QSystemTrayIcon

from sync_client import rsync_ops
from sync_client.gui.browse_tab import BrowseTab
from sync_client.gui.log_tab import ACTION_FILTERS, LogTab
from sync_client.gui.tray import TrayIcon
from sync_client.i18n import set_language
from sync_client.logger import Event


class _Config:
    def __init__(self, local_root: str = "", tray_click: str = "menu") -> None:
        self._local_root = local_root
        self.tray_click = tray_click

    def local_root(self) -> str:
        return self._local_root

    def get(self, key, default=None):
        if key == "tray_single_click":
            return self.tray_click
        return default


class _EventLogger:
    def __init__(self, events: list[Event] | None = None) -> None:
        self.events = events or []

    def tail(self, limit=500, action_filter=None):
        events = self.events
        if action_filter:
            events = [event for event in events if event.action == action_filter]
        return events[-limit:]

    def log(self, *_args, **_kwargs) -> None:
        pass


class UsabilityTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls) -> None:
        cls.app = QApplication.instance() or QApplication([])

    def setUp(self) -> None:
        set_language("en")

    def tearDown(self) -> None:
        set_language("it")

    def test_log_filters_cover_produced_actions_and_keep_raw_codes(self) -> None:
        expected = {
            "UPLOAD", "DOWNLOAD", "DELETE_LOCAL", "DELETE_REMOTE", "PRUNE_LOCAL_TRASH",
            "PRUNE_REMOTE_TRIGGER", "CANCELLED", "PULL_DEFERRED", "LOCK_DEFERRED", "SERVER_DOWN",
            "SERVER_RESTARTED", "SERVER_OUTDATED", "SERVER_UPDATE_AVAILABLE", "CONFLICT",
            "ERROR", "BROWSE_DOWNLOAD", "BROWSE_RENAME", "BROWSE_DELETE",
            "RESTORE_REMOTE_VERSION", "JOURNAL_BLOCK", "JOURNAL_ERROR", "SAFETY_BLOCK",
            "STALE_DELETE", "UNSUPPORTED",
        }
        self.assertEqual({action for action, _category in ACTION_FILTERS}, expected)

        event = Event(time.time(), "STALE_DELETE", "folder/file.txt", "newer remote version")
        tab = LogTab(_EventLogger([event]), cfg=_Config())
        action_item = tab.table.item(0, 1)
        self.assertNotEqual(action_item.text(), "STALE_DELETE")
        self.assertEqual(action_item.data(Qt.ItemDataRole.UserRole), "STALE_DELETE")
        self.assertIn("STALE_DELETE", action_item.toolTip())
        filter_index = tab.filter_combo.findData("STALE_DELETE")
        self.assertGreaterEqual(filter_index, 0)
        self.assertNotEqual(tab.filter_combo.itemText(filter_index), "STALE_DELETE")
        tab.deleteLater()

    def test_log_only_resolves_real_synced_relative_paths(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / "folder").mkdir()
            events = [
                Event(time.time(), "UPLOAD", "folder/file.txt"),
                Event(time.time(), "BROWSE_DELETE", "remote/only.txt"),
                Event(time.time(), "ERROR", "-"),
                Event(time.time(), "UPLOAD", "../outside.txt"),
            ]
            tab = LogTab(_EventLogger(events), cfg=_Config(directory))

            self.assertIsNone(tab._local_folder_for_row(0))
            self.assertIsNone(tab._local_folder_for_row(1))
            self.assertIsNone(tab._local_folder_for_row(2))
            self.assertEqual(tab._local_folder_for_row(3), root / "folder")
            with patch("sync_client.gui.log_tab.QDesktopServices.openUrl") as open_url:
                tab._open_row_folder(3)
            open_url.assert_called_once()
            tab.deleteLater()

    def test_browse_search_preserves_visible_selection_and_breadcrumb_navigates(self) -> None:
        engine = Mock()
        engine.connection = None
        tab = BrowseTab(_Config(), engine, _EventLogger(), Mock())
        tab._entries = [
            rsync_ops.BrowseEntry("Documents", "DIR", 0, 0),
            rsync_ops.BrowseEntry("photo.jpg", "FILE", 10, 0),
            rsync_ops.BrowseEntry("notes.txt", "FILE", 5, 0),
        ]
        tab._populate()
        tab.tree.topLevelItem(1).setSelected(True)
        selected_name = tab._selected_entries()[0].name
        tab.search_edit.setText("notes")

        self.assertEqual(tab.tree.topLevelItemCount(), 1)
        self.assertEqual(tab._selected_entries()[0].name, selected_name)
        self.assertIn("1 of 3", tab.status_label.text())

        tab._path = "projects/current"
        tab._update_breadcrumbs()
        with patch.object(tab, "refresh") as refresh:
            tab._breadcrumb_buttons[1].click()
        self.assertEqual(tab._path, "projects")
        refresh.assert_called_once_with()
        tab.deleteLater()

    def test_tray_single_click_preference_does_not_change_context_or_double_click(self) -> None:
        fake = type("FakeTray", (), {})()
        fake.cfg = _Config(tray_click="window")
        fake._menu = Mock()
        fake._show_window = Mock()

        TrayIcon._on_activated(fake, QSystemTrayIcon.ActivationReason.Trigger)
        fake._show_window.assert_called_once_with()
        fake._menu.popup.assert_not_called()

        fake._show_window.reset_mock()
        TrayIcon._on_activated(fake, QSystemTrayIcon.ActivationReason.Context)
        fake._menu.popup.assert_called_once()
        fake._show_window.assert_not_called()

        TrayIcon._on_activated(fake, QSystemTrayIcon.ActivationReason.DoubleClick)
        fake._show_window.assert_called_once_with()


if __name__ == "__main__":
    unittest.main()
