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

# RETIRED 2026-11-25: the Python client is no longer distributed. Every PC runs
# the Go client (client-go), which has its own channel and publisher
# (tools/publish_go_client.py). This script only builds a bundle for archival
# or emergency use and refuses to publish without --i-know-it-is-retired.
import sys as _sys

import argparse
import gzip
import hashlib
import json
import os
import shlex
import shutil
import subprocess
import sys
import tarfile
import tempfile
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parents[1]
CLIENT_DIR = REPO_ROOT / "client"
SERVER_SCRIPT = REPO_ROOT / "server" / "sync-daemon-server.sh"
SKIP_DIRS = {"__pycache__", ".pytest_cache", ".mypy_cache", ".ruff_cache"}

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
    # mtime=0 in the gzip header and a fixed tar format/sort order: identical
    # content -> identical sha256, no matter which machine (and which Python
    # version) builds it. Without the explicit ordering the members followed the
    # filesystem order, so two checkouts of the same commit produced different
    # archives (verified on the NAS vs this PC).
    with open(archive, "wb") as raw:
        with gzip.GzipFile(filename="", mode="wb", fileobj=raw, mtime=0) as compressed:
            with tarfile.open(fileobj=compressed, mode="w", format=tarfile.GNU_FORMAT) as tar:
                for dirpath, dirnames, filenames in os.walk(CLIENT_DIR):
                    dirnames[:] = sorted(name for name in dirnames if name not in SKIP_DIRS)
                    relative = os.path.relpath(dirpath, CLIENT_DIR)
                    base = "client" if relative == "." else os.path.join("client", relative)
                    tar.add(dirpath, arcname=base, recursive=False, filter=_normalise_member)
                    for name in sorted(filenames):
                        tar.add(
                            os.path.join(dirpath, name),
                            arcname=os.path.join(base, name),
                            recursive=False,
                            filter=_normalise_member,
                        )
    return archive


def _normalise_member(info: tarfile.TarInfo) -> tarfile.TarInfo | None:
    if info.name.endswith((".pyc", ".pyo")):
        return None
    # Normalise ownership and timestamps: without this the digest depended on
    # each file's mtime, so two checkouts of the same commit produced different
    # archives (verified: same commit on two machines -> different sha256).
    info.uid = 0
    info.gid = 0
    info.uname = ""
    info.gname = ""
    info.mtime = 0
    # Only the exec bit is meaningful content-wise; the rest comes from umask.
    info.mode = 0o755 if info.mode & 0o100 else 0o644
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
        # Same hardened extraction the client uses (rejects absolute paths, `..`,
        # links and device files) and it works on Python < 3.12 too, which is
        # what the NAS itself runs.
        updater._safe_extract(archive, root)
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


def _publish_local(directory: Path, destination: Path) -> None:
    """Copy the built files into a local update folder (e.g. on the NAS itself)."""
    destination.mkdir(parents=True, exist_ok=True)
    shutil.copy2(directory / updater.MANIFEST_NAME, destination / updater.MANIFEST_NAME)
    for archive in directory.glob("client-*.tar.gz"):
        shutil.copy2(archive, destination / archive.name)
    print(f"✓ pubblicato in {destination}")


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
    # rsync won't create a missing directory chain on the destination: make the
    # update folder first, so the very first publish works too.
    mkdir = subprocess.run(
        ["ssh", *rsync_ops.ssh_opts(cfg, connection), f"{user}@{connection.host}",
         f"mkdir -p {shlex.quote(remote_root.rstrip('/'))}"],
        capture_output=True, text=True, timeout=30,
    )
    if mkdir.returncode != 0:
        raise SystemExit(f"impossibile creare {remote_root}: {mkdir.stderr.strip() or mkdir.returncode}")
    print(f"→ carico su {user}@{connection.host}:{remote_root}")
    result = subprocess.run(command, capture_output=True, text=True, timeout=600)
    if result.returncode != 0:
        raise SystemExit(f"upload fallito: {result.stderr.strip() or result.returncode}")


def _refuse_when_retired() -> None:
    if "--i-know-it-is-retired" in _sys.argv:
        _sys.argv.remove("--i-know-it-is-retired")
        return
    if "--dest-dir" in _sys.argv or "--upload" in _sys.argv:
        _sys.exit(
            "Il client Python e' ritirato (2026-11-25): usa tools/publish_go_client.py. "
            "Per pubblicare comunque aggiungi --i-know-it-is-retired."
        )


def main() -> int:
    _refuse_when_retired()
    parser = argparse.ArgumentParser(description="Pubblica un client NASBox per l'auto-aggiornamento.")
    parser.add_argument("--out", default=str(REPO_ROOT / "dist-client-update"), help="cartella di output")
    parser.add_argument("--channel", default="stable", help="canale pubblicato nel manifest")
    parser.add_argument("--notes", default="", help="note di rilascio mostrate nel manifest")
    parser.add_argument("--upload", action="store_true", help="carica il risultato sul NAS via SSH")
    parser.add_argument(
        "--dest-dir", default="",
        help="copia il risultato in questa cartella locale (es. quando si pubblica dal NAS stesso)",
    )
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

    if args.dest_dir:
        _publish_local(destination, Path(args.dest_dir).expanduser())
    elif args.upload:
        from sync_client import config as config_module

        cfg = config_module.shared()
        if not (cfg.get("nas_user") and (cfg.get("nas_lan") or cfg.get("nas_wan"))):
            raise SystemExit("configurazione NAS assente: imposta nas_user e nas_lan/nas_wan nel client")
        _upload(destination, cfg)
    else:
        print("(solo build: usa --upload o --dest-dir per pubblicare)")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
