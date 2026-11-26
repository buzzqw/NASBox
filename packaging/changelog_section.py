#!/usr/bin/env python3
"""Print the CHANGELOG section for one version, ready to use as release notes."""
from __future__ import annotations

import argparse
import pathlib
import sys

ROOT = pathlib.Path(__file__).resolve().parent.parent


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("version")
    parser.add_argument("--changelog", default=str(ROOT / "CHANGELOG.md"))
    args = parser.parse_args()

    lines = pathlib.Path(args.changelog).read_text(encoding="utf-8").splitlines()
    wanted = f"## {args.version} - "
    inside = False
    body: list[str] = []
    for line in lines:
        if line.startswith("## "):
            if inside:
                break
            inside = line.startswith(wanted)
            continue
        if inside:
            body.append(line)
    text = "\n".join(body).strip()
    if not text:
        text = f"Vedi [CHANGELOG.md](CHANGELOG.md) per la versione {args.version}."
    print(text)
    return 0


if __name__ == "__main__":
    sys.exit(main())
