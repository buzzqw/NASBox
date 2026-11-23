"""Layout regressions for the compact Stato tab (2 columns + fixed header).

The goal of the compact layout is to lose vertical scrolling without losing any
of the status information, so these tests pin both the new structure and the
widgets/signals the rest of the application relies on.
"""
from __future__ import annotations

from unittest.mock import MagicMock

import pytest
from PyQt6.QtWidgets import QBoxLayout, QGroupBox, QHBoxLayout, QLayout, QScrollArea, QWidget

from sync_client.config import Config
from sync_client.gui.status_tab import StatusTab
from tests.support import ClientEnvironment


@pytest.fixture
def status_tab(qapp):
    with ClientEnvironment():
        tab = StatusTab(Config(), MagicMock(), MagicMock())
        qapp.processEvents()
        yield tab


def _card(tab: StatusTab, object_name: str) -> QGroupBox:
    card = tab.findChild(QGroupBox, object_name)
    assert card is not None, object_name
    return card


def _scroll(tab: StatusTab) -> QScrollArea:
    area = tab.findChild(QScrollArea)
    assert area is not None
    return area


def test_assurance_is_a_fixed_header_outside_the_scroll(status_tab):
    area = _scroll(status_tab)
    content = area.widget()
    assert content is not None
    # The card is a sibling of the scroll area, not a child of its content.
    assert not content.isAncestorOf(status_tab.assurance_box)
    outer = status_tab.layout()
    assert outer.itemAt(0).widget() is status_tab.assurance_box
    assert outer.itemAt(1).widget() is area
    assert not status_tab.assurance_box.isHidden()


def test_cards_are_not_stacked_directly_in_the_scroll_content(status_tab):
    root = _scroll(status_tab).widget().layout()
    for object_name in ("statusCard", "nasStatusBox", "attentionCard", "folderCard"):
        assert root.indexOf(_card(status_tab, object_name)) == -1, object_name


def test_status_and_nas_share_the_first_column_row(status_tab):
    status = _card(status_tab, "statusCard")
    nas = _card(status_tab, "nasStatusBox")
    row = status_tab._status_columns
    assert isinstance(row, QHBoxLayout)
    assert row.indexOf(nas) >= 0
    assert row.indexOf(status) >= 0
    assert row.indexOf(nas) >= 0
    # Columns keep a readable minimum width instead of being squeezed thin.
    assert status.minimumWidth() >= 360
    assert nas.minimumWidth() >= 360


def test_attention_and_folder_share_the_second_column_row(status_tab):
    attention = _card(status_tab, "attentionCard")
    folder = _card(status_tab, "folderCard")
    row = status_tab._secondary_columns
    assert isinstance(row, QHBoxLayout)
    assert row.indexOf(folder) >= 0
    assert row.indexOf(attention) >= 0
    assert row.indexOf(folder) >= 0


def test_column_rows_stack_when_the_window_is_narrow(status_tab):
    assert status_tab._status_columns.direction() == QBoxLayout.Direction.LeftToRight
    assert status_tab._secondary_columns.direction() == QBoxLayout.Direction.LeftToRight
    status_tab._update_column_layout(700)
    assert status_tab._status_columns.direction() == QBoxLayout.Direction.TopToBottom
    assert status_tab._secondary_columns.direction() == QBoxLayout.Direction.TopToBottom
    status_tab._update_column_layout(900)
    assert status_tab._status_columns.direction() == QBoxLayout.Direction.LeftToRight
    assert status_tab._secondary_columns.direction() == QBoxLayout.Direction.LeftToRight


def test_secondary_labels_moved_into_details_panel(status_tab):
    for name in (
        "remote_lock_label", "watcher_label", "queue_label", "pending_label",
        "last_sync_label", "last_problem_label",
    ):
        assert status_tab.details_widget.isAncestorOf(getattr(status_tab, name)), name
    assert status_tab.details_widget.isHidden()


def test_all_main_widgets_and_object_names_are_preserved(status_tab):
    names = (
        "assurance_box", "assurance_headline", "assurance_proof", "assurance_checks",
        "assurance_activity", "assurance_stats", "verify_now_btn", "status_label",
        "nas_host_label", "nas_activity_label", "remote_lock_label", "devices_label",
        "conflict_stat_label", "watcher_label", "queue_label", "pending_label",
        "last_sync_label", "last_problem_label", "details_widget", "details_toggle",
        "folder_label", "version_label", "update_available_label", "attention_box",
        "attention_layout", "sync_state_label", "sync_progress_bar", "batch_progress_bar",
        "nas_storage_label", "nas_storage_bar", "diagnostics_label", "integrity_label",
        "sync_now_btn", "pause_btn", "pause_for_btn", "hash_progress_label",
        "sync_feedback_label", "confirm_deletes_btn",
    )
    for name in names:
        control = getattr(status_tab, name)
        assert isinstance(control, QWidget | QLayout | dict), name
        if isinstance(control, QWidget):
            assert status_tab.isAncestorOf(control), name

    object_names = {
        "assuranceCard", "assuranceHeadline", "assuranceProof", "assuranceChecks",
        "assuranceActivity", "statusCard", "statusMessage", "statusConflictStat",
        "statusDevices", "statusWatcher", "statusQueue", "statusVersion",
        "statusUpdateAvailable", "nasStatusBox", "attentionCard", "folderCard",
        "confirmDeletes", "primaryButton", "secondaryButton",
    }
    present = {widget.objectName() for widget in status_tab.findChildren(QWidget)}
    assert object_names <= present
    assert set(status_tab.assurance_stats) == {
        "tracked", "pending", "queue", "conflicts", "revision", "lastcheck",
    }
    for key, label in status_tab.assurance_stats.items():
        assert label.objectName() == f"assuranceStat_{key}"


def test_compact_layout_keeps_public_signals(status_tab):
    assert hasattr(status_tab, "assurance_status")
    assert hasattr(status_tab, "attention_action_requested")
