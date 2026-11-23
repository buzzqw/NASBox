"""Compattezza del tab Impostazioni: header di ricerca fisso, due colonne
strette e TUTTE le sezioni aperte e visibili all'avvio (niente piu' limiter di
sezioni aperte: la ricerca filtra/evidenzia ma non lascia sezioni nascoste).

Nessuna funzione viene rimossa: i test qui sotto verificano esplicitamente che
gli attributi e le azioni pubblici restino raggiungibili per nome.
"""
from __future__ import annotations

from unittest.mock import MagicMock

import pytest
from PyQt6.QtWidgets import QBoxLayout, QGroupBox, QHBoxLayout, QVBoxLayout, QWidget

from sync_client import i18n
from sync_client.config import Config
from sync_client.gui.settings_tab import SettingsTab
from tests.support import ClientEnvironment


@pytest.fixture
def compact_tab(qapp):
    with ClientEnvironment():
        i18n.set_language("it")
        try:
            yield SettingsTab(Config(), MagicMock())
        finally:
            i18n.set_language(i18n.DEFAULT_LANG)


def _section(tab: SettingsTab, key: str) -> QGroupBox:
    box = tab.findChild(QGroupBox, f"settingsSection_{key}")
    assert box is not None, key
    return box


# --- struttura -------------------------------------------------------------

def test_two_columns_are_present(compact_tab):
    tab = compact_tab
    assert tab._columns.count() == 2
    for index in range(tab._columns.count()):
        assert isinstance(tab._columns.itemAt(index).layout(), QVBoxLayout)

    # La direzione cambia con la larghezza, ma le colonne restano due.
    tab.resize(640, 800)
    tab._update_column_layout(640)
    assert tab._single_column is True
    assert tab._columns.direction() == QBoxLayout.Direction.TopToBottom
    assert tab._columns.count() == 2

    tab.resize(1200, 800)
    tab._update_column_layout(1200)
    assert tab._single_column is False
    assert tab._columns.direction() == QBoxLayout.Direction.LeftToRight
    assert tab._columns.count() == 2


def test_groups_are_split_between_the_two_columns(compact_tab):
    tab = compact_tab
    left, right = tab._column_sections
    left_names = [tab._section_keys[id(box)] for box in left]
    right_names = {tab._section_keys[id(box)] for box in right}

    # NAS + banda + esclusioni a sinistra; tutto il resto a destra.
    assert left_names == ["nas", "bandwidth", "exclude"]
    assert {"cadence", "schedule", "feedback", "language", "theme",
            "info", "nas_facts", "maintenance"} <= right_names
    assert "exclude" not in right_names
    assert not (set(left_names) & right_names)


def test_search_header_sits_outside_the_scroll_area(compact_tab):
    tab = compact_tab
    header = tab._search_header
    assert isinstance(header, QWidget)
    assert header.isAncestorOf(tab.search_edit)
    assert tab.isAncestorOf(header)

    scroll_widget = tab._scroll_area.widget()
    assert scroll_widget is not None
    assert not scroll_widget.isAncestorOf(header)
    assert not scroll_widget.isAncestorOf(tab.search_edit)


def test_search_label_is_visible_and_translated(compact_tab):
    tab = compact_tab
    label = tab.findChild(QWidget, "settingsSearchLabel")
    assert label is not None
    assert label.text().strip()
    assert label.text() == i18n.t("transfers.search_label")


# --- tutte le sezioni esposte ----------------------------------------------

def test_all_sections_are_expanded_and_visible(compact_tab):
    tab = compact_tab
    assert len(tab._column_sections) == 2
    named = [
        box for box in tab.findChildren(QGroupBox)
        if tab._section_keys.get(id(box))
    ]
    assert len(named) == len(tab._sections)
    for section in tab._sections:
        assert section.isChecked(), section.objectName()
        assert not section.isHidden(), section.objectName()
        fields = tab._section_fields(section)
        assert fields, section.objectName()
        assert all(not field.isHidden() for field in fields), section.objectName()


def test_search_highlights_an_always_open_section(compact_tab):
    tab = compact_tab
    theme = _section(tab, "theme")
    assert theme.isChecked()  # gia' aperta all'avvio: nessun check da cliccare

    tab.search_edit.setText(theme.title())
    assert theme.isChecked()
    assert not theme.isHidden()
    fields = tab._section_fields(theme)
    assert fields
    assert all(not field.isHidden() for field in fields)

    tab.search_edit.setText("")
    assert theme.isChecked()
    assert not theme.isHidden()
    assert all(section.isChecked() for section in tab._sections)


def test_search_restores_all_sections_visible(compact_tab):
    tab = compact_tab
    tab.search_edit.setText(_section(tab, "nas").title())
    assert _section(tab, "nas").isChecked()
    tab.search_edit.setText("")
    assert all(not section.isHidden() for section in tab._sections)
    assert all(section.isChecked() for section in tab._sections)


