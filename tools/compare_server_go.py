#!/usr/bin/env python3
"""Confronta i comandi read-only del demone bash con quelli del demone Go.

Da eseguire sul NAS (i percorsi di stato devono essere gli stessi):

    python3 tools/compare_server_go.py \
      --bash /volume1/Varie/sync-daemon/server/sync-daemon-server.sh \
      --go   /volume1/Varie/nasbox-server-m0/nasbox-server \
      --config /volume1/Varie/sync-daemon/server/server.conf

Le righe con valori intrinsecamente dinamici (timestamp, contatori kernel,
spazio disco in movimento) e l'ordine delle righe dei comandi a map vengono
normalizzati: conta che i due demoni producano gli stessi *dati*, non gli
stessi byte in presenza di valori che cambiano da un secondo all'altro.
"""
from __future__ import annotations

import argparse
import subprocess
import sys
from pathlib import Path

DYNAMIC_KEYS = (
    "COLLECTED_AT_EPOCH", "UPTIME_SECONDS", "LOAD_1", "LOAD_5", "LOAD_15",
    "CPU_TOTAL_TICKS", "CPU_IDLE_TICKS", "CPU_IOWAIT_TICKS", "MEM_AVAILABLE_BYTES",
    "SWAP_FREE_BYTES", "DISK_USED_BYTES", "DISK_AVAILABLE_BYTES", "DISK_READ_BYTES",
    "DISK_WRITE_BYTES", "NET_RX_BYTES", "NET_TX_BYTES", "NET_RX_PACKETS",
    "NET_TX_PACKETS", "SHARE_USED_BYTES", "SHARE_AVAILABLE_BYTES",
    "SYNC_LOCK_AGE_SECONDS", "TRASH_USED_BYTES", "TRASH_FILES", "TRASH_USAGE_AT",
)

COMMANDS = [
    ("print-config", ["--print-config"], False),
    ("capabilities", ["--capabilities"], False),
    ("status", ["--status"], False),
    ("journal-status", ["--journal-status"], False),
    ("diagnostics", ["--diagnostics"], False),
    ("devices", ["--devices"], False),
    ("metrics", ["--metrics"], False),
    ("manifest-export", ["--manifest-export"], True),
    ("manifest-export-delta", ["--manifest-export-delta", "0"], True),
    ("history-list", ["--history-list"], True),
    ("browse-list", ["--browse-list", ""], True),
]


def run(binary: str, config: str, args: list[str]) -> bytes:
    cmd = [binary]
    if binary.endswith("-server") or "nasbox-server" in binary:
        cmd += ["-c", config]
    cmd += args
    return subprocess.run(cmd, stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=300).stdout


def normalize(data: bytes, sort_lines: bool) -> list[bytes]:
    if b"\x00" in data:
        fields = [part for part in data.split(b"\x00") if part]
        return sorted(fields)
    lines = []
    for line in data.decode("utf-8", "replace").splitlines():
        if line.startswith("Script:"):
            continue
        key = line.split("=", 1)[0]
        if key in DYNAMIC_KEYS:
            continue
        lines.append(line.encode("utf-8"))
    return sorted(lines) if sort_lines else lines


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--bash", required=True)
    parser.add_argument("--go", required=True)
    parser.add_argument("--config", required=True)
    args = parser.parse_args()
    failed = 0
    for name, argv, sort_lines in COMMANDS:
        try:
            a = normalize(run(args.bash, args.config, argv), sort_lines)
            b = normalize(run(args.go, args.config, argv), sort_lines)
        except subprocess.TimeoutExpired:
            print(f"FAIL {name}: timeout")
            failed += 1
            continue
        if a == b:
            print(f"PASS {name} ({len(a)} righe/campi)")
            continue
        failed += 1
        print(f"FAIL {name}: bash={len(a)} go={len(b)}")
        only_a = sorted(set(a) - set(b))
        only_b = sorted(set(b) - set(a))
        for line in only_a[:3]:
            print("  solo bash:", line[:160].decode("utf-8", "replace"))
        for line in only_b[:3]:
            print("  solo go:  ", line[:160].decode("utf-8", "replace"))
    print(f"\n{len(COMMANDS) - failed}/{len(COMMANDS)} comandi uguali")
    return 1 if failed else 0


if __name__ == "__main__":
    sys.exit(main())
