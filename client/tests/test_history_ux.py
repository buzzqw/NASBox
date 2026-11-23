"""UX improvements to the Storico/Cestino tab.

Grouping by day, batch restore (including "restore elsewhere"), the reclaim
panel and the empty state are covered here; the translation keys used by the
new widgets are injected into ``i18n.STRINGS`` so the assertions do not depend
on the language active at the start of the run.
"""
from __future__ import annotations

from datetime import date, timedelta
from pathlib import Path
from unittest.mock import MagicMock, patch

import pytest
from PyQt6.QtCore import Qt
from PyQt6.QtWidgets import QFileDialog, QMessageBox

from sync_client import i18n, trash
from sync_client.config import Config
from sync_client.gui import history_tab as history_module
from sync_client.gui.history_tab import HistoryTab, day_label
from sync_client.trash import RemoteTrashVersion, TrashVersion
from tests.support import ClientEnvironment

# Exact it/en text that must be added to sync_client/i18n.py (also reported to
# the parent session); injected here because i18n.py is out of scope for this
# change.
EXTRA_STRINGS = {
    "history.today_label": {"it": "Oggi", "en": "Today"},
    "history.yesterday_label": {"it": "Ieri", "en": "Yesterday"},
    "history.date_format": {"it": "%d/%m/%Y", "en": "%m/%d/%Y"},
    "history.day_group_label": {"it": "{label} - {count} elementi", "en": "{label} - {count} items"},
    "history.empty_state": {
        "it": "Lo storico è vuoto. I file sovrascritti o eliminati compariranno qui finché la retention li conserva.",
        "en": "History is empty. Overwritten or deleted files will appear here while retention keeps them.",
    },
    "history.empty_state_filtered": {
        "it": "Nessun elemento corrisponde alla ricerca. Prova con un altro termine o cancella il filtro.",
        "en": "No items match the search. Try a different term or clear the filter.",
    },
    "history.restore_elsewhere_btn": {
        "it": "Ripristina in un'altra cartella…", "en": "Restore to another folder…",
    },
    "history.restore_elsewhere_tooltip": {
        "it": "Ripristina le versioni selezionate in una cartella scelta, mantenendo la struttura delle sottocartelle.",
        "en": "Restore the selected versions into a chosen folder, preserving subfolder structure.",
    },
    "history.restore_elsewhere_picker_title": {
        "it": "Scegli la cartella di destinazione per il ripristino",
        "en": "Choose the destination folder for the restore",
    },
    "history.confirm_restore_elsewhere_body": {
        "it": "Ripristinare {count} versioni in '{destination}'? I file attuali (se presenti) verranno sovrascritti.",
        "en": "Restore {count} versions to '{destination}'? Current files (if any) will be overwritten.",
    },
    "history.reclaim_title": {
        "it": "Quanto spazio posso liberare?", "en": "How much space can I free?",
    },
    "history.reclaim_summary": {
        "it": "Spazio occupato dagli elementi mostrati: {size} in {count} versioni.",
        "en": "Space used by the items shown: {size} in {count} versions.",
    },
    "history.reclaim_clean_btn": {
        "it": "Pulisci vecchi di {days} giorni", "en": "Clean older than {days} days",
    },
    "history.reclaim_clean_tooltip": {
        "it": "Rimuove dallo storico le versioni più vecchie di {days} giorni, usando la retention configurata.",
        "en": "Removes versions older than {days} days from history, using the configured retention.",
    },
    "history.reclaim_confirm_local": {
        "it": "Rimuovere dallo storico locale le versioni più vecchie di {days} giorni? L'operazione non è reversibile.",
        "en": "Remove local history versions older than {days} days? This cannot be undone.",
    },
    "history.reclaim_confirm_remote": {
        "it": "Chiedere al NAS di rimuovere le proprie versioni storiche più vecchie della retention configurata? L'operazione non è reversibile.",
        "en": "Ask the NAS to remove its own history versions older than the configured retention? This cannot be undone.",
    },
    "history.confirm_restore_group_title": {
        "it": "Conferma ripristino gruppo", "en": "Confirm group restore",
    },
    "history.confirm_restore_group_body": {
        "it": "Ripristinare {count} file del gruppo '{label}' alla loro versione più recente?\n"
              "I file attuali (se presenti) verranno sovrascritti.",
        "en": "Restore {count} files from the '{label}' group to their most recent version?\n"
              "Current files (if any) will be overwritten.",
    },
}


