"""Client-side classification of apparent differences.

The old safety guard only knew *how many* paths were about to disappear.  That
is enough to stop a disaster but not to understand it: 8.739 files missing from
the local tree can be a mistaken ``rm -rf``, an unmounted drive, or a legitimate
folder rename.  This module turns the raw list into evidence the user (and the
UI) can act on.

It is deliberately pure logic and I/O-free: the caller supplies the remote
manifest, the local identity snapshot from ``SyncStateStore`` and a few
thresholds.  Everything runs on the PC, never on the small NAS.
"""
from __future__ import annotations

from collections import Counter
from dataclasses import dataclass, field
from typing import Mapping, Sequence

from .reconcile import RemoteKind


@dataclass(frozen=True)
class RenameMatch:
    """A missing remote path whose content is present at another local path."""

    missing_path: str
    local_path: str
    reason: str  # "digest" or "digest+mtime"


@dataclass(frozen=True)
class DirectoryAnomaly:
    """A directory that lost an improbable share of its remote file count."""

    directory: str
    missing: int
    remote: int

    @property
    def ratio(self) -> float:
        return self.missing / self.remote if self.remote else 1.0


@dataclass
class ChangeReport:
    missing_total: int
    renames: list[RenameMatch] = field(default_factory=list)
    unexplained: list[str] = field(default_factory=list)
    missing_by_dir: Counter = field(default_factory=Counter)
    remote_by_dir: Counter = field(default_factory=Counter)
    anomalies: list[DirectoryAnomaly] = field(default_factory=list)

    @property
    def renamed_paths(self) -> set[str]:
        return {match.missing_path for match in self.renames}

    @property
    def effective_deletions(self) -> int:
        return len(self.unexplained)

    def summary(self, max_dirs: int = 4) -> str:
        parts: list[str] = []
        if self.renames:
            parts.append(f"{len(self.renames)} rename")
        if self.unexplained:
            parts.append(f"{len(self.unexplained)} cancellazioni sospette")
        if self.anomalies:
            shown = self.anomalies[:max_dirs]
            details = ", ".join(
                f"{item.directory} {item.missing}/{item.remote} ({item.ratio:.0%})"
                for item in shown
            )
            parts.append(f"cartelle anomale: {details}")
            if len(self.anomalies) > max_dirs:
                parts.append(f"+{len(self.anomalies) - max_dirs} altre")
        return "; ".join(parts) or "nessuna differenza rilevante"


def _top_dir(path: str) -> str:
    return path.split("/", 1)[0] if "/" in path else path


def _local_digest_index(identity: Mapping[str, Mapping]) -> dict[str, list[str]]:
    index: dict[str, list[str]] = {}
    for path, info in identity.items():
        digest = info.get("digest")
        if digest:
            index.setdefault(digest, []).append(path)
    return index


def classify(
    missing_paths: Sequence[str],
    remote_entries: Mapping[str, object],
    identity: Mapping[str, Mapping],
    *,
    dir_ratio: float = 0.5,
    min_files: int = 20,
) -> ChangeReport:
    """Classify remote FILE entries that no longer exist in the local tree.

    ``remote_entries`` maps relative paths to ``RemoteState``-like objects with
    ``kind``, ``digest`` and ``size`` attributes.  ``identity`` is the local
    ``path -> {digest, size, ctime_ns, ...}`` snapshot from SQLite.
    """
    local_by_digest = _local_digest_index(identity)
    report = ChangeReport(missing_total=len(missing_paths))
    consumed_locals: set[str] = set()

    for path in missing_paths:
        state = remote_entries.get(path)
        digest = getattr(state, "digest", "") or ""
        size = getattr(state, "size", None)
        match: RenameMatch | None = None
        for local_path in local_by_digest.get(digest, ()):
            if local_path in consumed_locals or local_path == path:
                continue
            info = identity[local_path]
            if size is not None and info.get("size") not in (None, size):
                continue
            reason = "digest"
            if info.get("ctime_ns"):
                reason = "digest+mtime"
            match = RenameMatch(path, local_path, reason)
            consumed_locals.add(local_path)
            break
        if match is not None:
            report.renames.append(match)
        else:
            report.unexplained.append(path)

    # Anomalies must describe *unexplained* losses: a folder rename produces a
    # full directory of missing paths that are all matched to new local names.
    for path in report.unexplained:
        report.missing_by_dir[_top_dir(path)] += 1
    for path, state in remote_entries.items():
        if getattr(state, "kind", None) is RemoteKind.FILE:
            report.remote_by_dir[_top_dir(path)] += 1

    for directory, missing in report.missing_by_dir.items():
        remote = report.remote_by_dir.get(directory, 0)
        if missing >= min_files and remote and missing / remote >= dir_ratio:
            report.anomalies.append(DirectoryAnomaly(directory, missing, remote))
    report.anomalies.sort(key=lambda item: (-item.missing, item.directory))
    return report
