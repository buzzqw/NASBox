from __future__ import annotations

import hashlib
import os
import unittest
from unittest.mock import patch

from sync_client import rsync_ops
from sync_client.config import Config
from sync_client.reconcile import RemoteKind
from sync_client.sync_state import Fingerprint

from tests.support import ClientEnvironment, ServerSandbox


class ProtocolParsingTests(unittest.TestCase):
    def test_itemize_parser_tracks_files_deletes_and_rsync_octal_names(self) -> None:
        self.assertEqual(
            rsync_ops._parse_itemize_line(">f+++++++++|12|folder/file.txt", "upload"),
            rsync_ops.TransferItem("upload", "folder/file.txt", 12),
        )
        self.assertEqual(
            rsync_ops._parse_itemize_line("*deleting|0|old\\#011name", "download"),
            rsync_ops.TransferItem("delete_local", "old\tname", 0),
        )
        self.assertIsNone(rsync_ops._parse_itemize_line(".d..t......|0|folder", "upload"))
        self.assertIsNone(rsync_ops._parse_itemize_line("not-an-item", "upload"))

    def test_progress_parser_accepts_binary_units_and_rejects_locale_format(self) -> None:
        self.assertEqual(
            rsync_ops._parse_progress_line("  2048  50%  2.00MB/s    (xfr#1, to-chk=0/1)"),
            (50, 2 * 1024 * 1024),
        )
        self.assertIsNone(rsync_ops._parse_progress_line("2.048 50% 2,00MB/s"))

    def test_journal_payload_is_nul_delimited_and_binary_safe(self) -> None:
        with ClientEnvironment():
            cfg = Config()
            cfg.set("remote_server_script", "/tmp/server.sh", persist=False)
            cfg.set("repository_id", "repository-123", persist=False)
            item = rsync_ops.TransferItem("upload", "folder/100%\tline\nname.txt", 3)
            fingerprint = Fingerprint("a" * 64, 3, 123456789)
            with patch.object(rsync_ops, "new_run_ts", return_value="2026-08-30--12-00-00-000001Z"), patch.object(
                rsync_ops.time, "time", return_value=1700000000
            ):
                payload, error = rsync_ops.build_remote_journal_payload(
                    cfg, "device-a", [item], {item.path: fingerprint},
                )
        self.assertEqual(error, "")
        self.assertIsNotNone(payload)
        assert payload is not None
        self.assertEqual(
            payload.split(b"\0"),
            [
                b"JOURNAL_V2", b"repository-123", b"2026-08-30--12-00-00-000001Z",
                b"device-a", b"1700000000",
                b"1", b"PUT", os.fsencode(item.path), b"a" * 64, b"3", b"123456789", b"",
            ],
        )

    def test_journal_payload_rejects_path_traversal(self) -> None:
        with ClientEnvironment():
            cfg = Config()
            cfg.set("remote_server_script", "/tmp/server.sh", persist=False)
            cfg.set("repository_id", "repository-123", persist=False)
            payload, error = rsync_ops.build_remote_journal_payload(
                cfg, "device-a", [rsync_ops.TransferItem("upload", "../escape")], {},
            )
        self.assertIsNone(payload)
        self.assertIn("percorso non valido", error)

    def test_manifest_snapshot_parses_files_tombstones_and_reuses_revision(self) -> None:
        with ClientEnvironment():
            cfg = Config()
            cfg.set("remote_server_script", "/tmp/server.sh", persist=False)
            digest = "b" * 64
            manifest = (
                b"NASBOX_MANIFEST_V1\n"
                b"folder%09name%0A.txt\t" + digest.encode() + b"\t5\t123000000000\tdevice-a\t1700000000\n"
                b"deleted.txt\t\t0\t0\tdevice-a\t1700000001\n"
            )
            with patch.object(
                rsync_ops, "run_remote_script", return_value=(True, "MANIFEST_REVISION=7\n", "")
            ) as status_call, patch.object(
                rsync_ops, "run_remote_script_bytes", return_value=(True, manifest, "")
            ) as export_call:
                snapshot = rsync_ops.remote_manifest_snapshot(cfg, rsync_ops.NasConnection("nas"), 6)
                unchanged = rsync_ops.remote_manifest_snapshot(cfg, rsync_ops.NasConnection("nas"), 7)
        self.assertIsNotNone(snapshot)
        assert snapshot is not None
        revision, entries = snapshot
        self.assertEqual(revision, 7)
        self.assertEqual(entries["folder\tname\n.txt"].kind, RemoteKind.FILE)
        self.assertEqual(entries["folder\tname\n.txt"].digest, digest)
        self.assertEqual(entries["deleted.txt"].kind, RemoteKind.TOMBSTONE)
        self.assertEqual(unchanged, (7, None))
        self.assertEqual(status_call.call_count, 2)
        export_call.assert_called_once()

    def test_checked_delete_response_parser_rejects_incomplete_output(self) -> None:
        with ClientEnvironment():
            cfg = Config()
            cfg.set("remote_server_script", "/tmp/server.sh", persist=False)
            requests = [("file.txt", "a" * 64, 10)]
            with patch.object(
                rsync_ops, "run_remote_script_input_bytes",
                return_value=(True, b"CHECKED_DELETE_V1\0" b"1\0", ""),
            ):
                result = rsync_ops.checked_delete_remote(
                    cfg, rsync_ops.NasConnection("nas"), requests,
                    "2026-08-30--12-00-00-000001Z", "device-a",
                )
        self.assertFalse(result.ok)
        self.assertIn("incompleta", result.raw_error)


