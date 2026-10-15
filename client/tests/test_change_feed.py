from __future__ import annotations

import subprocess
import time
import unittest
import os
from unittest.mock import patch

from sync_client import rsync_ops
from sync_client.config import Config

from tests.support import ClientEnvironment, ServerSandbox


class ChangeFeedClientTests(unittest.TestCase):
    def test_parser_accepts_changed_and_timeout_and_rejects_invalid_output(self) -> None:
        self.assertEqual(
            rsync_ops.parse_remote_revision_wait(
                "NASBOX_CHANGE_WAIT_V1\nRESULT=CHANGED\nREVISION=8\n"
            ),
            rsync_ops.RemoteRevisionWait("CHANGED", 8),
        )
        self.assertEqual(
            rsync_ops.parse_remote_revision_wait(
                "NASBOX_CHANGE_WAIT_V1\nRESULT=TIMEOUT\nREVISION=7\n"
            ),
            rsync_ops.RemoteRevisionWait("TIMEOUT", 7),
        )
        self.assertIsNone(rsync_ops.parse_remote_revision_wait("NASBOX_CHANGE_WAIT_V1\nRESULT=CHANGED\n"))
        self.assertIsNone(
            rsync_ops.parse_remote_revision_wait(
                "NASBOX_CHANGE_WAIT_V1\nRESULT=CHANGED\nREVISION=not-a-number\n"
            )
        )

    def test_call_has_validated_arguments_and_controlled_ssh_timeout(self) -> None:
        with ClientEnvironment():
            cfg = Config()
            cfg.set("remote_server_script", "/tmp/server.sh", persist=False)
            with patch.object(
                rsync_ops, "run_remote_script",
                return_value=(True, "NASBOX_CHANGE_WAIT_V1\nRESULT=TIMEOUT\nREVISION=7\n", ""),
            ) as call:
                result = rsync_ops.wait_for_remote_revision(
                    cfg, rsync_ops.NasConnection("nas"), 7, timeout=3,
                )
            self.assertEqual(result, rsync_ops.RemoteRevisionWait("TIMEOUT", 7))
            call.assert_called_once()
            self.assertEqual(call.call_args.args[3], ["--wait-for-revision", "7", "3"])
            self.assertEqual(call.call_args.kwargs["timeout"], 23)

    def test_unsupported_or_invalid_remote_response_falls_back(self) -> None:
        with ClientEnvironment():
            cfg = Config()
            cfg.set("remote_server_script", "/tmp/server.sh", persist=False)
            with patch.object(rsync_ops, "run_remote_script", return_value=(False, "", "unknown option")):
                self.assertIsNone(rsync_ops.wait_for_remote_revision(cfg, rsync_ops.NasConnection("nas"), 7, 1))
            with patch.object(
                rsync_ops, "run_remote_script",
                return_value=(True, "not a NASBox response\n", ""),
            ):
                self.assertIsNone(rsync_ops.wait_for_remote_revision(cfg, rsync_ops.NasConnection("nas"), 7, 1))

    def test_manifest_snapshot_uses_journal_status_without_a_known_revision(self) -> None:
        with ClientEnvironment():
            cfg = Config()
            cfg.set("remote_server_script", "/tmp/server.sh", persist=False)
            status = (
                "JOURNAL_FILE=/x\nMANIFEST_FILE=/y\nJOURNAL_BYTES=1\n"
                "MANIFEST_BYTES=2\nJOURNAL_MAX_BYTES=3\nMANIFEST_REVISION=7\n"
            )
            with patch.object(rsync_ops, "run_remote_script", return_value=(True, status, "")) as status_call, \
                    patch.object(rsync_ops, "run_remote_script_bytes") as export_call:
                result = rsync_ops.remote_manifest_snapshot(cfg, rsync_ops.NasConnection("nas"), 7)
            status_call.assert_called_once()
            export_call.assert_not_called()
            self.assertEqual(result, (7, None))

    def test_manifest_snapshot_skips_journal_status_when_feed_knows_the_revision(self) -> None:
        with ClientEnvironment():
            cfg = Config()
            cfg.set("remote_server_script", "/tmp/server.sh", persist=False)
            with patch.object(rsync_ops, "run_remote_script") as status_call, \
                    patch.object(
                        rsync_ops, "run_remote_script_bytes",
                        return_value=(True, b"NASBOX_MANIFEST_V1\n", ""),
                    ) as export_call:
                result = rsync_ops.remote_manifest_snapshot(
                    cfg, rsync_ops.NasConnection("nas"), 7, known_revision=9,
                )
            status_call.assert_not_called()
            export_call.assert_called_once()
            self.assertEqual(result, (9, {}))


