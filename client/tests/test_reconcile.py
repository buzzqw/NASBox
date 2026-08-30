from __future__ import annotations

import unittest

from sync_client.reconcile import Action, RemoteKind, RemoteState, plan_path
from sync_client.sync_state import CausalVersion, Fingerprint


def fp(digest: str, mtime: int = 10, causal: CausalVersion | None = None) -> Fingerprint:
    return Fingerprint(digest, len(digest), mtime, causal)


def remote(
    kind: RemoteKind, digest: str = "", mtime: int = 10,
    causal: CausalVersion | None = None,
) -> RemoteState:
    return RemoteState(kind, digest, len(digest), mtime, causal)


def clock(*entries: tuple[str, int]) -> CausalVersion:
    return CausalVersion(entries)


class ReconcileTests(unittest.TestCase):
    def test_unknown_history_adopts_equal_remote_file(self) -> None:
        decision = plan_path(None, fp("same"), remote(RemoteKind.FILE, "same"), delete_enabled=True)
        self.assertEqual(decision.action, Action.ADOPT)

    def test_unknown_history_uploads_local_file_when_remote_is_absent(self) -> None:
        decision = plan_path(None, fp("local"), remote(RemoteKind.ABSENT), delete_enabled=True)
        self.assertEqual(decision.action, Action.UPLOAD)

    def test_unknown_history_preserves_concurrent_versions_by_timestamp(self) -> None:
        local_wins = plan_path(
            None, fp("local", 20_000_000_000), remote(RemoteKind.FILE, "remote", 10_000_000_000),
            delete_enabled=True,
        )
        equal_time = plan_path(
            None, fp("local", 10_000_000_000), remote(RemoteKind.FILE, "remote", 10_000_000_000),
            delete_enabled=True,
        )
        self.assertEqual(local_wins.action, Action.CONFLICT_LOCAL_WINS)
        self.assertEqual(equal_time.action, Action.CONFLICT_REMOTE_WINS)

    def test_baseline_local_change_uploads_when_remote_is_unchanged(self) -> None:
        decision = plan_path(
            fp("base"), fp("local"), remote(RemoteKind.FILE, "base"), delete_enabled=True,
        )
        self.assertEqual(decision.action, Action.UPLOAD)

    def test_baseline_remote_change_wins_when_local_is_unchanged(self) -> None:
        decision = plan_path(
            fp("base"), fp("base"), remote(RemoteKind.FILE, "remote"), delete_enabled=True,
        )
        self.assertEqual(decision.action, Action.REMOTE_WINS)

    def test_concurrent_change_uses_remote_on_equal_second(self) -> None:
        decision = plan_path(
            fp("base", 10_000_000_000), fp("local", 20_000_000_000),
            remote(RemoteKind.FILE, "remote", 20_000_000_000), delete_enabled=True,
        )
        self.assertEqual(decision.action, Action.CONFLICT_REMOTE_WINS)

    def test_local_delete_only_deletes_matching_remote_baseline(self) -> None:
        decision = plan_path(fp("base"), None, remote(RemoteKind.FILE, "base"), delete_enabled=True)
        stale = plan_path(fp("base"), None, remote(RemoteKind.FILE, "new"), delete_enabled=True)
        disabled = plan_path(fp("base"), None, remote(RemoteKind.FILE, "base"), delete_enabled=False)
        self.assertEqual(decision.action, Action.DELETE_REMOTE)
        self.assertEqual(stale.action, Action.REMOTE_WINS)
        self.assertEqual(disabled.action, Action.REMOTE_WINS)

    def test_remote_deletion_does_not_erase_a_concurrent_local_change(self) -> None:
        decision = plan_path(fp("base"), fp("local"), remote(RemoteKind.TOMBSTONE), delete_enabled=True)
        self.assertEqual(decision.action, Action.CONFLICT_REMOTE_WINS)

    def test_known_tombstone_allows_local_recreation_to_upload(self) -> None:
        decision = plan_path(
            fp("", 0), fp("recreated"), remote(RemoteKind.TOMBSTONE), delete_enabled=True,
        )
        self.assertEqual(decision.action, Action.UPLOAD)

    def test_unmanaged_remote_directory_is_ignored_but_expected_directory_blocks(self) -> None:
        unmanaged = plan_path(None, None, remote(RemoteKind.OTHER), delete_enabled=True)
        expected = plan_path(fp("base"), None, remote(RemoteKind.OTHER), delete_enabled=True)
        self.assertEqual(unmanaged.action, Action.NOTHING)
        self.assertEqual(expected.action, Action.BLOCK)

    def test_identical_states_are_adopted(self) -> None:
        decision = plan_path(fp("base"), fp("base"), remote(RemoteKind.FILE, "base"), delete_enabled=True)
        self.assertEqual(decision.action, Action.ADOPT)

    def test_causal_local_version_wins_before_timestamp_fallback(self) -> None:
        decision = plan_path(
            fp("base", causal=clock(("device-a", 1))),
            fp("local", mtime=10, causal=clock(("device-a", 1), ("device-b", 1))),
            remote(RemoteKind.FILE, "remote", mtime=20, causal=clock(("device-a", 1))),
            delete_enabled=True,
        )
        self.assertEqual(decision.action, Action.CONFLICT_LOCAL_WINS)
        self.assertIn("causalmente", decision.detail)

    def test_causal_remote_tombstone_wins_before_timestamp_fallback(self) -> None:
        decision = plan_path(
            fp("base", causal=clock(("device-a", 1))),
            fp("local", mtime=20, causal=clock(("device-a", 2))),
            remote(RemoteKind.TOMBSTONE, mtime=10, causal=clock(("device-a", 2), ("device-b", 1))),
            delete_enabled=True,
        )
        self.assertEqual(decision.action, Action.CONFLICT_REMOTE_WINS)
        self.assertIn("causalmente", decision.detail)

    def test_concurrent_vectors_still_use_timestamp_fallback(self) -> None:
        decision = plan_path(
            fp("base", causal=clock(("device-a", 1))),
            fp("local", mtime=20_000_000_000, causal=clock(("device-a", 2))),
            remote(RemoteKind.FILE, "remote", mtime=10_000_000_000, causal=clock(("device-a", 1), ("device-b", 1))),
            delete_enabled=True,
        )
        self.assertEqual(decision.action, Action.CONFLICT_LOCAL_WINS)


if __name__ == "__main__":
    unittest.main()
