from __future__ import annotations

import os
import select
import subprocess
import tempfile
import time
import unittest
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]
SCRIPT = ROOT / "sync-daemon-server.sh"


class TransferSchedulerTests(unittest.TestCase):
    def test_pruning_scan_does_not_hold_transfer_lock(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            sandbox = Path(directory)
            share = sandbox / "share"
            trash = share / ".sync-trash"
            trash.mkdir(parents=True)
            for index in range(2000):
                (trash / f"version-{index}-2000-01-01--00-00-00-000000Z").write_text("old")
            script = sandbox / "server.sh"
            script.write_bytes(SCRIPT.read_bytes())
            script.chmod(0o755)
            config = sandbox / "server.conf"
            config.write_text(f"SHARE_ROOT={share}\nRETENTION_DAYS=1\n")

            pruning = subprocess.Popen(
                [str(script), "-c", str(config), "--run-once"],
                stdout=subprocess.PIPE, stderr=subprocess.PIPE,
            )
            assert pruning.stderr is not None
            ready, _, _ = select.select([pruning.stderr], [], [], 5)
            self.assertTrue(ready, "pruning did not begin its metadata scan")
            self.assertIn(b"avvio pass di pruning", pruning.stderr.readline())
            transfer = subprocess.Popen(
                [str(script), "-c", str(config), "--transfer-wait"],
                stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE,
            )
            try:
                assert transfer.stdin is not None and transfer.stdout is not None
                transfer.stdin.write(b"TRANSFER_WAIT_V1\0device-a\0" b"1\0request-a-123456\0host-a\0")
                transfer.stdin.flush()
                ready, _, _ = select.select([transfer.stdout], [], [], 5)
                self.assertTrue(ready, "the pruning metadata scan monopolized sync-transfer.lock")
                self.assertEqual(transfer.stdout.readline(), b"NASBOX_LOCKED\n")
            finally:
                if transfer.stdin is not None:
                    transfer.stdin.close()
                transfer.wait(timeout=5)
                if pruning.poll() is None:
                    pruning.terminate()
                pruning.wait(timeout=5)
                for process in (pruning, transfer):
                    if process.stdout is not None:
                        process.stdout.close()
                    if process.stderr is not None:
                        process.stderr.close()

    def test_pruning_limit_does_not_leave_find_blocked(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            sandbox = Path(directory)
            share = sandbox / "share"
            trash = share / ".sync-trash"
            trash.mkdir(parents=True)
            old_time = 946684800
            for index in range(600):
                file = trash / f"version-{index}.txt"
                file.write_text("old")
                os.utime(file, (old_time, old_time))
            script = sandbox / "server.sh"
            script.write_bytes(SCRIPT.read_bytes())
            script.chmod(0o755)
            config = sandbox / "server.conf"
            config.write_text(
                f"SHARE_ROOT={share}\nRETENTION_DAYS=1\nPRUNE_MAX_FILES_PER_PASS=1\n"
            )

            result = subprocess.run(
                [str(script), "-c", str(config), "--run-once"],
                capture_output=True, text=True, timeout=10,
            )
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertEqual(len(list(trash.glob("version-*.txt"))), 599)

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
