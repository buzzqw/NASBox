"""Priority gate shared by all local transfer and preview workers."""
from __future__ import annotations

import threading
import time
from dataclasses import dataclass


PRIORITY = {"push": 0, "pull": 1, "mirror": 2, "preview": 3}


@dataclass
class _Request:
    kind: str
    sequence: int


class TransferScheduler:
    """Allow one operation at a time and prefer real sync work over previews."""

    def __init__(self) -> None:
        self._condition = threading.Condition()
        self._active = False
        self._sequence = 0
        self._waiting: list[_Request] = []

    def permit(self, kind: str) -> "TransferPermit":
        if kind not in PRIORITY:
            raise ValueError(f"tipo scheduler sconosciuto: {kind}")
        return TransferPermit(self, kind)

    def _acquire(self, kind: str, blocking: bool = True, timeout: float | None = None) -> bool:
        request = _Request(kind, self._sequence)
        with self._condition:
            self._sequence += 1
            if not blocking and (self._active or self._waiting):
                return False
            self._waiting.append(request)
            deadline = None if timeout is None else time.monotonic() + timeout
            while True:
                first = min(self._waiting, key=lambda item: (PRIORITY[item.kind], item.sequence))
                if not self._active and first is request:
                    self._waiting.remove(request)
                    self._active = True
                    return True
                if not blocking:
                    self._waiting.remove(request)
                    return False
                if deadline is None:
                    self._condition.wait()
                else:
                    remaining = deadline - time.monotonic()
                    if remaining <= 0:
                        self._waiting.remove(request)
                        self._condition.notify_all()
                        return False
                    self._condition.wait(remaining)

    def _release(self) -> None:
        with self._condition:
            if not self._active:
                raise RuntimeError("scheduler rilasciato senza possesso")
            self._active = False
            self._condition.notify_all()


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
