"""Shared manifest cache: the push must not re-queue applied deletions."""
from __future__ import annotations

import hashlib
from pathlib import Path

from sync_client import rsync_ops
from sync_client.config import Config
from sync_client.reconcile import RemoteKind, RemoteState
from sync_client.sync_state import Fingerprint, SyncStateStore
from tests.support import ClientEnvironment


def test_manifest_cache_is_shared_and_expires(tmp_path):
    with ClientEnvironment():
        cfg = Config()
        cfg.set_local_root(str(tmp_path))
        assert rsync_ops.cached_manifest(cfg) is None
        entries = {"a.txt": RemoteState(RemoteKind.FILE, "a" * 64, 1, 1, None)}
        rsync_ops.cache_manifest(cfg, 42, entries)
        assert rsync_ops.cached_manifest(cfg) == entries
        # A stale cache must never be trusted for planning.
        assert rsync_ops.cached_manifest(cfg, max_age_seconds=-1) is None


def test_changed_paths_single_pass_detects_changes_and_tombstones(tmp_path):
    with ClientEnvironment():
        cfg = Config()
        cfg.set_local_root(str(tmp_path))
        state = SyncStateStore(cfg)
        (tmp_path / "same.txt").write_text("same", encoding="utf-8")
        (tmp_path / "changed.txt").write_text("old", encoding="utf-8")
        (tmp_path / "deleted.txt").write_text("gone", encoding="utf-8")
        (tmp_path / "already_gone.txt").write_text("removed", encoding="utf-8")

        def fp(name):
            p = tmp_path / name
            return Fingerprint(hashlib.sha256(p.read_bytes()).hexdigest(), p.stat().st_size, p.stat().st_mtime_ns)

        state.record_fingerprints({
            "same.txt": fp("same.txt"),
            "changed.txt": fp("changed.txt"),
            "deleted.txt": fp("deleted.txt"),
            "already_gone.txt": fp("already_gone.txt"),
        })
        # A tombstone baseline must stay invisible once the file is gone.
        (tmp_path / "already_gone.txt").unlink()
        state.record_fingerprints({"already_gone.txt": None})

        (tmp_path / "changed.txt").write_text("brand new content", encoding="utf-8")
        (tmp_path / "deleted.txt").unlink()
        (tmp_path / "new.txt").write_text("new", encoding="utf-8")

        changed = state.changed_paths(str(tmp_path))
        assert "changed.txt" in changed
        assert "deleted.txt" in changed
        assert "new.txt" in changed
        assert "same.txt" not in changed
        assert "already_gone.txt" not in changed
