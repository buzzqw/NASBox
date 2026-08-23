from __future__ import annotations

import time

from PyQt6.QtCore import Qt, QTimer
from PyQt6.QtWidgets import (
    QAbstractItemView, QGroupBox, QHBoxLayout, QHeaderView, QLabel,
    QLineEdit, QProgressBar, QTableWidget, QTableWidgetItem, QVBoxLayout, QWidget,
)

from ..i18n import t
from ..rsync_ops import TransferItem
from .format_utils import human_size as _human_size

FLUSH_INTERVAL_MS = 250  # batch fast-arriving updates instead of touching the table per event
MAX_VISIBLE_ROWS = 500  # keep large initial syncs responsive; the full queue still drives totals/search


def _direction_labels() -> dict[str, str]:
    return {
        "upload": t("transfers.dir_upload"),
        "download": t("transfers.dir_download"),
        "delete_remote": t("transfers.dir_delete_remote"),
        "delete_local": t("transfers.dir_delete_local"),
    }


class TransfersTab(QWidget):
    def __init__(self, parent=None) -> None:
        super().__init__(parent)
        self._direction_labels = _direction_labels()  # resolved once, at this language's startup
        root = QVBoxLayout(self)

        speed_box = QGroupBox(t("transfers.speed_title"))
        speed_layout = QHBoxLayout(speed_box)
        self.upload_speed_label = QLabel(t("transfers.upload_speed_idle"))
        self.download_speed_label = QLabel(t("transfers.download_speed_idle"))
        for label in (self.upload_speed_label, self.download_speed_label):
            label.setFixedWidth(150)
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

        cols = [
            t("transfers.col_direction"), t("transfers.col_file"),
            t("transfers.col_size"), t("transfers.col_state"), t("transfers.col_progress"),
        ]
        self.table = QTableWidget(0, len(cols))
        self.table.setHorizontalHeaderLabels(cols)
        self.table.horizontalHeader().setSectionResizeMode(1, QHeaderView.ResizeMode.Stretch)
        self.table.horizontalHeader().setSectionResizeMode(3, QHeaderView.ResizeMode.ResizeToContents)
        self.table.horizontalHeader().setSectionResizeMode(4, QHeaderView.ResizeMode.ResizeToContents)
        self.table.setSelectionBehavior(QAbstractItemView.SelectionBehavior.SelectRows)
        self.table.setEditTriggers(QAbstractItemView.EditTrigger.NoEditTriggers)
        root.addWidget(self.table)

        # Full item list from the last scan, independent of the search filter --
        # the table only ever shows a subset of this, so a search doesn't lose data
        # and gets reapplied whenever a fresh scan or a completion comes in.
        self._all_items: list[TransferItem] = []
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
                existing.add(key)
            self._ensure_queue_sequence(key)
        self._refresh_queue_total_from_items()
        self._apply_filter()

    def _apply_filter(self) -> None:
        needle = self.search_edit.text().strip().lower()
        # Stable priority sort only runs on queue/state changes, never on live
        # speed samples: active files stay at the top, waiting files follow,
        # and completed files move to the bottom so the queue visibly advances.
        items = sorted(self._all_items, key=self._queue_sort_key)
        if needle:
            items = [it for it in items if needle in it.path.lower()]
        visible_items = items[:MAX_VISIBLE_ROWS]
        self.table.setRowCount(len(visible_items))
        for row, item in enumerate(visible_items):
            self.table.setItem(row, 0, QTableWidgetItem(self._direction_labels.get(item.direction, item.direction)))
            self.table.setItem(row, 1, QTableWidgetItem(item.path))
            self.table.setItem(row, 2, QTableWidgetItem(_human_size(item.size) if item.size else "-"))
            key = (item.direction, item.path)
            if key in self._active_items:
                state = t("transfers.item_active")
            elif key in self._completed_items:
                state = t("transfers.item_completed")
            else:
                state = t("transfers.item_waiting")
            self.table.setItem(row, 3, QTableWidgetItem(state))
            self.table.setVerticalHeaderItem(
                row, QTableWidgetItem(str(self._ensure_queue_sequence(key))),
            )
            self._set_progress_cell(row, key)
        self.queue_note_label.setText(
            t("transfers.visible_limit", shown=len(visible_items), total=len(items))
            if len(items) > MAX_VISIBLE_ROWS else t("transfers.queue_note")
        )
        self._update_summary()

    def _queue_sort_key(self, item: TransferItem) -> tuple[int, int, int]:
        key = (item.direction, item.path)
        if key in self._completed_items:
            state_priority = 2
        elif key in self._active_items:
            state_priority = 0
        else:
            state_priority = 1
        direction_priority = 0 if item.direction == "upload" else 1
        return state_priority, direction_priority, self._queue_sequence.get(key, 0)

    def _ensure_queue_sequence(self, key: tuple[str, str]) -> int:
        sequence = self._queue_sequence.get(key)
        if sequence is None:
            sequence = self._next_queue_sequence
            self._queue_sequence[key] = sequence
            self._next_queue_sequence += 1
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
        self._preflight_progress = None
        self._phase_started_at = time.monotonic()
        self._last_lock_error = ""
        self._last_completion = ""
        self._begin_queue_progress()
        self._apply_filter()
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
        known = key in {(item.direction, item.path) for item in self._all_items}
        if not known:
            self._all_items.append(TransferItem(direction, path, size))
        self._ensure_queue_sequence(key)
        self._refresh_queue_total_from_items()
        self._active_items.add((direction, path))
        self._current_files[direction] = path
        self._item_progress[key] = 0
        if known:
            self._mark_visible_item_active(direction, path)
        else:
            self._apply_filter()
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
        self._pending_removals.add(key)
        self._completed_items.add(key)
        transfer_direction = "upload" if direction in ("upload", "delete_remote") else "download"
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
            self._active_items.difference_update(
                {key for key in self._active_items if key[0] == direction}
            )
            if not self._all_items:
                self._queue_sequence.clear()
                self._next_queue_sequence = 1
            self._completed_items.clear()
            self._apply_filter()
        self._refresh_activity()

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
                    label.setText(f"{prefix}: {_human_size(bytes_per_sec)}/s ({percent}%)")
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
            self._apply_filter()
            self._refresh_activity()

        if self._active_transfers or self._preflight_progress is not None or self._queue_scan_running:
            self._refresh_activity()

    def _update_summary(self) -> None:
        n_up = n_down = n_del = 0
        for item in self._all_items:
            if item.direction == "upload":
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
                messages.append(t(
                    "transfers.active_file",
                    direction=self._direction_labels[direction], path=path, seconds=self._phase_elapsed(),
                ))
            else:
                messages.append(t(
                    "transfers.active_starting",
                    direction=self._direction_labels[direction], seconds=self._phase_elapsed(),
                ))
        self.activity_label.setText("\n".join(messages))