class ChangeFeedServerTests(unittest.TestCase):
    def test_revision_already_changed_returns_without_waiting(self) -> None:
        with ServerSandbox() as sandbox:
            sandbox.init_repository()
            (sandbox.state / "manifest.revision").write_text("7\n")
            started = time.monotonic()
            result = sandbox.run("--wait-for-revision", "6", "10", timeout=3)
        self.assertEqual(result.returncode, 0, result.stderr.decode())
        self.assertEqual(
            result.stdout.decode(),
            "NASBOX_CHANGE_WAIT_V1\nRESULT=CHANGED\nREVISION=7\n",
        )
        self.assertLess(time.monotonic() - started, 2)

    def test_timeout_reads_only_revision_file(self) -> None:
        with ServerSandbox() as sandbox:
            sandbox.init_repository()
            started = time.monotonic()
            result = sandbox.run("--wait-for-revision", "0", "1", timeout=4)
            elapsed = time.monotonic() - started
            share_entries = list(sandbox.share.iterdir())
        self.assertEqual(result.returncode, 0, result.stderr.decode())
        self.assertEqual(result.stdout.decode(), "NASBOX_CHANGE_WAIT_V1\nRESULT=TIMEOUT\nREVISION=0\n")
        self.assertGreaterEqual(elapsed, 0.7)
        self.assertNotIn("manifest.tsv", {entry.name for entry in share_entries})

    def test_timeout_uses_light_file_poll_when_inotify_is_unusable(self) -> None:
        with ServerSandbox() as sandbox:
            fake_bin = sandbox.root / "bin"
            fake_bin.mkdir()
            fake_inotify = fake_bin / "inotifywait"
            fake_inotify.write_text("#!/bin/sh\nexit 1\n")
            fake_inotify.chmod(0o755)
            result = sandbox.run(
                "--wait-for-revision", "0", "1",
                env={"PATH": f"{fake_bin}:{os.defpath}"}, timeout=4,
            )
        self.assertEqual(result.returncode, 0, result.stderr.decode())
        self.assertEqual(result.stdout.decode(), "NASBOX_CHANGE_WAIT_V1\nRESULT=TIMEOUT\nREVISION=0\n")

    def test_wait_wakes_when_revision_file_is_replaced(self) -> None:
        with ServerSandbox() as sandbox:
            sandbox.init_repository()
            process = subprocess.Popen(
                [str(sandbox.script), "-c", str(sandbox.config), "--wait-for-revision", "0", "5"],
                stdout=subprocess.PIPE, stderr=subprocess.PIPE,
            )
            time.sleep(0.2)
            (sandbox.state / "manifest.revision").write_text("1\n")
            stdout, stderr = process.communicate(timeout=4)
        self.assertEqual(process.returncode, 0, stderr.decode())
        self.assertEqual(stdout.decode(), "NASBOX_CHANGE_WAIT_V1\nRESULT=CHANGED\nREVISION=1\n")

    def test_invalid_arguments_return_machine_readable_error(self) -> None:
        with ServerSandbox() as sandbox:
            result = sandbox.run("--wait-for-revision", "not-a-revision", "1")
        self.assertEqual(result.returncode, 2)
        self.assertEqual(result.stdout.decode(), "NASBOX_CHANGE_WAIT_V1\nRESULT=ERROR\nREVISION=0\n")

    def test_print_config_advertises_change_feed_capability(self) -> None:
        with ServerSandbox() as sandbox:
            result = sandbox.run("--print-config")
        self.assertEqual(result.returncode, 0, result.stderr.decode())
        self.assertIn(b"CHANGE_FEED_AVAILABLE=true\n", result.stdout)


if __name__ == "__main__":
    unittest.main()
