from __future__ import annotations

import tempfile
import unittest
from pathlib import Path

from sync_client import rsync_ops
from sync_client.config import Config
from sync_client.selective import list_directories, minimize_excluded

from tests.support import ClientEnvironment


class SelectiveHelpersTests(unittest.TestCase):
    def test_list_directories_walks_recursively_and_skips_ignored(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / "RPG").mkdir()
            (root / "RPG" / "sub").mkdir()
            (root / "lavoro").mkdir()
            (root / ".sync-trash").mkdir()
            (root / "@eaDir").mkdir()
            (root / "file.txt").write_text("x")

            paths, truncated = list_directories(str(root), ignore_names={".sync-trash", "@eaDir"})

            self.assertFalse(truncated)
            self.assertEqual(paths, ["RPG", "RPG/sub", "lavoro"])

    def test_list_directories_honours_the_limit(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            for index in range(10):
                (root / f"d{index}").mkdir()
            paths, truncated = list_directories(str(root), ignore_names=set(), limit=4)
            self.assertTrue(truncated)
            self.assertEqual(len(paths), 4)

    def test_minimize_excluded_drops_children_of_excluded_folders(self) -> None:
        self.assertEqual(
            minimize_excluded(["a/b/c", "a", "d"]),
            ["a", "d"],
        )
        # Same-named sibling at another depth is NOT covered by a prefix match.
        self.assertEqual(minimize_excluded(["ab", "a"]), ["a", "ab"])

    def test_missing_root_is_empty(self) -> None:
        self.assertEqual(list_directories("/no/such/root/here", ignore_names=set()), ([], False))


class SelectiveConfigTests(unittest.TestCase):
    def setUp(self) -> None:
        self.environment = ClientEnvironment()
        self.environment.__enter__()
        self.cfg = Config()

    def tearDown(self) -> None:
        self.environment.__exit__(None, None, None)

    def test_set_excludes_is_minimized(self) -> None:
        self.cfg.set_selective_sync_excludes(["lavoro/archivio", "lavoro", "privato"])
        self.assertEqual(self.cfg.selective_sync_excludes(), ["lavoro", "privato"])

    def test_path_is_excluded_matches_the_folder_and_its_children_only(self) -> None:
        self.cfg.set("selective_sync_excludes", ["RPG"], persist=False)
        self.assertTrue(rsync_ops.path_is_excluded(self.cfg, "RPG"))
        self.assertTrue(rsync_ops.path_is_excluded(self.cfg, "RPG/mappa.pdf"))
        self.assertFalse(rsync_ops.path_is_excluded(self.cfg, "RPG2"))
        self.assertFalse(rsync_ops.path_is_excluded(self.cfg, "docs/RPG"))

    def test_nested_exclude_is_anchored(self) -> None:
        self.cfg.set("selective_sync_excludes", ["lavoro/archivio"], persist=False)
        self.assertTrue(rsync_ops.path_is_excluded(self.cfg, "lavoro/archivio/2024/x.zip"))
        self.assertFalse(rsync_ops.path_is_excluded(self.cfg, "archivio"))
        self.assertFalse(rsync_ops.path_is_excluded(self.cfg, "personale/lavoro/archivio"))

    def test_rsync_args_use_root_anchored_excludes(self) -> None:
        self.cfg.set("selective_sync_excludes", ["RPG", "lavoro/archivio"], persist=False)
        args = rsync_ops._exclude_args(self.cfg)
        self.assertIn("/RPG", args)
        self.assertIn("/lavoro/archivio", args)


if __name__ == "__main__":
    unittest.main()
