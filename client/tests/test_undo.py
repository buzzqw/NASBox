"""Undo stile Rewind: dal journal locale al cestino del NAS."""
from __future__ import annotations

import subprocess

import pytest

from sync_client import rsync_ops
from sync_client.config import Config
from sync_client.sync_state import SyncStateStore
from tests.support import ClientEnvironment, ServerSandbox


def test_undo_event_lifecycle(tmp_path):
    with ClientEnvironment():
        cfg = Config()
        cfg.set_local_root(str(tmp_path))
        cfg.set("repository_id", "repo1", persist=False)
        store = SyncStateStore(cfg)
        store.record_undo(["a.txt", "folder/b.txt"], "2026-11-24--07-00-00-000001Z")
        store.record_undo(["c.txt"], "2026-11-24--08-00-00-000001Z")

        assert store.undo_count() == 3
        recent = store.recent_undo()
        assert {item["path"] for item in recent} == {"a.txt", "folder/b.txt", "c.txt"}

        store.mark_undo_applied("2026-11-24--07-00-00-000001Z", ["a.txt"])
        assert store.undo_count() == 2


def test_restore_batch_parses_statuses(monkeypatch):
    with ClientEnvironment():
        cfg = Config()
        cfg.set("remote_server_script", "/fake/server.sh", persist=False)
        payload = b"RESTORE_BATCH_V1\x002\x00a.txt\x00RESTORED\x00b.txt\x00NO_VERSION\x00"
        monkeypatch.setattr(
            rsync_ops, "run_remote_script_input_bytes",
            lambda *a, **k: (True, payload, ""),
        )
        result = rsync_ops.restore_batch(
            cfg, rsync_ops.NasConnection("nas"),
            [("a.txt", "latest"), ("b.txt", "latest")], "device-a",
        )
    assert result == {"a.txt": "RESTORED", "b.txt": "NO_VERSION"}


def test_server_restore_batch_moves_from_trash_and_updates_manifest():
    with ServerSandbox() as sandbox:
        sandbox.init_repository()
        run_ts = "2026-11-24--07-00-00-000001Z"
        trashed = sandbox.share / ".sync-trash" / "folder" / f"old.txt-{run_ts}"
        trashed.parent.mkdir(parents=True, exist_ok=True)
        trashed.write_bytes(b"restored content")

        payload = b"\0".join((
            b"RESTORE_BATCH_V1", b"device-a", b"1",
            b"folder/old.txt", run_ts.encode(),
        )) + b"\0"
        result = sandbox.run_with_lease("--restore-batch", input=payload)
        exported = sandbox.run("--manifest-export")
        target = sandbox.share / "folder" / "old.txt"
        assert result.returncode == 0, result.stderr.decode(errors="replace")
        assert b"\0RESTORED\0" in result.stdout, result.stdout
        assert target.read_bytes() == b"restored content"
        assert b"folder/old.txt\t" in exported.stdout
        assert b"RESTORE_BATCH_V1\x001\x00folder/old.txt\x00RESTORED" in result.stdout


def test_server_restore_batch_requires_the_global_lease():
    with ServerSandbox() as sandbox:
        sandbox.init_repository()
        trashed = sandbox.share / ".sync-trash" / f"file.txt-2026-11-24--07-00-00-000001Z"
        trashed.parent.mkdir(parents=True, exist_ok=True)
        trashed.write_bytes(b"x")
        payload = b"\0".join((
            b"RESTORE_BATCH_V1", b"device-a", b"1",
            b"file.txt", b"2026-11-24--07-00-00-000001Z",
        )) + b"\0"
        unlocked = sandbox.run("--restore-batch", input=payload)
        preserved = trashed.exists()
    assert unlocked.returncode == 75
    assert preserved
