from __future__ import annotations

from sync_client.logger import EventLogger


def test_logger_tail_single_and_set_filters(tmp_path, monkeypatch):
    monkeypatch.setattr("sync_client.paths.events_file", lambda: tmp_path / "events.jsonl")
    monkeypatch.setattr("sync_client.paths.log_file", lambda: tmp_path / "sync.log")

    logger = EventLogger()
    logger.log("UPLOAD", "doc.txt", "100 B")
    logger.log("DOWNLOAD", "photo.jpg", "2 MB")
    logger.log("ERROR", "network", "timeout")
    logger.log("CONFLICT", "data.csv", "timestamp mismatch")
    logger.log("PRUNE", "old.bak", "deleted")

    # Single string filter
    errors = logger.tail(action_filter="ERROR")
    assert len(errors) == 1
    assert errors[0].action == "ERROR"

    # Set filter (macro-category like transfers)
    transfers = logger.tail(action_filter={"UPLOAD", "DOWNLOAD"})
    assert len(transfers) == 2
    assert {e.action for e in transfers} == {"UPLOAD", "DOWNLOAD"}

    # Macro-category: errors & warnings
    issues = logger.tail(action_filter={"ERROR", "WARN"})
    assert len(issues) == 1
    assert issues[0].action == "ERROR"

    # No filter returns all
    all_events = logger.tail()
    assert len(all_events) == 5
