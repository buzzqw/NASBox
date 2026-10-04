from __future__ import annotations

from pathlib import Path
from unittest.mock import patch

from sync_client import sync_state
from sync_client.sync_state import SyncStateStore


def test_fingerprint_stat_cache(tmp_path):
    test_file = tmp_path / "hello.txt"
    test_file.write_text("hello world")

    # Clear stat digest cache
    sync_state.clear_fingerprint_cache()

    # First call: computes sha256 and populates cache
    fp1 = SyncStateStore.fingerprint(test_file)
    assert fp1 is not None
    assert fp1.size == 11
    assert fp1.digest is not None
    with sync_state._stat_digest_cache_lock:
        assert len(sync_state._stat_digest_cache) == 1

    # Second call without modifying file: should hit cache
    with patch("hashlib.file_digest") as mock_digest:
        fp2 = SyncStateStore.fingerprint(test_file)
        assert fp2 is not None
        assert fp2.digest == fp1.digest
        assert fp2.size == fp1.size
        # file_digest should NOT be called because it was cached
        mock_digest.assert_not_called()

    # Modifying content should result in cache miss or new cache entry
    test_file.write_text("hello world modified")
    fp3 = SyncStateStore.fingerprint(test_file)
    assert fp3 is not None
    assert fp3.digest != fp1.digest
    assert fp3.size == len("hello world modified")
