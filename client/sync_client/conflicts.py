"""Discovery and safe resolution of NASBox conflict copies."""
from __future__ import annotations

import errno
import os
import re
import shutil
from dataclasses import dataclass
from pathlib import Path

from . import trash
from .sync_state import SyncStateStore


CONFLICT_TAG = " (conflitto da "
# rsync glob matching every conflict copy name. Mirrors use it so their
# `--delete` pass never removes a conflict copy from the mirrored folder:
# those copies are what the Conflicts tab lists, and the mirror's source
# folder obviously does not contain them.
#
# IMPORTANT: spaces must be escaped. rsync treats unescaped whitespace inside
# --exclude values as a rule separator, so the old "* (conflitto da *)"
# silently matched nothing and the mirror's --delete-after removed every
# conflict copy on its next pass. The escaped form is a single precise rule
# (verified against real rsync); the broader "*conflitto*" also works but would
# hide legitimate user files with "conflitto" in their name from a mirror.
CONFLICT_GLOB = "*conflitto\\ da\\ *"
CONFLICT_RE = re.compile(
    r"^(?P<stem>.*) \(conflitto da (?P<origin>[^ )]+) [^)]+\)(?P<suffix>\.[^/]*)?$"
)
INTERNAL_DIRS = {".sync-partial", ".sync-trash", ".nasbox-root"}


@dataclass(frozen=True)
class ConflictGroup:
    original: Path
    candidates: tuple[Path, ...]
    group_id: str = ""


def _original_path(path: Path) -> Path | None:
    match = CONFLICT_RE.match(path.name)
    if match is None:
        return None
    return path.with_name(match.group("stem") + (match.group("suffix") or ""))


def scan_conflict_groups(local_root: str, sync_state: SyncStateStore | None = None) -> list[ConflictGroup]:
    root = Path(local_root)
    groups: dict[Path, list[Path]] = {}
    if not root.exists():
        return []
    # os.walk (rather than rglob("*") + is_file()) lets us prune the internal
    # directories and avoids a stat for every ordinary file. On a 100k-file
    # tree the old form was the dominant cost of the Conflicts tab's refresh.
    for dirpath, dirnames, filenames in os.walk(root):
        dirnames[:] = sorted(
            name for name in dirnames if name not in INTERNAL_DIRS
        )
        for name in filenames:
            match = CONFLICT_RE.match(name)
            if match is None:
                continue
            path = Path(dirpath, name)
            try:
                if not path.is_file():
                    continue
            except OSError:
                continue
            original = path.with_name(match.group("stem") + (match.group("suffix") or ""))
            groups.setdefault(original, []).append(path)
    result: list[ConflictGroup] = []
    root = root.resolve()
    for original, candidates in sorted(groups.items(), key=lambda item: str(item[0]).lower()):
        candidates_tuple = tuple(sorted(candidates))
        group_id = ""
        if sync_state is not None:
            relative_original = str(original.resolve().relative_to(root))
            group_id = sync_state.upsert_conflict_group(
                relative_original,
                [_member_info(path, root) for path in candidates_tuple],
            )
        result.append(ConflictGroup(original, candidates_tuple, group_id))
    return result


def _member_info(path: Path, root: Path) -> dict[str, object]:
    try:
        info = path.stat()
    except OSError:
        return {"path": str(path.relative_to(root))}
    match = CONFLICT_RE.match(path.name)
    return {
        "path": str(path.relative_to(root)),
        "origin_device": match.group("origin") if match else "",
        "size": info.st_size,
        "mtime_ns": info.st_mtime_ns,
    }


def resolve_conflict(
    group: ConflictGroup, chosen: Path, local_root: str,
    sync_state: SyncStateStore | None = None,
) -> tuple[bool, str]:
    """Keep one version and move every losing version into local history."""
    root = Path(local_root).resolve()
    original = group.original.resolve()
    candidates = {path.resolve() for path in group.candidates if path.exists()}
    chosen = chosen.resolve()
    if chosen != original and chosen not in candidates:
        return False, "la versione scelta non è più disponibile"
    try:
        original.relative_to(root)
        for path in candidates:
            path.relative_to(root)
    except ValueError:
        return False, "percorso conflitto fuori dalla cartella NASBox"

    try:
        if chosen != original:
            # Promote the chosen copy by renaming it, not by copying it. The
            # old implementation copied the whole file into a temporary name,
            # which doubled disk I/O (and free space) for large conflicts and ran
            # on the GUI thread. Move the losing original into history first, so
            # the promotion itself stays an atomic rename on the same filesystem.
            if original.exists() and not trash.move_to_local_trash(original, str(root)):
                return False, "impossibile conservare l'originale nello storico locale"
            try:
                chosen.replace(original)
            except OSError as exc:
                if exc.errno != errno.EXDEV:
                    return False, str(exc)
                # Defensive fallback: a conflict copy can only live under the
                # NASBox root, but keep this working on exotic mount layouts.
                shutil.copy2(chosen, original)
                chosen.unlink(missing_ok=True)

        for path in sorted(candidates):
            if path.exists() and not trash.move_to_local_trash(path, str(root)):
                return False, f"impossibile spostare nello storico {path.name}"
        if sync_state is not None and group.group_id:
            chosen_relative = str(chosen.relative_to(root)) if chosen != original else str(original.relative_to(root))
            sync_state.mark_conflict_resolved(group.group_id, chosen_relative)
            if chosen != original:
                sync_state.mark_pending({str(original.relative_to(root))})
        return True, ""
    except OSError as exc:
        return False, str(exc)


def discard_conflict_copies(
    group: ConflictGroup, local_root: str,
    sync_state: SyncStateStore | None = None,
) -> tuple[bool, str]:
    """Move every alternative version into local history, keeping the original.

    This is the explicit "delete the conflicts" action: unlike resolve_conflict
    it never rewrites the original file, it only clears the extra copies.
    """
    root = Path(local_root).resolve()
    original = group.original.resolve()
    try:
        original.relative_to(root)
    except ValueError:
        return False, "percorso conflitto fuori dalla cartella NASBox"

    for path in sorted(group.candidates):
        if not path.exists():
            continue
        try:
            path.resolve().relative_to(root)
        except ValueError:
            return False, "percorso conflitto fuori dalla cartella NASBox"
        if not trash.move_to_local_trash(path, str(root)):
            return False, f"impossibile spostare nello storico {path.name}"

    if sync_state is not None and group.group_id:
        sync_state.mark_conflict_resolved(group.group_id, str(original.relative_to(root)))
    return True, ""
