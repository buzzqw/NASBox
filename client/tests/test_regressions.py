from __future__ import annotations

import tempfile
import unittest
import subprocess
import os
import re
from io import StringIO
from pathlib import Path
from unittest.mock import Mock
from unittest.mock import patch

from sync_client import (
    engine, paths, pull_worker, push_worker, repository_safety, rsync_ops,
    scan_worker, trash, updater,
)
from sync_client.sync_state import Fingerprint, SyncStateStore
from sync_client.watcher import WatcherHandle

os.environ.setdefault("QT_QPA_PLATFORM", "offscreen")


class _Config:
    def __init__(self, local_root: str) -> None:
        self._local_root = local_root

    def local_root(self) -> str:
        return self._local_root


class _Logger:
    def log(self, *_args) -> None:
        pass


class _Watcher:
    def __init__(self, *_args, **_kwargs) -> None:
        self.was_marked_dirty = False

    def start(self) -> None:
        pass

    def stop(self) -> None:
        pass

    def mark_dirty(self) -> None:
        self.was_marked_dirty = True


class RegressionTests(unittest.TestCase):
    def test_pull_worker_exposes_batch_size_signal(self) -> None:
        self.assertTrue(hasattr(pull_worker.PullWorker, "batch_size_known"))

    def test_pull_skips_rsync_when_manifest_revision_is_unchanged(self) -> None:
        import threading
        import time

        cfg = Mock()
        cfg.is_paused.return_value = False
        cfg.is_configured.return_value = True
        cfg.get.side_effect = lambda key, default=None: {
            "poll_interval": 60,
            "remote_server_script": "/server.sh",
        }.get(key, default)
        watcher = Mock()
        watcher.is_dirty.return_value = False
        watchers = Mock()
        watchers.get.return_value = watcher
        state = Mock()
        worker = pull_worker.PullWorker(
            cfg, _Logger(), watchers, threading.Lock(), state,
        )
        worker._conn = rsync_ops.NasConnection("fake-host")
        worker._last_manifest_revision = 12
        worker._last_full_pull = time.time()

        with patch.object(rsync_ops, "remote_lock") as remote_lock, \
             patch.object(rsync_ops, "retry_pending_journal", return_value=(True, "")), \
             patch.object(rsync_ops, "validate_transfer_safety"), \
             patch.object(rsync_ops, "ensure_remote_dir", return_value=True), \
             patch.object(rsync_ops, "remote_manifest_snapshot", return_value=(12, None)), \
             patch.object(worker, "_run_transfer_tracked") as run_transfer:
            remote_lock.return_value.__enter__ = Mock(return_value=None)
            remote_lock.return_value.__exit__ = Mock(return_value=False)
            worker._tick()

        run_transfer.assert_not_called()
        self.assertGreater(worker._last_pull, 0)

    def test_pull_uses_manifest_paths_for_journalized_file_changes(self) -> None:
        import threading
        import time

        cfg = Mock()
        cfg.is_paused.return_value = False
        cfg.is_configured.return_value = True
        cfg.exclude_patterns.return_value = []
        cfg.local_root.return_value = "/fake-root"
        cfg.get.side_effect = lambda key, default=None: {
            "poll_interval": 60,
            "remote_server_script": "/server.sh",
            "remote_prefix": "/remote",
            "delete_enabled": False,
        }.get(key, default)
        watcher = Mock()
        watcher.is_dirty.return_value = False
        watchers = Mock()
        watchers.get.return_value = watcher
        state = Mock()
        state.get_many.return_value = {
            "remote.txt": Fingerprint("old", 3, 1_000_000_000),
        }
        state.fingerprint.return_value = Fingerprint("new", 123, 2_000_000_000)
        worker = pull_worker.PullWorker(
            cfg, _Logger(), watchers, threading.Lock(), state,
        )
        worker._conn = rsync_ops.NasConnection("fake-host")
        worker._last_manifest_revision = 11
        worker._last_full_pull = time.time()
        manifest_entries = {
            "remote.txt": rsync_ops.RemoteState(
                rsync_ops.RemoteKind.FILE, "new", 123, 2_000_000_000,
            ),
        }
        transfer_result = rsync_ops.TransferResult(
            True, [rsync_ops.TransferItem("download", "remote.txt", 123)],
        )

        with patch.object(rsync_ops, "remote_lock") as remote_lock, \
             patch.object(rsync_ops, "retry_pending_journal", return_value=(True, "")), \
             patch.object(rsync_ops, "validate_transfer_safety"), \
             patch.object(rsync_ops, "ensure_remote_dir", return_value=True), \
             patch.object(rsync_ops, "remote_manifest_snapshot", return_value=(12, manifest_entries)), \
             patch.object(rsync_ops, "remote_file_states", return_value=manifest_entries), \
             patch.object(worker, "_run_transfer_tracked", return_value=transfer_result) as run_transfer:
            remote_lock.return_value.__enter__ = Mock(return_value=None)
            remote_lock.return_value.__exit__ = Mock(return_value=False)
            worker._tick()

        self.assertEqual(run_transfer.call_args.kwargs["paths"], {"remote.txt"})
        self.assertEqual(worker._last_manifest_revision, 12)

    def test_server_package_exclusion_covers_the_whole_sync_daemon_folder(self) -> None:
        cfg = type("Config", (), {"get": lambda _self, key, default=None: {
            "remote_server_script": "/volume1/NASBox/sync-daemon/server/sync-daemon-server.sh",
            "remote_prefix": "/volume1/NASBox",
            "server_state_dir_remote": "/volume1/NASBox/sync-daemon/server/state",
            "exclude_patterns": [],
        }.get(key, default), "exclude_patterns": lambda _self: []})()

        self.assertEqual(rsync_ops.server_package_excluded_path(cfg), "sync-daemon")
        self.assertTrue(rsync_ops.path_is_excluded(cfg, "sync-daemon/README.md"))
        self.assertTrue(rsync_ops.path_is_excluded(cfg, "sync-daemon/server/server.conf"))
        self.assertFalse(rsync_ops.path_is_excluded(cfg, "RPG/manuale.pdf"))

    def test_empty_initial_folder_is_not_queued_for_push(self) -> None:
        with tempfile.TemporaryDirectory() as folder:
            with patch("sync_client.engine.FolderWatcher", _Watcher):
                watchers = WatcherHandle()
                coordinator = engine.SyncEngine(_Config(folder), _Logger(), watchers)
                coordinator._reconcile_watcher()

            watcher = watchers.get()
            self.assertIsNotNone(watcher)
            self.assertFalse(watcher.was_marked_dirty)

    def test_non_empty_initial_folder_is_queued_for_push(self) -> None:
        with tempfile.TemporaryDirectory() as folder:
            Path(folder, "existing.txt").touch()
            with patch("sync_client.engine.FolderWatcher", _Watcher):
                watchers = WatcherHandle()
                coordinator = engine.SyncEngine(_Config(folder), _Logger(), watchers)
                coordinator._reconcile_watcher()

            watcher = watchers.get()
            self.assertIsNotNone(watcher)
            self.assertTrue(watcher.was_marked_dirty)

    def test_manual_sync_requests_push_and_wakes_workers(self) -> None:
        cfg = type("Config", (), {"is_configured": lambda _self: True})()
        watchers = WatcherHandle()
        push_worker = Mock()
        pull_worker = Mock()
        coordinator = engine.SyncEngine(cfg, _Logger(), watchers, push_worker, pull_worker)

        coordinator.sync_now()

        push_worker.request_full_sync.assert_called_once_with()
        pull_worker.reset_schedule.assert_called_once_with()
        push_worker.wake.assert_called_once_with()
        pull_worker.wake.assert_called_once_with()

    def test_unknown_push_change_is_resolved_from_persistent_state(self) -> None:
        worker = push_worker.PushWorker.__new__(push_worker.PushWorker)
        worker.sync_state = Mock()
        worker.sync_state.changed_paths.return_value = {"changed.txt"}
        worker.cfg = type("Config", (), {"local_root": lambda _self: "/missing"})()

        with patch.object(rsync_ops, "path_is_excluded", return_value=False):
            paths = worker._resolve_paths({""}, False)

        self.assertEqual(paths, {"changed.txt"})
        worker.sync_state.changed_paths.assert_called_once_with("/missing", on_progress=worker._on_hash_progress)

    def test_clean_watcher_does_not_trigger_full_queue_state_walk(self) -> None:
        import threading

        cfg = Mock()
        cfg.is_configured.return_value = True
        cfg.local_root.return_value = "/fake-root"
        watcher = Mock()
        watcher.is_dirty.return_value = False
        watchers = Mock()
        watchers.get.return_value = watcher
        sync_state = Mock()
        sync_state.all_entries.return_value = {}
        worker = scan_worker.ScanWorker(
            cfg, sync_state=sync_state, transfer_lock=threading.Lock(), watchers=watchers,
        )
        worker._conn = rsync_ops.NasConnection("fake-host")

        with patch.object(rsync_ops, "scan", return_value=[]) as scan, \
             patch.object(rsync_ops, "remote_manifest_snapshot", return_value=(1, {})) as manifest:
            worker._scan_once()
            scan.assert_not_called()
            manifest.assert_called_once_with(worker.cfg, worker._conn, -1)

        sync_state.changed_paths.assert_not_called()

    def test_manifest_preview_reports_remote_changes_without_rsync_scan(self) -> None:
        import threading

        cfg = Mock()
        cfg.is_configured.return_value = True
        cfg.local_root.return_value = "/fake-root"
        cfg.exclude_patterns.return_value = []
        cfg.get.side_effect = lambda key, default=None: {
            "remote_server_script": "/server.sh",
            "delete_enabled": False,
            "remote_prefix": "/remote",
        }.get(key, default)
        watcher = Mock()
        watcher.is_dirty.return_value = False
        watchers = Mock()
        watchers.get.return_value = watcher
        sync_state = Mock()
        sync_state.all_entries.return_value = {
            "remote.txt": Fingerprint("old", 3, 1_000_000_000),
        }
        sync_state.fingerprint.return_value = None
        worker = scan_worker.ScanWorker(
            cfg, sync_state=sync_state, transfer_lock=threading.Lock(), watchers=watchers,
        )
        worker._conn = rsync_ops.NasConnection("fake-host")
        manifest_entries = {
            "remote.txt": rsync_ops.RemoteState(
                rsync_ops.RemoteKind.FILE, "new", 123, 2_000_000_000,
            ),
        }

        with patch.object(
            rsync_ops, "remote_manifest_snapshot", return_value=(7, manifest_entries),
        ), patch.object(rsync_ops, "scan") as scan:
            worker._scan_once()

        scan.assert_not_called()
        self.assertEqual(worker._manifest_revision, 7)
        self.assertEqual(worker._manifest_entries, manifest_entries)

    def test_manifest_preview_ignores_deleted_local_path_when_remote_is_absent(self) -> None:
        import threading

        with tempfile.TemporaryDirectory() as folder:
            cfg = Mock()
            cfg.is_configured.return_value = True
            cfg.local_root.return_value = folder
            cfg.exclude_patterns.return_value = []
            cfg.get.side_effect = lambda key, default=None: {
                "remote_server_script": "/server.sh",
                "delete_enabled": True,
                "remote_prefix": "/remote",
            }.get(key, default)
            watcher = Mock()
            watcher.is_dirty.return_value = True
            watcher.dirty_paths.return_value = {"already-gone.tmp"}
            watchers = Mock()
            watchers.get.return_value = watcher
            sync_state = Mock()
            sync_state.all_entries.return_value = {}
            sync_state.fingerprint.return_value = None
            worker = scan_worker.ScanWorker(
                cfg, sync_state=sync_state, transfer_lock=threading.Lock(), watchers=watchers,
            )
            worker._conn = rsync_ops.NasConnection("fake-host")

            with patch.object(
                rsync_ops, "remote_manifest_snapshot", return_value=(7, {}),
            ), patch.object(
                rsync_ops, "remote_file_states",
                return_value={"already-gone.tmp": rsync_ops.RemoteState(rsync_ops.RemoteKind.ABSENT)},
            ):
                items = worker._manifest_preview()

        self.assertEqual(items, [])

    def test_manifest_preview_falls_back_to_dry_run_when_unavailable(self) -> None:
        import threading

        cfg = Mock()
        cfg.is_configured.return_value = True
        cfg.local_root.return_value = "/fake-root"
        cfg.get.return_value = "/server.sh"
        watcher = Mock()
        watcher.is_dirty.return_value = False
        watchers = Mock()
        watchers.get.return_value = watcher
        sync_state = Mock()
        worker = scan_worker.ScanWorker(
            cfg, sync_state=sync_state, transfer_lock=threading.Lock(), watchers=watchers,
        )
        worker._conn = rsync_ops.NasConnection("fake-host")

        with patch.object(rsync_ops, "remote_manifest_snapshot", return_value=None), \
             patch.object(rsync_ops, "scan", return_value=[]) as scan:
            worker._scan_once()

        scan.assert_called_once()

    def _make_push_worker_for_tick(self):
        """A fully-initialized PushWorker (real __init__, so its Qt signals work)
        wired to no-op/mock collaborators, for exercising the real _tick() chunk
        loop end to end without touching a real NAS."""
        import threading

        cfg = Mock()
        cfg.is_paused.return_value = False
        cfg.is_configured.return_value = True
        cfg.local_root.return_value = "/fake-root"
        cfg.get.side_effect = lambda key, default=None: {
            "debounce_seconds": 2, "delete_enabled": True, "max_delete_files": 1000,
        }.get(key, default)

        watcher = Mock()
        watcher.is_dirty.return_value = True
        watcher.consume_paths_if_ready.return_value = set()
        watchers = Mock()
        watchers.get.return_value = watcher

        sync_state = Mock()
        sync_state.device_id.return_value = "dev01"

        worker = push_worker.PushWorker(cfg, _Logger(), watchers, threading.Lock(), sync_state)
        worker._conn = rsync_ops.NasConnection("fake-host")
        return worker, watcher

    def test_push_tick_processes_a_large_batch_in_chunks(self) -> None:
        worker, _watcher = self._make_push_worker_for_tick()
        all_paths = {f"file{i}.txt" for i in range(250)}  # > 2 * PUSH_CHUNK_SIZE (100)

        build_plan_calls: list[set[str]] = []
        compact_manifest_calls: list[bool] = []

        def fake_build_plan(relative_paths, **kwargs):
            build_plan_calls.append(set(relative_paths))
            compact_manifest_calls.append(kwargs["compact_remote_manifest"])
            return set(relative_paths), [], set()  # every path in the chunk is a plain upload, no deletes

        def fake_run_transfer_tracked(_transfer_fn, _run_ts, paths=None, **_kwargs):
            return rsync_ops.TransferResult(True, [rsync_ops.TransferItem("upload", p) for p in paths])

        with patch.object(worker, "_resolve_paths", return_value=all_paths), \
             patch.object(worker, "_build_plan", side_effect=fake_build_plan), \
             patch.object(worker, "_run_transfer_tracked", side_effect=fake_run_transfer_tracked), \
             patch.object(worker, "_authoritative_fingerprints", return_value={}), \
             patch.object(rsync_ops, "remote_lock") as remote_lock_mock, \
             patch.object(rsync_ops, "retry_pending_journal", return_value=(True, "")), \
             patch.object(rsync_ops, "validate_transfer_safety", return_value=None), \
             patch.object(rsync_ops, "ensure_remote_dir", return_value=True), \
             patch.object(rsync_ops, "checked_delete_remote", return_value=rsync_ops.CheckedDeleteResult(True, [], set(), set())), \
             patch.object(rsync_ops, "append_remote_journal", return_value=(True, "")):
            remote_lock_mock.return_value.__enter__ = Mock(return_value=None)
            remote_lock_mock.return_value.__exit__ = Mock(return_value=False)
            worker._force_sync.set()
            worker._tick()

        # 250 paths at PUSH_CHUNK_SIZE=100 -> chunks of 100, 100, 50
        self.assertEqual(len(build_plan_calls), 3)
        self.assertEqual([len(c) for c in build_plan_calls], [100, 100, 50])
        self.assertEqual(set().union(*build_plan_calls), all_paths)
        self.assertEqual(compact_manifest_calls, [True, False, False])
        # every chunk succeeded -> the whole operation is considered clean
        self.assertFalse(worker._force_sync.is_set())

    def test_push_plan_skips_manifest_compaction_after_first_chunk(self) -> None:
        worker, _watcher = self._make_push_worker_for_tick()
        worker.sync_state.fingerprint.return_value = Fingerprint("a" * 64, 1, 1)
        worker.sync_state.get.return_value = None
        remote_states = {"file.txt": rsync_ops.RemoteState(rsync_ops.RemoteKind.ABSENT)}

        with patch.object(rsync_ops, "remote_file_states", return_value=remote_states) as states:
            uploads, deletes, adopted = worker._build_plan(
                {"file.txt"}, compact_remote_manifest=False,
            )

        states.assert_called_once_with(
            worker.cfg, worker._conn, {"file.txt"}, compact=False, on_progress=None,
        )
        self.assertEqual(uploads, {"file.txt"})
        self.assertEqual(deletes, [])
        self.assertEqual(adopted, set())

    def test_push_tick_stops_at_cumulative_delete_limit_across_chunks(self) -> None:
        worker, watcher = self._make_push_worker_for_tick()
        worker.cfg.get.side_effect = lambda key, default=None: {
            "debounce_seconds": 2, "delete_enabled": True, "max_delete_files": 150,
        }.get(key, default)
        all_paths = {f"file{i}.txt" for i in range(250)}

        # Every chunk proposes 100 deletes and 0 uploads -- individually each
        # chunk is far under max_delete_files=150, but the running total crosses
        # it inside the *second* chunk (100 + 100 > 150).
        def fake_build_plan(relative_paths, **_kwargs):
            deletes = [(p, "a" * 64, 0) for p in relative_paths]
            return set(), deletes, set()

        checked_delete_calls: list[int] = []

        def fake_checked_delete(_cfg, _conn, requests, _run_ts, _device_id):
            checked_delete_calls.append(len(requests))
            return rsync_ops.CheckedDeleteResult(
                True, [rsync_ops.TransferItem("delete_remote", p) for p, _d, _m in requests],
                {p for p, _d, _m in requests}, set(),
            )

        with patch.object(worker, "_resolve_paths", return_value=all_paths), \
             patch.object(worker, "_build_plan", side_effect=fake_build_plan), \
             patch.object(worker, "_authoritative_fingerprints", return_value={}), \
             patch.object(rsync_ops, "remote_lock") as remote_lock_mock, \
             patch.object(rsync_ops, "retry_pending_journal", return_value=(True, "")), \
             patch.object(rsync_ops, "validate_transfer_safety", return_value=None), \
             patch.object(rsync_ops, "ensure_remote_dir", return_value=True), \
             patch.object(rsync_ops, "checked_delete_remote", side_effect=fake_checked_delete), \
             patch.object(rsync_ops, "append_remote_journal", return_value=(True, "")):
            remote_lock_mock.return_value.__enter__ = Mock(return_value=None)
            remote_lock_mock.return_value.__exit__ = Mock(return_value=False)
            worker._force_sync.set()
            worker._tick()

        # First chunk's 100 deletes were committed (under the limit at that point);
        # the second chunk would push the running total to 200 > 150, so it must
        # be rejected *before* being applied -- exactly one committed batch.
        self.assertEqual(checked_delete_calls, [100])
        # The operation as a whole is not considered clean -- retried later.
        self.assertTrue(worker._force_sync.is_set())
        self.assertTrue(watcher.mark_dirty.called)

    def test_push_tick_emits_one_started_finished_pair_even_when_no_chunk_uploads_anything(self) -> None:
        """Regression test: a multi-chunk tick that's all deletes (zero uploads in
        every chunk) never calls _run_transfer_tracked, so before emit_lifecycle
        existed, transfer_started never fired -- leaving TransfersTab stuck
        showing "waiting for the NAS lock" for the whole operation, even though
        real work (checked-delete calls) was actively happening chunk by chunk."""
        worker, _watcher = self._make_push_worker_for_tick()
        all_paths = {f"file{i}.txt" for i in range(250)}  # 3 chunks, all deletes, zero uploads

        def fake_build_plan(relative_paths, **_kwargs):
            deletes = [(p, "a" * 64, 0) for p in relative_paths]
            return set(), deletes, set()

        started_calls: list[str] = []
        finished_calls: list[tuple[str, bool]] = []
        worker.transfer_started.connect(lambda direction: started_calls.append(direction))
        worker.transfer_finished.connect(lambda direction, ok: finished_calls.append((direction, ok)))

        with patch.object(worker, "_resolve_paths", return_value=all_paths), \
             patch.object(worker, "_build_plan", side_effect=fake_build_plan), \
             patch.object(worker, "_authoritative_fingerprints", return_value={}), \
             patch.object(rsync_ops, "remote_lock") as remote_lock_mock, \
             patch.object(rsync_ops, "retry_pending_journal", return_value=(True, "")), \
             patch.object(rsync_ops, "validate_transfer_safety", return_value=None), \
             patch.object(rsync_ops, "ensure_remote_dir", return_value=True), \
             patch.object(
                 rsync_ops, "checked_delete_remote",
                 return_value=rsync_ops.CheckedDeleteResult(True, [], set(), set()),
             ), \
             patch.object(rsync_ops, "append_remote_journal", return_value=(True, "")):
            remote_lock_mock.return_value.__enter__ = Mock(return_value=None)
            remote_lock_mock.return_value.__exit__ = Mock(return_value=False)
            worker._force_sync.set()
            worker._tick()

        self.assertEqual(started_calls, ["upload"])
        self.assertEqual(finished_calls, [("upload", True)])

    def test_remote_lock_exposes_ssh_process_for_shutdown_cancellation(self) -> None:
        cfg = type("Config", (), {"get": lambda _self, key: {
            "server_lock_file_remote": "/remote/lock", "nas_user": "user", "ssh_port": 22,
        }.get(key, "")})()
        conn = rsync_ops.NasConnection("nas")
        proc = Mock()
        proc.stdout = StringIO("NASBOX_LOCKED\n")
        proc.stdin = StringIO()
        started = []

        with patch.object(rsync_ops.subprocess, "Popen", return_value=proc):
            with rsync_ops.remote_lock(cfg, conn, on_start=started.append):
                pass

        self.assertEqual(started, [proc])
        proc.terminate.assert_not_called()

    def test_transfer_queue_marks_active_file_and_removes_completed_item(self) -> None:
        from PyQt6.QtWidgets import QApplication
        from sync_client.gui.transfers_tab import TransfersTab

        app = QApplication.instance() or QApplication([])
        tab = TransfersTab()
        tab.on_queue_updated([
            rsync_ops.TransferItem("upload", "first.txt", 10),
            rsync_ops.TransferItem("upload", "second.txt", 20),
            rsync_ops.TransferItem("upload", "third.txt", 30),
        ])
        tab.on_transfer_started("upload")
        tab.on_item_started("upload", "first.txt")

        self.assertEqual(tab.table.item(0, 3).text(), "In corso")
        self.assertEqual(tab.table.verticalHeaderItem(0).text(), "1")
        self.assertEqual(tab.table.item(1, 1).text(), "second.txt")
        self.assertEqual(tab.table.item(1, 3).text(), "In attesa")
        self.assertEqual(tab.table.verticalHeaderItem(1).text(), "2")
        self.assertEqual(tab.table.item(2, 1).text(), "third.txt")
        self.assertEqual(tab.table.verticalHeaderItem(2).text(), "3")
        self.assertIn("first.txt", tab.activity_label.text())

        tab.on_item_done("upload", "first.txt")
        self.assertEqual(tab.queue_progress.value(), 33)
        tab._flush()
        self.assertEqual(tab.table.rowCount(), 2)
        self.assertEqual(tab.table.item(0, 1).text(), "second.txt")
        self.assertEqual(tab.table.item(0, 3).text(), "In attesa")
        self.assertEqual(tab.table.verticalHeaderItem(0).text(), "2")
        self.assertEqual(tab.table.item(1, 1).text(), "third.txt")
        self.assertEqual(tab.table.verticalHeaderItem(1).text(), "3")
        tab.on_queue_updated([
            rsync_ops.TransferItem("upload", "second.txt", 20),
            rsync_ops.TransferItem("upload", "third.txt", 30),
        ])
        self.assertEqual(tab.table.rowCount(), 2)
        tab.on_transfer_finished("upload", True)
        tab._flush()
        self.assertEqual(tab.table.rowCount(), 2)
        self.assertEqual(tab.table.item(0, 1).text(), "second.txt")
        self.assertEqual(tab.table.verticalHeaderItem(0).text(), "2")
        self.assertIn("1 operazioni", tab.activity_label.text())
        tab.deleteLater()

    def test_transfer_queue_shows_progress_only_on_active_file(self) -> None:
        from PyQt6.QtWidgets import QApplication, QProgressBar
        from sync_client.gui.transfers_tab import TransfersTab

        app = QApplication.instance() or QApplication([])
        tab = TransfersTab()
        tab.on_queue_updated([
            rsync_ops.TransferItem("upload", "first.txt", 10),
            rsync_ops.TransferItem("upload", "second.txt", 20),
        ])
        tab.on_transfer_started("upload")
        tab.on_item_started("upload", "first.txt")

        self.assertIsInstance(tab.table.cellWidget(0, 4), QProgressBar)
        self.assertIsNone(tab.table.cellWidget(1, 4))

        tab.on_item_progress("upload", "first.txt", 47)
        tab._flush()
        self.assertEqual(tab.table.cellWidget(0, 4).value(), 47)

        tab.on_item_done("upload", "first.txt")
        tab._flush()
        self.assertIsNone(tab.table.cellWidget(0, 4))
        self.assertIsNone(tab.table.cellWidget(1, 4))
        tab.deleteLater()

    def test_transfer_item_parser_keeps_rsync_file_size(self) -> None:
        item = rsync_ops._parse_itemize_line(
            ">f+++++++++|4096|folder/file.bin", "upload",
        )

        self.assertIsNotNone(item)
        self.assertEqual(item.size, 4096)

    def test_transfer_queue_shows_preflight_progress(self) -> None:
        from PyQt6.QtWidgets import QApplication
        from sync_client.gui.transfers_tab import TransfersTab

        app = QApplication.instance() or QApplication([])
        tab = TransfersTab()
        tab.on_preflight_progress(25, 100)

        self.assertIn("25", tab.activity_label.text())
        self.assertIn("100", tab.activity_label.text())

        tab.on_preflight_progress(100, 100)
        self.assertIn("monitorando", tab.activity_label.text())
        tab.deleteLater()

    def test_transfer_queue_does_not_claim_synced_while_preparing(self) -> None:
        from PyQt6.QtWidgets import QApplication
        from sync_client.gui.transfers_tab import TransfersTab

        app = QApplication.instance() or QApplication([])
        tab = TransfersTab()
        tab.on_transfer_preparing("upload")

        self.assertIn("preparazione", tab.activity_label.text())
        self.assertIn("Piano in preparazione", tab.summary_label.text())
        self.assertNotIn("tutto sincronizzato", tab.summary_label.text())
        self.assertIn("sto preparando", tab.queue_progress.format())

        tab.on_transfer_waiting_for_lock("upload")
        self.assertIn("lock NAS", tab.summary_label.text())
        self.assertNotIn("tutto sincronizzato", tab.summary_label.text())

        tab.on_transfer_finished("upload", True)
        self.assertIn("Nessuna differenza", tab.summary_label.text())

        tab.on_queue_scan_started()
        self.assertIn("verificando le differenze", tab.summary_label.text())
        self.assertIn("aggiorno l'anteprima", tab.queue_progress.format())
        tab.on_queue_scan_finished()
        self.assertIn("Nessuna differenza", tab.summary_label.text())
        tab.deleteLater()

    def test_watcher_honors_configured_exclusions(self) -> None:
        from sync_client.watcher import FolderWatcher

        watcher = FolderWatcher(
            "/tmp/NASBox",
            is_excluded=lambda relative: relative.startswith("sync-daemon/"),
        )

        self.assertTrue(watcher._is_internal_path("sync-daemon/client/main.py"))
        self.assertTrue(watcher._is_internal_path(".folder/.document.json.mWRFWA"))
        self.assertFalse(watcher._is_internal_path(".folder/.document.json"))
        self.assertFalse(watcher._is_internal_path("RPG/manuale.pdf"))

    def test_transfer_queue_places_uploads_first_and_removes_completed_items(self) -> None:
        from PyQt6.QtWidgets import QApplication
        from sync_client.gui.transfers_tab import TransfersTab

        app = QApplication.instance() or QApplication([])
        tab = TransfersTab()
        tab.on_queue_updated([
            rsync_ops.TransferItem("download", "remote.txt"),
            rsync_ops.TransferItem("upload", "local.txt"),
        ])

        self.assertEqual(tab.table.item(0, 1).text(), "local.txt")
        self.assertEqual(tab.table.item(1, 1).text(), "remote.txt")
        tab.on_transfer_started("upload")
        tab.on_item_started("upload", "local.txt")
        tab.on_item_done("upload", "local.txt")
        tab._flush()

        self.assertEqual(tab.table.item(0, 1).text(), "remote.txt")
        self.assertEqual(tab.table.rowCount(), 1)
        self.assertEqual(tab.table.verticalHeaderItem(0).text(), "2")
        tab.deleteLater()

    def test_tray_quit_hides_window_and_starts_shutdown(self) -> None:
        from sync_client.gui.tray import TrayIcon

        tray = type("FakeTray", (), {
            "_menu": Mock(),
            "main_window": Mock(),
        })()

        TrayIcon._quit(tray)

        tray._menu.hide.assert_called_once_with()
        tray.main_window.hide.assert_called_once_with()
        tray.main_window.quit_application.assert_called_once_with()

    def test_transfer_queue_shows_item_started_when_preview_was_empty(self) -> None:
        from PyQt6.QtWidgets import QApplication
        from sync_client.gui.transfers_tab import TransfersTab

        app = QApplication.instance() or QApplication([])
        tab = TransfersTab()
        tab.on_queue_updated([])
        tab.on_transfer_started("upload")
        tab.on_item_started("upload", "live.txt")

        self.assertEqual(tab.table.rowCount(), 1)
        self.assertEqual(tab.table.item(0, 1).text(), "live.txt")
        self.assertEqual(tab.table.item(0, 3).text(), "In corso")
        tab.deleteLater()

    def test_large_transfer_queue_limits_rendered_rows_but_keeps_search(self) -> None:
        from PyQt6.QtWidgets import QApplication
        from sync_client.gui.transfers_tab import MAX_VISIBLE_ROWS, TransfersTab

        app = QApplication.instance() or QApplication([])
        tab = TransfersTab()
        tab.on_queue_updated([
            rsync_ops.TransferItem("upload", f"folder/file-{index}.txt")
            for index in range(MAX_VISIBLE_ROWS + 1)
        ])

        self.assertEqual(tab.table.rowCount(), MAX_VISIBLE_ROWS)
        self.assertIn(str(MAX_VISIBLE_ROWS + 1), tab.queue_note_label.text())
        tab.search_edit.setText(f"file-{MAX_VISIBLE_ROWS}.txt")
        self.assertEqual(tab.table.rowCount(), 1)
        tab.deleteLater()

    def test_discovered_server_path_is_used_without_saved_config(self) -> None:
        cfg = type("Config", (), {"get": lambda _self, _key: ""})()
        calls: list[str] = []

        def run_remote_script(_cfg, _conn, path, _args, timeout):
            calls.append(path)
            self.assertEqual(timeout, 30)
            return True, "SHARE_ROOT=/volume1/NASBox\nRUNNING=true\n", ""

        with patch.object(rsync_ops, "run_remote_script", run_remote_script):
            ok, values, error = trash.fetch_remote_config(cfg, object(), "/remote/sync-daemon-server.sh")

        self.assertTrue(ok, error)
        self.assertEqual(values["SHARE_ROOT"], "/volume1/NASBox")
        self.assertEqual(calls, ["/remote/sync-daemon-server.sh"])

    def test_new_and_legacy_history_suffixes_are_readable(self) -> None:
        new_suffix = rsync_ops.new_run_ts()
        self.assertTrue(new_suffix.endswith("Z"))
        self.assertIsNotNone(trash._parse_suffix(f"report.txt-{new_suffix}"))
        self.assertIsNotNone(trash._parse_suffix("report.txt-2025-05-01--10-31-57"))

    def test_startup_update_finds_newer_local_bundle(self) -> None:
        with tempfile.TemporaryDirectory() as folder:
            root = Path(folder)
            current = root / "client"
            current.mkdir()
            (current / "main.py").write_text("old", encoding="utf-8")
            (current / "sync_client").mkdir()
            (current / "sync_client" / "version.py").write_text(
                'APP_VERSION = "1.0.0"\n', encoding="utf-8",
            )
            update = root / "client-update"
            update.mkdir()
            (update / "main.py").write_text("new", encoding="utf-8")
            (update / "sync_client").mkdir()
            (update / "sync_client" / "version.py").write_text(
                'APP_VERSION = "2.0.0"\n', encoding="utf-8",
            )
            cfg = type("Config", (), {"get": lambda _self, _key, default=None: default})()

            candidate = updater.find_update(cfg, current, str(current / "main.py"))

        self.assertIsNotNone(candidate)
        self.assertEqual(candidate.version, "2.0.0")

    def test_persistent_state_detects_later_local_change(self) -> None:
        with tempfile.TemporaryDirectory() as folder, tempfile.TemporaryDirectory() as state_dir:
            root = Path(folder)
            (root / "report.txt").write_text("first", encoding="utf-8")
            cfg = type("Config", (), {"get": lambda _self, key: {
                "nas_user": "user", "nas_lan": "nas", "remote_prefix": "/volume1/NASBox",
            }.get(key, "")})()
            with patch.object(paths, "sync_state_db_file", return_value=Path(state_dir) / "state.sqlite3"):
                state = SyncStateStore(cfg)
                state.record_local(str(root), "report.txt")
                (root / "report.txt").write_text("second", encoding="utf-8")
                self.assertIn("report.txt", state.changed_paths(str(root)))

    def test_persistent_state_queues_new_files_without_hashing_them_first(self) -> None:
        with tempfile.TemporaryDirectory() as folder, tempfile.TemporaryDirectory() as state_dir:
            root = Path(folder)
            (root / "new.txt").write_text("new", encoding="utf-8")
            cfg = type("Config", (), {"get": lambda _self, key: {
                "nas_user": "user", "nas_lan": "nas", "remote_prefix": "/volume1/NASBox",
            }.get(key, "")})()
            with patch.object(paths, "sync_state_db_file", return_value=Path(state_dir) / "state.sqlite3"):
                state = SyncStateStore(cfg)
                with patch.object(state, "fingerprint", wraps=state.fingerprint) as fingerprint:
                    self.assertEqual(state.changed_paths(str(root)), {"new.txt"})

            fingerprint.assert_not_called()

    def test_persistent_state_batches_many_completed_paths(self) -> None:
        with tempfile.TemporaryDirectory() as folder, tempfile.TemporaryDirectory() as state_dir:
            root = Path(folder)
            names = [f"batch/{index}.txt" for index in range(100)]
            for name in names:
                path = root / name
                path.parent.mkdir(parents=True, exist_ok=True)
                path.write_text(name, encoding="utf-8")
            cfg = type("Config", (), {"get": lambda _self, key: {
                "nas_user": "user", "nas_lan": "nas", "remote_prefix": "/volume1/NASBox",
            }.get(key, "")})()
            with patch.object(paths, "sync_state_db_file", return_value=Path(state_dir) / "state.sqlite3"):
                state = SyncStateStore(cfg)
                state.record_local_many(str(root), names)
                self.assertEqual(state.changed_paths(str(root)), set())

    def test_persistent_state_migrates_once_to_repository_id(self) -> None:
        with tempfile.TemporaryDirectory() as folder, tempfile.TemporaryDirectory() as state_dir:
            root = Path(folder)
            (root / "report.txt").write_text("base", encoding="utf-8")
            values = {
                "nas_user": "user", "nas_lan": "nas", "remote_prefix": "/volume1/NASBox",
            }
            legacy_cfg = type("Config", (), {
                "get": lambda _self, key, default=None: values.get(key, default),
            })()
            with patch.object(paths, "sync_state_db_file", return_value=Path(state_dir) / "state.sqlite3"):
                legacy = SyncStateStore(legacy_cfg)
                legacy.record_local(str(root), "report.txt")
                values["repository_id"] = "repository-1234567890"
                migrated = SyncStateStore(legacy_cfg)

                self.assertIsNotNone(migrated.get("report.txt"))
                self.assertTrue(migrated.has_entries())

    def test_remote_history_protocol_is_parsed_without_losing_spaces(self) -> None:
        cfg = type("Config", (), {"get": lambda _self, key: "/server.sh" if key == "remote_server_script" else ""})()
        payload = b"\x00".join((
            b"HISTORY_V1", b"folder/report one.txt", b"2025-05-01--10-31-57",
            b"folder/report one.txt-2025-05-01--10-31-57", b"123", b"",
        ))
        with patch.object(rsync_ops, "run_remote_script_bytes", return_value=(True, payload, "")):
            ok, versions, error = trash.list_remote_versions(cfg, object())

        self.assertTrue(ok, error)
        self.assertEqual(len(versions), 1)
        self.assertEqual(versions[0].relative_path, "folder/report one.txt")
        self.assertEqual(versions[0].size, 123)

    def test_remote_diagnostics_are_parsed(self) -> None:
        cfg = type("Config", (), {"get": lambda _self, key: "/server.sh" if key == "remote_server_script" else ""})()
        output = "TRASH_FILE_COUNT=4\nTRASH_DISK_BYTES=512\nSHARE_AVAILABLE_BYTES=1024\n"
        with patch.object(rsync_ops, "run_remote_script", return_value=(True, output, "")):
            ok, values, error = trash.fetch_remote_diagnostics(cfg, object())

        self.assertTrue(ok, error)
        self.assertEqual(values["TRASH_FILE_COUNT"], "4")

    def test_server_history_protocol_with_real_script(self) -> None:
        server_script = Path(__file__).resolve().parents[2] / "server" / "sync-daemon-server.sh"
        with tempfile.TemporaryDirectory() as folder:
            root = Path(folder) / "share"
            trash_root = root / ".sync-trash" / "nested"
            trash_root.mkdir(parents=True)
            (trash_root / "file name.txt-2025-05-01--10-31-57").write_text("version", encoding="utf-8")
            config = Path(folder) / "server.conf"
            config.write_text(f"SHARE_ROOT={root}\nRETENTION_DAYS=30\nCHECK_INTERVAL_MINUTES=60\n", encoding="utf-8")
            output = subprocess.run(
                [str(server_script), "-c", str(config), "--history-list"],
                capture_output=True, check=True, timeout=20,
            ).stdout

        fields = output.split(b"\0")
        self.assertEqual(fields[0], b"HISTORY_V1")
        self.assertEqual(fields[1], b"nested/file name.txt")
        self.assertEqual(fields[2], b"2025-05-01--10-31-57")

    def test_server_diagnostics_protocol_with_real_script(self) -> None:
        server_script = Path(__file__).resolve().parents[2] / "server" / "sync-daemon-server.sh"
        with tempfile.TemporaryDirectory() as folder:
            root = Path(folder) / "share"
            (root / ".sync-trash").mkdir(parents=True)
            (root / ".sync-trash" / "version-2025-05-01--10-31-57").write_bytes(b"x")
            config = Path(folder) / "server.conf"
            config.write_text(f"SHARE_ROOT={root}\nRETENTION_DAYS=30\n", encoding="utf-8")
            output = subprocess.run(
                [str(server_script), "-c", str(config), "--diagnostics"],
                capture_output=True, text=True, check=True, timeout=20,
            ).stdout

        values = dict(line.split("=", 1) for line in output.splitlines() if "=" in line)
        self.assertEqual(values["TRASH_FILE_COUNT"], "1")
        self.assertGreater(int(values["SHARE_AVAILABLE_BYTES"]), 0)

    def test_integrity_check_uses_checksums_in_both_directions(self) -> None:
        cfg, conn = object(), object()
        with patch.object(rsync_ops, "_dry_run", side_effect=[[], []]) as dry_run:
            self.assertEqual(rsync_ops.integrity_check(cfg, conn), [])

        self.assertEqual(dry_run.call_args_list[0].args[2], "upload")
        self.assertEqual(dry_run.call_args_list[1].args[2], "download")
        self.assertTrue(dry_run.call_args_list[0].kwargs["checksum"])
        self.assertTrue(dry_run.call_args_list[1].kwargs["checksum"])

    def test_local_repository_marker_rejects_a_changed_filesystem(self) -> None:
        with tempfile.TemporaryDirectory() as folder, tempfile.TemporaryDirectory() as state_dir:
            with patch.object(paths, "state_dir", return_value=Path(state_dir)):
                paths.ensure_dirs()
                repository_safety.initialize_local_root(folder, "repository-1234567890")
                marker = paths.repository_marker_file()
                value = marker.read_text(encoding="utf-8").replace(
                    '"st_dev": ', '"st_dev": 999999999, "old_st_dev": ', 1
                )
                marker.write_text(value, encoding="utf-8")
                with self.assertRaises(repository_safety.RepositorySafetyError):
                    repository_safety.validate_local_root(
                        folder, "repository-1234567890", destructive=True,
                    )

    def test_destructive_transfer_requires_remote_repository_marker(self) -> None:
        with tempfile.TemporaryDirectory() as folder, tempfile.TemporaryDirectory() as state_dir:
            with patch.object(paths, "state_dir", return_value=Path(state_dir)):
                repository_safety.initialize_local_root(folder, "repository-1234567890")
                cfg = type("Config", (), {
                    "local_root": lambda _self: folder,
                    "get": lambda _self, key, default=None: {
                        "repository_id": "repository-1234567890",
                        "remote_repository_ready": False,
                        "remote_prefix": "/volume1/NASBox",
                    }.get(key, default),
                })()
                with self.assertRaises(repository_safety.RepositorySafetyError):
                    rsync_ops.validate_transfer_safety(cfg, object(), destructive=True)

    def test_destructive_preflight_blocks_large_delete_batch(self) -> None:
        with tempfile.TemporaryDirectory() as folder, tempfile.TemporaryDirectory() as state_dir:
            with patch.object(paths, "state_dir", return_value=Path(state_dir)):
                repository_safety.initialize_local_root(folder, "repository-1234567890")
                cfg = type("Config", (), {
                    "local_root": lambda _self: folder,
                    "get": lambda _self, key, default=None: {
                        "repository_id": "repository-1234567890",
                        "remote_repository_ready": True,
                        "remote_prefix": "/volume1/NASBox",
                        "remote_server_script": "/server.sh",
                        "max_delete_files": 1,
                        "nas_user": "user",
                        "ssh_port": 22,
                    }.get(key, default),
                })()
                with patch.object(
                    rsync_ops, "run_remote_script",
                    return_value=(True, (
                        "REPOSITORY_ID=repository-1234567890\nREPOSITORY_READY=true\n"
                        "JOURNAL_READY=true\nPATH_RECONCILIATION_AVAILABLE=true\n"
                        "SYNC_LOCK_AVAILABLE=true\nSHARE_ROOT=/volume1/NASBox\n"
                    ), ""),
                ), patch.object(
                    rsync_ops, "_dry_run",
                    return_value=[
                        rsync_ops.TransferItem("delete_local", "one.txt"),
                        rsync_ops.TransferItem("delete_local", "two.txt"),
                    ],
                ):
                    with self.assertRaises(repository_safety.RepositorySafetyError):
                        rsync_ops.validate_transfer_safety(
                            cfg, rsync_ops.NasConnection("nas"),
                            destructive=True, direction="download",
                        )

    def test_tombstone_is_preserved_in_sync_state(self) -> None:
        with tempfile.TemporaryDirectory() as folder, tempfile.TemporaryDirectory() as state_dir:
            root = Path(folder)
            cfg = type("Config", (), {"get": lambda _self, key: {
                "nas_user": "user", "nas_lan": "nas", "remote_prefix": "/volume1/NASBox",
            }.get(key, "")})()
            with patch.object(paths, "sync_state_db_file", return_value=Path(state_dir) / "state.sqlite3"):
                state = SyncStateStore(cfg)
                state.record_local(str(root), "deleted.txt")
                tombstone = state.get("deleted.txt")
                self.assertIsNotNone(tombstone)
                self.assertTrue(tombstone.is_tombstone)
                self.assertNotIn("deleted.txt", state.changed_paths(str(root)))

    def test_server_repository_marker_can_be_initialized_explicitly(self) -> None:
        server_script = Path(__file__).resolve().parents[2] / "server" / "sync-daemon-server.sh"
        with tempfile.TemporaryDirectory() as folder:
            root = Path(folder) / "share"
            root.mkdir()
            config = Path(folder) / "server.conf"
            config.write_text(f"SHARE_ROOT={root}\nRETENTION_DAYS=30\n", encoding="utf-8")
            subprocess.run(
                [str(server_script), "-c", str(config), "--init-repository"],
                capture_output=True, text=True, check=True, timeout=20,
            )
            output = subprocess.run(
                [str(server_script), "-c", str(config), "--print-config"],
                capture_output=True, text=True, check=True, timeout=20,
            ).stdout
            values = dict(line.split("=", 1) for line in output.splitlines() if "=" in line)
            self.assertEqual(values["REPOSITORY_READY"], "true")
            self.assertTrue(values["REPOSITORY_ID"])

    def test_server_journal_commit_is_exported_as_manifest(self) -> None:
        server_script = Path(__file__).resolve().parents[2] / "server" / "sync-daemon-server.sh"
        with tempfile.TemporaryDirectory() as folder:
            root = Path(folder) / "share"
            root.mkdir()
            config = Path(folder) / "server.conf"
            config.write_text(f"SHARE_ROOT={root}\nRETENTION_DAYS=30\n", encoding="utf-8")
            subprocess.run(
                [str(server_script), "-c", str(config), "--init-repository"],
                capture_output=True, text=True, check=True, timeout=20,
            )
            payload = b"\0".join((
                b"JOURNAL_V1", b"tx-test", b"device01", b"1700000000", b"1",
                b"PUT", b"folder/report one.txt", b"a" * 64, b"12", b"1700000000000000000", b"",
            )) + b"\0"
            subprocess.run(
                [str(server_script), "-c", str(config), "--journal-append"],
                input=payload, capture_output=True, check=True, timeout=20,
            )
            control_payload = b"\0".join((
                b"JOURNAL_V1", b"tx-control", b"device01", b"1700000001", b"1",
                b"PUT", b"folder/report\tline\nname.txt", b"c" * 64, b"8", b"2", b"",
            )) + b"\0"
            subprocess.run(
                [str(server_script), "-c", str(config), "--journal-append"],
                input=control_payload, capture_output=True, check=True, timeout=20,
            )
            manifest = subprocess.run(
                [str(server_script), "-c", str(config), "--manifest-export"],
                capture_output=True, text=True, check=True, timeout=20,
            ).stdout
            self.assertIn("folder/report one.txt", manifest)
            self.assertIn("a" * 64, manifest)
            self.assertIn("folder/report%09line%0Aname.txt", manifest)
            entry = subprocess.run(
                [str(server_script), "-c", str(config), "--manifest-get", "folder/report one.txt"],
                capture_output=True, text=True, check=True, timeout=20,
            ).stdout
            self.assertTrue(entry.startswith("MANIFEST_HIT\t"))

    def test_client_journal_payload_preserves_control_characters_in_paths(self) -> None:
        with tempfile.TemporaryDirectory() as folder:
            root = Path(folder)
            relative = "folder/report\tone\nline.txt"
            target = root / relative
            target.parent.mkdir(parents=True)
            target.write_bytes(b"journal payload")
            cfg = type("Config", (), {
                "local_root": lambda _self: folder,
                "get": lambda _self, key, default=None: {
                    "remote_server_script": "/server.sh",
                    "repository_id": "repository-1234567890",
                }.get(key, default),
            })()
            payload, error = rsync_ops.build_remote_journal_payload(
                cfg, "device01", [rsync_ops.TransferItem("upload", relative)],
            )
            self.assertEqual(error, "")
            self.assertIsNotNone(payload)
            self.assertTrue(payload.startswith(b"JOURNAL_V2\0repository-1234567890\0"))
            self.assertIn(relative.encode(), payload)

    def test_rsync_item_parser_decodes_escaped_newline(self) -> None:
        item = rsync_ops._parse_itemize_line(">f+++++++++|4|folder/line\\#012name.txt", "upload")

        self.assertIsNotNone(item)
        self.assertEqual(item.path, "folder/line\nname.txt")

    def test_queue_preview_never_reports_upload_and_delete_for_same_path(self) -> None:
        upload = rsync_ops.TransferItem("upload", "local-only.txt")
        delete_local = rsync_ops.TransferItem("delete_local", "local-only.txt")
        with patch.object(rsync_ops, "_dry_run", side_effect=[[upload], [delete_local]]):
            items = rsync_ops.scan(object(), object())

        self.assertEqual(items, [upload])

    def test_selected_upload_is_checksum_guarded_without_update_or_delete(self) -> None:
        with tempfile.TemporaryDirectory() as folder:
            cfg = type("Config", (), {
                "local_root": lambda _self: folder,
                "exclude_patterns": lambda _self: [],
                "get": lambda _self, key, default=None: {
                    "nas_user": "user", "remote_prefix": "/share", "ssh_port": 22,
                    "delete_enabled": True,
                }.get(key, default),
            })()
            proc = Mock(returncode=0, stdout=StringIO())
            with patch.object(rsync_ops.subprocess, "Popen", return_value=proc) as popen:
                result = rsync_ops.push(
                    cfg, rsync_ops.NasConnection("nas"), rsync_ops.new_run_ts(), paths={"file.txt"},
                )

        self.assertTrue(result.ok)
        command = popen.call_args.args[0]
        self.assertIn("--checksum", command)
        self.assertIn("--no-links", command)
        self.assertNotIn("--update", command)
        self.assertNotIn(rsync_ops.DELETE_FLAG, command)
        self.assertTrue(any(arg.startswith("--files-from=") for arg in command))

    def test_selected_pull_uses_checksum_without_global_delete(self) -> None:
        with tempfile.TemporaryDirectory() as folder:
            cfg = type("Config", (), {
                "local_root": lambda _self: folder,
                "exclude_patterns": lambda _self: [],
                "get": lambda _self, key, default=None: {
                    "nas_user": "user", "remote_prefix": "/share", "ssh_port": 22,
                    "delete_enabled": True,
                }.get(key, default),
            })()
            proc = Mock(returncode=0, stdout=StringIO())
            with patch.object(rsync_ops.subprocess, "Popen", return_value=proc) as popen:
                result = rsync_ops.pull(
                    cfg, rsync_ops.NasConnection("nas"), rsync_ops.new_run_ts(), paths={"file.txt"},
                )

        self.assertTrue(result.ok)
        command = popen.call_args.args[0]
        self.assertIn("--checksum", command)
        self.assertNotIn("--update", command)
        self.assertNotIn(rsync_ops.DELETE_FLAG, command)

    def test_transfer_safety_rejects_different_server_root(self) -> None:
        with tempfile.TemporaryDirectory() as folder, tempfile.TemporaryDirectory() as state_dir:
            with patch.object(paths, "state_dir", return_value=Path(state_dir)):
                repository_safety.initialize_local_root(folder, "repository-1234567890")
                cfg = type("Config", (), {
                    "local_root": lambda _self: folder,
                    "get": lambda _self, key, default=None: {
                        "repository_id": "repository-1234567890",
                        "remote_prefix": "/expected/share",
                        "remote_server_script": "/server.sh",
                    }.get(key, default),
                })()
                output = (
                    "REPOSITORY_ID=repository-1234567890\nREPOSITORY_READY=true\n"
                    "JOURNAL_READY=true\nPATH_RECONCILIATION_AVAILABLE=true\n"
                    "SYNC_LOCK_AVAILABLE=true\nSHARE_ROOT=/different/share\n"
                )
                with patch.object(rsync_ops, "run_remote_script", return_value=(True, output, "")):
                    with self.assertRaises(repository_safety.RepositorySafetyError):
                        rsync_ops.validate_transfer_safety(
                            cfg, rsync_ops.NasConnection("nas"), destructive=False,
                        )

    def test_remote_manifest_state_parses_hit_and_miss(self) -> None:
        cfg = type("Config", (), {"get": lambda _self, key, default=None: {
            "remote_server_script": "/server.sh",
        }.get(key, default)})()
        digest = "b" * 64
        with patch.object(rsync_ops, "run_remote_script", return_value=(
            True, f"MANIFEST_HIT\tfile.txt\t{digest}\t4\t1\tdevice\t2\n", "",
        )):
            self.assertEqual(rsync_ops.remote_manifest_state(cfg, rsync_ops.NasConnection("nas"), "file.txt"), (True, digest))
        with patch.object(rsync_ops, "run_remote_script", return_value=(True, "MANIFEST_MISS\n", "")):
            self.assertEqual(rsync_ops.remote_manifest_state(cfg, rsync_ops.NasConnection("nas"), "file.txt"), (False, None))

    def test_manifest_snapshot_is_downloaded_only_when_revision_changes(self) -> None:
        cfg = type("Config", (), {"get": lambda _self, key, default=None: {
            "remote_server_script": "/server.sh",
        }.get(key, default)})()
        digest = b"d" * 64
        manifest = b"NASBOX_MANIFEST_V1\nfile.txt\t" + digest + b"\t4\t10\tdevice\t20\n"
        with patch.object(
            rsync_ops, "run_remote_script",
            return_value=(True, "MANIFEST_REVISION=7\n", ""),
        ), patch.object(
            rsync_ops, "run_remote_script_bytes", return_value=(True, manifest, ""),
        ) as export:
            revision, entries = rsync_ops.remote_manifest_snapshot(
                cfg, rsync_ops.NasConnection("nas"), 6,
            )
            unchanged_revision, unchanged = rsync_ops.remote_manifest_snapshot(
                cfg, rsync_ops.NasConnection("nas"), 7,
            )

        self.assertEqual(revision, 7)
        self.assertEqual(entries["file.txt"].digest, digest.decode())
        self.assertEqual((unchanged_revision, unchanged), (7, None))
        export.assert_called_once()

    def test_remote_script_discovery_uses_ps_tokens_without_grep_extensions(self) -> None:
        cfg = type("Config", (), {
            "get": lambda _self, key, default=None: {
                "nas_user": "user", "ssh_port": 22,
            }.get(key, default),
        })()
        process = Mock(returncode=0, stdout=(
            "/volume1/NASBox/server/sync-daemon-server.sh\n"
            "/volume1/NASBox/server/sync-daemon-server.sh\n"
        ))
        with patch.object(rsync_ops.subprocess, "run", return_value=process) as run:
            found = rsync_ops.discover_remote_scripts(cfg, rsync_ops.NasConnection("nas"))
        self.assertEqual(found, ["/volume1/NASBox/server/sync-daemon-server.sh"])
        self.assertIn("awk", run.call_args.args[0][-1])
        self.assertNotIn("grep -o", run.call_args.args[0][-1])

    def test_client_required_server_version_matches_server_script(self) -> None:
        server_script = Path(__file__).resolve().parents[2] / "server" / "sync-daemon-server.sh"
        source = server_script.read_text(encoding="utf-8")
        match = re.search(r'^VERSION="([0-9.]+)"$', source, re.MULTILINE)
        self.assertIsNotNone(match)
        self.assertEqual(rsync_ops.EXPECTED_SERVER_VERSION, match.group(1))

    def test_detect_falls_back_when_saved_server_script_is_stale(self) -> None:
        from sync_client.gui.settings_tab import _detect_worker

        cfg = type("Config", (), {"get": lambda _self, key, default=None: default})()
        with patch.object(rsync_ops, "resolve_connection", return_value=rsync_ops.NasConnection("nas")), \
             patch.object(rsync_ops, "discover_remote_scripts", return_value=["/new/server.sh"]), \
             patch.object(
                 trash, "fetch_remote_config",
                 side_effect=[
                     (False, {}, "percorso vecchio"),
                     (True, {"SHARE_ROOT": "/volume1/NASBox", "RUNNING": "true"}, ""),
                 ],
             ) as fetch:
            result = _detect_worker(cfg, "/old/server.sh")

        self.assertTrue(result["fetch_ok"])
        self.assertEqual(result["script_path"], "/new/server.sh")
        self.assertEqual(fetch.call_count, 2)

    def test_server_version_mismatch_emits_update_warning(self) -> None:
        from sync_client import trash

        values = {
            "server_state_dir_remote": "",
            "server_lock_file_remote": "",
            "repository_id": "repository-1234567890",
            "remote_repository_ready": False,
            "remote_journal_ready": False,
            "remote_server_script": "/server.sh",
        }
        cfg = type("Config", (), {
            "get": lambda _self, key, default=None: values.get(key, default),
            "set": lambda _self, key, value: values.__setitem__(key, value),
        })()
        coordinator = engine.SyncEngine(cfg, _Logger(), WatcherHandle())
        coordinator._conn = rsync_ops.NasConnection("nas")
        messages: list[str] = []
        coordinator.server_outdated.connect(messages.append)

        with patch.object(
            trash, "fetch_remote_config",
            return_value=(True, {
                "VERSION": "3.8.0", "running": True,
                "REPOSITORY_ID": "repository-1234567890",
                "REPOSITORY_READY": "true", "JOURNAL_READY": "true",
            }, ""),
        ), patch.object(rsync_ops, "discover_remote_server_update", return_value=None):
            coordinator._check_server_health()

        self.assertEqual(len(messages), 1)
        self.assertIn("3.8.0", messages[0])
        self.assertIn("cartella server/", messages[0])

    def test_server_daemon_down_is_started_automatically(self) -> None:
        values = {
            "server_state_dir_remote": "",
            "server_lock_file_remote": "",
            "repository_id": "repository-1234567890",
            "remote_repository_ready": False,
            "remote_journal_ready": False,
            "remote_server_script": "/server/sync-daemon-server.sh",
        }
        cfg = type("Config", (), {
            "get": lambda _self, key, default=None: values.get(key, default),
            "set": lambda _self, key, value: values.__setitem__(key, value),
        })()
        coordinator = engine.SyncEngine(cfg, _Logger(), WatcherHandle())
        coordinator._conn = rsync_ops.NasConnection("nas")

        with patch.object(
            trash, "fetch_remote_config",
            return_value=(True, {
                "VERSION": "3.8.1", "running": False,
                "REPOSITORY_ID": "repository-1234567890",
                "REPOSITORY_READY": "true", "JOURNAL_READY": "true",
            }, ""),
        ), patch.object(rsync_ops, "discover_remote_server_update", return_value=None), \
             patch.object(coordinator, "_restart_server_daemon") as restart:
            coordinator._check_server_health()

        restart.assert_called_once_with()

    def test_versioned_server_script_is_offered_when_newer(self) -> None:
        values = {
            "server_state_dir_remote": "",
            "server_lock_file_remote": "",
            "repository_id": "repository-1234567890",
            "remote_repository_ready": False,
            "remote_journal_ready": False,
            "remote_server_script": "/server/sync-daemon-server.sh",
        }
        cfg = type("Config", (), {
            "get": lambda _self, key, default=None: values.get(key, default),
            "set": lambda _self, key, value: values.__setitem__(key, value),
        })()
        coordinator = engine.SyncEngine(cfg, _Logger(), WatcherHandle())
        coordinator._conn = rsync_ops.NasConnection("nas")
        offered: list[tuple[str, str, str]] = []
        coordinator.server_update_available.connect(lambda *args: offered.append(args))

        with patch.object(
            trash, "fetch_remote_config",
            return_value=(True, {
                "VERSION": "3.8.0", "running": True,
                "REPOSITORY_ID": "repository-1234567890",
                "REPOSITORY_READY": "true", "JOURNAL_READY": "true",
            }, ""),
        ), patch.object(
            rsync_ops, "discover_remote_server_update",
            return_value=("/server/sync-daemon-server-3.8.1.sh", "3.8.1"),
        ):
            coordinator._check_server_health()

        self.assertEqual(len(offered), 1)
        self.assertIn("3.8.1", offered[0][0])
        self.assertEqual(offered[0][1], "/server/sync-daemon-server-3.8.1.sh")


if __name__ == "__main__":
    unittest.main()
