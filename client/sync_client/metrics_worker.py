"""Read-only NAS load metrics.

The server returns one small KEY=VALUE payload.  This module validates that
payload and keeps the previous sample needed to turn /proc/net/dev counters
into rates.  SSH itself is run by the caller's background thread, never by a
Qt GUI slot.
"""
from __future__ import annotations

import hashlib
import os
import time
from collections import defaultdict
from dataclasses import dataclass, replace
from pathlib import Path

from . import rsync_ops, trash
from .config import Config
from .rsync_ops import NasConnection

METRICS_PROTOCOL = "NASBOX_METRICS_V1"


class MetricsProtocolError(ValueError):
    """The NAS response is not a valid metrics payload."""


@dataclass(frozen=True)
class NasMetrics:
    collected_at_epoch: int
    uptime_seconds: int
    load_1: float
    load_5: float
    load_15: float
    cpu_total_ticks: int
    cpu_idle_ticks: int
    cpu_iowait_ticks: int
    memory_total_bytes: int
    memory_available_bytes: int
    swap_total_bytes: int
    swap_free_bytes: int
    disk_total_bytes: int
    disk_used_bytes: int
    disk_available_bytes: int
    disk_usage_percent: float
    disk_read_bytes: int
    disk_write_bytes: int
    net_rx_bytes: int
    net_tx_bytes: int
    net_rx_packets: int
    net_tx_packets: int
    net_interface_count: int
    net_rx_rate: float | None = None
    net_tx_rate: float | None = None
    cpu_usage_percent: float | None = None
    cpu_iowait_percent: float | None = None
    disk_read_rate: float | None = None
    disk_write_rate: float | None = None
    nasbox_lock_held: bool = False
    nasbox_lock_owner: str = ""
    nasbox_lock_host: str = ""
    nasbox_lock_phase: str = ""
    nasbox_lock_started_at: int = 0
    nasbox_lock_progress_done: int = 0
    nasbox_lock_progress_total: int = 0
    nasbox_queue_count: int = 0
    nasbox_staging_count: int = 0
    journal_bytes: int = 0
    manifest_bytes: int = 0


@dataclass(frozen=True)
class MetricsFetchResult:
    supported: bool
    metrics: NasMetrics | None = None
    detail: str = ""

    @property
    def ok(self) -> bool:
        return self.metrics is not None and not self.detail


_REQUIRED = (
    "COLLECTED_AT_EPOCH", "UPTIME_SECONDS", "LOAD_1", "LOAD_5", "LOAD_15",
    "CPU_TOTAL_TICKS", "CPU_IDLE_TICKS", "CPU_IOWAIT_TICKS",
    "MEM_TOTAL_BYTES", "MEM_AVAILABLE_BYTES", "SWAP_TOTAL_BYTES", "SWAP_FREE_BYTES",
    "DISK_TOTAL_BYTES", "DISK_USED_BYTES", "DISK_AVAILABLE_BYTES", "DISK_USAGE_PERCENT",
    "DISK_READ_BYTES", "DISK_WRITE_BYTES",
    "NET_RX_BYTES", "NET_TX_BYTES", "NET_RX_PACKETS", "NET_TX_PACKETS",
    "NET_INTERFACE_COUNT",
)
_INTEGER_FIELDS = {
    "COLLECTED_AT_EPOCH", "UPTIME_SECONDS", "MEM_TOTAL_BYTES", "MEM_AVAILABLE_BYTES",
    "SWAP_TOTAL_BYTES", "SWAP_FREE_BYTES", "DISK_TOTAL_BYTES", "DISK_USED_BYTES",
    "DISK_AVAILABLE_BYTES", "CPU_TOTAL_TICKS", "CPU_IDLE_TICKS", "CPU_IOWAIT_TICKS",
    "DISK_READ_BYTES", "DISK_WRITE_BYTES", "NET_RX_BYTES", "NET_TX_BYTES", "NET_RX_PACKETS",
    "NET_TX_PACKETS", "NET_INTERFACE_COUNT",
}


