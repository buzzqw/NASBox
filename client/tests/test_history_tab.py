from __future__ import annotations

from pathlib import Path
from unittest.mock import MagicMock
import pytest

from sync_client.config import Config
from sync_client.gui.history_tab import HistoryTab
from sync_client.trash import TrashVersion
from tests.support import ClientEnvironment


@pytest.fixture
def history_tab(tmp_path, qapp):
    with ClientEnvironment():
        cfg = Config()
        engine = MagicMock()
        engine.connection = None
        logger = MagicMock()
        tab = HistoryTab(cfg, engine, logger)
        return tab


def test_history_tab_totals_label(history_tab, tmp_path):
    v1 = TrashVersion(
        relative_path="docs/report.pdf",
        timestamp="2026-10-01--10-00-00",
        trash_path=tmp_path / "report.pdf",
        age_days=1.5,
        size=1024 * 1024,  # 1 MB
    )
    v2 = TrashVersion(
        relative_path="photos/vacation.jpg",
        timestamp="2026-10-02--12-00-00",
        trash_path=tmp_path / "vacation.jpg",
        age_days=0.5,
        size=2 * 1024 * 1024,  # 2 MB
    )
    history_tab._versions = [v1, v2]
    history_tab._apply_filter()

    # Total summary without filter
    text = history_tab.totals_label.text()
    assert "2 versioni" in text or "2 versions" in text
    assert "2 cartelle" in text or "2 folders" in text
    assert "3.0 MB" in text or "3 MB" in text

    # Filter with search needle
    history_tab.search_edit.setText("report")
    filtered_text = history_tab.totals_label.text()
    assert "1 versioni" in filtered_text or "1 versions" in filtered_text
    assert "1.0 MB" in filtered_text or "1 MB" in filtered_text
    assert "2 totali" in filtered_text or "2 total" in filtered_text
