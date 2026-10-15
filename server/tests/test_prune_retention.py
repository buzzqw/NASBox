from __future__ import annotations

import os
import shutil
import subprocess
import tempfile
import unittest
from datetime import datetime, timedelta, timezone
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]
SCRIPT = ROOT / "sync-daemon-server.sh"


UTC_RUN_TS_FORMAT = "%Y-%m-%d--%H-%M-%S-%fZ"


def run_ts(days_ago: float) -> str:
    """The exact suffix the client appends: UTC, microseconds, trailing Z."""
    moment = datetime.now(timezone.utc) - timedelta(days=days_ago)
    return moment.strftime(UTC_RUN_TS_FORMAT)


class PruneRetentionTests(unittest.TestCase):
    def fixture(self, sandbox: Path, retention: int = 30) -> tuple[Path, Path, Path, Path]:
        script = sandbox / "server.sh"
        share = sandbox / "share"
        share.mkdir()
        shutil.copy2(SCRIPT, script)
        script.chmod(0o755)
        config = sandbox / "server.conf"
        config.write_text(f"SHARE_ROOT={share}\nRETENTION_DAYS={retention}\n")
        trash = share / ".sync-trash"
        trash.mkdir()
        return script, share, trash, config

    @staticmethod
    def run_once(script: Path, config: Path) -> subprocess.CompletedProcess:
        return subprocess.run(
            [str(script), "-c", str(config), "--run-once"],
            capture_output=True, text=True, timeout=120,
        )

    def test_old_versions_pruned_and_recent_kept(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            sandbox = Path(directory)
            script, _share, trash, config = self.fixture(sandbox)
            old = run_ts(100)
            fresh = run_ts(1)
            nested = trash / "sub"
            nested.mkdir()
            (trash / f"a.txt-{old}").write_text("old")
            (trash / f"b.txt-{fresh}").write_text("fresh")
            (nested / f"c.txt-{old}").write_text("old")

            result = self.run_once(script, config)

            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertFalse((trash / f"a.txt-{old}").exists())
            self.assertFalse((nested / f"c.txt-{old}").exists())
            self.assertTrue((trash / f"b.txt-{fresh}").exists(), "recent version must survive")
            # A removal happened, so the now-empty subdirectory is reclaimed too.
            self.assertFalse(nested.exists())

    def test_legacy_name_without_timestamp_falls_back_to_mtime(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            sandbox = Path(directory)
            script, _share, trash, config = self.fixture(sandbox)
            legacy = trash / "referto.pdf"
            legacy.write_text("legacy")
            ancient = (datetime.now(timezone.utc) - timedelta(days=200)).timestamp()
            os.utime(legacy, (ancient, ancient))

            result = self.run_once(script, config)

            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertFalse(legacy.exists())

    def test_epoch_conversion_matches_date_for_many_instants(self) -> None:
        """The pure-bash epoch math must agree with `date -u` exactly.

        Runs the retention decision the daemon makes: a version stamped one day
        older than the threshold is removed, one day newer is kept, for several
        dates across leap years and month boundaries.
        """
        with tempfile.TemporaryDirectory() as directory:
            sandbox = Path(directory)
            script, _share, trash, config = self.fixture(sandbox, retention=30)
            survivors = []
            for days_ago in (31, 32, 60, 365, 366, 400):
                name = f"f{days_ago}.bin-{run_ts(days_ago)}"
                (trash / name).write_text("x")
            for days_ago in (0, 1, 29):
                name = f"keep{days_ago}.bin-{run_ts(days_ago)}"
                (trash / name).write_text("x")
                survivors.append(trash / name)

            result = self.run_once(script, config)

            self.assertEqual(result.returncode, 0, result.stderr)
            remaining = {p.name for p in trash.iterdir()}
            self.assertEqual(remaining, {p.name for p in survivors})

    def test_second_pass_is_idempotent(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            sandbox = Path(directory)
            script, _share, trash, config = self.fixture(sandbox)
            (trash / f"a.txt-{run_ts(100)}").write_text("old")
            fresh_name = f"b.txt-{run_ts(1)}"
            (trash / fresh_name).write_text("fresh")

            first = self.run_once(script, config)
            second = self.run_once(script, config)

            self.assertEqual(first.returncode, 0, first.stderr)
            self.assertEqual(second.returncode, 0, second.stderr)
            self.assertEqual([p.name for p in trash.iterdir()], [fresh_name])
            # The second pass must skip the whole metadata walk: the oldest
            # version is still far inside the retention window.
            self.assertIn("Pruning saltato", second.stderr)

    def test_watermark_is_written_after_a_complete_scan(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            sandbox = Path(directory)
            script, _share, trash, config = self.fixture(sandbox, retention=30)
            (trash / f"a.txt-{run_ts(100)}").write_text("old")
            (trash / f"b.txt-{run_ts(1)}").write_text("fresh")

            result = self.run_once(script, config)

            self.assertEqual(result.returncode, 0, result.stderr)
            watermark = (sandbox / "state" / "trash-watermark").read_text().split()
            self.assertEqual(len(watermark), 2)
            self.assertTrue(all(value.isdigit() for value in watermark))

    def test_past_watermark_triggers_a_new_scan(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            sandbox = Path(directory)
            script, _share, trash, config = self.fixture(sandbox, retention=30)
            state = sandbox / "state"
            state.mkdir(exist_ok=True)
            (state / "trash-watermark").write_text("1 1\n")  # ancient scan
            old = trash / f"c.txt-{run_ts(100)}"
            old.write_text("old")

            result = self.run_once(script, config)

            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertFalse(old.exists())


if __name__ == "__main__":
    unittest.main()
