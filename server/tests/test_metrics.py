from __future__ import annotations

import shutil
import subprocess
import tempfile
import unittest
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]
SCRIPT = ROOT / "sync-daemon-server.sh"


class MetricsCommandTests(unittest.TestCase):
    def test_metrics_is_machine_readable_and_does_not_create_state(self) -> None:
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
                [str(script), "-c", str(config), "--metrics"],
                capture_output=True, text=True, timeout=5,
            )

            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertEqual(result.stdout.splitlines()[0], "NASBOX_METRICS_V1")
            keys = {line.split("=", 1)[0] for line in result.stdout.splitlines()[1:]}
            self.assertTrue({
                "UPTIME_SECONDS", "LOAD_1", "LOAD_5", "LOAD_15",
                "CPU_TOTAL_TICKS", "CPU_IDLE_TICKS", "CPU_IOWAIT_TICKS",
                "MEM_TOTAL_BYTES", "MEM_AVAILABLE_BYTES", "DISK_AVAILABLE_BYTES",
                "DISK_READ_BYTES", "DISK_WRITE_BYTES",
                "NET_RX_BYTES", "NET_TX_BYTES",
            }.issubset(keys))
            self.assertFalse((sandbox / "state").exists())


if __name__ == "__main__":
    unittest.main()
