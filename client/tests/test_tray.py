from __future__ import annotations

import time
from unittest.mock import MagicMock
import pytest

from sync_client.config import Config
from sync_client.gui.tray import TrayIcon
from tests.support import ClientEnvironment


@pytest.fixture
def tray_icon(qapp):
    with ClientEnvironment():
        cfg = Config()
        cfg.set("notify_sync_completion", True)
        engine = MagicMock()
        logger = MagicMock()
        main_win = MagicMock()
        tray = TrayIcon(cfg, engine, logger, main_win)
        tray.showMessage = MagicMock()
        return tray


def test_tray_transfer_completion_duration(tray_icon):
    tray_icon.on_transfer_preparing("upload")
    assert "upload" in tray_icon._active_transfers
    assert "upload" in tray_icon._transfer_start_times

    # Record 2 items done
    tray_icon.on_upload_item_done("upload", "file1.txt")
    tray_icon.on_upload_item_done("upload", "file2.txt")

    # Simulate 5.5 seconds elapsed
    tray_icon._transfer_start_times["upload"] = time.monotonic() - 5.5
    tray_icon.on_transfer_finished("upload", True)

    assert "upload" not in tray_icon._active_transfers
    tray_icon.showMessage.assert_called_once()
    args, _kwargs = tray_icon.showMessage.call_args
    title, body = args[0], args[1]
    assert "Sincronizzazione completata" in title or "Sync completed" in title
    assert "2 operazioni" in body or "2 operations" in body
    assert "5s" in body or "6s" in body
