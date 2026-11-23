"""UX guardrails for the 'Esplora NAS' tab.

The context menu is the main discoverability surface for the newer settings
(selective sync, sync mode, mirrors); the empty state and the drag payload are
the two things that silently regress when the tree is refactored.
"""
from __future__ import annotations

from unittest.mock import MagicMock

import pytest
from PyQt6.QtCore import QPoint
from PyQt6.QtWidgets import QApplication, QMenu, QMessageBox

from sync_client.config import Config
from sync_client.gui.browse_tab import BrowseTab, SYNC_MODE_MENU_KEYS
from sync_client.i18n import t
from sync_client.rsync_ops import BrowseEntry
from tests.support import ClientEnvironment


@pytest.fixture
def browse(qapp, qtbot, tmp_path):
    with ClientEnvironment():
        cfg = Config()
        root = tmp_path / "nasbox"
        root.mkdir()
        cfg.set_local_root(str(root))
        engine = MagicMock()
        logger = MagicMock()
        sync_state = MagicMock()
        tab = BrowseTab(cfg, engine, logger, sync_state)
        qtbot.addWidget(tab)
        yield tab, cfg, root, engine, logger, sync_state


def _seed(tab: BrowseTab, *entries: BrowseEntry) -> None:
    tab._entries = list(entries)
    tab._populate()
    tab.tree.topLevelItem(0).setSelected(True)


def _open_menu(tab: BrowseTab, item, monkeypatch) -> QMenu:
    monkeypatch.setattr(tab.tree, "itemAt", lambda _pos: item)
    menus: list[QMenu] = []
    monkeypatch.setattr(QMenu, "exec", lambda self, *_args: menus.append(self))
    tab._show_context_menu(QPoint(1, 1))
    assert menus, "context menu was not displayed"
    return menus[0]


def _action(menu: QMenu, key: str):
    return next(action for action in menu.actions() if action.text() == t(key))


def _seed_and_get(tab: BrowseTab, entry: BrowseEntry):
    _seed(tab, entry)
    return tab.tree.topLevelItem(0)


def test_context_menu_exposes_all_ux_actions(browse, monkeypatch):
    tab, _cfg, _root, _engine, _logger, _sync_state = browse
    item = _seed_and_get(tab, BrowseEntry("Cartella", "DIR", 0, 0))
    menu = _open_menu(tab, item, monkeypatch)

    texts = [action.text() for action in menu.actions() if not action.isSeparator()]
    for key in (
        "browse.menu_copy_remote_path",
        "browse.menu_copy_local_path",
        "browse.menu_open_local_folder",
        "browse.menu_exclude_folder",
        "browse.menu_include_folder",
        "browse.menu_create_mirror",
        "browse.download_btn",
        "browse.rename_btn",
        "browse.delete_btn",
        *SYNC_MODE_MENU_KEYS.values(),
    ):
        assert t(key) in texts, key

    # Folder-only actions must not be offered for a stray selected file.
    file_item = _seed_and_get(tab, BrowseEntry("file.txt", "FILE", 3, 0))
    file_menu = _open_menu(tab, file_item, monkeypatch)
    for key in ("browse.menu_exclude_folder", "browse.menu_create_mirror"):
        assert not _action(file_menu, key).isEnabled(), key


def test_copy_remote_path_writes_relative_nas_path_to_clipboard(browse, monkeypatch):
    tab, _cfg, _root, _engine, _logger, _sync_state = browse
    tab._path = "lavoro"
    item = _seed_and_get(tab, BrowseEntry("file.txt", "FILE", 12, 0))
    menu = _open_menu(tab, item, monkeypatch)
    clipboard = MagicMock()
    monkeypatch.setattr(QApplication, "clipboard", lambda: clipboard)

    _action(menu, "browse.menu_copy_remote_path").trigger()

    clipboard.setText.assert_called_once_with("lavoro/file.txt")
    assert tab.status_label.text() == t("browse.path_copied", path="lavoro/file.txt")


def test_copy_local_path_writes_absolute_path_to_clipboard(browse, monkeypatch):
    tab, _cfg, root, _engine, _logger, _sync_state = browse
    item = _seed_and_get(tab, BrowseEntry("file.txt", "FILE", 12, 0))
    menu = _open_menu(tab, item, monkeypatch)
    clipboard = MagicMock()
    monkeypatch.setattr(QApplication, "clipboard", lambda: clipboard)

    _action(menu, "browse.menu_copy_local_path").trigger()

    clipboard.setText.assert_called_once_with(str(root / "file.txt"))


