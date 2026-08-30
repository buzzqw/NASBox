from __future__ import annotations

import os
import shutil
import tempfile
import threading
import unittest
from pathlib import Path
from unittest.mock import Mock, patch

from sync_client.config import Config
from sync_client.push_worker import PushWorker
from sync_client.reconcile import RemoteKind, RemoteState
from sync_client.sync_state import SyncStateStore

from tests.support import ClientEnvironment


class LocalRenameTests(unittest.TestCase):
    def setUp(self) -> None:
        self.environment = ClientEnvironment()
        self.environment.__enter__()
        self.root = Path(tempfile.mkdtemp(prefix="nasbox-rename-test-"))
        self.cfg = Config()
        self.cfg.set("local_root", str(self.root), persist=False)
        self.cfg.set("repository_id", "rename-repository", persist=False)
        self.store = SyncStateStore(self.cfg)
        self.worker = PushWorker(self.cfg, Mock(), Mock(), threading.Lock(), self.store)

    def tearDown(self) -> None:
        shutil.rmtree(self.root, ignore_errors=True)
        self.environment.__exit__(None, None, None)

    def test_same_fingerprint_is_planned_as_remote_rename(self) -> None:
        old = self.root / "old.txt"
        old.write_bytes(b"same bytes")
        self.store.record_local(str(self.root), "old.txt")
        baseline = self.store.get("old.txt")
        assert baseline is not None
        old.rename(self.root / "new.txt")
        new = self.store.local_fingerprint(str(self.root), "new.txt")
        assert new is not None
        plans = self.worker._find_rename_plans(
            {"old.txt", "new.txt"},
            {
                "old.txt": RemoteState(RemoteKind.FILE, baseline.digest, baseline.size, baseline.mtime_ns),
                "new.txt": RemoteState(RemoteKind.ABSENT),
            },
        )
        self.assertEqual(len(plans), 1)
        self.assertEqual((plans[0].source_path, plans[0].destination_path), ("old.txt", "new.txt"))

    def test_duplicate_new_fingerprints_are_left_to_upload_delete(self) -> None:
        old = self.root / "old.txt"
        old.write_bytes(b"same bytes")
        self.store.record_local(str(self.root), "old.txt")
        old_stat = old.stat()
        old.unlink()
        for name in ("new-a.txt", "new-b.txt"):
            path = self.root / name
            path.write_bytes(b"same bytes")
            path.touch()
            path.stat()
            # Preserve the complete local fingerprint so the ambiguity is real.
            os.utime(path, ns=(old_stat.st_atime_ns, old_stat.st_mtime_ns))
        baseline = self.store.get("old.txt")
        assert baseline is not None
        remote = {
            "old.txt": RemoteState(RemoteKind.FILE, baseline.digest, baseline.size, baseline.mtime_ns),
            "new-a.txt": RemoteState(RemoteKind.ABSENT),
            "new-b.txt": RemoteState(RemoteKind.ABSENT),
        }
        self.assertEqual(self.worker._find_rename_plans(set(remote), remote), [])

    def test_nonempty_directory_event_plans_one_directory_rename(self) -> None:
        source = self.root / "old-dir"
        source.mkdir()
        (source / "nested").mkdir()
        (source / "nested" / "file.txt").write_bytes(b"directory bytes")
        self.store.record_local(str(self.root), "old-dir/nested/file.txt")
        baseline = self.store.get("old-dir/nested/file.txt")
        assert baseline is not None
        source.rename(self.root / "new-dir")
        self.worker._directory_events = {"old-dir", "new-dir"}
        remote = {
            "old-dir": RemoteState(RemoteKind.OTHER),
            "new-dir": RemoteState(RemoteKind.ABSENT),
            "old-dir/nested/file.txt": RemoteState(
                RemoteKind.FILE, baseline.digest, baseline.size, baseline.mtime_ns,
            ),
            "new-dir/nested/file.txt": RemoteState(RemoteKind.ABSENT),
        }
        plans = self.worker._find_rename_plans(
            set(remote) - {"old-dir", "new-dir"}, remote,
        )
        self.assertEqual(len(plans), 1)
        self.assertEqual(plans[0].kind, "DIR")

    def test_empty_directory_event_has_no_resolved_transfer_path(self) -> None:
        (self.root / "empty").mkdir()
        self.assertEqual(self.worker._resolve_paths({"empty"}, False), set())
        self.assertEqual(self.worker._directory_events, {"empty"})

    def test_successful_rename_updates_both_baseline_paths(self) -> None:
        old = self.root / "old.txt"
        old.write_bytes(b"same bytes")
        self.store.record_local(str(self.root), "old.txt")
        baseline = self.store.get("old.txt")
        assert baseline is not None
        old.rename(self.root / "new.txt")
        plans = self.worker._find_rename_plans(
            {"old.txt", "new.txt"},
            {
                "old.txt": RemoteState(RemoteKind.FILE, baseline.digest, baseline.size, baseline.mtime_ns),
                "new.txt": RemoteState(RemoteKind.ABSENT),
            },
        )
        self.store.mark_pending({"old.txt", "new.txt"})
        lease = Mock()
        with patch("sync_client.rsync_ops.rename_remote", return_value=(True, "")) as rename_remote:
            items, failed = self.worker._run_renames(plans, None, lease)
        self.assertEqual(failed, set())
        self.assertIs(rename_remote.call_args.kwargs["lease"], lease)
        self.assertEqual(items[0].source_path, "old.txt")
        self.assertTrue(self.store.get("old.txt").is_tombstone)  # type: ignore[union-attr]
        self.assertEqual(self.store.get("new.txt").digest, baseline.digest)  # type: ignore[union-attr]
        self.assertEqual(self.store.pending_paths(), set())


if __name__ == "__main__":
    unittest.main()
