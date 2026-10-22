from __future__ import annotations

import subprocess
import unittest
from pathlib import Path
from unittest.mock import patch

from sync_client import conflicts, mirrors, rsync_ops, trash
from sync_client.config import Config

from tests.support import ClientEnvironment


def conflict_name(stem: str, suffix: str = "", origin: str = "dev1") -> str:
    return f"{stem}{conflicts.CONFLICT_TAG}{origin} deadbeef){suffix}"


class ScanConflictGroupsTests(unittest.TestCase):
    def setUp(self) -> None:
        self.environment = ClientEnvironment()
        self.environment.__enter__()
        self.root = Path(self.environment._temporary.name) / "root"
        self.root.mkdir()

    def tearDown(self) -> None:
        self.environment.__exit__(None, None, None)

    def test_groups_localised_conflict_copies_with_their_original(self) -> None:
        (self.root / "doc.txt").write_text("original")
        (self.root / conflict_name("doc", ".txt")).write_text("nas")
        (self.root / conflict_name("doc", ".txt", origin="dev2")).write_text("nas2")

        groups = conflicts.scan_conflict_groups(str(self.root))

        self.assertEqual(len(groups), 1)
        self.assertEqual(groups[0].original, self.root / "doc.txt")
        self.assertEqual(len(groups[0].candidates), 2)

    def test_ignores_internal_and_legacy_names(self) -> None:
        (self.root / "doc.txt").write_text("original")
        (self.root / "legacy [conflicted].txt").write_text("other tool")
        partial = self.root / ".sync-partial"
        partial.mkdir()
        (partial / conflict_name("doc", ".txt")).write_text("half")

        self.assertEqual(conflicts.scan_conflict_groups(str(self.root)), [])


class DiscardConflictCopiesTests(unittest.TestCase):
    def setUp(self) -> None:
        self.environment = ClientEnvironment()
        self.environment.__enter__()
        self.root = Path(self.environment._temporary.name) / "root"
        self.root.mkdir()
        (self.root / "doc.txt").write_text("original")
        (self.root / conflict_name("doc", ".txt")).write_text("nas")
        self.group = conflicts.scan_conflict_groups(str(self.root))[0]

    def tearDown(self) -> None:
        self.environment.__exit__(None, None, None)

    def test_discard_keeps_original_and_moves_copies_to_history(self) -> None:
        ok, detail = conflicts.discard_conflict_copies(self.group, str(self.root))

        self.assertTrue(ok, detail)
        self.assertEqual((self.root / "doc.txt").read_text(), "original")
        self.assertFalse(list(self.root.glob("*conflitto da*")))
        versions = trash.list_local_versions()
        self.assertEqual(len(versions), 1)
        self.assertTrue(versions[0].relative_path.endswith(conflict_name("doc", ".txt")))

    def test_discard_works_without_an_original(self) -> None:
        (self.root / "doc.txt").unlink()

        ok, detail = conflicts.discard_conflict_copies(self.group, str(self.root))

        self.assertTrue(ok, detail)
        self.assertFalse(list(self.root.glob("*conflitto da*")))


class MirrorCommandTests(unittest.TestCase):
    def test_mirror_never_deletes_conflict_copies(self) -> None:
        cfg = Config()
        cmd = mirrors._mirror_rsync_command(cfg, "/source", "/destination")

        index = cmd.index(conflicts.CONFLICT_GLOB)
        self.assertEqual(cmd[index - 1], "--exclude")
        self.assertEqual(cmd[-2:], ["/source/", "/destination/"])


class CopyRemoteFileTests(unittest.TestCase):
    """LOCAL_WINS conflicts must leave a local copy the Conflicts tab can see."""

    def setUp(self) -> None:
        self.environment = ClientEnvironment()
        self.environment.__enter__()
        self.root = Path(self.environment._temporary.name) / "root"
        self.root.mkdir()
        self.cfg = Config()
        self.cfg.set("local_root", str(self.root), persist=False)
        self.cfg.set("nas_user", "nas", persist=False)
        self.cfg.set("remote_prefix", "/volume1/NASBox", persist=False)
        self.conn = rsync_ops.NasConnection("nas")
        self.relative = conflict_name("doc", ".txt")

    def tearDown(self) -> None:
        self.environment.__exit__(None, None, None)

    def test_conflict_copy_is_also_downloaded_locally(self) -> None:
        def fake_run(cmd, **_kwargs):
            if cmd[0] == "rsync":
                temporary = Path(cmd[-1])
                temporary.parent.mkdir(parents=True, exist_ok=True)
                temporary.write_text("nas version")
            return subprocess.CompletedProcess(cmd, 0, stdout=b"", stderr=b"")

        with patch("sync_client.rsync_ops.subprocess.run", side_effect=fake_run):
            ok = rsync_ops.copy_remote_file(self.cfg, self.conn, "doc.txt", self.relative)

        self.assertTrue(ok)
        self.assertEqual((self.root / self.relative).read_text(), "nas version")
        self.assertFalse(list((self.root / ".sync-partial").rglob("*")) if (self.root / ".sync-partial").exists() else [])

    def test_failed_download_is_reported(self) -> None:
        def fake_run(cmd, **_kwargs):
            return subprocess.CompletedProcess(cmd, 1, stdout=b"", stderr=b"boom")

        with patch("sync_client.rsync_ops.subprocess.run", side_effect=fake_run):
            ok = rsync_ops.copy_remote_file(self.cfg, self.conn, "doc.txt", self.relative)

        self.assertFalse(ok)
        self.assertFalse((self.root / self.relative).exists())


if __name__ == "__main__":
    unittest.main()
