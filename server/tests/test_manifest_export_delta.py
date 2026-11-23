from __future__ import annotations

import shutil
import subprocess
import tempfile
import unittest
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]
SCRIPT = ROOT / "sync-daemon-server.sh"

DIGEST = "a" * 64
OTHER_DIGEST = "b" * 64


def row(path: str, digest: str = DIGEST, size: int = 1, mtime: int = 100,
        device: str = "device-a", timestamp: int = 10) -> str:
    return f"{path}\t{digest}\t{size}\t{mtime}\t{device}\t{timestamp}"


class ManifestExportDeltaTests(unittest.TestCase):
    def fixture(self, sandbox: Path, revision: int = 1, manifest_rows: str = "",
                journal: str = "", snapshot_revision: int | None = None,
                snapshot_rows: str = "") -> tuple[Path, Path, Path]:
        script = sandbox / "server.sh"
        share = sandbox / "share"
        share.mkdir()
        shutil.copy2(SCRIPT, script)
        script.chmod(0o755)
        config = sandbox / "server.conf"
        config.write_text(f"SHARE_ROOT={share}\nRETENTION_DAYS=30\n")
        state = sandbox / "state"
        state.mkdir()
        (state / "manifest.tsv").write_text("NASBOX_MANIFEST_V1\n" + manifest_rows)
        (state / "transfer-journal.tsv").write_text(journal or "NASBOX_JOURNAL_V1\n")
        (state / "manifest.revision").write_text(f"{revision}\n")
        if snapshot_revision is not None:
            (state / "manifest.snapshot").write_text(
                "NASBOX_MANIFEST_SNAPSHOT_V1\n"
                f"REVISION={snapshot_revision}\n"
                + snapshot_rows
            )
        return script, config, state

    @staticmethod
    def run_delta(script: Path, config: Path, since: object) -> subprocess.CompletedProcess:
        return subprocess.run(
            [str(script), "-c", str(config), "--manifest-export-delta", str(since)],
            capture_output=True, text=True, timeout=60,
        )

    @staticmethod
    def data_lines(output: str) -> list[str]:
        """Header values, changed rows and removed paths of a delta response."""
        return output.splitlines()

    def test_plain_export_without_argument_is_unchanged_and_read_only(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            sandbox = Path(directory)
            journal = (
                "NASBOX_JOURNAL_V1\n"
                "BEGIN\ttx-1\t10\tdevice-a\n"
                f"PUT\tfrom-journal.txt\t{DIGEST}\t3\t100\tdevice-a\t10\n"
                "COMMIT\ttx-1\n"
            )
            script, config, state = self.fixture(
                sandbox, revision=4,
                manifest_rows=row("from-manifest.txt") + "\n",
                journal=journal,
            )

            result = subprocess.run(
                [str(script), "-c", str(config), "--manifest-export"],
                capture_output=True, text=True, timeout=60,
            )

            self.assertEqual(result.returncode, 0, result.stderr)
            lines = result.stdout.splitlines()
            self.assertEqual(lines[0], "NASBOX_MANIFEST_V1")
            self.assertNotIn("NASBOX_MANIFEST_DELTA_V1", result.stdout)
            self.assertNotIn("FALLBACK_FULL", result.stdout)
            self.assertIn(row("from-manifest.txt"), lines)
            # A V1 journal PUT carries no causal field, so the render keeps the
            # empty seventh field as a trailing tab -- exactly as before.
            self.assertIn(row("from-journal.txt", size=3) + "\t", lines)
            self.assertFalse((state / "manifest.snapshot").exists())
            self.assertEqual((state / "manifest.revision").read_text().strip(), "4")

    def test_delta_with_same_revision_is_empty_and_touches_no_state(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            sandbox = Path(directory)
            script, config, state = self.fixture(
                sandbox, revision=7, manifest_rows=row("a.txt") + "\n",
                snapshot_revision=7, snapshot_rows=row("a.txt") + "\n",
            )
            snapshot = state / "manifest.snapshot"
            before = snapshot.read_bytes()

            result = self.run_delta(script, config, 7)

            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertEqual(result.stdout, (
                "NASBOX_MANIFEST_DELTA_V1\n"
                "BASE_REVISION=7\n"
                "CURRENT_REVISION=7\n"
                "CHANGED=0\n"
                "REMOVED=\n"
            ))
            self.assertEqual(snapshot.read_bytes(), before)
            self.assertEqual((state / "manifest.revision").read_text().strip(), "7")

    def test_delta_rejects_invalid_and_future_revisions(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            sandbox = Path(directory)
            script, config, _state = self.fixture(sandbox, revision=4)

            for since in ("abc", "-1", "1.5", "", "1e3", "5", "9999999999999999999999"):
                with self.subTest(since=since):
                    result = self.run_delta(script, config, since)
                    self.assertEqual(result.returncode, 75, (since, result.stdout, result.stderr))
                    self.assertEqual(result.stdout, "")
                    self.assertTrue(result.stderr.strip())

    def test_delta_falls_back_to_full_then_builds_real_delta(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            sandbox = Path(directory)
            script, config, state = self.fixture(
                sandbox, revision=4,
                manifest_rows=row("a.txt") + "\n" + row("b.txt") + "\n",
            )

            first = self.run_delta(script, config, 1)
            self.assertEqual(first.returncode, 0, first.stderr)
            lines = self.data_lines(first.stdout)
            self.assertEqual(lines[0], "NASBOX_MANIFEST_DELTA_V1")
            self.assertEqual(lines[1], "BASE_REVISION=1")
            self.assertEqual(lines[2], "CURRENT_REVISION=4")
            self.assertEqual(lines[3], "FALLBACK_FULL=1")
            self.assertEqual(lines[4], "CHANGED=2")
            self.assertEqual(set(lines[5:7]), {row("a.txt"), row("b.txt")})
            self.assertEqual(lines[7], "REMOVED=")
            self.assertEqual(len(lines), 8)
            self.assertIn("REVISION=4", (state / "manifest.snapshot").read_text())

            # Advance the revision and change a.txt, drop b.txt, add c.txt.
            (state / "manifest.tsv").write_text(
                "NASBOX_MANIFEST_V1\n"
                + row("a.txt", digest=OTHER_DIGEST) + "\n"
                + row("c.txt") + "\n"
            )
            (state / "manifest.revision").write_text("5\n")

            second = self.run_delta(script, config, 4)
            self.assertEqual(second.returncode, 0, second.stderr)
            lines = self.data_lines(second.stdout)
            self.assertEqual(lines[0], "NASBOX_MANIFEST_DELTA_V1")
            self.assertEqual(lines[1], "BASE_REVISION=4")
            self.assertEqual(lines[2], "CURRENT_REVISION=5")
            self.assertNotIn("FALLBACK_FULL=1", lines)
            self.assertEqual(lines[3], "CHANGED=2")
            self.assertEqual(set(lines[4:6]), {
                row("a.txt", digest=OTHER_DIGEST), row("c.txt"),
            })
            self.assertEqual(lines[6], "REMOVED=")
            self.assertEqual(lines[7:], ["b.txt"])

    def test_delta_preserves_escaping_for_changed_rows_and_removals(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            sandbox = Path(directory)
            escaped_tab = "dir/tab%09file.txt"
            escaped_newline = "dir/new%0Aline.txt"
            script, config, _state = self.fixture(
                sandbox, revision=2,
                snapshot_revision=2,
                snapshot_rows=(
                    row("keep.txt") + "\n" + row(escaped_tab) + "\n"
                    + row("gone.txt") + "\n"
                ),
                manifest_rows=(
                    row("keep.txt") + "\n"
                    + row("changed.txt") + "\n"
                    + row(escaped_newline, digest=OTHER_DIGEST, size=9) + "\n"
                ),
            )
            # Move to a newer revision so the delta is actually computed.
            (sandbox / "state" / "manifest.revision").write_text("3\n")

            result = self.run_delta(script, config, 2)

            self.assertEqual(result.returncode, 0, result.stderr)
            lines = self.data_lines(result.stdout)
            self.assertEqual(lines[3], "CHANGED=2")
            self.assertEqual(set(lines[4:6]), {
                row("changed.txt"),
                row(escaped_newline, digest=OTHER_DIGEST, size=9),
            })
            removed_at = lines.index("REMOVED=")
            self.assertEqual(lines[removed_at + 1:], [escaped_tab, "gone.txt"])

    def test_delta_reads_rendered_manifest_when_journal_has_entries(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            sandbox = Path(directory)
            journal = (
                "NASBOX_JOURNAL_V1\n"
                "BEGIN\ttx-1\t10\tdevice-a\n"
                f"PUT\tj.txt\t{DIGEST}\t3\t100\tdevice-a\t10\n"
                "COMMIT\ttx-1\n"
            )
            script, config, _state = self.fixture(
                sandbox, revision=8, manifest_rows=row("base.txt") + "\n",
                journal=journal, snapshot_revision=7,
                snapshot_rows=row("base.txt") + "\n",
            )

            result = self.run_delta(script, config, 7)

            self.assertEqual(result.returncode, 0, result.stderr)
            lines = result.stdout.splitlines()
            self.assertEqual(lines[3], "CHANGED=1")
            self.assertEqual(lines[4], row("j.txt", size=3) + "\t")
            self.assertEqual(lines[5], "REMOVED=")

    def test_manifest_export_with_argument_matches_delta_command(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            sandbox = Path(directory)
            manifest = row("a.txt") + "\n" + row("b.txt") + "\n"
            script, config, state = self.fixture(
                sandbox, revision=6, manifest_rows=manifest,
                snapshot_revision=5, snapshot_rows=row("a.txt") + "\n",
            )
            snapshot_bytes = (state / "manifest.snapshot").read_bytes()

            extended = subprocess.run(
                [str(script), "-c", str(config), "--manifest-export", "5"],
                capture_output=True, text=True, timeout=60,
            )
            # Each export advances the snapshot to the current revision; restore
            # the same base before exercising the named command.
            (state / "manifest.snapshot").write_bytes(snapshot_bytes)
            named = self.run_delta(script, config, 5)

            self.assertEqual(extended.returncode, 0, extended.stderr)
            self.assertEqual(named.returncode, 0, named.stderr)
            self.assertEqual(extended.stdout, named.stdout)
            self.assertEqual(extended.stdout.splitlines()[3], "CHANGED=1")


if __name__ == "__main__":
    unittest.main()
