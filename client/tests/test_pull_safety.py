from __future__ import annotations

import threading
import tempfile
import unittest
from pathlib import Path
from unittest.mock import Mock
from unittest.mock import patch

from sync_client import conflicts, paths, pull_worker, push_worker, rsync_ops
from sync_client.lock_coordinator import LockCoordinator
from sync_client.sync_state import SyncStateStore


class PullSafetyTests(unittest.TestCase):
    def test_pending_paths_survive_a_store_reopen(self) -> None:
        cfg = type(
            "Config",
            (),
            {
                "get": lambda _self, key, default=None: {
                    "repository_id": "repo-test",
                    "nas_user": "user",
                    "nas_lan": "nas",
                    "remote_prefix": "/share",
                }.get(key, default),
            },
        )()
        with tempfile.TemporaryDirectory() as state_dir:
            db_path = Path(state_dir) / "state.sqlite3"
            with patch.object(paths, "sync_state_db_file", return_value=db_path):
                first = SyncStateStore(cfg)
                first.mark_pending({"manuale.txt", "basicF-ITA/regole.pdf"})
                second = SyncStateStore(cfg)
                self.assertEqual(
                    second.pending_paths(), {"manuale.txt", "basicF-ITA/regole.pdf"},
                )
                second.clear_pending({"manuale.txt"})
                self.assertEqual(second.pending_paths(), {"basicF-ITA/regole.pdf"})

    def test_pending_summary_records_age_and_defer_reason(self) -> None:
        cfg = type(
            "Config",
            (),
            {
                "get": lambda _self, key, default=None: {
                    "repository_id": "repo-summary",
                }.get(key, default),
            },
        )()
        with tempfile.TemporaryDirectory() as state_dir:
            with patch.object(paths, "state_dir", return_value=Path(state_dir)):
                store = SyncStateStore(cfg)
                store.mark_pending({"manuale.txt"})
                store.record_pending_attempt({"manuale.txt"}, "lock occupato")
                summary = store.pending_summary()

        self.assertEqual(summary["count"], 1)
        self.assertEqual(summary["last_reason"], "lock occupato")
        self.assertEqual(summary["attempt_count"], 1)

    def test_lock_coordinator_shares_backoff_until_acquired(self) -> None:
        coordinator = LockCoordinator(initial_delay=10, max_delay=20)
        self.assertTrue(coordinator.can_attempt())
        delay = coordinator.defer()
        self.assertGreaterEqual(delay, 8)
        self.assertLessEqual(delay, 12)
        self.assertFalse(coordinator.can_attempt())
        self.assertGreater(coordinator.retry_after(), 0)
        coordinator.acquired()
        self.assertTrue(coordinator.can_attempt())

    def test_conflict_resolution_keeps_selected_version_and_trashes_the_other(self) -> None:
        with tempfile.TemporaryDirectory() as root, tempfile.TemporaryDirectory() as state_dir:
            root_path = Path(root)
            original = root_path / "manuale.txt"
            conflict = root_path / "manuale (conflitto da pc-a abc123).txt"
            original.write_text("originale")
            conflict.write_text("alternativa")
            with patch.object(paths, "state_dir", return_value=Path(state_dir)):
                groups = conflicts.scan_conflict_groups(root)
                self.assertEqual(len(groups), 1)
                ok, detail = conflicts.resolve_conflict(groups[0], conflict, root)

            self.assertTrue(ok, detail)
            self.assertEqual(original.read_text(), "alternativa")
            self.assertFalse(conflict.exists())
            self.assertTrue(any(Path(state_dir).rglob("manuale.txt-*")))

    def test_destructive_pull_defers_when_local_copy_is_not_in_baseline(self) -> None:
        cfg = Mock()
        cfg.get.side_effect = lambda key, default=None: {
            "delete_enabled": True,
        }.get(key, default)
        cfg.exclude_patterns.return_value = []
        cfg.local_root.return_value = "/local/nasbox"

        watcher = Mock()
        worker = pull_worker.PullWorker(
            cfg, Mock(), Mock(get=Mock(return_value=watcher)),
            threading.Lock(), Mock(),
        )
        worker.sync_state.changed_paths.return_value = {
            "basicF-ITA/Basic-Fantasy-RPG-Rules-r142-ITA.pdf",
        }
        worker.sync_state.pending_paths.return_value = set()

        with self.assertRaises(pull_worker._PullDeferred):
            worker._defer_if_local_changes_pending(
                watcher, full_pull_required=False, tombstone_count=1,
            )

        watcher.mark_dirty.assert_called_once_with()
        self.assertTrue(worker._self_cancelled)

    def test_push_keeps_debounced_paths_after_recording_them_pending(self) -> None:
        cfg = Mock()
        cfg.is_paused.return_value = False
        cfg.is_configured.return_value = True
        cfg.local_root.return_value = "/local/nasbox"
        cfg.get.side_effect = lambda key, default=None: {
            "debounce_seconds": 2,
        }.get(key, default)
        cfg.exclude_patterns.return_value = []
        watcher = Mock()
        watcher.is_dirty.return_value = True
        watcher.consume_paths_if_ready.return_value = {"new.txt"}
        watchers = Mock()
        watchers.get.return_value = watcher
        state = Mock()
        state.pending_paths.return_value = set()
        worker = push_worker.PushWorker(cfg, Mock(), watchers, threading.Lock(), state)
        worker._conn = rsync_ops.NasConnection("fake-host")

        with patch.object(rsync_ops, "remote_lock") as remote_lock, \
             patch.object(rsync_ops, "retry_pending_journal", return_value=(True, "")), \
             patch.object(rsync_ops, "validate_transfer_safety"), \
             patch.object(rsync_ops, "ensure_remote_dir", return_value=True), \
             patch.object(worker, "_resolve_paths", return_value={"new.txt"}) as resolve, \
             patch.object(worker, "_run_chunk", return_value=(True, 0, False)):
            remote_lock.return_value.__enter__ = Mock(return_value=None)
            remote_lock.return_value.__exit__ = Mock(return_value=False)
            worker._tick()

        state.mark_pending.assert_called_once_with({"new.txt"})
        resolve.assert_called_once_with({"new.txt"}, False)

    def test_push_discards_directory_only_pending_events_without_locking_nas(self) -> None:
        with tempfile.TemporaryDirectory() as root:
            Path(root, ".git", "objects", "info").mkdir(parents=True)
            cfg = Mock()
            cfg.is_paused.return_value = False
            cfg.is_configured.return_value = True
            cfg.local_root.return_value = root
            cfg.get.side_effect = lambda key, default=None: {
                "debounce_seconds": 2,
            }.get(key, default)
            cfg.exclude_patterns.return_value = []
            watcher = Mock()
            watcher.is_dirty.return_value = True
            watcher.consume_paths_if_ready.return_value = {".git/objects/info"}
            watchers = Mock()
            watchers.get.return_value = watcher
            state = Mock()
            state.pending_paths.return_value = {".git/objects/info"}
            worker = push_worker.PushWorker(cfg, Mock(), watchers, threading.Lock(), state)
            worker._conn = rsync_ops.NasConnection("fake-host")

            with patch.object(rsync_ops, "remote_lock") as remote_lock:
                worker._tick()

            state.clear_pending.assert_called_once_with({".git/objects/info"})
            remote_lock.assert_not_called()


if __name__ == "__main__":
    unittest.main()
