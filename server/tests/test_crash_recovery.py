from __future__ import annotations

import hashlib
import os
import shutil
import subprocess
import tempfile
import unittest
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]
SCRIPT = ROOT / "sync-daemon-server.sh"


class ServerCrashRecoveryTests(unittest.TestCase):
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
            crashed = subprocess.run(
                [str(script), "-c", str(config), "--checked-delete"],
                input=payload, capture_output=True, env=environment,
            )

            self.assertEqual(crashed.returncode, -9)
            self.assertFalse(target.exists())
            trash_files = list((share / ".sync-trash").rglob("manuale.txt-*"))
            self.assertEqual(len(trash_files), 1)

            recovered = subprocess.run(
                [str(script), "-c", str(config), "--checked-delete"],
                input=payload, capture_output=True,
            )
            self.assertEqual(recovered.returncode, 0, recovered.stderr.decode())
            self.assertIn(b"manuale.txt\0ABSENT\0", recovered.stdout)
            journal = (sandbox / "state" / "transfer-journal.tsv").read_text()
            self.assertIn("manuale.txt", journal)

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
            crashed = subprocess.run(
                [str(script), "-c", str(config), "--browse-delete"],
                input=payload, capture_output=True, env=environment,
            )

            self.assertEqual(crashed.returncode, -9)
            self.assertFalse((share / "folder").exists())
            self.assertTrue((share / ".sync-trash").is_dir())

            recovered = subprocess.run(
                [str(script), "-c", str(config), "--checked-delete"],
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
            crashed = subprocess.run(
                [str(script), "-c", str(config), "--browse-rename"],
                input=payload, capture_output=True, env=environment,
            )

            self.assertEqual(crashed.returncode, -9)
            self.assertFalse((share / "old.txt").exists())
            self.assertTrue((share / "new.txt").is_file())

            recovered = subprocess.run(
                [str(script), "-c", str(config), "--checked-delete"],
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
            crashed = subprocess.run(
                [str(script), "-c", str(config), "--browse-rename"],
                input=payload, capture_output=True, env=environment,
            )

            self.assertEqual(crashed.returncode, -9)
            self.assertFalse((share / "old.txt").exists())
            self.assertEqual((share / "new.txt").read_text(), "must remain moved")

            recovered = subprocess.run(
                [str(script), "-c", str(config), "--checked-delete"],
                input=self.empty_checked_delete_payload(), capture_output=True,
            )
            self.assertEqual(recovered.returncode, 0, recovered.stderr.decode())
            self.assertFalse((share / "old.txt").exists())
            self.assertEqual((share / "new.txt").read_text(), "must remain moved")
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