def parse_metrics(output: str) -> NasMetrics:
    """Parse and validate a server ``--metrics`` response."""
    lines = output.splitlines()
    if not lines or lines[0].strip() != METRICS_PROTOCOL:
        raise MetricsProtocolError("risposta NAS non riconosciuta")

    values: dict[str, str] = {}
    for line in lines[1:]:
        key, separator, value = line.partition("=")
        if separator and key and key not in values:
            values[key] = value.strip()
    missing = [key for key in _REQUIRED if key not in values]
    if missing:
        raise MetricsProtocolError(f"metriche NAS mancanti: {', '.join(missing)}")

    parsed: dict[str, int | float] = {}
    try:
        for key in _REQUIRED:
            parsed[key] = int(values[key]) if key in _INTEGER_FIELDS else float(values[key])
    except (TypeError, ValueError) as exc:
        raise MetricsProtocolError("valore non numerico nelle metriche NAS") from exc

    integer_values = [parsed[key] for key in _INTEGER_FIELDS]
    if any(not isinstance(value, int) or value < 0 for value in integer_values):
        raise MetricsProtocolError("valore negativo nelle metriche NAS")
    if any(float(parsed[key]) < 0 for key in ("LOAD_1", "LOAD_5", "LOAD_15")):
        raise MetricsProtocolError("carico NAS non valido")
    if not 0 <= float(parsed["DISK_USAGE_PERCENT"]) <= 100:
        raise MetricsProtocolError("percentuale disco NAS non valida")

    optional_integers = (
        "NASBOX_LOCK_STARTED_AT", "NASBOX_LOCK_PROGRESS_DONE", "NASBOX_LOCK_PROGRESS_TOTAL",
        "NASBOX_QUEUE_COUNT", "JOURNAL_BYTES", "MANIFEST_BYTES",
        "NASBOX_STAGING_COUNT",
    )
    optional: dict[str, int] = {}
    try:
        for key in optional_integers:
            if key in values:
                optional[key] = int(values[key])
    except ValueError as exc:
        raise MetricsProtocolError("valore opzionale non numerico nelle metriche NAS") from exc
    if any(value < 0 for value in optional.values()):
        raise MetricsProtocolError("valore opzionale negativo nelle metriche NAS")
    lock_held = values.get("NASBOX_LOCK_HELD", "false")
    if lock_held not in {"true", "false"}:
        raise MetricsProtocolError("stato lock NAS non valido")

    return NasMetrics(
        collected_at_epoch=int(parsed["COLLECTED_AT_EPOCH"]),
        uptime_seconds=int(parsed["UPTIME_SECONDS"]),
        load_1=float(parsed["LOAD_1"]),
        load_5=float(parsed["LOAD_5"]),
        load_15=float(parsed["LOAD_15"]),
        cpu_total_ticks=int(parsed["CPU_TOTAL_TICKS"]),
        cpu_idle_ticks=int(parsed["CPU_IDLE_TICKS"]),
        cpu_iowait_ticks=int(parsed["CPU_IOWAIT_TICKS"]),
        memory_total_bytes=int(parsed["MEM_TOTAL_BYTES"]),
        memory_available_bytes=int(parsed["MEM_AVAILABLE_BYTES"]),
        swap_total_bytes=int(parsed["SWAP_TOTAL_BYTES"]),
        swap_free_bytes=int(parsed["SWAP_FREE_BYTES"]),
        disk_total_bytes=int(parsed["DISK_TOTAL_BYTES"]),
        disk_used_bytes=int(parsed["DISK_USED_BYTES"]),
        disk_available_bytes=int(parsed["DISK_AVAILABLE_BYTES"]),
        disk_usage_percent=float(parsed["DISK_USAGE_PERCENT"]),
        disk_read_bytes=int(parsed["DISK_READ_BYTES"]),
        disk_write_bytes=int(parsed["DISK_WRITE_BYTES"]),
        net_rx_bytes=int(parsed["NET_RX_BYTES"]),
        net_tx_bytes=int(parsed["NET_TX_BYTES"]),
        net_rx_packets=int(parsed["NET_RX_PACKETS"]),
        net_tx_packets=int(parsed["NET_TX_PACKETS"]),
        net_interface_count=int(parsed["NET_INTERFACE_COUNT"]),
        nasbox_lock_held=lock_held == "true",
        nasbox_lock_owner=values.get("NASBOX_LOCK_OWNER", ""),
        nasbox_lock_host=values.get("NASBOX_LOCK_HOST", ""),
        nasbox_lock_phase=values.get("NASBOX_LOCK_PHASE", ""),
        nasbox_lock_started_at=optional.get("NASBOX_LOCK_STARTED_AT", 0),
        nasbox_lock_progress_done=optional.get("NASBOX_LOCK_PROGRESS_DONE", 0),
        nasbox_lock_progress_total=optional.get("NASBOX_LOCK_PROGRESS_TOTAL", 0),
        nasbox_queue_count=optional.get("NASBOX_QUEUE_COUNT", 0),
        nasbox_staging_count=optional.get("NASBOX_STAGING_COUNT", 0),
        journal_bytes=optional.get("JOURNAL_BYTES", 0),
        manifest_bytes=optional.get("MANIFEST_BYTES", 0),
    )


