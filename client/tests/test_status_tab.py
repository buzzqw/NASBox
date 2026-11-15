from __future__ import annotations

from unittest.mock import MagicMock
import pytest

from sync_client.config import Config
from sync_client.gui.status_tab import StatusTab
from tests.support import ClientEnvironment


@pytest.fixture
def status_tab(tmp_path, qapp):
    with ClientEnvironment():
        cfg = Config()
        engine = MagicMock()
        scan_worker = MagicMock()
        tab = StatusTab(cfg, engine, scan_worker)
        yield tab


def test_status_tab_nas_overview(status_tab):
    # Initial status
    assert "Connessione NAS" in status_tab.nas_host_label.text()
    assert "Attività NAS" in status_tab.nas_activity_label.text()
    assert "Spazio disco NAS" in status_tab.nas_storage_label.text()

    status_tab.cfg.set_local_root("/home/user/NASBox")
    status_tab.cfg.set("nas_lan", "192.168.1.119")
    status_tab.cfg.set("nas_user", "andres")

    # on_status_changed with connected=True
    status_tab.on_status_changed({
        "host": "192.168.1.119",
        "connected": True,
        "configured": True,
        "paused": False,
    })
    assert "192.168.1.119" in status_tab.nas_host_label.text()
    assert "A riposo" in status_tab.nas_activity_label.text()

    # Remote lock held
    status_tab.on_status_changed({
        "host": "192.168.1.119",
        "connected": True,
        "configured": True,
        "server_lock_held": True,
        "server_lock_owner_host": "laptop-andres",
        "server_lock_age_seconds": 45,
    })
    assert "laptop-andres" in status_tab.nas_activity_label.text()

    # Remote lock released, transfer starts
    status_tab.on_status_changed({
        "host": "192.168.1.119",
        "connected": True,
        "configured": True,
        "server_lock_held": False,
    })
    status_tab.on_transfer_started("upload")
    status_tab.on_transfer_item_started("upload", "/home/user/NASBox/document.pdf", 1024)
    status_tab.on_transfer_speed("upload", 5_242_880, 50)
    assert not status_tab.sync_progress_bar.isHidden()
    assert status_tab.sync_progress_bar.value() == 50
    assert "document.pdf" in status_tab.sync_state_label.text()
    assert "5.0 MB/s" in status_tab.sync_state_label.text()
    assert "Upload verso NAS" in status_tab.nas_activity_label.text()

    # Transfer finished resets progress bar
    status_tab.on_transfer_finished("upload", True)
    assert status_tab.sync_progress_bar.isHidden()
    assert status_tab.sync_progress_bar.value() == 0

    # Diagnostics updates storage overview and storage progress bar
    status_tab._on_diagnostics_done(
        (True, {
            "SHARE_TOTAL_BYTES": str(100 * 1024 * 1024 * 1024),
            "SHARE_AVAILABLE_BYTES": str(30 * 1024 * 1024 * 1024),
            "TRASH_DISK_BYTES": str(2 * 1024 * 1024 * 1024),
            "TRASH_FILE_COUNT": "42",
        }, ""),
        None,
    )
    assert "30.0 GB liberi su 100.0 GB" in status_tab.nas_storage_label.text()
    assert "70% occupato" in status_tab.nas_storage_label.text()
    assert not status_tab.nas_storage_bar.isHidden()
    assert status_tab.nas_storage_bar.value() == 70
