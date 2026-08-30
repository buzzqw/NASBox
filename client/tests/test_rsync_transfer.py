from __future__ import annotations

import io
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch

from sync_client import rsync_ops
from sync_client.config import Config
from sync_client.push_worker import PushWorker
from sync_client.sync_state import Fingerprint

from tests.support import ClientEnvironment


class _FakeProcess:
    def __init__(self, output: str = "", returncode: int | None = 0) -> None:
        self.stdout = io.StringIO(output)
        self.returncode = returncode
        self.args = ["rsync"]
        self.terminated = False
        self.killed = False

    def poll(self) -> int | None:
        return self.returncode

    def wait(self, timeout: float | None = None) -> int:
        if self.returncode is None:
            self.returncode = 143 if self.terminated else 1
        return self.returncode

    def terminate(self) -> None:
        self.terminated = True

    def kill(self) -> None:
        self.killed = True
        self.returncode = 137


class RsyncTransferTests(unittest.TestCase):
    def setUp(self) -> None:
        self.environment = ClientEnvironment()
        self.environment.__enter__()
        self.root = Path(tempfile.mkdtemp(prefix="nasbox-rsync-client-"))
        self.cfg = Config()
        self.cfg.set("local_root", str(self.root), persist=False)
        self.cfg.set("nas_user", "nas", persist=False)
        self.cfg.set("remote_prefix", "/volume1/NASBox", persist=False)
        self.conn = rsync_ops.NasConnection("nas")

    def tearDown(self) -> None:
        for path in sorted(self.root.rglob("*"), reverse=True):
            if path.is_file() or path.is_symlink():
                path.unlink()
            elif path.is_dir():
                path.rmdir()
        self.root.rmdir()
        self.environment.__exit__(None, None, None)

    def _run_with_process(self, process: _FakeProcess, function, **kwargs):
        with patch.object(rsync_ops, "_old_args_flag", return_value=[]), patch.object(
            rsync_ops.subprocess, "Popen", return_value=process,
        ) as popen:
            result = function(self.cfg, self.conn, "run", paths={"large.bin"}, **kwargs)
        return result, popen.call_args.args[0]

    def test_canonical_transfers_keep_atomic_resume_profile(self) -> None:
        result, command = self._run_with_process(_FakeProcess(), rsync_ops.push)
        pull_result, pull_command = self._run_with_process(_FakeProcess(), rsync_ops.pull)

        self.assertTrue(result.ok)
        self.assertTrue(pull_result.ok)
        self.assertIn("--partial", command)
        self.assertIn("--partial-dir=.sync-partial", command)
        self.assertIn("--delay-updates", command)
        self.assertIn("--outbuf=L", command)
        self.assertNotIn("--append-verify", command)
        self.assertIn("--partial-dir=.sync-partial", pull_command)
        self.assertIn("--delay-updates", pull_command)
        self.assertNotIn("--append-verify", pull_command)
        files_from = next(value.split("=", 1)[1] for value in command if value.startswith("--files-from="))
        self.assertFalse(Path(files_from).exists())

    def test_private_staging_uses_append_verify_without_live_atomic_option(self) -> None:
        process = _FakeProcess()
        with patch.object(rsync_ops, "_old_args_flag", return_value=[]), patch.object(
            rsync_ops.subprocess, "Popen", return_value=process,
        ) as popen:
            result = rsync_ops.push_to_staging(
                self.cfg, self.conn, "run", "/volume1/NASBox/.nasbox-staging/tx",
                paths={"large.bin"},
            )
        command = popen.call_args.args[0]
        self.assertTrue(result.ok)
        self.assertIn("--append-verify", command)
        self.assertIn("--partial", command)
        self.assertNotIn("--partial-dir=.sync-partial", command)
        self.assertNotIn("--delay-updates", command)

    def test_cancel_preserves_resume_partial_and_does_not_report_completion(self) -> None:
        process = _FakeProcess(">f+++++++++|100|large.bin\n", returncode=None)
        with patch.object(rsync_ops, "_old_args_flag", return_value=[]), patch.object(
            rsync_ops.subprocess, "Popen", return_value=process,
        ):
            result = rsync_ops.push(
                self.cfg, self.conn, "run", paths={"large.bin"}, cancel_check=lambda: True,
            )

        self.assertFalse(result.ok)
        self.assertTrue(result.cancelled)
        self.assertTrue(result.partial_preserved)
        self.assertTrue(process.terminated)
        self.assertEqual(result.items, [])

    def test_failed_pull_keeps_partial_but_successful_pull_cleans_named_partial(self) -> None:
        partial = self.root / ".sync-partial" / "large.bin"
        partial.parent.mkdir()
        partial.write_bytes(b"prefix")
        failed = _FakeProcess(returncode=23)
        result, _command = self._run_with_process(failed, rsync_ops.pull)
        self.assertFalse(result.ok)
        self.assertTrue(partial.exists())

        successful = _FakeProcess()
        result, _command = self._run_with_process(successful, rsync_ops.pull)
        self.assertTrue(result.ok)
        self.assertFalse(partial.exists())

    def test_diagnostic_output_is_bounded(self) -> None:
        noisy_output = "".join("rsync warning: transient output\n" for _ in range(10000))
        result, _command = self._run_with_process(_FakeProcess(noisy_output, returncode=23), rsync_ops.push)

        self.assertFalse(result.ok)
        self.assertLessEqual(len(result.raw_error.encode()), rsync_ops.MAX_TRANSFER_ERROR_BYTES)

    def test_staging_transaction_id_changes_when_source_fingerprint_changes(self) -> None:
        worker = PushWorker.__new__(PushWorker)
        first = Fingerprint("a" * 64, 10, 20)
        second = Fingerprint("b" * 64, 10, 20)

        first_id = worker._staging_transaction_id({"large.bin"}, {"large.bin": first})
        same_id = worker._staging_transaction_id({"large.bin"}, {"large.bin": first})
        changed_id = worker._staging_transaction_id({"large.bin"}, {"large.bin": second})

        self.assertEqual(first_id, same_id)
        self.assertNotEqual(first_id, changed_id)
        self.assertTrue(first_id.startswith("publish-"))


if __name__ == "__main__":
    unittest.main()
