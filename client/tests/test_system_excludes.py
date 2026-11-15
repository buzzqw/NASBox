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


def test_published_client_bundle_never_enters_synchronization(tmp_path):
    """The update folder lives under remote_prefix: it is software, not content.

    Without this exclusion every client would pull the .tar.gz into its own
    NASBox folder on the release after the one that introduced it.
    """
    with ClientEnvironment():
        cfg = Config()
        cfg.set("local_path", str(tmp_path / "sync"))

        assert rsync_ops.path_is_excluded(cfg, ".nasbox-client-update/manifest.json")
        assert rsync_ops.path_is_excluded(cfg, "sub/.nasbox-client-update/client-1.26.9.tar.gz")
        args = rsync_ops._exclude_args(cfg)
        assert rsync_ops.CLIENT_UPDATE_DIRNAME in [args[i + 1] for i, val in enumerate(args) if val == "--exclude"]


def test_exclude_args_contains_default_excludes(tmp_path):
    with ClientEnvironment():
        cfg = Config()
        args = rsync_ops._exclude_args(cfg)

        for item in rsync_ops.DEFAULT_SYSTEM_EXCLUDES:
            assert "--exclude" in args
            # Every item in DEFAULT_SYSTEM_EXCLUDES must appear after an --exclude flag
            assert item in [args[i + 1] for i, val in enumerate(args) if val == "--exclude"]


def _legacy_path_is_excluded(cfg, relative_path: str) -> bool:
    """The pre-1.26.7 implementation, kept verbatim as the reference.

    ``path_is_excluded`` now compiles the globs into alternations instead of
    running one ``fnmatch`` per pattern per path component: ~60 million calls
    per full sweep (22s of CPU on a 100 GB folder, returning "nothing
    changed"). These tests pin that the rewrite is behaviour-identical.
    ``server_package_excluded_path``/``_server_state_exclude_arg`` are unset in
    this fixture, and the selective list is empty, so the pattern rules below
    are the only ones in play.
    """
    import fnmatch

    parts = [part for part in relative_path.replace("\\", "/").split("/") if part]
    if any(part in rsync_ops.EXCLUDE_DIRNAMES or part == rsync_ops.REPOSITORY_MARKER_NAME for part in parts):
        return True
    if any(
        fnmatch.fnmatch(relative_path, pat) or any(fnmatch.fnmatch(part, pat) for part in parts)
        for pat in rsync_ops.DEFAULT_SYSTEM_EXCLUDES
    ):
        return True
    for raw_pattern in cfg.exclude_patterns():
        pattern = raw_pattern.strip().replace("\\", "/")
        if not pattern:
            continue
        directory_pattern = pattern.rstrip("/")
        if pattern.endswith("/") and any(fnmatch.fnmatch(part, directory_pattern) for part in parts):
            return True
        if fnmatch.fnmatch(relative_path, pattern) or any(fnmatch.fnmatch(part, pattern) for part in parts):
            return True
    return False


_PATHS = [
    ".DS_Store",
    "docs/.DS_Store",
    "module.pyc",
    "pkg/__pycache__/module.cpython-314.pyc",
    "web/node_modules/pkg/index.js",
    "node_modules/index.js",
    "build/out.bin",
    "a/b/build/out.bin",
    "dist/app.tar.gz",
    "repo/.git/refs/heads/master",
    ".git/config",
    "repo/.hg/store/data/file.i",
    "SADE/SADE.aux",
    "SADE/SADE.pdf",
    "Report.docx",
    "work/~$Report.docx",
    ".~lock.document.odt#",
    "archive.crdownload",
    "file.tmp.12345",
    "sync-daemon/client/sync_client/engine.py",
    "sync-daemon/client-update/sync_client/version.py",
    ".nasbox-client-update/sync_client/version.py",
    "nasbox-client-update-1.26.6.tar.gz",
    "aX/b",
    "abc/def",
    "x.tar.gz",
    "dir/x.tar.gz",
    "notes with space.md",
    "cartella con spazi/file con spazi.txt",
    "ünïcode/ñame.txt",
    "deep/a/b/c/d/e/f/g.txt",
    "project/.pytest_cache/v/cache/lastfailed",
    "htmlcov/index.html",
    "pkg.egg-info/PKG-INFO",
    ".coverage",
    "target/debug/app",
    ".idea/workspace.xml",
    ".vscode/settings.json",
    ".next/static/chunk.js",
    ".cache/thing",
]

_PATTERN_SETS = [
    None,  # defaults shipped with the client
    ["build/", "dist/", "*.bak", "node_modules/", "tmp"],
    ["*.log", "vendor/*", "vendor", "Photos/2024", "*.ISO", "venv"],
]


@pytest.mark.parametrize("patterns", _PATTERN_SETS)
def test_compiled_excludes_match_the_legacy_semantics(patterns):
    with ClientEnvironment():
        cfg = Config()
        if patterns is not None:
            cfg.set("exclude_patterns", patterns, persist=False)
        for path in _PATHS:
            assert rsync_ops.path_is_excluded(cfg, path) == _legacy_path_is_excluded(cfg, path), path
