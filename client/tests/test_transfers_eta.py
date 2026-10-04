from __future__ import annotations

import pytest

from sync_client.gui.transfers_tab import TransfersTab
from sync_client.rsync_ops import TransferItem


@pytest.fixture
def transfers_tab(qapp):
    return TransfersTab()


def test_transfers_speed_and_eta(transfers_tab):
    item = TransferItem(
        direction="upload",
        path="video.mp4",
        size=10 * 1024 * 1024,  # 10 MB
    )
    transfers_tab._all_items = [item]

    # Test speed update with ETA calculation
    transfers_tab.on_speed_update("upload", 1024 * 1024, 50)  # 1 MB/s, 50%
    transfers_tab._flush()

    text = transfers_tab.upload_speed_label.text()
    assert "1.0 MB/s" in text or "1 MB/s" in text
    assert "(50%)" in text
    assert "~10s" in text

    # Test transfer finish resets speed label
    transfers_tab.on_transfer_finished("upload", True)
    transfers_tab._flush()
    assert transfers_tab.upload_speed_label.text().endswith("-")