@pytest.fixture(autouse=True)
def translated_history_strings(monkeypatch):
    for key, value in EXTRA_STRINGS.items():
        monkeypatch.setitem(i18n.STRINGS, key, value)
    i18n.set_language("en")
    yield
    i18n.set_language(i18n.DEFAULT_LANG)


@pytest.fixture(autouse=True)
def quiet_dialogs(monkeypatch):
    """Never open a real modal dialog in an offscreen test run."""
    monkeypatch.setattr(
        QMessageBox, "question",
        staticmethod(lambda *_args, **_kwargs: QMessageBox.StandardButton.Yes),
    )
    monkeypatch.setattr(
        QMessageBox, "information",
        staticmethod(lambda *_args, **_kwargs: QMessageBox.StandardButton.Ok),
    )
    monkeypatch.setattr(
        QMessageBox, "warning",
        staticmethod(lambda *_args, **_kwargs: QMessageBox.StandardButton.Ok),
    )


@pytest.fixture
def history_tab(tmp_path, qapp):
    with ClientEnvironment():
        cfg = Config()
        cfg.set_local_root(str(tmp_path / "NASBox"))
        cfg.set("retention_days_local", 7)
        engine = MagicMock()
        engine.connection = None
        logger = MagicMock()
        tab = HistoryTab(cfg, engine, logger)
        yield tab


def make_local_version(relative_path: str, days_ago: int, size: int = 1024, age: float | None = None) -> TrashVersion:
    day = date.today() - timedelta(days=days_ago)
    return TrashVersion(
        relative_path=relative_path,
        timestamp=f"{day.isoformat()}--12-00-00",
        trash_path=Path("/trash") / f"{days_ago}-{Path(relative_path).name}",
        age_days=float(days_ago) if age is None else age,
        size=size,
    )


def make_remote_version(relative_path: str, days_ago: int, size: int = 1024) -> RemoteTrashVersion:
    day = date.today() - timedelta(days=days_ago)
    return RemoteTrashVersion(
        relative_path=relative_path,
        timestamp=f"{day.isoformat()}--12-00-00",
        remote_trash_relative_path=f".sync-trash/{relative_path}-{day.isoformat()}",
        age_days=float(days_ago),
        size=size,
    )


def leaf_items(tab: HistoryTab):
    return [
        tab.tree.topLevelItem(group_index).child(leaf_index)
        for group_index in range(tab.tree.topLevelItemCount())
        for leaf_index in range(tab.tree.topLevelItem(group_index).childCount())
    ]


# --- pure helpers (trash.py) ------------------------------------------------


def test_version_date_prefers_timestamp_and_falls_back_to_age():
    version = make_local_version("docs/a.txt", 3)
    assert trash.version_date(version) == date.today() - timedelta(days=3)

    broken = TrashVersion("docs/b.txt", "garbage", Path("/trash/b"), age_days=2.25)
    assert trash.version_date(broken, today=date(2026, 1, 10)) == date(2026, 1, 8)


def test_group_by_day_is_newest_first_and_total_size_adds_up():
    today = date.today()
    versions = [
        make_local_version("a.txt", 0, size=100),
        make_local_version("b.txt", 2, size=200),
        make_local_version("c.txt", 0, size=300),
        make_local_version("d.txt", 1, size=400),
    ]
    groups = trash.group_by_day(versions)
    assert list(groups) == [today, today - timedelta(days=1), today - timedelta(days=2)]
    assert [v.relative_path for v in groups[today]] == ["a.txt", "c.txt"]
    assert trash.total_size(versions) == 1000
    assert trash.total_size([]) == 0


def test_day_labels_in_both_languages():
    today = date.today()
    assert day_label(today) == "Today"
    assert day_label(today - timedelta(days=1)) == "Yesterday"
    assert day_label(today - timedelta(days=2)) == (today - timedelta(days=2)).strftime("%m/%d/%Y")

    i18n.set_language("it")
    try:
        assert day_label(today) == "Oggi"
        assert day_label(today - timedelta(days=1)) == "Ieri"
        assert day_label(today - timedelta(days=2)) == (today - timedelta(days=2)).strftime("%d/%m/%Y")
    finally:
        i18n.set_language("en")


