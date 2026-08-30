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


if __name__ == "__main__":
    unittest.main()
