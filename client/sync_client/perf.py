"""Very small timing helper for the handful of operations that can spike.

It logs only when an operation is slow enough to be felt (default 0.5 s), so it
does not add noise or measurable overhead to normal syncs. The goal is to answer
"where did the CPU go?" from the existing client log before changing algorithms.
"""
from __future__ import annotations

import time
from contextlib import contextmanager
from typing import Callable


@contextmanager
def timed(
    label: str,
    report: Callable[[str], None] | None = None,
    *,
    threshold: float = 0.5,
):
    """Report ``label: N.NNs`` when the block takes longer than ``threshold``."""
    started = time.perf_counter()
    try:
        yield
    finally:
        elapsed = time.perf_counter() - started
        if report is not None and elapsed >= threshold:
            report(f"{label}: {elapsed:.2f}s")
