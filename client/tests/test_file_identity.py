"""Identità filesystem locale usata per spiegare rename e cancellazioni."""
from __future__ import annotations

from sync_client.config import Config
from sync_client.sync_state import SyncStateStore
from tests.support import ClientEnvironment


def test_identity_snapshot_survives_a_new_store(tmp_path):
    with ClientEnvironment():
        cfg = Config()
        cfg.set_local_root(str(tmp_path))
        cfg.set("repository_id", "repo1", persist=False)
        target = tmp_path / "file.txt"
        target.write_text("content", encoding="utf-8")

        store = SyncStateStore(cfg)
        store.record_identities(str(tmp_path), ["file.txt"], {"file.txt": "deadbeef"})
        snapshot = store.identity_snapshot()
        assert snapshot["file.txt"]["digest"] == "deadbeef"
        assert snapshot["file.txt"]["size"] == len("content")
        assert snapshot["file.txt"]["ino"] > 0

        # A fresh store (new process) reads the same identity from SQLite.
        reopened = SyncStateStore(cfg)
        assert reopened.identity_snapshot()["file.txt"]["digest"] == "deadbeef"


def test_backfill_records_tree_without_hashing(tmp_path):
    with ClientEnvironment():
        cfg = Config()
        cfg.set_local_root(str(tmp_path))
        store = SyncStateStore(cfg)
        for name in ("a.txt", "sub/b.txt", "sub/c.bin"):
            target = tmp_path / name
            target.parent.mkdir(parents=True, exist_ok=True)
            target.write_bytes(b"x")
        count = store.backfill_identities(str(tmp_path))
        assert count == 3
        snapshot = store.identity_snapshot()
        assert set(snapshot) == {"a.txt", "sub/b.txt", "sub/c.bin"}
        assert all(item["ino"] > 0 for item in snapshot.values())


def test_identity_does_not_hash_missing_paths(tmp_path):
    with ClientEnvironment():
        cfg = Config()
        cfg.set_local_root(str(tmp_path))
        store = SyncStateStore(cfg)
        store.record_identities(str(tmp_path), ["gone.txt"], {"gone.txt": "abc"})
        assert "gone.txt" not in store.identity_snapshot()
