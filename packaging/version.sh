#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

python3 - "$ROOT_DIR/client/sync_client/version.py" <<'PY'
import ast
import pathlib
import sys

path = pathlib.Path(sys.argv[1])
tree = ast.parse(path.read_text(encoding="utf-8"), str(path))
for node in tree.body:
    if not isinstance(node, ast.Assign):
        continue
    if any(isinstance(target, ast.Name) and target.id == "APP_VERSION" for target in node.targets):
        if isinstance(node.value, ast.Constant) and isinstance(node.value.value, str):
            print(node.value.value)
            raise SystemExit(0)
raise SystemExit(f"APP_VERSION not found in {path}")
PY
