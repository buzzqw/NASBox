"""Tests for the compact, single-screen Panoramica tab.

The overview keys are injected into the i18n table for the duration of these
tests: the module under test must use ``t("overview.*")`` for every string, and
the assertions below must not depend on whether i18n.py has been updated yet.
"""
from __future__ import annotations

import time

import pytest
from PyQt6.QtWidgets import QScrollArea

from sync_client import i18n
from sync_client.gui.overview_tab import OverviewTab

# Keep this table identical to the it/en list reported with the new tab.
OVERVIEW_STRINGS = {
    "overview.banner_ok": {"it": "✅ Tutto sincronizzato", "en": "✅ All synced"},
    "overview.banner_warning": {"it": "⚠ Attenzione", "en": "⚠ Attention"},
    "overview.banner_progress": {"it": "⏳ In corso", "en": "⏳ In progress"},
    "overview.banner_offline": {"it": "🔴 Offline", "en": "🔴 Offline"},
    "overview.stat_tracked": {"it": "File tracciati", "en": "Tracked files"},
    "overview.stat_queue": {"it": "In coda", "en": "Queued"},
    "overview.stat_conflicts": {"it": "Conflitti", "en": "Conflicts"},
    "overview.stat_audit": {"it": "Ultimo audit", "en": "Last audit"},
    "overview.last_check": {
        "it": "Ultima verifica checksum: {value}",
        "en": "Last checksum check: {value}",
    },
    "overview.devices": {"it": "Dispositivi: {value}", "en": "Devices: {value}"},
    "overview.open_details": {"it": "Apri dettagli", "en": "Open details"},
    "overview.never": {"it": "Mai", "en": "Never"},
    "overview.no_devices": {"it": "Nessuno", "en": "None"},
    "overview.audit_running": {"it": "In corso…", "en": "Running…"},
    "overview.audit_summary": {
        "it": "{count} differenze · {when}",
        "en": "{count} differences · {when}",
    },
    "overview.check_mismatch": {
        "it": "{count} differenze",
        "en": "{count} mismatches",
    },
    "overview.age_seconds": {"it": "{count} secondi fa", "en": "{count} seconds ago"},
    "overview.age_minutes": {"it": "{count} minuti fa", "en": "{count} minutes ago"},
    "overview.age_hours": {"it": "{count} ore fa", "en": "{count} hours ago"},
    "overview.age_days": {"it": "{count} giorni fa", "en": "{count} days ago"},
}


@pytest.fixture
def overview_strings(monkeypatch):
    for key, entry in OVERVIEW_STRINGS.items():
        monkeypatch.setitem(i18n.STRINGS, key, dict(entry))
    i18n.set_language("it")
    yield
    i18n.set_language(i18n.DEFAULT_LANG)


@pytest.fixture
def overview_tab(qtbot, overview_strings):
    tab = OverviewTab()
    qtbot.addWidget(tab)
    return tab


def _status(**overrides) -> dict:
    now = int(time.time())
    status = {
        "connected": True,
        "paused": False,
        "assurance": {
            "tracked_files": 12,
            "pending_count": 0,
            "queue_count": 0,
            "active_kind": "",
            "last_integrity_check_ts": now - 60,
            "last_integrity_files": 12,
            "last_integrity_mismatches": 0,
            "manifest_revision": 7,
            "server_health": "ok",
            "repository_ready": True,
            "journal_ready": True,
        },
        "audit": {
            "running": False,
            "last": {
                "status": "ok",
                "files_checked": 12,
                "differences": 0,
                "finished_at": now - 120,
            },
        },
        "devices": [{"host": "pc-122"}, {"host": "pc-134"}],
    }
    status.update(overrides)
    return status


def test_banner_ok_when_everything_is_clean(overview_tab):
    overview_tab.set_status(_status())
    assert overview_tab.banner.text() == "✅ Tutto sincronizzato"
    assert overview_tab.banner.property("state") == "ok"


def test_banner_warning_when_files_are_pending(overview_tab):
    status = _status()
    status["assurance"]["pending_count"] = 3
    overview_tab.set_status(status)
    assert overview_tab.banner.text() == "⚠ Attenzione"
    assert overview_tab.banner.property("state") == "warning"
    assert overview_tab.stat_labels["queue"].text() == "0"


def test_banner_progress_when_a_transfer_is_active(overview_tab):
    status = _status()
    status["assurance"]["active_kind"] = "upload"
    overview_tab.set_status(status)
    assert overview_tab.banner.text() == "⏳ In corso"
    assert overview_tab.banner.property("state") == "progress"


