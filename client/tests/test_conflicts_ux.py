"""Focused tests for the improved Conflict Center UX.

Only the Conflicts tab is exercised here: the side-by-side comparison, the
large resolution buttons, the stdlib-only three-way merge and the exact name
preview of a conflict copy.
"""
from __future__ import annotations

from pathlib import Path
from types import SimpleNamespace
from unittest.mock import MagicMock

import pytest

from PyQt6.QtWidgets import QPlainTextEdit, QSplitter

from sync_client import conflicts
from sync_client.config import Config
from sync_client.gui.conflicts_tab import (
    ConflictsTab,
    conflict_copy_name,
    diff_changed_lines,
    merge_three_way,
)
from sync_client.i18n import STRINGS, t
from sync_client.push_worker import PushWorker
from tests.support import ClientEnvironment


@pytest.fixture
def conflict_tab(tmp_path, qapp):
    """A tab with one real text conflict: doc.txt vs. its dev1 copy."""
    with ClientEnvironment():
        cfg = Config()
        root = tmp_path / "nasbox"
        root.mkdir()
        cfg.set_local_root(str(root))
        (root / "doc.txt").write_text("one\ntwo\nthree\n", encoding="utf-8")
        (root / "doc (conflitto da dev1 aaaabbbb).txt").write_text(
            "one\nTWO\nthree\n", encoding="utf-8",
        )
        engine = MagicMock()
        engine.sync_state.device_id.return_value = "dev1"
        tab = ConflictsTab(cfg, engine)
        yield tab, root, cfg


def _wait_for_group(qtbot, tab):
    qtbot.waitUntil(lambda: tab.tree.topLevelItemCount() == 1, timeout=5000)


# --- task 1: side-by-side comparison ---------------------------------------

def test_side_by_side_preview_highlights_differing_lines(qtbot, conflict_tab):
    tab, _root, _cfg = conflict_tab
    _wait_for_group(qtbot, tab)

    assert isinstance(tab.compare_splitter, QSplitter)
    assert tab.compare_splitter.count() == 2
    assert isinstance(tab.preview_original, QPlainTextEdit)
    assert isinstance(tab.preview_alternative, QPlainTextEdit)
    assert tab.preview_original.isReadOnly()
    assert tab.preview_alternative.isReadOnly()

    assert "two" in tab.preview_original.toPlainText()
    assert "TWO" in tab.preview_alternative.toPlainText()

    left_lines, right_lines = diff_changed_lines(
        ["one", "two", "three"], ["one", "TWO", "three"],
    )
    assert left_lines == [2]
    assert right_lines == [2]
    for widget, lines, color in (
        (tab.preview_original, left_lines, "#ffe0e0"),
        (tab.preview_alternative, right_lines, "#e0ffe0"),
    ):
        for number in (1, 2, 3):
            block = widget.document().findBlockByLineNumber(number - 1)
            background = block.begin().fragment().charFormat().background().color().name()
            assert background == (color if number in lines else "#000000")


# --- task 2: large, clear action buttons ------------------------------------

def test_large_action_buttons_are_present_and_wired(qtbot, conflict_tab, monkeypatch):
    tab, _root, _cfg = conflict_tab
    _wait_for_group(qtbot, tab)

    expected = (
        (tab.use_newest_btn, "conflicts.use_newest_btn", "conflicts.use_newest_tooltip"),
        (tab.keep_both_big_btn, "conflicts.keep_both_big_btn", "conflicts.keep_both_tooltip"),
        (tab.ignore_close_btn, "conflicts.ignore_close_btn", "conflicts.ignore_close_tooltip"),
    )
    for button, text_key, tooltip_key in expected:
        assert button.text() == t(text_key)
        assert button.toolTip() == t(tooltip_key)
        assert button.accessibleName().strip()
        assert button.minimumHeight() >= 44

    assert tab.use_newest_btn.isEnabled()
    assert tab.keep_both_big_btn.isEnabled()
    assert tab.ignore_close_btn.isEnabled()

    # Each button delegates to the existing resolution/UI helper.
    keep = MagicMock()
    monkeypatch.setattr(tab, "_keep_selected", keep)
    started: list[str] = []

    def fake_run_in_background(parent, name, worker, on_done, on_progress=None):
        started.append(name)

    monkeypatch.setattr(
        "sync_client.gui.conflicts_tab.run_in_background", fake_run_in_background,
    )

    tab.use_newest_btn.click()
    chosen = tab._selected_candidate()
    info = tab._infos[str(tab._selected_group().original)]
    assert chosen == info.recommended
    keep.assert_called_once_with()

    tab.keep_both_big_btn.click()
    assert started == ["_conflict_action_call"]  # same worker as the compact button

    # The previous button left an action in flight; invoke the slot directly
    # (the button itself is disabled while busy, like every other action).
    tab._ignore_close()
    assert not tab.tree.selectedItems()
    assert tab.candidate_combo.count() == 0
    assert tab.preview_original.toPlainText() == ""


