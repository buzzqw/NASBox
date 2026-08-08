"""Startup update discovery and atomic replacement of the client package."""
from __future__ import annotations

import ast
import os
import shlex
import shutil
import subprocess
import tempfile
from dataclasses import dataclass, field
from pathlib import Path
from typing import Any

from . import rsync_ops
from .version import APP_VERSION


DEFAULT_REMOTE_UPDATE_PATH = ".nasbox-client-update"


def version_tuple(version: str) -> tuple[int, ...]:
    try:
        return tuple(int(part) for part in version.strip().split("."))
    except (AttributeError, ValueError):
        return (0,)


def _read_version(root: Path) -> str | None:
    version_file = root / "sync_client" / "version.py"
    try:
        tree = ast.parse(version_file.read_text(encoding="utf-8"), str(version_file))
    except (OSError, SyntaxError, UnicodeError):
        return None
    for node in tree.body:
        if not isinstance(node, ast.Assign):
            continue
        if not any(isinstance(target, ast.Name) and target.id == "APP_VERSION" for target in node.targets):
            continue
        if isinstance(node.value, ast.Constant) and isinstance(node.value.value, str):
            return node.value.value
    return None


def _bundle_root(path: Path) -> Path | None:
    """Accept either a client directory or a bundle containing ``client/``."""
    try:
        candidates = (path.expanduser().resolve(), (path / "client").expanduser().resolve())
    except OSError:
        return None
    for root in candidates:
        if (root / "main.py").is_file() and (root / "sync_client" / "version.py").is_file():
            return root
    return None


@dataclass
class UpdateCandidate:
    version: str
    origin: str
    source_root: Path | None = None
    remote_path: str = ""
    cfg: Any = None
    connection: rsync_ops.NasConnection | None = None
    _temporary: tempfile.TemporaryDirectory[str] | None = field(default=None, repr=False)

    def materialize(self) -> Path:
        if self.source_root is not None:
            return self.source_root
        if self.cfg is None or self.connection is None or not self.remote_path:
            raise RuntimeError("sorgente aggiornamento non disponibile")
        self._temporary = tempfile.TemporaryDirectory(prefix="nasbox-client-update-")
        destination = Path(self._temporary.name)
        user = self.cfg.get("nas_user")
        source = f"{user}@{self.connection.host}:{self.remote_path.rstrip('/')}/"
        command = [
            "rsync", "-az", "--no-owner", "--no-group", "--no-perms",
            "-e", rsync_ops._ssh_e_arg(self.cfg, self.connection),
            source, str(destination),
        ]
        try:
            result = subprocess.run(command, capture_output=True, text=True, timeout=120)
        except (OSError, subprocess.TimeoutExpired) as exc:
            self._temporary.cleanup()
            self._temporary = None
            raise RuntimeError(f"download aggiornamento fallito: {exc}") from exc
        if result.returncode != 0:
            self._temporary.cleanup()
            self._temporary = None
            detail = result.stderr.strip() or "errore rsync sconosciuto"
            raise RuntimeError(f"download aggiornamento fallito: {detail}")
        root = _bundle_root(destination)
        if root is None:
            self._temporary.cleanup()
            self._temporary = None
            raise RuntimeError("il bundle remoto non contiene un client valido")
        self.source_root = root
        return root

    def cleanup(self) -> None:
        if self._temporary is not None:
            self._temporary.cleanup()
            self._temporary = None


def _local_candidate(path: Path, current_root: Path) -> UpdateCandidate | None:
    root = _bundle_root(path)
    if root is None or root == current_root:
        return None
    version = _read_version(root)
    if version is None or version_tuple(version) <= version_tuple(APP_VERSION):
        return None
    return UpdateCandidate(version, str(path), source_root=root)


def _remote_update_path(cfg: Any) -> str:
    configured = str(cfg.get("client_update_remote_path") or DEFAULT_REMOTE_UPDATE_PATH).strip()
    if configured.startswith("/"):
        return configured
    prefix = str(cfg.get("remote_prefix") or "").rstrip("/")
    return f"{prefix}/{configured}" if prefix else configured


