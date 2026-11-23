"""Storage insights: read-only collection, cooldown and empty states."""
from __future__ import annotations

import hashlib
import os
import time
from unittest.mock import MagicMock, patch

import pytest
from PyQt6.QtWidgets import QGroupBox, QMainWindow

from sync_client import metrics_worker
from sync_client.config import Config
from sync_client.gui import metrics_tab as metrics_tab_module
from sync_client.gui.metrics_tab import MetricsTab, STORAGE_COOLDOWN_SECONDS
from sync_client.i18n import t
from sync_client.metrics_worker import DuplicateGroup, StorageInsights, TrashInsight, collect_storage_insights
from tests.support import ClientEnvironment


def test_duplicate_render_is_one_path_per_line_without_repeated_title(qtbot):
    """The duplicate list must be readable and the section title unique."""
    with ClientEnvironment():
        cfg = Config()
        tab = MetricsTab(cfg, MagicMock())
        qtbot.addWidget(tab)
        assert tab.storage_duplicates_section.title() == t("storage.duplicates_title")
        assert tab.findChild(QGroupBox, "storageDuplicatesBox").title() == t(
            "storage.duplicate_groups_title"
        )
        group = DuplicateGroup(2048, "a" * 64, ("doc/a.txt", "copy/b.txt", "old/c.txt"))
        result = StorageInsights(
            root="/tmp/nasbox", collected_at=time.time(), scanned_files=10,
            scan_errors=0, candidates_limit_reached=False, top_folders=(),
            duplicates=(group,), duplicate_candidates=10, duplicate_wasted_bytes=4096,
            trash=TrashInsight(0, 0, 0, 0, 30),
        )
        tab._render_storage(result)
        text = tab.storage_duplicates_label.text()
        lines = [line.strip() for line in text.splitlines() if line.strip()]
        assert "doc/a.txt" in lines
        assert "copy/b.txt" in lines
        assert "old/c.txt" in lines
        # each path on its own line; never a comma-joined blob
        assert not any("," in line for line in lines if ".txt" in line)
        assert t("storage.duplicate_groups_title") not in text


class MockWindow(QMainWindow):
    """Deterministic visibility, independent of the offscreen platform."""

    def __init__(self) -> None:
        super().__init__()
        self._minimized = False
        self._visible = True

    def isMinimized(self) -> bool:
        return self._minimized

    def isVisible(self) -> bool:
        return self._visible


@pytest.fixture
def storage_env(tmp_path):
    """Isolated config/state dirs plus an empty local NASBox root."""
    with ClientEnvironment() as env:
        cfg = Config()
        root = tmp_path / "nasbox"
        root.mkdir()
        cfg.set_local_root(str(root))
        cfg.set("retention_days_local", 30, persist=False)
        yield env, cfg, root


def _make_tab(cfg, parent=None) -> MetricsTab:
    return MetricsTab(cfg, MagicMock(), parent=parent)


# --- pure collector --------------------------------------------------------


def test_collect_storage_insights_on_temp_tree_with_duplicates(storage_env):
    _env, cfg, root = storage_env
    (root / "docs").mkdir()
    (root / "media").mkdir()
    content_a = b"unique-A" * 10   # 80 bytes
    content_b = b"unique-B" * 20   # 160 bytes
    (root / "docs" / "a.txt").write_bytes(content_a)
    (root / "media" / "a_copy.txt").write_bytes(content_a)
    (root / "docs" / "b.txt").write_bytes(content_b)
    (root / "media" / "b_copy.txt").write_bytes(content_b)
    (root / "single.txt").write_bytes(b"only one")

    # Local .sync-trash lives under XDG_STATE_HOME/trash (see paths.py).
    trash_dir = _env.state_home / "sync-daemon" / "trash" / "old"
    trash_dir.mkdir(parents=True, exist_ok=True)
    version = trash_dir / "old.txt-2020-01-01--00-00-00-000000Z"
    version.write_bytes(b"X" * 500)

    progress: list[int] = []
    result = collect_storage_insights(cfg, progress=progress.append)

    assert result.root == str(root)
    assert result.scanned_files == 5
    assert result.scan_errors == 0
    assert result.candidates_limit_reached is False
    assert progress and progress[-1] == 5

    folders = {folder.path: folder for folder in result.top_folders}
    assert {"docs", "media", "."} <= set(folders)
    assert folders["docs"].size_bytes == 240
    assert folders["docs"].file_count == 2
    assert folders["media"].size_bytes == 240
    assert folders["."].size_bytes == 488

    assert result.duplicate_candidates == 5
    assert len(result.duplicates) == 2
    digests = {group.sha256 for group in result.duplicates}
    assert hashlib.sha256(content_a).hexdigest() in digests
    assert hashlib.sha256(content_b).hexdigest() in digests
    assert all(len(group.paths) == 2 for group in result.duplicates)
    assert result.duplicate_wasted_bytes == 240

    assert result.trash.version_count == 1
    assert result.trash.total_bytes == 500
    assert result.trash.retention_days == 30
    assert result.trash.expired_count == 1
    assert result.trash.expired_bytes == 500


