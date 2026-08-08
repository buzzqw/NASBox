"""Local repository and filesystem safety checks.

The marker lives outside NASBox so it cannot be copied, deleted, or replaced by
rsync. It records the device containing the selected local root; if a mount is
removed and the empty mountpoint is still present, the device number changes
and destructive synchronization is refused.
"""
from __future__ import annotations

import json
import os
from pathlib import Path

from . import paths


class RepositorySafetyError(RuntimeError):
    """A repository precondition is not safe for a transfer."""


def _read_marker() -> dict:
    marker = paths.repository_marker_file()
    try:
        value = json.loads(marker.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as exc:
        raise RepositorySafetyError(f"marker locale assente o non valido: {marker}") from exc
    if not isinstance(value, dict):
        raise RepositorySafetyError(f"marker locale non valido: {marker}")
    return value


def initialize_local_root(local_root: str, repository_id: str = "") -> None:
    """Record the selected path and its filesystem device after explicit setup."""
    root = Path(local_root).expanduser().resolve()
    try:
        stat = root.stat()
    except OSError as exc:
        raise RepositorySafetyError(f"cartella locale non accessibile: {root}") from exc
    if not root.is_dir():
        raise RepositorySafetyError(f"il percorso locale non è una cartella: {root}")

    marker = paths.repository_marker_file()
    paths.ensure_dirs()
    value = {
        "version": 1,
        "local_root": str(root),
        "st_dev": stat.st_dev,
        "repository_id": repository_id or "",
    }
    temporary = marker.with_suffix(".json.tmp")
    temporary.write_text(json.dumps(value, indent=2) + "\n", encoding="utf-8")
    try:
        os.chmod(temporary, 0o600)
    except OSError:
        pass
    temporary.replace(marker)


def bind_repository(local_root: str, repository_id: str) -> None:
    """Bind a previously selected local filesystem to the NAS repository."""
    if not repository_id:
        raise RepositorySafetyError("il NAS non ha fornito un ID repository valido")
    marker = paths.repository_marker_file()
    if not marker.exists():
        initialize_local_root(local_root, repository_id)
        return

    value = _read_marker()
    existing_id = str(value.get("repository_id") or "")
    if existing_id and existing_id != repository_id:
        raise RepositorySafetyError(
            "la cartella locale è associata a un repository NASBox diverso"
        )
    value["repository_id"] = repository_id
    marker.write_text(json.dumps(value, indent=2) + "\n", encoding="utf-8")
    try:
        os.chmod(marker, 0o600)
    except OSError:
        pass


def validate_local_root(local_root: str, repository_id: str = "", destructive: bool = False) -> None:
    """Validate path, device and repository identity before a transfer."""
    root = Path(local_root).expanduser().resolve()
    if not root.is_dir():
        raise RepositorySafetyError(f"cartella locale non accessibile: {root}")
    try:
        current_dev = root.stat().st_dev
    except OSError as exc:
        raise RepositorySafetyError(f"impossibile leggere il filesystem locale: {root}") from exc

    try:
        value = _read_marker()
    except RepositorySafetyError:
        # A never-initialized local_root (no repository_id known yet either) has
        # legitimately never had a marker -- that's the first-run bootstrap case,
        # not a safety problem. But once a repository_id is known, this local_root
        # was bound before and should have a marker; one going missing on its own
        # (independent of the mount itself, e.g. lost state dir) must not be read
        # as "nothing to check" -- that would silently skip the device/mount check
        # this function exists for, even for ordinary non-destructive syncs.
        if destructive or repository_id:
            raise
        return

    if value.get("local_root") != str(root):
        raise RepositorySafetyError("il percorso locale è cambiato senza una nuova verifica")
    if value.get("st_dev") != current_dev:
        raise RepositorySafetyError(
            "il filesystem della cartella locale è cambiato o il volume non è montato"
        )
    marker_id = str(value.get("repository_id") or "")
    if repository_id and marker_id and marker_id != repository_id:
        raise RepositorySafetyError("ID repository locale e NAS non coincidono")
    if destructive and not marker_id:
        raise RepositorySafetyError("repository locale non associato al NAS")
