"""Stale local-deletion queue entries must not defer every pull forever."""
from __future__ import annotations

import threading
from unittest.mock import MagicMock

from sync_client.config import Config
from sync_client.pull_worker import PullWorker
from sync_client.reconcile import RemoteKind, RemoteState
from tests.support import ClientEnvironment


def test_already_applied_deletions_are_cleared_from_pending(tmp_path):
    with ClientEnvironment():
        cfg = Config()
        cfg.set_local_root(str(tmp_path))
        cfg.set("delete_enabled", True, persist=False)
        state = MagicMock()
        state.pending_paths.return_value = {"gone.txt"}
        worker = PullWorker(
            cfg, MagicMock(), MagicMock(), threading.Lock(), state,
        )
        (tmp_path / "still_here.txt").write_text("x", encoding="utf-8")
        remote = {
            "still_here.txt": RemoteState(RemoteKind.FILE, "a" * 64, 1, 1, None),
        }
        # No exception means the pull was not deferred.
        worker._defer_if_local_changes_pending(
            None, full_pull_required=True, tombstone_count=1,
            remote_entries=remote,
        )
        state.clear_pending.assert_called_once_with({"gone.txt"})


def test_real_local_change_still_defers_the_pull(tmp_path):
    with ClientEnvironment():
        cfg = Config()
        cfg.set_local_root(str(tmp_path))
        cfg.set("delete_enabled", True, persist=False)
        state = MagicMock()
        state.pending_paths.return_value = {"changed.txt"}
        worker = PullWorker(
            cfg, MagicMock(), MagicMock(), threading.Lock(), state,
        )
        (tmp_path / "changed.txt").write_text("new", encoding="utf-8")
        remote = {
            "changed.txt": RemoteState(RemoteKind.FILE, "a" * 64, 1, 1, None),
        }
        try:
            worker._defer_if_local_changes_pending(
                None, full_pull_required=True, tombstone_count=1,
                remote_entries=remote,
            )
        except Exception as exc:  # _PullDeferred is a private exception type
            assert "PullDeferred" in type(exc).__name__
        else:
            raise AssertionError("a real local change must still defer the pull")