def test_empty_state_shows_hint_instead_of_blank_tree(browse):
    tab, _cfg, _root, _engine, _logger, _sync_state = browse
    tab._populate()

    assert tab.tree.topLevelItemCount() == 0
    assert tab.tree.isHidden()
    assert not tab.empty_label.isHidden()
    assert tab.empty_label.text() == t("browse.empty_hint")

    tab._entries = [BrowseEntry("file.txt", "FILE", 1, 0)]
    tab._populate()

    assert not tab.tree.isHidden()
    assert tab.empty_label.isHidden()
    assert tab.tree.topLevelItem(0).text(0) == "file.txt"


def test_empty_state_distinguishes_a_search_with_no_matches(browse):
    tab, _cfg, _root, _engine, _logger, _sync_state = browse
    tab._entries = [BrowseEntry("file.txt", "FILE", 1, 0)]
    tab.search_edit.setText("nessuna-corrispondenza")

    assert tab.tree.topLevelItemCount() == 0
    assert tab.empty_label.text() == t("browse.empty_filtered_hint")


def test_exclude_and_include_folder_update_selective_sync(browse, monkeypatch):
    tab, cfg, _root, engine, logger, _sync_state = browse
    tab._path = "lavoro"
    item = _seed_and_get(tab, BrowseEntry("archivio", "DIR", 0, 0))
    menu = _open_menu(tab, item, monkeypatch)

    _action(menu, "browse.menu_exclude_folder").trigger()
    assert cfg.selective_sync_excludes() == ["lavoro/archivio"]
    # Re-opening rebuilds the menu, so the include action is fetched again.
    menu = _open_menu(tab, tab.tree.topLevelItem(0), monkeypatch)
    _action(menu, "browse.menu_include_folder").trigger()
    assert cfg.selective_sync_excludes() == []
    engine.wake.assert_called()
    assert logger.log.call_count == 2


def test_sync_mode_actions_update_config(browse, monkeypatch):
    tab, cfg, _root, _engine, _logger, _sync_state = browse
    item = _seed_and_get(tab, BrowseEntry("Cartella", "DIR", 0, 0))
    for mode, key in SYNC_MODE_MENU_KEYS.items():
        menu = _open_menu(tab, item, monkeypatch)
        _action(menu, key).trigger()
        assert cfg.sync_mode() == mode


def test_drag_payload_carries_local_path_as_text_and_uri(browse):
    tab, _cfg, root, _engine, _logger, _sync_state = browse
    (root / "file.txt").write_text("contenuto", encoding="utf-8")
    item = _seed_and_get(tab, BrowseEntry("file.txt", "FILE", 9, 0))

    mime = tab._drag_mime_data()

    assert mime is not None
    assert mime.text() == str(root / "file.txt")
    assert [url.toLocalFile() for url in mime.urls()] == [str(root / "file.txt")]


def test_dropped_file_is_staged_locally_and_queued_for_upload(browse, monkeypatch, tmp_path):
    tab, cfg, root, engine, logger, sync_state = browse
    source = tmp_path / "from-desktop.txt"
    source.write_text("nuovo", encoding="utf-8")
    questions: list[str] = []
    monkeypatch.setattr(
        QMessageBox, "question",
        lambda self, title, body, *args: questions.append(body) or QMessageBox.StandardButton.Yes,
    )
    monkeypatch.setattr(QMessageBox, "information", lambda *args: None)

    tab._upload_dropped_urls([str(source)])

    assert (root / "from-desktop.txt").read_text(encoding="utf-8") == "nuovo"
    sync_state.mark_pending.assert_called_once_with(["from-desktop.txt"])
    engine.wake.assert_called_once()
    assert logger.log.called
    # No collision: the confirmation is still shown but is not an overwrite one.
    assert questions == [
        t("browse.upload_confirm_body", names="from-desktop.txt", path="/")
    ]


def test_dropped_file_asks_for_overwrite_confirmation(browse, monkeypatch, tmp_path):
    tab, _cfg, root, _engine, _logger, sync_state = browse
    (root / "existing.txt").write_text("vecchio", encoding="utf-8")
    source = tmp_path / "existing.txt"
    source.write_text("nuovo", encoding="utf-8")
    questions = []
    monkeypatch.setattr(
        QMessageBox, "question",
        lambda self, title, body, *args: questions.append(body) or QMessageBox.StandardButton.No,
    )

    tab._upload_dropped_urls([str(source)])

    assert questions == [
        t("browse.upload_overwrite_body", names="existing.txt", path="/")
    ]
    assert (root / "existing.txt").read_text(encoding="utf-8") == "vecchio"
    sync_state.mark_pending.assert_not_called()
