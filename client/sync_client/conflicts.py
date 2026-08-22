"""Discovery and safe resolution of NASBox conflict copies."""
from __future__ import annotations

import re
import shutil
import uuid
from dataclasses import dataclass
from pathlib import Path

from . import trash


CONFLICT_RE = re.compile(r"^(?P<stem>.*) \(conflitto da [^)]+\)(?P<suffix>\.[^/]*)?$")
INTERNAL_DIRS = {".sync-partial", ".sync-trash", ".nasbox-root"}


@dataclass(frozen=True)
class ConflictGroup:
    original: Path
    candidates: tuple[Path, ...]


def _original_path(path: Path) -> Path | None:
    match = CONFLICT_RE.match(path.name)
    if match is None:
        return None
    return path.with_name(match.group("stem") + (match.group("suffix") or ""))


def scan_conflict_groups(local_root: str) -> list[ConflictGroup]:
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
    return [
        ConflictGroup(original, tuple(sorted(candidates)))
        for original, candidates in sorted(groups.items(), key=lambda item: str(item[0]).lower())
    ]


def resolve_conflict(group: ConflictGroup, chosen: Path, local_root: str) -> tuple[bool, str]:
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
        return True, ""
    except OSError as exc:
        return False, str(exc)
    finally:
        if temporary is not None:
            temporary.unlink(missing_ok=True)
