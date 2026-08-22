"""Persistent common-base metadata used to detect cross-client conflicts."""
from __future__ import annotations

import hashlib
import os
import sqlite3
import stat
import threading
import time
import uuid
from concurrent.futures import ThreadPoolExecutor
from contextlib import contextmanager
from dataclasses import dataclass
from pathlib import Path
from typing import Callable, Optional

from . import paths
from .config import Config


# A complete reconciliation can hash tens of thousands of files. Keep the
# throughput benefit of parallel reads without allowing that exceptional path to
# occupy every CPU core and make the desktop unresponsive.
HASH_MAX_WORKERS = min(8, max(1, (os.cpu_count() or 1) // 2))


@dataclass(frozen=True)
class Fingerprint:
    digest: str
    size: int
    mtime_ns: int

    @property
    def is_tombstone(self) -> bool:
        return self.digest == ""


class SyncStateStore:
    """Small SQLite store for paths confirmed identical after a transfer.

    A row is only written after rsync has reported that path complete. Missing
    rows are intentionally treated as unknown, never as proof that a side can
    safely overwrite an existing file.
    """

    def __init__(self, cfg: Config) -> None:
        self.cfg = cfg
        self._lock = threading.RLock()
        paths.ensure_dirs()
        self._db_path = paths.sync_state_db_file()
        self._init_db()

    def _connect(self) -> sqlite3.Connection:
        conn = sqlite3.connect(self._db_path, timeout=10)
        conn.execute("PRAGMA journal_mode=WAL")
        conn.execute("PRAGMA busy_timeout=10000")
        return conn

    @contextmanager
    def _connection(self):
        conn = self._connect()
        try:
            yield conn
            conn.commit()
        finally:
            conn.close()

    def _init_db(self) -> None:
        with self._connection() as conn:
            conn.executescript(
                """
                CREATE TABLE IF NOT EXISTS meta (
                    key TEXT PRIMARY KEY,
                    value TEXT NOT NULL
                );
                CREATE TABLE IF NOT EXISTS entry (
                    repository TEXT NOT NULL,
                    path TEXT NOT NULL,
                    digest TEXT,
                    size INTEGER,
                    mtime_ns INTEGER,
                    synced_at REAL NOT NULL,
                    PRIMARY KEY (repository, path)
                );
                CREATE TABLE IF NOT EXISTS pending (
                    repository TEXT NOT NULL,
                    path TEXT NOT NULL,
                    queued_at REAL NOT NULL,
                    PRIMARY KEY (repository, path)
                );
                """
            )
            repository_id = str(self.cfg.get("repository_id") or "")
            if repository_id:
                legacy = self._legacy_repository()
                current = self._repository()
                migration_key = f"repository-migration:{legacy}"
                migrated = conn.execute(
                    "SELECT value FROM meta WHERE key = ?", (migration_key,)
                ).fetchone()
                if migrated is None:
                    conn.execute(
                        """
                        INSERT INTO entry(repository, path, digest, size, mtime_ns, synced_at)
                        SELECT ?, path, digest, size, mtime_ns, synced_at
                        FROM entry WHERE repository = ?
                        ON CONFLICT(repository, path) DO NOTHING
                        """,
                        (current, legacy),
                    )
                    conn.execute(
                        """
                        INSERT INTO pending(repository, path, queued_at)
                        SELECT ?, path, queued_at
                        FROM pending WHERE repository = ?
                        ON CONFLICT(repository, path) DO NOTHING
                        """,
                        (current, legacy),
                    )
                    conn.execute(
                        "INSERT INTO meta(key, value) VALUES (?, ?)",
                        (migration_key, repository_id),
                    )

    def _legacy_repository(self) -> str:
        identity = "\0".join(
            str(self.cfg.get(key) or "") for key in ("nas_user", "nas_lan", "remote_prefix")
        )
        return hashlib.sha256(identity.encode()).hexdigest()

    def _repository(self) -> str:
        repository_id = str(self.cfg.get("repository_id") or "")
        identity = (
            f"repository-id\0{repository_id}" if repository_id else
            ""
        )
        return hashlib.sha256(identity.encode()).hexdigest() if repository_id else self._legacy_repository()

    def device_id(self) -> str:
        with self._lock, self._connection() as conn:
            row = conn.execute("SELECT value FROM meta WHERE key = 'device_id'").fetchone()
            if row:
                return row[0]
            value = uuid.uuid4().hex[:8]
            conn.execute("INSERT INTO meta(key, value) VALUES ('device_id', ?)", (value,))
            return value

    @staticmethod
    def fingerprint(path: Path) -> Fingerprint | None:
        for _attempt in range(2):
            try:
                before = path.lstat()
                if not stat.S_ISREG(before.st_mode):
                    return None
                with path.open("rb") as f:
                    digest = hashlib.file_digest(f, "sha256").hexdigest()
                after = path.lstat()
                if (
                    stat.S_ISREG(after.st_mode)
                    and before.st_dev == after.st_dev and before.st_ino == after.st_ino
                    and before.st_size == after.st_size and before.st_mtime_ns == after.st_mtime_ns
                ):
                    return Fingerprint(digest, after.st_size, after.st_mtime_ns)
            except OSError:
                return None
        return None

    def get(self, relative_path: str) -> Fingerprint | None:
        with self._lock, self._connection() as conn:
            row = conn.execute(
                "SELECT digest, size, mtime_ns FROM entry WHERE repository = ? AND path = ?",
                (self._repository(), relative_path),
            ).fetchone()
        if not row:
            return None
        return Fingerprint(*row) if row[0] else Fingerprint("", -1, -1)

    def get_many(self, relative_paths: set[str] | list[str]) -> dict[str, Fingerprint]:
        """Read a large manifest comparison set with one SQLite connection."""
        wanted = set(relative_paths)
        if not wanted:
            return {}
        with self._lock, self._connection() as conn:
            result = {
                row[0]: Fingerprint(row[1], row[2], row[3]) if row[1] else Fingerprint("", -1, -1)
                for row in conn.execute(
                    "SELECT path, digest, size, mtime_ns FROM entry WHERE repository = ?",
                    (self._repository(),),
                )
                if row[0] in wanted
            }
        return result

    def all_entries(self) -> dict[str, Fingerprint]:
        """Read the complete baseline without touching the local files."""
        with self._lock, self._connection() as conn:
            return {
                row[0]: Fingerprint(row[1], row[2], row[3]) if row[1]
                else Fingerprint("", -1, -1)
                for row in conn.execute(
                    "SELECT path, digest, size, mtime_ns FROM entry WHERE repository = ?",
                    (self._repository(),),
                )
            }

    def record_local(self, local_root: str, relative_path: str) -> None:
        self.record_local_many(local_root, [relative_path])

    def record_local_many(self, local_root: str, relative_paths: list[str] | set[str]) -> None:
        """Commit all completed transfer paths in one SQLite transaction."""
        fingerprints = {
            relative_path: self.fingerprint(Path(local_root, relative_path))
            for relative_path in set(relative_paths)
        }
        self.record_fingerprints(fingerprints)

    def record_fingerprints(self, fingerprints: dict[str, Fingerprint | None]) -> None:
        """Commit authoritative per-path fingerprints in one SQLite transaction."""
        rows = []
        repository = self._repository()
        synced_at = time.time()
        for relative_path, fp in fingerprints.items():
            rows.append((
                repository, relative_path, fp.digest if fp else None,
                fp.size if fp else None, fp.mtime_ns if fp else None, synced_at,
            ))
        if not rows:
            return
        with self._lock, self._connection() as conn:
            conn.executemany(
                """
                INSERT INTO entry(repository, path, digest, size, mtime_ns, synced_at)
                VALUES (?, ?, ?, ?, ?, ?)
                ON CONFLICT(repository, path) DO UPDATE SET
                    digest=excluded.digest, size=excluded.size, mtime_ns=excluded.mtime_ns,
                    synced_at=excluded.synced_at
                """,
                rows,
            )

    def pending_paths(self) -> set[str]:
        """Return local changes that still need a successful push."""
        with self._lock, self._connection() as conn:
            return {
                row[0] for row in conn.execute(
                    "SELECT path FROM pending WHERE repository = ?", (self._repository(),)
                )
            }

    def has_pending(self) -> bool:
        with self._lock, self._connection() as conn:
            row = conn.execute(
                "SELECT 1 FROM pending WHERE repository = ? LIMIT 1", (self._repository(),)
            ).fetchone()
        return row is not None

    def mark_pending(self, relative_paths: set[str] | list[str]) -> None:
        """Remember local paths before attempting a network operation."""
        paths = {path for path in relative_paths if path}
        if not paths:
            return
        repository = self._repository()
        queued_at = time.time()
        with self._lock, self._connection() as conn:
            conn.executemany(
                """
                INSERT INTO pending(repository, path, queued_at)
                VALUES (?, ?, ?)
                ON CONFLICT(repository, path) DO UPDATE SET queued_at=excluded.queued_at
                """,
                [(repository, path, queued_at) for path in paths],
            )

    def clear_pending(self, relative_paths: set[str] | list[str]) -> None:
        """Forget paths only after their complete push and journal commit."""
        paths = {path for path in relative_paths if path}
        if not paths:
            return
        with self._lock, self._connection() as conn:
            conn.executemany(
                "DELETE FROM pending WHERE repository = ? AND path = ?",
                [(self._repository(), path) for path in paths],
            )

    def has_entries(self) -> bool:
        """Whether this client has ever established a baseline for this NAS."""
        with self._lock, self._connection() as conn:
            row = conn.execute(
                "SELECT 1 FROM entry WHERE repository = ? LIMIT 1", (self._repository(),)
            ).fetchone()
        return row is not None

    def changed_paths(
        self, local_root: str, on_progress: Optional[Callable[[int, int], None]] = None,
    ) -> set[str]:
        """Find changed/new/deleted regular files when the watcher lacks a path."""
        root = Path(local_root)
        current: dict[str, os.stat_result] = {}
        for path in root.rglob("*"):
            if ".sync-partial" in path.parts:
                continue
            try:
                path_stat = path.lstat()
            except OSError:
                continue
            if stat.S_ISREG(path_stat.st_mode):
                current[str(path.relative_to(root))] = path_stat
        with self._lock, self._connection() as conn:
            known = {
                row[0]: Fingerprint(row[1], row[2], row[3]) if row[1] else Fingerprint("", -1, -1)
                for row in conn.execute(
                    "SELECT path, digest, size, mtime_ns FROM entry WHERE repository = ?", (self._repository(),)
                )
            }
        candidates = set(current) | set(known)
        changed: set[str] = set()
        # Files whose content actually needs reading. Matching metadata, missing
        # files, and files without a common baseline are resolved without hashing.
        to_hash: list[str] = []
        for relative_path in candidates:
            known_fp = known.get(relative_path)
            current_stat = current.get(relative_path)
            if (
                current_stat is not None and known_fp is not None and not known_fp.is_tombstone
                and current_stat.st_size == known_fp.size and current_stat.st_mtime_ns == known_fp.mtime_ns
            ):
                continue
            if current_stat is None:
                if known_fp is not None and known_fp.is_tombstone:
                    continue
                changed.add(relative_path)
                continue
            if known_fp is None or known_fp.is_tombstone:
                # New files and local recreations are candidates by definition.
                # Their digest is needed for the per-chunk reconciliation, not to
                # discover that they must be queued, so do not hash an entire bulk
                # import before its first chunk can start transferring.
                changed.add(relative_path)
                continue
            to_hash.append(relative_path)

        if to_hash:
            # Hashing is I/O-bound (hashlib/file reads release the GIL), so a thread
            # pool speeds up reconciliation of existing files whose metadata changed.
            # Do not use ThreadPoolExecutor's default (min(32, cpu_count+4)) here:
            # a full-tree reconciliation is an exceptional path, not a reason to
            # consume every CPU core.
            total = len(to_hash)
            with ThreadPoolExecutor(max_workers=HASH_MAX_WORKERS) as pool:
                hashed = pool.map(lambda rp: self.fingerprint(root / rp), to_hash)
                for index, (relative_path, current_fp) in enumerate(zip(to_hash, hashed)):
                    known_fp = known.get(relative_path)
                    if current_fp is None and known_fp is not None and known_fp.is_tombstone:
                        continue
                    if current_fp != known_fp:
                        changed.add(relative_path)
                    if on_progress is not None:
                        on_progress(index + 1, total)
        return changed
