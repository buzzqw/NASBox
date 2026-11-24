"""Server-side P0 hardening contracts: transactional restore, journal/revision
ordering and valid JSON events for paths with control characters.

These tests run the real ``sync-daemon-server.sh`` through ``ServerSandbox`` so
that markers, journal, revision and filesystem moves are exercised together.
"""
from __future__ import annotations

import hashlib
import json
import os
import time
import unittest
from pathlib import Path

from tests.support import ServerSandbox


DIGEST = hashlib.sha256(b"payload").hexdigest()
RUN_TS = "2026-11-24--12-00-00-000001Z"


def _journal_put_payload(repository_id: str, txid: str = "bump-fail") -> bytes:
    return b"\0".join([
        b"JOURNAL_V2", repository_id.encode("ascii"), txid.encode("ascii"),
        b"device-a", b"1700000000", b"1", b"PUT", b"bump.txt",
        DIGEST.encode("ascii"), b"7", b"1700000000000000000",
    ]) + b"\0"


def _restore_payload(path: str, version: str = "latest") -> bytes:
    return b"\0".join([
        b"RESTORE_BATCH_V1", b"device-a", b"1", os.fsencode(path), version.encode("ascii"),
    ]) + b"\0"


def _empty_checked_delete_payload() -> bytes:
    return b"\0".join([
        b"CHECKED_DELETE_V1", RUN_TS.encode("ascii"), b"device-a", b"0",
    ]) + b"\0"


def _restore_status(raw: bytes) -> dict[str, str]:
    values = raw.split(b"\0")
    if values and values[-1] == b"":
        values.pop()
    assert values[0] == b"RESTORE_BATCH_V1", values
    count = int(values[1])
    return {
        os.fsdecode(values[2 + index * 2]): values[3 + index * 2].decode("ascii", "replace")
        for index in range(count)
    }


def _make_trash_version(sandbox: ServerSandbox, relative: str, content: bytes) -> Path:
    trash_dir = sandbox.share / ".sync-trash"
    parent = str(Path(relative).parent)
    if parent != ".":
        trash_dir = trash_dir / parent
    trash_dir.mkdir(parents=True, exist_ok=True)
    version = trash_dir / f"{Path(relative).name}-{RUN_TS}"
    version.write_bytes(content)
    return version


