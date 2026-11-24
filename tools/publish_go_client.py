#!/usr/bin/env python3
"""Publish the Go client to its own update channel (never touches the Python one)."""
from __future__ import annotations

import argparse
import hashlib
import json
import re
import shutil
from datetime import datetime, timezone
from pathlib import Path


def read_version(source: Path) -> str:
    text = source.read_text(encoding="utf-8")
    match = re.search(r'AppVersion\s*=\s*"([^"]+)"', text)
    if not match:
        raise SystemExit(f"versione Go non trovata in {source}")
    return match.group(1)


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--binary", required=True, help="binario Go da pubblicare")
    parser.add_argument("--dest-dir", required=True)
    parser.add_argument("--notes", default="")
    parser.add_argument("--target", default="linux-amd64", help="es. linux-amd64 o linux-arm64")
    parser.add_argument("--requires-server", default="3.26.0")
    parser.add_argument(
        "--version-file",
        default="client-go/internal/version/version.go",
    )
    args = parser.parse_args()

    repo = Path(__file__).resolve().parents[1]
    version = read_version(repo / args.version_file)
    binary = Path(args.binary).expanduser().resolve()
    if not binary.is_file():
        raise SystemExit(f"binario non trovato: {binary}")
    content = binary.read_bytes()
    digest = hashlib.sha256(content).hexdigest()

    dest = Path(args.dest_dir).expanduser().resolve()
    dest.mkdir(parents=True, exist_ok=True)
    artifact = f"nasbox-go-{version}"
    target = dest / artifact
    tmp = dest / (artifact + ".tmp")
    shutil.copy2(binary, tmp)
    tmp.replace(target)
    target.chmod(0o755)

    manifest = {
        "version": version,
        "channel": "go-stable",
        "artifact": artifact,
        "sha256": digest,
        "size": len(content),
        "notes": args.notes,
        "target": args.target,
        "requires_server": args.requires_server,
        "published_at": datetime.now(timezone.utc).isoformat(timespec="seconds"),
    }
    manifest_tmp = dest / "manifest-go.json.tmp"
    manifest_tmp.write_text(
        json.dumps(manifest, ensure_ascii=False, indent=2) + "\n", encoding="utf-8"
    )
    manifest_tmp.replace(dest / "manifest-go.json")

    print(f"versione : {version} (canale go-stable)")
    print(f"archivio : {artifact}  {len(content)} byte")
    print(f"sha256   : {digest}")
    print(f"cartella : {dest}")


if __name__ == "__main__":
    main()
