"""Classificazione lato client: rename non sono cancellazioni."""
from __future__ import annotations

from types import SimpleNamespace

from sync_client.change_classifier import classify
from sync_client.reconcile import RemoteKind


def _remote(digest: str, size: int = 10):
    return SimpleNamespace(kind=RemoteKind.FILE, digest=digest, size=size)


def test_rename_is_not_counted_as_deletion():
    missing = ["old/a.png"]
    remote_entries = {
        "old/a.png": _remote("abc"),
        "new/b.png": _remote("abc"),
    }
    identity = {"new/b.png": {"digest": "abc", "size": 10, "ctime_ns": 123}}
    report = classify(missing, remote_entries, identity)

    assert len(report.renames) == 1
    assert report.renames[0].missing_path == "old/a.png"
    assert report.renames[0].local_path == "new/b.png"
    assert report.renames[0].reason == "digest+mtime"
    assert report.effective_deletions == 0
    assert "1 rename" in report.summary()


def test_scattered_deletions_have_no_directory_anomaly():
    remote_entries = {f"folder/file{i}.txt": _remote(f"d{i}") for i in range(10)}
    missing = list(remote_entries)[:3]
    report = classify(missing, remote_entries, {})
    assert report.anomalies == []
    assert report.effective_deletions == 3


def test_whole_directory_disappearance_is_flagged():
    remote_entries = {f"OptAlarm/file{i}.txt": _remote(f"d{i}") for i in range(40)}
    missing = list(remote_entries)[:35]
    report = classify(missing, remote_entries, {}, dir_ratio=0.5, min_files=20)
    assert len(report.anomalies) == 1
    anomaly = report.anomalies[0]
    assert anomaly.directory == "OptAlarm"
    assert anomaly.missing == 35 and anomaly.remote == 40
    assert 0.87 <= anomaly.ratio <= 0.88
    assert "OptAlarm 35/40" in report.summary()


def test_rename_match_requires_same_size():
    remote_entries = {
        "old/a.png": _remote("abc", size=10),
        "new/b.png": _remote("abc", size=999),
    }
    identity = {"new/b.png": {"digest": "abc", "size": 999, "ctime_ns": 1}}
    report = classify(["old/a.png"], remote_entries, identity)
    assert report.renames == []
    assert report.effective_deletions == 1
