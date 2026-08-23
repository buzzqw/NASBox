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


if __name__ == "__main__":
    unittest.main()
