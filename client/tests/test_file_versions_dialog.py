from __future__ import annotations

import time
from unittest.mock import MagicMock, patch

import pytest

from sync_client import conflicts, paths, trash
from sync_client.config import Config
from sync_client.gui.conflicts_tab import ConflictsTab
from sync_client.gui.dialogs import FileVersionDialog
from tests.support import ClientEnvironment


def _make_local_version(relative_path: str, content: str) -> None:
    target = paths.local_trash_dir() / relative_path
    target.parent.mkdir(parents=True, exist_ok=True)
    versioned = target.with_name(f"{target.name}-{time.strftime('%Y-%m-%d--%H-%M-%S', time.gmtime())}-000001Z")
    versioned.write_text(content)


def test_dialog_lists_local_versions_and_enables_restore(qtbot, tmp_path):
    with ClientEnvironment():
        cfg = Config()
        root = tmp_path / "nasbox"
        root.mkdir()
        cfg.set_local_root(str(root))
        (root / "doc.txt").write_text("current")
        _make_local_version("doc.txt", "old content")

        engine = MagicMock()
        engine.connection = None
        dialog = FileVersionDialog(None, cfg, engine, "doc.txt", str(root))

        qtbot.waitUntil(lambda: dialog.table.topLevelItemCount() == 1, timeout=3000)
        assert dialog.restore_btn.isEnabled() is False  # nothing selected yet
        dialog.table.setCurrentItem(dialog.table.topLevelItem(0))
        assert dialog.restore_btn.isEnabled()


def test_dialog_restore_local_overwrites_the_original(tmp_path):
    with ClientEnvironment():
        cfg = Config()
        root = tmp_path / "nasbox"
        root.mkdir()
        cfg.set_local_root(str(root))
        (root / "doc.txt").write_text("current")
        _make_local_version("doc.txt", "old content")
        version = next(v for v in trash.list_local_versions() if v.relative_path == "doc.txt")

        engine = MagicMock()
        engine.connection = None
        dialog = FileVersionDialog(None, cfg, engine, "doc.txt", str(root))

        ok, detail = dialog._do_restore("local", version)

        assert ok, detail
        assert (root / "doc.txt").read_text() == "old content"


def test_conflict_tab_resolve_after_restore_moves_candidates(tmp_path):
    with ClientEnvironment():
        cfg = Config()
        root = tmp_path / "nasbox"
        root.mkdir()
        cfg.set_local_root(str(root))
        (root / "doc.txt").write_text("original")
        (root / "doc (conflitto da dev1 deadbeef).txt").write_text("copy")
        groups = conflicts.scan_conflict_groups(str(root))
        assert len(groups) == 1

        engine = MagicMock()
        engine.sync_state = None  # mark_pending then raises and is swallowed
        tab = ConflictsTab(cfg, engine)
        tab._resolve_after_restore(groups[0], str(root), "doc.txt")

        assert (root / "doc.txt").exists()
        assert not (root / "doc (conflitto da dev1 deadbeef).txt").exists()


def test_settings_unaffected_smoke(qtbot, tmp_path):
    # The dialog must not require a connection to open (local history is enough).
    with ClientEnvironment():
        cfg = Config()
        root = tmp_path / "nasbox"
        root.mkdir()
        cfg.set_local_root(str(root))
        engine = MagicMock()
        engine.connection = None
        dialog = FileVersionDialog(None, cfg, engine, "missing.txt", str(root))
        qtbot.waitUntil(lambda: "Nessuna versione" in dialog.info.text() or "No historical" in dialog.info.text(), timeout=3000)
