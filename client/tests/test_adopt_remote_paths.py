"""Convergence of NAS-winning conflicts (``_adopt_remote_paths``).

A path the NAS deleted is gone whether the deletion was journaled as a
tombstone or the path is simply absent there (never journaled, or its tombstone
already pruned by retention). Treating the plain ABSENT case as an invalid,
"non regular" entry failed the whole adoption batch on every pass, so the same
conflicts were regenerated forever: one field client burned a full CPU core and
re-logged the same 31 conflicts every ~50 seconds for hours.
"""
from __future__ import annotations

import tempfile
import threading
import unittest
from pathlib import Path
from unittest.mock import Mock, patch

from sync_client import push_worker as push_worker_module
from sync_client import trash
from sync_client.push_worker import PushWorker
from sync_client.reconcile import RemoteKind, RemoteState
from sync_client.sync_state import Fingerprint


class AdoptRemotePathsTests(unittest.TestCase):
    def _worker(self, local_root: str) -> PushWorker:
        cfg = Mock()
        cfg.local_root.return_value = local_root
        worker = PushWorker(cfg, Mock(), Mock(), threading.Lock(), Mock())
        worker.set_connection(object())
        worker._log = Mock()
        return worker

    def test_absent_remote_path_is_adopted_instead_of_blocking_the_batch(self) -> None:
        with tempfile.TemporaryDirectory() as root:
            Path(root, "a.txt").write_text("local content", encoding="utf-8")
            worker = self._worker(root)
            expected = Fingerprint("digest", 13, 10)
            worker.sync_state.local_fingerprint.return_value = expected
            states = {"a.txt": RemoteState(RemoteKind.ABSENT)}
            with patch.object(
                push_worker_module.rsync_ops, "remote_file_states", return_value=states,
            ), patch.object(trash, "move_to_local_trash", return_value=True) as move:
                result = worker._adopt_remote_paths({"a.txt": expected}, "run-ts", None)

            self.assertTrue(result.ok, result.raw_error)
            move.assert_called_once()
            worker._log.assert_called_with(
                "DELETE_LOCAL", "a.txt", "versione NAS vincente: cancellazione adottata",
            )

    def test_tombstone_and_absent_paths_are_adopted_together(self) -> None:
        with tempfile.TemporaryDirectory() as root:
            for name in ("gone.txt", "never-journaled.txt"):
                Path(root, name).write_text("local", encoding="utf-8")
            worker = self._worker(root)
            expected = {
                "gone.txt": Fingerprint("a", 5, 10),
                "never-journaled.txt": Fingerprint("b", 5, 10),
            }
            worker.sync_state.local_fingerprint.side_effect = lambda _root, path: expected[path]
            states = {
                "gone.txt": RemoteState(RemoteKind.TOMBSTONE, mtime_ns=1),
                "never-journaled.txt": RemoteState(RemoteKind.ABSENT),
            }
            with patch.object(
                push_worker_module.rsync_ops, "remote_file_states", return_value=states,
            ), patch.object(trash, "move_to_local_trash", return_value=True):
                result = worker._adopt_remote_paths(expected, "run-ts", None)

            self.assertTrue(result.ok, result.raw_error)

    def test_non_regular_remote_path_still_blocks(self) -> None:
        # A directory/symlink where a regular file is expected must never be
        # reconciled silently -- only a genuinely deleted path was misclassified.
        with tempfile.TemporaryDirectory() as root:
            Path(root, "dir.txt").write_text("local", encoding="utf-8")
            worker = self._worker(root)
            expected = {"dir.txt": Fingerprint("digest", 5, 10)}
            worker.sync_state.local_fingerprint.return_value = expected["dir.txt"]
            states = {"dir.txt": RemoteState(RemoteKind.OTHER)}
            with patch.object(
                push_worker_module.rsync_ops, "remote_file_states", return_value=states,
            ):
                result = worker._adopt_remote_paths(expected, "run-ts", None)

            self.assertFalse(result.ok)
            self.assertIn("non regolare", result.raw_error or "")

    def test_local_edit_during_adoption_aborts_the_batch(self) -> None:
        with tempfile.TemporaryDirectory() as root:
            Path(root, "a.txt").write_text("local", encoding="utf-8")
            worker = self._worker(root)
            expected = {"a.txt": Fingerprint("digest", 5, 10)}
            worker.sync_state.local_fingerprint.return_value = Fingerprint("changed", 7, 11)
            states = {"a.txt": RemoteState(RemoteKind.FILE, "remote", 6, 12)}
            watcher = Mock()
            with patch.object(
                push_worker_module.rsync_ops, "remote_file_states", return_value=states,
            ):
                result = worker._adopt_remote_paths(expected, "run-ts", watcher)

            self.assertFalse(result.ok)
            self.assertIn("modifica locale durante", result.raw_error or "")
            watcher.mark_dirty.assert_called_once_with("a.txt")


if __name__ == "__main__":
    unittest.main()