# --- densità dei controlli -------------------------------------------------

def test_group_boxes_use_tight_margins_and_spacing(compact_tab):
    tab = compact_tab
    for section in tab._sections:
        layout = section.layout()
        assert layout is not None, section.objectName()
        margins = layout.getContentsMargins()
        assert max(margins) <= 6, section.objectName()
        assert layout.spacing() <= 6, section.objectName()


def test_list_widgets_are_height_capped(compact_tab):
    tab = compact_tab
    assert 0 < tab.schedule_list.maximumHeight() <= 100
    assert 0 < tab.exclude_list.maximumHeight() <= 100


def test_short_fields_are_width_capped(compact_tab):
    tab = compact_tab
    for name in (
        "ssh_port", "jump_port", "max_delete_spin", "bw_upload_spin",
        "bw_download_spin", "poll_spin", "metrics_refresh_spin", "full_pull_spin",
    ):
        field = getattr(tab, name)
        assert 0 < field.maximumWidth() <= 120, name
    for name in (
        "sync_mode_combo", "conflict_policy_combo", "compression_combo",
        "tray_click_combo", "language_combo", "theme_combo",
        "exclusion_profile_combo",
    ):
        field = getattr(tab, name)
        assert 0 < field.maximumWidth() <= 250, name


def test_bandwidth_fields_are_narrow_and_on_one_row(compact_tab):
    tab = compact_tab
    bw_box = _section(tab, "bandwidth")
    layout = bw_box.layout()
    assert isinstance(layout, QHBoxLayout)

    for name in ("bw_upload_spin", "bw_download_spin"):
        field = getattr(tab, name)
        assert 90 <= field.maximumWidth() <= 110, name
        assert layout.indexOf(field) >= 0, name
    # Anche le etichette restano corte: nessun campo si allarga a tutta la colonna.
    for name in ("bw_upload_label", "bw_download_label"):
        label = getattr(tab, name)
        assert 0 < label.maximumWidth() <= 110, name
        assert layout.indexOf(label) >= 0, name

    widgets = [
        layout.itemAt(index).widget()
        for index in range(layout.count())
        if layout.itemAt(index).widget() is not None
    ]
    assert widgets == [
        tab.bw_upload_label, tab.bw_upload_spin,
        tab.bw_download_label, tab.bw_download_spin,
    ]


def test_exclude_box_sits_directly_below_bandwidth_in_left_column(compact_tab):
    tab = compact_tab
    left, right = tab._column_sections
    exclude = _section(tab, "exclude")
    bandwidth = _section(tab, "bandwidth")

    assert [tab._section_keys[id(s)] for s in left] == ["nas", "bandwidth", "exclude"]
    assert exclude in left
    assert exclude not in right

    left_layout = tab._columns.itemAt(0).layout()
    assert left_layout.indexOf(exclude) == left_layout.indexOf(bandwidth) + 1


# --- nessuna funzione rimossa ----------------------------------------------

def test_original_attributes_and_actions_are_preserved(compact_tab):
    tab = compact_tab
    for name in (
        "search_edit", "search_shortcut", "_sections", "_columns",
        "_collapse_state", "_section_keys", "_highlighted",
        "nas_lan", "nas_wan", "nas_user", "ssh_port", "jump_host", "jump_port",
        "jump_user", "remote_server_script", "restart_daemon_btn",
        "remote_prefix", "detect_btn", "sync_mode_combo", "delete_enabled",
        "max_delete_spin", "conflict_policy_combo",
        "bw_upload_label", "bw_upload_spin", "bw_download_label",
        "bw_download_spin",
        "poll_spin", "metrics_refresh_spin", "compression_combo",
        "full_pull_spin", "nas_friendly_btn", "multiplexing_checkbox",
        "schedule_enabled", "schedule_list",
        "notify_sync_checkbox", "animate_sync_checkbox", "tray_click_combo",
        "language_combo", "theme_combo", "export_diag_btn",
        "nas_fact_retention", "nas_fact_server_version", "nas_fact_lock",
        "conflict_count_label", "maint_scan_btn", "maint_delete_btn",
        "exclude_list", "exclusion_profile_combo", "exclude_input",
        "remove_exclude_btn", "selective_btn", "selective_label",
        "auto_exclude_label", "save_feedback", "save_btn",
    ):
        assert hasattr(tab, name), name
    for method in (
        "_apply_search_filter", "_apply_initial_collapse", "_focus_search",
        "_on_section_toggled", "_section_fields", "_export_diagnostics",
        "_detect_from_nas", "_refresh_nas_facts", "_save_settings",
    ):
        assert callable(getattr(tab, method)), method
