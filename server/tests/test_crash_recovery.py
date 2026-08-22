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


if __name__ == "__main__":
    unittest.main()
