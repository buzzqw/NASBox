"""Guardia delete intelligente: per-cartella, rename-aware e con sentinelle."""
from __future__ import annotations

from types import SimpleNamespace

import pytest

from sync_client import delete_safety, i18n, rsync_ops
from sync_client.config import Config
from sync_client.reconcile import RemoteKind
from sync_client.repository_safety import RepositorySafetyError
from tests.support import ClientEnvironment


CONFIG_OUTPUT = "\n".join((
    "VERSION=3.24.0",
    "REPOSITORY_READY=true",
    "REPOSITORY_ID=repo1",
    "JOURNAL_READY=true",
    "PATH_RECONCILIATION_AVAILABLE=true",
    "SHARE_ROOT=/volume1/NASBox",
    "SYNC_LOCK_AVAILABLE=true",
))


@pytest.fixture(autouse=True)
def italian():
    i18n.set_language("it")
    yield
    i18n.set_language(i18n.DEFAULT_LANG)


@pytest.fixture
def guarded(tmp_path, monkeypatch):
    with ClientEnvironment():
        cfg = Config()
        cfg.set_local_root(str(tmp_path))
        cfg.set("repository_id", "repo1", persist=False)
        cfg.set("remote_prefix", "/volume1/NASBox", persist=False)
        cfg.set("remote_server_script", "/fake/server.sh", persist=False)
        cfg.set("delete_enabled", True, persist=False)
        cfg.set("max_delete_files", 1000, persist=False)
        cfg.set("delete_dir_guard_ratio", 0.5, persist=False)
        cfg.set("delete_dir_guard_min_files", 20, persist=False)
        cfg.set("delete_guard_classify", True, persist=False)
        cfg.set("delete_sentinels", "", persist=False)

        monkeypatch.setattr(rsync_ops, "validate_local_root", lambda *a, **k: None)
        monkeypatch.setattr(
            rsync_ops, "run_remote_script",
            lambda *a, **k: (True, CONFIG_OUTPUT, ""),
        )
        monkeypatch.setattr(
            delete_safety, "shared",
            lambda: SimpleNamespace(check=lambda cfg, count, direction: None),
        )
        yield cfg, tmp_path, monkeypatch


def _entries(prefix: str, count: int) -> dict:
    return {
        f"{prefix}/file{i}.txt": SimpleNamespace(
            kind=RemoteKind.FILE, digest=f"d{i}", size=10,
        )
        for i in range(count)
    }


def _state(identity=None):
    return SimpleNamespace(identity_snapshot=lambda: identity or {})


def test_directory_anomaly_blocks_with_explanation(guarded, monkeypatch):
    cfg, root, _ = guarded
    entries = _entries("OptAlarm", 40)
    # Five files still exist locally: only 35 of the 40 remote files are gone.
    for name in list(entries)[:5]:
        target = root / name
        target.parent.mkdir(parents=True, exist_ok=True)
        target.write_text("kept", encoding="utf-8")
    monkeypatch.setattr(rsync_ops, "cached_manifest", lambda *a, **k: entries)

    with pytest.raises(RepositorySafetyError) as exc:
        rsync_ops.validate_transfer_safety(
            cfg, SimpleNamespace(), destructive=True,
            direction="download", sync_state=_state(),
        )
    message = str(exc.value)
    assert "OptAlarm" in message
    assert "35" in message and "40" in message
    assert "Nessun file è stato modificato" in message


def test_renames_are_excluded_from_delete_count(guarded, monkeypatch):
    cfg, _root, _ = guarded
    entries = _entries("folder", 30)
    monkeypatch.setattr(rsync_ops, "cached_manifest", lambda *a, **k: entries)
    identity = {
        f"new/file{i}.txt": {"digest": f"d{i}", "size": 10, "ctime_ns": 1}
        for i in range(30)
    }
    # No exception: every missing path is explained by a rename.
    rsync_ops.validate_transfer_safety(
        cfg, SimpleNamespace(), destructive=True,
        direction="download", sync_state=_state(identity),
    )


def test_missing_sentinel_blocks_before_anything_else(guarded, monkeypatch):
    cfg, _root, _ = guarded
    cfg.set("delete_sentinels", ".nasbox-root", persist=False)
    entries = _entries("folder", 5)
    monkeypatch.setattr(rsync_ops, "cached_manifest", lambda *a, **k: entries)

    with pytest.raises(RepositorySafetyError) as exc:
        rsync_ops.validate_transfer_safety(
            cfg, SimpleNamespace(), destructive=True,
            direction="download", sync_state=_state(),
        )
    assert "sentinella" in str(exc.value)


def test_absolute_limit_message_includes_preview(guarded, monkeypatch):
    cfg, _root, _ = guarded
    cfg.set("max_delete_files", 3, persist=False)
    cfg.set("delete_dir_guard_min_files", 1000, persist=False)
    entries = _entries("folder", 10)
    monkeypatch.setattr(rsync_ops, "cached_manifest", lambda *a, **k: entries)

    with pytest.raises(RepositorySafetyError) as exc:
        rsync_ops.validate_transfer_safety(
            cfg, SimpleNamespace(), destructive=True,
            direction="upload", sync_state=_state(),
        )
    message = str(exc.value)
    assert "Anteprima" in message
    assert "10 cancellazioni sospette" in message
