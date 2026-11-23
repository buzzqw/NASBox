"""Shared manifest cache: the push must not re-queue applied deletions."""
from __future__ import annotations

from sync_client import rsync_ops
from sync_client.config import Config
from sync_client.reconcile import RemoteKind, RemoteState
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
