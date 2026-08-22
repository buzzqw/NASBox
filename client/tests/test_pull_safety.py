from __future__ import annotations

import threading
import unittest
from unittest.mock import Mock

from sync_client import pull_worker


class PullSafetyTests(unittest.TestCase):
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

        with self.assertRaises(pull_worker._PullDeferred):
            worker._defer_if_local_changes_pending(
                watcher, full_pull_required=False, tombstone_count=1,
            )

        watcher.mark_dirty.assert_called_once_with()
        self.assertTrue(worker._self_cancelled)


if __name__ == "__main__":
    unittest.main()
