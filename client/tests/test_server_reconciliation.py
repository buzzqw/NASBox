from __future__ import annotations

import hashlib
import os
import subprocess
import tempfile
import unittest
from pathlib import Path


class ServerReconciliationTests(unittest.TestCase):
    def setUp(self) -> None:
        self.temporary = tempfile.TemporaryDirectory()
        folder = Path(self.temporary.name)
        self.root = folder / "share"
        self.root.mkdir()
        self.config = folder / "server.conf"
        self.config.write_text(f"SHARE_ROOT={self.root}\nRETENTION_DAYS=30\n", encoding="utf-8")
        self.script = Path(__file__).resolve().parents[2] / "server" / "sync-daemon-server.sh"
        subprocess.run(
            [str(self.script), "-c", str(self.config), "--init-repository"],
            capture_output=True, check=True, timeout=20,
        )

    def tearDown(self) -> None:
        self.temporary.cleanup()

    def run_binary(self, action: str, fields: list[bytes]) -> list[bytes]:
        output = subprocess.run(
            [str(self.script), "-c", str(self.config), action],
            input=b"\0".join(fields) + b"\0", capture_output=True, check=True, timeout=20,
        ).stdout
        values = output.split(b"\0")
        return values[:-1] if values and values[-1] == b"" else values

    def run_binary_allow_failure(self, action: str, fields: list[bytes]) -> list[bytes]:
        """Like run_binary, but for calls expected to exit non-zero (browse-delete/
        browse-rename still print an ERROR\0detail response on validation failures,
        rather than failing silently -- this reads that response instead of raising)."""
        output = subprocess.run(
            [str(self.script), "-c", str(self.config), action],
            input=b"\0".join(fields) + b"\0", capture_output=True, timeout=20,
        ).stdout
        values = output.split(b"\0")
        return values[:-1] if values and values[-1] == b"" else values

    def test_checked_delete_rejects_stale_baseline_then_deletes_exact_version(self) -> None:
        target = self.root / "report.txt"
        target.write_bytes(b"new data")
        os.utime(target, (20, 20))
        digest = hashlib.sha256(b"new data").hexdigest().encode()
        run_ts = b"2026-08-07--12-00-00-000001Z"

        stale = self.run_binary("--checked-delete", [
            b"CHECKED_DELETE_V1", run_ts, b"device01", b"1", b"report.txt", b"a" * 64, b"10",
        ])
        self.assertEqual(stale[-1], b"STALE")
        self.assertTrue(target.exists())

        deleted = self.run_binary("--checked-delete", [
            b"CHECKED_DELETE_V1", run_ts, b"device01", b"1", b"report.txt", digest, b"20",
        ])
        self.assertEqual(deleted[-1], b"DELETED")
        self.assertFalse(target.exists())
        self.assertTrue((self.root / ".sync-trash" / f"report.txt-{run_ts.decode()}").is_file())
        tombstone = self.run_binary("--file-states", [
            b"FILE_STATES_V1", b"1", b"report.txt",
        ])
        self.assertEqual(tombstone[3], b"TOMBSTONE")

    def test_v2_journal_rejects_a_different_repository(self) -> None:
        payload = b"\0".join((
            b"JOURNAL_V2", b"wrong-repository", b"tx-test", b"device01", b"1", b"0",
        )) + b"\0"
        result = subprocess.run(
            [str(self.script), "-c", str(self.config), "--journal-append"],
            input=payload, capture_output=True, timeout=20,
        )
        self.assertNotEqual(result.returncode, 0)
        self.assertIn(b"repository non corrispondente", result.stderr)

    def test_file_states_reports_live_file_and_retained_tombstone(self) -> None:
        target = self.root / "folder" / "report.txt"
        target.parent.mkdir()
        target.write_bytes(b"version")
        os.utime(target, (30, 30))
        digest = hashlib.sha256(b"version").hexdigest().encode()

        states = self.run_binary("--file-states", [
            b"FILE_STATES_V1", b"1", b"folder/report.txt",
        ])
        self.assertEqual(states[2:5], [b"folder/report.txt", b"FILE", digest])
        self.assertEqual(states[6], b"30000000000")

        target.unlink()
        journal = b"\0".join((
            b"JOURNAL_V1", b"tx-delete", b"device01", b"1", b"1",
            b"DELETE", b"folder/report.txt", b"", b"0", b"0", b"",
        )) + b"\0"
        subprocess.run(
            [str(self.script), "-c", str(self.config), "--journal-append"],
            input=journal, capture_output=True, check=True, timeout=20,
        )
        tombstone = self.run_binary("--file-states", [
            b"FILE_STATES_V1", b"1", b"folder/report.txt",
        ])
        self.assertEqual(tombstone[2], b"folder/report.txt")
        self.assertEqual(tombstone[3], b"TOMBSTONE")
        self.assertGreater(int(tombstone[6]), 0)

        manifest = subprocess.run(
            [str(self.script), "-c", str(self.config), "--manifest-export"],
            capture_output=True, text=True, check=True, timeout=20,
        ).stdout
        row = next(line for line in manifest.splitlines() if line.startswith("folder/report.txt\t"))
        self.assertEqual(row.split("\t")[1], "")

    def test_file_states_protocol_preserves_newline_in_path(self) -> None:
        relative = "folder/line\nname.txt"
        target = self.root / relative
        target.parent.mkdir()
        target.write_bytes(b"newline")

        states = self.run_binary("--file-states", [
            b"FILE_STATES_V1", b"1", os.fsencode(relative),
        ])

        self.assertEqual(os.fsdecode(states[2]), relative)
        self.assertEqual(states[3], b"FILE")

    # --- browse-list / browse-delete / browse-rename ("Sfoglia NAS" tab) ---

    def run_arg(self, action: str, extra_args: list[str]) -> subprocess.CompletedProcess:
        return subprocess.run(
            [str(self.script), "-c", str(self.config), action, *extra_args],
            capture_output=True, timeout=20,
        )

    def test_browse_list_reports_files_and_dirs_and_excludes_internal_dirs(self) -> None:
        (self.root / "top.txt").write_bytes(b"hello")
        (self.root / "sub").mkdir()
        (self.root / "sub" / "inner.txt").write_bytes(b"world")

        result = self.run_arg("--browse-list", [""])
        self.assertEqual(result.returncode, 0)
        values = result.stdout.split(b"\0")
        values = values[:-1] if values and values[-1] == b"" else values
        self.assertEqual(values[0], b"BROWSE_LIST_V1")
        count = int(values[1])
        names = {values[2 + i * 4] for i in range(count)}
        self.assertIn(b"top.txt", names)
        self.assertIn(b"sub", names)
        self.assertNotIn(b".sync-trash", names)
        self.assertNotIn(b".nasbox-root", names)

    def test_browse_list_rejects_path_traversal(self) -> None:
        result = self.run_arg("--browse-list", ["../../etc"])
        self.assertNotEqual(result.returncode, 0)

    def test_browse_delete_file_moves_to_trash_and_journals_a_delete(self) -> None:
        target = self.root / "doomed.txt"
        target.write_bytes(b"bye")
        run_ts = b"2026-08-08--06-00-00-000001Z"

        result = self.run_binary("--browse-delete", [
            b"BROWSE_DELETE_V1", run_ts, b"device01", b"doomed.txt",
        ])
        self.assertEqual(result, [b"BROWSE_DELETE_V1", b"OK"])
        self.assertFalse(target.exists())
        self.assertTrue((self.root / ".sync-trash" / f"doomed.txt-{run_ts.decode()}").is_file())

        tombstone = self.run_binary("--file-states", [b"FILE_STATES_V1", b"1", b"doomed.txt"])
        self.assertEqual(tombstone[3], b"TOMBSTONE")

    def test_browse_delete_directory_recurses_and_preserves_structure_in_trash(self) -> None:
        (self.root / "folder").mkdir()
        (self.root / "folder" / "a.txt").write_bytes(b"a")
        (self.root / "folder" / "sub").mkdir()
        (self.root / "folder" / "sub" / "b.txt").write_bytes(b"b")
        run_ts = b"2026-08-08--06-01-00-000001Z"

        result = self.run_binary("--browse-delete", [
            b"BROWSE_DELETE_V1", run_ts, b"device01", b"folder",
        ])
        self.assertEqual(result, [b"BROWSE_DELETE_V1", b"OK"])
        self.assertFalse((self.root / "folder").exists())
        trashed = self.root / ".sync-trash" / f"folder-{run_ts.decode()}"
        self.assertTrue((trashed / "a.txt").is_file())
        self.assertTrue((trashed / "sub" / "b.txt").is_file())

        for relative in (b"folder/a.txt", b"folder/sub/b.txt"):
            tombstone = self.run_binary("--file-states", [b"FILE_STATES_V1", b"1", relative])
            self.assertEqual(tombstone[3], b"TOMBSTONE")

    def test_browse_delete_rejects_path_traversal(self) -> None:
        result = self.run_binary_allow_failure("--browse-delete", [
            b"BROWSE_DELETE_V1", b"2026-08-08--06-02-00-000001Z", b"device01", b"../outside.txt",
        ])
        self.assertEqual(result[0], b"BROWSE_DELETE_V1")
        self.assertEqual(result[1], b"ERROR")

    def test_browse_rename_moves_file_and_rejects_existing_destination(self) -> None:
        (self.root / "old.txt").write_bytes(b"data")
        run_ts = b"2026-08-08--06-03-00-000001Z"

        result = self.run_binary("--browse-rename", [
            b"BROWSE_RENAME_V1", run_ts, b"device01", b"old.txt", b"new.txt",
        ])
        self.assertEqual(result, [b"BROWSE_RENAME_V1", b"OK"])
        self.assertFalse((self.root / "old.txt").exists())
        self.assertTrue((self.root / "new.txt").is_file())

        (self.root / "old.txt").write_bytes(b"data2")
        collision = self.run_binary_allow_failure("--browse-rename", [
            b"BROWSE_RENAME_V1", run_ts, b"device01", b"old.txt", b"new.txt",
        ])
        self.assertEqual(collision[0], b"BROWSE_RENAME_V1")
        self.assertEqual(collision[1], b"ERROR")
        self.assertTrue((self.root / "old.txt").is_file())  # untouched by the rejected rename


if __name__ == "__main__":
    unittest.main()
