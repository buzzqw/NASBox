"""Client update discovery, verified download and atomic replacement.

Two ways an update can be published on the NAS (see ``tools/publish_client.py``):

* **manifest + single archive** (preferred): ``manifest.json`` next to
  ``client-<version>.tar.gz``. One small SSH command reads the manifest, the
  archive is verified against its SHA-256 before being extracted, and the whole
  thing is one immutable artifact instead of a live tree.
* **legacy bundle** (still supported, so an older publisher keeps working): a
  plain directory whose ``sync_client/version.py`` is read to discover the
  version and which is rsynced over as a tree.

Whatever the origin, the replacement itself is transactional: the old client is
kept as a backup, and it is deleted only once the new one has started
successfully (``confirm_started_ok``). If the new build never gets that far,
``rollback_if_needed`` puts the previous one back after two failed starts --
the failure mode that used to leave the user with an app that simply vanished.
"""
from __future__ import annotations

import ast
import hashlib
import json
import os
import shlex
import shutil
import subprocess
import sys
import tarfile
import tempfile
import time
from dataclasses import dataclass, field
from pathlib import Path
from typing import Any

from . import rsync_ops
from .version import APP_VERSION


DEFAULT_REMOTE_UPDATE_PATH = ".nasbox-client-update"
MANIFEST_NAME = "manifest.json"
#: How many times the freshly installed client may start before the update is
#: considered broken and the previous version is restored. The first start is
#: the update itself; the second one is the retry.
MAX_START_ATTEMPTS = 2
DOWNLOAD_TIMEOUT_SECONDS = 600


def version_tuple(version: str) -> tuple[int, ...]:
    """Compare numeric components, ignoring suffixes such as rc1/dev."""
    if not version:
        return (0,)
    parts: list[int] = []
    for part in str(version).strip().split("."):
        digits = ""
        for char in part:
            if not char.isdigit():
                break
            digits += char
        if digits:
            parts.append(int(digits))
        else:
            break
    return tuple(parts) or (0,)


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


# --- manifest ---------------------------------------------------------------


@dataclass
class Manifest:
    """What the NAS says about the published client build."""

    version: str
    artifact: str
    sha256: str
    size: int = 0
    channel: str = "stable"
    notes: str = ""
    published_at: str = ""
    requires_server: str = ""

    def to_json(self) -> str:
        return json.dumps({
            "version": self.version,
            "artifact": self.artifact,
            "sha256": self.sha256,
            "size": self.size,
            "channel": self.channel,
            "notes": self.notes,
            "published_at": self.published_at,
            "requires_server": self.requires_server,
        }, indent=2, ensure_ascii=False) + "\n"


def parse_manifest(text: str) -> Manifest | None:
    """Parse and validate a manifest; None when it isn't usable.

    A remote file is untrusted input: anything missing, mistyped or with a
    malformed digest is rejected instead of being half-applied.
    """
    try:
        data = json.loads(text)
    except (TypeError, ValueError):
        return None
    if not isinstance(data, dict):
        return None
    version = str(data.get("version") or "").strip()
    artifact = str(data.get("artifact") or "").strip()
    digest = str(data.get("sha256") or "").strip().lower()
    if not version or not artifact or len(digest) != 64 or any(c not in "0123456789abcdef" for c in digest):
        return None
    if "/" in artifact or artifact.startswith(".") or ".." in artifact:
        return None  # a name, not a path: never let it point outside the update dir
    try:
        size = int(data.get("size") or 0)
    except (TypeError, ValueError):
        size = 0
    return Manifest(
        version=version,
        artifact=artifact,
        sha256=digest,
        size=max(size, 0),
        channel=str(data.get("channel") or "stable"),
        notes=str(data.get("notes") or ""),
        published_at=str(data.get("published_at") or ""),
        requires_server=str(data.get("requires_server") or ""),
    )


