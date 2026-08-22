"""Discovery and safe resolution of NASBox conflict copies."""
from __future__ import annotations

import re
import shutil
import uuid
from dataclasses import dataclass
from pathlib import Path

from . import trash
from .sync_state import SyncStateStore


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
    for path in root.rglob("*"):
        if not path.is_file() or any(part in INTERNAL_DIRS for part in path.relative_to(root).parts):
            continue
        original = _original_path(path)
        if original is not None:
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

    temporary: Path | None = None
    try:
        if chosen != original:
            temporary = original.with_name(f".{original.name}.nasbox-conflict-{uuid.uuid4().hex}")
            temporary.parent.mkdir(parents=True, exist_ok=True)
            shutil.copy2(chosen, temporary)
            if original.exists() and not trash.move_to_local_trash(original, str(root)):
                return False, "impossibile conservare l'originale nello storico locale"
            temporary.replace(original)
            temporary = None

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
    finally:
        if temporary is not None:
            temporary.unlink(missing_ok=True)
