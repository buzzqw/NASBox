#!/usr/bin/env python3
"""Sign the Go update manifest with the offline Ed25519 release key."""
from __future__ import annotations

import argparse
import base64
from pathlib import Path

from cryptography.hazmat.primitives import serialization
from cryptography.hazmat.primitives.asymmetric.ed25519 import Ed25519PrivateKey


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--manifest", required=True, help="manifest-go.json")
    parser.add_argument("--key", required=True, help="Ed25519 private key PEM")
    parser.add_argument("--output", default="", help="firma (default: <manifest>.sig)")
    args = parser.parse_args()

    manifest = Path(args.manifest).expanduser().resolve()
    raw = manifest.read_bytes()
    key = serialization.load_pem_private_key(Path(args.key).expanduser().read_bytes(), password=None)
    if not isinstance(key, Ed25519PrivateKey):
        raise SystemExit("la chiave non è Ed25519")
    signature = base64.b64encode(key.sign(raw)) + b"\n"
    output = Path(args.output).expanduser().resolve() if args.output else Path(str(manifest) + ".sig")
    output.write_bytes(signature)
    output.chmod(0o644)
    print(f"firmato: {manifest} -> {output}")


if __name__ == "__main__":
    main()
