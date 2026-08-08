"""Tiny display-formatting helpers shared across tabs (Trasferimenti, Storico)
so a file size reads the same way everywhere in the app."""
from __future__ import annotations


def human_size(n: float) -> str:
    size = float(n)
    for unit in ("B", "KB", "MB", "GB", "TB"):
        if size < 1024 or unit == "TB":
            return f"{size:.0f} {unit}" if unit == "B" else f"{size:.1f} {unit}"
        size /= 1024
    return f"{size:.1f} TB"