def _looks_unsupported(detail: str) -> bool:
    text = detail.lower()
    return any(term in text for term in (
        "opzione sconosciuta", "unknown option", "unrecognized option", "invalid option",
    ))


def fetch_remote_metrics(
    cfg: Config, conn: NasConnection, on_start=None,
) -> MetricsFetchResult:
    """Fetch all metrics with exactly one SSH invocation."""
    script_path = str(cfg.get("remote_server_script") or "").strip()
    if not script_path:
        return MetricsFetchResult(False, detail="script server sul NAS non configurato")

    ok, output, error = rsync_ops.run_remote_script(
        cfg, conn, script_path, ["--metrics"], timeout=20, on_start=on_start,
    )
    if not ok:
        detail = (error or output or "comando metriche NAS fallito").strip()
        if _looks_unsupported(detail):
            return MetricsFetchResult(False, detail="server NAS senza supporto per le metriche")
        return MetricsFetchResult(True, detail=detail)
    try:
        return MetricsFetchResult(True, metrics=parse_metrics(output))
    except MetricsProtocolError as exc:
        return MetricsFetchResult(True, detail=str(exc))


class MetricsCollector:
    """Adds client-side network rates to otherwise stateless server samples."""

    def __init__(self) -> None:
        self._previous: tuple[float, NasMetrics] | None = None

    def reset(self) -> None:
        self._previous = None

    def collect(self, cfg: Config, conn: NasConnection, on_start=None) -> MetricsFetchResult:
        result = fetch_remote_metrics(cfg, conn, on_start=on_start)
        if result.metrics is None:
            return result
        now = time.monotonic()
        previous = self._previous
        self._previous = (now, result.metrics)
        if previous is None:
            return result
        elapsed = now - previous[0]
        if elapsed <= 0:
            return result
        old = previous[1]
        cpu_delta = result.metrics.cpu_total_ticks - old.cpu_total_ticks
        busy_delta = (
            result.metrics.cpu_total_ticks - result.metrics.cpu_idle_ticks
            - old.cpu_total_ticks + old.cpu_idle_ticks
        )
        iowait_delta = result.metrics.cpu_iowait_ticks - old.cpu_iowait_ticks
        return replace(
            result,
            metrics=replace(
                result.metrics,
                net_rx_rate=max(0, result.metrics.net_rx_bytes - old.net_rx_bytes) / elapsed,
                net_tx_rate=max(0, result.metrics.net_tx_bytes - old.net_tx_bytes) / elapsed,
                cpu_usage_percent=(max(0, busy_delta) * 100 / cpu_delta) if cpu_delta > 0 else None,
                cpu_iowait_percent=(max(0, iowait_delta) * 100 / cpu_delta) if cpu_delta > 0 else None,
                disk_read_rate=max(0, result.metrics.disk_read_bytes - old.disk_read_bytes) / elapsed,
                disk_write_rate=max(0, result.metrics.disk_write_bytes - old.disk_write_bytes) / elapsed,
            ),
        )


# ---------------------------------------------------------------------------
# Storage insights (local filesystem only, read-only).
#
# These helpers are pure: no Qt, no SSH, no writes. The caller runs them on a
# background thread (see MetricsTab._analyze_storage) because scanning a large
# NASBox tree and hashing candidate duplicates can take a while. Every loop is
# bounded so a pathological tree cannot block the scan forever, and permission
# errors are counted instead of aborting the whole analysis.
# ---------------------------------------------------------------------------

