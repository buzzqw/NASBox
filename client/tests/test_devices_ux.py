"""UX contract for the multi-PC activity feed in the 'Monitor NAS' tab.

The parent agent owns i18n.py, so the exact keys/placeholders consumed by this
widget are injected here for the duration of each test. That keeps the suite
deterministic and verifies the formatting contract even before the real
translations are merged into i18n.STRINGS.
"""
from __future__ import annotations

import time
from unittest.mock import MagicMock

import pytest
from PyQt6.QtWidgets import QGroupBox

from sync_client import i18n
from sync_client.config import Config
from sync_client.gui.metrics_tab import MetricsTab
from sync_client.i18n import t
from tests.support import ClientEnvironment

DEVICE_STRINGS = {
    "devices.title": {"it": "Dispositivi recenti", "en": "Recent devices"},
    "devices.empty": {"it": "Nessun dispositivo rilevato", "en": "No devices detected"},
    "devices.active": {"it": "attivo", "en": "active"},
    "devices.minutes_ago": {"it": "{minutes} min fa", "en": "{minutes} min ago"},
    "devices.row": {"it": "🖥 {host} ({status})", "en": "🖥 {host} ({status})"},
    "devices.refresh_btn": {"it": "Aggiorna dispositivi", "en": "Refresh devices"},
}


@pytest.fixture
def devices_tab(qapp, qtbot, tmp_path, monkeypatch):
    for key, entry in DEVICE_STRINGS.items():
        monkeypatch.setitem(i18n.STRINGS, key, entry)
    i18n.set_language("it")
    with ClientEnvironment():
        cfg = Config()
        root = tmp_path / "nasbox"
        root.mkdir()
        cfg.set_local_root(str(root))
        tab = MetricsTab(cfg, MagicMock())
        qtbot.addWidget(tab)
        yield tab
    i18n.set_language(i18n.DEFAULT_LANG)


def test_devices_section_is_a_compact_group_box_starting_empty(devices_tab):
    box = devices_tab.findChild(QGroupBox, "recentDevicesBox")
    assert box is not None
    assert box.title() == t("devices.title")
    assert devices_tab.devices_label.parentWidget() is box
    assert devices_tab.devices_label.text() == t("devices.empty")
    assert devices_tab.devices_refresh_btn.text() == t("devices.refresh_btn")


def test_set_devices_shows_two_devices_with_one_active(devices_tab):
    now = time.time()
    devices_tab.set_devices([
        {"device": "pc-122", "host": "PC-122", "last_seen": now, "active": True},
        {"device": "pc-134", "host": "PC-134", "last_seen": now - 7 * 60 - 5, "active": False},
    ])

    lines = devices_tab.devices_label.text().splitlines()
    assert lines == [
        t("devices.row", host="PC-122", status=t("devices.active")),
        t("devices.row", host="PC-134", status=t("devices.minutes_ago", minutes=7)),
    ]


def test_set_devices_accepts_none_empty_and_caps_at_five_rows(devices_tab):
    devices = [
        {"device": f"pc-{index}", "host": f"PC-{index}", "last_seen": 0, "active": True}
        for index in range(8)
    ]
    devices_tab.set_devices(devices)
    assert len(devices_tab.devices_label.text().splitlines()) == 5

    devices_tab.set_devices([])
    assert devices_tab.devices_label.text() == t("devices.empty")
    devices_tab.set_devices(devices[:1])
    devices_tab.set_devices(None)
    assert devices_tab.devices_label.text() == t("devices.empty")


def test_refresh_button_emits_devices_refresh_requested(devices_tab, qtbot):
    with qtbot.waitSignal(devices_tab.devices_refresh_requested, timeout=1000):
        devices_tab.devices_refresh_btn.click()
