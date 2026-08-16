from __future__ import annotations

import os
import time
import unittest
from unittest.mock import Mock, patch

os.environ.setdefault("QT_QPA_PLATFORM", "offscreen")

from PyQt6.QtWidgets import QApplication, QPushButton

from sync_client.gui import status_tab as status_tab_module
from sync_client.gui.status_tab import StatusTab
from sync_client.i18n import set_language
from sync_client.logger import Event
from sync_client.rsync_ops import TransferItem


class _Config:
    def __init__(self, **overrides) -> None:
        self.values = {
            "local_root": "/tmp/NASBox",
            "nas_lan": "nas.local",
            "nas_wan": "",
            "nas_user": "sync",
            "ssh_host_key_pinned": True,
            "repository_id": "repository-123",
            "remote_repository_ready": True,
            "remote_journal_ready": True,
            "journal_error": "",
            "paused": False,
        }
        self.values.update(overrides)

    def get(self, key, default=None):
        return self.values.get(key, default)

    def local_root(self) -> str:
        return self.values["local_root"]

    def is_configured(self) -> bool:
        return bool(self.local_root())

    def is_paused(self) -> bool:
        return bool(self.values["paused"])

    def pause_remaining_seconds(self):
        return None


class _Logger:
    def __init__(self, events=None) -> None:
        self.events = events or []

    def tail(self, limit=500):
        return self.events[-limit:]


class StatusTabTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls) -> None:
        cls.app = QApplication.instance() or QApplication([])

    def setUp(self) -> None:
        set_language("en")
        self.engine = Mock()
        self.engine.connection = object()
        self.scan_worker = Mock()

    def tearDown(self) -> None:
        set_language("it")

    def _tab(self, cfg=None, logger=None) -> StatusTab:
        return StatusTab(
            cfg or _Config(), self.engine, self.scan_worker,
            logger=logger or _Logger(),
        )

    def test_attention_waits_for_known_status_then_reports_unreachable_nas(self) -> None:
        tab = self._tab()
        self.assertEqual(tab.attention_problems, [])

        tab.on_status_changed({
            "configured": True, "connected": False, "paused": False,
            "host": None, "via_jump": False,
        })

        self.assertEqual([problem[0] for problem in tab.attention_problems], ["unreachable"])
        self.assertFalse(tab.attention_box.isHidden())
        tab.deleteLater()

    def test_incomplete_configuration_is_not_misreported_as_unreachable(self) -> None:
        tab = self._tab(_Config(local_root="", nas_lan="", nas_user=""))
        tab.on_status_changed({
            "configured": False, "connected": False, "paused": False,
            "host": None, "via_jump": False,
        })

        ids = [problem[0] for problem in tab.attention_problems]
        self.assertEqual(ids, ["configuration"])
        self.assertIn("Setup is incomplete", tab.attention_problems[0][1])
        tab.deleteLater()

    def test_connected_state_loads_diagnostics_automatically_once(self) -> None:
        tab = self._tab()
        status = {
            "configured": True, "connected": True, "paused": False,
            "host": "nas.local", "via_jump": False,
        }
        with patch.object(status_tab_module, "run_in_background") as background:
            tab.on_status_changed(status)
            tab.on_status_changed(status)

        background.assert_called_once()
        self.assertIn("Connected to NAS", tab.status_label.text())
        self.assertIn("no transfer in progress", tab.sync_state_label.text())
        tab.deleteLater()

    def test_transfer_lifecycle_separates_current_state_and_last_success(self) -> None:
        tab = self._tab()
        tab.on_status_changed({
            "configured": True, "connected": True, "paused": False,
            "host": "nas.local", "via_jump": False,
        })
        tab.on_queue_updated([TransferItem("upload", "report.txt")])
        tab.on_transfer_preparing("upload")
        self.assertIn("Preparing upload", tab.sync_state_label.text())
        tab.on_transfer_item_started("upload", "report.txt")
        self.assertIn("report.txt", tab.sync_state_label.text())
        tab.on_transfer_item_done("upload", "report.txt")

        tab.on_transfer_finished("upload", True)

        self.assertIn("Last successful synchronization", tab.last_sync_label.text())
        self.assertIn("upload to NAS", tab.last_sync_label.text())
        self.assertIn("no transfer in progress", tab.sync_state_label.text())
        self.assertIn("no operations waiting", tab.queue_label.text())
        tab.deleteLater()

    def test_remote_safety_warnings_wait_for_stable_connected_state(self) -> None:
        cfg = _Config(
            repository_id="", remote_repository_ready=False,
            remote_journal_ready=False,
        )
        tab = self._tab(cfg)
        status = {
            "configured": True, "connected": True, "paused": False,
            "host": "nas.local", "via_jump": False,
        }
        with patch.object(status_tab_module, "run_in_background"):
            tab.on_status_changed(status)
            self.assertNotIn("repository", [p[0] for p in tab.attention_problems])
            tab.on_status_changed(status)

        ids = [problem[0] for problem in tab.attention_problems]
        self.assertIn("repository", ids)
        self.assertNotIn("journal_ready", ids)
        tab.deleteLater()

    def test_pending_queue_and_unavailable_lock_remain_truthful(self) -> None:
        tab = self._tab()
        with patch.object(status_tab_module, "run_in_background"):
            tab.on_status_changed({
                "configured": True, "connected": True, "paused": False,
                "host": "nas.local", "via_jump": False,
            })
        tab.on_queue_updated([TransferItem("upload", "waiting.txt")])
        self.assertIn("1 queued", tab.sync_state_label.text())

        tab.on_transfer_waiting_for_lock("upload")
        self.assertIn("waiting for the NAS", tab.sync_state_label.text())
        tab.on_transfer_lock_unavailable("upload", "busy")
        self.assertIn("1 queued", tab.sync_state_label.text())
        tab.deleteLater()

    def test_recent_safety_event_and_low_space_are_actionable(self) -> None:
        event = Event(time.time(), "SAFETY_BLOCK", "-", "repository identity changed")
        tab = self._tab(logger=_Logger([event]))
        tab.on_status_changed({
            "configured": True, "connected": True, "paused": False,
            "host": "nas.local", "via_jump": False,
        })
        tab._on_diagnostics_done((True, {
            "SHARE_AVAILABLE_BYTES": str(1024 * 1024),
            "TRASH_DISK_BYTES": "0",
            "TRASH_FILE_COUNT": "0",
        }, ""), None)

        ids = [problem[0] for problem in tab.attention_problems]
        self.assertIn("event_safety_block", ids)
        self.assertIn("low_space", ids)
        actions = []
        tab.attention_action_requested.connect(actions.append)
        row = tab.findChild(status_tab_module.QWidget, "attention_low_space")
        self.assertIsNotNone(row)
        row.findChild(QPushButton).click()
        self.assertEqual(actions, ["history"])
        tab.deleteLater()


if __name__ == "__main__":
    unittest.main()