def _sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as handle:
        for chunk in iter(lambda: handle.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def _safe_extract(archive: Path, destination: Path) -> None:
    """Extract an update archive, refusing anything that escapes ``destination``."""
    destination = destination.resolve()

    def _is_unsafe(member: tarfile.TarInfo) -> bool:
        name = Path(member.name)
        if name.is_absolute() or ".." in name.parts:
            return True
        if member.issym() or member.islnk() or member.isdev():
            return True
        return False

    with tarfile.open(archive) as tar:
        members = tar.getmembers()
        for member in members:
            if _is_unsafe(member):
                raise RuntimeError(f"archivio di aggiornamento non valido: {member.name}")
        try:
            tar.extractall(destination, members=members, filter="data")
        except TypeError:  # Python without the extraction filter (pre-3.12)
            tar.extractall(destination, members=members)


# --- candidates -------------------------------------------------------------


@dataclass
class UpdateCandidate:
    version: str
    origin: str
    source_root: Path | None = None
    remote_path: str = ""
    cfg: Any = None
    connection: rsync_ops.NasConnection | None = None
    manifest: Manifest | None = None
    _temporary: tempfile.TemporaryDirectory[str] | None = field(default=None, repr=False)

    def materialize(self) -> Path:
        if self.source_root is not None:
            return self.source_root
        if self.cfg is None or self.connection is None or not self.remote_path:
            raise RuntimeError("sorgente aggiornamento non disponibile")
        self._temporary = tempfile.TemporaryDirectory(prefix="nasbox-client-update-")
        destination = Path(self._temporary.name)
        try:
            if self.manifest is not None:
                archive = self._download_artifact(destination)
                root = self._unpack(archive, destination)
            else:
                self._download_tree(destination)
                root = _bundle_root(destination)
        except Exception:
            self.cleanup()
            raise
        if root is None:
            self.cleanup()
            raise RuntimeError("il bundle remoto non contiene un client valido")
        self.source_root = root
        return root

    def _ssh_target(self) -> str:
        user = self.cfg.get("nas_user")
        return f"{user}@{self.connection.host}"

    def _download_tree(self, destination: Path) -> None:
        source = f"{self._ssh_target()}:{self.remote_path.rstrip('/')}/"
        command = [
            "rsync", "-az", "--no-owner", "--no-group", "--no-perms",
            "-e", rsync_ops._ssh_e_arg(self.cfg, self.connection),
            source, str(destination),
        ]
        try:
            result = subprocess.run(command, capture_output=True, text=True, timeout=120)
        except (OSError, subprocess.TimeoutExpired) as exc:
            raise RuntimeError(f"download aggiornamento fallito: {exc}") from exc
        if result.returncode != 0:
            detail = result.stderr.strip() or "errore rsync sconosciuto"
            raise RuntimeError(f"download aggiornamento fallito: {detail}")

    def _download_artifact(self, destination: Path) -> Path:
        assert self.manifest is not None
        source = f"{self._ssh_target()}:{self.remote_path.rstrip('/')}/{self.manifest.artifact}"
        archive = destination / self.manifest.artifact
        command = [
            "rsync", "-az", "--no-owner", "--no-group", "--no-perms",
            "-e", rsync_ops._ssh_e_arg(self.cfg, self.connection),
            source, str(archive),
        ]
        try:
            result = subprocess.run(
                command, capture_output=True, text=True, timeout=DOWNLOAD_TIMEOUT_SECONDS,
            )
        except (OSError, subprocess.TimeoutExpired) as exc:
            raise RuntimeError(f"download aggiornamento fallito: {exc}") from exc
        if result.returncode != 0:
            detail = result.stderr.strip() or "errore rsync sconosciuto"
            raise RuntimeError(f"download aggiornamento fallito: {detail}")
        if not archive.is_file():
            raise RuntimeError("download aggiornamento fallito: archivio assente")
        actual = _sha256_file(archive)
        if actual != self.manifest.sha256:
            raise RuntimeError(
                "archivio di aggiornamento corrotto o manomesso "
                f"(sha256 {actual[:12]}… atteso {self.manifest.sha256[:12]}…)"
            )
        if self.manifest.size and archive.stat().st_size != self.manifest.size:
            raise RuntimeError("archivio di aggiornamento di dimensione inattesa")
        return archive

    def _unpack(self, archive: Path, destination: Path) -> Path | None:
        extracted = destination / "unpacked"
        extracted.mkdir()
        _safe_extract(archive, extracted)
        return _bundle_root(extracted)

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


def _remote_manifest(
    cfg: Any, connection: rsync_ops.NasConnection, remote_root: str,
) -> Manifest | None:
    """Read the published manifest with a single SSH round-trip."""
    user = cfg.get("nas_user")
    path = f"{remote_root.rstrip('/')}/{MANIFEST_NAME}"
    command = f"cat {shlex.quote(path)}"
    try:
        result = subprocess.run(
            ["ssh", *rsync_ops.ssh_opts(cfg, connection), f"{user}@{connection.host}", command],
            capture_output=True, text=True, timeout=15,
        )
    except (OSError, subprocess.TimeoutExpired):
        return None
    if result.returncode != 0:
        return None
    return parse_manifest(result.stdout)


def _server_version(cfg: Any, connection: rsync_ops.NasConnection) -> str | None:
    """Read VERSION from the NAS daemon's authoritative --print-config."""
    script = str(cfg.get("remote_server_script") or "").strip()
    if not script:
        return None
    ok, output, _error = rsync_ops.run_remote_script(
        cfg, connection, script, ["--print-config"], timeout=30,
    )
    if not ok:
        return None
    for line in output.splitlines():
        key, separator, value = line.partition("=")
        if separator and key.strip() == "VERSION":
            return value.strip()
    return None


def _remote_version(cfg: Any, connection: rsync_ops.NasConnection, remote_root: str) -> str | None:
    """Legacy discovery: ask the published tree for its version.py."""
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
    if os.environ.get("NASBOX_DISABLE_CLIENT_UPDATE") == "1":
        return None
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
            manifest = _remote_manifest(cfg, connection, remote_path)
            if manifest is not None:
                server_ok = True
                if manifest.requires_server:
                    server_version = _server_version(cfg, connection)
                    server_ok = not (
                        server_version
                        and version_tuple(server_version) < version_tuple(manifest.requires_server)
                    )
                    if not server_ok:
                        # Installing would produce a client that refuses every
                        # operation with SERVER_OUTDATED. Keep the working build.
                        pass
                if server_ok and version_tuple(manifest.version) > version_tuple(APP_VERSION):
                    candidates.append(UpdateCandidate(
                        manifest.version, f"NAS:{remote_path}",
                        remote_path=remote_path, cfg=cfg, connection=connection, manifest=manifest,
                    ))
            else:
                # No manifest published: fall back to the legacy tree layout so
                # an existing publishing setup keeps working.
                remote_version = _remote_version(cfg, connection, remote_path)
                if remote_version and version_tuple(remote_version) > version_tuple(APP_VERSION):
                    candidates.append(UpdateCandidate(
                        remote_version, f"NAS:{remote_path}",
                        remote_path=remote_path, cfg=cfg, connection=connection,
                    ))

    return max(candidates, key=lambda candidate: version_tuple(candidate.version), default=None)


def is_development_checkout(root: Path) -> bool:
    """True when ``root`` lives inside a git work tree.

    Installing an update there would overwrite the developer's checkout (and its
    uncommitted work), so automatic updates step aside and let git own the code.
    """
    try:
        root = root.expanduser().resolve()
    except OSError:
        return False
    return any((parent / ".git").exists() for parent in (root, *list(root.parents)[:3]))


def auto_update_allowed(cfg: Any, root: Path) -> bool:
    if os.environ.get("NASBOX_UPDATE_FORCE") == "1":
        return True
    if bool(cfg.get("client_update_allow_dev_tree")):
        return True
    return not is_development_checkout(root)


# --- install / rollback -----------------------------------------------------


def _pending_file(destination_root: Path) -> Path:
    return destination_root.parent / f".{destination_root.name}.update-pending.json"


def _write_pending(path: Path, payload: dict[str, Any]) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    temporary = path.with_suffix(path.suffix + ".tmp")
    temporary.write_text(json.dumps(payload, indent=2), encoding="utf-8")
    os.replace(temporary, path)


def _read_pending(path: Path) -> dict[str, Any] | None:
    try:
        return json.loads(path.read_text(encoding="utf-8"))
    except (OSError, ValueError):
        return None


def install_update(source_root: Path, destination_root: Path) -> None:
    """Replace the client directory, keeping the old one until it has started.

    Two renames (old aside, new in place) are unavoidable without a symlinked
    layout, but the old tree is no longer deleted straight away: it stays as the
    rollback target until ``confirm_started_ok``.
    """
    source_root = source_root.expanduser().resolve()
    destination_root = destination_root.expanduser().resolve()
    if source_root == destination_root or source_root in destination_root.parents:
        raise RuntimeError("sorgente e destinazione dell'aggiornamento coincidono")
    if _bundle_root(source_root) != source_root:
        raise RuntimeError("bundle client non valido")

    staging = Path(tempfile.mkdtemp(prefix=f".{destination_root.name}.update-", dir=destination_root.parent))
    shutil.rmtree(staging)
    backup = destination_root.parent / f".{destination_root.name}.backup"
    pending = _pending_file(destination_root)
    try:
        shutil.copytree(source_root, staging, symlinks=False)
        if (destination_root / "tests").is_dir() and not (staging / "tests").exists():
            shutil.copytree(destination_root / "tests", staging / "tests")
        if backup.exists():
            # A leftover from an update that never confirmed: the caller is
            # installing again, so that rollback target is stale.
            shutil.rmtree(backup)
        installed_version = _read_version(staging) or ""
        destination_root.rename(backup)
        try:
            staging.rename(destination_root)
        except OSError:
            backup.rename(destination_root)
            raise
        _write_pending(pending, {
            "backup": str(backup),
            "version": installed_version,
            "installed_at": time.strftime("%Y-%m-%d %H:%M:%S"),
            "attempts": 0,
        })
    except Exception:
        if staging.exists():
            shutil.rmtree(staging, ignore_errors=True)
        raise


def mark_start_attempt(destination_root: Path) -> int:
    """Count this start, so a build that never gets going is rolled back."""
    pending = _pending_file(destination_root)
    payload = _read_pending(pending)
    if payload is None:
        return 0
    attempts = int(payload.get("attempts") or 0) + 1
    payload["attempts"] = attempts
    try:
        _write_pending(pending, payload)
    except OSError:
        return attempts
    return attempts


def confirm_started_ok(destination_root: Path) -> bool:
    """The new build is running: drop the rollback target and the pending marker."""
    pending = _pending_file(destination_root)
    payload = _read_pending(pending)
    if payload is None:
        return False
    backup = Path(str(payload.get("backup") or ""))
    try:
        if backup.is_dir():
            shutil.rmtree(backup)
        pending.unlink(missing_ok=True)
    except OSError:
        return False
    return True


def rollback_if_needed(destination_root: Path) -> str | None:
    """Restore the previous client when the new one never confirmed a start.

    Returns the version that was restored, or None when nothing was rolled back.
    """
    pending = _pending_file(destination_root)
    payload = _read_pending(pending)
    if payload is None:
        return None
    attempts = int(payload.get("attempts") or 0)
    if attempts < MAX_START_ATTEMPTS:
        return None
    backup = Path(str(payload.get("backup") or ""))
    if not backup.is_dir():
        pending.unlink(missing_ok=True)
        return None
    # Never report the incoming version as "restored": that is the version we
    # just rolled back, not the one now running.
    restored_version = _read_version(backup) or "precedente"
    failed = destination_root.parent / f".{destination_root.name}.failed-{os.getpid()}"
    moved_broken = False
    try:
        if destination_root.exists():
            destination_root.rename(failed)
            moved_broken = True
        backup.rename(destination_root)
    except OSError:
        # The broken build must never be left occupying the canonical path, and
        # the backup must be kept so a later start can retry the rollback.
        if moved_broken and not destination_root.exists() and failed.is_dir():
            try:
                failed.rename(destination_root)
            except OSError:
                pass
        return None
    pending.unlink(missing_ok=True)
    if failed.is_dir():
        shutil.rmtree(failed, ignore_errors=True)
    return restored_version


def restart(current_root: Path) -> None:
    """Replace this process with the freshly installed client.

    os.execv() keeps the PID and the instance-lock fd, so the lock must be
    handed over explicitly (see instance_lock.prepare_reexec). Without that,
    the new image opened a second lock fd, flock() failed and the updated
    client exited immediately with "already running".
    """
    from . import instance_lock

    instance_lock.prepare_reexec()
    argv = [sys.executable]
    if not getattr(sys, "frozen", False):
        argv.append(str(current_root.expanduser().resolve() / "main.py"))
    argv.extend(sys.argv[1:])
    try:
        os.execv(sys.executable, argv)
    except OSError:
        instance_lock.cancel_reexec()
        raise
