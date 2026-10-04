from __future__ import annotations

import pytest

from sync_client import rsync_ops
from sync_client.config import Config
from tests.support import ClientEnvironment


def test_default_system_excludes_in_path_is_excluded(tmp_path):
    with ClientEnvironment():
        cfg = Config()
        cfg.set("local_path", str(tmp_path / "sync"))

        # System temp, lock, and metadata files should be excluded by default
        assert rsync_ops.path_is_excluded(cfg, ".DS_Store")
        assert rsync_ops.path_is_excluded(cfg, "subdir/.DS_Store")
        assert rsync_ops.path_is_excluded(cfg, "Thumbs.db")
        assert rsync_ops.path_is_excluded(cfg, "photos/Thumbs.db")
        assert rsync_ops.path_is_excluded(cfg, "desktop.ini")
        assert rsync_ops.path_is_excluded(cfg, "docs/desktop.ini")
        assert rsync_ops.path_is_excluded(cfg, "~$Report.docx")
        assert rsync_ops.path_is_excluded(cfg, "work/~$Report.docx")
        assert rsync_ops.path_is_excluded(cfg, ".~lock.document.odt#")
        assert rsync_ops.path_is_excluded(cfg, "download.part")
        assert rsync_ops.path_is_excluded(cfg, "archive.crdownload")
        assert rsync_ops.path_is_excluded(cfg, "file.tmp.12345")

        # Regular files must NOT be excluded
        assert not rsync_ops.path_is_excluded(cfg, "Report.docx")
        assert not rsync_ops.path_is_excluded(cfg, "document.odt")
        assert not rsync_ops.path_is_excluded(cfg, "photo.jpg")
        assert not rsync_ops.path_is_excluded(cfg, "code/script.py")


def test_exclude_args_contains_default_excludes(tmp_path):
    with ClientEnvironment():
        cfg = Config()
        args = rsync_ops._exclude_args(cfg)

        for item in rsync_ops.DEFAULT_SYSTEM_EXCLUDES:
            assert "--exclude" in args
            # Every item in DEFAULT_SYSTEM_EXCLUDES must appear after an --exclude flag
            assert item in [args[i + 1] for i, val in enumerate(args) if val == "--exclude"]