class ServerProtocolInvariantTests(unittest.TestCase):
    def test_mutating_commands_require_the_matching_global_lease(self) -> None:
        with ServerSandbox() as sandbox:
            sandbox.init_repository()
            target = sandbox.share / "protected.txt"
            target.write_bytes(b"protected")
            digest = hashlib.sha256(target.read_bytes()).hexdigest()
            payload = b"\0".join(
                [
                    b"CHECKED_DELETE_V1", b"2026-08-30--12-00-00-000001Z", b"device-a", b"1",
                    b"protected.txt", digest.encode(), str(int(target.stat().st_mtime)).encode(),
                ]
            ) + b"\0"
            unlocked = sandbox.run("--checked-delete", input=payload)
            wrong_owner = sandbox.run_with_lease("--checked-delete", device="device-b", input=payload)
            target_preserved = target.exists()
        self.assertEqual(unlocked.returncode, 75)
        self.assertEqual(wrong_owner.returncode, 75)
        self.assertTrue(target_preserved)

    def test_journal_and_manifest_round_trip_preserves_control_characters(self) -> None:
        with ServerSandbox() as sandbox:
            repository_id = sandbox.init_repository()
            path = "folder/percent%\tline\nname.txt"
            digest = hashlib.sha256(b"payload").hexdigest()
            payload = b"\0".join(
                [
                    b"JOURNAL_V2", repository_id.encode(), b"tx-control", b"device-a", b"1700000000", b"1",
                    b"PUT", os.fsencode(path), digest.encode(), b"7", b"1700000000000000000",
                ]
            ) + b"\0"
            appended = sandbox.run_with_lease("--journal-append", input=payload)
            exported = sandbox.run("--manifest-export")
            looked_up = sandbox.run("--manifest-get", path)
        self.assertEqual(appended.returncode, 0, appended.stderr.decode())
        self.assertEqual(exported.returncode, 0, exported.stderr.decode())
        encoded = b"folder/percent%25%09line%0Aname.txt"
        self.assertIn(encoded + b"\t" + digest.encode(), exported.stdout)
        self.assertIn(b"MANIFEST_HIT\t" + encoded, looked_up.stdout)

    def test_checked_delete_records_tombstone_only_after_move(self) -> None:
        with ServerSandbox() as sandbox:
            repository_id = sandbox.init_repository()
            target = sandbox.share / "delete-me.txt"
            target.write_bytes(b"remove me")
            digest = hashlib.sha256(target.read_bytes()).hexdigest()
            mtime = int(target.stat().st_mtime)
            payload = b"\0".join(
                [
                    b"CHECKED_DELETE_V1", b"2026-08-30--12-00-00-000001Z", b"device-a", b"1",
                    b"delete-me.txt", digest.encode(), str(mtime).encode(),
                ]
            ) + b"\0"
            result = sandbox.run_with_lease("--checked-delete", input=payload)
            journal = (sandbox.state / "transfer-journal.tsv").read_text()
            manifest = sandbox.run("--manifest-export")
            self.assertEqual(result.returncode, 0, result.stderr.decode())
            self.assertIn(b"delete-me.txt\0DELETED\0", result.stdout)
            self.assertFalse(target.exists())
            self.assertTrue(list((sandbox.share / ".sync-trash").rglob("delete-me.txt-*")))
            self.assertIn(
                "DELETE\tdelete-me.txt\t", journal,
                result.stdout.decode(errors="replace") + result.stderr.decode(errors="replace"),
            )
            self.assertIn(b"delete-me.txt\t\t0\t0\t", manifest.stdout)

    def test_invalid_journal_does_not_append_a_partial_transaction(self) -> None:
        with ServerSandbox() as sandbox:
            sandbox.init_repository()
            before = (sandbox.state / "transfer-journal.tsv").read_bytes()
            invalid = b"\0".join(
                [b"JOURNAL_V2", b"wrong-repository", b"tx", b"device-a", b"1", b"0"]
            ) + b"\0"
            result = sandbox.run("--journal-append", input=invalid)
            after = (sandbox.state / "transfer-journal.tsv").read_bytes()
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(after, before)

    def test_repeating_a_committed_journal_transaction_is_idempotent(self) -> None:
        with ServerSandbox() as sandbox:
            repository_id = sandbox.init_repository()
            digest = hashlib.sha256(b"payload").hexdigest()
            payload = b"\0".join([
                b"JOURNAL_V2", repository_id.encode(), b"same-tx", b"device-a", b"1700000000", b"1",
                b"PUT", b"same.txt", digest.encode(), b"7", b"1700000000000000000",
            ]) + b"\0"
            first = sandbox.run_with_lease("--journal-append", input=payload)
            second = sandbox.run_with_lease("--journal-append", input=payload)
            journal = (sandbox.state / "transfer-journal.tsv").read_text()
            revision = (sandbox.state / "manifest.revision").read_text().strip()
        self.assertEqual(first.returncode, 0, first.stderr.decode())
        self.assertEqual(second.returncode, 0, second.stderr.decode())
        self.assertEqual(journal.count("COMMIT\tsame-tx"), 1)
        self.assertEqual(revision, "1")


if __name__ == "__main__":
    unittest.main()
