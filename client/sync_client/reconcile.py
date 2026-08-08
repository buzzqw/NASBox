"""Pure per-path reconciliation rules for the NAS-authoritative sync model."""
from __future__ import annotations

from dataclasses import dataclass
from enum import Enum

from .sync_state import Fingerprint


class RemoteKind(str, Enum):
    ABSENT = "ABSENT"
    FILE = "FILE"
    TOMBSTONE = "TOMBSTONE"
    OTHER = "OTHER"


@dataclass(frozen=True)
class RemoteState:
    kind: RemoteKind
    digest: str = ""
    size: int = 0
    mtime_ns: int = 0

    @property
    def is_file(self) -> bool:
        return self.kind == RemoteKind.FILE


class Action(str, Enum):
    NOTHING = "nothing"
    ADOPT = "adopt"
    UPLOAD = "upload"
    DELETE_REMOTE = "delete_remote"
    REMOTE_WINS = "remote_wins"
    CONFLICT_LOCAL_WINS = "conflict_local_wins"
    CONFLICT_REMOTE_WINS = "conflict_remote_wins"
    BLOCK = "block"


@dataclass(frozen=True)
class Decision:
    action: Action
    detail: str = ""


def _same_file(left: Fingerprint | None, right: RemoteState) -> bool:
    return left is not None and not left.is_tombstone and right.is_file and left.digest == right.digest


def _event_is_newer(local: Fingerprint, remote: RemoteState) -> bool:
    """mtime is only a tie-breaker for concurrent/unknown histories.

    Comparing whole seconds matches the portable precision exposed by stat on
    both BusyBox-based NAS appliances and GNU/Linux. Equal timestamps are
    deliberately resolved in favour of the NAS while preserving the local
    version as a conflict, so clock ambiguity can never destroy data.
    """
    return local.mtime_ns // 1_000_000_000 > remote.mtime_ns // 1_000_000_000


def plan_path(
    baseline: Fingerprint | None,
    local: Fingerprint | None,
    remote: RemoteState,
    *,
    delete_enabled: bool,
) -> Decision:
    """Choose one convergent action from the common base and both live states."""
    if remote.kind == RemoteKind.OTHER:
        return Decision(Action.BLOCK, "sul NAS il percorso non e' un file regolare")

    local_absent = local is None
    baseline_absent = baseline is None or baseline.is_tombstone

    if baseline is None:
        if local_absent:
            return Decision(Action.ADOPT if remote.kind == RemoteKind.TOMBSTONE else Action.REMOTE_WINS)
        if remote.kind == RemoteKind.ABSENT:
            return Decision(Action.UPLOAD)
        if remote.kind == RemoteKind.TOMBSTONE:
            return Decision(Action.CONFLICT_REMOTE_WINS, "storia locale sconosciuta rispetto alla cancellazione NAS")
        if _same_file(local, remote):
            return Decision(Action.ADOPT)
        if _event_is_newer(local, remote):
            return Decision(Action.CONFLICT_LOCAL_WINS, "file locale piu' recente")
        return Decision(Action.CONFLICT_REMOTE_WINS, "file NAS piu' recente o contemporaneo")

    if baseline_absent:
        if local_absent:
            return Decision(Action.ADOPT if remote.kind != RemoteKind.FILE else Action.REMOTE_WINS)
        if remote.kind == RemoteKind.ABSENT:
            return Decision(Action.UPLOAD)
        if remote.kind == RemoteKind.TOMBSTONE:
            # This client had already adopted the deletion, therefore the
            # recreated local file is causally newer regardless of clock skew.
            return Decision(Action.UPLOAD, "ricreazione locale successiva alla cancellazione NAS")
        if _same_file(local, remote):
            return Decision(Action.ADOPT)
        if _event_is_newer(local, remote):
            return Decision(Action.CONFLICT_LOCAL_WINS, "ricreazione locale piu' recente")
        return Decision(Action.CONFLICT_REMOTE_WINS, "versione NAS piu' recente o contemporanea")

    assert baseline is not None and not baseline.is_tombstone
    if local_absent:
        if remote.kind in (RemoteKind.ABSENT, RemoteKind.TOMBSTONE):
            return Decision(Action.ADOPT)
        remote_is_base = (
            remote.digest == baseline.digest
            and remote.mtime_ns // 1_000_000_000 == baseline.mtime_ns // 1_000_000_000
        )
        if delete_enabled and remote_is_base:
            return Decision(Action.DELETE_REMOTE)
        if not delete_enabled:
            return Decision(Action.REMOTE_WINS, "propagazione cancellazioni disabilitata")
        return Decision(Action.REMOTE_WINS, "cancellazione locale stantia: il NAS e' cambiato")

    local_changed = local.digest != baseline.digest
    if remote.kind in (RemoteKind.ABSENT, RemoteKind.TOMBSTONE):
        if not local_changed:
            return Decision(Action.REMOTE_WINS)
        return Decision(Action.CONFLICT_REMOTE_WINS, "modifica locale concorrente con cancellazione NAS")

    if _same_file(local, remote):
        return Decision(Action.ADOPT)

    remote_changed = remote.digest != baseline.digest
    if local_changed and not remote_changed:
        return Decision(Action.UPLOAD)
    if not local_changed and remote_changed:
        return Decision(Action.REMOTE_WINS)
    if not local_changed and not remote_changed:
        return Decision(Action.ADOPT)
    if _event_is_newer(local, remote):
        return Decision(Action.CONFLICT_LOCAL_WINS, "modifiche concorrenti: file locale piu' recente")
    return Decision(Action.CONFLICT_REMOTE_WINS, "modifiche concorrenti: file NAS piu' recente o contemporaneo")
