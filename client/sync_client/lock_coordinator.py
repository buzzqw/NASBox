"""Shared backoff for attempts to acquire the NAS transfer lock."""
from __future__ import annotations

import random
import threading
import time


class LockCoordinator:
    """Coordinate lock retries across transfers and queue previews in one client."""

    def __init__(self, initial_delay: float = 5.0, max_delay: float = 300.0) -> None:
        self._lock = threading.Lock()
        self._retry_until = 0.0
        self._next_delay = initial_delay
        self._initial_delay = initial_delay
        self._max_delay = max_delay

    def can_attempt(self, now: float | None = None) -> bool:
        with self._lock:
            return (time.time() if now is None else now) >= self._retry_until

    def defer(self) -> int:
        """Record contention and return the rounded delay before the next try."""
        with self._lock:
            delay = self._next_delay * random.uniform(0.8, 1.2)
            self._retry_until = time.time() + delay
            self._next_delay = min(self._next_delay * 2.0, self._max_delay)
            return max(1, round(delay))

    def acquired(self) -> None:
        with self._lock:
            self._retry_until = 0.0
            self._next_delay = self._initial_delay

    def retry_after(self, now: float | None = None) -> int:
        with self._lock:
            remaining = self._retry_until - (time.time() if now is None else now)
            return max(0, round(remaining))
