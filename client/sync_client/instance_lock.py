"""Single-process guard for the local NASBox client."""
from __future__ import annotations

import fcntl
import os
from pathlib import Path

from . import paths


_lock_fd: int | None = None
# The client updates itself with os.execv(), which keeps the process alive but
# replaces its image. The lock fd is deliberately inheritable, so the new image
# already owns the lock; without handing its number over, the fresh instance
# would open a second fd and flock() would refuse it -- the updated client then
# exited with "already running" and the user saw the app simply disappear.
_REEXEC_FD_ENV = "NASBOX_INSTANCE_LOCK_FD"


def _adopt_inherited() -> bool:
    """Reuse a lock fd inherited from the pre-exec image, if still valid."""
    global _lock_fd
    raw = os.environ.pop(_REEXEC_FD_ENV, "")
    if not raw:
        return False
    try:
        fd = int(raw)
        os.fstat(fd)
        fcntl.flock(fd, fcntl.LOCK_EX | fcntl.LOCK_NB)
    except (OSError, ValueError):
        return False
    _lock_fd = fd
    try:
        os.ftruncate(fd, 0)
        os.write(fd, f"{os.getpid()}\n".encode())
    except OSError:
        pass
    return True


def acquire(lock_path: Path | None = None) -> bool:
    """Acquire the local instance lock, returning False if another client owns it."""
    global _lock_fd
    if _lock_fd is not None:
        return True
    # Never adopt an inherited fd in an isolated test/alternate-lock-path call:
    # that fd belongs to the real single-instance lock, not to this path.
    if lock_path is None and _adopt_inherited():
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


def prepare_reexec() -> bool:
    """Hand the held lock to the process image that is about to replace this one.

    Must be called immediately before os.execv(). Returns False when no lock is
    held (for example in a test), in which case the callee can exec anyway.
    """
    if _lock_fd is None:
        return False
    try:
        os.set_inheritable(_lock_fd, True)
    except OSError:
        return False
    os.environ[_REEXEC_FD_ENV] = str(_lock_fd)
    return True


def cancel_reexec() -> None:
    """Drop a prepared hand-over after os.execv() failed and this process lives on."""
    os.environ.pop(_REEXEC_FD_ENV, None)


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
