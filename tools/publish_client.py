#!/usr/bin/env python3
"""Publish a client build for NASBox's self-update mechanism.

Produces, in one directory:

  manifest.json          version, channel, sha256/size of the archive, notes
  client-<version>.tar.gz

and (with ``--upload``) copies both to the NAS update folder read by the
clients (``remote_prefix`` + ``client_update_remote_path``, default
``.nasbox-client-update``).

The archive is verified before publishing: it is extracted into a temporary
directory and must contain a usable client bundle whose ``APP_VERSION``
matches the version written in the manifest -- a broken build never reaches
the NAS.

Examples:

    python3 tools/publish_client.py                       # build only, ./dist-client-update
    python3 tools/publish_client.py --notes "fix freeze"   # build with release notes
    python3 tools/publish_client.py --upload               # build + push to the NAS
"""
from __future__ import annotations

import argparse
import hashlib
import json
import shutil
import subprocess
import sys
import tarfile
import tempfile
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parents[1]
CLIENT_DIR = REPO_ROOT / "client"
SERVER_SCRIPT = REPO_ROOT / "server" / "sync-daemon-server.sh"

sys.path.insert(0, str(CLIENT_DIR))

from sync_client import rsync_ops  # noqa: E402  (needs the path above)
from sync_client import updater  # noqa: E402


def _client_version() -> str:
    version = updater._read_version(CLIENT_DIR)
    if not version:
        raise SystemExit(f"impossibile leggere APP_VERSION da {CLIENT_DIR / 'sync_client' / 'version.py'}")
    return version


def _server_version() -> str:
    try:
        for line in SERVER_SCRIPT.read_text(encoding="utf-8").splitlines():
            if line.startswith("VERSION="):
                return line.split("=", 1)[1].strip().strip('"').strip("'")
    except OSError:
        pass
    return ""


def _build_archive(destination: Path) -> Path:
    archive = destination / f"client-{_client_version()}.tar.gz"
    with tarfile.open(archive, "w:gz") as tar:
        tar.add(CLIENT_DIR, arcname="client", filter=_skip_junk)
    return archive


def _skip_junk(info: tarfile.TarInfo) -> tarfile.TarInfo | None:
    parts = Path(info.name).parts
    if any(part in {"__pycache__", ".pytest_cache", ".mypy_cache", ".ruff_cache"} for part in parts):
        return None
    if info.name.endswith((".pyc", ".pyo")):
        return None
    return info


def _sha256(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as handle:
        for chunk in iter(lambda: handle.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def _verify(archive: Path, expected_version: str) -> None:
    """Extract what we just built and check it really is that client version."""
    temporary = tempfile.TemporaryDirectory(prefix="nasbox-publish-check-")
    try:
        root = Path(temporary.name)
        with tarfile.open(archive) as tar:
            tar.extractall(root, filter="data")
        bundle = updater._bundle_root(root)
        if bundle is None:
            raise SystemExit("verifica fallita: l'archivio non contiene un client valido")
        found = updater._read_version(bundle)
        if found != expected_version:
            raise SystemExit(
                f"verifica fallita: l'archivio contiene la versione {found}, attesa {expected_version}"
            )
    finally:
        temporary.cleanup()


def _manifest(version: str, archive: Path, channel: str, notes: str) -> updater.Manifest:
    return updater.Manifest(
        version=version,
        artifact=archive.name,
        sha256=_sha256(archive),
        size=archive.stat().st_size,
        channel=channel,
        notes=notes,
        published_at=__import__("time").strftime("%Y-%m-%dT%H:%M:%SZ", __import__("time").gmtime()),
        requires_server=_server_version(),
    )


def _upload(directory: Path, cfg) -> None:
    connection = rsync_ops.resolve_connection(cfg)
    user = cfg.get("nas_user")
    remote_root = str(cfg.get("client_update_remote_path") or updater.DEFAULT_REMOTE_UPDATE_PATH).strip()
    if not remote_root.startswith("/"):
        prefix = str(cfg.get("remote_prefix") or "").rstrip("/")
        remote_root = f"{prefix}/{remote_root}" if prefix else remote_root
    command = [
        "rsync", "-az", "--no-owner", "--no-group", "--no-perms",
        "-e", rsync_ops._ssh_e_arg(cfg, connection),
        str(directory) + "/", f"{user}@{connection.host}:{remote_root.rstrip('/')}/",
    ]
    print(f"→ carico su {user}@{connection.host}:{remote_root}")
    result = subprocess.run(command, capture_output=True, text=True, timeout=600)
    if result.returncode != 0:
        raise SystemExit(f"upload fallito: {result.stderr.strip() or result.returncode}")


def main() -> int:
    parser = argparse.ArgumentParser(description="Pubblica un client NASBox per l'auto-aggiornamento.")
    parser.add_argument("--out", default=str(REPO_ROOT / "dist-client-update"), help="cartella di output")
    parser.add_argument("--channel", default="stable", help="canale pubblicato nel manifest")
    parser.add_argument("--notes", default="", help="note di rilascio mostrate nel manifest")
    parser.add_argument("--upload", action="store_true", help="carica il risultato sul NAS")
    parser.add_argument("--keep", action="store_true", help="tieni le versioni precedenti (default: rimuove gli archivi vecchi)")
    args = parser.parse_args()

    version = _client_version()
    destination = Path(args.out).expanduser().resolve()
    destination.mkdir(parents=True, exist_ok=True)

    archive = _build_archive(destination)
    _verify(archive, version)
    manifest = _manifest(version, archive, args.channel, args.notes)
    (destination / updater.MANIFEST_NAME).write_text(manifest.to_json(), encoding="utf-8")

    if not args.keep:
        for old in destination.glob("client-*.tar.gz"):
            if old != archive:
                old.unlink()

    print(f"versione   : {version} (canale {manifest.channel})")
    print(f"archivio   : {archive.name}  {manifest.size} byte")
    print(f"sha256     : {manifest.sha256}")
    print(f"server     : {manifest.requires_server or 'n/d'}")
    print(f"cartella   : {destination}")

    if args.upload:
        from sync_client import config as config_module

        cfg = config_module.shared()
        if not (cfg.get("nas_user") and (cfg.get("nas_lan") or cfg.get("nas_wan"))):
            raise SystemExit("configurazione NAS assente: imposta nas_user e nas_lan/nas_wan nel client")
        _upload(destination, cfg)
        print("✓ pubblicato sul NAS")
    else:
        print("(solo build: usa --upload per pubblicare sul NAS)")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
