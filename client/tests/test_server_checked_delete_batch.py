"""Checked delete on the real server script: the whole request commits with ONE
journal transaction and ONE revision bump (it used to be one of each per file,
which on a small NAS meant a few files per second), while every file is still
moved under its own transaction record so a crash is recovered file by file."""
from __future__ import annotations

import hashlib
import os
import unittest
from pathlib import Path

from tests.support import ServerSandbox

RUN_TS = "2026-11-25--12-00-00-000001Z"


def _payload(items: list[tuple[str, str, int]]) -> bytes:
    fields = [b"CHECKED_DELETE_V1", RUN_TS.encode(), b"device-a", str(len(items)).encode()]
    for path, digest, mtime in items:
        fields += [os.fsencode(path), digest.encode(), str(mtime).encode()]
    return b"\0".join(fields) + b"\0"


def _statuses(raw: bytes) -> dict[str, str]:
    values = raw.split(b"\0")
    if values and values[-1] == b"":
        values.pop()
    assert values[0] == b"CHECKED_DELETE_V1", values
    count = int(values[1])
    return {os.fsdecode(values[2 + i * 2]): values[3 + i * 2].decode() for i in range(count)}


def _make(sandbox: ServerSandbox, name: str, body: bytes) -> tuple[str, str, int]:
    target = sandbox.share / name
    target.parent.mkdir(parents=True, exist_ok=True)
    target.write_bytes(body)
    return name, hashlib.sha256(body).hexdigest(), int(target.stat().st_mtime)


class CheckedDeleteBatchTests(unittest.TestCase):
    def test_many_files_one_journal_transaction_and_one_revision_bump(self) -> None:
        with ServerSandbox() as sandbox:
            sandbox.init_repository()
            self.assertEqual(sandbox.run("--manifest-export").returncode, 0)
            items = [_make(sandbox, f"dir/f{i}.txt", f"body {i}".encode()) for i in range(6)]
            stale = _make(sandbox, "stale.txt", b"original")
            (sandbox.share / "stale.txt").write_bytes(b"changed after the manifest")
            os_mtime = int((sandbox.share / "stale.txt").stat().st_mtime) + 5
            os.utime(sandbox.share / "stale.txt", (os_mtime, os_mtime))
            items.append(stale)
            items.append(("never-existed.txt", "0" * 64, 0))
            journal = sandbox.state / "transfer-journal.tsv"
            revision = sandbox.state / "manifest.revision"
            before = int(revision.read_text().strip() or 0)

            done = sandbox.run_with_lease("--checked-delete", input=_payload(items))
            self.assertEqual(done.returncode, 0, done.stderr.decode())
            result = _statuses(done.stdout)
            for i in range(6):
                self.assertEqual(result[f"dir/f{i}.txt"], "DELETED")
                self.assertFalse((sandbox.share / f"dir/f{i}.txt").exists())
                self.assertTrue((sandbox.share / ".sync-trash" / "dir" / f"f{i}.txt-{RUN_TS}").exists())
            self.assertEqual(result["stale.txt"], "STALE")
            self.assertTrue((sandbox.share / "stale.txt").exists())
            self.assertEqual(result["never-existed.txt"], "ABSENT")

            text = journal.read_text()
            self.assertEqual(text.count("BEGIN\t"), 1)
            self.assertEqual(text.count("COMMIT\t"), 1)
            self.assertEqual(text.count("DELETE\t"), 7)  # six moved + one already absent
            self.assertEqual(int(revision.read_text().strip()), before + 1)
            self.assertEqual(list((sandbox.state / "transactions").glob("*")), [])

    def test_journal_failure_puts_every_file_back(self) -> None:
        with ServerSandbox() as sandbox:
            sandbox.init_repository()
            self.assertEqual(sandbox.run("--manifest-export").returncode, 0)
            items = [_make(sandbox, f"keep{i}.txt", f"keep {i}".encode()) for i in range(3)]
            journal = sandbox.state / "transfer-journal.tsv"
            before = journal.read_text()

            failed = sandbox.run_with_lease(
                "--checked-delete", input=_payload(items),
                env={"NASBOX_TEST_FAILPOINT": "bump_revision_failure"},
            )
            self.assertEqual(failed.returncode, 0, failed.stderr.decode())
            self.assertEqual(set(_statuses(failed.stdout).values()), {"ERROR"})
            for i in range(3):
                self.assertEqual((sandbox.share / f"keep{i}.txt").read_bytes(), f"keep {i}".encode())
            self.assertEqual(journal.read_text(), before)
            self.assertEqual(list((sandbox.state / "transactions").glob("*")), [])

    def test_crash_after_moves_is_recovered_file_by_file(self) -> None:
        with ServerSandbox() as sandbox:
            sandbox.init_repository()
            self.assertEqual(sandbox.run("--manifest-export").returncode, 0)
            items = [_make(sandbox, f"crash{i}.txt", f"crash {i}".encode()) for i in range(3)]
            journal = sandbox.state / "transfer-journal.tsv"

            crashed = sandbox.run_with_lease(
                "--checked-delete", input=_payload(items),
                env={"NASBOX_TEST_FAILPOINT": "checked_delete_after_move"},
            )
            self.assertEqual(crashed.returncode, -9)
            self.assertNotIn("DELETE\tcrash0.txt", journal.read_text())

            recovered = sandbox.run_with_lease(
                "--checked-delete", input=_payload([]),
            )
            self.assertEqual(recovered.returncode, 0, recovered.stderr.decode())
            self.assertEqual(list((sandbox.state / "transactions").glob("*.txn")), [])
            text = journal.read_text()
            moved = [i for i in range(3) if not (sandbox.share / f"crash{i}.txt").exists()]
            for i in moved:
                self.assertEqual(text.count(f"DELETE\tcrash{i}.txt\t"), 1)


