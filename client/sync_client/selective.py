"""Helpers for selective sync: choosing which subfolders take part.

The picker writes an anchored list of excluded *relative* paths (for example
``"RPG"`` or ``"lavoro/archivio"``). Unlike a user-typed rsync pattern, an entry
here matches only that exact path (and everything under it), never an unrelated
folder with the same name at another depth. rsync_ops turns the list into
anchor-prefixed ``--exclude`` arguments and applies the same rule to the
pre-transfer checks, so push, pull, the watcher and previews all agree.
"""
from __future__ import annotations

import os
from pathlib import Path


def list_directories(root: str, ignore_names: set[str], *, limit: int = 1000) -> tuple[list[str], bool]:
    """Relative paths of every directory under ``root``, in pre-order, skipping ignored names.

    Returns ``(paths, truncated)``. ``truncated`` is True when ``limit`` stopped
    the walk, so the caller can warn that the list is partial. Pre-order keeps a
    folder and its children together in the picker.
    """
    root_path = Path(root)
    if not root_path.is_dir():
        return [], False
    found: list[str] = []
    truncated = False

    def visit(directory: Path) -> None:
        nonlocal truncated
        if truncated:
            return
        try:
            entries = sorted(os.scandir(directory), key=lambda entry: entry.name)
        except OSError:
            return
        for entry in entries:
            try:
                is_dir = entry.is_dir(follow_symlinks=False)
            except OSError:
                continue
            if not is_dir or entry.name in ignore_names:
                continue
            found.append(os.path.relpath(entry.path, root_path).replace(os.sep, "/"))
            if len(found) >= limit:
                truncated = True
                return
            visit(Path(entry.path))

    visit(root_path)
    return found, truncated


def minimize_excluded(paths) -> list[str]:
    """Return the excluded paths with any entry already covered by an ancestor removed.

    A folder that is excluded hides its whole subtree, so listing a child of an
    already-excluded folder would be redundant (and would break when the parent
    is later re-included).
    """
    cleaned = sorted({str(path).strip().strip("/") for path in paths if str(path).strip().strip("/")})
    result: list[str] = []
    for path in cleaned:
        if any(path == kept or path.startswith(kept + "/") for kept in result):
            continue
        result.append(path)
    return result
