"""I timer PERF devono essere silenziosi se non c'è un picco."""
from __future__ import annotations

from sync_client import perf


def test_timed_reports_only_slow_operations():
    seen: list[str] = []
    with perf.timed("fast", seen.append, threshold=10):
        pass
    assert seen == []

    with perf.timed("slow", seen.append, threshold=-1):
        pass
    assert len(seen) == 1
    assert seen[0].startswith("slow: ")
    assert seen[0].endswith("s")
