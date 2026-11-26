#!/usr/bin/env bash
# Stampa la versione del client Go (unica fonte: client-go/internal/version/version.go).
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

python3 - "$ROOT_DIR/client-go/internal/version/version.go" <<'PY'
import pathlib
import re
import sys

text = pathlib.Path(sys.argv[1]).read_text(encoding="utf-8")
match = re.search(r'AppVersion\s*=\s*"([^"]+)"', text)
if not match:
    raise SystemExit(f"AppVersion non trovata in {sys.argv[1]}")
print(match.group(1))
PY
