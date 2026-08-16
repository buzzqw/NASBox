from __future__ import annotations

import os
import unittest
from unittest.mock import Mock, patch

os.environ.setdefault("QT_QPA_PLATFORM", "offscreen")

from PyQt6.QtWidgets import QApplication, QBoxLayout

from sync_client.gui.dialogs import FirstRunSetupWizard, _test_connection_worker
from sync_client.gui.settings_tab import SettingsTab
from sync_client.i18n import set_language
from sync_client.rsync_ops import NasConnection


class _Config:
    def __init__(self) -> None:
        self.values = {
            "nas_lan": "nas.local",
            "nas_wan": "",
            "nas_user": "sync",
            "ssh_port": 22,
            "jump_host": "",
            "jump_port": 22,
            "jump_user": "",
            "remote_server_script": "",
            "remote_prefix": "/volume1/NASBox",
            "delete_enabled": False,
            "max_delete_files": 1000,
            "bandwidth_upload_kbps": 0,
            "bandwidth_download_kbps": 0,
            "poll_interval": 60,
            "notify_sync_completion": False,
            "animate_sync_icon": False,
            "tray_single_click": "menu",
            "language": "en",
            "exclude_patterns": ["*.tmp"],
            "retention_days_remote": 30,
        }
        self.save_count = 0

    def get(self, key, default=None):
        return self.values.get(key, default)

    def set(self, key, value, persist=True) -> None:
        self.values[key] = value
        if persist:
            self.save()

    def save(self) -> None:
        self.save_count += 1

    def local_root(self) -> str:
        return ""

    def exclude_patterns(self) -> list[str]:
        return list(self.values["exclude_patterns"])

    def language(self) -> str:
        return self.values["language"]


class ConfigurationUiTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls) -> None:
        cls.app = QApplication.instance() or QApplication([])

    def setUp(self) -> None:
        set_language("en")

    def tearDown(self) -> None:
        set_language("it")

    def test_settings_stage_every_category_until_one_save(self) -> None:
        cfg = _Config()
        engine = Mock()
        tab = SettingsTab(cfg, engine)

        tab.nas_lan.setText("new-nas.local")
        tab.bw_upload_spin.setValue(512)
        tab.poll_spin.setValue(120)
        tab.notify_sync_checkbox.setChecked(True)
        tab.tray_click_combo.setCurrentIndex(tab.tray_click_combo.findData("window"))
        tab.delete_enabled.setChecked(True)
        tab.exclude_input.setText("node_modules/")
        tab._add_exclude()

        self.assertEqual(cfg.get("nas_lan"), "nas.local")
        self.assertEqual(cfg.exclude_patterns(), ["*.tmp"])
        self.assertTrue(tab.save_btn.isEnabled())
        self.assertIn("Unsaved", tab.save_feedback.text())

        tab._save_settings()

        self.assertEqual(cfg.get("nas_lan"), "new-nas.local")
        self.assertEqual(cfg.get("bandwidth_upload_kbps"), 512)
        self.assertEqual(cfg.get("poll_interval"), 120)
        self.assertTrue(cfg.get("notify_sync_completion"))
        self.assertEqual(cfg.get("tray_single_click"), "window")
        self.assertTrue(cfg.get("delete_enabled"))
        self.assertEqual(cfg.exclude_patterns(), ["*.tmp", "node_modules/"])
        self.assertEqual(cfg.save_count, 1)
        self.assertFalse(tab.save_btn.isEnabled())
        engine.wake.assert_called_once_with()
        tab.deleteLater()

    def test_settings_switch_to_one_column_at_narrow_width(self) -> None:
        tab = SettingsTab(_Config(), Mock())
        tab._update_column_layout(800)
        self.assertTrue(tab._single_column)
        self.assertEqual(tab._columns.direction(), QBoxLayout.Direction.TopToBottom)

        tab._update_column_layout(1100)
        self.assertFalse(tab._single_column)
        self.assertEqual(tab._columns.direction(), QBoxLayout.Direction.LeftToRight)
        tab.deleteLater()

    def test_detect_saves_all_current_fields_before_starting_worker(self) -> None:
        cfg = _Config()
        tab = SettingsTab(cfg, Mock())
        tab.nas_lan.setText("typed-nas.local")
        tab.poll_spin.setValue(180)

        with patch("sync_client.gui.settings_tab.run_in_background") as background:
            tab._detect_from_nas()

        self.assertEqual(cfg.get("nas_lan"), "typed-nas.local")
        self.assertEqual(cfg.get("poll_interval"), 180)
        self.assertEqual(cfg.save_count, 1)
        background.assert_called_once()
        tab.deleteLater()

    def test_wizard_stages_values_and_applies_detected_server_fields(self) -> None:
        cfg = _Config()
        wizard = FirstRunSetupWizard(cfg)
        wizard.connection_page.host.setText("new-nas.local")
        wizard.connection_page.user.setText("alice")
        wizard.connection_page.port.setValue(2222)
        wizard.server_page._on_detect_done({
            "connected": True,
            "fetch_ok": True,
            "script_path": "/opt/nasbox/server.sh",
            "values": {
                "SHARE_ROOT": "/srv/NASBox",
                "REPOSITORY_ID": "repo-1",
                "REPOSITORY_READY": "true",
                "JOURNAL_READY": "true",
                "SYNC_LOCK_FILE": "/run/nasbox.lock",
            },
        }, None)

        values = wizard.setup_values()
        self.assertEqual(cfg.get("nas_lan"), "nas.local")
        self.assertEqual(cfg.save_count, 0)
        self.assertEqual(values["nas_lan"], "new-nas.local")
        self.assertEqual(values["ssh_port"], 2222)
        self.assertEqual(values["remote_prefix"], "/srv/NASBox")
        self.assertEqual(values["repository_id"], "repo-1")
        self.assertTrue(values["remote_journal_ready"])
        wizard.reject()
        self.assertEqual(cfg.get("nas_lan"), "nas.local")
        wizard.deleteLater()

    def test_wizard_ssh_test_uses_resolved_connection_and_shell(self) -> None:
        cfg = _Config()
        with patch("sync_client.gui.dialogs.rsync_ops.resolve_connection", return_value=NasConnection("nas.local")), \
             patch("sync_client.gui.dialogs.rsync_ops.run_remote_script", return_value=(True, "", "")) as run:
            result = _test_connection_worker(cfg)

        self.assertTrue(result["ok"])
        run.assert_called_once_with(cfg, NasConnection("nas.local"), "true", [], timeout=20)


if __name__ == "__main__":
    unittest.main()
