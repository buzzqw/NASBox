"""Full consistency audit: bounded, persistent and non-destructive."""
from __future__ import annotations

import time
from pathlib import Path

from sync_client import integrity_audit
from sync_client.config import Config
from sync_client.gui.status_tab import StatusTab
from sync_client.reconcile import RemoteKind, RemoteState
from sync_client.sync_state import Fingerprint
from tests.support import ClientEnvironment


def _entries():
    return {
        "ok.txt": RemoteState(RemoteKind.FILE, "a" * 64, 2, 1000, None),
        "missing.txt": RemoteState(RemoteKind.FILE, "b" * 64, 2, 1000, None),
        "wrong.txt": RemoteState(RemoteKind.FILE, "c" * 64, 2, 1000, None),
    }


def _fingerprint(path: Path):
    name = path.name
    if name == "ok.txt":
        return Fingerprint("a" * 64, 2, 1000)
    if name == "wrong.txt":
        return Fingerprint("d" * 64, 2, 1000)
    return None


def test_run_audit_reports_differences_and_keeps_them_bounded(tmp_path, monkeypatch):
    with ClientEnvironment():
        cfg = Config()
        cfg.set_local_root(str(tmp_path))
        monkeypatch.setattr(integrity_audit, "report_file", lambda: tmp_path / "audit.json")
        progress = []
        report = integrity_audit.run_audit(
            cfg,
            get_manifest=_entries,
            fingerprint=_fingerprint,
            progress=lambda done, total: progress.append((done, total)),
            batch_size=50,
        )
        assert report.status == "differences"
        assert report.files_checked == 3
        reasons = {(item["path"], item["reason"]) for item in report.differences}
        assert ("missing.txt", "missing_local") in reasons
        assert ("wrong.txt", "content_mismatch") in reasons
        assert ("ok.txt", "content_mismatch") not in reasons
        assert progress and progress[-1][0] == progress[-1][1]


def test_audit_ignores_excluded_paths(tmp_path, monkeypatch):
    with ClientEnvironment():
        cfg = Config()
        cfg.set_local_root(str(tmp_path))
        cfg.set_exclude_patterns(["sync-daemon/"])
        monkeypatch.setattr(integrity_audit, "report_file", lambda: tmp_path / "audit.json")
        entries = {
            "sync-daemon/client/main.py": RemoteState(RemoteKind.FILE, "a" * 64, 1, 1, None),
            "documenti/reale.txt": RemoteState(RemoteKind.FILE, "b" * 64, 1, 1, None),
        }
        report = integrity_audit.run_audit(
            cfg, get_manifest=lambda: entries,
            fingerprint=lambda path: Fingerprint("b" * 64, 1, 1),
        )
        assert report.status == "ok"
        assert report.files_checked == 1
        assert report.differences == []


def test_audit_detects_stale_pending_and_missing_local(tmp_path, monkeypatch):
    from unittest.mock import MagicMock

    with ClientEnvironment():
        cfg = Config()
        cfg.set_local_root(str(tmp_path))
        cfg.set_exclude_patterns(["sync-daemon/"])
        monkeypatch.setattr(integrity_audit, "report_file", lambda: tmp_path / "audit.json")
        (tmp_path / "presente.txt").write_text("x", encoding="utf-8")
        entries = {
            "presente.txt": RemoteState(RemoteKind.FILE, "a" * 64, 1, 1, None),
            "sparito.txt": RemoteState(RemoteKind.FILE, "b" * 64, 1, 1, None),
        }
        state = MagicMock()
        state.pending_paths.return_value = {"stale.txt"}
        state.all_entries.return_value = {
            "sparito.txt": Fingerprint("b" * 64, 1, 1),
            "stale.txt": Fingerprint("", -1, -1),
        }
        report = integrity_audit.run_audit(
            cfg, get_manifest=lambda: entries,
            fingerprint=lambda path: (
                Fingerprint("a" * 64, 1, 1) if Path(path).exists() else None
            ),
            state_store=state,
        )
        codes = {issue["code"] for issue in report.issues}
        assert "stale_pending" in codes
        assert "missing_local" in codes
        assert report.status in ("problems", "differences")
        assert report.checks["excluded_manifest_paths"] == 0


def test_report_history_is_persistent_and_bounded(tmp_path, monkeypatch):
    with ClientEnvironment():
        monkeypatch.setattr(integrity_audit, "report_file", lambda: tmp_path / "audit.json")
        for index in range(8):
            integrity_audit.save_report(
                integrity_audit.AuditReport(status="ok", started_at=index, finished_at=index + 1,
                                            files_checked=10)
            )
        history = integrity_audit.load_history()
        assert len(history) == integrity_audit.MAX_REPORTS
        assert history[-1].files_checked == 10


def test_audit_is_due_only_after_the_interval(tmp_path, monkeypatch):
    with ClientEnvironment():
        cfg = Config()
        cfg.set_local_root(str(tmp_path))
        cfg.set("integrity_audit_interval_days", 7, persist=False)
        monkeypatch.setattr(integrity_audit, "report_file", lambda: tmp_path / "audit.json")
        now = time.time()
        assert integrity_audit.is_due(cfg, now)
        integrity_audit.save_report(
            integrity_audit.AuditReport(status="ok", started_at=now, finished_at=now)
        )
        assert not integrity_audit.is_due(cfg, now + 3600)
        assert integrity_audit.is_due(cfg, now + 8 * 86400)


def test_status_tab_shows_last_audit(qtbot):
    from unittest.mock import MagicMock

    with ClientEnvironment():
        tab = StatusTab(Config(), MagicMock(), MagicMock())
        qtbot.addWidget(tab)
        tab._refresh_audit({"running": False, "last": {
            "status": "ok", "finished_at": time.time(), "files_checked": 42,
            "differences": 0, "duration_seconds": 3, "error": "",
        }})
        assert "42" in tab.audit_label.text()
        assert "0 differenze" in tab.audit_label.text()