# --- tree: day grouping, filter and totals ----------------------------------

def test_tree_groups_by_day_with_count_and_age_sort(history_tab):
    today_new = make_local_version("docs/fresh.txt", 0, age=0.2)
    today_old = make_local_version("photos/old.jpg", 0, age=0.9)
    yesterday = make_local_version("docs/yesterday.txt", 1)
    history_tab._versions = [today_old, yesterday, today_new]
    history_tab._apply_filter()

    assert history_tab.tree.topLevelItemCount() == 2
    first = history_tab.tree.topLevelItem(0)
    assert "Today" in first.text(0)
    assert "2" in first.text(0)
    assert "Yesterday" in history_tab.tree.topLevelItem(1).text(0)

    # pre-existing age sort: newest leaf first inside a day group
    assert [leaf.text(0) for leaf in leaf_items(history_tab)][:2] == ["fresh.txt", "old.jpg"]
    assert leaf_items(history_tab)[0].toolTip(0) == "docs/fresh.txt"


def test_search_filter_and_totals_still_work(history_tab):
    history_tab._versions = [
        make_local_version("docs/report.pdf", 1, size=1024 * 1024),
        make_local_version("photos/vacation.jpg", 2, size=2 * 1024 * 1024),
    ]
    history_tab._apply_filter()
    assert "2 versions" in history_tab.totals_label.text()
    assert "2 folders" in history_tab.totals_label.text()
    assert "3.0 MB" in history_tab.totals_label.text()

    history_tab.search_edit.setText("report")
    filtered = history_tab.totals_label.text()
    assert "1 version" in filtered
    assert "1.0 MB" in filtered
    assert "2 total" in filtered
    assert history_tab.tree.topLevelItemCount() == 1
    assert len(leaf_items(history_tab)) == 1


def test_empty_state_explains_and_suggests(history_tab):
    history_tab._versions = []
    history_tab._apply_filter()
    assert history_tab.tree.topLevelItemCount() == 1
    empty = history_tab.tree.topLevelItem(0)
    assert empty is history_tab._empty_item
    assert "History is empty" in empty.text(0)
    assert not empty.flags() & Qt.ItemFlag.ItemIsSelectable

    history_tab.search_edit.setText("missing")
    history_tab._apply_filter()
    assert "No items match the search" in history_tab.tree.topLevelItem(0).text(0)


# --- batch restore -----------------------------------------------------------

def test_multi_selection_restores_latest_version_per_path(history_tab):
    older = make_local_version("docs/a.txt", 3, age=3.0)
    newer = make_local_version("docs/a.txt", 0, age=0.5)
    other = make_local_version("photos/b.jpg", 1)
    history_tab._versions = [older, newer, other]
    history_tab._apply_filter()

    for leaf in leaf_items(history_tab):
        leaf.setSelected(True)
    selected = history_tab._selected_versions(latest_per_path=True)
    assert len(selected) == 2
    assert any(v is newer for v in selected)  # the older version of a.txt is dropped
    assert any(v is other for v in selected)

    with patch.object(history_module.trash, "restore_version", return_value=True) as restore:
        history_tab._restore_selected()
    assert restore.call_count == 2
    restored = [call.args[0] for call in restore.call_args_list]
    assert any(v is newer for v in restored)
    assert any(v is other for v in restored)
    assert all(call.args[1] == history_tab.cfg.local_root() for call in restore.call_args_list)


def test_group_plus_leaf_selection_is_de_duplicated(history_tab):
    first = make_local_version("docs/a.txt", 0, age=0.2)
    second = make_local_version("docs/b.txt", 0, age=0.8)
    history_tab._versions = [first, second]
    history_tab._apply_filter()

    group = history_tab.tree.topLevelItem(0)
    group.setSelected(True)
    leaf_items(history_tab)[1].setSelected(True)
    # Both leaves are already pulled in by the selected group; selecting one
    # of them as well must not restore it twice.
    selected = history_tab._selected_versions(latest_per_path=True)
    assert len(selected) == 2
    assert any(v is first for v in selected)
    assert any(v is second for v in selected)

    group.setSelected(False)
    with patch.object(history_module.trash, "restore_version", return_value=True) as restore:
        history_tab._restore_group(group, history_tab.cfg.local_root())
    assert restore.call_count == 2


