from __future__ import annotations

import os
import tempfile
import threading
import unittest
from pathlib import Path
from unittest.mock import Mock, patch

os.environ.setdefault("QT_QPA_PLATFORM", "offscreen")

from PyQt6.QtWidgets import QApplication

from sync_client.i18n import set_language, t
from sync_client.mirrors import MirrorWatcher, validate_mirror


class _Config:
    def __init__(self, root: str, mirrors: list[dict]) -> None:
        self.root = root
        self.entries = mirrors
        self.updates: list[tuple[str, dict]] = []

    def local_root(self) -> str:
        return self.root

    def mirrors(self) -> list[dict]:
        return self.entries

    def mirror_by_source(self, source: str) -> dict | None:
        return next((entry for entry in self.entries if entry.get("source") == source), None)

    def update_mirror(self, source: str, **updates: object) -> None:
        self.updates.append((source, updates))

    def exclude_patterns(self) -> list[str]:
        return []


class MirrorSafetyTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls) -> None:
        cls.app = QApplication.instance() or QApplication([])

    def setUp(self) -> None:
        set_language("en")

    def tearDown(self) -> None:
        set_language("it")

    def test_validate_mirror_rejects_dot_destination(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            base = Path(directory)
            root = base / "NASBox"
            source = base / "source"
            root.mkdir()
            source.mkdir()
            cfg = _Config(str(root), [])

            for dest in (".", "./", "nested/./child"):
                ok, error = validate_mirror(cfg, str(source), dest)
                self.assertFalse(ok)
                self.assertEqual(error, t("mirrors.err_dest_invalid"))

    def test_validate_mirror_rejects_source_symlink_inside_root(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            base = Path(directory)
            root = base / "NASBox"
            inside = root / "inside"
            source_link = base / "source-link"
            inside.mkdir(parents=True)
            source_link.symlink_to(inside, target_is_directory=True)
            cfg = _Config(str(root), [])

            ok, error = validate_mirror(cfg, str(source_link), "mirror")
            self.assertFalse(ok)
            self.assertEqual(error, t("mirrors.err_source_inside_nasbox"))

    def test_validate_mirror_rejects_destination_symlink_escaping_root(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            base = Path(directory)
            root = base / "NASBox"
            source = base / "source"
            outside = base / "outside"
            root.mkdir()
            source.mkdir()
            outside.mkdir()
            (root / "escape").symlink_to(outside, target_is_directory=True)
            cfg = _Config(str(root), [])

            ok, error = validate_mirror(cfg, str(source), "escape")
            self.assertFalse(ok)
            self.assertEqual(error, t("mirrors.err_dest_invalid"))

    def test_persisted_invalid_mirror_never_starts_rsync(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            base = Path(directory)
            root = base / "NASBox"
            source = base / "source"
            root.mkdir()
            source.mkdir()
            cfg = _Config(str(root), [{"source": str(source), "dest": ".", "enabled": True}])
            watcher = MirrorWatcher(cfg, Mock(), str(source), ".", str(root), threading.Lock())

            with patch("sync_client.mirrors.subprocess.Popen") as popen:
                watcher._sync()

            popen.assert_not_called()
            self.assertEqual(cfg.updates, [(str(source), {"error": t("mirrors.err_dest_invalid")})])


if __name__ == "__main__":
    unittest.main()
