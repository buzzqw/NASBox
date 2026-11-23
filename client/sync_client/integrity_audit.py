"""Verifiable, low-memory full-tree consistency audit.

A full audit is intentionally rare. It compares the authoritative NAS manifest
against the local tree in bounded batches, never holds the transfer lease and
writes a durable report so the answer to "is everything synced?" is a dated
fact rather than an impression.
"""
from __future__ import annotations

import json
import os
import tempfile
import threading
import time
from dataclasses import asdict, dataclass, field
from pathlib import Path
from typing import Callable, Iterable, Mapping

from . import paths as client_paths
from .config import Config
from .reconcile import RemoteKind, RemoteState
from .version import APP_VERSION

MAX_REPORTS = 5
DEFAULT_INTERVAL_DAYS = 7
DEFAULT_BATCH_SIZE = 250


@dataclass
class AuditReport:
    status: str = "unknown"          # ok | differences | error
    started_at: float = 0.0
    finished_at: float = 0.0
    duration_seconds: float = 0.0
    files_checked: int = 0
    differences: list[dict[str, str]] = field(default_factory=list)
    manifest_revision: int = -1
    client_version: str = APP_VERSION
    error: str = ""

    @property
    def ok(self) -> bool:
        return self.status == "ok"


def report_file() -> Path:
    return client_paths.state_dir() / "integrity-audit.json"


def load_history() -> list[AuditReport]:
    try:
        data = json.loads(report_file().read_text(encoding="utf-8"))
        raw = data.get("reports", [])
        return [AuditReport(**item) for item in raw[-MAX_REPORTS:]]
    except (OSError, ValueError, TypeError):
        return []


def save_report(report: AuditReport) -> list[AuditReport]:
    history = [item for item in load_history() if item.started_at != report.started_at]
    history.append(report)
    history = history[-MAX_REPORTS:]
    target = report_file()
    target.parent.mkdir(parents=True, exist_ok=True)
    fd, temp_name = tempfile.mkstemp(prefix=".integrity-audit-", dir=str(target.parent))
    try:
        with os.fdopen(fd, "w", encoding="utf-8") as stream:
            json.dump({"reports": [asdict(item) for item in history]}, stream)
        os.replace(temp_name, target)
    finally:
        if os.path.exists(temp_name):
            os.unlink(temp_name)
    return history


def interval_seconds(cfg: Config) -> float:
    try:
        days = float(cfg.get("integrity_audit_interval_days") or DEFAULT_INTERVAL_DAYS)
    except (TypeError, ValueError):
        days = float(DEFAULT_INTERVAL_DAYS)
    return max(3600.0, days * 86400)


def is_due(cfg: Config, now: float | None = None) -> bool:
    if not bool(cfg.get("integrity_audit_enabled", True)):
        return False
    now = time.time() if now is None else now
    last = 0.0
    for report in load_history():
        last = max(last, report.finished_at)
    return now - last >= interval_seconds(cfg)


def _manifest_entries(
    cfg: Config, get_manifest: Callable[[], Mapping[str, RemoteState] | None] | None,
) -> tuple[dict[str, RemoteState], int]:
    """Return manifest entries and their revision, without holding any lock."""
    if get_manifest is not None:
        entries = get_manifest() or {}
        return dict(entries), int(cfg.get("last_manifest_revision") or -1)
    # Late import: rsync_ops imports this module indirectly through the engine.
    from . import rsync_ops

    script = str(cfg.get("remote_server_script") or "").strip()
    if not script:
        return {}, -1
    ok, status, _error = rsync_ops.run_remote_script(
        cfg, None, script, ["--journal-status"], timeout=30,
    )
    revision = -1
    if ok:
        for line in status.splitlines():
            key, separator, value = line.partition("=")
            if separator and key.strip() == "MANIFEST_REVISION":
                try:
                    revision = int(value.strip())
                except ValueError:
                    pass
    ok, output, _error = rsync_ops.run_remote_script(
        cfg, None, script, ["--manifest-export"], timeout=180,
    )
    if not ok:
        return {}, revision
    rows = rsync_ops._parse_manifest_rows(
        output.encode("utf-8", errors="replace").splitlines()[1:]
    )
    return dict(rows or {}), revision


def run_audit(
    cfg: Config,
    *,
    get_manifest: Callable[[], Mapping[str, RemoteState] | None] | None = None,
    fingerprint: Callable[[Path], object] | None = None,
    progress: Callable[[int, int], None] | None = None,
    cancel: Callable[[], bool] | None = None,
    batch_size: int | None = None,
) -> AuditReport:
    """Compare every tracked NAS file with the local copy, in bounded batches.

    ``get_manifest`` and ``fingerprint`` are injectable for tests; in production
    the callers pass the shared manifest cache and SyncStateStore.fingerprint.
    """
    started = time.time()
    report = AuditReport(status="error", started_at=started)
    try:
        entries, revision = _manifest_entries(cfg, get_manifest)
        report.manifest_revision = revision
        if not entries:
            report.error = "manifest NAS non disponibile per l'audit"
            return report

        root = Path(cfg.local_root())
        try:
            size = max(50, int(cfg.get("integrity_audit_batch_size") or DEFAULT_BATCH_SIZE))
        except (TypeError, ValueError):
            size = DEFAULT_BATCH_SIZE
        if batch_size:
            size = max(50, int(batch_size))

        if fingerprint is None:
            from .sync_state import SyncStateStore

            fingerprint = SyncStateStore(cfg).fingerprint

        from . import rsync_ops

        # Excluded trees (notably the repo checkout under sync-daemon/) are
        # intentionally not mirrored: comparing them produces hundreds of
        # phantom differences and keeps the dashboard permanently on "attention".
        candidates = [
            (path, state) for path, state in entries.items()
            if state.kind == RemoteKind.FILE
            and path and not rsync_ops.path_is_excluded(cfg, path)
        ]
        total = len(candidates)
        checked = 0
        for index, (relative_path, state) in enumerate(candidates, start=1):
            if cancel is not None and cancel():
                report.error = "audit interrotto"
                return report
            if not relative_path or not state.digest:
                continue
            local = Path(root, relative_path)
            fp = fingerprint(local)
            if fp is None:
                report.differences.append({"path": relative_path, "reason": "missing_local"})
            elif getattr(fp, "digest", None) != state.digest:
                report.differences.append({"path": relative_path, "reason": "content_mismatch"})
            checked += 1
            if progress is not None and index % size == 0:
                progress(index, total)
        if progress is not None:
            progress(total, total)
        report.files_checked = checked
        report.status = "differences" if report.differences else "ok"
    except Exception as exc:  # a rare audit must never break sync
        report.error = str(exc)
        report.status = "error"
    finally:
        report.finished_at = time.time()
        report.duration_seconds = max(0.0, report.finished_at - report.started_at)
    return report
