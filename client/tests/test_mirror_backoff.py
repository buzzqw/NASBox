from __future__ import annotations

import threading
import time
import unittest
from unittest.mock import Mock

from sync_client import mirrors


class MirrorBackoffTests(unittest.TestCase):
    """A missing source or a failing rsync must not retry (and rewrite the
    config) on every watcher tick."""

    def setUp(self) -> None:
        self.entry = {
            "source": "/source",
            "dest": "dest",
            "enabled": True,
            "last_sync": 0,
            "error": "",
        }
        self.cfg = Mock()
        self.cfg.is_paused.return_value = False
        self.cfg.mirror_by_source.side_effect = lambda _source: dict(self.entry)
        self.cfg.update_mirror.side_effect = lambda _source, **updates: self.entry.update(updates)
        self.watcher = mirrors.MirrorWatcher(
            self.cfg, Mock(), "/source", "dest", "/root", threading.Lock(),
        )

    def test_unchanged_error_is_not_saved_again(self) -> None:
        self.assertTrue(self.watcher._set_error("boom"))
        self.cfg.update_mirror.assert_called_once_with("/source", error="boom")

        self.cfg.update_mirror.reset_mock()
        self.assertFalse(self.watcher._set_error("boom"))
        self.cfg.update_mirror.assert_not_called()

    def test_changed_error_is_saved(self) -> None:
        self.watcher._set_error("boom")
        self.cfg.update_mirror.reset_mock()

        self.assertTrue(self.watcher._set_error("altra causa"))
        self.cfg.update_mirror.assert_called_once_with("/source", error="altra causa")

    def test_failed_sync_is_retried_after_the_period_not_every_tick(self) -> None:
        self.watcher._sync = Mock()
        self.watcher._last_attempt = time.monotonic()

        self.watcher._maybe_periodic_sync()
        self.watcher._sync.assert_not_called()

        self.watcher._last_attempt = time.monotonic() - mirrors.PERIODIC_SECONDS - 1
        self.watcher._maybe_periodic_sync()
        self.watcher._sync.assert_called_once_with()

    def test_successful_sync_still_respects_the_periodic_interval(self) -> None:
        self.entry["last_sync"] = time.time()
        self.watcher._sync = Mock()
        self.watcher._last_attempt = 0.0

        self.watcher._maybe_periodic_sync()
        self.watcher._sync.assert_not_called()

    def test_paused_client_never_starts_a_sync(self) -> None:
        self.cfg.is_paused.return_value = True
        self.watcher._sync = Mock()
        self.watcher._last_attempt = 0.0

        self.watcher._maybe_periodic_sync()
        self.watcher._sync.assert_not_called()


if __name__ == "__main__":
    unittest.main()
