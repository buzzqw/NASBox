from __future__ import annotations

import pytest

from sync_client import instance_lock, paths, updater

from tests.support import ClientEnvironment


def test_reexec_handover_adopts_the_inherited_lock() -> None:
    """os.execv keeps the lock fd; the new image must reuse it, not re-lock it."""
    with ClientEnvironment():
        paths.ensure_dirs()
        instance_lock.release()
        assert instance_lock.acquire()
        original_fd = instance_lock._lock_fd
        assert original_fd is not None
        assert instance_lock.prepare_reexec()

        # Simulate the fresh process image: module state is gone, the inherited
        # fd and environment survive across exec.
        instance_lock._lock_fd = None
        try:
            assert instance_lock.acquire()
            assert instance_lock._lock_fd == original_fd
        finally:
            instance_lock.cancel_reexec()
            instance_lock.release()


def test_second_acquire_without_handover_is_refused() -> None:
    """The bug: a fresh image re-opened the lock file and flock() failed."""
    with ClientEnvironment():
        paths.ensure_dirs()
        instance_lock.release()
        assert instance_lock.acquire()
        original_fd = instance_lock._lock_fd

        instance_lock._lock_fd = None
        try:
            assert not instance_lock.acquire()
        finally:
            instance_lock._lock_fd = original_fd
            instance_lock.release()


def test_restart_hands_the_lock_over_before_exec(monkeypatch, tmp_path) -> None:
    calls: list[tuple[str, list[str]]] = []
    prepared: list[bool] = []

    monkeypatch.setattr(updater.sys, "executable", "/usr/bin/python3")
    monkeypatch.setattr(updater.sys, "argv", ["main.py", "--flag"])
    monkeypatch.setattr(updater.os, "execv", lambda exe, argv: calls.append((exe, argv)))
    monkeypatch.setattr(instance_lock, "prepare_reexec", lambda: prepared.append(True) or True)

    updater.restart(tmp_path)

    assert prepared == [True]
    assert calls == [(
        "/usr/bin/python3",
        ["/usr/bin/python3", str(tmp_path / "main.py"), "--flag"],
    )]


def test_restart_failure_cancels_handover(monkeypatch, tmp_path) -> None:
    cancelled: list[bool] = []

    monkeypatch.setattr(updater.sys, "argv", ["main.py"])
    monkeypatch.setattr(instance_lock, "prepare_reexec", lambda: True)
    monkeypatch.setattr(instance_lock, "cancel_reexec", lambda: cancelled.append(True))

    def failing_exec(*_args):
        raise OSError("exec failed")

    monkeypatch.setattr(updater.os, "execv", failing_exec)

    with pytest.raises(OSError):
        updater.restart(tmp_path)
    assert cancelled == [True]
