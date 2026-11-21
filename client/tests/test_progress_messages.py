"""Regressioni sui messaggi di avanzamento.

Il problema riportato: dopo un rinvio o un errore la finestra sembrava
bloccarsi — nessun file che scorre, nessuna indicazione di cosa sarebbe
successo dopo. Questi test fissano le tre promesse della UI:

* un rinvio per lock occupato mostra un conto alla rovescia che scorre;
* la coda resta visibile e viene detto che nessun file è perso;
* il numero di file (e l'ultimo completato) è sempre leggibile;
* un errore vero è distinto da una normale attesa tra PC.
"""
from __future__ import annotations

import time
from unittest.mock import MagicMock

import pytest

from sync_client.config import Config
from sync_client.gui.status_tab import StatusTab
from sync_client.gui.tray import TrayIcon
from sync_client.gui.transfers_tab import TransfersTab
from sync_client.rsync_ops import TransferItem
from sync_client.pull_worker import PullWorker
from sync_client.push_worker import PushWorker
from tests.support import ClientEnvironment


def _queue(count: int, direction: str = "upload") -> list[TransferItem]:
    return [
        TransferItem(direction, f"cartella/file{index}.bin", 1024)
        for index in range(count)
    ]


@pytest.fixture
def status_tab(tmp_path, qapp):
    with ClientEnvironment():
        cfg = Config()
        cfg.set_local_root(str(tmp_path / "NASBox"))
        cfg.set("nas_lan", "192.168.1.119")
        cfg.set("nas_user", "andres")
        tab = StatusTab(cfg, MagicMock(), MagicMock())
        tab.on_status_changed({
            "host": "192.168.1.119",
            "connected": True,
            "configured": True,
            "paused": False,
        })
        yield tab


@pytest.fixture
def transfers_tab(qapp):
    return TransfersTab()


# --- attesa del lock NAS: conto alla rovescia, non un vicolo cieco ---


def test_status_tab_retry_countdown_moves_and_keeps_the_queue_visible(status_tab):
    status_tab.on_queue_updated(_queue(3))

    status_tab.on_transfer_retry_scheduled("upload", 42)
    text = status_tab.sync_state_label.text()

    assert "42s" in text
    assert "3 file in coda" in text
    assert "Nessun errore" in text
    assert "riprovo" in text.lower()

    # Un secondo dopo il conto alla rovescia è sceso: la UI non è ferma.
    status_tab._retry_deadlines["upload"] = time.monotonic() + 12
    status_tab._tick_countdown()
    assert "12s" in status_tab.sync_state_label.text()

    # Scaduto il ritardo: "sto riprovando adesso", non un messaggio congelato.
    status_tab._retry_deadlines["upload"] = time.monotonic() - 1
    status_tab._tick_countdown()
    assert "riprovando adesso" in status_tab.sync_state_label.text()


def test_status_tab_forgets_a_retry_that_never_started(status_tab):
    status_tab.on_transfer_retry_scheduled("upload", 5)
    assert "riprovo" in status_tab.sync_state_label.text().lower()

    status_tab._retry_deadlines["upload"] = time.monotonic() - 10
    status_tab._tick_countdown()

    assert "riprovo" not in status_tab.sync_state_label.text().lower()
    assert "upload" not in status_tab._retry_deadlines


def test_status_tab_nas_activity_shows_the_countdown(status_tab):
    status_tab.on_transfer_retry_scheduled("download", 8)
    assert "riprovo tra 8s" in status_tab.nas_activity_label.text()


def test_transfers_tab_retry_message_names_the_queue(transfers_tab):
    transfers_tab.on_queue_updated(_queue(4))
    transfers_tab.on_transfer_retry_scheduled("upload", 30)
    transfers_tab._refresh_activity()

    text = transfers_tab.activity_label.text()
    assert "30s" in text
    assert "4 file" in text
    assert "nessuno è perso" in text

    transfers_tab._retry_deadlines["upload"] = time.monotonic() - 1
    transfers_tab._tick_retry_countdown()
    assert "riprovando adesso" in transfers_tab.activity_label.text()


def test_transfers_tab_retry_is_shown_instead_of_the_lock_error(transfers_tab):
    """Il dettaglio tecnico del lock non deve coprire l'attesa in corso."""
    transfers_tab.on_transfer_retry_scheduled("upload", 20)
    transfers_tab.on_transfer_lock_unavailable("upload", "lock NAS occupato da un altro PC")

    text = transfers_tab.activity_label.text()
    assert "20s" in text
    assert "lock NAS occupato" not in text


# --- file che scorrono: posizione nel batch e ultimo completato ---


