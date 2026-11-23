"""UX della scheda Trasferimenti: gruppi, priorita'/aging, controlli, empty state.

I test non fanno nessuna chiamata remota: lo scheduler e' finto e espone solo
snapshot(), esattamente come quello reale. Le stringhe nuove non sono ancora
presenti in i18n.py (verra' aggiornato a parte), quindi le aspettative usano
sempre t(chiave): passano sia con la chiave grezza sia con la traduzione.
"""
from __future__ import annotations

import time

import pytest
from PyQt6.QtWidgets import QProgressBar, QToolButton

from sync_client import i18n
from sync_client.gui.transfers_tab import TransfersTab
from sync_client.i18n import t
from sync_client.rsync_ops import TransferItem
from sync_client.transfer_scheduler import AGING_SECONDS, PRIORITY


# Traduzioni provvisorie per le chiavi nuove: i18n.py viene aggiornato in un
# secondo momento. Vengono installate solo se la chiave non esiste gia', cosi'
# quando le traduzioni definitive saranno presenti i test useranno quelle.
NEW_STRINGS = {
    "transfers.group_push": {"it": "Carica (push)", "en": "Upload (push)"},
    "transfers.group_pull": {"it": "Scarica (pull)", "en": "Download (pull)"},
    "transfers.group_conflict": {"it": "Conflitti", "en": "Conflicts"},
    "transfers.group_mirror": {"it": "Mirror", "en": "Mirrors"},
    "transfers.group_preview": {"it": "Anteprime", "en": "Previews"},
    "transfers.group_count": {"it": "{count} operazioni", "en": "{count} operations"},
    "transfers.group_progress_format": {"it": "Gruppo {percent}%", "en": "Group {percent}%"},
    "transfers.group_progress_tooltip": {
        "it": "Avanzamento del gruppo", "en": "Group progress",
    },
    "transfers.group_priority_tooltip": {
        "it": "Priorita' effettiva dell'operazione piu' vecchia del gruppo",
        "en": "Effective priority of the oldest operation in the group",
    },
    "transfers.group_active": {"it": "In corso", "en": "Running"},
    "transfers.group_waiting": {"it": "In attesa", "en": "Waiting"},
    "transfers.col_priority": {"it": "Priorita'", "en": "Priority"},
    "transfers.col_aging": {"it": "In coda da", "en": "Queued for"},
    "transfers.col_controls": {"it": "Azioni", "en": "Actions"},
    "transfers.priority_cell": {
        "it": "base {base} → effettiva {effective}",
        "en": "base {base} → effective {effective}",
    },
    "transfers.priority_tooltip": {
        "it": "Priorita' base meno l'invecchiamento (30 secondi)",
        "en": "Base priority minus aging (30 seconds)",
    },
    "transfers.aging": {"it": "{seconds}s", "en": "{seconds}s"},
    "transfers.aging_tooltip": {"it": "Tempo in coda", "en": "Time spent in queue"},
    "transfers.pause_btn": {"it": "Pausa", "en": "Pause"},
    "transfers.resume_btn": {"it": "Riprendi", "en": "Resume"},
    "transfers.pause_tooltip": {
        "it": "Interrompi l'operazione corrente", "en": "Stop the current operation",
    },
    "transfers.pause_only_current_tooltip": {
        "it": "Pausa disponibile solo per l'operazione corrente",
        "en": "Pause is available only for the current operation",
    },
    "transfers.resume_tooltip": {
        "it": "Riprendi la sincronizzazione", "en": "Resume synchronization",
    },
    "transfers.move_top_btn": {"it": "Sposta in cima", "en": "Move to top"},
    "transfers.move_top_tooltip": {
        "it": "La priorita' e' gestita automaticamente dallo scheduler con invecchiamento: non si puo' spostare un singolo elemento in cima.",
        "en": "Priority is managed automatically by the scheduler with aging: a single item cannot be moved to the top.",
    },
    "transfers.empty_hint": {
        "it": "Coda vuota: modifica un file nella cartella NASBox oppure avvia una sincronizzazione dalla scheda Stato.",
        "en": "Queue is empty: change a file in the NASBox folder or start a sync from the Status tab.",
    },
    "transfers.empty_search": {
        "it": "Nessun file corrisponde alla ricerca: prova con un altro nome.",
        "en": "No file matches the search: try a different name.",
    },
    "transfers.scheduler_request": {
        "it": "{kind}: richiesta {request_id}", "en": "{kind}: request {request_id}",
    },
}


@pytest.fixture(autouse=True)
def _install_new_translations(monkeypatch):
    for key, entry in NEW_STRINGS.items():
        if key not in i18n.STRINGS:
            monkeypatch.setitem(i18n.STRINGS, key, dict(entry))


