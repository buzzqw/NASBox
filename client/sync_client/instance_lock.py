"""Single-process guard for the local NASBox client."""
from __future__ import annotations

import fcntl
import os
from pathlib import Path

from . import paths


_lock_fd: int | None = None


def acquire(lock_path: Path | None = None) -> bool:
    """Acquire the local instance lock, returning False if another client owns it."""
    global _lock_fd
    if _lock_fd is not None:
        return True
    path = lock_path or paths.instance_lock_file()
    fd: int | None = None
    try:
        fd = os.open(path, os.O_RDWR | os.O_CREAT, 0o600)
        os.set_inheritable(fd, True)
        fcntl.flock(fd, fcntl.LOCK_EX | fcntl.LOCK_NB)
    except (OSError, BlockingIOError):
        if fd is not None:
            try:
                os.close(fd)
            except OSError:
                pass
        return False
    os.ftruncate(fd, 0)
    os.write(fd, f"{os.getpid()}\n".encode())
    _lock_fd = fd
    return True


def release() -> None:
    """Release the lock, primarily for isolated tests."""
    global _lock_fd
    if _lock_fd is None:
        return
    try:
        fcntl.flock(_lock_fd, fcntl.LOCK_UN)
    finally:
        os.close(_lock_fd)
        _lock_fd = None
