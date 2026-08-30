from __future__ import annotations

import os
import tempfile
import unittest
from pathlib import Path

from sync_client.config import Config
from sync_client.sync_state import CausalVersion, Fingerprint, SyncStateStore

from tests.support import ClientEnvironment


class SyncStateTests(unittest.TestCase):
    def setUp(self) -> None:
        self.environment = ClientEnvironment()
        self.environment.__enter__()
        self.root = Path(tempfile.mkdtemp(prefix="nasbox-local-test-"))
        self.config = Config()
        self.config.set("local_root", str(self.root), persist=False)
        self.config.set("repository_id", "repo-test", persist=False)
        self.store = SyncStateStore(self.config)

    def tearDown(self) -> None:
        for path in self.root.rglob("*"):
            if path.is_file() or path.is_symlink():
                path.unlink()
        for path in sorted(self.root.rglob("*"), reverse=True):
            if path.is_dir():
                path.rmdir()
        self.environment.__exit__(None, None, None)

    def test_fingerprint_accepts_regular_file_and_rejects_missing_or_directory(self) -> None:
        file_path = self.root / "file.txt"
        file_path.write_bytes(b"hello")
        directory = self.root / "directory"
        directory.mkdir()
        fingerprint = self.store.fingerprint(file_path)
        self.assertIsInstance(fingerprint, Fingerprint)
        assert fingerprint is not None
        self.assertEqual(fingerprint.size, 5)
        self.assertTrue(fingerprint.digest)
        self.assertIsNone(self.store.fingerprint(self.root / "missing"))
        self.assertIsNone(self.store.fingerprint(directory))

    def test_record_local_and_tombstone_round_trip(self) -> None:
        file_path = self.root / "file.txt"
        file_path.write_bytes(b"hello")
        self.store.record_local(str(self.root), "file.txt")
        recorded = self.store.get("file.txt")
        self.assertIsNotNone(recorded)
        assert recorded is not None
        self.assertFalse(recorded.is_tombstone)

        self.store.record_fingerprints({"file.txt": None})
        tombstone = self.store.get("file.txt")
        self.assertIsNotNone(tombstone)
        assert tombstone is not None
        self.assertTrue(tombstone.is_tombstone)
        self.assertTrue(self.store.has_entries())

    def test_local_observations_get_stable_incrementing_causal_versions(self) -> None:
        file_path = self.root / "causal.txt"
        file_path.write_bytes(b"one")
        first = self.store.local_fingerprint(str(self.root), "causal.txt")
        same = self.store.local_fingerprint(str(self.root), "causal.txt")
        self.assertIsNotNone(first)
        self.assertEqual(first, same)
        assert first is not None and first.causal is not None

        file_path.write_bytes(b"two")
        second = self.store.local_fingerprint(str(self.root), "causal.txt")
        assert second is not None and second.causal is not None
        self.assertTrue(second.causal.dominates(first.causal))
        self.assertEqual(second.causal.counter("missing"), 0)

        file_path.unlink()
        deleted = self.store.local_causal(str(self.root), "causal.txt")
        self.assertIsNotNone(deleted)
        assert deleted is not None
        self.assertTrue(deleted.dominates(second.causal))

    def test_causal_version_round_trip_on_baseline_and_legacy_rows(self) -> None:
        version = CausalVersion((("device-a", 4), ("device-b", 2)))
        self.store.record_fingerprints({"causal.txt": Fingerprint("a", 1, 10, version)})
        recorded = self.store.get("causal.txt")
        self.assertIsNotNone(recorded)
        assert recorded is not None
        self.assertEqual(recorded.causal, version)
        self.store.record_fingerprints({"legacy.txt": Fingerprint("b", 1, 10)})
        legacy = self.store.get("legacy.txt")
        assert legacy is not None
        self.assertIsNone(legacy.causal)

    def test_changed_paths_detects_new_modified_and_deleted_files(self) -> None:
        unchanged = self.root / "unchanged.txt"
        modified = self.root / "modified.txt"
        deleted = self.root / "deleted.txt"
        unchanged.write_bytes(b"same")
        modified.write_bytes(b"old")
        deleted.write_bytes(b"gone")
        self.store.record_local_many(str(self.root), ["unchanged.txt", "modified.txt", "deleted.txt"])

        modified.write_bytes(b"new")
        deleted.unlink()
        (self.root / "new.txt").write_bytes(b"new file")
        changed = self.store.changed_paths(str(self.root))
        self.assertEqual(changed, {"modified.txt", "deleted.txt", "new.txt"})

    def test_changed_paths_ignores_partial_files_and_reports_progress_for_hashed_paths(self) -> None:
        file_path = self.root / "file.txt"
        file_path.write_bytes(b"old")
        self.store.record_local(str(self.root), "file.txt")
        file_path.write_bytes(b"new")
        (self.root / ".sync-partial").mkdir()
        (self.root / ".sync-partial" / "partial.txt").write_bytes(b"ignore")
        progress: list[tuple[int, int]] = []
        changed = self.store.changed_paths(str(self.root), lambda done, total: progress.append((done, total)))
        self.assertEqual(changed, {"file.txt"})
        self.assertEqual(progress, [(1, 1)])

    def test_stable_paths_distinguishes_missing_and_changed_metadata(self) -> None:
        stable = self.root / "stable.txt"
        unstable = self.root / "unstable.txt"
        stable.write_bytes(b"stable")
        unstable.write_bytes(b"before")
        original = unstable.stat().st_mtime_ns

        def change_during_interval(_interval: float) -> None:
            os.utime(unstable, ns=(original, original + 1))

        from unittest.mock import patch
        with patch("sync_client.sync_state.time.sleep", side_effect=change_during_interval):
            stable_paths, unstable_paths = self.store.stable_paths(
                str(self.root), {"stable.txt", "unstable.txt", "missing.txt"}, 1,
            )
        self.assertEqual(stable_paths, {"stable.txt", "missing.txt"})
        self.assertEqual(unstable_paths, {"unstable.txt"})

    def test_pending_queue_records_attempts_and_can_be_cleared(self) -> None:
        self.store.mark_pending({"a.txt", "b.txt", ""})
        self.assertEqual(self.store.pending_paths(), {"a.txt", "b.txt"})
        self.store.record_pending_attempt(["a.txt"], "lock busy")
        summary = self.store.pending_summary()
        self.assertEqual(summary["count"], 2)
        self.assertEqual(summary["last_reason"], "lock busy")
        self.assertEqual(summary["attempt_count"], 1)
        self.store.clear_pending(["a.txt"])
        self.assertEqual(self.store.pending_paths(), {"b.txt"})

    def test_scheduler_queue_is_persistent_and_stale_rows_can_be_cleared(self) -> None:
        device_id = self.store.device_id()
        self.store.scheduler_queue_add("request-1", device_id, "pull", 4, 10.0)
        self.assertEqual(self.store.scheduler_queue_rows()[0]["kind"], "pull")
        reopened = SyncStateStore(self.config)
        self.assertEqual(reopened.scheduler_queue_rows()[0]["request_id"], "request-1")
        reopened.clear_scheduler_queue(device_id)
        self.assertEqual(reopened.scheduler_queue_rows(), [])

    def test_conflict_group_replaces_members_and_resolves(self) -> None:
        group_id = self.store.upsert_conflict_group(
            "notes.txt",
            [{"path": "notes (device-a).txt", "origin_device": "device-a", "digest": "a"}],
        )
        groups = self.store.open_conflict_groups()
        self.assertEqual(len(groups), 1)
        self.assertEqual(groups[0]["group_id"], group_id)
        self.assertEqual(groups[0]["members"][0]["path"], "notes (device-a).txt")

        self.store.upsert_conflict_group(
            "notes.txt",
            [{"path": "notes (device-b).txt", "origin_device": "device-b", "digest": "b"}],
        )
        self.assertEqual(self.store.open_conflict_groups()[0]["members"][0]["origin_device"], "device-b")
        self.store.mark_conflict_resolved(group_id, "notes (device-b).txt")
        self.assertEqual(self.store.open_conflict_groups(), [])


if __name__ == "__main__":
    unittest.main()
