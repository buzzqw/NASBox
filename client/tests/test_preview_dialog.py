"""Anteprima modifiche: tabella per cartella e azioni dirette."""
from __future__ import annotations

from sync_client import i18n
from sync_client.gui.preview_dialog import ChangePreviewDialog
from sync_client.gui.status_tab import StatusTab
from sync_client.config import Config
from tests.support import ClientEnvironment
from unittest.mock import MagicMock


def test_preview_dialog_lists_directories_and_anomalies(qtbot):
    i18n.set_language("it")
    data = {
        "ok": True,
        "summary": "cartelle anomale: OptAlarm 35/40 (88%)",
        "top_dirs": [
            {"directory": "OptAlarm", "missing": 35, "remote": 40},
            {"directory": "RPG", "missing": 2, "remote": 100},
        ],
        "anomalies": [{"directory": "OptAlarm", "missing": 35, "remote": 40, "ratio": 0.875}],
        "renames": ["old.txt → new.txt"],
        "deletions": ["gone.txt"],
    }
    dialog = ChangePreviewDialog(data)
    qtbot.addWidget(dialog)
    assert dialog.table.rowCount() == 2
    assert dialog.table.item(0, 0).text() == "OptAlarm"
    assert "anomalia" in dialog.table.item(0, 2).text()
    assert "ok" in dialog.table.item(1, 2).text()
    assert "OptAlarm" in dialog.summary_label.text()
    i18n.set_language(i18n.DEFAULT_LANG)


def test_preview_button_appears_only_with_a_safety_block(qtbot):
    with ClientEnvironment():
        cfg = Config()
        cfg.set("nas_lan", "192.168.1.119", persist=False)
        cfg.set("nas_user", "andres", persist=False)
        engine = MagicMock()
        engine.connection = None
        tab = StatusTab(cfg, engine, MagicMock())
        qtbot.addWidget(tab)
        status = {
            "connected": True, "configured": True, "host": "nas", "paused": False,
            "scheduler": {}, "pending_summary": {},
            "assurance": {
                "tracked_files": 10, "pending_count": 0, "queue_count": 0,
                "active_kind": "", "last_integrity_check_ts": 1,
                "last_integrity_mismatches": 0, "server_health": "ok",
                "repository_ready": True, "journal_ready": True,
            },
        }
        tab.on_status_changed(status)
        assert tab.preview_btn.isHidden()
        tab.on_log_event("SAFETY_BLOCK", "-", "blocco: 12 cancellazioni")
        assert not tab.preview_btn.isHidden()