@pytest.fixture
def transfers_tab(qapp):
    return TransfersTab()


class _FakeScheduler:
    def __init__(self, waiting=(), active=None) -> None:
        self._waiting = list(waiting)
        self._active = active
        self.snapshot_calls = 0

    def snapshot(self) -> dict:
        self.snapshot_calls += 1
        return {
            "active": self._active,
            "waiting": list(self._waiting),
            "count": len(self._waiting),
            "aging_seconds": AGING_SECONDS,
        }


class _FakeEngine:
    def __init__(self, waiting=(), active=None) -> None:
        self.transfer_scheduler = _FakeScheduler(waiting, active)
        self.cancel_calls = 0
        self.wake_calls = 0

    def cancel_current_transfer(self) -> None:
        self.cancel_calls += 1

    def wake(self) -> None:
        self.wake_calls += 1


def _queue() -> list[TransferItem]:
    return [
        TransferItem("upload", "cartella/a.bin", 100),
        TransferItem("upload", "cartella/b.bin", 200),
        TransferItem("download", "cartella/c.bin", 300),
    ]


def _header_rows(tab: TransfersTab) -> dict[str, int]:
    return {kind: row for row, kind in tab._group_header_rows.items()}


def _row_for_key(tab: TransfersTab, key: tuple[str, str]) -> int:
    return next(row for row, value in tab._row_item_keys.items() if value == key)


def _buttons(tab: TransfersTab, key: tuple[str, str]) -> tuple[QToolButton, QToolButton]:
    row = _row_for_key(tab, key)
    buttons = tab.table.cellWidget(row, 7).findChildren(QToolButton)
    return buttons[0], buttons[1]


# --- 1. raggruppamento per operazione ---


def test_queue_is_grouped_by_operation_with_count_and_bar(transfers_tab):
    transfers_tab.on_queue_updated(_queue())

    headers = _header_rows(transfers_tab)
    assert set(headers) == {"push", "pull"}
    assert "2" in transfers_tab.table.item(headers["push"], 1).text()
    assert "1" in transfers_tab.table.item(headers["pull"], 1).text()
    assert t("transfers.group_push") in transfers_tab.table.item(headers["push"], 0).text()
    for row in headers.values():
        assert isinstance(transfers_tab.table.cellWidget(row, 4), QProgressBar)

    # La barra del gruppo riflette i file completati del gruppo.
    transfers_tab.on_item_done("upload", "cartella/a.bin")
    transfers_tab._apply_filter()
    bar = transfers_tab.table.cellWidget(headers["push"], 4)
    assert bar.value() == 50


def test_group_header_click_collapses_and_expands(transfers_tab):
    transfers_tab.on_queue_updated(_queue())
    assert transfers_tab.table.rowCount() == 5  # 2 intestazioni + 3 file

    row = _header_rows(transfers_tab)["push"]
    transfers_tab.table.cellClicked.emit(row, 0)
    assert "push" in transfers_tab._collapsed_groups
    assert transfers_tab.table.rowCount() == 3  # file push nascosti, pull resta

    transfers_tab.table.cellClicked.emit(row, 0)
    assert "push" not in transfers_tab._collapsed_groups
    assert transfers_tab.table.rowCount() == 5
    assert transfers_tab._row_item_keys


# --- 2. priorita' effettiva e aging ---


def test_each_item_shows_effective_priority_and_time_in_queue(transfers_tab):
    transfers_tab.on_queue_updated(_queue())
    upload_key = ("upload", "cartella/a.bin")
    download_key = ("download", "cartella/c.bin")
    transfers_tab._queued_at[upload_key] = time.monotonic() - 15
    transfers_tab._queued_at[download_key] = time.monotonic() - 3
    transfers_tab._apply_filter()

    upload_row = _row_for_key(transfers_tab, upload_key)
    assert transfers_tab.table.item(upload_row, 6).text() == t("transfers.aging", seconds=15)
    effective = PRIORITY["push"] - 15 / AGING_SECONDS
    assert transfers_tab.table.item(upload_row, 5).text() == t(
        "transfers.priority_cell", base=PRIORITY["push"], effective=f"{effective:.1f}",
    )

    download_row = _row_for_key(transfers_tab, download_key)
    assert transfers_tab.table.item(download_row, 6).text() == t("transfers.aging", seconds=3)
    assert str(PRIORITY["pull"]) in transfers_tab.table.item(download_row, 5).text()