class EmptyDirectoryCleanupTests(unittest.TestCase):
    """A folder removed on a PC used to leave its empty shell on the NAS. Clients
    never create directories there: a directory only exists as the parent of a
    file, so once its last file is gone the folder is an artifact of the deletion.
    """

    def test_deleting_every_file_removes_the_empty_folders(self) -> None:
        with ServerSandbox() as sandbox:
            sandbox.init_repository()
            self.assertEqual(sandbox.run("--manifest-export").returncode, 0)
            items = [
                _make(sandbox, "docs/2026/a.txt", b"a"),
                _make(sandbox, "docs/2026/sub/b.txt", b"b"),
            ]
            _make(sandbox, "keep/altro.txt", b"keep")

            done = sandbox.run_with_lease("--checked-delete", input=_payload(items))
            self.assertEqual(done.returncode, 0, done.stderr.decode())
            self.assertEqual(set(_statuses(done.stdout).values()), {"DELETED"})
            self.assertFalse((sandbox.share / "docs").exists())
            # What belongs to someone else is untouched, and the versions stay.
            self.assertTrue((sandbox.share / "keep" / "altro.txt").exists())
            self.assertTrue((sandbox.share / ".sync-trash" / "docs" / "2026" / f"a.txt-{RUN_TS}").exists())
            self.assertTrue((sandbox.share / ".sync-trash" / "docs" / "2026" / "sub" / f"b.txt-{RUN_TS}").exists())

    def test_a_folder_with_remaining_files_stays(self) -> None:
        with ServerSandbox() as sandbox:
            sandbox.init_repository()
            self.assertEqual(sandbox.run("--manifest-export").returncode, 0)
            gone = _make(sandbox, "misto/uno.txt", b"uno")
            _make(sandbox, "misto/due.txt", b"due")

            done = sandbox.run_with_lease("--checked-delete", input=_payload([gone]))
            self.assertEqual(done.returncode, 0, done.stderr.decode())
            self.assertTrue((sandbox.share / "misto" / "due.txt").exists())
            self.assertFalse((sandbox.share / "misto" / "uno.txt").exists())

    def test_a_file_at_the_share_root_does_not_touch_it(self) -> None:
        with ServerSandbox() as sandbox:
            sandbox.init_repository()
            self.assertEqual(sandbox.run("--manifest-export").returncode, 0)
            item = _make(sandbox, "radice.txt", b"radice")

            done = sandbox.run_with_lease("--checked-delete", input=_payload([item]))
            self.assertEqual(done.returncode, 0, done.stderr.decode())
            self.assertEqual(_statuses(done.stdout)["radice.txt"], "DELETED")
            self.assertTrue(sandbox.share.is_dir())
            self.assertTrue((sandbox.share / ".sync-trash").is_dir())

    def test_an_already_absent_file_also_clears_its_empty_folder(self) -> None:
        with ServerSandbox() as sandbox:
            sandbox.init_repository()
            self.assertEqual(sandbox.run("--manifest-export").returncode, 0)
            item = _make(sandbox, "resti/interno/a.txt", b"a")
            (sandbox.share / "resti" / "interno" / "a.txt").unlink()

            done = sandbox.run_with_lease("--checked-delete", input=_payload([item]))
            self.assertEqual(done.returncode, 0, done.stderr.decode())
            self.assertEqual(_statuses(done.stdout)["resti/interno/a.txt"], "ABSENT")
            self.assertFalse((sandbox.share / "resti").exists())


if __name__ == "__main__":
    unittest.main()
