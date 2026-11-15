"""A staged publish is a real upload and must be reported like one.

`_try_run_staged_chunk` is the fast path for a pure batch of brand-new files:
it uploads into a private staging tree and publishes it atomically. It used to
return success without emitting any per-file event, so those uploads appeared on
the NAS but never in client.log, events.jsonl, the Log tab or the transfers
list -- the client looked idle right after publishing dozens of files.
"""
from __future__ import annotations

import threading
import unittest
from unittest.mock import MagicMock, Mock, patch

from sync_client import push_worker as push_worker_module
from sync_client import rsync_ops
from sync_client.push_worker import PushWorker
from sync_client.sync_state import Fingerprint


class StagedPublishReportingTests(unittest.TestCase):
    def _worker(self) -> PushWorker:
        cfg = Mock()
        cfg.local_root.return_value = "/tmp/nasbox-staging-test"
        sync_state = Mock()
        sync_state.local_fingerprint.return_value = Fingerprint("a" * 64, 5, 10)
        sync_state.device_id.return_value = "device-1"
        worker = PushWorker(cfg, Mock(), Mock(), threading.Lock(), sync_state)
        worker.set_connection(object())
        worker._log = Mock()
        return worker

    def test_staged_publish_reports_every_uploaded_file(self) -> None:
        worker = self._worker()
        plan = ({"a.txt", "dir/b.txt"}, [], set(), set(), {}, [])
        done: list[tuple[str, str]] = []
        worker.transfer_item_done.connect(lambda direction, path: done.append((direction, path)))

        def fake_plan(*_args, **_kwargs):
            # _build_plan normally records which paths are brand-new; the
            # staging path only accepts a batch that is exactly that set.
            worker._staging_upload_paths = {"a.txt", "dir/b.txt"}
            return plan

        lock = MagicMock()
        lock_cm = MagicMock()
        lock_cm.__enter__.return_value = lock

        with patch.object(push_worker_module.rsync_ops, "remote_lock", return_value=lock_cm), \
             patch.object(worker, "_build_plan", side_effect=fake_plan), \
             patch.object(worker, "_run_transfer_tracked", return_value=rsync_ops.TransferResult(True, [])), \
             patch.object(push_worker_module.rsync_ops, "create_remote_staging", return_value="/remote/staging/x"), \
             patch.object(push_worker_module.rsync_ops, "publish_staging", return_value=(True, "")), \
             patch.object(push_worker_module.rsync_ops, "cleanup_remote_staging"):
            result = worker._try_run_staged_chunk({"a.txt", "dir/b.txt"}, 0, 2, 0, None)

        self.assertEqual(result, (True, 0, False))
        logged = sorted(call.args for call in worker._log.call_args_list if call.args[0] == "UPLOAD")
        self.assertEqual(
            logged,
            [("UPLOAD", "a.txt", "5 byte"), ("UPLOAD", "dir/b.txt", "5 byte")],
        )
        self.assertEqual(done, [("upload", "a.txt"), ("upload", "dir/b.txt")])

    def test_failed_publish_reports_nothing(self) -> None:
        worker = self._worker()
        plan = ({"a.txt", "dir/b.txt"}, [], set(), set(), {}, [])

        def fake_plan(*_args, **_kwargs):
            worker._staging_upload_paths = {"a.txt", "dir/b.txt"}
            return plan

        lock_cm = MagicMock()
        lock_cm.__enter__.return_value = MagicMock()

        with patch.object(push_worker_module.rsync_ops, "remote_lock", return_value=lock_cm), \
             patch.object(worker, "_build_plan", side_effect=fake_plan), \
             patch.object(worker, "_run_transfer_tracked", return_value=rsync_ops.TransferResult(True, [])), \
             patch.object(push_worker_module.rsync_ops, "create_remote_staging", return_value="/remote/staging/x"), \
             patch.object(push_worker_module.rsync_ops, "publish_staging", return_value=(False, "conflitto")), \
             patch.object(push_worker_module.rsync_ops, "cleanup_remote_staging"):
            result = worker._try_run_staged_chunk({"a.txt", "dir/b.txt"}, 0, 2, 0, None)

        self.assertEqual(result, (False, 0, False))
        uploaded = [call for call in worker._log.call_args_list if call.args[0] == "UPLOAD"]
        self.assertEqual(uploaded, [])


if __name__ == "__main__":
    unittest.main()
