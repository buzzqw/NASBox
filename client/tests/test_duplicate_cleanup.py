"""Guided duplicate cleanup: selection, accounting and safe trash moves."""
from __future__ import annotations

import pytest
from PyQt6.QtCore import Qt
from PyQt6.QtWidgets import QDialog, QMessageBox

from sync_client import paths
from sync_client.config import Config
from sync_client.gui.duplicate_cleanup import (
    DUP_CLEAN_STRINGS, CleanupTarget, DuplicateCleanupDialog,
)
from sync_client.i18n import SUPPORTED, t
from sync_client.metrics_worker import DuplicateGroup
from tests.support import ClientEnvironment

GROUPS = (
    DuplicateGroup(3, "a" * 64, ("keep1.txt", "copy1.txt")),
    DuplicateGroup(5, "b" * 64, ("keep2.txt", "copy2.txt", "old/copy2.txt")),
)


@pytest.fixture
def cleanup(tmp_path, qtbot):
    with ClientEnvironment():
        cfg = Config()
        root = tmp_path / "nasbox"
        (root / "old").mkdir(parents=True)
        cfg.set_local_root(str(root))
        for group in GROUPS:
            for relative in group.paths:
                (root / relative).write_bytes(b"x" * group.size_bytes)
        dialog = DuplicateCleanupDialog(cfg, GROUPS)
        qtbot.addWidget(dialog)
        yield dialog, cfg, root, tmp_path


def _group_item(dialog, index=0):
    return dialog.tree.topLevelItem(index)


def _child(dialog, group_index, relative):
    group_item = _group_item(dialog, group_index)
    for index in range(group_item.childCount()):
        child = group_item.child(index)
        if child.text(0) in (relative, t("dupclean.keep_path", path=relative)):
            return child
    raise AssertionError(f"no row for {relative!r}")


def _run_trash(
    qtbot, monkeypatch, dialog, answer=QMessageBox.StandardButton.Yes,
    expect_run=True,
):
    """Answer the confirmation and wait for the background worker's callback."""
    questions = []
    infos = []

    def ask(*_args, **_kwargs):
        questions.append(True)
        return answer

    monkeypatch.setattr(QMessageBox, "question", ask)
    monkeypatch.setattr(
        QMessageBox, "information", lambda *_args, **_kwargs: infos.append(True)
    )
    monkeypatch.setattr(
        QMessageBox, "warning", lambda *_args, **_kwargs: infos.append(True)
    )
    dialog.last_result = None
    dialog.trash_btn.click()
    if expect_run:
        qtbot.waitUntil(lambda: dialog.last_result is not None, timeout=10000)
    else:
        qtbot.wait(50)  # a declined confirmation must not start any worker
    return questions, infos


def test_dupclean_strings_are_registered_for_both_languages():
    assert all(key.startswith("dupclean.") for key in DUP_CLEAN_STRINGS)
    assert DUP_CLEAN_STRINGS
    for key, entry in DUP_CLEAN_STRINGS.items():
        assert set(entry) == set(SUPPORTED), key
        for language in SUPPORTED:
            assert entry[language].strip(), (key, language)
            assert t(key)  # plain t("dupclean.*") lookups work after import


def test_two_groups_selection_and_reclaimable_space(cleanup):
    dialog, _cfg, _root, _tmp = cleanup
    assert dialog.tree.topLevelItemCount() == 2

    # "Mantieni" is shown but can never be checked or selected.
    keep = _child(dialog, 0, GROUPS[0].paths[0])
    assert keep.text(0) == t("dupclean.keep_path", path=GROUPS[0].paths[0])
    assert not keep.flags() & Qt.ItemFlag.ItemIsUserCheckable
    assert dialog.selected_targets() == []
    assert dialog.reclaimable_bytes() == 0

    # A group checkbox drives all of its removable children.
    _group_item(dialog, 0).setCheckState(0, Qt.CheckState.Checked)
    assert [target.path for target in dialog.selected_targets()] == ["copy1.txt"]
    assert dialog.reclaimable_bytes() == 3

    _group_item(dialog, 1).setCheckState(0, Qt.CheckState.Checked)
    assert {target.path for target in dialog.selected_targets()} == {
        "copy1.txt", "copy2.txt", "old/copy2.txt",
    }
    assert dialog.reclaimable_bytes() == 13
    assert dialog.reclaim_label.text() == t("dupclean.reclaim", size="13 B")

    # A single child can be toggled off; the group becomes partially checked.
    _child(dialog, 1, "old/copy2.txt").setCheckState(0, Qt.CheckState.Unchecked)
    assert _group_item(dialog, 1).checkState(0) == Qt.CheckState.PartiallyChecked
    assert dialog.reclaimable_bytes() == 8

    dialog.select_none()
    assert dialog.selected_targets() == []
    assert dialog.reclaimable_bytes() == 0
    dialog.select_all()
    assert len(dialog.selected_targets()) == 3
    assert dialog.reclaimable_bytes() == 13