STORAGE_TOP_FOLDERS = 20
STORAGE_MAX_DUPLICATE_CANDIDATES = 2000
STORAGE_MAX_FILES = 50_000
STORAGE_MAX_FILES_PER_DIR = 10_000
STORAGE_MAX_DIRS = 20_000
_STORAGE_HASH_CHUNK = 1024 * 1024
_STORAGE_PROGRESS_EVERY = 250


@dataclass(frozen=True)
class FolderSize:
    """Subtree size of one directory below the local NASBox root."""

    path: str          # relative to the local root ("." for the root itself)
    size_bytes: int
    file_count: int


@dataclass(frozen=True)
class DuplicateGroup:
    """Files with identical size *and* sha256 digest."""

    size_bytes: int
    sha256: str
    paths: tuple[str, ...]   # relative paths, sorted for stable display

    @property
    def wasted_bytes(self) -> int:
        """Bytes reclaimable by keeping a single copy of this group."""
        return self.size_bytes * (len(self.paths) - 1)


@dataclass(frozen=True)
class TrashInsight:
    """Read-only view of the local .sync-trash version history."""

    version_count: int
    total_bytes: int
    expired_count: int
    expired_bytes: int
    retention_days: float


@dataclass(frozen=True)
class StorageInsights:
    """Snapshot returned by :func:`collect_storage_insights`."""

    root: str
    collected_at: float
    scanned_files: int
    scan_errors: int
    candidates_limit_reached: bool
    top_folders: tuple[FolderSize, ...]
    duplicates: tuple[DuplicateGroup, ...]
    duplicate_candidates: int
    duplicate_wasted_bytes: int
    trash: TrashInsight


def _empty_insights(root: Path, retention_days: float = 0.0) -> StorageInsights:
    return StorageInsights(
        root=str(root),
        collected_at=time.time(),
        scanned_files=0,
        scan_errors=0,
        candidates_limit_reached=False,
        top_folders=(),
        duplicates=(),
        duplicate_candidates=0,
        duplicate_wasted_bytes=0,
        trash=TrashInsight(0, 0, 0, 0, retention_days),
    )


def _hash_file(path: str) -> str | None:
    digest = hashlib.sha256()
    try:
        with open(path, "rb") as handle:
            for chunk in iter(lambda: handle.read(_STORAGE_HASH_CHUNK), b""):
                digest.update(chunk)
    except OSError:
        return None
    return digest.hexdigest()


def _scan_folders(root: Path, progress) -> tuple[list[FolderSize], list[tuple[str, str, int]], int]:
    """Walk *root* once with os.scandir, returning folder stats and file records.

    Symlinks are skipped (no loops / leaving the tree), unreadable entries are
    counted as errors, and traversal is bounded by the module constants. File
    records are ``(relative_path, absolute_path, size)`` tuples.
    """
    root_str = os.path.normpath(str(root))
    dir_sizes: dict[str, list[int]] = {root_str: [0, 0]}  # abs dir -> [bytes, files]
    dir_parent: dict[str, str | None] = {root_str: None}
    per_dir_files: dict[str, int] = defaultdict(int)
    files: list[tuple[str, str, int]] = []
    errors = 0
    stack = [root_str]

    while stack:
        current = stack.pop()
        try:
            with os.scandir(current) as iterator:
                entries = sorted(iterator, key=lambda entry: entry.name)
        except (PermissionError, OSError):
            errors += 1
            continue

        subdirs: list[str] = []
        for entry in entries:
            try:
                if entry.is_symlink():
                    continue
                if entry.is_dir(follow_symlinks=False):
                    child = os.path.normpath(entry.path)
                    if len(dir_sizes) >= STORAGE_MAX_DIRS:
                        continue
                    dir_sizes[child] = [0, 0]
                    dir_parent[child] = current
                    subdirs.append(child)
                elif entry.is_file(follow_symlinks=False):
                    if (per_dir_files[current] >= STORAGE_MAX_FILES_PER_DIR
                            or len(files) >= STORAGE_MAX_FILES):
                        continue
                    per_dir_files[current] += 1
                    try:
                        size = entry.stat(follow_symlinks=False).st_size
                    except OSError:
                        errors += 1
                        continue
                    dir_sizes[current][0] += size
                    dir_sizes[current][1] += 1
                    files.append((os.path.relpath(entry.path, root_str), entry.path, size))
                    if progress is not None and len(files) % _STORAGE_PROGRESS_EVERY == 0:
                        progress(len(files))
            except (PermissionError, OSError):
                errors += 1

        # Reverse so the alphabetical first folder is popped first.
        stack.extend(reversed(subdirs))

    # Accumulate descendant sizes into ancestors, deepest directories first.
    for directory in sorted(dir_sizes, key=lambda item: item.count(os.sep), reverse=True):
        parent = dir_parent.get(directory)
        if parent is not None and parent in dir_sizes:
            dir_sizes[parent][0] += dir_sizes[directory][0]
            dir_sizes[parent][1] += dir_sizes[directory][1]

    folders = [
        FolderSize(
            path=os.path.relpath(directory, root_str),
            size_bytes=values[0],
            file_count=values[1],
        )
        for directory, values in dir_sizes.items()
        if values[1] > 0  # skip empty directories (and an entirely empty root)
    ]
    folders.sort(key=lambda folder: (-folder.size_bytes, folder.path))
    return folders[:STORAGE_TOP_FOLDERS], files, errors