def test_scheduler_waiting_operations_appear_with_priority_and_age(transfers_tab):
    engine = _FakeEngine(waiting=[
        {"kind": "conflict", "request_id": "conflict-1", "age_seconds": 12},
        {"kind": "mirror", "request_id": "mirror-2", "age_seconds": 40},
        {"kind": "preview", "request_id": "preview-3", "age_seconds": 7},
    ])
    transfers_tab._engine = engine
    transfers_tab.on_queue_updated([])

    headers = _header_rows(transfers_tab)
    assert set(headers) == {"conflict", "mirror", "preview"}
    row = next(
        index for index, value in transfers_tab._row_requests.items()
        if value[1] == "conflict-1"
    )
    assert "conflict-1" in transfers_tab.table.item(row, 1).text()
    assert transfers_tab.table.item(row, 6).text() == t("transfers.aging", seconds=12)
    effective = PRIORITY["conflict"] - 12 / AGING_SECONDS
    assert transfers_tab.table.item(row, 5).text() == t(
        "transfers.priority_cell", base=PRIORITY["conflict"], effective=f"{effective:.1f}",
    )
    assert engine.transfer_scheduler.snapshot_calls >= 1

    # Il tick del timer aggiorna l'eta' senza ricostruire la tabella.
    engine.transfer_scheduler._waiting[0]["age_seconds"] = 20
    transfers_tab._tick_aging()
    assert transfers_tab.table.item(row, 6).text() == t("transfers.aging", seconds=20)


# --- 3. controlli: pausa dell'operazione corrente, move-top informativo ---


def test_pause_resume_only_for_the_current_item(transfers_tab):
    engine = _FakeEngine()
    transfers_tab._engine = engine
    transfers_tab.on_queue_updated(_queue())
    transfers_tab.on_transfer_started("upload")
    transfers_tab.on_item_started("upload", "cartella/a.bin", 100)
    transfers_tab._apply_filter()

    current = ("upload", "cartella/a.bin")
    other = ("upload", "cartella/b.bin")
    pause, move_top = _buttons(transfers_tab, current)
    assert pause.isEnabled()
    assert pause.text() == t("transfers.pause_btn")
    assert not move_top.isEnabled()
    assert move_top.toolTip() == t("transfers.move_top_tooltip")

    other_pause, _ = _buttons(transfers_tab, other)
    assert not other_pause.isEnabled()
    assert other_pause.toolTip() == t("transfers.pause_only_current_tooltip")

    pause.click()
    assert engine.cancel_calls == 1
    pause, _ = _buttons(transfers_tab, current)
    assert pause.text() == t("transfers.resume_btn")
    pause.click()
    assert engine.wake_calls == 1
    assert engine.cancel_calls == 1


def test_controls_do_not_invent_a_reprioritize_api(transfers_tab):
    """Senza API dedicata il pulsante resta disabilitato e spiega il perche'."""
    transfers_tab.on_queue_updated([TransferItem("download", "file.bin", 1)])
    key = ("download", "file.bin")
    _pause, move_top = _buttons(transfers_tab, key)
    assert not move_top.isEnabled()
    assert move_top.toolTip() == t("transfers.move_top_tooltip")
    assert "priorit" in move_top.toolTip().lower()


# --- 4. empty state con suggerimento ---


def test_empty_state_suggests_what_to_do(transfers_tab):
    transfers_tab.on_queue_updated([])
    assert not transfers_tab.empty_state_label.isHidden()
    assert transfers_tab.empty_state_label.text() == t("transfers.empty_hint")

    transfers_tab.on_queue_updated([TransferItem("upload", "file.bin", 1)])
    assert transfers_tab.empty_state_label.isHidden()

    transfers_tab.search_edit.setText("inesistente.bin")
    assert not transfers_tab.empty_state_label.isHidden()
    assert transfers_tab.empty_state_label.text() == t("transfers.empty_search")


# --- 5. tutte le nuove stringhe passano da t() ---


def test_new_labels_come_from_the_translation_table(qapp, monkeypatch):
    previous = i18n.current_language()
    monkeypatch.setitem(i18n.STRINGS, "transfers.empty_hint", {
        "it": "SUGGERIMENTO IT", "en": "HINT EN",
    })
    monkeypatch.setitem(i18n.STRINGS, "transfers.group_push", {
        "it": "PUSH IT", "en": "PUSH EN",
    })
    try:
        i18n.set_language("it")
        tab_it = TransfersTab()
        tab_it.on_queue_updated([])
        assert tab_it.empty_state_label.text() == "SUGGERIMENTO IT"
        tab_it.on_queue_updated([TransferItem("upload", "file.bin", 1)])
        assert "PUSH IT" in tab_it.table.item(0, 0).text()

        i18n.set_language("en")
        tab_en = TransfersTab()
        tab_en.on_queue_updated([])
        assert tab_en.empty_state_label.text() == "HINT EN"
    finally:
        i18n.set_language(previous)
