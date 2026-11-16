from __future__ import annotations

import threading
import unittest
from unittest.mock import Mock

from sync_client.config import (
    Config,
    SYNC_MODE_ARCHIVE,
    SYNC_MODE_BIDIRECTIONAL,
    SYNC_MODE_PULL_ONLY,
    SYNC_MODE_PUSH_ONLY,
)
from sync_client import rsync_ops
from sync_client.engine import SyncEngine
from sync_client.pull_worker import PullWorker
from sync_client.push_worker import PushWorker
from sync_client.reconcile import Action, RemoteKind, RemoteState, plan_path
from sync_client.sync_state import Fingerprint

from tests.support import ClientEnvironment


class SyncModeTests(unittest.TestCase):
    def setUp(self) -> None:
        self.environment = ClientEnvironment()
        self.environment.__enter__()
        self.cfg = Config()

    def tearDown(self) -> None:
        self.environment.__exit__(None, None, None)

    def test_existing_configuration_defaults_to_bidirectional(self) -> None:
        self.assertEqual(self.cfg.sync_mode(), SYNC_MODE_BIDIRECTIONAL)
        self.cfg.set("sync_mode", "not-a-mode", persist=False)
        self.assertEqual(self.cfg.sync_mode(), SYNC_MODE_BIDIRECTIONAL)

    def test_sync_mode_is_persisted(self) -> None:
        self.cfg.set_sync_mode(SYNC_MODE_PUSH_ONLY)
        self.assertEqual(Config().sync_mode(), SYNC_MODE_PUSH_ONLY)

    def test_direction_matrix(self) -> None:
        expected = {
            SYNC_MODE_BIDIRECTIONAL: (True, True),
            SYNC_MODE_PUSH_ONLY: (True, False),
            SYNC_MODE_PULL_ONLY: (False, True),
            SYNC_MODE_ARCHIVE: (True, False),
        }
        for mode, (push, pull) in expected.items():
            with self.subTest(mode=mode):
                self.cfg.set("sync_mode", mode, persist=False)
                self.assertEqual((self.cfg.allows_push(), self.cfg.allows_pull()), (push, pull))

    def test_mirror_destinations_are_locally_authoritative(self) -> None:
        self.cfg.set("mirrors", [
            {"source": "/home/me/SADE", "dest": "SADE", "enabled": True},
            {"source": "/home/me/off", "dest": "off", "enabled": False},
        ], persist=False)

        self.assertEqual(self.cfg.mirror_owned_roots(), ["SADE"])
        self.assertTrue(rsync_ops.path_is_mirror_owned(self.cfg, "SADE/notes.tex"))
        self.assertTrue(rsync_ops.path_is_mirror_owned(self.cfg, "SADE"))
        self.assertFalse(rsync_ops.path_is_mirror_owned(self.cfg, "SADE2/notes.tex"))
        self.assertFalse(rsync_ops.path_is_mirror_owned(self.cfg, "other/SADE/notes.tex"))
        self.assertFalse(rsync_ops.path_is_mirror_owned(self.cfg, "off/notes.tex"))

    def test_archive_never_allows_remote_deletions(self) -> None:
        self.cfg.set("delete_enabled", True, persist=False)
        self.cfg.set("sync_mode", SYNC_MODE_ARCHIVE, persist=False)
        decision = plan_path(
            Fingerprint("base", 4, 10), None,
            RemoteState(RemoteKind.FILE, "base", 4, 10),
            delete_enabled=self.cfg.allows_remote_deletions(),
        )
        self.assertEqual(decision.action, Action.REMOTE_WINS)
        self.assertFalse(self.cfg.allows_remote_deletions())

    def test_push_worker_does_not_run_in_pull_only(self) -> None:
        cfg = Mock()
        cfg.allows_push.return_value = False
        cfg.is_paused.side_effect = AssertionError("push guard was not applied")
        worker = PushWorker(cfg, Mock(), Mock(), threading.Lock(), Mock())
        worker._tick()

    def test_push_tick_without_changes_does_not_reference_transfer_result(self) -> None:
        cfg = Mock()
        cfg.allows_push.return_value = True
        cfg.is_paused.return_value = False
        cfg.is_configured.return_value = True
        cfg.get.return_value = 2
        cfg.local_root.return_value = "/tmp/nasbox-test"
        watchers = Mock()
        watchers.get.return_value = None
        sync_state = Mock()
        sync_state.pending_paths.return_value = set()
        worker = PushWorker(cfg, Mock(), watchers, threading.Lock(), sync_state)
        worker.set_connection(object())
        worker._tick()

    def test_pull_worker_does_not_run_in_push_only_or_archive(self) -> None:
        for mode in (SYNC_MODE_PUSH_ONLY, SYNC_MODE_ARCHIVE):
            with self.subTest(mode=mode):
                cfg = Mock()
                cfg.allows_pull.return_value = False
                cfg.is_paused.side_effect = AssertionError("pull guard was not applied")
                worker = PullWorker(cfg, Mock(), Mock(), threading.Lock(), Mock())
                worker._tick()

    def test_engine_sync_now_requests_only_enabled_directions(self) -> None:
        for mode, push_enabled, pull_enabled in (
            (SYNC_MODE_BIDIRECTIONAL, True, True),
            (SYNC_MODE_PUSH_ONLY, True, False),
            (SYNC_MODE_PULL_ONLY, False, True),
            (SYNC_MODE_ARCHIVE, True, False),
        ):
            with self.subTest(mode=mode):
                self.cfg.set("sync_mode", mode, persist=False)
                push = Mock()
                pull = Mock()
                engine = SyncEngine(self.cfg, Mock(), Mock(), push_worker=push, pull_worker=pull)
                engine.sync_now()
                self.assertEqual(push.request_full_sync.called, push_enabled)
                self.assertEqual(pull.reset_schedule.called, pull_enabled)

    def test_pull_tick_skips_the_nas_lease_after_a_change_feed_timeout(self) -> None:
        """An unchanged feed must not wake the lease/safety/journal round-trips."""
        import tempfile
        import time
        from unittest.mock import patch

        from sync_client import rsync_ops

        with tempfile.TemporaryDirectory() as local_root:
            self.cfg.set("sync_mode", SYNC_MODE_PULL_ONLY, persist=False)
            self.cfg.set("remote_change_feed_available", True, persist=False)
            self.cfg.set("remote_server_script", "/tmp/server.sh", persist=False)
            self.cfg.set_local_root(local_root)
            watchers = Mock()
            watchers.get.return_value = None
            worker = PullWorker(self.cfg, Mock(), watchers, threading.Lock(), Mock())
            worker.set_connection(object())
            worker._last_manifest_revision = 5
            worker._last_full_pull = time.time()
            with patch.object(
                rsync_ops, "wait_for_remote_revision",
                return_value=rsync_ops.RemoteRevisionWait("TIMEOUT", 5),
            ), patch.object(
                rsync_ops, "remote_lock",
                side_effect=AssertionError("lease must not be taken when the feed reports no change"),
            ):
                worker._tick()

    def test_pull_defer_marks_exact_pending_paths_and_backs_off(self) -> None:
        """Stepping aside for pending changes must not force a full-tree sweep."""
        import time

        from sync_client.pull_worker import _PullDeferred

        self.cfg.set("delete_enabled", True, persist=False)
        sync_state = Mock()
        sync_state.pending_paths.return_value = {"a.txt", "b.txt"}
        watcher = Mock()
        worker = PullWorker(self.cfg, Mock(), Mock(), threading.Lock(), sync_state)

        with self.assertRaises(_PullDeferred):
            worker._defer_if_local_changes_pending(
                watcher, full_pull_required=True, tombstone_count=0,
            )

        marked = {call.args[0] for call in watcher.mark_dirty.call_args_list}
        self.assertEqual(marked, {"a.txt", "b.txt"})
        # No unknown-path sentinel: that is what triggered a whole-tree scan.
        self.assertNotIn("", marked)

        worker._defer_retry_at = time.time() + 60
        worker.wake()
        self.assertEqual(worker._defer_retry_at, 0.0)

    def test_full_sweep_drops_pending_paths_already_at_baseline(self) -> None:
        """A stale queue entry must not keep pulling deferred forever."""
        import tempfile
        from pathlib import Path
        from unittest.mock import patch

        from sync_client import rsync_ops
        from sync_client.sync_state import SyncStateStore

        with tempfile.TemporaryDirectory() as local_root:
            self.cfg.set_local_root(local_root)
            self.cfg.set("repository_id", "repo-test", persist=False)
            (Path(local_root) / "dirty.txt").write_bytes(b"changed")
            store = SyncStateStore(self.cfg)
            store.mark_pending({"stale.txt", "dirty.txt"})

            watcher = Mock()
            watcher.is_dirty.return_value = True
            watcher.consume_paths_if_ready.return_value = {""}
            watchers = Mock()
            watchers.get.return_value = watcher
            worker = PushWorker(self.cfg, Mock(), watchers, threading.Lock(), store)
            worker.set_connection(object())

            with patch.object(store, "changed_paths", return_value={"dirty.txt"}), \
                    patch.object(rsync_ops, "remote_lock", side_effect=rsync_ops.RemoteLockBusy()):
                worker._tick()

            self.assertEqual(store.pending_paths(), {"dirty.txt"})


if __name__ == "__main__":
    unittest.main()
