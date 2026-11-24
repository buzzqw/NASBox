"""Messaggi di stato guidati: il banner deve dire cosa fare, non solo che c'è un problema."""
from __future__ import annotations

import os
import subprocess
import sys
import time
from unittest.mock import MagicMock

import pytest

from sync_client import i18n
from sync_client.config import Config
from sync_client.delete_safety import DeleteSafety
from sync_client.gui.overview_tab import OverviewTab
from sync_client.gui.status_tab import StatusTab
from tests.support import ClientEnvironment


ASSURANCE = {
    "tracked_files": 100,
    "pending_count": 0,
    "queue_count": 0,
    "active_kind": "",
    "last_integrity_check_ts": time.time() - 60,
    "last_integrity_files": 100,
    "last_integrity_mismatches": 0,
    "last_integrity_error": "",
    "manifest_revision": 7,
    "server_health": "ok",
    "repository_ready": True,
    "journal_ready": True,
    "delete_used": 0,
    "delete_limit": 1000,
}


@pytest.fixture(autouse=True)
def italian():
    i18n.set_language("it")
    yield
    i18n.set_language(i18n.DEFAULT_LANG)


def _status(**overrides):
    data = {
        "connected": True,
        "configured": True,
        "paused": False,
        "host": "nas",
        "scheduler": {},
        "pending_summary": {},
        "assurance": dict(ASSURANCE),
    }
    data.update(overrides)
    return data


@pytest.fixture
def status_tab(qtbot):
    with ClientEnvironment():
        cfg = Config()
        cfg.set("nas_lan", "192.168.1.119", persist=False)
        cfg.set("nas_user", "andres", persist=False)
        engine = MagicMock()
        engine.connection = None
        tab = StatusTab(cfg, engine, MagicMock())
        qtbot.addWidget(tab)
        yield tab


def test_clean_state_has_no_guidance_and_no_override_button(status_tab):
    status_tab.on_status_changed(_status())
    assert status_tab.assurance_guidance.isHidden()
    assert status_tab.guidance_btn.isHidden()
    assert status_tab.confirm_deletes_btn.isHidden()
    assert "Tutto sincronizzato" in status_tab.assurance_headline.text()


def test_safety_block_gives_numbered_guidance_and_shows_override(status_tab, qtbot):
    status_tab.on_log_event(
        "SAFETY_BLOCK", "-",
        "operazione bloccata: 8739 cancellazioni proposte, limite di sicurezza 1000",
    )
    status = _status()
    status["assurance"] = dict(ASSURANCE, queue_count=3, last_integrity_check_ts=0)
    status_tab.on_status_changed(status)

    headline = status_tab.assurance_headline.text()
    guidance = status_tab.assurance_guidance.text()
    assert "Attenzione" in headline
    assert "cancellazioni di massa bloccate" in headline
    assert "8739" in guidance
    assert "Nessun file è stato modificato" in guidance
    assert "Conferma cancellazioni di massa" in guidance
    assert not status_tab.assurance_guidance.isHidden()
    assert not status_tab.confirm_deletes_btn.isHidden()
    assert "Log" in status_tab.guidance_btn.text()

    with qtbot.waitSignal(status_tab.attention_action_requested) as signal:
        status_tab.guidance_btn.click()
    assert signal.args == ["log"]


def test_queue_only_points_to_log_and_transfers(status_tab, qtbot):
    status = _status()
    status["assurance"] = dict(ASSURANCE, queue_count=3)
    status_tab.on_status_changed(status)
    assert "operazioni in coda" in status_tab.assurance_headline.text()
    assert "3 operazioni in coda" in status_tab.assurance_guidance.text()
    assert status_tab.confirm_deletes_btn.isHidden()

    with qtbot.waitSignal(status_tab.attention_action_requested) as signal:
        status_tab.guidance_btn.click()
    assert signal.args == ["transfers"]


def test_missing_integrity_check_offers_verify_action(status_tab):
    status = _status()
    status["assurance"] = dict(ASSURANCE, last_integrity_check_ts=0)
    status_tab.on_status_changed(status)
    assert "Verifica adesso" in status_tab.assurance_guidance.text()
    status_tab.guidance_btn.click()
    status_tab.engine.verify_integrity_now.assert_called_once()


def test_confirm_override_button_is_only_visible_while_blocked(status_tab):
    status_tab.on_log_event("SAFETY_BLOCK", "-", "blocco: 12 cancellazioni")
    status_tab.on_status_changed(_status())
    assert not status_tab.confirm_deletes_btn.isHidden()
    # A later status refresh must not hide it while the block is still recent.
    status_tab.on_status_changed(_status())
    assert not status_tab.confirm_deletes_btn.isHidden()


def test_i18n_imports_without_pyqt6_for_headless_tools():
    client_root = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
    code = (
        "import sys\n"
        "class Blocker:\n"
        "    def find_spec(self, fullname, path=None, target=None):\n"
        "        if fullname == 'PyQt6' or fullname.startswith('PyQt6.'):\n"
        "            raise ImportError(fullname)\n"
        "sys.meta_path.insert(0, Blocker())\n"
        "import sync_client.i18n as i18n\n"
        "assert i18n.QLocale is None\n"
        "assert i18n.t('assurance.guidance_title')\n"
    )
    env = dict(os.environ, PYTHONPATH=client_root)
    result = subprocess.run(
        [sys.executable, "-c", code], env=env, capture_output=True, text=True,
    )
    assert result.returncode == 0, result.stderr


def test_delete_safety_error_is_guided(tmp_path):
    with ClientEnvironment():
        cfg = Config()
        cfg.set("delete_enabled", True, persist=False)
        safety = DeleteSafety(tmp_path / "delete-safety.json")
        with pytest.raises(Exception) as exc:
            safety.check(cfg, 2000, "download")
        message = str(exc.value)
    assert "Nessun file è stato modificato" in message
    assert "Conferma cancellazioni di massa" in message
    assert "2000" in message


def test_overview_adds_next_step_line(qtbot):
    tab = OverviewTab()
    qtbot.addWidget(tab)
    status = {
        "connected": True,
        "paused": False,
        "assurance": dict(ASSURANCE, queue_count=3),
        "audit": {"running": False, "last": {}},
        "devices": [],
    }
    tab.set_status(status)
    assert "Cosa fare" in tab.guidance_label.text()
    assert "controlla il Log" in tab.guidance_label.text()

    status["assurance"] = dict(ASSURANCE)
    tab.set_status(status)
    assert tab.guidance_label.text() == ""