def test_collect_storage_insights_counts_permission_errors(storage_env, monkeypatch):
    _env, cfg, root = storage_env
    (root / "blocked").mkdir()
    (root / "ok.txt").write_bytes(b"fine")

    real_scandir = os.scandir

    def fake_scandir(path):
        if os.path.basename(os.fspath(path)) == "blocked":
            raise PermissionError("test")
        return real_scandir(path)

    monkeypatch.setattr(metrics_worker.os, "scandir", fake_scandir)
    result = collect_storage_insights(cfg)

    assert result.scan_errors == 1
    assert result.scanned_files == 1
    assert result.top_folders


def test_duplicate_candidate_sample_is_bounded(storage_env):
    _env, cfg, root = storage_env
    # Distinct sizes avoid hashing 2000 files while still exercising the cap.
    for index in range(metrics_worker.STORAGE_MAX_DUPLICATE_CANDIDATES + 1):
        (root / f"file-{index:05d}.bin").write_bytes(b"x" * (index + 1))

    result = collect_storage_insights(cfg)

    assert result.duplicate_candidates == metrics_worker.STORAGE_MAX_DUPLICATE_CANDIDATES
    assert result.candidates_limit_reached is True
    assert result.duplicates == ()


# --- cooldown / visibility (GUI wiring) ------------------------------------


def test_storage_auto_analysis_respects_cooldown(storage_env, qapp):
    _env, cfg, _root = storage_env
    window = MockWindow()
    tab = _make_tab(cfg, parent=window)
    tab._active = True

    with patch.object(metrics_tab_module, "run_in_background") as run_bg, \
            patch.object(metrics_tab_module, "collect_storage_insights") as collect:
        assert tab._storage_cooldown_ok() is True

        tab._storage_last_finished = time.monotonic()
        assert tab._storage_cooldown_ok() is False
        assert tab._maybe_auto_analyze() is False
        run_bg.assert_not_called()
        collect.assert_not_called()

        tab._storage_last_finished = time.monotonic() - (STORAGE_COOLDOWN_SECONDS + 1)
        assert tab._maybe_auto_analyze() is True
        run_bg.assert_called_once()
        collect.assert_not_called()  # run_in_background is mocked; fn is never invoked

        tab._storage_busy = True
        assert tab._maybe_auto_analyze() is False
        run_bg.assert_called_once()

        # The user's "Analizza ora" button explicitly bypasses the cooldown.
        tab._storage_busy = False
        tab._storage_last_finished = time.monotonic()
        assert tab._analyze_storage(force=True) is True
        assert run_bg.call_count == 2


def test_set_active_does_not_scan_when_inactive_or_hidden(storage_env, qapp):
    _env, cfg, _root = storage_env
    window = MockWindow()
    tab = _make_tab(cfg, parent=window)

    with patch.object(tab, "_maybe_auto_analyze") as auto:
        tab.set_active(False)
        auto.assert_not_called()
        window._visible = False
        tab.set_active(True)
        auto.assert_not_called()
        window._visible = True
        tab.set_active(True)
        auto.assert_called_once()


def test_completion_starts_the_cooldown(storage_env, qapp):
    _env, cfg, _root = storage_env
    tab = _make_tab(cfg)

    assert tab._storage_cooldown_ok() is True
    tab._on_storage_done(StorageInsights(
        root="", collected_at=time.time(), scanned_files=0, scan_errors=0,
        candidates_limit_reached=False, top_folders=(), duplicates=(),
        duplicate_candidates=0, duplicate_wasted_bytes=0,
        trash=metrics_worker.TrashInsight(0, 0, 0, 0, 0),
    ), None)
    assert tab._storage_last_finished > 0
    assert tab._storage_cooldown_ok() is False
    assert tab.storage_analyze_btn.isEnabled() is True
    assert tab.storage_progress.isVisible() is False


# --- empty / error states --------------------------------------------------


def test_storage_empty_state_before_and_after_empty_scan(storage_env, qapp):
    _env, cfg, _root = storage_env
    tab = _make_tab(cfg)

    assert tab.storage_status_label.text() == t("storage.status_idle")
    assert tab.storage_last_label.text() == t("storage.never_updated")
    assert tab.storage_folders_label.text() == t("storage.empty_folders")
    assert tab.storage_duplicates_label.text() == t("storage.empty_duplicates")
    assert tab.storage_trash_label.text() == t("storage.empty_trash")

    empty = collect_storage_insights(cfg)
    tab._on_storage_done(empty, None)

    assert tab.storage_folders_label.text() == t("storage.empty_folders")
    assert tab.storage_duplicates_label.text() == t("storage.empty_duplicates")
    assert tab.storage_trash_label.text() == t("storage.empty_trash")
    assert tab.storage_status_label.text() == t("storage.status_done", files=0)
    assert tab.storage_last_label.text() != t("storage.never_updated")


def test_storage_error_states(storage_env, qapp):
    _env, cfg, _root = storage_env
    tab = _make_tab(cfg)

    tab._render_storage_error("boom")
    assert tab.storage_status_label.text() == t("storage.error", detail="boom")

    cfg.set_local_root("")
    assert tab._analyze_storage(force=True) is False
    assert tab.storage_status_label.text() == t("storage.error", detail=t("storage.missing_root"))
    assert tab._storage_busy is False
