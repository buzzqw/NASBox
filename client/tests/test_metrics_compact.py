"""Layout contract for the compact 'Monitor NAS' tab.

The dashboard must scroll less without losing any readout: the status/refresh
bar is a fixed header, the tall sections collapse, and the two charts plus the
summary cards stay in two compact columns.
"""
from __future__ import annotations

import time
from unittest.mock import MagicMock, patch

import pytest
from PyQt6.QtWidgets import (
    QFormLayout, QGridLayout, QGroupBox, QHBoxLayout, QScrollArea, QVBoxLayout, QWidget,
)

from sync_client.config import Config
from sync_client.gui import metrics_tab as metrics_tab_module
from sync_client.gui.metrics_tab import MetricsTab
from sync_client.metrics_worker import StorageInsights, TrashInsight
from tests.support import ClientEnvironment


@pytest.fixture
def compact_tab(qapp, tmp_path):
    with ClientEnvironment():
        cfg = Config()
        root = tmp_path / "nasbox"
        root.mkdir()
        cfg.set_local_root(str(root))
        yield MetricsTab(cfg, MagicMock())


def _content(tab: MetricsTab, section_name: str) -> QWidget:
    widget = tab.findChild(QWidget, section_name + "Content")
    assert widget is not None, section_name
    return widget


def _root_layout(tab: MetricsTab) -> QVBoxLayout:
    scroll = tab.findChild(QScrollArea)
    assert scroll is not None
    return scroll.widget().layout()


def _layout_at(tab: MetricsTab, layout_type):
    root = _root_layout(tab)
    for index in range(root.count()):
        layout = root.itemAt(index).layout()
        if isinstance(layout, layout_type):
            return layout
    raise AssertionError(layout_type)


def _empty_result() -> StorageInsights:
    return StorageInsights(
        root="", collected_at=time.time(), scanned_files=0, scan_errors=0,
        candidates_limit_reached=False, top_folders=(), duplicates=(),
        duplicate_candidates=0, duplicate_wasted_bytes=0,
        trash=TrashInsight(0, 0, 0, 0, 0),
    )


# --- fixed header -----------------------------------------------------------


def test_status_and_refresh_are_outside_the_scroll_area(compact_tab):
    scroll = compact_tab.findChild(QScrollArea)
    assert scroll is not None
    assert not scroll.isAncestorOf(compact_tab.status_label)
    assert not scroll.isAncestorOf(compact_tab.refresh_btn)

    outer = compact_tab.layout()
    assert isinstance(outer, QVBoxLayout)
    assert outer.count() == 2
    header = outer.itemAt(0).widget()
    assert isinstance(header, QWidget)
    assert header.objectName() == "metricsHeader"
    assert header.isAncestorOf(compact_tab.status_label)
    assert header.isAncestorOf(compact_tab.refresh_btn)
    assert outer.itemAt(1).widget() is scroll


# --- collapsible sections and their defaults --------------------------------


def test_tall_sections_are_checkable(compact_tab):
    for attribute, object_name in (
        ("load_chart_section", "loadChartSection"),
        ("cpu_chart_section", "cpuChartSection"),
        ("storage_section", "storageSection"),
        ("storage_details_section", "storageDetailsSection"),
        ("storage_duplicates_section", "storageDuplicatesSection"),
    ):
        section = getattr(compact_tab, attribute)
        assert isinstance(section, QGroupBox)
        assert section.isCheckable()
        assert section.objectName() == object_name


def test_default_expanded_state(compact_tab):
    assert compact_tab.load_chart_section.isChecked() is True
    assert compact_tab.storage_section.isChecked() is True
    assert compact_tab.cpu_chart_section.isChecked() is False
    # Analysis details and duplicates start open: no extra checkbox to reveal.
    assert compact_tab.storage_details_section.isChecked() is True
    assert compact_tab.storage_duplicates_section.isChecked() is True

    assert _content(compact_tab, "loadChartSection").isHidden() is False
    assert _content(compact_tab, "storageSection").isHidden() is False
    assert _content(compact_tab, "cpuChartSection").isHidden() is True
    assert _content(compact_tab, "storageDetailsSection").isHidden() is False
    assert _content(compact_tab, "storageDuplicatesSection").isHidden() is False


def test_toggling_a_section_shows_and_hides_its_contents(compact_tab):
    for attribute, object_name in (
        ("cpu_chart_section", "cpuChartSection"),
        ("storage_details_section", "storageDetailsSection"),
        ("storage_duplicates_section", "storageDuplicatesSection"),
        ("load_chart_section", "loadChartSection"),
    ):
        section = getattr(compact_tab, attribute)
        content = _content(compact_tab, object_name)
        original = section.isChecked()
        section.setChecked(not original)
        assert content.isHidden() is original
        section.setChecked(original)
        assert content.isHidden() is (not original)


def test_analyze_button_reveals_storage_details(compact_tab):
    compact_tab.storage_details_section.setChecked(False)
    with patch.object(metrics_tab_module, "run_in_background") as run_bg:
        compact_tab.storage_analyze_btn.click()
        run_bg.assert_called_once()
    assert compact_tab.storage_details_section.isChecked() is True
    assert _content(compact_tab, "storageDetailsSection").isHidden() is False


def test_storage_result_reveals_details_but_error_does_not(compact_tab):
    compact_tab.storage_details_section.setChecked(False)
    compact_tab._on_storage_done(_empty_result(), None)
    assert compact_tab.storage_details_section.isChecked() is True
    assert _content(compact_tab, "storageDetailsSection").isHidden() is False

    compact_tab.storage_details_section.setChecked(False)
    compact_tab._on_storage_done(None, RuntimeError("boom"))
    assert compact_tab.storage_details_section.isChecked() is False