# --- task 3: stdlib three-way merge ----------------------------------------

def test_merge_three_way_combines_non_overlapping_changes():
    base = "a\nb\nc\n"
    ours = "A\nb\nc\n"
    theirs = "a\nb\nC\n"
    merged = merge_three_way(base, ours, theirs)
    assert "A\n" in merged
    assert "C\n" in merged
    assert "b\n" in merged
    assert "<<<<<<<" not in merged


def test_merge_three_way_marks_overlapping_changes_for_review():
    merged = merge_three_way("a\nb\nc\n", "a\nOURS\nc\n", "a\nTHEIRS\nc\n")
    assert "<<<<<<<" in merged
    assert "=======" in merged
    assert ">>>>>>>" in merged
    assert "OURS" in merged and "THEIRS" in merged


def test_merge_three_way_honours_a_deletion_on_one_side():
    merged = merge_three_way("a\nb\nc\n", "a\nc\n", "a\nb\nC\n")
    assert "b\n" not in merged
    assert "C\n" in merged
    assert merged.count("a\n") == 1


class _GroupWithBase:
    """Minimal stand-in for a future ConflictGroup carrying a common base."""

    def __init__(self, original: Path, candidates: tuple[Path, ...], base: Path):
        self.original = original
        self.candidates = candidates
        self.base = base
        self.group_id = ""


def test_merge_button_disabled_without_base_and_editable_preview_when_present(
    qtbot, conflict_tab, tmp_path,
):
    tab, root, _cfg = conflict_tab
    _wait_for_group(qtbot, tab)

    # The production ConflictGroup has no base: no invented ancestor.
    assert tab.merge_btn.toolTip() == t("conflicts.merge_base_unavailable")
    assert not tab.merge_btn.isEnabled()

    base = root / "base.txt"
    base.write_text("one\ntwo\nthree\n", encoding="utf-8")
    group = tab._selected_group()
    stub = _GroupWithBase(group.original, group.candidates, base)
    tab._groups = [stub]
    tab._infos = {str(stub.original): tab._group_info(stub)}
    tab._populate()
    assert tab.merge_btn.isEnabled()

    tab.merge_btn.click()
    assert not tab.merge_box.isHidden()
    assert tab.compare_splitter.isHidden()
    assert not tab.merge_editor.isReadOnly()
    assert "TWO" in tab.merge_editor.toPlainText()

    tab.merge_cancel_btn.click()
    assert tab.merge_box.isHidden()
    assert not tab.compare_splitter.isHidden()


# --- task 4: exact conflict-copy name preview -------------------------------

def test_copy_name_preview_uses_the_pusher_format(qtbot, conflict_tab, monkeypatch):
    tab, _root, _cfg = conflict_tab
    _wait_for_group(qtbot, tab)

    # The label is rendered through t(); provide the future translation so the
    # assertion checks the actual name, not just the untranslated key.
    monkeypatch.setitem(
        STRINGS, "conflicts.copy_name_preview",
        {"it": "Nome copia: {name}", "en": "Copy name: {name}"},
    )
    tab._update_copy_name(tab._selected_group())
    text = tab.copy_name_label.text()
    name = conflict_copy_name("doc.txt", "dev1")
    assert text == t("conflicts.copy_name_preview", name=name)
    assert conflicts.CONFLICT_TAG in text
    assert "dev1" in text
    assert tab.copy_name_label.toolTip() == name

    # The helper mirrors the production naming rule exactly.
    helper = PushWorker._conflict_path
    expected = helper(
        SimpleNamespace(sync_state=SimpleNamespace(device_id=lambda: "dev1")),
        "doc.txt", "",
    )
    assert conflict_copy_name("doc.txt", "dev1") == expected
