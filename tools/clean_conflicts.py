#!/usr/bin/env python3
"""Archive stale NASBox conflict copies instead of letting them accumulate.

Older client versions (or folders that were synced before build/VCS excludes
were configured) can leave dozens of ``file (conflitto da ...)`` copies in the
tree. Removing only the local copies is not enough when ``delete_enabled`` is
off: the next pull downloads them again from the NAS.

This tool:
  1. finds every conflict copy under the local root;
  2. moves the matching NAS files into the NAS trash/history, so a later pull
     cannot recreate them;
  3. moves the local copies into a timestamped archive outside the synced tree.

Nothing is deleted. The default is a dry run; pass ``--apply`` to change files.
Stop or pause the NASBox client first: a running client may re-upload a copy
while the cleanup is in progress, and it holds the NAS transfer lease.
"""
from __future__ import annotations

import argparse
import os
import shutil
import sys
from collections import Counter
from datetime import datetime, timezone
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(REPO_ROOT / "client"))

from sync_client import conflicts, paths, rsync_ops  # noqa: E402
from sync_client.config import Config  # noqa: E402
from sync_client.reconcile import RemoteKind  # noqa: E402
from sync_client.sync_state import SyncStateStore  # noqa: E402


def find_conflict_copies(root: Path) -> list[Path]:
    copies: list[Path] = []
    for dirpath, dirnames, filenames in os.walk(root):
        dirnames[:] = sorted(name for name in dirnames if name not in conflicts.INTERNAL_DIRS)
        for name in filenames:
            if conflicts.CONFLICT_RE.match(name):
                copies.append(Path(dirpath, name))
    return sorted(copies)


def archive_local(copy: Path, root: Path, archive_root: Path) -> Path:
    relative = copy.relative_to(root)
    destination = archive_root / relative
    counter = 1
    while destination.exists():
        counter += 1
        destination = archive_root / f"{relative}.{counter}"
    destination.parent.mkdir(parents=True, exist_ok=True)
    shutil.move(str(copy), str(destination))
    return destination


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument(
        "--root", default="", help="NASBox local folder (default: the configured local_root)",
    )
    parser.add_argument(
        "--archive-dir", default="",
        help="where to move local copies (default: <state dir>/conflicts-archive/<timestamp>)",
    )
    parser.add_argument(
        "--local-only", action="store_true",
        help="only archive local copies; do not also move the NAS versions to history",
    )
    parser.add_argument(
        "--apply", action="store_true",
        help="actually move files (default: list what would be moved)",
    )
    args = parser.parse_args(argv)

    cfg = Config()
    root = Path(args.root or cfg.local_root()).expanduser().resolve()
    if not root.is_dir():
        print(f"errore: cartella locale non valida: {root}", file=sys.stderr)
        return 2

    copies = find_conflict_copies(root)
    if not copies:
        print(f"nessuna copia di conflitto in {root}")
        return 0

    per_directory = Counter(str(copy.relative_to(root).parts[0]) for copy in copies)
    print(f"{len(copies)} copie di conflitto in {root}")
    for name, count in per_directory.most_common():
        print(f"  {count:5d}  {name}")

    stamp = datetime.now(timezone.utc).strftime("%Y%m%dT%H%M%SZ")
    archive_root = Path(args.archive_dir).expanduser() if args.archive_dir else (
        paths.state_dir() / "conflicts-archive" / stamp
    )
    print(f"archivio locale: {archive_root}")

    if not args.apply:
        print("\ndry run: nessun file modificato. Rilancia con --apply per procedere.")
        return 0

    remote_failures: dict[str, str] = {}
    if not args.local_only:
        conn = rsync_ops.resolve_connection(cfg)
        if conn is None:
            print(
                "errore: NAS non raggiungibile. Le copie locali non vengono toccate, "
                "altrimenti il prossimo pull le riscaricherebbe. Usa --local-only per "
                "procedere comunque.",
                file=sys.stderr,
            )
            return 3
        relative_paths = [str(copy.relative_to(root)) for copy in copies]
        states = rsync_ops.remote_file_states(cfg, conn, relative_paths, compact=False)
        if states is None:
            print("errore: impossibile leggere lo stato dei file sul NAS", file=sys.stderr)
            return 3
        remote_copies = [path for path in relative_paths if states[path].kind == RemoteKind.FILE]
        if remote_copies:
            results = rsync_ops.browse_delete_many(
                cfg, conn, remote_copies, SyncStateStore(cfg).device_id(),
            )
            for path, ok, detail in results:
                if not ok:
                    remote_failures[path] = detail or "cancellazione NAS fallita"
            print(f"NAS: {len(remote_copies) - len(remote_failures)} copie spostate nello storico")
            for path, detail in sorted(remote_failures.items()):
                print(f"  NON rimossa dal NAS: {path}: {detail}", file=sys.stderr)
        else:
            print("NAS: nessuna copia corrispondente da spostare nello storico")

    moved = 0
    for copy in copies:
        relative = str(copy.relative_to(root))
        if relative in remote_failures:
            continue
        if not copy.exists():
            continue
        archive_local(copy, root, archive_root)
        moved += 1

    print(f"archiviate {moved} copie locali in {archive_root}")
    if remote_failures:
        print(
            f"{len(remote_failures)} copie locali lasciate al loro posto perché "
            "la copia sul NAS non è stata rimossa.",
            file=sys.stderr,
        )
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