# --- two compact columns ----------------------------------------------------


def test_charts_are_two_columns_of_at_least_300px(compact_tab):
    charts = _layout_at(compact_tab, QHBoxLayout)
    widgets = [charts.itemAt(i).widget() for i in range(charts.count())]
    assert compact_tab.load_chart_section in widgets
    assert compact_tab.cpu_chart_section in widgets
    assert compact_tab.load_chart_section.minimumWidth() >= 300
    assert compact_tab.cpu_chart_section.minimumWidth() >= 300
    assert compact_tab.load_plot.parentWidget() is not None
    assert compact_tab.cpu_plot.parentWidget() is not None


def _details_row(compact_tab: MetricsTab) -> QHBoxLayout:
    """The QHBoxLayout that places analysis details next to duplicate files."""
    storage_content = _content(compact_tab, "storageSection")
    layout = storage_content.layout()
    for index in range(layout.count()):
        candidate = layout.itemAt(index).layout()
        if not isinstance(candidate, QHBoxLayout):
            continue
        widgets = [candidate.itemAt(i).widget() for i in range(candidate.count())]
        if (
            compact_tab.storage_details_section in widgets
            and compact_tab.storage_duplicates_section in widgets
        ):
            return candidate
    raise AssertionError("Dettagli analisi e File duplicati non sono affiancati")


def test_details_and_duplicates_are_two_side_by_side_columns(compact_tab):
    row = _details_row(compact_tab)
    widgets = [row.itemAt(i).widget() for i in range(row.count())]
    assert widgets == [
        compact_tab.storage_details_section,
        compact_tab.storage_duplicates_section,
    ]
    assert compact_tab.storage_details_section.minimumWidth() >= 260
    assert compact_tab.storage_duplicates_section.minimumWidth() >= 260

    # The duplicate readout was extracted from "Dettagli analisi" into its own
    # section; folders and history stay in the analysis-details section.
    assert not compact_tab.storage_details_section.isAncestorOf(
        compact_tab.storage_duplicates_label
    )
    assert compact_tab.storage_duplicates_section.isAncestorOf(
        compact_tab.storage_duplicates_label
    )
    assert compact_tab.storage_details_section.isAncestorOf(
        compact_tab.storage_folders_label
    )
    assert compact_tab.storage_details_section.isAncestorOf(
        compact_tab.storage_trash_label
    )


def test_details_and_duplicates_are_open_by_default(compact_tab):
    for section, content_name in (
        (compact_tab.storage_details_section, "storageDetailsSection"),
        (compact_tab.storage_duplicates_section, "storageDuplicatesSection"),
    ):
        assert section.isChecked() is True
        assert _content(compact_tab, content_name).isHidden() is False


def test_summary_cards_stay_in_two_columns(compact_tab):
    cards = _layout_at(compact_tab, QGridLayout)

    def geometry(widget: QWidget):
        for index in range(cards.count()):
            if cards.itemAt(index).widget() is widget:
                return cards.getItemPosition(index)
        raise AssertionError(widget.objectName())

    load_box = compact_tab.load_label.parentWidget()
    cpu_box = compact_tab.cpu_label.parentWidget()
    assert geometry(load_box)[:2] == (0, 0)
    assert geometry(cpu_box)[:2] == (0, 1)

    for widget in (
        compact_tab.net_rx_label.parentWidget(),
        compact_tab.uptime_label.parentWidget(),
        compact_tab.activity_label.parentWidget(),
    ):
        assert isinstance(widget, QGroupBox)
        _row, column, _row_span, column_span = geometry(widget)
        assert (column, column_span) == (0, 2)


def test_group_box_margins_and_narrow_secondary_controls(compact_tab):
    def margins(widget: QWidget):
        m = widget.layout().contentsMargins()
        return (m.left(), m.top(), m.right(), m.bottom())

    for widget in (
        compact_tab.load_chart_section,
        compact_tab.storage_section,
        compact_tab.storage_details_section,
        compact_tab.findChild(QGroupBox, "storageFoldersBox"),
        compact_tab.net_rx_label.parentWidget(),
        compact_tab.activity_label.parentWidget(),
    ):
        assert margins(widget) == (6, 8, 6, 6)

    assert compact_tab.refresh_btn.maximumWidth() <= 120
    assert compact_tab.storage_analyze_btn.maximumWidth() <= 160


# --- preserved API ----------------------------------------------------------


def test_existing_attributes_object_names_and_methods_are_preserved(compact_tab):
    for name in (
        "sync_state_box", "sync_state_title", "sync_state_detail",
        "status_label", "refresh_btn", "load_plot", "cpu_plot",
        "load_label", "cpu_label", "memory_label", "swap_label", "disk_label",
        "io_read_label", "io_write_label", "net_rx_label", "net_tx_label",
        "net_interfaces_label", "uptime_label", "activity_label", "queue_label",
        "staging_label", "metadata_label", "storage_analyze_btn",
        "storage_status_label", "storage_progress", "storage_last_label",
        "storage_details_section", "storage_duplicates_section",
        "storage_folders_label", "storage_duplicates_label", "storage_trash_label",
    ):
        assert hasattr(compact_tab, name), name

    assert compact_tab.sync_state_box.objectName() == "syncStateBox"
    assert compact_tab.sync_state_title.objectName() == "syncStateTitle"
    assert compact_tab.sync_state_detail.objectName() == "syncStateDetail"

    for name in (
        "set_active", "stop", "hideEvent", "showEvent", "on_connection_changed",
        "_refresh_metrics", "_analyze_storage", "_maybe_auto_analyze",
        "_on_storage_done", "_display", "_set_sync_state",
    ):
        assert callable(getattr(compact_tab, name)), name
