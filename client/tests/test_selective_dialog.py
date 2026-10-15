from __future__ import annotations

import tempfile
from pathlib import Path
from unittest.mock import MagicMock

import pytest
from PyQt6.QtCore import Qt

from sync_client.config import Config
from sync_client.gui.dialogs import SelectiveSyncDialog
from sync_client.gui.settings_tab import SettingsTab
from tests.support import ClientEnvironment


def _focus(dialog: SelectiveSyncDialog) -> dict:
    return {dialog.list.item(i).text(): dialog.list.item(i) for i in range(dialog.list.count())}


def test_dialog_lists_directories_and_minimizes_selection(qapp):
    with tempfile.TemporaryDirectory() as directory:
        root = Path(directory)
        (root / "a").mkdir()
        (root / "a" / "b").mkdir()
        (root / "c").mkdir()
        dialog = SelectiveSyncDialog(root=str(root), excluded=[])
        items = _focus(dialog)
        assert set(items) == {"a", "a/b", "c"}
        items["a"].setCheckState(Qt.CheckState.Unchecked)
        items["a/b"].setCheckState(Qt.CheckState.Unchecked)
        dialog._on_accept()
        # "a/b" is covered by its parent, so only "a" survives.
        assert dialog.excluded_paths() == ["a"]


def test_dialog_preselects_existing_exclusions(qapp):
    with tempfile.TemporaryDirectory() as directory:
        root = Path(directory)
        (root / "keep").mkdir()
        (root / "drop").mkdir()
        dialog = SelectiveSyncDialog(root=str(root), excluded=["drop"])
        items = _focus(dialog)
        assert items["drop"].checkState() == Qt.CheckState.Unchecked
        assert items["keep"].checkState() == Qt.CheckState.Checked


def test_settings_label_reflects_excluded_folders(qapp):
    with ClientEnvironment():
        cfg = Config()
        tab = SettingsTab(cfg, MagicMock())
        tab._selective_excludes = []
        tab._refresh_selective_label()
        assert "0" not in tab.selective_label.text() or "tutte" in tab.selective_label.text().lower()
        tab._selective_excludes = ["a", "b/c"]
        tab._refresh_selective_label()
        assert "2" in tab.selective_label.text()


def test_settings_persists_selective_excludes(qapp):
    with ClientEnvironment():
        cfg = Config()
        tab = SettingsTab(cfg, MagicMock())
        tab._selective_excludes = ["lavoro/archivio"]
        tab._save_settings()
        assert cfg.selective_sync_excludes() == ["lavoro/archivio"]