def test_restore_elsewhere_uses_chosen_directory(history_tab, tmp_path):
    versions = [make_local_version("docs/a.txt", 0), make_local_version("photos/b.jpg", 1)]
    history_tab._versions = versions
    history_tab._apply_filter()
    for leaf in leaf_items(history_tab):
        leaf.setSelected(True)
    destination = str(tmp_path / "altro")

    with (
        patch.object(QFileDialog, "getExistingDirectory", return_value=destination) as picker,
        patch.object(history_module.trash, "restore_version", return_value=True) as restore,
        patch.object(QMessageBox, "question", return_value=QMessageBox.StandardButton.Yes) as question,
    ):
        history_tab._restore_selected_elsewhere()

    picker.assert_called_once()
    assert restore.call_count == 2
    assert all(call.args[1] == destination for call in restore.call_args_list)
    assert destination in question.call_args.args[2]


def test_restore_elsewhere_reuses_remote_batch_path(history_tab, tmp_path):
    history_tab._versions = [make_remote_version("docs/a.txt", 0), make_remote_version("docs/b.txt", 1)]
    history_tab._apply_filter()
    for leaf in leaf_items(history_tab):
        leaf.setSelected(True)
    destination = str(tmp_path / "altro")

    with (
        patch.object(QFileDialog, "getExistingDirectory", return_value=destination),
        patch.object(history_tab, "_restore_remote_many") as batch,
    ):
        history_tab._restore_selected_elsewhere()

    batch.assert_called_once()
    assert batch.call_args.kwargs["destination_root"] == destination
    assert len(batch.call_args.args[0]) == 2


# --- reclaim panel -----------------------------------------------------------

def test_reclaim_panel_follows_shown_rows_and_saved_retention(history_tab):
    history_tab._versions = [
        make_local_version("docs/a.txt", 0, size=1024),
        make_local_version("docs/b.txt", 2, size=2048),
    ]
    history_tab._apply_filter()
    assert "3.0 KB" in history_tab.reclaim_size_label.text()
    assert "2" in history_tab.reclaim_size_label.text()
    assert "7" in history_tab.reclaim_clean_btn.text()

    history_tab.search_edit.setText("b.txt")
    assert "2.0 KB" in history_tab.reclaim_size_label.text()

    history_tab.local_spin.setValue(30)
    assert "7" in history_tab.reclaim_clean_btn.text()  # unsaved value is not used
    history_tab._save_retention()
    assert "30" in history_tab.reclaim_clean_btn.text()


def test_clean_old_needs_confirmation_and_calls_local_retention(history_tab):
    history_tab._versions = [make_local_version("docs/a.txt", 9)]
    history_tab._apply_filter()

    with (
        patch.object(QMessageBox, "question", return_value=QMessageBox.StandardButton.No),
        patch.object(history_tab, "_prune_local") as prune,
    ):
        history_tab._clean_old_reclaim()
    prune.assert_not_called()

    with (
        patch.object(QMessageBox, "question", return_value=QMessageBox.StandardButton.Yes) as question,
        patch.object(history_tab, "_prune_local") as prune,
    ):
        history_tab._clean_old_reclaim()
    prune.assert_called_once_with()
    assert "7" in question.call_args.args[2]


def test_prune_local_still_uses_trash_api(history_tab):
    with (
        patch.object(history_module, "run_in_background") as run,
        patch.object(history_module.trash, "prune_local", return_value=1) as prune_api,
        patch.object(history_module.trash, "list_local_versions", return_value=[]),
    ):
        history_tab._prune_local()
        assert run.called
        worker = run.call_args.args[2]
        assert worker(lambda _update: None) == (1, [])
    prune_api.assert_called_once()
    assert prune_api.call_args.args[0] is history_tab.cfg


def test_clean_old_on_remote_scope_asks_the_nas(history_tab):
    history_tab._showing_remote = True
    with patch.object(history_tab, "_prune_remote") as prune:
        history_tab._clean_old_reclaim()
    prune.assert_called_once_with()
