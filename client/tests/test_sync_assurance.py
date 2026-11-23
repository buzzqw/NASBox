from __future__ import annotations

import time
from unittest.mock import MagicMock

from sync_client.config import Config
from sync_client.gui.status_tab import StatusTab
from tests.support import ClientEnvironment


ASSURANCE = {
    "tracked_files": 1234,
    "pending_count": 0,
    "queue_count": 0,
    "active_kind": "",
    "last_integrity_check_ts": time.time() - 60,
    "last_integrity_files": 1234,
    "last_integrity_mismatches": 0,
    "last_integrity_error": "",
    "manifest_revision": 42,
    "server_health": "ok",
    "repository_ready": True,
    "journal_ready": True,
    "delete_used": 0,
    "delete_limit": 1000,
}


def _status(**overrides):
    data = {
        "paused": False,
        "connected": True,
        "configured": True,
        "host": "nas",
        "server_health": "ok",
        "scheduler": {},
        "pending_summary": {},
        "assurance": dict(ASSURANCE),
    }
    data.update(overrides)
    return data


def test_assurance_card_reports_all_synced(qtbot):
    with ClientEnvironment():
        engine = MagicMock()
        engine.connection = None
        cfg = Config()
        cfg.set("nas_lan", "192.168.1.119", persist=False)
        cfg.set("nas_user", "andres", persist=False)
        tab = StatusTab(cfg, engine, MagicMock())
        qtbot.addWidget(tab)
        tab.on_status_changed(_status())
        assert "Tutto sincronizzato" in tab.assurance_headline.text()
        assert tab.assurance_headline.property("state") == "ok"
        assert "1234 file confrontati" in tab.assurance_proof.text()
        assert "0 differenze" in tab.assurance_proof.text()
        assert tab.assurance_stats["tracked"].text() == "1234"
        assert tab.assurance_stats["revision"].text() == "42"
        assert "✓" in tab.assurance_checks.text()
        assert "✗" not in tab.assurance_checks.text()


def test_assurance_card_reports_pending_and_errors(qtbot):
    with ClientEnvironment():
        engine = MagicMock()
        engine.connection = None
        cfg = Config()
        cfg.set("nas_lan", "192.168.1.119", persist=False)
        cfg.set("nas_user", "andres", persist=False)
        tab = StatusTab(cfg, engine, MagicMock())
        qtbot.addWidget(tab)
        status = _status()
        status["assurance"] = dict(ASSURANCE, pending_count=7, last_integrity_mismatches=2)
        tab.on_status_changed(status)
        assert "Attenzione" in tab.assurance_headline.text()
        assert "2 differenze" in tab.assurance_proof.text()
        assert "✗" in tab.assurance_checks.text()
        assert tab.assurance_stats["pending"].text() == "7"


def test_verify_now_triggers_engine(qtbot):
    with ClientEnvironment():
        engine = MagicMock()
        engine.connection = None
        tab = StatusTab(Config(), engine, MagicMock())
        qtbot.addWidget(tab)
        tab._verify_now()
        engine.verify_integrity_now.assert_called_once()