class ServerHardeningTests(unittest.TestCase):
    def test_bump_failure_after_journal_append_rolls_back_both_files(self) -> None:
        with ServerSandbox() as sandbox:
            repository_id = sandbox.init_repository()
            # Create the empty journal/revision pair before taking the snapshot.
            self.assertEqual(sandbox.run("--manifest-export").returncode, 0)
            journal = sandbox.state / "transfer-journal.tsv"
            revision = sandbox.state / "manifest.revision"
            before = journal.read_bytes()

            failed = sandbox.run_with_lease(
                "--journal-append", input=_journal_put_payload(repository_id),
                env={"NASBOX_TEST_FAILPOINT": "bump_revision_failure"},
            )
            self.assertNotEqual(failed.returncode, 0, failed.stderr.decode())
            self.assertEqual(journal.read_bytes(), before)
            self.assertEqual(revision.read_text().strip(), "0")
            self.assertEqual(list(sandbox.state.glob("*.rollback.*")), [])

            retry = sandbox.run_with_lease(
                "--journal-append", input=_journal_put_payload(repository_id),
            )
            self.assertEqual(retry.returncode, 0, retry.stderr.decode())
            self.assertEqual(revision.read_text().strip(), "1")
            self.assertIn(b"COMMIT\tbump-fail", journal.read_bytes())
            manifest = sandbox.run("--manifest-export")
            self.assertIn(b"bump.txt\t" + DIGEST.encode(), manifest.stdout)

    def test_restore_twice_is_idempotent(self) -> None:
        with ServerSandbox() as sandbox:
            sandbox.init_repository()
            version = _make_trash_version(sandbox, "restore.txt", b"restored bytes")
            journal = sandbox.state / "transfer-journal.tsv"
            revision = sandbox.state / "manifest.revision"
            payload = _restore_payload("restore.txt")

            first = sandbox.run_with_lease("--restore-batch", input=payload)
            self.assertEqual(first.returncode, 0, first.stderr.decode())
            self.assertEqual(_restore_status(first.stdout), {"restore.txt": "RESTORED"})
            self.assertEqual((sandbox.share / "restore.txt").read_bytes(), b"restored bytes")
            self.assertFalse(version.exists())
            self.assertEqual(journal.read_text().count("PUT\trestore.txt\t"), 1)
            self.assertEqual(revision.read_text().strip(), "1")

            second = sandbox.run_with_lease("--restore-batch", input=payload)
            self.assertEqual(second.returncode, 0, second.stderr.decode())
            self.assertEqual(_restore_status(second.stdout), {"restore.txt": "RESTORED"})
            self.assertEqual((sandbox.share / "restore.txt").read_bytes(), b"restored bytes")
            self.assertEqual(journal.read_text().count("PUT\trestore.txt\t"), 1)
            self.assertEqual(revision.read_text().strip(), "1")
            self.assertEqual(list((sandbox.state / "transactions").glob("*.txn")), [])
            self.assertEqual(list((sandbox.state / "transactions").glob("*.paths")), [])

    def test_restore_crash_after_move_recovers_journal_without_a_second_put(self) -> None:
        with ServerSandbox() as sandbox:
            sandbox.init_repository()
            version = _make_trash_version(sandbox, "recover.txt", b"recovered bytes")
            journal = sandbox.state / "transfer-journal.tsv"
            payload = _restore_payload("recover.txt")

            crashed = sandbox.run_with_lease(
                "--restore-batch", input=payload,
                env={"NASBOX_TEST_FAILPOINT": "restore_after_move"},
            )
            self.assertEqual(crashed.returncode, -9)
            self.assertEqual((sandbox.share / "recover.txt").read_bytes(), b"recovered bytes")
            self.assertFalse(version.exists())
            self.assertNotIn("PUT\trecover.txt\t", journal.read_text())
            self.assertEqual(len(list((sandbox.state / "transactions").glob("*.txn"))), 1)

            repeated = sandbox.run_with_lease("--restore-batch", input=payload)
            self.assertEqual(repeated.returncode, 0, repeated.stderr.decode())
            self.assertEqual(_restore_status(repeated.stdout), {"recover.txt": "RESTORED"})
            self.assertEqual(journal.read_text().count("PUT\trecover.txt\t"), 1)
            self.assertEqual(list((sandbox.state / "transactions").glob("*.txn")), [])
            self.assertEqual(list((sandbox.state / "transactions").glob("*.paths")), [])

    def test_restore_crash_after_marker_discards_unstarted_transaction(self) -> None:
        with ServerSandbox() as sandbox:
            sandbox.init_repository()
            version = _make_trash_version(sandbox, "unstarted.txt", b"still in trash")
            journal = sandbox.state / "transfer-journal.tsv"
            payload = _restore_payload("unstarted.txt")

            crashed = sandbox.run_with_lease(
                "--restore-batch", input=payload,
                env={"NASBOX_TEST_FAILPOINT": "restore_after_marker"},
            )
            self.assertEqual(crashed.returncode, -9)
            self.assertFalse((sandbox.share / "unstarted.txt").exists())
            self.assertTrue(version.exists())
            self.assertEqual(len(list((sandbox.state / "transactions").glob("*.txn"))), 1)

            recovered = sandbox.run_with_lease(
                "--checked-delete", input=_empty_checked_delete_payload(),
            )
            self.assertEqual(recovered.returncode, 0, recovered.stderr.decode())
            self.assertTrue(version.exists())
            self.assertEqual(list((sandbox.state / "transactions").glob("*.txn")), [])
            self.assertEqual(list((sandbox.state / "transactions").glob("*.paths")), [])
            self.assertNotIn("PUT\tunstarted.txt\t", journal.read_text())

    def test_restore_crash_after_journal_cleans_marker_without_duplicate(self) -> None:
        with ServerSandbox() as sandbox:
            sandbox.init_repository()
            _make_trash_version(sandbox, "committed.txt", b"committed bytes")
            journal = sandbox.state / "transfer-journal.tsv"

            crashed = sandbox.run_with_lease(
                "--restore-batch", input=_restore_payload("committed.txt"),
                env={"NASBOX_TEST_FAILPOINT": "restore_after_journal"},
            )
            self.assertEqual(crashed.returncode, -9)
            self.assertEqual((sandbox.share / "committed.txt").read_bytes(), b"committed bytes")
            self.assertEqual(journal.read_text().count("PUT\tcommitted.txt\t"), 1)
            self.assertEqual(len(list((sandbox.state / "transactions").glob("*.txn"))), 1)

            recovered = sandbox.run_with_lease(
                "--checked-delete", input=_empty_checked_delete_payload(),
            )
            self.assertEqual(recovered.returncode, 0, recovered.stderr.decode())
            self.assertEqual(list((sandbox.state / "transactions").glob("*.txn")), [])
            self.assertEqual(list((sandbox.state / "transactions").glob("*.paths")), [])
            self.assertEqual(journal.read_text().count("PUT\tcommitted.txt\t"), 1)

    def test_restore_bump_failure_restores_the_trash_version(self) -> None:
        with ServerSandbox() as sandbox:
            sandbox.init_repository()
            version = _make_trash_version(sandbox, "rollback.txt", b"rollback bytes")
            journal = sandbox.state / "transfer-journal.tsv"
            revision = sandbox.state / "manifest.revision"

            failed = sandbox.run_with_lease(
                "--restore-batch", input=_restore_payload("rollback.txt"),
                env={"NASBOX_TEST_FAILPOINT": "bump_revision_failure"},
            )
            self.assertEqual(failed.returncode, 0, failed.stderr.decode())
            self.assertEqual(_restore_status(failed.stdout), {"rollback.txt": "ERROR"})
            self.assertFalse((sandbox.share / "rollback.txt").exists())
            self.assertTrue(version.exists())
            self.assertNotIn("PUT\trollback.txt\t", journal.read_text())
            self.assertEqual(revision.read_text().strip(), "0")

            recovered = sandbox.run_with_lease(
                "--checked-delete", input=_empty_checked_delete_payload(),
            )
            self.assertEqual(recovered.returncode, 0, recovered.stderr.decode())
            self.assertTrue(version.exists())
            self.assertEqual(list((sandbox.state / "transactions").glob("*.txn")), [])
            self.assertNotIn("PUT\trollback.txt\t", journal.read_text())

    def test_existing_target_with_expected_bytes_is_a_successful_restore(self) -> None:
        with ServerSandbox() as sandbox:
            sandbox.init_repository()
            version = _make_trash_version(sandbox, "already.txt", b"same bytes")
            target = sandbox.share / "already.txt"
            target.write_bytes(b"same bytes")
            stamp = time.time() - 60
            os.utime(target, (stamp, stamp))
            os.utime(version, (stamp, stamp))
            journal = sandbox.state / "transfer-journal.tsv"

            result = sandbox.run_with_lease("--restore-batch", input=_restore_payload("already.txt"))
            self.assertEqual(result.returncode, 0, result.stderr.decode())
            self.assertEqual(_restore_status(result.stdout), {"already.txt": "RESTORED"})
            self.assertEqual(target.read_bytes(), b"same bytes")
            self.assertTrue(version.exists())
            self.assertEqual(journal.read_text().count("PUT\talready.txt\t"), 1)

    def test_log_event_writes_valid_json_for_paths_with_control_characters(self) -> None:
        rel = "odd\nline\t\x01\x7f\"back\\slash.txt"
        with ServerSandbox(RETENTION_DAYS="1") as sandbox:
            sandbox.init_repository()
            version = sandbox.share / ".sync-trash" / rel
            version.parent.mkdir(parents=True, exist_ok=True)
            version.write_bytes(b"old version")
            old = time.time() - 90 * 86400
            os.utime(version, (old, old))

            result = sandbox.run("--run-once")
            self.assertEqual(result.returncode, 0, result.stderr.decode())
            self.assertFalse(version.exists())

            lines = (sandbox.state / "events.jsonl").read_text(encoding="utf-8").splitlines()
            records = [json.loads(line) for line in lines]
            pruned = [record for record in records if record["action"] == "PRUNE_REMOTE_TRASH"]
            self.assertEqual(len(pruned), 1)
            self.assertEqual(pruned[0]["path"], rel)


if __name__ == "__main__":
    unittest.main()