def _find_duplicates(
    files: list[tuple[str, str, int]], progress=None,
) -> tuple[int, tuple[DuplicateGroup, ...], int, bool]:
    """Group a bounded sample of files by (size, sha256)."""
    # Empty files are ignored: they are not meaningful reclaimable space.
    non_empty = [record for record in files if record[2] > 0]
    candidates = non_empty[:STORAGE_MAX_DUPLICATE_CANDIDATES]
    limited = len(non_empty) > len(candidates)

    by_size: dict[int, list[tuple[str, str, int]]] = defaultdict(list)
    for record in candidates:
        by_size[record[2]].append(record)

    groups: list[DuplicateGroup] = []
    for size, records in by_size.items():
        if len(records) < 2:
            continue
        by_digest: dict[str, list[str]] = defaultdict(list)
        for relative, absolute, _size in records:
            digest = _hash_file(absolute)
            if digest is not None:
                by_digest[digest].append(relative)
        for digest, paths in by_digest.items():
            if len(paths) >= 2:
                groups.append(DuplicateGroup(size, digest, tuple(sorted(paths))))

    groups.sort(key=lambda group: (-group.wasted_bytes, group.paths))
    wasted = sum(group.wasted_bytes for group in groups)
    if progress is not None:
        progress(len(candidates))
    return len(candidates), tuple(groups[:STORAGE_TOP_FOLDERS]), wasted, limited


def _retention(cfg: Config) -> float:
    try:
        return float(cfg.get("retention_days_local") or 0)
    except (TypeError, ValueError):
        return 0.0


def _trash_insight(cfg: Config) -> TrashInsight:
    """Summarise local version history without ever pruning or modifying it."""
    retention = _retention(cfg)
    try:
        versions = trash.list_local_versions()
    except OSError:
        versions = []
    expired = [version for version in versions if retention > 0 and version.age_days > retention]
    return TrashInsight(
        version_count=len(versions),
        total_bytes=sum(version.size for version in versions),
        expired_count=len(expired),
        expired_bytes=sum(version.size for version in expired),
        retention_days=retention,
    )


def collect_storage_insights(
    cfg: Config, *, progress=None, local_root: str | None = None,
) -> StorageInsights:
    """Collect storage insights from the local NASBox folder (read-only).

    ``progress``, when given, is called with an integer number of examined
    files every :data:`_STORAGE_PROGRESS_EVERY` files. ``local_root`` exists so
    tests can point the scan at a temporary tree; by default the configured
    ``local_root`` is used.
    """
    root = Path(local_root if local_root is not None else (cfg.local_root() or ""))
    if not root.is_dir():
        return _empty_insights(root, _retention(cfg))

    folders, files, errors = _scan_folders(root, progress)
    candidates, groups, wasted, limited = _find_duplicates(files, progress)
    return StorageInsights(
        root=str(root),
        collected_at=time.time(),
        scanned_files=len(files),
        scan_errors=errors,
        candidates_limit_reached=limited,
        top_folders=tuple(folders),
        duplicates=groups,
        duplicate_candidates=candidates,
        duplicate_wasted_bytes=wasted,
        trash=_trash_insight(cfg),
    )
