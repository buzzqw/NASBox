from __future__ import annotations

import shutil
import subprocess
import tempfile
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
SCRIPT = ROOT / "sync-daemon-server.sh"


class DevicesCommandTests(unittest.TestCase):
    def test_devices_is_read_only_and_machine_readable(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            sandbox = Path(directory)
            share = sandbox / "share"
            share.mkdir()
            script = sandbox / "server.sh"
            shutil.copy2(SCRIPT, script)
            script.chmod(0o755)
            config = sandbox / "server.conf"
            config.write_text(f"SHARE_ROOT={share}\nRETENTION_DAYS=30\n")

            result = subprocess.run(
                [str(script), "-c", str(config), "--devices"],
                capture_output=True, text=True, timeout=5,
            )
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertEqual(result.stdout.splitlines()[0], "NASBOX_DEVICES_V1")
            self.assertFalse((sandbox / "state").exists())

    def test_devices_reports_recent_journal_devices(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            sandbox = Path(directory)
            share = sandbox / "share"
            share.mkdir()
            script = sandbox / "server.sh"
            shutil.copy2(SCRIPT, script)
            script.chmod(0o755)
            config = sandbox / "server.conf"
            config.write_text(f"SHARE_ROOT={share}\nRETENTION_DAYS=30\n")
            state = sandbox / "state"
            state.mkdir()
            (state / "transfer-journal.tsv").write_text(
                "NASBOX_JOURNAL_V1\n"
                "BEGIN\ttx1\t100\tdevA\n"
                "BEGIN\ttx2\t200\tdevB\n"
                "BEGIN\ttx3\t300\tdevA\n",
                encoding="utf-8",
            )
            result = subprocess.run(
                [str(script), "-c", str(config), "--devices"],
                capture_output=True, text=True, timeout=5,
            )
            self.assertEqual(result.returncode, 0, result.stderr)
            lines = result.stdout.splitlines()
            self.assertEqual(lines[0], "NASBOX_DEVICES_V1")
            rows = [line.split("|") for line in lines[1:]]
            self.assertIn(["devA", "300", "2"], rows)
            self.assertIn(["devB", "200", "1"], rows)


if __name__ == "__main__":
    unittest.main()
