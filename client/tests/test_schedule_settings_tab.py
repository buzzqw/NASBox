from __future__ import annotations

from unittest.mock import MagicMock, patch

import pytest

from sync_client.config import Config
from sync_client.gui.dialogs import ScheduleWindowDialog
from sync_client.gui.settings_tab import SettingsTab
from tests.support import ClientEnvironment


@pytest.fixture
def settings_tab(tmp_path, qapp):
    with ClientEnvironment():
        cfg = Config()
        tab = SettingsTab(cfg, MagicMock())
        yield tab, cfg


def test_schedule_list_renders_windows(settings_tab):
    tab, _cfg = settings_tab
    tab._schedule_windows = [
        {"days": [0, 1, 2, 3, 4], "start": "08:00", "end": "18:00"},
        {"days": [5, 6], "start": "22:00", "end": "07:00"},
    ]
    tab._refresh_schedule_list()
    assert tab.schedule_list.count() == 2
    assert "08:00-18:00" in tab.schedule_list.item(0).text()
    assert "22:00-07:00" in tab.schedule_list.item(1).text()


def test_save_persists_enable_flag_and_windows(settings_tab):
    tab, cfg = settings_tab
    tab._schedule_windows = [{"days": [6], "start": "22:00", "end": "07:00"}]
    tab.schedule_enabled.setChecked(True)
    tab._save_settings()
    assert cfg.get("pause_schedule_enabled") is True
    assert cfg.pause_schedule() == [{"days": [6], "start": "22:00", "end": "07:00"}]


def test_remove_window_updates_the_list(settings_tab):
    tab, _cfg = settings_tab
    tab._schedule_windows = [{"days": [0], "start": "01:00", "end": "02:00"}]
    tab._refresh_schedule_list()
    tab.schedule_list.setCurrentRow(0)
    tab._remove_schedule_window()
    assert tab.schedule_list.count() == 0
    assert tab._schedule_windows == []


def test_schedule_dialog_defaults_and_rejects_empty_days(qapp):
    dialog = ScheduleWindowDialog()
    # Mon-Fri preselected by default.
    assert dialog._day_checks[0].isChecked()
    assert not dialog._day_checks[5].isChecked()
    # Unchecking every day must not produce a window (and must not block on a
    # modal warning -- it is patched away here).
    for checkbox in dialog._day_checks:
        checkbox.setChecked(False)
    with patch("sync_client.gui.dialogs.QMessageBox.warning") as warning:
        dialog._on_accept()
    warning.assert_called_once()
    assert dialog.window() is None