def test_status_tab_shows_file_position_in_the_batch(status_tab):
    status_tab.on_transfer_started("upload")
    status_tab.on_batch_size_known(10)
    status_tab.on_transfer_item_started("upload", "cartella/documento.pdf", 1024)
    for index in range(3):
        status_tab.on_transfer_item_done("upload", f"cartella/{index}.bin")

    text = status_tab.sync_state_label.text()
    assert "documento.pdf" in text
    assert "file 3 di 10" in text
    assert "file 3 di 10" in status_tab.nas_activity_label.text()


def test_status_tab_batch_bar_moves_with_completed_files(status_tab):
    """La barra del singolo file resta a 0->100; quella del batch deve avanzare."""
    status_tab.on_transfer_started("upload")
    status_tab.on_batch_size_known(4)
    status_tab.on_transfer_item_started("upload", "cartella/a.bin", 1024)
    assert not status_tab.batch_progress_bar.isHidden()
    assert status_tab.batch_progress_bar.value() == 0

    status_tab.on_transfer_item_done("upload", "cartella/a.bin")
    assert status_tab.batch_progress_bar.value() == 25
    assert "1 di 4" in status_tab.batch_progress_bar.text()

    status_tab.on_transfer_finished("upload", True)
    assert status_tab.batch_progress_bar.isHidden()


def test_transfers_tab_shows_position_and_last_completed(transfers_tab):
    transfers_tab.on_queue_updated(_queue(5))
    transfers_tab.on_batch_size_known(5)
    transfers_tab.on_transfer_started("upload")
    transfers_tab.on_item_done("upload", "cartella/precedente.bin")
    transfers_tab.on_item_started("upload", "cartella/attuale.bin", 1024)
    transfers_tab._refresh_activity()

    text = transfers_tab.activity_label.text()
    assert "attuale.bin" in text
    assert "file 1 di 5" in text
    assert "precedente.bin" in text


# --- errore vero vs attesa normale ---


def test_status_tab_contention_is_not_reported_as_an_error(status_tab):
    status_tab.on_transfer_retry_scheduled("upload", 15)
    status_tab.on_transfer_lock_unavailable(
        "upload",
        "Un altro PC sta sincronizzando con il NAS: non è un errore e nessun dato è stato perso.",
    )

    assert status_tab.sync_feedback_label.isHidden()


def test_status_tab_a_real_lock_error_is_still_reported(status_tab):
    status_tab.on_transfer_lock_unavailable(
        "upload", "ssh: connect to host 192.168.1.119 port 22: Connection refused",
    )

    assert not status_tab.sync_feedback_label.isHidden()
    assert "lock NAS" in status_tab.sync_feedback_label.text()


def test_status_tab_interrupted_transfer_says_what_happens_next(status_tab):
    status_tab.on_transfer_started("upload")
    status_tab.on_transfer_finished("upload", False)

    assert not status_tab.sync_feedback_label.isHidden()
    text = status_tab.sync_feedback_label.text()
    assert "coda" in text
    assert "da solo" in text


def test_status_tab_a_deferral_does_not_add_an_error_line(status_tab):
    status_tab.on_transfer_preparing("upload")
    status_tab.on_transfer_retry_scheduled("upload", 10)
    status_tab.on_transfer_lock_unavailable("upload", "Un altro PC sta sincronizzando con il NAS.")
    status_tab.on_transfer_finished("upload", False)

    assert status_tab.sync_feedback_label.isHidden()
    assert "10s" in status_tab.sync_state_label.text()


# --- tray: il menu deve davvero fare qualcosa ---


class _StrictEngine:
    """Engine finto con la firma reale: wake() non accetta argomenti."""

    def __init__(self) -> None:
        self.sync_now_calls = 0
        self.wake_calls = 0

    def wake(self) -> None:
        self.wake_calls += 1

    def sync_now(self) -> None:
        self.sync_now_calls += 1


def test_tray_sync_now_actually_requests_a_sync(qapp):
    with ClientEnvironment():
        cfg = Config()
        engine = _StrictEngine()
        tray = TrayIcon(cfg, engine, MagicMock(), MagicMock())

        tray._sync_now()  # prima: TypeError silenzioso dentro lo slot Qt

        assert engine.sync_now_calls == 1


def test_tray_tooltip_counts_down_the_retry(qapp):
    with ClientEnvironment():
        cfg = Config()
        tray = TrayIcon(cfg, _StrictEngine(), MagicMock(), MagicMock())
        tray.on_status_changed({"connected": True, "configured": True, "host": "nas"})

        tray.on_transfer_retry_scheduled("upload", 25)
        assert "riprovo tra 25s" in tray.toolTip()

        tray._retry_until = time.monotonic() - 1
        tray._tick_retry()
        assert "riprovo" not in tray.toolTip()


def test_both_workers_expose_the_retry_signal():
    """La UI si aggancia a questo segnale: se cambia nome il conteggio sparisce."""
    assert hasattr(PushWorker, "transfer_retry_scheduled")
    assert hasattr(PullWorker, "transfer_retry_scheduled")
