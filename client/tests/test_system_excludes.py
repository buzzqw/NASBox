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
        assert rsync_ops.path_is_excluded(cfg, "project/__pycache__/module.cpython-314.pyc")
        assert rsync_ops.path_is_excluded(cfg, "module.pyc")
        assert rsync_ops.path_is_excluded(cfg, "report_ai/v3/.venv/bin/python")
        assert rsync_ops.path_is_excluded(cfg, "dns-shield/venv/lib/package.py")
        assert rsync_ops.path_is_excluded(cfg, "web/node_modules/pkg/index.js")

        # Version-control metadata must never be synchronized: conflict copies
        # inside .git/refs leave the repository with malformed refs.
        assert rsync_ops.path_is_excluded(cfg, ".git/config")
        assert rsync_ops.path_is_excluded(cfg, "repo/.git/refs/heads/master")
        assert rsync_ops.path_is_excluded(cfg, "repo/.hg/store/data/file.i")

        # Tool caches and LaTeX build intermediates are regenerated, not authored.
        assert rsync_ops.path_is_excluded(cfg, "project/.pytest_cache/v/cache/lastfailed")
        assert rsync_ops.path_is_excluded(cfg, "SADE/SADE.aux")
        assert rsync_ops.path_is_excluded(cfg, "SADE/SADE.synctex.gz")
        assert rsync_ops.path_is_excluded(cfg, "SADE/SADE.toc")

        # Regular files must NOT be excluded
        assert not rsync_ops.path_is_excluded(cfg, "Report.docx")
        assert not rsync_ops.path_is_excluded(cfg, "document.odt")
        assert not rsync_ops.path_is_excluded(cfg, "photo.jpg")
        assert not rsync_ops.path_is_excluded(cfg, "code/script.py")
        assert not rsync_ops.path_is_excluded(cfg, "SADE/SADE.pdf")


def test_exclude_args_contains_default_excludes(tmp_path):
    with ClientEnvironment():
        cfg = Config()
        args = rsync_ops._exclude_args(cfg)

        for item in rsync_ops.DEFAULT_SYSTEM_EXCLUDES:
            assert "--exclude" in args
            # Every item in DEFAULT_SYSTEM_EXCLUDES must appear after an --exclude flag
            assert item in [args[i + 1] for i, val in enumerate(args) if val == "--exclude"]