def test_trash_button_moves_only_candidates_and_accepts(cleanup, qtbot, monkeypatch):
    dialog, _cfg, root, _tmp = cleanup
    dialog.select_all()
    questions, infos = _run_trash(qtbot, monkeypatch, dialog)

    assert len(questions) == 1  # confirmation happened before acting
    assert len(infos) == 1  # final result shown
    assert dialog.last_result.moved == ["copy1.txt", "copy2.txt", "old/copy2.txt"]
    assert dialog.last_result.errors == []
    assert dialog.result() == QDialog.DialogCode.Accepted

    # Kept files survive; duplicates were moved into restorable local history.
    for relative in (GROUPS[0].paths[0], GROUPS[1].paths[0]):
        assert (root / relative).is_file()
    for relative in ("copy1.txt", "copy2.txt", "old/copy2.txt"):
        assert not (root / relative).exists()
    trash_names = [p.name for p in paths.local_trash_dir().rglob("*")]
    for name in ("copy1.txt", "copy2.txt", "copy2.txt"):
        assert any(trash_name.startswith(name + "-") for trash_name in trash_names)


def test_missing_file_is_reported_and_accepted_when_something_moved(
    cleanup, qtbot, monkeypatch,
):
    dialog, _cfg, root, _tmp = cleanup
    (root / "copy2.txt").unlink()
    dialog.select_all()

    _run_trash(qtbot, monkeypatch, dialog)

    assert sorted(dialog.last_result.moved) == ["copy1.txt", "old/copy2.txt"]
    assert dialog.last_result.errors == [t("dupclean.error_missing", path="copy2.txt")]
    assert dialog.result() == QDialog.DialogCode.Accepted


def test_no_move_means_dialog_stays_open(cleanup, qtbot, monkeypatch):
    dialog, _cfg, root, _tmp = cleanup
    for relative in ("copy1.txt", "copy2.txt", "old/copy2.txt"):
        (root / relative).unlink()
    dialog.select_all()

    _run_trash(qtbot, monkeypatch, dialog)

    assert dialog.last_result.moved == []
    assert len(dialog.last_result.errors) == 3
    assert dialog.result() == QDialog.DialogCode.Rejected


def test_confirmation_can_be_cancelled(cleanup, qtbot, monkeypatch):
    dialog, _cfg, root, _tmp = cleanup
    dialog.select_all()
    questions, infos = _run_trash(
        qtbot, monkeypatch, dialog, QMessageBox.StandardButton.No, expect_run=False
    )

    assert len(questions) == 1
    assert infos == []
    assert dialog.last_result is None
    assert (root / "copy1.txt").is_file()
    assert dialog.result() == QDialog.DialogCode.Rejected


def test_outside_paths_are_rejected(cleanup, tmp_path):
    dialog, _cfg, root, _tmp = cleanup
    outside = tmp_path / "outside.txt"
    outside.write_bytes(b"x" * 3)

    assert dialog.resolve_relative_path("../outside.txt", str(root)) is None
    assert dialog.resolve_relative_path(str(outside), str(root)) is None
    # A symlink inside the synced tree must not be a back door either.
    link = root / "escape.txt"
    link.symlink_to(outside)
    assert dialog.resolve_relative_path("escape.txt", str(root)) is None
    assert dialog.resolve_relative_path("keep1.txt", str(root)) == root / "keep1.txt"

    result = dialog._move_targets_to_trash(
        [CleanupTarget(GROUPS[0], "../outside.txt")], str(root)
    )
    assert result.moved == []
    assert result.errors == [
        t("dupclean.error_outside_root", path="../outside.txt")
    ]
    assert outside.is_file()


def test_keep_path_can_never_be_trashed(cleanup):
    dialog, _cfg, root, _tmp = cleanup
    keep = GROUPS[0].paths[0]
    result = dialog._move_targets_to_trash(
        [CleanupTarget(GROUPS[0], keep)], str(root)
    )
    assert result.moved == []
    assert result.errors == [t("dupclean.error_keep", path=keep)]
    assert (root / keep).is_file()
