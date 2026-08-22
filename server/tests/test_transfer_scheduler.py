from __future__ import annotations

import subprocess
import tempfile
import time
import unittest
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]
SCRIPT = ROOT / "sync-daemon-server.sh"


class TransferSchedulerTests(unittest.TestCase):
    def test_priority_ticket_waits_for_current_lease(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            sandbox = Path(directory)
            share = sandbox / "share"
            share.mkdir()
            script = sandbox / "server.sh"
            script.write_bytes(SCRIPT.read_bytes())
            script.chmod(0o755)
            config = sandbox / "server.conf"
            config.write_text(f"SHARE_ROOT={share}\nRETENTION_DAYS=30\n")

            first = subprocess.Popen(
                [str(script), "-c", str(config), "--transfer-wait"],
                stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE,
            )
            assert first.stdin is not None and first.stdout is not None
            first.stdin.write(b"TRANSFER_WAIT_V1\0device-a\0" b"1\0request-a-123456\0host-a\0")
            first.stdin.flush()
            self.assertEqual(first.stdout.readline(), b"NASBOX_LOCKED\n")

            second = subprocess.Popen(
                [str(script), "-c", str(config), "--transfer-wait"],
                stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE,
            )
            assert second.stdin is not None and second.stdout is not None
            second.stdin.write(b"TRANSFER_WAIT_V1\0device-b\0" b"0\0request-b-123456\0host-b\0")
            second.stdin.flush()
            time.sleep(0.3)
            self.assertIsNone(second.poll())

            first.stdin.close()
            first.wait(timeout=5)
            self.assertEqual(second.stdout.readline(), b"NASBOX_LOCKED\n")
            second.stdin.close()
            second.wait(timeout=5)
            self.assertEqual(first.returncode, 0)
            self.assertEqual(second.returncode, 0)
            for process in (first, second):
                if process.stdout is not None:
                    process.stdout.close()
                if process.stderr is not None:
                    process.stderr.close()


if __name__ == "__main__":
    unittest.main()
