from __future__ import annotations

import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch

from sync_client import delete_safety, rsync_ops, updater
from sync_client.config import Config
from sync_client.repository_safety import RepositorySafetyError

from tests.support import ClientEnvironment


class DeleteSafetyTests(unittest.TestCase):
    def test_cumulative_limit_blocks_a_sequence_of_small_batches(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            cfg = Config()
            cfg.set("delete_enabled", True, persist=False)
            cfg.set("max_delete_files_per_window", 10, persist=False)
            tracker = delete_safety.DeleteSafety(Path(tmp) / "delete-safety.json")
            tracker.check(cfg, 6, "upload")
            tracker.record(cfg, 6, "upload")
            with self.assertRaises(RepositorySafetyError):
                tracker.check(cfg, 6, "upload")
            # A fresh tracker (restart) must keep the same budget.
            restarted = delete_safety.DeleteSafety(Path(tmp) / "delete-safety.json")
            with self.assertRaises(RepositorySafetyError):
                restarted.check(cfg, 6, "upload")

    def test_explicit_confirmation_temporarily_lifts_the_cumulative_limit(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            cfg = Config()
            cfg.set("delete_enabled", True, persist=False)
            cfg.set("max_delete_files_per_window", 10, persist=False)
            tracker = delete_safety.DeleteSafety(Path(tmp) / "delete-safety.json")
            tracker.record(cfg, 20, "upload")
            with self.assertRaises(RepositorySafetyError):
                tracker.check(cfg, 1, "upload")
            tracker.confirm(cfg)
            tracker.check(cfg, 1, "upload")
            self.assertTrue(tracker.snapshot(cfg)["confirmed"])

    def test_disabled_deletions_do_not_consume_budget(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            cfg = Config()
            cfg.set("delete_enabled", False, persist=False)
            tracker = delete_safety.DeleteSafety(Path(tmp) / "delete-safety.json")
            tracker.record(cfg, 10000, "upload")
            tracker.check(cfg, 10000, "upload")
            self.assertEqual(tracker.snapshot(cfg)["used"], 0)


class ManifestDeltaClientTests(unittest.TestCase):
    def test_delta_merges_changes_and_removals(self) -> None:
        with ClientEnvironment():
            cfg = Config()
            cfg.set("remote_server_script", "/tmp/server.sh", persist=False)
            row_a = b"a.txt\t" + b"a" * 64 + b"\t10\t1000\t5\t1\tdev1:1"
            row_b = b"b.txt\t" + b"b" * 64 + b"\t20\t2000\t6\t2\tdev1:2"
            status = "MANIFEST_REVISION=7\n"
            delta = (
                b"NASBOX_MANIFEST_DELTA_V1\nBASE_REVISION=5\nCURRENT_REVISION=7\nCHANGED=1\n"
                + row_b + b"\nREMOVED=\nc.txt\n"
            )
            previous = rsync_ops._parse_manifest_rows([row_a, b"c.txt\t\t0\t0\t4\t0\t"])
            self.assertIsNotNone(previous)
            with patch.object(rsync_ops, "run_remote_script", return_value=(True, status, "")), \
                    patch.object(rsync_ops, "run_remote_script_bytes", return_value=(True, delta, "")) as call:
                revision, entries = rsync_ops.remote_manifest_snapshot(
                    cfg, rsync_ops.NasConnection("nas"), 5, previous_entries=previous,
                )
            self.assertEqual(call.call_args.args[3], ["--manifest-export-delta", "5"])
            self.assertEqual(revision, 7)
            self.assertIsNotNone(entries)
            assert entries is not None
            self.assertEqual(set(entries), {"a.txt", "b.txt"})
            self.assertEqual(entries["b.txt"].size, 20)

    def test_fallback_full_replaces_previous_state(self) -> None:
        with ClientEnvironment():
            cfg = Config()
            cfg.set("remote_server_script", "/tmp/server.sh", persist=False)
            row = b"new.txt\t" + b"c" * 64 + b"\t30\t3000\t7\t1\tdev1:1"
            delta = (
                b"NASBOX_MANIFEST_DELTA_V1\nBASE_REVISION=5\nCURRENT_REVISION=9\n"
                b"FALLBACK_FULL=1\nCHANGED=1\n" + row + b"\nREMOVED=\n"
            )
            with patch.object(rsync_ops, "run_remote_script", return_value=(True, "MANIFEST_REVISION=9\n", "")), \
                    patch.object(rsync_ops, "run_remote_script_bytes", return_value=(True, delta, "")):
                revision, entries = rsync_ops.remote_manifest_snapshot(
                    cfg, rsync_ops.NasConnection("nas"), 5,
                    previous_entries={"old.txt": rsync_ops.RemoteState(rsync_ops.RemoteKind.FILE, "d" * 64, 1, 1)},
                )
            self.assertEqual(revision, 9)
            assert entries is not None
            self.assertEqual(set(entries), {"new.txt"})

    def test_legacy_server_error_falls_back_to_full_export(self) -> None:
        with ClientEnvironment():
            cfg = Config()
            cfg.set("remote_server_script", "/tmp/server.sh", persist=False)
            row = b"a.txt\t" + b"a" * 64 + b"\t1\t1\t1\t1\t"
            full = b"NASBOX_MANIFEST_V1\n" + row + b"\n"
            with patch.object(rsync_ops, "run_remote_script", return_value=(True, "MANIFEST_REVISION=8\n", "")), \
                    patch.object(rsync_ops, "run_remote_script_bytes", side_effect=[
                        (False, b"", "unknown option"), (True, full, b""),
                    ]):
                revision, entries = rsync_ops.remote_manifest_snapshot(
                    cfg, rsync_ops.NasConnection("nas"), 5,
                    previous_entries={"a.txt": rsync_ops.RemoteState(rsync_ops.RemoteKind.FILE, "a" * 64, 1, 1)},
                )
            self.assertEqual(revision, 8)
            assert entries is not None
            self.assertIn("a.txt", entries)


class IntegritySamplingTests(unittest.TestCase):
    def test_integrity_check_uses_bounded_sample(self) -> None:
        with ClientEnvironment():
            cfg = Config()
            cfg.set("remote_server_script", "/tmp/server.sh", persist=False)
            cfg.set("sync_mode", "bidirectional", persist=False)
            cfg.set("integrity_check_sample_size", 2, persist=False)
            rows = b"".join(
                f"f{i}.txt\t".encode() + b"a" * 64 + b"\t1\t1\t1\t1\t\n"
                for i in range(10)
            )
            manifest = b"NASBOX_MANIFEST_V1\n" + rows
            with patch.object(rsync_ops, "run_remote_script",
                              return_value=(True, manifest.decode(), "")), \
                 patch.object(rsync_ops, "_dry_run", return_value=[]) as dry:
                result = rsync_ops.integrity_check(cfg, rsync_ops.NasConnection("nas"))
            self.assertEqual(result, [])
            self.assertEqual(dry.call_count, 2)
            for call in dry.call_args_list:
                self.assertEqual(len(call.kwargs["paths"]), 2)
                self.assertTrue(call.kwargs["checksum"])

    def test_full_audit_when_sample_disabled(self) -> None:
        with ClientEnvironment():
            cfg = Config()
            cfg.set("sync_mode", "bidirectional", persist=False)
            cfg.set("integrity_check_sample_size", 0, persist=False)
            with patch.object(rsync_ops, "_dry_run", return_value=[]) as dry:
                rsync_ops.integrity_check(cfg, rsync_ops.NasConnection("nas"))
            self.assertEqual(dry.call_count, 2)
            self.assertIsNone(dry.call_args_list[0].kwargs.get("paths"))


class VersionParsingTests(unittest.TestCase):
    def test_suffixes_are_ignored(self) -> None:
        self.assertEqual(updater.version_tuple("1.26.14rc1"), (1, 26, 14))
        self.assertEqual(rsync_ops._version_tuple("3.24.0-beta"), (3, 24, 0))
        self.assertTrue(updater.version_tuple("1.26.15") > updater.version_tuple("1.26.14"))


if __name__ == "__main__":
    unittest.main()
