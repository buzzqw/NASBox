from __future__ import annotations

import time

from PyQt6.QtCore import Qt, QTimer
from PyQt6.QtWidgets import (
    QAbstractItemView, QGroupBox, QHBoxLayout, QHeaderView, QLabel,
    QLineEdit, QProgressBar, QTableWidget, QTableWidgetItem, QToolButton,
    QVBoxLayout, QWidget,
)

from ..i18n import t
from ..rsync_ops import TransferItem
from ..transfer_scheduler import AGING_SECONDS, PRIORITY
from .format_utils import human_size as _human_size

FLUSH_INTERVAL_MS = 250  # batch fast-arriving updates instead of touching the table per event
MAX_VISIBLE_ROWS = 500  # keep large initial syncs responsive; the full queue still drives totals/search

# Ordine di massima priorita' (i valori numerici sono in transfer_scheduler.PRIORITY).
GROUP_ORDER = ("push", "pull", "conflict", "mirror", "preview")


def _direction_to_kind(direction: str) -> str:
    """Map a TransferItem direction to the scheduler operation it belongs to."""
    if direction in ("upload", "delete_remote", "rename_remote"):
        return "push"
    if direction in ("download", "delete_local"):
        return "pull"
    return "push"


def _direction_labels() -> dict[str, str]:
    return {
        "upload": t("transfers.dir_upload"),
        "download": t("transfers.dir_download"),
        "delete_remote": t("transfers.dir_delete_remote"),
        "delete_local": t("transfers.dir_delete_local"),
        "rename_remote": t("transfers.dir_rename_remote"),
    }


