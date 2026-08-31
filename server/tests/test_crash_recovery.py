from __future__ import annotations

import hashlib
import os
import shutil
import subprocess
import tempfile
import unittest
import fcntl
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]
SCRIPT = ROOT / "sync-daemon-server.sh"


class ServerCrashRecoveryTests(unittest.TestCase):
    def staging_fixture(self, sandbox: Path) -> tuple[Path, Path, Path, Path]:
        script = sandbox / "server.sh"
        share = sandbox / "share"
        share.mkdir()
        shutil.copy2(SCRIPT, script)
        script.chmod(0o755)
        config = sandbox / "server.conf"
        config.write_text(f"SHARE_ROOT={share}\nRETENTION_DAYS=30\n")
        staging = share / ".nasbox-staging" / "batch-a"
        staging.mkdir(parents=True)
        return script, share, config, staging

    @staticmethod
    def staging_payload(staging: Path, txid: str, files: list[tuple[str, bytes]]) -> bytes:
        payload = b"STAGING_PUBLISH_V1\0" + os.fsencode(staging) + b"\0"
        payload += txid.encode() + b"\0device-a\0" + str(len(files)).encode() + b"\0"
        for relative, content in files:
            staged = staging / relative
            staged.parent.mkdir(parents=True, exist_ok=True)
            staged.write_bytes(content)
            payload += (
                os.fsencode(relative) + b"\0" + hashlib.sha256(content).hexdigest().encode() + b"\0"
                + str(len(content)).encode() + b"\0"
                + str(staged.stat().st_mtime_ns).encode() + b"\0"
            )
        return payload

    @staticmethod
    def hold_global_lock(sandbox: Path):
        lock_path = sandbox / "state" / "sync-transfer.lock"
        lock_path.parent.mkdir(exist_ok=True)
        lock = lock_path.open("w")
        fcntl.flock(lock, fcntl.LOCK_EX)
        (sandbox / "state" / "sync-transfer.lock.owner").write_text(
            "device-a|test-host|0|idle|0|0\n"
        )
        return lock

    @staticmethod
    def release_lock(lock) -> None:
        fcntl.flock(lock, fcntl.LOCK_UN)
        lock.close()

    @classmethod
    def run_with_global_lock(cls, sandbox: Path, command: list[str], **kwargs):
        lock = cls.hold_global_lock(sandbox)
        try:
            return subprocess.run(command, **kwargs)
        finally:
            owner = sandbox / "state" / "sync-transfer.lock.owner"
            owner.unlink(missing_ok=True)
            cls.release_lock(lock)

    @staticmethod
    def empty_checked_delete_payload() -> bytes:
        return (
            b"CHECKED_DELETE_V1\0"
            b"2026-08-22--12-00-00-000001Z\0"
            b"device-a\0"
            b"0\0"
        )

    def test_checked_delete_crash_after_move_keeps_recoverable_file(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            sandbox = Path(directory)
            script = sandbox / "server.sh"
            share = sandbox / "share"
            share.mkdir()
            shutil.copy2(SCRIPT, script)
            script.chmod(0o755)
            config = sandbox / "server.conf"
            config.write_text(f"SHARE_ROOT={share}\nRETENTION_DAYS=30\n")
            target = share / "manuale.txt"
            target.write_text("contenuto recuperabile")
            digest = hashlib.sha256(target.read_bytes()).hexdigest()
            mtime = int(target.stat().st_mtime)
            payload = (
                b"CHECKED_DELETE_V1\0"
                b"2026-08-22--12-00-00-000001Z\0"
                b"device-a\0"
                b"1\0manuale.txt\0"
                + digest.encode()
                + b"\0"
                + str(mtime).encode()
                + b"\0"
            )
            environment = os.environ.copy()
            environment["NASBOX_TEST_FAILPOINT"] = "checked_delete_after_move"
            crashed = self.run_with_global_lock(
                sandbox, [str(script), "-c", str(config), "--checked-delete"],
                input=payload, capture_output=True, env=environment,
            )

            self.assertEqual(crashed.returncode, -9)
            self.assertFalse(target.exists())
            trash_files = list((share / ".sync-trash").rglob("manuale.txt-*"))
            self.assertEqual(len(trash_files), 1)

            recovered = self.run_with_global_lock(
                sandbox, [str(script), "-c", str(config), "--checked-delete"],
                input=payload, capture_output=True,
            )
            self.assertEqual(recovered.returncode, 0, recovered.stderr.decode())
            self.assertIn(b"manuale.txt\0ABSENT\0", recovered.stdout)
            journal = (sandbox / "state" / "transfer-journal.tsv").read_text()
            self.assertIn("manuale.txt", journal)

    def test_staging_publish_moves_only_new_files_journals_them_and_hides_staging(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            sandbox = Path(directory)
            script, share, config, staging = self.staging_fixture(sandbox)
            payload = self.staging_payload(staging, "publish-normal", [("folder/new.txt", b"new bytes")])
            lock = self.hold_global_lock(sandbox)
            try:
                result = subprocess.run(
                    [str(script), "-c", str(config), "--staging-publish"], input=payload, capture_output=True,
                )
            finally:
                self.release_lock(lock)
            self.assertEqual(result.returncode, 0, result.stderr.decode())
            self.assertEqual(result.stdout, b"STAGING_PUBLISH_V1\0OK\0" b"1\0")
            self.assertEqual((share / "folder" / "new.txt").read_bytes(), b"new bytes")
            self.assertFalse((staging / "folder" / "new.txt").exists())
            journal = (sandbox / "state" / "transfer-journal.tsv").read_text()
            self.assertIn("PUT\tfolder/new.txt", journal)
            listing = subprocess.run(
                [str(script), "-c", str(config), "--browse-list"], capture_output=True,
            )
            self.assertNotIn(b".nasbox-staging\0", listing.stdout)

    def test_staging_publish_v2_carries_causal_metadata_into_manifest(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            sandbox = Path(directory)
            script, share, config, staging = self.staging_fixture(sandbox)
            payload = self.staging_payload(staging, "publish-causal", [("causal.txt", b"causal")])
            fields = payload.split(b"\0")
            fields[0] = b"STAGING_PUBLISH_V2"
            fields.insert(9, b"device-a:2")
            lock = self.hold_global_lock(sandbox)
            try:
                result = subprocess.run(
                    [str(script), "-c", str(config), "--staging-publish"],
                    input=b"\0".join(fields), capture_output=True,
                )
            finally:
                self.release_lock(lock)
            manifest = subprocess.run(
                [str(script), "-c", str(config), "--manifest-export"], capture_output=True,
            )
        self.assertEqual(result.returncode, 0, result.stderr.decode())
        self.assertEqual(result.stdout, b"STAGING_PUBLISH_V2\0OK\0" b"1\0")
        self.assertIn(b"causal.txt\t", manifest.stdout)
        self.assertTrue(manifest.stdout.rstrip().endswith(b"\tdevice-a:2"))

    def test_staging_publish_refuses_unlocked_or_existing_targets_without_losing_staging(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            sandbox = Path(directory)
            script, share, config, staging = self.staging_fixture(sandbox)
            payload = self.staging_payload(staging, "publish-conflict", [("exists.txt", b"staged")])
            unlocked = subprocess.run(
                [str(script), "-c", str(config), "--staging-publish"], input=payload, capture_output=True,
            )
            self.assertNotEqual(unlocked.returncode, 0)
            self.assertTrue((staging / "exists.txt").exists())
            (share / "exists.txt").write_bytes(b"live")
            lock = self.hold_global_lock(sandbox)
            try:
                conflict = subprocess.run(
                    [str(script), "-c", str(config), "--staging-publish"], input=payload, capture_output=True,
                )
            finally:
                self.release_lock(lock)
            self.assertNotEqual(conflict.returncode, 0)
            self.assertEqual((share / "exists.txt").read_bytes(), b"live")
            self.assertEqual((staging / "exists.txt").read_bytes(), b"staged")

    def test_staging_publish_reclaims_identical_existing_target(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            sandbox = Path(directory)
            script, share, config, staging = self.staging_fixture(sandbox)
            payload = self.staging_payload(staging, "publish-already-done", [("exists.txt", b"same")])
            (share / "exists.txt").write_bytes(b"same")
            lock = self.hold_global_lock(sandbox)
            try:
                result = subprocess.run(
                    [str(script), "-c", str(config), "--staging-publish"],
                    input=payload, capture_output=True,
                )
            finally:
                self.release_lock(lock)

            self.assertEqual(result.returncode, 0, result.stderr.decode())
            self.assertEqual(result.stdout, b"STAGING_PUBLISH_V1\0OK\0" b"1\0")
            self.assertEqual((share / "exists.txt").read_bytes(), b"same")
            self.assertFalse(staging.exists())
            journal = (sandbox / "state" / "transfer-journal.tsv").read_text()
            self.assertNotIn("PUT\texists.txt", journal)

    def test_staging_publish_crash_recovery_completes_marker_move_and_journal(self) -> None:
        for failpoint in ("publish_after_marker", "publish_after_move"):
            with self.subTest(failpoint=failpoint), tempfile.TemporaryDirectory() as directory:
                sandbox = Path(directory)
                script, share, config, staging = self.staging_fixture(sandbox)
                payload = self.staging_payload(staging, f"publish-{failpoint}", [("one.txt", b"one"), ("two.txt", b"two")])
                lock = self.hold_global_lock(sandbox)
                environment = os.environ.copy()
                environment["NASBOX_TEST_FAILPOINT"] = failpoint
                try:
                    crashed = subprocess.run(
                        [str(script), "-c", str(config), "--staging-publish"], input=payload,
                        capture_output=True, env=environment,
                    )
                finally:
                    self.release_lock(lock)
                self.assertEqual(crashed.returncode, -9)
                journal_before_recovery = (sandbox / "state" / "transfer-journal.tsv").read_text()
                self.assertNotIn("PUT\tone.txt", journal_before_recovery)
                self.assertNotIn("PUT\ttwo.txt", journal_before_recovery)
                if failpoint == "publish_after_marker":
                    self.assertFalse((share / "one.txt").exists())
                    self.assertTrue((staging / "one.txt").exists())
                else:
                    self.assertTrue((share / "one.txt").exists())
                recovery_lock = self.hold_global_lock(sandbox)
                try:
                    recovered = subprocess.run(
                        [str(script), "-c", str(config), "--checked-delete"],
                        input=self.empty_checked_delete_payload(), capture_output=True,
                    )
                finally:
                    self.release_lock(recovery_lock)
                self.assertEqual(recovered.returncode, 0, recovered.stderr.decode())
                self.assertEqual((share / "one.txt").read_bytes(), b"one")
                self.assertEqual((share / "two.txt").read_bytes(), b"two")
                journal = (sandbox / "state" / "transfer-journal.tsv").read_text()
                self.assertEqual(journal.count("PUT\tone.txt"), 1)
                self.assertEqual(journal.count("PUT\ttwo.txt"), 1)
                self.assertFalse(list((sandbox / "state" / "transactions").glob("*.txn")))

    def test_staging_publish_crash_after_journal_keeps_files_and_recovers_marker(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            sandbox = Path(directory)
            script, share, config, staging = self.staging_fixture(sandbox)
            payload = self.staging_payload(staging, "publish-after-journal", [("done.txt", b"durable")])
            lock = self.hold_global_lock(sandbox)
            environment = os.environ.copy()
            environment["NASBOX_TEST_FAILPOINT"] = "publish_after_journal_append"
            try:
                crashed = subprocess.run(
                    [str(script), "-c", str(config), "--staging-publish"], input=payload,
                    capture_output=True, env=environment,
                )
            finally:
                self.release_lock(lock)
            self.assertEqual(crashed.returncode, -9)
            self.assertEqual((share / "done.txt").read_bytes(), b"durable")
            recovery_lock = self.hold_global_lock(sandbox)
            try:
                recovered = subprocess.run(
                    [str(script), "-c", str(config), "--checked-delete"],
                    input=self.empty_checked_delete_payload(), capture_output=True,
                )
            finally:
                self.release_lock(recovery_lock)
            self.assertEqual(recovered.returncode, 0, recovered.stderr.decode())
            self.assertFalse(list((sandbox / "state" / "transactions").glob("*.txn")))
            journal = (sandbox / "state" / "transfer-journal.tsv").read_text()
            self.assertEqual(journal.count("PUT\tdone.txt"), 1)

    def test_browse_delete_crash_after_move_recovers_all_directory_paths(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            sandbox = Path(directory)
            script = sandbox / "server.sh"
            share = sandbox / "share"
            share.mkdir()
            shutil.copy2(SCRIPT, script)
            script.chmod(0o755)
            config = sandbox / "server.conf"
            config.write_text(f"SHARE_ROOT={share}\nRETENTION_DAYS=30\n")
            (share / "folder").mkdir()
            (share / "folder" / "one.txt").write_text("one")
            (share / "folder" / "nested").mkdir()
            (share / "folder" / "nested" / "two.txt").write_text("two")
            payload = (
                b"BROWSE_DELETE_V1\0"
                b"2026-08-22--12-00-00-000002Z\0"
                b"device-a\0folder\0"
            )
            environment = os.environ.copy()
            environment["NASBOX_TEST_FAILPOINT"] = "browse_delete_after_move"
            crashed = self.run_with_global_lock(
                sandbox, [str(script), "-c", str(config), "--browse-delete"],
                input=payload, capture_output=True, env=environment,
            )

            self.assertEqual(crashed.returncode, -9)
            self.assertFalse((share / "folder").exists())
            self.assertTrue((share / ".sync-trash").is_dir())

            recovered = self.run_with_global_lock(
                sandbox, [str(script), "-c", str(config), "--checked-delete"],
                input=self.empty_checked_delete_payload(), capture_output=True,
            )
            self.assertEqual(recovered.returncode, 0, recovered.stderr.decode())
            journal = (sandbox / "state" / "transfer-journal.tsv").read_text()
            self.assertIn("folder/one.txt", journal)
            self.assertIn("folder/nested/two.txt", journal)

    def test_browse_rename_crash_after_move_recovers_source_tombstone(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            sandbox = Path(directory)
            script = sandbox / "server.sh"
            share = sandbox / "share"
            share.mkdir()
            shutil.copy2(SCRIPT, script)
            script.chmod(0o755)
            config = sandbox / "server.conf"
            config.write_text(f"SHARE_ROOT={share}\nRETENTION_DAYS=30\n")
            (share / "old.txt").write_text("renamed")
            payload = (
                b"BROWSE_RENAME_V1\0"
                b"2026-08-22--12-00-00-000003Z\0"
                b"device-a\0old.txt\0new.txt\0"
            )
            environment = os.environ.copy()
            environment["NASBOX_TEST_FAILPOINT"] = "browse_rename_after_move"
            crashed = self.run_with_global_lock(
                sandbox, [str(script), "-c", str(config), "--browse-rename"],
                input=payload, capture_output=True, env=environment,
            )

            self.assertEqual(crashed.returncode, -9)
            self.assertFalse((share / "old.txt").exists())
            self.assertTrue((share / "new.txt").is_file())

            recovered = self.run_with_global_lock(
                sandbox, [str(script), "-c", str(config), "--checked-delete"],
                input=self.empty_checked_delete_payload(), capture_output=True,
            )
            self.assertEqual(recovered.returncode, 0, recovered.stderr.decode())
            journal = (sandbox / "state" / "transfer-journal.tsv").read_text()
            self.assertIn("old.txt", journal)

    def test_browse_rename_crash_after_journal_keeps_moved_file(self) -> None:
        """A completed move must never be rolled back after its journal commit."""
        with tempfile.TemporaryDirectory() as directory:
            sandbox = Path(directory)
            script = sandbox / "server.sh"
            share = sandbox / "share"
            share.mkdir()
            shutil.copy2(SCRIPT, script)
            script.chmod(0o755)
            config = sandbox / "server.conf"
            config.write_text(f"SHARE_ROOT={share}\nRETENTION_DAYS=30\n")
            (share / "old.txt").write_text("must remain moved")
            payload = (
                b"BROWSE_RENAME_V1\0"
                b"2026-08-22--12-00-00-000004Z\0"
                b"device-a\0old.txt\0new.txt\0"
            )
            environment = os.environ.copy()
            environment["NASBOX_TEST_FAILPOINT"] = "browse_rename_after_journal"
            crashed = self.run_with_global_lock(
                sandbox, [str(script), "-c", str(config), "--browse-rename"],
                input=payload, capture_output=True, env=environment,
            )

            self.assertEqual(crashed.returncode, -9)
            self.assertFalse((share / "old.txt").exists())
            self.assertEqual((share / "new.txt").read_text(), "must remain moved")

            recovered = self.run_with_global_lock(
                sandbox, [str(script), "-c", str(config), "--checked-delete"],
                input=self.empty_checked_delete_payload(), capture_output=True,
            )
            self.assertEqual(recovered.returncode, 0, recovered.stderr.decode())
            self.assertFalse((share / "old.txt").exists())
            self.assertEqual((share / "new.txt").read_text(), "must remain moved")
            self.assertFalse(list((sandbox / "state" / "transactions").glob("*.txn")))

    def test_local_directory_rename_crash_after_move_recovers_both_journal_sides(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            sandbox = Path(directory)
            script = sandbox / "server.sh"
            share = sandbox / "share"
            share.mkdir()
            shutil.copy2(SCRIPT, script)
            script.chmod(0o755)
            config = sandbox / "server.conf"
            config.write_text(f"SHARE_ROOT={share}\nRETENTION_DAYS=30\n")
            (share / "old-dir").mkdir()
            (share / "old-dir" / "file.txt").write_text("directory rename")
            payload = b"\0".join([
                b"BROWSE_RENAME_V2", b"2026-08-22--12-00-00-000005Z", b"device-a",
                b"old-dir", b"new-dir", b"DIR", b"", b"0", b"0",
            ]) + b"\0"
            environment = os.environ.copy()
            environment["NASBOX_TEST_FAILPOINT"] = "browse_rename_after_move"
            crashed = self.run_with_global_lock(
                sandbox, [str(script), "-c", str(config), "--browse-rename"],
                input=payload, capture_output=True, env=environment,
            )
            self.assertEqual(crashed.returncode, -9)
            self.assertFalse((share / "old-dir").exists())
            self.assertTrue((share / "new-dir" / "file.txt").is_file())

            recovered = self.run_with_global_lock(
                sandbox, [str(script), "-c", str(config), "--checked-delete"],
                input=self.empty_checked_delete_payload(), capture_output=True,
            )
            self.assertEqual(recovered.returncode, 0, recovered.stderr.decode())
            journal = (sandbox / "state" / "transfer-journal.tsv").read_text()
            self.assertIn("DELETE\told-dir/file.txt\t", journal)
            self.assertIn("PUT\tnew-dir/file.txt\t", journal)
            self.assertFalse(list((sandbox / "state" / "transactions").glob("*.txn")))

    def test_file_states_reuses_manifest_digest_when_metadata_matches(self) -> None:
        """Known files must not be re-hashed for every large batch."""
        with tempfile.TemporaryDirectory() as directory:
            sandbox = Path(directory)
            script = sandbox / "server.sh"
            share = sandbox / "share"
            share.mkdir()
            shutil.copy2(SCRIPT, script)
            script.chmod(0o755)
            config = sandbox / "server.conf"
            config.write_text(f"SHARE_ROOT={share}\nRETENTION_DAYS=30\n")
            target = share / "cached.txt"
            target.write_text("already journaled")
            digest = hashlib.sha256(target.read_bytes()).hexdigest()
            size = target.stat().st_size
            mtime_ns = int(target.stat().st_mtime) * 1_000_000_000
            state = sandbox / "state"
            state.mkdir()
            (state / "manifest.tsv").write_text(
                f"NASBOX_MANIFEST_V1\ncached.txt\t{digest}\t{size}\t{mtime_ns}\tdevice-a\t1\n"
            )
            # If the command reaches sha256sum this fake executable fails. A
            # metadata-identical manifest entry must therefore still succeed.
            fake_bin = sandbox / "bin"
            fake_bin.mkdir()
            fake_sha = fake_bin / "sha256sum"
            fake_sha.write_text("#!/bin/sh\nexit 1\n")
            fake_sha.chmod(0o755)
            environment = os.environ.copy()
            environment["PATH"] = f"{fake_bin}:{environment['PATH']}"
            result = subprocess.run(
                [str(script), "-c", str(config), "--file-states"],
                input=b"FILE_STATES_V1\0" b"1\0cached.txt\0",
                capture_output=True, env=environment,
            )

            self.assertEqual(result.returncode, 0, result.stderr.decode())
            self.assertEqual(
                result.stdout,
                b"FILE_STATES_V1\0" b"1\0cached.txt\0FILE\0"
                + digest.encode() + b"\0" + str(size).encode() + b"\0"
                + str(mtime_ns).encode() + b"\0",
            )


if __name__ == "__main__":
    unittest.main()
