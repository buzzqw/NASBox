from __future__ import annotations

import pytest

from sync_client.gui.transfers_tab import TransfersTab
from sync_client.rsync_ops import TransferItem


@pytest.fixture
def transfers_tab(qapp):
    return TransfersTab()


def _queue(count: int) -> list[TransferItem]:
    return [TransferItem("upload", f"cartella/file{index}.bin", 1024) for index in range(count)]


def test_burst_of_started_files_rebuilds_the_queue_once(transfers_tab, monkeypatch):
    """Regressione: ogni on_item_started() ricostruiva il set di tutti i percorsi
    in coda e riordinava l'intera lista. Su una coda da decine di migliaia di file
    e un trasferimento da centinaia di file al secondo questo teneva il thread GUI
    al 100% di un core e la finestra non rispondeva piu'."""
    transfers_tab._all_items = _queue(5000)
    rebuilds: list[bool] = []
    monkeypatch.setattr(transfers_tab, "_apply_filter", lambda: rebuilds.append(True))

    for index in range(200):
        transfers_tab.on_item_started("upload", f"nuovo{index}.bin", 1024)

    assert rebuilds == []  # differito: nessun render per singolo file
    transfers_tab._flush()
    assert rebuilds == [True]  # un solo render per l'intero burst


def test_known_key_is_not_duplicated_in_the_queue(transfers_tab):
    transfers_tab._all_items = _queue(3)

    transfers_tab.on_item_started("upload", "cartella/file1.bin", 1024)
    assert len(transfers_tab._all_items) == 3

    transfers_tab.on_item_started("upload", "nuovo.bin", 1024)
    paths = [item.path for item in transfers_tab._all_items]
    assert paths.count("nuovo.bin") == 1
    assert ("upload", "nuovo.bin") in transfers_tab._known_keys()


def test_queue_replacement_invalidates_the_key_cache(transfers_tab):
    transfers_tab.on_queue_updated(_queue(2))
    assert ("upload", "cartella/file0.bin") in transfers_tab._known_keys()

    transfers_tab.on_queue_updated([TransferItem("upload", "altro.bin", 10)])
    assert ("upload", "altro.bin") in transfers_tab._known_keys()
    assert ("upload", "cartella/file0.bin") not in transfers_tab._known_keys()