class TransfersTab(QWidget):
    def __init__(self, parent=None, engine=None) -> None:
        super().__init__(parent)
        self._direction_labels = _direction_labels()  # resolved once, at this language's startup
        # Injected by tests, otherwise discovered from the top-level window in
        # _engine_or_none(). Never creates a new engine or a remote connection.
        self._engine = engine
        root = QVBoxLayout(self)

        speed_box = QGroupBox(t("transfers.speed_title"))
        speed_layout = QHBoxLayout(speed_box)
        self.upload_speed_label = QLabel(t("transfers.upload_speed_idle"))
        self.download_speed_label = QLabel(t("transfers.download_speed_idle"))
        for label in (self.upload_speed_label, self.download_speed_label):
            label.setMinimumWidth(160)
            label.setAlignment(Qt.AlignmentFlag.AlignCenter)
        speed_layout.addWidget(self.upload_speed_label)
        speed_layout.addWidget(self.download_speed_label)
        self.queue_progress = QProgressBar()
        self.queue_progress.setMinimumWidth(240)
        self.queue_progress.setRange(0, 100)
        self.queue_progress.setValue(0)
        self.queue_progress.setFormat(t("transfers.progress_idle"))
        speed_layout.addWidget(self.queue_progress, 1)
        root.addWidget(speed_box)
        self.activity_label = QLabel(t("transfers.waiting"))
        self.activity_label.setObjectName("transferActivity")
        self.activity_label.setWordWrap(True)
        root.addWidget(self.activity_label)

        search_row = QHBoxLayout()
        search_row.addWidget(QLabel(t("transfers.search_label")))
        self.search_edit = QLineEdit()
        self.search_edit.setPlaceholderText(t("transfers.search_placeholder"))
        self.search_edit.textChanged.connect(lambda _: self._apply_filter())
        search_row.addWidget(self.search_edit)
        root.addLayout(search_row)

        self.summary_label = QLabel(t("transfers.no_items"))
        root.addWidget(self.summary_label)
        self.queue_note_label = QLabel(t("transfers.queue_note"))
        self.queue_note_label.setWordWrap(True)
        root.addWidget(self.queue_note_label)
        # Empty state with a concrete next action instead of an unexplained
        # blank table. Shown only when nothing is visible (queue empty or a
        # search with no matches).
        self.empty_state_label = QLabel(t("transfers.empty_hint"))
        self.empty_state_label.setObjectName("transferEmptyState")
        self.empty_state_label.setAccessibleName(t("transfers.empty_hint"))
        self.empty_state_label.setWordWrap(True)
        self.empty_state_label.hide()
        root.addWidget(self.empty_state_label)

        cols = [
            t("transfers.col_direction"), t("transfers.col_file"),
            t("transfers.col_size"), t("transfers.col_state"), t("transfers.col_progress"),
            t("transfers.col_priority"), t("transfers.col_aging"), t("transfers.col_controls"),
        ]
        self.table = QTableWidget(0, len(cols))
        self.table.setHorizontalHeaderLabels(cols)
        self.table.horizontalHeader().setSectionResizeMode(1, QHeaderView.ResizeMode.Stretch)
        self.table.horizontalHeader().setSectionResizeMode(3, QHeaderView.ResizeMode.ResizeToContents)
        self.table.horizontalHeader().setSectionResizeMode(4, QHeaderView.ResizeMode.ResizeToContents)
        self.table.horizontalHeader().setSectionResizeMode(5, QHeaderView.ResizeMode.ResizeToContents)
        self.table.horizontalHeader().setSectionResizeMode(6, QHeaderView.ResizeMode.ResizeToContents)
        self.table.horizontalHeader().setSectionResizeMode(7, QHeaderView.ResizeMode.ResizeToContents)
        self.table.setSelectionBehavior(QAbstractItemView.SelectionBehavior.SelectRows)
        self.table.setEditTriggers(QAbstractItemView.EditTrigger.NoEditTriggers)
        self.table.setContextMenuPolicy(Qt.ContextMenuPolicy.NoContextMenu)
        self.table.cellClicked.connect(self._on_table_cell_clicked)
        root.addWidget(self.table)

        # Full item list from the last scan, independent of the search filter --
        # the table only ever shows a subset of this, so a search doesn't lose data
        # and gets reapplied whenever a fresh scan or a completion comes in.
        self._all_items: list[TransferItem] = []
        # O(1) membership test for per-file events. Rebuilding
        # {(item.direction, item.path) for item in self._all_items} inside every
        # on_item_started() call put the GUI thread at 100% (and froze the
        # window) on a 30k-file transfer. None means stale: rebuilt on next use.
        self._queue_keys: set[tuple[str, str]] | None = None
        # A burst of newly started files must not re-render the table once per
        # file either; _flush() applies one coalesced rebuild per interval.
        self._filter_pending = False
        self._queue_sequence: dict[tuple[str, str], int] = {}
        self._next_queue_sequence = 1
        self._active_transfers: set[str] = set()
        self._preparing_transfers: set[str] = set()
        self._lock_waiting: set[str] = set()
        self._transfer_phases: dict[str, tuple[str, int, int]] = {}
        self._active_items: set[tuple[str, str]] = set()
        self._current_files: dict[str, str] = {}
        self._item_progress: dict[tuple[str, str], int] = {}
        self._pending_item_progress: dict[tuple[str, str], int] = {}
        self._preflight_progress: tuple[int, int] | None = None
        self._last_lock_error = ""
        self._completed_in_transfer = {"upload": 0, "download": 0}
        self._last_completion = ""
        self._queue_progress_running = False
        self._queue_progress_total = 0
        self._queue_progress_done = 0
        self._phase_started_at: float | None = None
        self._queue_scan_running = False
        self._queue_scan_started_at: float | None = None
        # A fallback scan can complete after a real transfer has already removed
        # rows. Suppress those just-completed paths once so they do not visibly
        # jump back into the queue before the next fresh scan.
        self._recently_completed: set[tuple[str, str]] = set()
        self._completed_items: set[tuple[str, str]] = set()
        # Deadline (monotonic) of the next automatic attempt after the NAS lock
        # was busy, per direction. Without a live countdown the queue looked
        # frozen right after the deferral message.
        self._retry_deadlines: dict[str, float] = {}
        # Last file each direction actually finished, so the activity line can
        # show files flowing instead of only the one currently in flight.
        self._last_completed: dict[str, str] = {}

        # --- raggruppamento per operazione / priorita' / aging ---
        self._collapsed_groups: set[str] = set()
        self._group_header_rows: dict[int, str] = {}
        self._group_labels: dict[str, str] = {
            kind: t(f"transfers.group_{kind}") for kind in GROUP_ORDER
        }
        # Timestamp locale (monotonic) in cui ogni elemento e' entrato in coda:
        # il modello TransferItem non lo porta, ma e' tutto cio' che serve per
        # mostrare l'aging senza nuove chiamate remote.
        self._queued_at: dict[tuple[str, str], float] = {}
        self._scheduler_snapshot: dict[str, object] = {}
        self._scheduler_signature: tuple[object, ...] = ()
        self._scheduler_signature_last: tuple[object, ...] = ()
        self._row_item_keys: dict[int, tuple[str, str]] = {}
        self._row_requests: dict[int, tuple[str, str, float]] = {}
        self._group_render_rows: dict[int, list[dict[str, object]]] = {}
        # Nessuna API per mettere in pausa un singolo file: il pulsante agisce
        # solo sull'operazione corrente (engine.cancel_current_transfer()).
        self._paused_keys: set[tuple[str, str]] = set()

        # Fast-arriving per-file completions and speed samples (during a big
        # transfer, potentially many per second) are buffered here and applied
        # to the table/labels in one batch every FLUSH_INTERVAL_MS, instead of
        # doing a full table scan + widget mutation for every single one --
        # that per-event approach is what made the UI lock up on large syncs.
        self._pending_removals: set[tuple[str, str]] = set()
        self._pending_speed: dict[str, tuple[float, int]] = {}

        self._flush_timer = QTimer(self)
        self._flush_timer.setInterval(FLUSH_INTERVAL_MS)
        self._flush_timer.timeout.connect(self._flush)
        self._flush_timer.start()

        # The countdown after a deferral has to move on its own; the 250 ms
        # flush timer only runs while something is actually transferring.
        self._activity_timer = QTimer(self)
        self._activity_timer.setInterval(1000)
        self._activity_timer.timeout.connect(self._tick_retry_countdown)
        self._activity_timer.timeout.connect(self._tick_aging)
        self._activity_timer.start()

    # --- full refresh from a periodic dry-run scan ---

    def on_queue_updated(self, items: list[TransferItem]) -> None:
        incoming = [
            item for item in items if (item.direction, item.path) not in self._recently_completed
        ]
        if self._active_transfers:
            incoming_keys = {(item.direction, item.path) for item in incoming}
            incoming.extend(
                item for item in self._all_items
                if (item.direction, item.path) not in incoming_keys
            )
        self._all_items = incoming
        self._queue_keys = None
        for item in sorted(incoming, key=self._queue_sort_key):
            self._ensure_queue_sequence((item.direction, item.path))
        self._recently_completed.clear()
        if self._queue_progress_running:
            self._refresh_queue_total_from_items()
            self._refresh_queue_progress()
        elif self._all_items and not self._queue_scan_running:
            self._queue_progress_total = len(self._all_items)
            self._queue_progress_done = 0
            self._refresh_queue_progress()
        self._apply_filter()

    def on_queue_items_known(self, items: list[TransferItem]) -> None:
        """Show the real plan when a transfer starts before the scanner refreshes."""
        existing = {(item.direction, item.path) for item in self._all_items}
        for item in items:
            key = (item.direction, item.path)
            self._recently_completed.discard(key)
            if key not in existing:
                self._all_items.append(item)
                if self._queue_keys is not None:
                    self._queue_keys.add(key)
                existing.add(key)
            self._ensure_queue_sequence(key)
        self._refresh_queue_total_from_items()
        self._apply_filter()

    def _known_keys(self) -> set[tuple[str, str]]:
        """Cached set of queued (direction, path) keys -- rebuilt only when the
        item list was replaced, never once per started file."""
        if self._queue_keys is None:
            self._queue_keys = {(item.direction, item.path) for item in self._all_items}
        return self._queue_keys

    def _schedule_filter_update(self) -> None:
        """Coalesce a burst of new queue items into the next _flush() rebuild."""
        self._filter_pending = True

    # --- raggruppamento per operazione, priorita' e aging ---

    def _engine_or_none(self):
        """Trova l'engine senza nuove dipendenze: nei test viene iniettato,
        nell'app e' un attributo della finestra principale."""
        if self._engine is not None:
            return self._engine
        engine = getattr(self.window(), "engine", None)
        if engine is not None:
            self._engine = engine
        return engine

    def _refresh_snapshot(self) -> None:
        """Legge solo lo snapshot locale dello scheduler (nessuna rete)."""
        engine = self._engine_or_none()
        scheduler = getattr(engine, "transfer_scheduler", None) if engine is not None else None
        if scheduler is None:
            self._scheduler_snapshot = {}
            self._scheduler_signature = ()
            return
        try:
            snapshot = scheduler.snapshot()
        except (AttributeError, OSError, RuntimeError, TypeError):
            self._scheduler_snapshot = {}
            self._scheduler_signature = ()
            return
        if not isinstance(snapshot, dict):
            self._scheduler_snapshot = {}
            self._scheduler_signature = ()
            return
        self._scheduler_snapshot = snapshot
        requests = snapshot.get("waiting") or []
        signature = (
            str(snapshot.get("active") or ""),
            tuple(
                (str(req.get("kind", "")), str(req.get("request_id", "")))
                for req in requests if isinstance(req, dict)
            ),
        )
        self._scheduler_signature = signature

    def _item_age(self, key: tuple[str, str]) -> float:
        queued_at = self._queued_at.get(key)
        if queued_at is None:
            queued_at = time.monotonic()
            self._queued_at[key] = queued_at
        return max(0.0, time.monotonic() - queued_at)

    @staticmethod
    def _priority_text(kind: str, age: float) -> str:
        base = PRIORITY.get(kind, PRIORITY["preview"])
        # Stessa formula di TransferScheduler._sort_key: la priorita' scende
        # con l'eta' (AGING_SECONDS) finche' l'operazione non viene servita.
        effective = base - age / AGING_SECONDS
        return t("transfers.priority_cell", base=base, effective=f"{effective:.1f}")

    @staticmethod
    def _age_text(age: float) -> str:
        return t("transfers.aging", seconds=max(0, int(age)))

    def _group_rows(self, kind: str, visible_items: list[TransferItem],
                    needle: str) -> list[dict[str, object]]:
        rows: list[dict[str, object]] = []
        for item in visible_items:
            if _direction_to_kind(item.direction) != kind:
                continue
            if needle and needle not in item.path.lower():
                continue
            rows.append({"kind": kind, "item": item})
        if not rows or kind in ("conflict", "mirror", "preview"):
            active_kind = str(self._scheduler_snapshot.get("active") or "")
            for request in self._scheduler_snapshot.get("waiting") or []:
                if not isinstance(request, dict) or request.get("kind") != kind:
                    continue
                request_id = str(request.get("request_id", ""))
                if needle and needle not in (self._group_labels[kind] + request_id).lower():
                    continue
                rows.append({
                    "kind": kind,
                    "item": None,
                    "request_id": request_id,
                    "age": float(request.get("age_seconds") or 0),
                    "active": False,
                })
            if kind == active_kind and not any(row["item"] is None for row in rows):
                rows.append({
                    "kind": kind, "item": None,
                    "request_id": t("transfers.item_active"),
                    "age": 0.0, "active": True,
                })
        return rows

    def _row_age(self, row: dict[str, object]) -> float:
        item = row.get("item")
        if item is not None:
            return self._item_age((item.direction, item.path))  # type: ignore[union-attr]
        return float(row.get("age") or 0)

    def _group_progress(self, rows: list[dict[str, object]]) -> int:
        if not rows:
            return 0
        done = 0.0
        for row in rows:
            item = row.get("item")
            if item is None:
                continue
            key = (item.direction, item.path)  # type: ignore[union-attr]
            if key in self._completed_items:
                done += 1.0
            elif key in self._active_items:
                done += max(0, min(100, self._item_progress.get(key, 0))) / 100.0
        return min(100, int(100 * done / len(rows)))

    def _set_group_progress_cell(self, row_index: int, rows: list[dict[str, object]]) -> None:
        percent = self._group_progress(rows)
        widget = QProgressBar()
        widget.setRange(0, 100)
        widget.setValue(percent)
        widget.setFormat(t("transfers.group_progress_format", percent=percent))
        widget.setToolTip(t("transfers.group_progress_tooltip"))
        widget.setMinimumWidth(80)
        self.table.setCellWidget(row_index, 4, widget)

    def _make_controls(self, key: tuple[str, str], active: bool) -> QWidget:
        container = QWidget()
        layout = QHBoxLayout(container)
        layout.setContentsMargins(2, 0, 2, 0)
        layout.setSpacing(2)
        engine = self._engine_or_none()
        can_pause = callable(getattr(engine, "cancel_current_transfer", None))

        pause = QToolButton()
        pause.setObjectName("transferPauseButton")
        pause.setToolButtonStyle(Qt.ToolButtonStyle.ToolButtonTextOnly)
        paused = key in self._paused_keys
        pause.setText(t("transfers.resume_btn") if paused else t("transfers.pause_btn"))
        pause.setAccessibleName(pause.text())
        pause.setToolTip(
            t("transfers.resume_tooltip") if paused else
            t("transfers.pause_tooltip") if active else
            t("transfers.pause_only_current_tooltip")
        )
        # Nessuna API per singolo elemento: solo l'operazione corrente.
        pause.setEnabled(active and can_pause)
        if active and can_pause:
            pause.clicked.connect(lambda _checked=False, item_key=key: self._toggle_item_pause(item_key))
        layout.addWidget(pause)

        move_top = QToolButton()
        move_top.setObjectName("transferMoveTopButton")
        move_top.setToolButtonStyle(Qt.ToolButtonStyle.ToolButtonTextOnly)
        move_top.setText(t("transfers.move_top_btn"))
        move_top.setAccessibleName(move_top.text())
        move_top.setEnabled(False)  # lo scheduler non espone una repioritizzazione
        move_top.setToolTip(t("transfers.move_top_tooltip"))
        layout.addWidget(move_top)
        layout.addStretch(1)
        return container

    def _toggle_item_pause(self, key: tuple[str, str]) -> None:
        engine = self._engine_or_none()
        if engine is None or not callable(getattr(engine, "cancel_current_transfer", None)):
            return
        if key in self._paused_keys:
            self._paused_keys.discard(key)
            wake = getattr(engine, "wake", None)
            if callable(wake):
                wake()
        else:
            self._paused_keys.clear()
            engine.cancel_current_transfer()
            self._paused_keys.add(key)
        self._apply_filter()

    def _toggle_group(self, kind: str) -> None:
        if kind in self._collapsed_groups:
            self._collapsed_groups.discard(kind)
        else:
            self._collapsed_groups.add(kind)
        self._apply_filter()

    def _on_table_cell_clicked(self, row: int, _column: int) -> None:
        kind = self._group_header_rows.get(row)
        if kind is not None:
            self._toggle_group(kind)

    def _apply_filter(self) -> None:
        needle = self.search_edit.text().strip().lower()
        self._refresh_snapshot()
        self._scheduler_signature_last = self._scheduler_signature
        # Stable priority sort only runs on queue/state changes, never on live
        # speed samples: active files stay at the top, waiting files follow,
        # and completed files move to the bottom so the queue visibly advances.
        items = sorted(self._all_items, key=self._queue_sort_key)
        visible_items = items[:MAX_VISIBLE_ROWS]

        groups: dict[str, list[dict[str, object]]] = {}
        for kind in GROUP_ORDER:
            groups[kind] = self._group_rows(kind, visible_items, needle)
        if not any(groups.values()):
            self.table.setRowCount(0)
            self._group_header_rows.clear()
            self._row_item_keys.clear()
            self._row_requests.clear()
            self._group_render_rows.clear()
            self.empty_state_label.setText(
                t("transfers.empty_search") if needle else t("transfers.empty_hint")
            )
            self.empty_state_label.show()
            self.queue_note_label.setText(t("transfers.queue_note"))
            self._update_summary()
            return
        self.empty_state_label.hide()

        total_rows = sum(
            1 + (0 if kind in self._collapsed_groups else len(rows))
            for kind, rows in groups.items() if rows
        )
        self.table.setRowCount(total_rows)
        # I widget delle righe vengono riutilizzati da Qt: puliscili sempre,
        # altrimenti una barra di gruppo puo' restare attaccata a una riga
        # diversa dopo un cambio di gruppo/collasso.
        for row_index in range(total_rows):
            for column in (4, 7):
                self.table.removeCellWidget(row_index, column)
            self.table.setVerticalHeaderItem(row_index, QTableWidgetItem(""))
        self._group_header_rows.clear()
        self._row_item_keys.clear()
        self._row_requests.clear()
        self._group_render_rows.clear()
        row_index = 0
        active_kind = str(self._scheduler_snapshot.get("active") or "")
        for kind in GROUP_ORDER:
            rows = groups[kind]
            if not rows:
                continue
            collapsed = kind in self._collapsed_groups
            marker = "▸" if collapsed else "▾"
            self._group_header_rows[row_index] = kind
            self._group_render_rows[row_index] = rows
            ages = [self._row_age(row) for row in rows]
            label = self._group_labels[kind]
            self.table.setItem(row_index, 0, QTableWidgetItem(f"{marker} {label}"))
            self.table.setItem(row_index, 1, QTableWidgetItem(t("transfers.group_count", count=len(rows))))
            total_size = sum(
                int(row["item"].size or 0) for row in rows if row.get("item") is not None
            )
            self.table.setItem(
                row_index, 2, QTableWidgetItem(_human_size(total_size) if total_size else "-"),
            )
            state = (
                t("transfers.group_active") if kind == active_kind else
                t("transfers.group_waiting") if ages else t("transfers.item_waiting")
            )
            self.table.setItem(row_index, 3, QTableWidgetItem(state))
            self._set_group_progress_cell(row_index, rows)
            oldest = max(ages, default=0.0)
            self.table.setItem(row_index, 5, QTableWidgetItem(self._priority_text(kind, oldest)))
            self.table.item(row_index, 5).setToolTip(t("transfers.group_priority_tooltip"))
            self.table.setItem(row_index, 6, QTableWidgetItem(self._age_text(oldest)))
            row_index += 1
            if collapsed:
                continue
            for row in rows:
                self._render_queue_row(row_index, row)
                row_index += 1

        self.queue_note_label.setText(
            t("transfers.visible_limit", shown=len(visible_items), total=len(items))
            if len(items) > MAX_VISIBLE_ROWS else t("transfers.queue_note")
        )
        self._update_summary()

    def _render_queue_row(self, row_index: int, row: dict[str, object]) -> None:
        kind = str(row["kind"])
        age = self._row_age(row)
        label = self._group_labels[kind]
        item = row.get("item")
        priority_cell = QTableWidgetItem(self._priority_text(kind, age))
        priority_cell.setToolTip(t("transfers.priority_tooltip"))
        age_cell = QTableWidgetItem(self._age_text(age))
        age_cell.setToolTip(t("transfers.aging_tooltip"))
        self.table.setItem(row_index, 5, priority_cell)
        self.table.setItem(row_index, 6, age_cell)

        if item is None:
            request_id = str(row.get("request_id") or "")
            active = bool(row.get("active"))
            self.table.setItem(row_index, 0, QTableWidgetItem(label))
            self.table.setItem(row_index, 1, QTableWidgetItem(t(
                "transfers.scheduler_request", kind=label, request_id=request_id,
            )))
            self.table.setItem(row_index, 2, QTableWidgetItem("-"))
            self.table.setItem(
                row_index, 3,
                QTableWidgetItem(t("transfers.item_active") if active else t("transfers.item_waiting")),
            )
            self._row_requests[row_index] = (kind, request_id, age)
            return

        key = (item.direction, item.path)  # type: ignore[union-attr]
        self._row_item_keys[row_index] = key
        active = key in self._active_items and self._current_files.get(item.direction) == item.path  # type: ignore[union-attr]
        state = (
            t("transfers.item_active") if key in self._active_items else
            t("transfers.item_completed") if key in self._completed_items else
            t("transfers.item_waiting")
        )
        self.table.setItem(row_index, 0, QTableWidgetItem(
            self._direction_labels.get(item.direction, item.direction)  # type: ignore[union-attr]
        ))
        self.table.setItem(row_index, 1, QTableWidgetItem(item.path))  # type: ignore[union-attr]
        self.table.setItem(row_index, 2, QTableWidgetItem(
            _human_size(item.size) if item.size else "-"  # type: ignore[union-attr]
        ))
        self.table.setItem(row_index, 3, QTableWidgetItem(state))
        self.table.setVerticalHeaderItem(
            row_index, QTableWidgetItem(str(self._ensure_queue_sequence(key))),
        )
        self._set_progress_cell(row_index, key)
        self.table.setCellWidget(row_index, 7, self._make_controls(key, active))

    def _tick_aging(self) -> None:
        """Aggiorna il tempo in coda e la priorita' effettiva ogni secondo."""
        self._refresh_snapshot()
        if self._scheduler_signature != self._scheduler_signature_last:
            self._scheduler_signature_last = self._scheduler_signature
            self._apply_filter()
            return
        for row_index, key in list(self._row_item_keys.items()):
            age = self._item_age(key)
            cell = self.table.item(row_index, 5)
            if cell is not None:
                cell.setText(self._priority_text(_direction_to_kind(key[0]), age))
            cell = self.table.item(row_index, 6)
            if cell is not None:
                cell.setText(self._age_text(age))
        waiting = {
            str(req.get("request_id")): float(req.get("age_seconds") or 0)
            for req in (self._scheduler_snapshot.get("waiting") or [])
            if isinstance(req, dict)
        }
        for row_index, (kind, request_id, frozen_age) in list(self._row_requests.items()):
            age = waiting.get(request_id, frozen_age)
            cell = self.table.item(row_index, 5)
            if cell is not None:
                cell.setText(self._priority_text(kind, age))
            cell = self.table.item(row_index, 6)
            if cell is not None:
                cell.setText(self._age_text(age))
        for row_index, rows in list(self._group_render_rows.items()):
            kind = self._group_header_rows.get(row_index)
            if kind is None:
                continue
            ages = [self._row_age(row) for row in rows]
            oldest = max(ages, default=0.0)
            cell = self.table.item(row_index, 5)
            if cell is not None:
                cell.setText(self._priority_text(kind, oldest))
            cell = self.table.item(row_index, 6)
            if cell is not None:
                cell.setText(self._age_text(oldest))

    def _queue_sort_key(self, item: TransferItem) -> tuple[int, int, int]:
        key = (item.direction, item.path)
        if key in self._completed_items:
            state_priority = 2
        elif key in self._active_items:
            state_priority = 0
        else:
            state_priority = 1
        kind_priority = PRIORITY.get(_direction_to_kind(item.direction), PRIORITY["preview"])
        return state_priority, kind_priority, self._queue_sequence.get(key, 0)

    def _ensure_queue_sequence(self, key: tuple[str, str]) -> int:
        sequence = self._queue_sequence.get(key)
        if sequence is None:
            sequence = self._next_queue_sequence
            self._queue_sequence[key] = sequence
            self._next_queue_sequence += 1
            self._queued_at.setdefault(key, time.monotonic())
        return sequence

    def _refresh_queue_total_from_items(self) -> None:
        if not self._queue_progress_running:
            return
        remaining = sum(
            1 for item in self._all_items
            if (item.direction, item.path) not in self._completed_items
        )
        self._queue_progress_total = max(
            self._queue_progress_total, self._queue_progress_done + remaining,
        )

    # --- live updates during an in-progress transfer (buffered, see _flush) ---

    def on_queue_scan_started(self) -> None:
        self._queue_scan_running = True
        self._queue_scan_started_at = time.monotonic()
        if not self._active_transfers:
            self.queue_progress.setRange(0, 0)
            self.queue_progress.setValue(0)
            self.queue_progress.setFormat(t("transfers.progress_scan"))
        self._apply_filter()
        self._refresh_activity()

    def on_queue_scan_finished(self) -> None:
        self._queue_scan_running = False
        self._queue_scan_started_at = None
        if not self._active_transfers:
            self.queue_progress.setRange(0, 100)
            self.queue_progress.setValue(0)
            self.queue_progress.setFormat(t("transfers.progress_idle"))
        self._apply_filter()
        self._refresh_activity()

    def on_transfer_preparing(self, direction: str) -> None:
        self._preparing_transfers.add(direction)
        self._active_transfers.add(direction)
        self._lock_waiting.discard(direction)
        self._retry_deadlines.pop(direction, None)
        self._preflight_progress = None
        self._phase_started_at = time.monotonic()
        self._last_lock_error = ""
        self._last_completion = ""
        self._begin_queue_progress()
        self._apply_filter()
        self._refresh_activity()

    def on_transfer_retry_scheduled(self, direction: str, seconds: int) -> None:
        """NAS lock busy: hold the queue and count down to the next attempt."""
        try:
            delay = max(1, int(seconds))
        except (TypeError, ValueError):
            delay = 1
        self._retry_deadlines[direction] = time.monotonic() + delay
        self._refresh_activity()

    def _retry_remaining(self, direction: str) -> int | None:
        deadline = self._retry_deadlines.get(direction)
        if deadline is None:
            return None
        remaining = deadline - time.monotonic()
        if remaining <= -2:
            self._retry_deadlines.pop(direction, None)
            return None
        return max(0, int(round(remaining)))

    def _retry_activity_text(self) -> str:
        for direction in ("upload", "download"):
            remaining = self._retry_remaining(direction)
            if remaining is None:
                continue
            label = self._direction_labels.get(direction, direction)
            if remaining <= 0:
                return t("transfers.retry_now", direction=label)
            return t(
                "transfers.retry_countdown",
                direction=label, seconds=remaining, count=len(self._all_items),
            )
        return ""

    def _tick_retry_countdown(self) -> None:
        if not self._retry_deadlines:
            return
        self._refresh_activity()

    def on_transfer_waiting_for_lock(self, direction: str) -> None:
        self._begin_queue_progress()
        self._active_transfers.add(direction)
        self._lock_waiting.add(direction)
        self._last_lock_error = ""
        self._last_completion = ""
        if self._phase_started_at is None:
            self._phase_started_at = time.monotonic()
        self._apply_filter()
        self._refresh_activity()

    def on_transfer_phase(self, direction: str, phase: str, done: int, total: int) -> None:
        self._transfer_phases[direction] = (phase, done, total)
        self._refresh_activity()

    def on_transfer_lock_unavailable(self, direction: str, detail: str) -> None:
        self._preparing_transfers.discard(direction)
        self._active_transfers.discard(direction)
        self._lock_waiting.discard(direction)
        self._last_lock_error = detail
        if not self._active_transfers:
            self._preflight_progress = None
            self._phase_started_at = None
            self._queue_progress_running = False
            self.queue_progress.setRange(0, 100)
            self.queue_progress.setValue(0)
            self.queue_progress.setFormat(t("transfers.progress_idle"))
        self._apply_filter()
        self._refresh_activity()

    def on_transfer_started(self, direction: str) -> None:
        self._preparing_transfers.discard(direction)
        self._retry_deadlines.pop(direction, None)
        self._preflight_progress = None
        if self._phase_started_at is None:
            self._phase_started_at = time.monotonic()
        self._begin_queue_progress()
        self._active_transfers.add(direction)
        self._lock_waiting.discard(direction)
        self._last_lock_error = ""
        self._last_completion = ""
        self._completed_in_transfer[direction] = 0
        self._refresh_activity()

    def on_item_started(self, direction: str, path: str, size: int = 0) -> None:
        key = (direction, path)
        previous_path = self._current_files.get(direction)
        if previous_path and previous_path != path:
            self._remove_visible_progress_cell((direction, previous_path))
        self._recently_completed.discard(key)
        known = key in self._known_keys()
        if not known:
            self._all_items.append(TransferItem(direction, path, size))
            self._known_keys().add(key)
        self._ensure_queue_sequence(key)
        self._active_items.add((direction, path))
        self._current_files[direction] = path
        self._item_progress[key] = 0
        if known:
            self._mark_visible_item_active(direction, path)
        else:
            self._schedule_filter_update()
        self._refresh_activity()

    def on_item_progress(self, direction: str, path: str, percent: int) -> None:
        if direction not in ("upload", "download"):
            return
        key = (direction, path)
        self._pending_item_progress[key] = max(0, min(100, percent))

    def on_item_done(self, direction: str, path: str) -> None:
        key = (direction, path)
        self._item_progress[key] = 100
        self._pending_item_progress.pop(key, None)
        self._last_completed["upload" if direction in ("upload", "delete_remote", "rename_remote") else "download"] = path
        self._pending_removals.add(key)
        self._completed_items.add(key)
        transfer_direction = "upload" if direction in ("upload", "delete_remote", "rename_remote") else "download"
        self._completed_in_transfer[transfer_direction] += 1
        self._begin_queue_progress()
        self._queue_progress_done += 1
        self._queue_progress_total = max(self._queue_progress_total, self._queue_progress_done)
        self._refresh_queue_progress()

    def on_speed_update(self, direction: str, bytes_per_sec: float, percent: int) -> None:
        self._pending_speed[direction] = (bytes_per_sec, percent)

    def on_preflight_progress(self, done: int, total: int) -> None:
        if total <= 0 or done >= total:
            self._preflight_progress = None
        else:
            self._preflight_progress = (done, total)
        self._refresh_activity()

    def on_transfer_finished(self, direction: str, ok: bool) -> None:
        self._preparing_transfers.discard(direction)
        if ok:
            self._retry_deadlines.pop(direction, None)
        self._active_items.difference_update(self._pending_removals)
        self._completed_items.update(self._pending_removals)
        self._pending_removals.clear()
        self._active_transfers.discard(direction)
        self._lock_waiting.discard(direction)
        self._current_files.pop(direction, None)
        self._transfer_phases.pop(direction, None)
        self._item_progress = {
            key: percent for key, percent in self._item_progress.items()
            if key[0] != direction
        }
        self._pending_item_progress = {
            key: percent for key, percent in self._pending_item_progress.items()
            if key[0] != direction
        }
        completed = self._completed_in_transfer[direction]
        if ok and completed:
            self._last_completion = t(
                "transfers.completed_batch", direction=self._direction_labels[direction], count=completed,
            )
        elif not ok:
            self._last_completion = t("transfers.failed_batch", direction=self._direction_labels[direction])
        if not self._active_transfers:
            self._preflight_progress = None
            self._phase_started_at = None
            self._queue_progress_running = False
            if not self._queue_scan_running:
                self.queue_progress.setRange(0, 100)
                self.queue_progress.setValue(0)
                self.queue_progress.setFormat(t("transfers.progress_idle"))
            self._recently_completed.update(self._completed_items)
            self._all_items = [
                item for item in self._all_items
                if (item.direction, item.path) not in self._completed_items
            ]
            self._queue_keys = None
            self._active_items.difference_update(
                {key for key in self._active_items if key[0] == direction}
            )
            if not self._all_items:
                self._queue_sequence.clear()
                self._queued_at.clear()
                self._next_queue_sequence = 1
            self._paused_keys = {
                key for key in self._paused_keys if key[0] != direction
            }
            self._completed_items.clear()
            self._apply_filter()
        if direction == "upload":
            self.upload_speed_label.setText(t("transfers.upload_speed_idle"))
        elif direction == "download":
            self.download_speed_label.setText(t("transfers.download_speed_idle"))
        self._refresh_activity()

    def _remaining_bytes_for_direction(self, direction: str) -> int:
        total = 0
        for item in self._all_items:
            key = (item.direction, item.path)
            if key in self._completed_items:
                continue
            item_dir = "upload" if item.direction in ("upload", "rename_remote") else "download"
            if item_dir != direction:
                continue
            size = item.size or 0
            progress = self._pending_item_progress.get(key, self._item_progress.get(key, 0))
            if progress > 0:
                remaining = int(size * (100 - progress) / 100)
            else:
                remaining = size
            total += max(0, remaining)
        return total

    def _flush(self) -> None:
        if self._pending_item_progress:
            pending_progress = self._pending_item_progress
            self._pending_item_progress = {}
            self._item_progress.update(pending_progress)
            for key in pending_progress:
                self._refresh_visible_progress_cell(key)

        if self._pending_speed:
            for direction, (bytes_per_sec, percent) in self._pending_speed.items():
                label = self.upload_speed_label if direction == "upload" else self.download_speed_label
                prefix = t("transfers.upload_prefix") if direction == "upload" else t("transfers.download_prefix")
                if bytes_per_sec <= 0:
                    label.setText(f"{prefix}: -")
                else:
                    eta_text = ""
                    remaining_bytes = self._remaining_bytes_for_direction(direction)
                    if remaining_bytes > 0 and bytes_per_sec > 1024:
                        eta_seconds = int(remaining_bytes / bytes_per_sec)
                        if eta_seconds < 60:
                            eta_text = f" • ~{eta_seconds}s"
                        elif eta_seconds < 3600:
                            eta_text = f" • ~{eta_seconds // 60}m {eta_seconds % 60}s"
                        else:
                            eta_text = f" • ~{eta_seconds // 3600}h {(eta_seconds % 3600) // 60}m"
                    label.setText(f"{prefix}: {_human_size(bytes_per_sec)}/s ({percent}%){eta_text}")
            self._pending_speed.clear()

        if self._pending_removals:
            pending = self._pending_removals
            self._pending_removals = set()
            self._active_items.difference_update(pending)
            self._recently_completed.update(pending)
            self._all_items = [
                item for item in self._all_items
                if (item.direction, item.path) not in pending
            ]
            self._queue_keys = None
            self._filter_pending = False
            self._apply_filter()
            self._refresh_activity()

        if self._filter_pending:
            # Coalesced rebuild from a burst of on_item_started() calls: one
            # sort + row refresh per flush interval instead of one per file.
            self._filter_pending = False
            self._refresh_queue_total_from_items()
            self._apply_filter()

        if self._active_transfers or self._preflight_progress is not None or self._queue_scan_running:
            self._refresh_activity()

    def _update_summary(self) -> None:
        n_up = n_down = n_del = 0
        for item in self._all_items:
            if item.direction in ("upload", "rename_remote"):
                n_up += 1
            elif item.direction == "download":
                n_down += 1
            elif item.direction in ("delete_local", "delete_remote"):
                n_del += 1

        if self._lock_waiting:
            self.summary_label.setText(t("transfers.summary_waiting_for_lock"))
        elif self._preparing_transfers or self._preflight_progress is not None:
            self.summary_label.setText(t("transfers.summary_preparing"))
        elif self._active_transfers and not self._all_items:
            self.summary_label.setText(t("transfers.summary_active_no_items"))
        elif self._active_transfers:
            # Transfer in progress with known items: show real counts below
            pass
        elif self._queue_scan_running:
            self.summary_label.setText(t("transfers.summary_scan_running"))
        elif not self._all_items:
            self.summary_label.setText(t("transfers.no_items_all_synced"))
        else:
            self.summary_label.setText(t("transfers.summary", up=n_up, down=n_down, delete=n_del))

    def _mark_visible_item_active(self, direction: str, path: str) -> None:
        """Update only the active row instead of rebuilding a large filtered table."""
        direction_label = self._direction_labels.get(direction, direction)
        for row in range(self.table.rowCount()):
            if (
                self.table.item(row, 0).text() == direction_label
                and self.table.item(row, 1).text() == path
            ):
                self.table.item(row, 3).setText(t("transfers.item_active"))
                self._set_progress_cell(row, (direction, path))
                return

    def _set_progress_cell(self, row: int, key: tuple[str, str]) -> None:
        direction, path = key
        active = (
            direction in ("upload", "download")
            and key in self._active_items
            and self._current_files.get(direction) == path
        )
        widget = self.table.cellWidget(row, 4)
        if not active:
            if widget is not None:
                self.table.removeCellWidget(row, 4)
            return
        if not isinstance(widget, QProgressBar):
            widget = QProgressBar()
            widget.setRange(0, 100)
            widget.setTextVisible(True)
            widget.setMinimumWidth(90)
            self.table.setCellWidget(row, 4, widget)
        widget.setValue(self._item_progress.get(key, 0))

    def _remove_visible_progress_cell(self, key: tuple[str, str]) -> None:
        direction, path = key
        direction_label = self._direction_labels.get(direction, direction)
        for row in range(self.table.rowCount()):
            if (
                self.table.item(row, 0).text() == direction_label
                and self.table.item(row, 1).text() == path
            ):
                self.table.removeCellWidget(row, 4)
                return

    def _refresh_visible_progress_cell(self, key: tuple[str, str]) -> None:
        direction, path = key
        direction_label = self._direction_labels.get(direction, direction)
        for row in range(self.table.rowCount()):
            if (
                self.table.item(row, 0).text() == direction_label
                and self.table.item(row, 1).text() == path
            ):
                self._set_progress_cell(row, key)
                return

    def on_batch_size_known(self, count: int) -> None:
        """PushWorker's authoritative count for the batch it's about to
        process, fired before its chunk loop starts. Seeded here (setting
        _queue_progress_running early) so the _begin_queue_progress() call
        that follows from on_transfer_started sees "already running" and
        leaves this real total alone, instead of overwriting it with
        ScanWorker's own preview count -- which stays empty/stale for the
        whole operation, since ScanWorker only ever tries a non-blocking
        transfer_lock acquire and this push holds that same lock the entire
        time. See push_worker.py's batch_size_known for the full story."""
        self._queue_progress_running = True
        self._queue_progress_total = count
        self._queue_progress_done = 0
        self._refresh_queue_progress()

    def _begin_queue_progress(self) -> None:
        if self._queue_progress_running:
            return
        self._queue_progress_running = True
        self._queue_progress_total = len(self._all_items)
        self._queue_progress_done = 0
        self._refresh_queue_progress()

    def _refresh_queue_progress(self) -> None:
        if self._queue_progress_total <= 0:
            self.queue_progress.setRange(0, 0)
            self.queue_progress.setValue(0)
            self.queue_progress.setFormat(t("transfers.progress_preparing"))
            return
        self.queue_progress.setRange(0, 100)
        percent = min(100, int(100 * self._queue_progress_done / self._queue_progress_total))
        self.queue_progress.setValue(percent)
        self.queue_progress.setFormat(t(
            "transfers.progress_format",
            done=self._queue_progress_done,
            total=self._queue_progress_total,
            percent=percent,
        ))

    def _phase_elapsed(self) -> int:
        if self._phase_started_at is None:
            return 0
        return max(0, int(time.monotonic() - self._phase_started_at))

    def _queue_scan_elapsed(self) -> int:
        if self._queue_scan_started_at is None:
            return 0
        return max(0, int(time.monotonic() - self._queue_scan_started_at))

    def _refresh_activity(self) -> None:
        if self._preflight_progress is not None:
            done, total = self._preflight_progress
            self.activity_label.setText(t(
                "transfers.preflight_progress", done=done, total=total,
                seconds=self._phase_elapsed(),
            ))
            return
        if not self._active_transfers:
            if self._queue_scan_running:
                self.activity_label.setText(t(
                    "transfers.queue_scan_running", seconds=self._queue_scan_elapsed(),
                ))
                return
            retry_text = self._retry_activity_text()
            if retry_text:
                self.activity_label.setText(retry_text)
                return
            self.activity_label.setText(
                t("transfers.lock_unavailable", detail=self._last_lock_error)
                if self._last_lock_error else self._last_completion or t("transfers.waiting")
            )
            return
        messages = []
        for direction in sorted(self._active_transfers):
            if direction in self._lock_waiting:
                messages.append(t(
                    "transfers.waiting_for_lock",
                    direction=self._direction_labels[direction], seconds=self._phase_elapsed(),
                ))
            elif direction in self._transfer_phases:
                phase, done, total = self._transfer_phases[direction]
                detail = t(f"transfers.phase_{phase}")
                if total > 0:
                    detail = f"{detail} ({done}/{total})"
                messages.append(t(
                    "transfers.phase_activity",
                    direction=self._direction_labels[direction], phase=detail,
                    seconds=self._phase_elapsed(),
                ))
                continue
            if direction in self._preparing_transfers:
                messages.append(t(
                    "transfers.preparing",
                    direction=self._direction_labels[direction], seconds=self._phase_elapsed(),
                ))
                continue
            path = self._current_files.get(direction)
            if path:
                position = ""
                if self._queue_progress_total > 0:
                    position = t(
                        "transfers.active_file_progress",
                        direction=self._direction_labels[direction],
                        path=path,
                        done=min(self._queue_progress_done, self._queue_progress_total),
                        total=self._queue_progress_total,
                        seconds=self._phase_elapsed(),
                    )
                messages.append(position or t(
                    "transfers.active_file",
                    direction=self._direction_labels[direction], path=path, seconds=self._phase_elapsed(),
                ))
                last_done = self._last_completed.get(direction)
                if last_done and last_done != path:
                    messages.append(t("transfers.last_completed", path=last_done))
            else:
                messages.append(t(
                    "transfers.active_starting",
                    direction=self._direction_labels[direction], seconds=self._phase_elapsed(),
                ))
        self.activity_label.setText("\n".join(messages))