def test_banner_progress_when_the_audit_is_running(overview_tab):
    status = _status()
    status["audit"]["running"] = True
    overview_tab.set_status(status)
    assert overview_tab.banner.text() == "⏳ In corso"
    assert overview_tab.stat_labels["audit"].text() == "In corso…"


def test_banner_offline_has_priority(overview_tab):
    status = _status(connected=False)
    status["assurance"]["pending_count"] = 5
    status["assurance"]["active_kind"] = "download"
    overview_tab.set_status(status)
    assert overview_tab.banner.text() == "🔴 Offline"
    assert overview_tab.banner.property("state") == "offline"


def test_banner_warning_on_checksum_mismatch_and_audit_differences(overview_tab):
    status = _status()
    status["assurance"]["last_integrity_mismatches"] = 2
    overview_tab.set_status(status)
    assert overview_tab.banner.text() == "⚠ Attenzione"

    status = _status()
    status["audit"]["last"]["status"] = "differences"
    status["audit"]["last"]["differences"] = 1
    overview_tab.set_status(status)
    assert overview_tab.banner.text() == "⚠ Attenzione"


def test_banner_warning_when_paused_or_unhealthy(overview_tab):
    overview_tab.set_status(_status(paused=True))
    assert overview_tab.banner.text() == "⚠ Attenzione"

    status = _status()
    status["assurance"]["server_health"] = "degraded"
    overview_tab.set_status(status)
    assert overview_tab.banner.text() == "⚠ Attenzione"


def test_numeric_tiles_and_audit_summary(overview_tab):
    status = _status(conflict_count=2)
    status["assurance"]["tracked_files"] = 42
    status["assurance"]["queue_count"] = 7
    status["audit"]["last"] = {
        "status": "differences",
        "files_checked": 40,
        "differences": 1,
        "finished_at": int(time.time()) - 2 * 86400 - 5,
    }
    overview_tab.set_status(status)
    assert overview_tab.stat_labels["tracked"].text() == "42"
    assert overview_tab.stat_labels["queue"].text() == "7"
    assert overview_tab.stat_labels["conflicts"].text() == "2"
    assert overview_tab.stat_labels["audit"].text() == "1 differenze · 2 giorni fa"


def test_conflict_count_signal_updates_the_tile_and_is_sticky(overview_tab):
    overview_tab.set_status(_status())
    assert overview_tab.stat_labels["conflicts"].text() == "0"
    overview_tab.on_conflict_count(4)
    assert overview_tab.stat_labels["conflicts"].text() == "4"
    # A later status that omits the conflict count must not silently reset it.
    overview_tab.set_status(_status())
    assert overview_tab.stat_labels["conflicts"].text() == "4"
    assert overview_tab.banner.text() == "⚠ Attenzione"


def test_checksum_and_devices_rows(overview_tab):
    status = _status()
    status["assurance"]["last_integrity_check_ts"] = int(time.time()) - 3 * 3600 - 5
    overview_tab.set_status(status)
    assert overview_tab.last_check_label.text() == (
        "Ultima verifica checksum: 0 differenze · 3 ore fa"
    )
    assert overview_tab.devices_label.text() == "Dispositivi: 2"

    overview_tab.set_status(_status(devices=[]))
    assert overview_tab.devices_label.text() == "Dispositivi: Nessuno"


def test_empty_and_malformed_status_never_crashes(overview_tab):
    overview_tab.set_status({})
    assert overview_tab.banner.text() == "🔴 Offline"
    assert overview_tab.stat_labels["tracked"].text() == "0"
    assert overview_tab.stat_labels["audit"].text() == "Mai"
    assert overview_tab.last_check_label.text() == "Ultima verifica checksum: Mai"

    overview_tab.set_status({
        "connected": 1, "assurance": None, "audit": "broken", "devices": 5,
    })
    assert overview_tab.banner.text() == "⚠ Attenzione"
    assert overview_tab.devices_label.text() == "Dispositivi: Nessuno"
    assert "overview." not in overview_tab.banner.text()


def test_details_button_emits_the_public_signal(overview_tab, qtbot):
    with qtbot.waitSignal(overview_tab.open_details_requested, timeout=1000):
        overview_tab.details_button.click()


def test_layout_is_fixed_and_has_no_scroll_area(overview_tab):
    assert overview_tab.findChildren(QScrollArea) == []
    assert overview_tab.banner.minimumHeight() >= 40
    # One screen: banner + tiles + two rows + button, no scrollable content.
    assert overview_tab.sizeHint().height() < 400
