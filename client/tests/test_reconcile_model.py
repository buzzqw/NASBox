from __future__ import annotations

import unittest

from sync_client.reconcile import Action, RemoteKind, RemoteState, plan_path
from sync_client.sync_state import Fingerprint


def fp(digest: str, mtime_seconds: int) -> Fingerprint:
    return Fingerprint(digest * 64, 1, mtime_seconds * 1_000_000_000)


def remote(kind: RemoteKind, digest: str = "", mtime_seconds: int = 0) -> RemoteState:
    return RemoteState(kind, digest * 64 if digest else "", 1, mtime_seconds * 1_000_000_000)


class ReconcileModelTests(unittest.TestCase):
    def test_valid_delete_requires_unchanged_remote_file_and_mtime(self) -> None:
        base = fp("a", 10)
        self.assertEqual(
            plan_path(base, None, remote(RemoteKind.FILE, "a", 10), delete_enabled=True).action,
            Action.DELETE_REMOTE,
        )
        self.assertEqual(
            plan_path(base, None, remote(RemoteKind.FILE, "a", 11), delete_enabled=True).action,
            Action.REMOTE_WINS,
        )

    def test_stale_delete_never_removes_new_remote_data(self) -> None:
        decision = plan_path(
            fp("a", 10), None, remote(RemoteKind.FILE, "b", 20), delete_enabled=True,
        )
        self.assertEqual(decision.action, Action.REMOTE_WINS)

    def test_concurrent_edits_use_mtime_and_preserve_the_loser(self) -> None:
        base = fp("a", 10)
        local_newer = plan_path(
            base, fp("b", 30), remote(RemoteKind.FILE, "c", 20), delete_enabled=True,
        )
        remote_newer = plan_path(
            base, fp("b", 20), remote(RemoteKind.FILE, "c", 30), delete_enabled=True,
        )
        equal_clock = plan_path(
            base, fp("b", 30), remote(RemoteKind.FILE, "c", 30), delete_enabled=True,
        )
        self.assertEqual(local_newer.action, Action.CONFLICT_LOCAL_WINS)
        self.assertEqual(remote_newer.action, Action.CONFLICT_REMOTE_WINS)
        self.assertEqual(equal_clock.action, Action.CONFLICT_REMOTE_WINS)

    def test_tombstone_prevents_stale_client_from_resurrecting_file(self) -> None:
        decision = plan_path(
            None, fp("a", 10), remote(RemoteKind.TOMBSTONE, mtime_seconds=20),
            delete_enabled=True,
        )
        self.assertEqual(decision.action, Action.CONFLICT_REMOTE_WINS)

    def test_newer_recreation_supersedes_tombstone(self) -> None:
        decision = plan_path(
            Fingerprint("", -1, -1), fp("a", 30),
            remote(RemoteKind.TOMBSTONE, mtime_seconds=20), delete_enabled=True,
        )
        self.assertEqual(decision.action, Action.UPLOAD)

    def test_remote_delete_and_concurrent_local_edit_keep_conflict_copy(self) -> None:
        decision = plan_path(
            fp("a", 10), fp("b", 20), remote(RemoteKind.ABSENT), delete_enabled=True,
        )
        self.assertEqual(decision.action, Action.CONFLICT_REMOTE_WINS)

    def test_first_client_seed_uploads_only_when_path_never_existed(self) -> None:
        self.assertEqual(
            plan_path(None, fp("a", 10), remote(RemoteKind.ABSENT), delete_enabled=True).action,
            Action.UPLOAD,
        )

    def test_disabled_deletions_restore_remote_file(self) -> None:
        decision = plan_path(
            fp("a", 10), None, remote(RemoteKind.FILE, "a", 10), delete_enabled=False,
        )
        self.assertEqual(decision.action, Action.REMOTE_WINS)


    def test_unmanaged_remote_directory_is_ignored_not_blocked(self) -> None:
        # A deleted folder leaves empty directories behind on the NAS (the file
        # model only removes regular files). The bare folder path must never halt
        # the whole sync with a "not a regular file" error.
        decision = plan_path(
            None, None, remote(RemoteKind.OTHER), delete_enabled=True,
        )
        self.assertEqual(decision.action, Action.NOTHING)

    def test_unmanaged_remote_directory_is_ignored_after_local_tombstone(self) -> None:
        decision = plan_path(
            Fingerprint("", -1, -1), None, remote(RemoteKind.OTHER), delete_enabled=True,
        )
        self.assertEqual(decision.action, Action.NOTHING)

    def test_remote_directory_still_blocks_delete_of_expected_file(self) -> None:
        # The client tracked a regular file here: never silently delete the
        # directory that replaced it.
        decision = plan_path(
            fp("a", 10), None, remote(RemoteKind.OTHER), delete_enabled=True,
        )
        self.assertEqual(decision.action, Action.BLOCK)

    def test_remote_directory_still_blocks_upload_over_it(self) -> None:
        # A local file appearing where the NAS has a directory must not be
        # pushed over it (that would delete the directory).
        decision = plan_path(
            None, fp("a", 10), remote(RemoteKind.OTHER), delete_enabled=True,
        )
        self.assertEqual(decision.action, Action.BLOCK)


if __name__ == "__main__":
    unittest.main()
