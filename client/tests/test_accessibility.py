"""Accessibility guardrails for the primary GUI controls."""
from __future__ import annotations

from unittest.mock import MagicMock

import pytest
from PyQt6.QtCore import Qt
from PyQt6.QtGui import QKeySequence
from PyQt6.QtWidgets import QToolButton

from sync_client.config import Config
from sync_client.gui import style
from sync_client.gui.conflicts_tab import ConflictsTab
from sync_client.gui.status_tab import StatusTab
from tests.support import ClientEnvironment


@pytest.fixture
def status_tab(qapp):
    with ClientEnvironment():
        yield StatusTab(Config(), MagicMock(), MagicMock())


@pytest.fixture
def conflicts_tab(qapp):
    with ClientEnvironment():
        yield ConflictsTab(Config(), MagicMock())


def test_clickable_badges_are_flat_tool_buttons(status_tab):
    badges = (
        status_tab.conflict_stat_label,
        status_tab.version_label,
        status_tab.update_available_label,
    )
    for badge in badges:
        assert isinstance(badge, QToolButton)
        assert badge.toolButtonStyle() == Qt.ToolButtonStyle.ToolButtonTextOnly
        assert badge.accessibleName().strip()
        assert badge.toolTip().strip()
    assert status_tab.version_label.text().startswith("NASBox")


def test_primary_status_controls_have_accessible_names(status_tab):
    names = (
        "status_label", "sync_state_label", "sync_progress_bar",
        "batch_progress_bar", "conflict_stat_label", "version_label",
        "update_available_label", "details_toggle", "sync_now_btn",
        "pause_btn", "pause_for_btn", "hash_progress_label",
        "diagnostics_label", "integrity_label", "nas_host_label",
        "nas_activity_label", "nas_storage_label", "nas_storage_bar",
        "folder_label", "sync_feedback_label",
    )
    for name in names:
        control = getattr(status_tab, name)
        assert control.accessibleName().strip(), name


def test_conflict_controls_have_accessible_names(conflicts_tab):
    names = (
        "search_edit", "refresh_btn", "resolve_everything_btn", "tree",
        "candidate_combo", "preview", "keep_orig_btn", "keep_alt_btn",
        "keep_both_btn", "open_btn", "restore_btn", "discard_btn",
        "keep_btn", "resolve_all_btn",
    )
    for name in names:
        control = getattr(conflicts_tab, name)
        assert control.accessibleName().strip(), name


def test_keyboard_shortcuts_are_valid():
    assert not QKeySequence(style.SHORTCUT_SYNC_NOW).isEmpty()
    assert not QKeySequence(style.SHORTCUT_SHOW_WINDOW).isEmpty()
    assert QKeySequence(style.SHORTCUT_SYNC_NOW) != QKeySequence(style.SHORTCUT_SHOW_WINDOW)


def test_status_indicators_carry_an_icon_as_well_as_a_colour(status_tab):
    status_tab._refresh_watcher({"watcher_mode": "inotify", "watcher_detail": ""})
    assert status_tab.watcher_label.text().startswith("✅")
    assert status_tab.watcher_label.property("state") == "active"
    status_tab._refresh_watcher({"watcher_mode": "polling", "watcher_detail": ""})
    assert status_tab.watcher_label.text().startswith("⚠")
    assert status_tab.watcher_label.property("state") == "warning"