def _remote_version(cfg: Any, connection: rsync_ops.NasConnection, remote_root: str) -> str | None:
    user = cfg.get("nas_user")
    paths = [
        f"{remote_root.rstrip('/')}/sync_client/version.py",
        f"{remote_root.rstrip('/')}/client/sync_client/version.py",
    ]
    command = "for path in %s; do if [ -f \"$path\" ]; then cat \"$path\"; exit 0; fi; done; exit 1" % (
        " ".join(shlex.quote(path) for path in paths),
    )
    try:
        result = subprocess.run(
            ["ssh", *rsync_ops.ssh_opts(cfg, connection), f"{user}@{connection.host}", command],
            capture_output=True, text=True, timeout=15,
        )
    except (OSError, subprocess.TimeoutExpired):
        return None
    if result.returncode != 0:
        return None
    temporary = tempfile.TemporaryDirectory(prefix="nasbox-version-")
    try:
        version_file = Path(temporary.name) / "sync_client" / "version.py"
        version_file.parent.mkdir()
        version_file.write_text(result.stdout, encoding="utf-8")
        return _read_version(Path(temporary.name))
    except (OSError, UnicodeError):
        return None
    finally:
        temporary.cleanup()


def find_update(cfg: Any, current_root: Path, startup_path: str) -> UpdateCandidate | None:
    """Find the newest candidate from local startup paths or the configured NAS."""
    current_root = current_root.expanduser().resolve()
    local_paths: list[Path] = []
    for raw in (
        os.environ.get("NASBOX_CLIENT_UPDATE_PATH", ""),
        str(cfg.get("client_update_path") or ""),
    ):
        if raw.strip():
            local_paths.append(Path(raw))
    try:
        startup_dir = Path(startup_path).expanduser().resolve().parent
        local_paths.extend((startup_dir / "client-update", current_root.parent / "client-update", current_root / ".update"))
    except OSError:
        pass

    candidates = [candidate for path in local_paths if (candidate := _local_candidate(path, current_root))]

    if cfg.get("nas_user") and cfg.get("remote_prefix") and (
        cfg.get("nas_lan") or cfg.get("nas_wan") or cfg.get("jump_host")
    ):
        try:
            connection = rsync_ops.resolve_connection(cfg)
        except (TypeError, ValueError, OSError):
            connection = None
        if connection is not None:
            remote_path = _remote_update_path(cfg)
            remote_version = _remote_version(cfg, connection, remote_path)
            if remote_version and version_tuple(remote_version) > version_tuple(APP_VERSION):
                candidates.append(UpdateCandidate(
                    remote_version, f"NAS:{remote_path}",
                    remote_path=remote_path, cfg=cfg, connection=connection,
                ))

    return max(candidates, key=lambda candidate: version_tuple(candidate.version), default=None)


def install_update(source_root: Path, destination_root: Path) -> None:
    """Replace the client directory atomically, restoring it if staging fails."""
    source_root = source_root.expanduser().resolve()
    destination_root = destination_root.expanduser().resolve()
    if source_root == destination_root or source_root in destination_root.parents:
        raise RuntimeError("sorgente e destinazione dell'aggiornamento coincidono")
    if _bundle_root(source_root) != source_root:
        raise RuntimeError("bundle client non valido")

    staging = Path(tempfile.mkdtemp(prefix=f".{destination_root.name}.update-", dir=destination_root.parent))
    shutil.rmtree(staging)
    backup = destination_root.parent / f".{destination_root.name}.backup-{os.getpid()}"
    try:
        shutil.copytree(source_root, staging, symlinks=False)
        if backup.exists():
            shutil.rmtree(backup)
        destination_root.rename(backup)
        try:
            staging.rename(destination_root)
        except OSError:
            backup.rename(destination_root)
            raise
        shutil.rmtree(backup)
    except Exception:
        if staging.exists():
            shutil.rmtree(staging, ignore_errors=True)
        raise
