from __future__ import annotations

import shutil
import subprocess
import tempfile
import threading
import time
import unittest
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]
SCRIPT = ROOT / "sync-daemon-server.sh"


class ChangeWaitTests(unittest.TestCase):
    def fixture(self, sandbox: Path, revision: str) -> tuple[Path, Path]:
        script = sandbox / "server.sh"
        share = sandbox / "share"
        share.mkdir()
        shutil.copy2(SCRIPT, script)
        script.chmod(0o755)
        config = sandbox / "server.conf"
        config.write_text(f"SHARE_ROOT={share}\nRETENTION_DAYS=30\n")
        state = sandbox / "state"
        state.mkdir()
        (state / "manifest.revision").write_text(revision)
        return script, config

    @staticmethod
    def wait(script: Path, config: Path, revision: str, timeout: str) -> subprocess.CompletedProcess:
        return subprocess.run(
            [str(script), "-c", str(config), "--wait-for-revision", revision, timeout],
            capture_output=True, text=True, timeout=60,
        )

    def test_returns_changed_immediately_when_revision_advanced(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            script, config = self.fixture(Path(directory), "5")
            result = self.wait(script, config, "3", "30")
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertIn("RESULT=CHANGED", result.stdout)
            self.assertIn("REVISION=5", result.stdout)

    def test_times_out_when_revision_unchanged(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            script, config = self.fixture(Path(directory), "5")
            started = time.monotonic()
            result = self.wait(script, config, "5", "1")
            elapsed = time.monotonic() - started
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertIn("RESULT=TIMEOUT", result.stdout)
            self.assertIn("REVISION=5", result.stdout)
            self.assertGreaterEqual(elapsed, 0.9)

    def test_wakes_up_when_revision_changes_mid_wait(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            sandbox = Path(directory)
            script, config = self.fixture(sandbox, "5")

            def bump() -> None:
                time.sleep(0.5)
                (sandbox / "state" / "manifest.revision").write_text("9")

            thread = threading.Thread(target=bump)
            thread.start()
            try:
                result = self.wait(script, config, "5", "20")
            finally:
                thread.join()
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertIn("RESULT=CHANGED", result.stdout)
            self.assertIn("REVISION=9", result.stdout)

    def test_rejects_out_of_range_timeout(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            script, config = self.fixture(Path(directory), "5")
            result = self.wait(script, config, "5", "99999999")
            self.assertNotEqual(result.returncode, 0)
            self.assertIn("RESULT=ERROR", result.stdout)


if __name__ == "__main__":
    unittest.main()
