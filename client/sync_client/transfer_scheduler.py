"""Fair gate shared by all local transfer and preview workers."""
from __future__ import annotations

import threading
import time
from dataclasses import dataclass
from typing import Any


PRIORITY = {"push": 0, "pull": 1, "mirror": 2, "preview": 3}


@dataclass
class _Request:
    kind: str
    sequence: int
    queued_at: float
    request_id: str


AGING_SECONDS = 30.0


class TransferScheduler:
    """Allow one operation at a time with priority aging.

    Sync operations are preferred while the queue is young, but a preview (or
    mirror) that keeps waiting eventually overtakes newer work.  This avoids
    starving low-priority work when a watcher continuously produces pushes.
    """

    def __init__(self, state_store: Any | None = None) -> None:
        self._condition = threading.Condition()
        self._active = False
        self._active_kind: str | None = None
        self._sequence = 0
        self._waiting: list[_Request] = []
        self._state_store = state_store
        self._device_id = ""
        if state_store is not None:
            try:
                self._device_id = state_store.device_id()
                state_store.clear_scheduler_queue(self._device_id)
            except (AttributeError, OSError, RuntimeError):
                self._state_store = None

    def permit(self, kind: str) -> "TransferPermit":
        if kind not in PRIORITY:
            raise ValueError(f"tipo scheduler sconosciuto: {kind}")
        return TransferPermit(self, kind)

    def _acquire(self, kind: str, blocking: bool = True, timeout: float | None = None) -> bool:
        with self._condition:
            queued_at = time.monotonic()
            request = _Request(
                kind, self._sequence, queued_at,
                f"{self._device_id or 'process'}-{self._sequence}",
            )
            self._sequence += 1
            if not blocking and (self._active or self._waiting):
                return False
            self._waiting.append(request)
            self._persist_add(request)
            deadline = None if timeout is None else time.monotonic() + timeout
            while True:
                first = min(self._waiting, key=self._sort_key)
                if not self._active and first is request:
                    self._waiting.remove(request)
                    self._persist_remove(request)
                    self._active = True
                    self._active_kind = kind
                    return True
                if not blocking:
                    self._waiting.remove(request)
                    self._persist_remove(request)
                    return False
                if deadline is None:
                    self._condition.wait()
                else:
                    remaining = deadline - time.monotonic()
                    if remaining <= 0:
                        self._waiting.remove(request)
                        self._persist_remove(request)
                        self._condition.notify_all()
                        return False
                    self._condition.wait(remaining)

    def _release(self) -> None:
        with self._condition:
            if not self._active:
                raise RuntimeError("scheduler rilasciato senza possesso")
            self._active = False
            self._active_kind = None
            self._condition.notify_all()

    def _sort_key(self, request: _Request) -> tuple[float, int]:
        waited = max(0.0, time.monotonic() - request.queued_at)
        effective_priority = PRIORITY[request.kind] - waited / AGING_SECONDS
        return effective_priority, request.sequence

    def _persist_add(self, request: _Request) -> None:
        if self._state_store is None:
            return
        try:
            self._state_store.scheduler_queue_add(
                request.request_id, self._device_id, request.kind,
                request.sequence, request.queued_at,
            )
        except (AttributeError, OSError, RuntimeError):
            pass

    def _persist_remove(self, request: _Request) -> None:
        if self._state_store is None:
            return
        try:
            self._state_store.scheduler_queue_remove(request.request_id)
        except (AttributeError, OSError, RuntimeError):
            pass

    def snapshot(self) -> dict[str, object]:
        """Return a GUI-safe view of the active operation and waiting queue."""
        now = time.monotonic()
        with self._condition:
            waiting = sorted(self._waiting, key=self._sort_key)
            return {
                "active": self._active_kind,
                "waiting": [
                    {
                        "kind": item.kind,
                        "sequence": item.sequence,
                        "request_id": item.request_id,
                        "age_seconds": round(max(0.0, now - item.queued_at), 1),
                    }
                    for item in waiting
                ],
                "count": len(waiting),
                "aging_seconds": AGING_SECONDS,
            }


class TransferPermit:
    def __init__(self, scheduler: TransferScheduler, kind: str) -> None:
        self.scheduler = scheduler
        self.kind = kind
        self._held = False

    def acquire(self, blocking: bool = True, timeout: float | None = None) -> bool:
        self._held = self.scheduler._acquire(self.kind, blocking, timeout)
        return self._held

    def release(self) -> None:
        if self._held:
            self._held = False
            self.scheduler._release()

    def __enter__(self) -> "TransferPermit":
        if not self.acquire():
            raise RuntimeError("impossibile acquisire il token scheduler")
        return self

    def __exit__(self, _exc_type, _exc, _tb) -> None:
        self.release()
