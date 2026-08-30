from __future__ import annotations

import hashlib
import os
import unittest
from unittest.mock import patch

from sync_client import rsync_ops
from sync_client.config import Config
from sync_client.reconcile import RemoteKind
from sync_client.sync_state import CausalVersion, Fingerprint

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

    def test_manifest_snapshot_accepts_optional_causal_field(self) -> None:
        with ClientEnvironment():
            cfg = Config()
            cfg.set("remote_server_script", "/tmp/server.sh", persist=False)
            digest = "c" * 64
            manifest = (
                b"NASBOX_MANIFEST_V1\n"
                b"causal.txt\t" + digest.encode() + b"\t5\t123000000000\tdevice-a\t1700000000\tdevice-a:2\n"
            )
            with patch.object(
                rsync_ops, "run_remote_script", return_value=(True, "MANIFEST_REVISION=8\n", "")
            ), patch.object(rsync_ops, "run_remote_script_bytes", return_value=(True, manifest, "")):
                snapshot = rsync_ops.remote_manifest_snapshot(cfg, rsync_ops.NasConnection("nas"), 7)
        assert snapshot is not None
        self.assertEqual(snapshot[1]["causal.txt"].causal, CausalVersion((("device-a", 2),)))

    def test_causal_journal_payload_is_opt_in(self) -> None:
        with ClientEnvironment():
            cfg = Config()
            cfg.set("remote_server_script", "/tmp/server.sh", persist=False)
            cfg.set("repository_id", "repository-123", persist=False)
            cfg.set("remote_causal_versions_available", True, persist=False)
            item = rsync_ops.TransferItem("upload", "causal.txt")
            payload, error = rsync_ops.build_remote_journal_payload(
                cfg, "device-a", [item],
                {item.path: Fingerprint("a" * 64, 3, 123)},
                {item.path: CausalVersion((("device-a", 3),))},
            )
        self.assertEqual(error, "")
        assert payload is not None
        self.assertEqual(payload.split(b"\0")[0], b"JOURNAL_V3")
        self.assertEqual(payload.split(b"\0")[-2], b"device-a:3")

    def test_file_states_parser_accepts_causal_v2_and_legacy_fallback(self) -> None:
        with ClientEnvironment():
            cfg = Config()
            cfg.set("remote_server_script", "/tmp/server.sh", persist=False)
            digest = "d" * 64
            v2 = b"FILE_STATES_V2\0" b"1\0file.txt\0FILE\0" + digest.encode() + b"\0" b"3\0" b"10\0device-a:5\0"
            with patch.object(rsync_ops, "run_remote_script_input_bytes", return_value=(True, v2, "")):
                states = rsync_ops.remote_file_states(
                    cfg, rsync_ops.NasConnection("nas"), {"file.txt"}, compact=False,
                )
            legacy = b"FILE_STATES_V1\0" b"1\0file.txt\0FILE\0" + digest.encode() + b"\0" b"3\0" b"10\0"
            with patch.object(
                rsync_ops, "run_remote_script_input_bytes",
                side_effect=[(False, b"", ""), (True, legacy, "")],
            ):
                fallback = rsync_ops.remote_file_states(
                    cfg, rsync_ops.NasConnection("nas"), {"file.txt"}, compact=False,
                )
        assert states is not None and fallback is not None
        self.assertEqual(states["file.txt"].causal, CausalVersion((("device-a", 5),)))
        self.assertIsNone(fallback["file.txt"].causal)

    def test_causal_journal_falls_back_to_v2_when_server_rejects_v3(self) -> None:
        with ClientEnvironment():
            cfg = Config()
            cfg.set("remote_server_script", "/tmp/server.sh", persist=False)
            cfg.set("repository_id", "repository-123", persist=False)
            cfg.set("remote_causal_versions_available", True, persist=False)
            item = rsync_ops.TransferItem("upload", "causal.txt")
            with patch.object(
                rsync_ops, "run_remote_script_input",
                side_effect=[
                    (False, "", "journal: protocollo non riconosciuto"),
                    (True, "legacy ok", ""),
                ],
            ) as call:
                ok, detail = rsync_ops.append_remote_journal(
                    cfg, rsync_ops.NasConnection("nas"), "device-a", [item],
                    {item.path: Fingerprint("a" * 64, 3, 123)},
                    {item.path: CausalVersion((("device-a", 3),))},
                )
        self.assertTrue(ok)
        self.assertEqual(detail, "legacy ok")
        self.assertEqual(call.call_count, 2)
        self.assertTrue(call.call_args_list[0].args[4].startswith(b"JOURNAL_V3\0"))
        self.assertTrue(call.call_args_list[1].args[4].startswith(b"JOURNAL_V2\0"))

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

    def test_local_rename_journals_source_and_destination_atomically(self) -> None:
        with ServerSandbox() as sandbox:
            repository_id = sandbox.init_repository()
            source = sandbox.share / "old.txt"
            source.write_bytes(b"rename me")
            digest = hashlib.sha256(source.read_bytes()).hexdigest()
            size = source.stat().st_size
            mtime_ns = int(source.stat().st_mtime) * 1_000_000_000
            payload = b"\0".join([
                b"BROWSE_RENAME_V2", b"2026-08-30--12-00-00-000010Z", b"device-a",
                b"old.txt", b"new.txt", b"FILE", digest.encode(), str(size).encode(),
                str(mtime_ns).encode(),
            ]) + b"\0"
            result = sandbox.run_with_lease("--browse-rename", input=payload)
            journal = (sandbox.state / "transfer-journal.tsv").read_text()
            manifest = sandbox.run("--manifest-export")
            self.assertFalse(source.exists())
            self.assertEqual((sandbox.share / "new.txt").read_bytes(), b"rename me")
        self.assertEqual(result.returncode, 0, result.stderr.decode())
        self.assertIn("DELETE\told.txt\t", journal)
        self.assertIn("PUT\tnew.txt\t" + digest, journal)
        self.assertIn(b"old.txt\t\t0\t0\t", manifest.stdout)
        self.assertIn(b"new.txt\t" + digest.encode(), manifest.stdout)

    def test_local_rename_rejects_changed_source_and_occupied_destination(self) -> None:
        with ServerSandbox() as sandbox:
            sandbox.init_repository()
            source = sandbox.share / "old.txt"
            source.write_bytes(b"current")
            wrong_digest = hashlib.sha256(b"stale").hexdigest()
            payload = b"\0".join([
                b"BROWSE_RENAME_V2", b"2026-08-30--12-00-00-000011Z", b"device-a",
                b"old.txt", b"new.txt", b"FILE", wrong_digest.encode(), b"7",
                str(int(source.stat().st_mtime) * 1_000_000_000).encode(),
            ]) + b"\0"
            result = sandbox.run_with_lease("--browse-rename", input=payload)
            self.assertNotEqual(result.returncode, 0)
            self.assertEqual(source.read_bytes(), b"current")
            destination = sandbox.share / "new.txt"
            destination.write_bytes(b"keep")
            digest = hashlib.sha256(source.read_bytes()).hexdigest()
            payload = b"\0".join([
                b"BROWSE_RENAME_V2", b"2026-08-30--12-00-00-000013Z", b"device-a",
                b"old.txt", b"new.txt", b"FILE", digest.encode(), b"7",
                str(int(source.stat().st_mtime) * 1_000_000_000).encode(),
            ]) + b"\0"
            result = sandbox.run_with_lease("--browse-rename", input=payload)
            self.assertNotEqual(result.returncode, 0)
            self.assertEqual(source.read_bytes(), b"current")
            self.assertEqual(destination.read_bytes(), b"keep")

    def test_local_directory_rename_journals_nested_files(self) -> None:
        with ServerSandbox() as sandbox:
            sandbox.init_repository()
            source = sandbox.share / "old-dir"
            source.mkdir()
            (source / "nested").mkdir()
            (source / "nested" / "file.txt").write_bytes(b"nested")
            payload = b"\0".join([
                b"BROWSE_RENAME_V2", b"2026-08-30--12-00-00-000012Z", b"device-a",
                b"old-dir", b"new-dir", b"DIR", b"", b"0", b"0",
            ]) + b"\0"
            result = sandbox.run_with_lease("--browse-rename", input=payload)
            journal = (sandbox.state / "transfer-journal.tsv").read_text()
            manifest = sandbox.run("--manifest-export")
            self.assertFalse(source.exists())
            self.assertEqual((sandbox.share / "new-dir" / "nested" / "file.txt").read_bytes(), b"nested")
        self.assertEqual(result.returncode, 0, result.stderr.decode())
        self.assertIn("DELETE\told-dir/nested/file.txt\t", journal)
        self.assertIn("PUT\tnew-dir/nested/file.txt\t", journal)
        self.assertIn(b"new-dir/nested/file.txt\t", manifest.stdout)

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

    def test_causal_journal_round_trip_preserves_version_metadata(self) -> None:
        with ServerSandbox() as sandbox:
            repository_id = sandbox.init_repository()
            digest = hashlib.sha256(b"payload").hexdigest()
            payload = b"\0".join([
                b"JOURNAL_V3", repository_id.encode(), b"causal-tx", b"device-a", b"1700000000", b"1",
                b"PUT", b"causal.txt", digest.encode(), b"7", b"1700000000000000000", b"device-a:4",
            ]) + b"\0"
            result = sandbox.run_with_lease("--journal-append", input=payload)
            manifest = sandbox.run("--manifest-export")
        self.assertEqual(result.returncode, 0, result.stderr.decode())
        self.assertIn(b"causal.txt\t" + digest.encode() + b"\t7\t1700000000000000000\tdevice-a\t", manifest.stdout)
        self.assertTrue(manifest.stdout.rstrip().endswith(b"\tdevice-a:4"))


if __name__ == "__main__":
    unittest.main()
