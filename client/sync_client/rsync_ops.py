"""Wraps rsync+ssh transfers: connectivity, dry-run scanning (transfer queue) and
real push/pull with live bandwidth limiting and backup-dir based versioning (trash).

There is exactly one synced tree per client -- the local NASBox folder
(cfg.local_root()) mirrored against the NAS's remote_prefix, root to root. No
per-folder "shares" to add/remove/nest (see MANUALE.md §8 for why that model was
retired).

Handles asymmetric network topologies where the NAS itself has no direct WAN
reachability but a *different* host (a bastion/jump host) does, and that
bastion can reach the NAS on its own LAN. When direct reachability fails but
the bastion is up, every ssh/rsync call is routed through it via
`-o ProxyJump=...` while still targeting the NAS's LAN address as the final
destination (that's what ProxyJump is for: the bastion makes the last hop).
"""
from __future__ import annotations

import os
import hashlib
import fnmatch
import re
import selectors
import shlex
import socket
import subprocess
import tempfile
import time
import uuid
from dataclasses import dataclass
from datetime import datetime, timezone
from pathlib import Path
from typing import Callable, Optional

from .config import Config
from .reconcile import RemoteKind, RemoteState
from .repository_safety import RepositorySafetyError, validate_local_root
from .sync_state import CausalVersion

TRASH_DIRNAME = ".sync-trash"
REPOSITORY_MARKER_NAME = ".nasbox-root"
# Synology auto-creates one of these in every folder (thumbnails/search index) --
# never sync it, in either direction, or every listing recurses into NAS-internal
# cache churn instead of the user's actual files.
SYNOLOGY_EADIR = "@eaDir"
STAGING_DIRNAME = ".nasbox-staging"
PARTIAL_DIRNAME = ".sync-partial"
EXCLUDE_DIRNAMES = [TRASH_DIRNAME, SYNOLOGY_EADIR, PARTIAL_DIRNAME, STAGING_DIRNAME]

# Keep the rsync pipe useful as backpressure: diagnostics are for the error
# message only, not an unbounded second copy of a large transfer's output.
MAX_TRANSFER_ERROR_BYTES = 64 * 1024
MAX_PENDING_TRANSFER_ITEMS = 4096

# rsync's default --delete mode (--delete-during) applies deletions incrementally,
# directory by directory, as the transfer proceeds -- using a source-side file list
# captured once at the very start. A local file created *during* an in-flight pull
# can still get swept up in that directory's deletion pass even if PullWorker's
# cancel-on-dirty check (see pull_worker.py) reacts within milliseconds, because
# the deletion for that specific item may already have been applied by rsync
# before our code ever sees a progress line to react to (verified against a real
# rsync run: with plain --delete the "*deleting" line for an extra local file came
# out *before* a large file's transfer finished; with --delete-after it came out
# only *after*). --delete-after defers every deletion to the very end of the
# transfer, so a cancellation triggered any time during the (typically much
# longer) transfer phase aborts the whole run before any deletion is ever applied.
DELETE_FLAG = "--delete-after"

# Lowest server/sync-daemon-server.sh version this client knows how to talk to.
# Bump this whenever the client starts relying on a new --print-config field or
# server-side capability, so an outdated NAS package gets flagged instead of
# silently misbehaving.
EXPECTED_SERVER_VERSION = "3.16.0"
_SERVER_UPDATE_NAME_RE = re.compile(r"^sync-daemon-server-([0-9]+(?:\.[0-9]+)+)\.sh$")


def _version_tuple(version: str) -> tuple[int, ...]:
    try:
        return tuple(int(part) for part in version.strip().split("."))
    except (ValueError, AttributeError):
        return (0,)


def server_is_outdated(remote_version: str) -> bool:
    if not remote_version:
        return False
    return _version_tuple(remote_version) < _version_tuple(EXPECTED_SERVER_VERSION)


# rsync 3.2.4 turned on backslash-escaping of remote-shell args by default (see
# --old-args in the manual). A sender that new talking to a receiver that old can
# corrupt the remote destination path it sends over ssh -- observed in practice
# against a Synology stuck on rsync 3.1.2 as a literal stray "'" spliced into the
# path, which then resolves relative to the login shell's home dir instead of the
# intended absolute path ("rsync: change_dir#N ... No such file or directory").
# --old-args (or RSYNC_OLD_ARGS=1) restores the pre-3.2.4 behavior and is the
# documented fix, but it's only needed talking to a receiver that predates it, so
# it isn't applied unconditionally -- see _detect_remote_old_args below.
RSYNC_ARG_PROTECTION_MIN_VERSION = "3.2.4"
_RSYNC_VERSION_RE = re.compile(r"version\s+(\d+(?:\.\d+)+)")

# One detection per host per process run -- the NAS's installed rsync binary
# doesn't change mid-session, and re-probing before every single push/pull would
# add a needless round trip to every transfer. Only successful detections are
# cached: a probe that couldn't tell (offline, timeout, unparsable output) is
# retried next time rather than permanently assumed modern.
_remote_old_args_cache: dict[str, bool] = {}


def _detect_remote_old_args(cfg: Config, conn: NasConnection) -> bool:
    cached = _remote_old_args_cache.get(conn.host)
    if cached is not None:
        return cached
    user = cfg.get("nas_user")
    cmd = ["ssh", *ssh_opts(cfg, conn), f"{user}@{conn.host}", "rsync --version"]
    try:
        proc = subprocess.Popen(
            cmd, stdin=subprocess.DEVNULL, stdout=subprocess.PIPE,
            stderr=subprocess.DEVNULL, text=True,
        )
    except OSError:
        return False  # can't tell -- leave rsync's own (modern, safe-by-default) behavior alone
    try:
        # `rsync --version`'s output is a few hundred bytes at most, well under a
        # pipe buffer -- waiting before draining stdout can't deadlock here the
        # way it could for a chatty command.
        proc.wait(timeout=15)
    except subprocess.TimeoutExpired:
        proc.kill()
        proc.wait()
        return False
    if proc.returncode != 0 or proc.stdout is None:
        return False
    match = _RSYNC_VERSION_RE.search(proc.stdout.read())
    if not match:
        return False
    needs_old_args = _version_tuple(match.group(1)) < _version_tuple(RSYNC_ARG_PROTECTION_MIN_VERSION)
    _remote_old_args_cache[conn.host] = needs_old_args
    return needs_old_args


def _old_args_flag(cfg: Config, conn: NasConnection) -> list[str]:
    return ["--old-args"] if _detect_remote_old_args(cfg, conn) else []


@dataclass
class TransferItem:
    direction: str   # "upload" | "download" | "delete_remote" | "delete_local" | "rename_remote"
    path: str        # file path relative to the NASBox root
    size: int = 0
    source_path: str = ""  # set for a remote rename; path is its destination


@dataclass
class TransferResult:
    ok: bool
    items: list[TransferItem]
    raw_error: str = ""
    cancelled: bool = False
    partial_preserved: bool = False


@dataclass
class CheckedDeleteResult:
    ok: bool
    items: list[TransferItem]
    completed_paths: set[str]
    stale_paths: set[str]
    raw_error: str = ""


@dataclass
class NasConnection:
    host: str            # NAS address to SSH into as the final destination (its LAN IP, usually)
    via_jump: bool = False  # True if reached through the bastion rather than directly


@dataclass(frozen=True)
class RemoteRevisionWait:
    result: str  # "CHANGED" | "TIMEOUT"
    revision: int


class RemoteLockError(RuntimeError):
    """The NAS transaction lock could not be acquired safely."""


class RemoteLockBusy(RemoteLockError):
    """Another client held the NAS transaction lock for the whole wait."""

    def __init__(self, detail: str = "lock occupato da un altro client", owner_id: str = "",
                 owner_host: str = "", started_at: int = 0) -> None:
        super().__init__(detail)
        self.owner_id = owner_id
        self.owner_host = owner_host
        self.started_at = started_at


REMOTE_LOCK_BUSY_EXIT_CODE = 75


class RemoteLock:
    """Exclusive lock held by a live SSH session for one full transfer.

    Closing stdin ends the remote `cat`, which closes flock's file descriptor.
    That makes client crashes and network failures release the lock without any
    unsafe stale-lock cleanup heuristic.
    """

    def __init__(
        self, cfg: Config, conn: NasConnection, timeout: int = 45,
        on_start: Optional[Callable[[subprocess.Popen], None]] = None,
        owner_id: str = "", priority: int = 1,
    ) -> None:
        self.cfg = cfg
        self.conn = conn
        self.timeout = timeout
        self.on_start = on_start
        self.owner_id = owner_id
        self.priority = priority
        self.proc: subprocess.Popen | None = None
        self._stdout_buffer = bytearray()
        self._scheduler_mode = False

    def _read_stdout_line(self, timeout: float) -> bytes:
        """Read one protocol line without allowing TextIO buffering to block."""
        assert self.proc is not None and self.proc.stdout is not None
        deadline = time.monotonic() + max(0, timeout)
        while True:
            newline = self._stdout_buffer.find(b"\n")
            if newline >= 0:
                line = bytes(self._stdout_buffer[:newline])
                del self._stdout_buffer[:newline + 1]
                return line
            remaining = deadline - time.monotonic()
            if remaining <= 0:
                raise subprocess.TimeoutExpired(self.proc.args, timeout)
            with selectors.DefaultSelector() as selector:
                selector.register(self.proc.stdout, selectors.EVENT_READ)
                if not selector.select(remaining):
                    raise subprocess.TimeoutExpired(self.proc.args, timeout)
            chunk = os.read(self.proc.stdout.fileno(), 4096)
            if not chunk:
                return bytes(self._stdout_buffer)
            self._stdout_buffer.extend(chunk)

    def __enter__(self) -> "RemoteLock":
        lock_file = (self.cfg.get("server_lock_file_remote") or "").strip()
        if not lock_file:
            raise RemoteLockError("server NAS senza lock multi-client; aggiornamento obbligatorio")

        parent = lock_file.rsplit("/", 1)[0] or "."
        remote_script = (self.cfg.get("remote_server_script") or "").strip()
        owner_file = (self.cfg.get("server_lock_owner_file_remote") or f"{lock_file}.owner").strip()
        owner = self.owner_id or "unknown"
        hostname = socket.gethostname() or "unknown"
        owner_payload = f"{owner}|{hostname}|{int(time.time())}"
        owner_tmp = f"{owner_file}.$$"
        owner_script = (
            f"trap 'rm -f {shlex.quote(owner_tmp)} {shlex.quote(owner_file)}' EXIT; "
            f"printf '%s\\n' {shlex.quote(owner_payload)} > {shlex.quote(owner_tmp)} && "
            f"mv -f {shlex.quote(owner_tmp)} {shlex.quote(owner_file)} && "
            "printf 'NASBOX_LOCKED\\n' && cat >/dev/null"
        )
        scheduler_mode = bool(remote_script)
        self._scheduler_mode = scheduler_mode
        if scheduler_mode:
            remote_cmd = f"exec {shlex.quote(remote_script)} --transfer-wait"
        else:
            remote_cmd = (
                f"mkdir -p {shlex.quote(parent)} && "
                f"flock -E {REMOTE_LOCK_BUSY_EXIT_CODE} -x -w {self.timeout} {shlex.quote(lock_file)} "
                f"sh -c {shlex.quote(owner_script)}; rc=$?; "
                f"if [ \"$rc\" -eq {REMOTE_LOCK_BUSY_EXIT_CODE} ]; then "
                f"printf 'NASBOX_LOCK_BUSY\\n'; cat {shlex.quote(owner_file)} 2>/dev/null || true; fi; exit \"$rc\""
            )
        user = self.cfg.get("nas_user")
        try:
            self.proc = subprocess.Popen(
                ["ssh", *ssh_opts(self.cfg, self.conn), f"{user}@{self.conn.host}", remote_cmd],
                stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE,
            )
            if self.on_start:
                self.on_start(self.proc)
            if scheduler_mode:
                assert self.proc.stdin is not None
                self.proc.stdin.write(
                    (
                        "TRANSFER_WAIT_V1\0%s\0%d\0%s\0%s\0"
                        % (owner, self.priority, uuid.uuid4().hex, hostname)
                    ).encode()
                )
                self.proc.stdin.flush()
            try:
                response = self._read_stdout_line(self.timeout).decode(errors="replace").strip()
            except subprocess.TimeoutExpired as exc:
                # A queued scheduler request does not read stdin until it owns
                # the lease. Terminate SSH so the remote shell receives HUP and
                # runs its EXIT trap, removing the ticket instead of leaving a
                # ghost waiter.
                self.release(abort=True)
                if scheduler_mode:
                    raise RemoteLockBusy("tempo massimo di attesa del lock NAS superato") from exc
                raise RemoteLockError(f"impossibile acquisire il lock remoto: {exc}") from exc
            if response == "NASBOX_LOCKED":
                return self
            owner_id = owner_host = ""
            started_at = 0
            if response == "NASBOX_LOCK_BUSY":
                owner_line = self._read_stdout_line(5).decode(errors="replace").strip()
                owner_id, separator, remainder = owner_line.partition("|")
                if separator:
                    owner_host, separator, started = remainder.partition("|")
                    try:
                        started_at = int(started) if separator else 0
                    except ValueError:
                        started_at = 0
            _stdout, stderr = self.proc.communicate(timeout=5)
            detail = _clean_ssh_stderr(stderr.decode(errors="replace"))
            if self.proc.returncode == REMOTE_LOCK_BUSY_EXIT_CODE:
                error = RemoteLockBusy(
                    "lock occupato da un altro client", owner_id, owner_host, started_at,
                )
                self.release()
                raise error
            if not detail:
                raise RemoteLockError("risposta inattesa durante l'acquisizione del lock remoto")
            raise RemoteLockError(detail)
        except (OSError, subprocess.TimeoutExpired) as exc:
            self.release(abort=True)
            raise RemoteLockError(f"impossibile acquisire il lock remoto: {exc}") from exc

    def release(self, *, abort: bool = False) -> None:
        proc, self.proc = self.proc, None
        if proc is None:
            return
        try:
            if proc.stdin is not None:
                proc.stdin.close()
            if abort:
                proc.terminate()
            proc.wait(timeout=5 if abort else 10)
        except (OSError, subprocess.TimeoutExpired):
            try:
                proc.terminate()
            except OSError:
                pass
        finally:
            self._scheduler_mode = False
            for stream in (proc.stdin, proc.stdout, proc.stderr):
                if stream is not None:
                    try:
                        stream.close()
                    except OSError:
                        pass

    def set_activity(self, phase: str, done: int = 0, total: int = 0) -> None:
        """Publish diagnostics through the live scheduler lease, best-effort."""
        proc = self.proc
        if not self._scheduler_mode or proc is None or proc.stdin is None or proc.poll() is not None:
            return
        if not re.fullmatch(r"[a-z_]{1,32}", phase) or done < 0 or total < 0:
            return
        try:
            proc.stdin.write(f"ACTIVITY_V1\0{phase}\0{done}\0{total}\0".encode())
            proc.stdin.flush()
        except (OSError, BrokenPipeError):
            pass

    def __exit__(self, _exc_type, _exc, _tb) -> None:
        self.release()


def remote_lock(
    cfg: Config, conn: NasConnection, timeout: int = 45,
    on_start: Optional[Callable[[subprocess.Popen], None]] = None,
    owner_id: str = "", priority: int = 1,
) -> RemoteLock:
    return RemoteLock(
        cfg, conn, timeout=timeout, on_start=on_start,
        owner_id=owner_id, priority=priority,
    )


def check_port(host: str, port: int, timeout: float) -> bool:
    if not host:
        return False
    try:
        with socket.create_connection((host, port), timeout=timeout):
            return True
    except OSError:
        return False


def resolve_connection(cfg: Config) -> Optional[NasConnection]:
    lan = cfg.get("nas_lan")
    wan = cfg.get("nas_wan")
    port = int(cfg.get("ssh_port") or 22)

    if check_port(lan, port, 2):
        return NasConnection(host=lan, via_jump=False)
    if wan and check_port(wan, port, 5):
        return NasConnection(host=wan, via_jump=False)

    jump_host = cfg.get("jump_host")
    if jump_host:
        jump_port = int(cfg.get("jump_port") or 22)
        if check_port(jump_host, jump_port, 5):
            # The bastion is up and (by configuration) can reach the NAS on its own
            # LAN -- so we still SSH into the NAS's LAN address, just routed through it.
            return NasConnection(host=lan, via_jump=True)
    return None


def ssh_opts(cfg: Config, conn: NasConnection) -> list[str]:
    port = int(cfg.get("ssh_port") or 22)
    opts = [
        "-p", str(port),
        "-o", "StrictHostKeyChecking=yes" if cfg.get("ssh_host_key_pinned") else "StrictHostKeyChecking=accept-new",
        "-o", "ConnectTimeout=15",
        "-o", "ServerAliveInterval=30",
    ]
    known_hosts = (cfg.get("ssh_known_hosts") or "").strip()
    if cfg.get("ssh_host_key_pinned") and known_hosts:
        opts += ["-o", f"UserKnownHostsFile={known_hosts}"]
    if conn.via_jump:
        jump_user = cfg.get("jump_user") or cfg.get("nas_user")
        jump_host = cfg.get("jump_host")
        jump_port = int(cfg.get("jump_port") or 22)
        opts += ["-o", f"ProxyJump={jump_user}@{jump_host}:{jump_port}"]
    return opts


def _ssh_e_arg(cfg: Config, conn: NasConnection) -> str:
    return "ssh " + " ".join(shlex.quote(o) for o in ssh_opts(cfg, conn))


def _clean_ssh_stderr(text: str) -> str:
    """Strip OpenSSH's own advisory banners (e.g. the post-quantum-KEX warning
    newer clients print on every connection) so real errors aren't buried under
    noise that has nothing to do with our remote command."""
    lines = [ln for ln in text.splitlines() if not ln.strip().startswith("**")]
    return "\n".join(lines).strip()


def run_remote_script(
    cfg: Config, conn: NasConnection, script_path: str, args: list[str], timeout: float = 30,
    on_start: Optional[Callable[[subprocess.Popen], None]] = None,
) -> tuple[bool, str, str]:
    """Run the NAS-side sync-daemon-server.sh (or any remote command) over SSH,
    reusing whatever connection path (direct or via bastion) is currently active."""
    user = cfg.get("nas_user")
    remote_cmd = " ".join(shlex.quote(part) for part in [script_path, *args])
    cmd = ["ssh", *ssh_opts(cfg, conn), f"{user}@{conn.host}", remote_cmd]
    try:
        proc = subprocess.Popen(cmd, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
        if on_start is not None:
            on_start(proc)
        try:
            stdout, stderr = proc.communicate(timeout=timeout)
        except subprocess.TimeoutExpired:
            proc.kill()
            stdout, stderr = proc.communicate()
            return False, stdout or "", "timeout durante l'esecuzione remota"
    except OSError as exc:
        return False, "", str(exc)
    return proc.returncode == 0, stdout, _clean_ssh_stderr(stderr)


def parse_remote_revision_wait(output: str) -> RemoteRevisionWait | None:
    """Parse the deliberately small, line-oriented change-feed response."""
    lines = output.splitlines()
    if len(lines) != 3 or lines[0] != "NASBOX_CHANGE_WAIT_V1":
        return None
    values: dict[str, str] = {}
    for line in lines[1:]:
        key, separator, value = line.partition("=")
        if separator != "=" or key in values or key not in {"RESULT", "REVISION"}:
            return None
        values[key] = value
    result = values.get("RESULT", "")
    revision = values.get("REVISION", "")
    if result not in {"CHANGED", "TIMEOUT"} or not re.fullmatch(r"[0-9]{1,18}", revision):
        return None
    return RemoteRevisionWait(result, int(revision))


def wait_for_remote_revision(
    cfg: Config, conn: NasConnection, previous_revision: int,
    timeout: int | None = None, on_start: Optional[Callable[[subprocess.Popen], None]] = None,
) -> RemoteRevisionWait | None:
    """Wait remotely without taking the transfer lock or listing the share."""
    script_path = (cfg.get("remote_server_script") or "").strip()
    if not script_path or not isinstance(previous_revision, int) or isinstance(previous_revision, bool):
        return None
    if previous_revision < 0 or previous_revision >= 10**18:
        return None
    if timeout is None:
        timeout = cfg.get("change_feed_wait_seconds", 55)
    try:
        timeout = int(timeout)
    except (TypeError, ValueError):
        return None
    if timeout < 0 or timeout > 3600:
        return None
    ok, output, _error = run_remote_script(
        cfg, conn, script_path,
        ["--wait-for-revision", str(previous_revision), str(timeout)],
        timeout=max(20, timeout + 20), on_start=on_start,
    )
    if not ok:
        return None
    response = parse_remote_revision_wait(output)
    if response is None or response.revision < previous_revision:
        return None
    if response.result == "CHANGED" and response.revision == previous_revision:
        return None
    if response.result == "TIMEOUT" and response.revision != previous_revision:
        return None
    return response


def run_remote_script_bytes(
    cfg: Config, conn: NasConnection, script_path: str, args: list[str], timeout: float = 120,
) -> tuple[bool, bytes, str]:
    """Binary-safe counterpart for remote APIs returning NUL-delimited paths."""
    user = cfg.get("nas_user")
    remote_cmd = " ".join(shlex.quote(part) for part in [script_path, *args])
    try:
        proc = subprocess.run(
            ["ssh", *ssh_opts(cfg, conn), f"{user}@{conn.host}", remote_cmd],
            capture_output=True, timeout=timeout,
        )
    except subprocess.TimeoutExpired:
        return False, b"", "timeout durante l'esecuzione remota"
    return proc.returncode == 0, proc.stdout, _clean_ssh_stderr(proc.stderr.decode(errors="replace"))


def run_remote_script_input(
    cfg: Config, conn: NasConnection, script_path: str, args: list[str],
    input_data: bytes, timeout: float = 60,
) -> tuple[bool, str, str]:
    """Run a remote command while preserving a binary-safe stdin protocol."""
    user = cfg.get("nas_user")
    remote_cmd = " ".join(shlex.quote(part) for part in [script_path, *args])
    try:
        proc = subprocess.run(
            ["ssh", *ssh_opts(cfg, conn), f"{user}@{conn.host}", remote_cmd],
            input=input_data, capture_output=True, timeout=timeout,
        )
    except OSError as exc:
        return False, "", str(exc)
    except subprocess.TimeoutExpired:
        return False, "", "timeout durante l'esecuzione remota"
    return proc.returncode == 0, proc.stdout.decode(errors="replace"), _clean_ssh_stderr(
        proc.stderr.decode(errors="replace")
    )


def run_remote_script_input_bytes(
    cfg: Config, conn: NasConnection, script_path: str, args: list[str],
    input_data: bytes, timeout: float = 120,
) -> tuple[bool, bytes, str]:
    """Binary-safe stdin/stdout counterpart used by NUL-delimited protocols."""
    user = cfg.get("nas_user")
    remote_cmd = " ".join(shlex.quote(part) for part in [script_path, *args])
    try:
        proc = subprocess.run(
            ["ssh", *ssh_opts(cfg, conn), f"{user}@{conn.host}", remote_cmd],
            input=input_data, capture_output=True, timeout=timeout,
        )
    except OSError as exc:
        return False, b"", str(exc)
    except subprocess.TimeoutExpired:
        return False, b"", "timeout durante l'esecuzione remota"
    return proc.returncode == 0, proc.stdout, _clean_ssh_stderr(proc.stderr.decode(errors="replace"))


def discover_remote_server_update(
    cfg: Config, conn: NasConnection, active_script: str,
) -> tuple[str, str] | None:
    """Find the newest versioned server script beside the active one."""
    directory = active_script.rsplit("/", 1)[0] or "."
    glob = f"{shlex.quote(directory)}/sync-daemon-server-*.sh"
    remote_cmd = f'for candidate in {glob}; do [ -f "$candidate" ] && printf "%s\\n" "$candidate"; done'
    user = cfg.get("nas_user")
    try:
        proc = subprocess.run(
            ["ssh", *ssh_opts(cfg, conn), f"{user}@{conn.host}", remote_cmd],
            capture_output=True, text=True, timeout=20,
        )
    except (OSError, subprocess.TimeoutExpired):
        return None
    if proc.returncode != 0:
        return None
    candidates: list[tuple[str, str]] = []
    for raw_path in proc.stdout.splitlines():
        path = raw_path.strip()
        match = _SERVER_UPDATE_NAME_RE.fullmatch(Path(path).name)
        if match:
            candidates.append((path, match.group(1)))
    return max(candidates, key=lambda item: _version_tuple(item[1]), default=None)


def update_remote_server_script(
    cfg: Config, conn: NasConnection, active_script: str,
    versioned_script: str, expected_version: str,
) -> tuple[bool, str]:
    """Install a versioned server script, restart it, and verify its status."""
    active_dir = active_script.rsplit("/", 1)[0] or "."
    versioned_dir = versioned_script.rsplit("/", 1)[0] or "."
    versioned_name = Path(versioned_script).name
    match = _SERVER_UPDATE_NAME_RE.fullmatch(versioned_name)
    if versioned_dir != active_dir or match is None or match.group(1) != expected_version:
        return False, "percorso script server versionato non valido"

    quoted_source = shlex.quote(versioned_script)
    quoted_active = shlex.quote(active_script)
    remote_cmd = (
        f"cp -f {quoted_source} {quoted_active} && "
        f"chmod +x {quoted_active} && "
        f"{quoted_active} --restart && "
        f"{quoted_active} --print-config"
    )
    user = cfg.get("nas_user")
    try:
        proc = subprocess.run(
            ["ssh", *ssh_opts(cfg, conn), f"{user}@{conn.host}", remote_cmd],
            capture_output=True, text=True, timeout=90,
        )
    except (OSError, subprocess.TimeoutExpired) as exc:
        return False, str(exc)
    detail = (proc.stdout + proc.stderr).strip()
    if proc.returncode != 0:
        return False, detail or "aggiornamento server remoto fallito"
    values = dict(
        line.split("=", 1) for line in proc.stdout.splitlines() if "=" in line
    )
    if values.get("VERSION") != expected_version or values.get("RUNNING", "").lower() != "true":
        return False, (
            f"verifica server fallita: VERSION={values.get('VERSION', '?')}, "
            f"RUNNING={values.get('RUNNING', '?')}"
        )
    return True, detail or f"server aggiornato alla versione {expected_version}"


def restore_remote_version(
    cfg: Config, conn: NasConnection, trash_relative_path: str, destination: Path,
) -> tuple[bool, str]:
    """Download a NAS historical version to a temporary sibling then replace."""
    if not trash_relative_path or trash_relative_path.startswith("/") or ".." in trash_relative_path.split("/"):
        return False, "percorso storico remoto non valido"
    remote_file = f"{_remote_dir(cfg)}/{TRASH_DIRNAME}/{trash_relative_path}"
    destination.parent.mkdir(parents=True, exist_ok=True)
    temporary = destination.with_name(f".{destination.name}.nasbox-restore")
    user = cfg.get("nas_user")
    source = f"{user}@{conn.host}:{shlex.quote(remote_file)}"
    cmd = ["rsync", "-az", *_old_args_flag(cfg, conn), "-e", _ssh_e_arg(cfg, conn), source, str(temporary)]
    try:
        proc = subprocess.run(cmd, capture_output=True, text=True, timeout=120)
    except (OSError, subprocess.TimeoutExpired) as exc:
        return False, str(exc)
    if proc.returncode != 0:
        return False, _clean_ssh_stderr(proc.stderr) or "ripristino remoto fallito"
    try:
        temporary.replace(destination)
    except OSError as exc:
        return False, str(exc)
    return True, ""


def discover_remote_scripts(cfg: Config, conn: NasConnection) -> list[str]:
    """Best-effort: find running sync-daemon-server.sh instance(s) on the NAS via
    `ps`, so the user doesn't have to know/type the install path by hand. Only
    works if the daemon is actually running there (it always runs with its
    absolute path as the process's own argv, whether started via --start,
    systemd, or a DSM rc.d hook -- so a plain `ps` scrape is enough, no fixed
    path guessing). Returns every distinct path found (usually one) so a caller
    can flag ambiguity instead of silently trusting whichever one happened to
    sort first."""
    user = cfg.get("nas_user")
    # Do not depend on grep -oE: Synology BusyBox variants differ in the
    # supported grep flags. `ps aux` is available on the target platforms and
    # awk can inspect every command-line token without matching the grep
    # command itself.
    remote_cmd = (
        "ps aux 2>/dev/null | "
        "awk '{ for (i = 1; i <= NF; i++) "
        "if ($i ~ /sync-daemon-server[.]sh$/) print $i }' | sort -u"
    )
    cmd = ["ssh", *ssh_opts(cfg, conn), f"{user}@{conn.host}", remote_cmd]
    try:
        proc = subprocess.run(cmd, capture_output=True, text=True, timeout=20)
    except (OSError, subprocess.TimeoutExpired):
        return []
    if proc.returncode != 0:
        return []
    candidates = []
    for line in proc.stdout.splitlines():
        path = line.strip()
        if path and path not in candidates and "/" in path:
            candidates.append(path)
    return candidates


def _remote_dir(cfg: Config) -> str:
    """Absolute path on the NAS for the NASBox folder -- remote_prefix's own root,
    always (there is no per-folder subpath anymore)."""
    return cfg.get("remote_prefix").rstrip("/")


def ensure_remote_dir(cfg: Config, conn: NasConnection) -> bool:
    user = cfg.get("nas_user")
    remote_path = _remote_dir(cfg)
    cmd = ["ssh", *ssh_opts(cfg, conn), f"{user}@{conn.host}", f"mkdir -p {shlex.quote(remote_path)}"]
    try:
        subprocess.run(cmd, stdin=subprocess.DEVNULL, capture_output=True, timeout=20, check=True)
        return True
    except (subprocess.CalledProcessError, subprocess.TimeoutExpired):
        return False


def remote_file_digest(cfg: Config, conn: NasConnection, relative_path: str) -> str | None:
    """Return a SHA-256 for one remote regular file, None when it is absent."""
    if not relative_path or relative_path.startswith("/") or ".." in relative_path.split("/"):
        return None
    remote_path = f"{_remote_dir(cfg)}/{relative_path}"
    remote_cmd = (
        f"if [ -f {shlex.quote(remote_path)} ]; then "
        f"sha256sum {shlex.quote(remote_path)} | cut -d ' ' -f1; fi"
    )
    user = cfg.get("nas_user")
    try:
        proc = subprocess.run(
            ["ssh", *ssh_opts(cfg, conn), f"{user}@{conn.host}", remote_cmd],
            capture_output=True, text=True, timeout=30,
        )
    except (OSError, subprocess.TimeoutExpired):
        return None
    digest = proc.stdout.strip()
    return digest if proc.returncode == 0 and re.fullmatch(r"[0-9a-f]{64}", digest) else None


def remote_file_state(
    cfg: Config, conn: NasConnection, relative_path: str,
) -> tuple[bool, str | None] | None:
    """Return (exists, sha256) or None when the remote check failed."""
    if not relative_path or relative_path.startswith("/") or ".." in relative_path.split("/"):
        return None
    remote_path = f"{_remote_dir(cfg)}/{relative_path}"
    remote_cmd = (
        f"if [ -f {shlex.quote(remote_path)} ]; then "
        f"printf 'FILE '; sha256sum {shlex.quote(remote_path)} | cut -d ' ' -f1; "
        f"elif [ -e {shlex.quote(remote_path)} ]; then printf 'OTHER'; "
        f"else printf 'ABSENT'; fi"
    )
    user = cfg.get("nas_user")
    try:
        proc = subprocess.run(
            ["ssh", *ssh_opts(cfg, conn), f"{user}@{conn.host}", remote_cmd],
            capture_output=True, text=True, timeout=30,
        )
    except (OSError, subprocess.TimeoutExpired):
        return None
    value = proc.stdout.strip()
    if proc.returncode != 0:
        return None
    if value == "ABSENT":
        return False, None
    if value == "OTHER":
        return True, None
    if value.startswith("FILE ") and re.fullmatch(r"[0-9a-f]{64}", value[5:]):
        return True, value[5:]
    if value.startswith("FILE"):
        # A NAS may not ship sha256sum. Presence is still known, so callers
        # preserve the remote copy conservatively rather than overwriting it.
        return True, None
    return None


def remote_file_states(
    cfg: Config, conn: NasConnection, relative_paths: set[str] | list[str],
    *, compact: bool = True, on_progress: Optional[Callable[[int, int], None]] = None,
) -> dict[str, RemoteState] | None:
    """Fetch live file state and retained deletion markers in one SSH round-trip."""
    paths = sorted(set(relative_paths))
    if not paths:
        return {}
    script_path = (cfg.get("remote_server_script") or "").strip()
    if not script_path:
        return None

    if compact:
        compact_ok, _stdout, _error = run_remote_script(
            cfg, conn, script_path, ["--journal-compact"], timeout=120,
        )
        if not compact_ok:
            return None
    result: dict[str, RemoteState] = {}
    batch_size = 2048
    for batch_start in range(0, len(paths), batch_size):
        batch = paths[batch_start:batch_start + batch_size]
        fields = [b"FILE_STATES_V2", str(len(batch)).encode()]
        fields.extend(os.fsencode(path) for path in batch)
        payload = b"\0".join(fields) + b"\0"
        ok, output, error = run_remote_script_input_bytes(
            cfg, conn, script_path, ["--file-states-current"], payload, timeout=600,
        )
        protocol = b"FILE_STATES_V2"
        if not ok:
            # The endpoint is read-only; retrying its legacy wire format is
            # safe and keeps older servers usable without causal metadata.
            fields[0] = b"FILE_STATES_V1"
            payload = b"\0".join(fields) + b"\0"
            ok, output, _error = run_remote_script_input_bytes(
                cfg, conn, script_path, ["--file-states-current"], payload, timeout=600,
            )
            protocol = b"FILE_STATES_V1"
        if not ok:
            return None
        values = output.split(b"\0")
        if values and values[-1] == b"":
            values.pop()
        if len(values) < 2 or values[0] != protocol:
            return None
        try:
            count = int(values[1])
        except ValueError:
            return None
        fields_per_path = 6 if protocol == b"FILE_STATES_V2" else 5
        if count != len(batch) or len(values) != 2 + count * fields_per_path:
            return None
        for index in range(count):
            offset = 2 + index * fields_per_path
            path = os.fsdecode(values[offset])
            try:
                kind = RemoteKind(values[offset + 1].decode("ascii"))
                digest = values[offset + 2].decode("ascii")
                size = int(values[offset + 3])
                mtime_ns = int(values[offset + 4])
            except (UnicodeDecodeError, ValueError):
                return None
            if kind == RemoteKind.FILE and not re.fullmatch(r"[0-9a-f]{64}", digest):
                return None
            causal = None
            if fields_per_path == 6:
                try:
                    causal_text = values[offset + 5].decode("ascii", errors="strict")
                except UnicodeDecodeError:
                    return None
                causal = CausalVersion.parse(causal_text)
                if causal_text and causal is None:
                    return None
            result[path] = RemoteState(kind, digest, size, mtime_ns, causal)
        if on_progress is not None:
            on_progress(min(batch_start + len(batch), len(paths)), len(paths))
    return result


def checked_delete_remote(
    cfg: Config, conn: NasConnection,
    requests: list[tuple[str, str, int]], run_ts: str, device_id: str,
) -> CheckedDeleteResult:
    """Delete only files that still match the live fingerprint just observed."""
    if not requests:
        return CheckedDeleteResult(True, [], set(), set())
    script_path = (cfg.get("remote_server_script") or "").strip()
    if not script_path:
        return CheckedDeleteResult(False, [], set(), set(), "script server NAS non configurato")

    fields = [
        b"CHECKED_DELETE_V1", run_ts.encode("ascii"), device_id.encode("ascii"),
        str(len(requests)).encode(),
    ]
    for path, digest, mtime_seconds in requests:
        fields.extend((os.fsencode(path), digest.encode("ascii"), str(mtime_seconds).encode("ascii")))
    payload = b"\0".join(fields) + b"\0"
    ok, output, error = run_remote_script_input_bytes(
        cfg, conn, script_path, ["--checked-delete"], payload, timeout=120,
    )
    if not ok:
        return CheckedDeleteResult(False, [], set(), set(), error or "cancellazione condizionata fallita")
    values = output.split(b"\0")
    if values and values[-1] == b"":
        values.pop()
    if len(values) < 2 or values[0] != b"CHECKED_DELETE_V1":
        return CheckedDeleteResult(False, [], set(), set(), "risposta cancellazione NAS non valida")
    try:
        count = int(values[1])
    except ValueError:
        return CheckedDeleteResult(False, [], set(), set(), "conteggio cancellazioni NAS non valido")
    if count != len(requests) or len(values) != 2 + count * 2:
        return CheckedDeleteResult(False, [], set(), set(), "risposta cancellazione NAS incompleta")

    items: list[TransferItem] = []
    completed: set[str] = set()
    stale: set[str] = set()
    for index in range(count):
        path = os.fsdecode(values[2 + index * 2])
        status = values[3 + index * 2].decode("ascii", errors="replace")
        if status == "DELETED":
            items.append(TransferItem("delete_remote", path))
            completed.add(path)
        elif status == "ABSENT":
            completed.add(path)
        elif status == "STALE":
            stale.add(path)
        else:
            return CheckedDeleteResult(False, items, completed, stale, f"cancellazione rifiutata per {path}: {status}")
    return CheckedDeleteResult(True, items, completed, stale)


def create_remote_staging(cfg: Config, conn: NasConnection, transaction_id: str) -> str | None:
    """Create one private, same-filesystem staging tree for a publish batch."""
    if not re.fullmatch(r"[A-Za-z0-9._:-]{1,128}", transaction_id):
        return None
    staging_dir = f"{_remote_dir(cfg)}/{STAGING_DIRNAME}/{transaction_id}"
    user = cfg.get("nas_user")
    try:
        result = subprocess.run(
            ["ssh", *ssh_opts(cfg, conn), f"{user}@{conn.host}", "mkdir -p -- " + shlex.quote(staging_dir)],
            capture_output=True, text=True, timeout=30,
        )
    except (OSError, subprocess.TimeoutExpired):
        return None
    return staging_dir if result.returncode == 0 else None


def cleanup_remote_staging(cfg: Config, conn: NasConnection, staging_dir: str) -> None:
    """Best-effort cleanup after a completed publish; failed staging is retained."""
    expected_prefix = f"{_remote_dir(cfg)}/{STAGING_DIRNAME}/"
    if not staging_dir.startswith(expected_prefix):
        return
    user = cfg.get("nas_user")
    try:
        subprocess.run(
            ["ssh", *ssh_opts(cfg, conn), f"{user}@{conn.host}", "rm -rf -- " + shlex.quote(staging_dir)],
            capture_output=True, text=True, timeout=30,
        )
    except (OSError, subprocess.TimeoutExpired):
        pass


def publish_staging(
    cfg: Config, conn: NasConnection, staging_dir: str, transaction_id: str,
    device_id: str, fingerprints: dict[str, "Fingerprint"],
    causal_versions: dict[str, CausalVersion | None] | None = None,
) -> tuple[bool, str]:
    """Atomically publish new staged files while the caller owns the NAS lease."""
    script_path = (cfg.get("remote_server_script") or "").strip()
    if not script_path or not fingerprints:
        return False, "publish staging senza script o file"
    include_causal = bool(cfg.get("remote_causal_versions_available")) and causal_versions is not None
    fields = [
        b"STAGING_PUBLISH_V2" if include_causal else b"STAGING_PUBLISH_V1",
        os.fsencode(staging_dir), transaction_id.encode("ascii"),
        device_id.encode("ascii"), str(len(fingerprints)).encode("ascii"),
    ]
    for path, fingerprint in sorted(fingerprints.items()):
        fields.extend((
            os.fsencode(path), fingerprint.digest.encode("ascii"), str(fingerprint.size).encode("ascii"),
            str(fingerprint.mtime_ns).encode("ascii"),
        ))
        if include_causal:
            causal = (causal_versions or {}).get(path)
            fields.append(causal.encode().encode("ascii") if causal is not None else b"")
    ok, output, error = run_remote_script_input_bytes(
        cfg, conn, script_path, ["--staging-publish"], b"\0".join(fields) + b"\0", timeout=300,
    )
    if not ok and include_causal and (
        "opzione sconosciuta" in error.lower() or "unknown option" in error.lower()
        or "protocollo" in error.lower()
    ):
        # V2 is rejected before the old server mutates anything. Retry the
        # same publish without metadata, preserving old-server compatibility.
        legacy_fields = fields[:5]
        for index in range(len(fingerprints)):
            start = 5 + index * 5
            legacy_fields.extend(fields[start:start + 4])
        legacy_fields[0] = b"STAGING_PUBLISH_V1"
        ok, output, error = run_remote_script_input_bytes(
            cfg, conn, script_path, ["--staging-publish"], b"\0".join(legacy_fields) + b"\0", timeout=300,
        )
        expected_magic = b"STAGING_PUBLISH_V1"
    else:
        expected_magic = b"STAGING_PUBLISH_V2" if include_causal else b"STAGING_PUBLISH_V1"
    if not ok:
        return False, error or "pubblicazione staging fallita"
    values = output.split(b"\0")
    if values and values[-1] == b"":
        values.pop()
    if len(values) != 3 or values[:2] != [expected_magic, b"OK"]:
        return False, "risposta publish staging non valida"
    try:
        return int(values[2]) == len(fingerprints), ""
    except ValueError:
        return False, "conteggio publish staging non valido"


# --- interactive remote browsing ("Sfoglia NAS" tab) ---
#
# Unlike the sync engine's own push/pull, these are direct results of the user
# looking at a live listing and acting on it -- there's no local baseline to
# compare against (the browsed path may never have been synced by this client
# at all), so there's no staleness check to run client-side either; the NAS
# script is the sole authority and itself never does a raw unlink -- see
# server/sync-daemon-server.sh's cmd_browse_delete/cmd_browse_rename.

_BROWSE_PATH_VALID = lambda path: bool(path) and not path.startswith("/") and ".." not in path.split("/")


@dataclass
class BrowseEntry:
    name: str
    kind: str   # "FILE" | "DIR" | "OTHER" (symlink or other special file)
    size: int
    mtime: int  # epoch seconds -- the NAS side only has second resolution here (stat %Y)


def browse_list(cfg: Config, conn: NasConnection, relative_path: str) -> list[BrowseEntry] | None:
    """List the immediate children of relative_path on the NAS ("" = repository root)."""
    if relative_path and not _BROWSE_PATH_VALID(relative_path):
        return None
    script_path = (cfg.get("remote_server_script") or "").strip()
    if not script_path:
        return None
    ok, output, _error = run_remote_script_bytes(
        cfg, conn, script_path, ["--browse-list", relative_path], timeout=30,
    )
    if not ok:
        return None
    values = output.split(b"\0")
    if values and values[-1] == b"":
        values.pop()
    if len(values) < 2 or values[0] != b"BROWSE_LIST_V1":
        return None
    try:
        count = int(values[1])
    except ValueError:
        return None
    if count != len(values[1:]) // 4 or len(values) != 2 + count * 4:
        return None
    entries: list[BrowseEntry] = []
    for index in range(count):
        offset = 2 + index * 4
        try:
            name = os.fsdecode(values[offset])
            kind = values[offset + 1].decode("ascii")
            size = int(values[offset + 2])
            mtime = int(values[offset + 3])
        except (UnicodeDecodeError, ValueError):
            return None
        if kind not in ("FILE", "DIR", "OTHER"):
            return None
        entries.append(BrowseEntry(name, kind, size, mtime))
    return entries


def browse_delete(cfg: Config, conn: NasConnection, relative_path: str, device_id: str) -> tuple[bool, str]:
    """Move a remote file or folder into the NAS trash (same retention as sync)."""
    if not _BROWSE_PATH_VALID(relative_path):
        return False, "percorso non valido"
    script_path = (cfg.get("remote_server_script") or "").strip()
    if not script_path:
        return False, "script server NAS non configurato"
    try:
        validate_transfer_safety(cfg, conn, destructive=False, direction="upload")
    except RepositorySafetyError as exc:
        return False, str(exc)
    payload = b"\0".join((
        b"BROWSE_DELETE_V1", new_run_ts().encode("ascii"), device_id.encode("ascii"),
        os.fsencode(relative_path),
    )) + b"\0"
    try:
        # Browse mutations must use the same NAS transaction lock as push/pull.
        # The server command also takes the journal lock, so filesystem and
        # history updates cannot interleave with an rsync transaction.
        with remote_lock(cfg, conn, owner_id=device_id):
            ok, output, error = run_remote_script_input_bytes(
                cfg, conn, script_path, ["--browse-delete"], payload, timeout=120,
            )
    except RemoteLockBusy:
        return False, "NAS occupato da un'altra sincronizzazione; riprova tra poco"
    except RemoteLockError as exc:
        return False, str(exc)
    if not ok:
        return False, error or "cancellazione remota fallita"
    values = output.split(b"\0")
    if len(values) < 2 or values[0] != b"BROWSE_DELETE_V1":
        return False, "risposta del NAS non valida"
    if values[1] == b"OK":
        return True, ""
    detail = values[2].decode(errors="replace") if len(values) > 2 else ""
    return False, detail or "cancellazione remota fallita"


def browse_rename(
    cfg: Config, conn: NasConnection, src_relative: str, dst_relative: str, device_id: str,
) -> tuple[bool, str]:
    """Rename/move a remote file or folder within the NAS repository."""
    if not _BROWSE_PATH_VALID(src_relative) or not _BROWSE_PATH_VALID(dst_relative):
        return False, "percorso non valido"
    script_path = (cfg.get("remote_server_script") or "").strip()
    if not script_path:
        return False, "script server NAS non configurato"
    try:
        validate_transfer_safety(cfg, conn, destructive=False, direction="upload")
    except RepositorySafetyError as exc:
        return False, str(exc)
    payload = b"\0".join((
        b"BROWSE_RENAME_V1", new_run_ts().encode("ascii"), device_id.encode("ascii"),
        os.fsencode(src_relative), os.fsencode(dst_relative),
    )) + b"\0"
    try:
        with remote_lock(cfg, conn, owner_id=device_id):
            ok, output, error = run_remote_script_input_bytes(
                cfg, conn, script_path, ["--browse-rename"], payload, timeout=120,
            )
    except RemoteLockBusy:
        return False, "NAS occupato da un'altra sincronizzazione; riprova tra poco"
    except RemoteLockError as exc:
        return False, str(exc)
    if not ok:
        return False, error or "spostamento remoto fallito"
    values = output.split(b"\0")
    if len(values) < 2 or values[0] != b"BROWSE_RENAME_V1":
        return False, "risposta del NAS non valida"
    if values[1] == b"OK":
        return True, ""
    detail = values[2].decode(errors="replace") if len(values) > 2 else ""
    return False, detail or "spostamento remoto fallito"


def rename_remote(
    cfg: Config, conn: NasConnection, src_relative: str, dst_relative: str,
    device_id: str, *, kind: str, digest: str = "", size: int = 0,
    mtime_ns: int = 0, lease: RemoteLock | None = None,
) -> tuple[bool, str]:
    """Atomically publish a locally detected rename on the NAS.

    Unlike the interactive V1 browse command, V2 carries the live source
    precondition. The server moves the path and writes both journal records in
    one recoverable transaction, so a failed journal cannot leave a silent
    upload/delete split.
    """
    if (
        not _BROWSE_PATH_VALID(src_relative) or not _BROWSE_PATH_VALID(dst_relative)
        or src_relative == dst_relative
        or kind not in ("FILE", "DIR")
        or (kind == "FILE" and not re.fullmatch(r"[0-9a-f]{64}", digest))
        or size < 0 or mtime_ns < 0
    ):
        return False, "precondizione rename non valida"
    script_path = (cfg.get("remote_server_script") or "").strip()
    if not script_path:
        return False, "script server NAS non configurato"
    try:
        validate_transfer_safety(cfg, conn, destructive=False, direction="upload")
    except RepositorySafetyError as exc:
        return False, str(exc)
    payload = b"\0".join((
        b"BROWSE_RENAME_V2", new_run_ts().encode("ascii"), device_id.encode("ascii"),
        os.fsencode(src_relative), os.fsencode(dst_relative), kind.encode("ascii"),
        digest.encode("ascii"), str(size).encode("ascii"), str(mtime_ns).encode("ascii"),
    )) + b"\0"
    try:
        if lease is None:
            lease_context = remote_lock(cfg, conn, owner_id=device_id)
        else:
            lease_context = None
        if lease_context is not None:
            with lease_context:
                ok, output, error = run_remote_script_input_bytes(
                    cfg, conn, script_path, ["--browse-rename"], payload, timeout=300,
                )
        else:
            ok, output, error = run_remote_script_input_bytes(
                cfg, conn, script_path, ["--browse-rename"], payload, timeout=300,
            )
    except RemoteLockBusy:
        return False, "NAS occupato da un'altra sincronizzazione; riprova tra poco"
    except RemoteLockError as exc:
        return False, str(exc)
    if not ok:
        return False, error or "spostamento remoto fallito"
    values = output.split(b"\0")
    if len(values) < 2 or values[0] != b"BROWSE_RENAME_V2":
        return False, "risposta del NAS non valida"
    if values[1] == b"OK":
        return True, ""
    detail = values[2].decode(errors="replace") if len(values) > 2 else ""
    return False, detail or "spostamento remoto fallito"


def browse_download(
    cfg: Config, conn: NasConnection, relative_path: str, destination: Path,
) -> tuple[bool, str]:
    """Download one remote file to a local destination path (does not touch the sync baseline)."""
    if not _BROWSE_PATH_VALID(relative_path):
        return False, "percorso non valido"
    user = cfg.get("nas_user")
    source = f"{user}@{conn.host}:{shlex.quote(f'{_remote_dir(cfg)}/{relative_path}')}"
    destination.parent.mkdir(parents=True, exist_ok=True)
    temporary = destination.with_name(f".{destination.name}.nasbox-download")
    cmd = ["rsync", "-az", *_old_args_flag(cfg, conn), "-e", _ssh_e_arg(cfg, conn), source, str(temporary)]
    try:
        proc = subprocess.run(cmd, capture_output=True, text=True, timeout=600)
    except (OSError, subprocess.TimeoutExpired) as exc:
        return False, str(exc)
    if proc.returncode != 0:
        temporary.unlink(missing_ok=True)
        return False, _clean_ssh_stderr(proc.stderr) or "download fallito"
    try:
        temporary.replace(destination)
    except OSError as exc:
        return False, str(exc)
    return True, ""


def remote_manifest_state(
    cfg: Config, conn: NasConnection, relative_path: str,
) -> tuple[bool, str | None] | None:
    """Read the journal-backed state for one path, if the server supports it."""
    script_path = (cfg.get("remote_server_script") or "").strip()
    if not script_path or not relative_path:
        return None
    ok, stdout, _error = run_remote_script(
        cfg, conn, script_path, ["--manifest-get", relative_path], timeout=60,
    )
    if not ok:
        return None  # old server or transient failure: caller may use live rsync state
    line = next((value.strip() for value in stdout.splitlines() if value.strip()), "")
    if line == "MANIFEST_MISS":
        return False, None
    if not line.startswith("MANIFEST_HIT\t"):
        return None
    fields = line.split("\t")
    if len(fields) < 3:
        return None
    digest = fields[2]
    if not re.fullmatch(r"[0-9a-f]{64}", digest):
        return None
    return True, digest


def _decode_manifest_path(value: bytes) -> str:
    # Encoding order on the server is %, tab, newline, carriage return; decode
    # controls first and % last so a literal name such as "%0A" stays literal.
    for encoded, decoded in ((b"%09", b"\t"), (b"%0A", b"\n"), (b"%0D", b"\r"), (b"%25", b"%")):
        value = value.replace(encoded, decoded)
    return os.fsdecode(value)


def remote_manifest_snapshot(
    cfg: Config, conn: NasConnection, previous_revision: int,
) -> tuple[int, dict[str, RemoteState] | None] | None:
    """Fetch the compact manifest only when its logical revision changed."""
    script_path = (cfg.get("remote_server_script") or "").strip()
    if not script_path:
        return None
    ok, status, _error = run_remote_script(
        cfg, conn, script_path, ["--journal-status"], timeout=30,
    )
    if not ok:
        return None
    values = dict(line.split("=", 1) for line in status.splitlines() if "=" in line)
    try:
        revision = int(values.get("MANIFEST_REVISION", ""))
    except ValueError:
        return None
    if revision == previous_revision:
        return revision, None

    ok, output, _error = run_remote_script_bytes(
        cfg, conn, script_path, ["--manifest-export"], timeout=120,
    )
    if not ok:
        return None
    entries: dict[str, RemoteState] = {}
    lines = output.splitlines()
    if not lines or lines[0] != b"NASBOX_MANIFEST_V1":
        return None
    for line in lines[1:]:
        fields = line.split(b"\t")
        if len(fields) < 6:
            return None
        path = _decode_manifest_path(fields[0])
        try:
            digest = fields[1].decode("ascii", errors="strict")
            size = int(fields[2])
            mtime_ns = int(fields[3])
            event_seconds = int(fields[5])
        except (UnicodeDecodeError, ValueError):
            return None
        try:
            causal_text = fields[6].decode("ascii", errors="strict") if len(fields) >= 7 else ""
        except UnicodeDecodeError:
            return None
        causal = CausalVersion.parse(causal_text)
        if causal_text and causal is None:
            return None
        if digest:
            if not re.fullmatch(r"[0-9a-f]{64}", digest):
                return None
            entries[path] = RemoteState(RemoteKind.FILE, digest, size, mtime_ns, causal)
        else:
            entries[path] = RemoteState(
                RemoteKind.TOMBSTONE, "", 0, event_seconds * 1_000_000_000,
                causal,
            )
    return revision, entries


def copy_remote_file(cfg: Config, conn: NasConnection, source_relative: str, destination_relative: str) -> bool:
    """Copy a remote losing version to a conflict name while the lock is held."""
    valid = lambda path: path and not path.startswith("/") and ".." not in path.split("/")
    if not valid(source_relative) or not valid(destination_relative):
        return False
    source = f"{_remote_dir(cfg)}/{source_relative}"
    destination = f"{_remote_dir(cfg)}/{destination_relative}"
    parent = destination.rsplit("/", 1)[0]
    remote_cmd = (
        f"mkdir -p {shlex.quote(parent)} && cp -p {shlex.quote(source)} {shlex.quote(destination)}"
    )
    user = cfg.get("nas_user")
    try:
        return subprocess.run(
            ["ssh", *ssh_opts(cfg, conn), f"{user}@{conn.host}", remote_cmd],
            capture_output=True, timeout=30,
        ).returncode == 0
    except (OSError, subprocess.TimeoutExpired):
        return False


def upload_conflict_copy(
    cfg: Config, conn: NasConnection, source_relative: str, destination_relative: str,
) -> tuple[bool, str]:
    """Upload a losing local version under a unique remote conflict name."""
    valid = lambda path: path and not path.startswith("/") and ".." not in path.split("/")
    if not valid(source_relative) or not valid(destination_relative):
        return False, "percorso di conflitto non valido"
    source = Path(cfg.local_root(), source_relative)
    if not source.is_file():
        return False, "versione locale di conflitto non disponibile"
    destination = f"{_remote_dir(cfg)}/{destination_relative}"
    parent = destination.rsplit("/", 1)[0]
    user = cfg.get("nas_user")
    mkdir_cmd = [
        "ssh", *ssh_opts(cfg, conn), f"{user}@{conn.host}",
        f"mkdir -p {shlex.quote(parent)}",
    ]
    try:
        mkdir = subprocess.run(mkdir_cmd, capture_output=True, text=True, timeout=30)
        if mkdir.returncode != 0:
            return False, _clean_ssh_stderr(mkdir.stderr) or "cartella conflitto NAS non creata"
        remote = f"{user}@{conn.host}:{shlex.quote(destination)}"
        proc = subprocess.run(
            ["rsync", "-azc", "--no-owner", "--no-group", "--no-perms",
             *_old_args_flag(cfg, conn), "-e", _ssh_e_arg(cfg, conn), str(source), remote],
            capture_output=True, text=True, timeout=300,
        )
    except (OSError, subprocess.TimeoutExpired) as exc:
        return False, str(exc)
    if proc.returncode != 0:
        return False, _clean_ssh_stderr(proc.stderr) or "upload conflitto fallito"
    return True, ""


def _remote_uri(cfg: Config, conn: NasConnection) -> str:
    user = cfg.get("nas_user")
    return f"{user}@{conn.host}:{_remote_dir(cfg)}/"


def _server_state_exclude_arg(cfg: Config) -> list[str]:
    """If the NAS-side daemon's own state dir (PID file, lock, log -- self-reported
    via --print-config's STATE_DIR, refreshed into config.server_state_dir_remote by
    "Rileva dal NAS" and by SyncEngine's periodic health check) falls inside the
    synced tree, exclude it from sync. These are NAS-side-only runtime files
    created by the daemon itself, with no local counterpart and nothing to sync *to*
    locally -- left unexcluded, an ordinary push can delete them as a side effect
    (there's nothing local to match them against), making the daemon's own PID file
    vanish. SyncEngine._check_server_health would then see "not running" and restart
    it, risking a duplicate instance stacked on top of one that's actually still
    alive. Anchored (leading "/") so it only matches at the daemon's exact install
    location, not any unrelated folder elsewhere in the tree that happens to also be
    named "state" -- verified against a real rsync run, not just read from the docs.
    Nothing here is guessed client-side from remote_server_script's path: the daemon
    is the one that actually knows where its own state dir is, so it just says so."""
    state_dir = (cfg.get("server_state_dir_remote") or "").rstrip("/")
    if not state_dir:
        return []  # not known yet -- needs one successful --print-config round-trip first
    share_dir = _remote_dir(cfg)
    if state_dir == share_dir:
        return []  # the state dir *is* the NASBox root -- excluding "/" would exclude everything, so don't
    if not state_dir.startswith(share_dir + "/"):
        return []  # the daemon's state dir isn't inside the synced tree at all
    rel = state_dir[len(share_dir) + 1:]
    return ["--exclude", f"/{rel}"]


def server_package_excluded_path(cfg: Config) -> Optional[str]:
    """The sync-daemon-server package's own folder (the .sh scripts,
    server.conf -- not just its state/ subdir, see _server_state_exclude_arg
    below), as a path relative to the synced root, if and only if it falls
    *inside* the synced tree and therefore needs excluding. Returns None
    otherwise (not configured yet, or installed outside the tree, or the
    package folder somehow *is* the tree root) -- also used to show the user
    what's being auto-protected (settings_tab.py's info panel), not just to
    build the rsync argument (_server_package_exclude_arg below).

    This is a real setup, not a hypothetical: nothing stops someone from
    putting the server/ package under SHARE_ROOT itself (e.g.
    SHARE_ROOT/sync-daemon/server/), and without this, every client happily
    syncs down its own copy of the NAS's install scripts and server.conf
    into their local NASBox folder, right alongside actual user files --
    clutter nobody asked for, and a config file that has no business being
    duplicated onto every PC. The package's location is derived from
    remote_server_script (set by hand or by "Rileva dal NAS"), which is the
    one thing the client already knows about where the daemon lives -- no
    separate self-reported field needed like the state dir has, since a
    script's own path IS the thing pointing at its containing folder."""
    script_path = (cfg.get("remote_server_script") or "").strip()
    if not script_path or "/" not in script_path:
        return None  # not configured yet -- nothing to derive a folder from
    package_dir = script_path.rsplit("/", 1)[0]
    share_dir = _remote_dir(cfg)
    if package_dir == share_dir:
        return None  # the package IS the share root -- excluding "/" would exclude everything, so don't
    if not package_dir.startswith(share_dir + "/"):
        return None  # installed outside the synced tree entirely -- nothing to protect it from
    package_relative = package_dir[len(share_dir) + 1:]
    # The server script lives in SHARE_ROOT/sync-daemon/server, but the whole
    # package is local installation data and must never enter synchronization.
    return package_relative.split("/", 1)[0]


def _server_package_exclude_arg(cfg: Config) -> list[str]:
    rel = server_package_excluded_path(cfg)
    return ["--exclude", f"/{rel}"] if rel else []


def _exclude_args(cfg: Config) -> list[str]:
    args: list[str] = []
    for name in [*EXCLUDE_DIRNAMES, REPOSITORY_MARKER_NAME, f"{REPOSITORY_MARKER_NAME}.tmp*"]:
        args += ["--exclude", name]
    args += _server_package_exclude_arg(cfg)
    args += _server_state_exclude_arg(cfg)
    for pattern in cfg.exclude_patterns():
        pattern = pattern.strip()
        if pattern:
            args += ["--exclude", pattern]
    return args


def path_is_excluded(cfg: Config, relative_path: str) -> bool:
    """Apply the same practical exclude rules to pre-transfer conflict checks."""
    parts = [part for part in relative_path.replace("\\", "/").split("/") if part]
    if any(part in EXCLUDE_DIRNAMES or part == REPOSITORY_MARKER_NAME for part in parts):
        return True
    package = server_package_excluded_path(cfg)
    if package and (relative_path == package or relative_path.startswith(package.rstrip("/") + "/")):
        return True
    state_args = _server_state_exclude_arg(cfg)
    if state_args:
        state_relative = state_args[-1].lstrip("/")
        if relative_path == state_relative or relative_path.startswith(state_relative + "/"):
            return True
    for raw_pattern in cfg.exclude_patterns():
        pattern = raw_pattern.strip().replace("\\", "/")
        if not pattern:
            continue
        directory_pattern = pattern.rstrip("/")
        if pattern.endswith("/") and any(fnmatch.fnmatch(part, directory_pattern) for part in parts):
            return True
        if fnmatch.fnmatch(relative_path, pattern) or any(fnmatch.fnmatch(part, pattern) for part in parts):
            return True
    return False


def cleanup_local_partial(cfg: Config, paths: set[str] | list[str]) -> None:
    """Remove only completed pull partials, leaving interrupted transfers resumable."""
    partial_root = Path(cfg.local_root(), PARTIAL_DIRNAME)
    root = partial_root.resolve()
    for relative_path in paths:
        if not relative_path or relative_path.startswith("/") or ".." in relative_path.split("/"):
            continue
        candidate = partial_root / relative_path
        try:
            if candidate.resolve().parent != root and not str(candidate.resolve()).startswith(str(root) + os.sep):
                continue
            if candidate.is_file() or candidate.is_symlink():
                candidate.unlink()
            parent = candidate.parent
            while parent != root:
                parent.rmdir()
                parent = parent.parent
        except OSError:
            # Rsync normally removes these itself. A stale partial must not make
            # an otherwise committed pull fail or turn cleanup into data loss.
            continue


def _partial_transfer_args(*, append_verify: bool = False) -> list[str]:
    """Build the safe resume profile used by real transfers.

    --append-verify implies --inplace and cannot be combined with
    --delay-updates or --partial-dir. It is therefore reserved for private
    staging, where the incomplete destination is never a canonical file.
    """
    if append_verify:
        return ["--partial", "--append-verify"]
    return ["--partial", f"--partial-dir={PARTIAL_DIRNAME}", "--delay-updates"]


def _bwlimit_args(cfg: Config, direction: str) -> list[str]:
    key = "bandwidth_upload_kbps" if direction == "upload" else "bandwidth_download_kbps"
    kbps = int(cfg.get(key) or 0)
    return [f"--bwlimit={kbps}"] if kbps > 0 else []


def _parse_itemize_line(line: str, direction: str) -> Optional[TransferItem]:
    line = line.strip()
    if not line or "|" not in line:
        return None
    try:
        code, length, name = line.split("|", 2)
    except ValueError:
        return None
    code = code.strip()
    if not code:
        return None
    if code.startswith("*deleting"):
        del_dir = "delete_remote" if direction == "upload" else "delete_local"
        return TransferItem(direction=del_dir, path=_decode_rsync_name(name), size=0)
    if len(code) < 2 or code[1] != "f":
        return None  # only track regular files, skip dirs/symlinks/attr-only noise
    if code[0] not in ("<", ">"):
        return None  # no real content transfer (metadata-only / hardlink / unchanged)
    try:
        size = int(length)
    except ValueError:
        size = 0
    return TransferItem(direction=direction, path=_decode_rsync_name(name), size=size)


_RSYNC_OCTAL_ESCAPE = re.compile(rb"\\#([0-7]{3})")


def _decode_rsync_name(name: str) -> str:
    """Decode rsync's escaped octal filenames without losing raw Unix bytes."""
    raw = os.fsencode(name)
    raw = _RSYNC_OCTAL_ESCAPE.sub(lambda match: bytes([int(match.group(1), 8)]), raw)
    return os.fsdecode(raw)


def _parse_itemize(output: str, direction: str) -> list[TransferItem]:
    items: list[TransferItem] = []
    for line in output.splitlines():
        item = _parse_itemize_line(line, direction)
        if item:
            items.append(item)
    return items


# rsync --info=progress2, with LC_ALL=C forced so the numeric formatting is
# predictable (locale-dependent thousands/decimal separators otherwise break
# this -- e.g. Italian locale prints "20.971.520" and "554,69MB/s").
_PROGRESS_RE = re.compile(r"^\s*[\d,]+\s+(\d+)%\s+([\d.]+)(B|kB|MB|GB|TB)/s")
_UNIT_BYTES = {"B": 1, "kB": 1024, "MB": 1024**2, "GB": 1024**3, "TB": 1024**4}

# rsync appends "(xfr#N, to-chk=X/Y)" to the progress line for the instant a file's
# bytes finish landing -- this, not the itemize line above (which fires the moment
# rsync *starts* that file), is the true per-file completion signal.
_XFR_DONE_RE = re.compile(r"\(xfr#\d+,")


def _parse_progress_line(line: str) -> Optional[tuple[int, float]]:
    m = _PROGRESS_RE.match(line)
    if not m:
        return None
    percent = int(m.group(1))
    speed_bytes_per_sec = float(m.group(2)) * _UNIT_BYTES[m.group(3)]
    return percent, speed_bytes_per_sec


def _dry_run(
    cfg: Config, conn: NasConnection, direction: str,
    on_start: Optional[Callable[[subprocess.Popen], None]] = None,
    checksum: bool = False,
    strict: bool = False,
) -> list[TransferItem]:
    # The NAS is the hub. Uploads never mirror-delete the destination; local
    # deletions use checked_delete_remote() with a baseline precondition.
    delete_flag = [DELETE_FLAG] if cfg.get("delete_enabled") and direction == "download" else []
    local = cfg.local_root().rstrip("/") + "/"
    remote = _remote_uri(cfg, conn)
    src, dst = (local, remote) if direction == "upload" else (remote, local)

    cmd = [
        "rsync", "-aniz", "--dry-run",
        *( ["--checksum"] if checksum else ["-u"] ),
        *delete_flag,
        *_exclude_args(cfg),
        *_old_args_flag(cfg, conn),
        "--out-format=%i|%l|%n",
        "-e", _ssh_e_arg(cfg, conn),
        src, dst,
    ]
    try:
        # Popen (not run()) so a caller on another thread can terminate a scan
        # that's taking a while via on_start's Popen handle -- e.g. on app quit,
        # instead of being stuck waiting out the full timeout below.
        proc = subprocess.Popen(cmd, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, text=True)
    except OSError as exc:
        if strict:
            raise RepositorySafetyError(f"preflight rsync non avviabile: {exc}") from exc
        return []
    if on_start:
        on_start(proc)
    try:
        stdout, _ = proc.communicate(timeout=60)
    except subprocess.TimeoutExpired:
        proc.kill()
        proc.communicate()
        if strict:
            raise RepositorySafetyError("preflight rsync scaduto: cancellazioni bloccate")
        return []
    if strict and proc.returncode != 0:
        raise RepositorySafetyError(
            f"preflight rsync fallito (codice {proc.returncode}): cancellazioni bloccate"
        )
    return _parse_itemize(stdout, direction)


def scan(
    cfg: Config, conn: NasConnection,
    on_start: Optional[Callable[[subprocess.Popen], None]] = None,
) -> list[TransferItem]:
    """Return one non-contradictory preview item per path.

    The upload dry-run uses --update and uploads are non-destructive, so an
    upload candidate takes precedence over the opposite pull's delete_local
    for a file that exists only on this client.
    """
    uploads = _dry_run(cfg, conn, "upload", on_start=on_start) if cfg.allows_push() else []
    downloads = _dry_run(cfg, conn, "download", on_start=on_start) if cfg.allows_pull() else []
    merged = {item.path: item for item in downloads}
    for item in uploads:
        merged[item.path] = item
    return [merged[path] for path in sorted(merged)]


def validate_transfer_safety(
    cfg: Config, conn: NasConnection, destructive: bool = False,
    direction: str | None = None,
) -> None:
    """Validate that planning, rsync and journal all target the same repository.

    The remote marker is reported by the NAS server's authoritative config. A
    missing marker is never repaired implicitly: doing so could bless an
    unmounted volume or the wrong directory as the real repository.
    """
    validate_local_root(
        cfg.local_root(),
        str(cfg.get("repository_id") or ""),
        destructive=destructive,
    )
    if cfg.get("journal_error"):
        raise RepositorySafetyError(
            f"journal NAS non aggiornato dopo un trasferimento precedente: {cfg.get('journal_error')}"
        )
    if not cfg.get("repository_id"):
        raise RepositorySafetyError("repository NASBox non verificato sul NAS")
    script_path = (cfg.get("remote_server_script") or "").strip()
    if not script_path:
        raise RepositorySafetyError("script server NAS non configurato: marker non verificabile")
    ok, output, error = run_remote_script(cfg, conn, script_path, ["--print-config"], timeout=30)
    if not ok:
        raise RepositorySafetyError(
            f"impossibile verificare il marker NAS: {error or output or 'comando remoto fallito'}"
        )
    remote_values = {}
    for line in output.splitlines():
        key, separator, value = line.partition("=")
        if separator:
            remote_values[key.strip()] = value.strip()
    remote_version = remote_values.get("VERSION", "")
    if not remote_version or server_is_outdated(remote_version):
        raise RepositorySafetyError(
            f"server NAS non aggiornato: trovato {remote_version or 'versione sconosciuta'}, "
            f"richiesta almeno la {EXPECTED_SERVER_VERSION}"
        )
    remote_id = remote_values.get("REPOSITORY_ID", "")
    if remote_values.get("REPOSITORY_READY", "").lower() != "true" or not remote_id:
        raise RepositorySafetyError("marker repository NAS assente o non valido")
    if remote_values.get("JOURNAL_READY", "").lower() != "true":
        raise RepositorySafetyError("journal NAS non disponibile: sincronizzazione bloccata")
    if remote_values.get("PATH_RECONCILIATION_AVAILABLE", "").lower() != "true":
        raise RepositorySafetyError(
            "riconciliazione sicura non disponibile sul NAS: aggiorna il pacchetto server"
        )
    if remote_id != cfg.get("repository_id"):
        raise RepositorySafetyError("ID repository NAS cambiato: verifica il volume prima di sincronizzare")
    remote_root = _remote_dir(cfg)
    if remote_root in ("", "/"):
        raise RepositorySafetyError("percorso NASBox remoto non sicuro per le cancellazioni")
    server_root = remote_values.get("SHARE_ROOT", "").rstrip("/")
    if server_root != remote_root:
        raise RepositorySafetyError(
            f"percorso NAS incoerente: rsync usa {remote_root}, il server usa {server_root or '(vuoto)'}"
        )
    if remote_values.get("SYNC_LOCK_AVAILABLE", "").lower() != "true":
        raise RepositorySafetyError("lock multi-client non disponibile sul NAS")
    if destructive and direction == "download":
        candidates = _dry_run(cfg, conn, direction, strict=True)
        deletions = [item for item in candidates if item.direction.startswith("delete_")]
        try:
            max_deletes = int(cfg.get("max_delete_files") or 1000)
        except (TypeError, ValueError):
            max_deletes = 1000
        if len(deletions) > max_deletes:
            raise RepositorySafetyError(
                f"operazione bloccata: {len(deletions)} cancellazioni proposte, "
                f"limite di sicurezza {max_deletes}; controlla il volume e la cartella NASBox"
            )


def host_key_fingerprints(host: str, port: int) -> list[str]:
    """Return SSH host-key fingerprints without changing known_hosts."""
    try:
        scan = subprocess.run(
            ["ssh-keyscan", "-T", "6", "-p", str(port), host],
            capture_output=True,
            text=True,
            timeout=10,
        )
    except (OSError, subprocess.TimeoutExpired):
        return []
    if scan.returncode != 0 or not scan.stdout.strip():
        return []
    public_keys = []
    for line in scan.stdout.splitlines():
        fields = line.split()
        if len(fields) >= 3 and not fields[0].startswith("#"):
            public_keys.append(" ".join(fields[1:]))
    if not public_keys:
        return []
    try:
        fingerprints = subprocess.run(
            ["ssh-keygen", "-lf", "-", "-E", "sha256"],
            input="\n".join(public_keys) + "\n",
            capture_output=True,
            text=True,
            timeout=10,
        )
    except (OSError, subprocess.TimeoutExpired):
        return []
    out: list[str] = []
    for line in fingerprints.stdout.splitlines():
        fields = line.split()
        if len(fields) >= 2 and fields[1].startswith("SHA256:") and fields[1] not in out:
            out.append(fields[1])
    return out


def pin_host_key(host: str, port: int, known_hosts: str, fingerprint: str) -> tuple[bool, str]:
    """Store a user-confirmed host key in a dedicated known_hosts file."""
    try:
        scan = subprocess.run(
            ["ssh-keyscan", "-T", "6", "-p", str(port), host],
            capture_output=True,
            text=True,
            timeout=10,
        )
    except (OSError, subprocess.TimeoutExpired) as exc:
        return False, str(exc)
    if scan.returncode != 0 or not scan.stdout.strip():
        return False, "nessuna chiave host ricevuta"
    fingerprints = host_key_fingerprints(host, port)
    if fingerprint not in fingerprints:
        return False, "l'impronta scelta non corrisponde alla chiave ricevuta"
    try:
        target = Path(known_hosts).expanduser()
        target.parent.mkdir(parents=True, exist_ok=True)
        existing = target.read_text(encoding="utf-8") if target.exists() else ""
        additions = [line for line in scan.stdout.splitlines() if line and line not in existing.splitlines()]
        if additions:
            with target.open("a", encoding="utf-8") as stream:
                if existing and not existing.endswith("\n"):
                    stream.write("\n")
                stream.write("\n".join(additions) + "\n")
        os.chmod(target, 0o600)
    except OSError as exc:
        return False, str(exc)
    return True, str(target)


def integrity_check(cfg: Config, conn: NasConnection) -> list[TransferItem]:
    """Checksum both directions without writing anything.

    Unlike the normal queue preview this intentionally does not use `-u`:
    checksum comparison must report a difference even when timestamps have
    been preserved or clocks disagree.
    """
    uploads = _dry_run(cfg, conn, "upload", checksum=True) if cfg.allows_push() else []
    downloads = _dry_run(cfg, conn, "download", checksum=True) if cfg.allows_pull() else []
    return uploads + downloads


def _run_transfer(
    cfg: Config, conn: NasConnection, direction: str, run_ts: str,
    on_item: Optional[Callable[[TransferItem], None]] = None,
    on_item_started: Optional[Callable[[TransferItem], None]] = None,
    on_item_progress: Optional[Callable[[TransferItem, int], None]] = None,
    on_progress: Optional[Callable[[int, float], None]] = None,
    on_start: Optional[Callable[[subprocess.Popen], None]] = None,
    paths: set[str] | None = None,
    remote_destination: str | None = None,
    keep_backups: bool = True,
    append_verify: bool = False,
    cancel_check: Optional[Callable[[], bool]] = None,
) -> TransferResult:
    """Runs the real (non-dry-run) push/pull, streaming rsync's output line by
    line as it happens instead of waiting for the whole thing to finish. This
    lets callers get live per-file completion (on_item) and live speed
    (on_progress), and -- via on_start, which hands back the Popen -- lets a
    caller on another thread cancel an in-flight transfer (e.g. on app quit)
    instead of being stuck waiting for it for up to the full timeout."""
    delete_flag = [DELETE_FLAG] if cfg.get("delete_enabled") and direction == "download" else []
    local = cfg.local_root().rstrip("/") + "/"
    remote = remote_destination or _remote_uri(cfg, conn)

    if direction == "upload":
        src, dst = local, remote
        backup_dir = f"{_remote_dir(cfg)}/{TRASH_DIRNAME}"
    else:
        src, dst = remote, local
        from . import paths as client_paths
        backup_dir = str(client_paths.local_trash_dir())

    files_from_path: str | None = None
    selection_args: list[str] = []
    if paths is not None:
        if not paths:
            return TransferResult(ok=True, items=[])
        # The reconciliation plan has already compared authoritative content
        # fingerprints. Re-running rsync with --checksum would hash every file
        # a second time on both machines, which dominates batches of thousands
        # of small files. Force the selected paths instead: the NAS lease keeps
        # another client from changing them between planning and transfer.
        selection_args += ["--ignore-times"]
        if direction == "download":
            delete_flag = []  # tombstones are handled by the normal authoritative pull
        with tempfile.NamedTemporaryFile(prefix="nasbox-files-", delete=False) as stream:
            for relative_path in sorted(paths):
                stream.write(os.fsencode(relative_path))
                stream.write(b"\0")
            files_from_path = stream.name
        selection_args += ["--from0", f"--files-from={files_from_path}", "--relative"]
    elif direction == "upload":
        # A full upload has no preceding per-path reconciliation plan, so it
        # still needs rsync's content comparison.
        selection_args += ["--checksum"]
    elif direction == "download":
        # Protect a local edit that lands after PullWorker's clean preflight.
        # Journal-changed canonical files are applied by the following targeted
        # checksum pull, which deliberately does not use --update.
        selection_args += ["--update"]

    # A private staging destination is never exposed as a canonical file, so it
    # can safely use append+full verification. Canonical push/pull keeps
    # delay-updates to preserve atomic publication of complete files.
    use_append_verify = append_verify and remote_destination is not None and not keep_backups
    cmd = [
        "rsync", "-avz",
        # NASBox files belong to the SSH account, not to the remote UID/GID
        # from another machine. Avoid chgrp/chown/mode failures on Synology
        # and QNAP while preserving contents, timestamps and directory layout.
        "--no-owner", "--no-group", "--no-perms",
        "--no-links",
        *_old_args_flag(cfg, conn),
        *selection_args,
        *delete_flag,
        *_partial_transfer_args(append_verify=use_append_verify),
        *_bwlimit_args(cfg, direction),
        "--outbuf=L",
        "--progress",
        "--info=progress2",
        *(["--backup", f"--backup-dir={backup_dir}", f"--suffix=-{run_ts}"] if keep_backups else []),
        *_exclude_args(cfg),
        "--out-format=%i|%l|%n",
        "-e", _ssh_e_arg(cfg, conn),
        src, dst,
    ]

    env = dict(os.environ)
    env["LC_ALL"] = "C"  # keep rsync's numeric output in a predictable, parseable format

    try:
        proc = subprocess.Popen(
            cmd, stdout=subprocess.PIPE, stderr=subprocess.STDOUT,
            text=True, bufsize=1, env=env,
        )
    except OSError as exc:
        if files_from_path:
            Path(files_from_path).unlink(missing_ok=True)
        return TransferResult(ok=False, items=[], raw_error=str(exc))

    if on_start:
        on_start(proc)

    items: list[TransferItem] = []
    other_lines: list[str] = []
    other_bytes = 0
    diagnostics_truncated = False
    # Files rsync has announced (itemize line seen) but not yet actually finished
    # sending, in the order it announced them -- which is also the order it
    # completes them in, since one rsync process sends its files strictly
    # sequentially. Popped off as each "(xfr#N, ...)" completion marker arrives.
    pending_items: list[TransferItem] = []
    cancelled = False
    output_overflow = False

    def stop_process() -> None:
        try:
            if proc.poll() is None:
                proc.terminate()
        except OSError:
            pass

    def remember_diagnostic(line: str) -> None:
        nonlocal other_bytes, diagnostics_truncated
        if other_bytes >= MAX_TRANSFER_ERROR_BYTES:
            diagnostics_truncated = True
            return
        encoded = line.encode(errors="replace")
        remaining = MAX_TRANSFER_ERROR_BYTES - other_bytes
        if len(encoded) > remaining:
            encoded = encoded[:remaining]
            diagnostics_truncated = True
        other_lines.append(encoded.decode(errors="replace"))
        other_bytes += len(encoded)

    assert proc.stdout is not None
    try:
        for line in proc.stdout:
            if cancel_check is not None and cancel_check():
                cancelled = True
                stop_process()
                break
            line = line.rstrip("\n")
            item = _parse_itemize_line(line, direction)
            if item:
                if item.direction in ("delete_remote", "delete_local"):
                    # deletions are atomic on the wire -- no partial-progress concept,
                    # so reporting them the moment they're itemized is already correct.
                    if on_item_started:
                        on_item_started(item)
                    items.append(item)
                    if on_item:
                        on_item(item)
                else:
                    if on_item_started:
                        on_item_started(item)
                    pending_items.append(item)
                    if len(pending_items) > MAX_PENDING_TRANSFER_ITEMS:
                        # A broken/unsupported rsync output mode must not let a
                        # large tree grow an unbounded completion queue. Abort
                        # safely; --partial-dir keeps completed prefix bytes.
                        output_overflow = True
                        stop_process()
                        break
                continue
            progress = _parse_progress_line(line)
            if progress:
                if pending_items and on_item_progress:
                    on_item_progress(pending_items[0], progress[0])
                if on_progress:
                    on_progress(*progress)
                if pending_items and _XFR_DONE_RE.search(line):
                    done_item = pending_items.pop(0)
                    items.append(done_item)
                    if on_item:
                        on_item(done_item)
                continue
            if "|" in line:
                # The out-format also reports directories and metadata-only rows.
                # They are intentionally not TransferItems, but they must not be
                # accumulated as an error string on a large tree.
                try:
                    item_code, _item_length, _item_name = line.split("|", 2)
                except ValueError:
                    item_code = ""
                if item_code.strip().startswith((".", "<", ">", "*")):
                    continue
            stripped = line.strip()
            if stripped:
                remember_diagnostic(stripped)
    finally:
        try:
            proc.wait(timeout=10)
        except subprocess.TimeoutExpired:
            try:
                proc.kill()
            except OSError:
                pass
            proc.wait()
        if files_from_path:
            Path(files_from_path).unlink(missing_ok=True)
        try:
            proc.stdout.close()
        except OSError:
            pass

    # A partial transfer is retried. Treating exit 23 as success could commit a
    # baseline for only part of a causal batch and then let a pull destroy the
    # paths that vanished while rsync was reading them.
    # A signal-killed rsync is NOT reliably reported as a negative returncode --
    # empirically it can catch SIGTERM and exit with its own ordinary-looking
    # positive code (e.g. "received SIGUSR1 (code 19)" from an internal relay
    # between its own generator/sender children) -- so this isn't used to detect
    # our own cancel_current_transfer()/stop(); see SyncEngine._report_failure,
    # which checks engine state instead of trying to infer intent from the exit code.
    if cancel_check is not None and cancel_check():
        cancelled = True
    ok = proc.returncode == 0 and not cancelled and not output_overflow
    if output_overflow:
        remember_diagnostic("output rsync non compatibile: coda completioni oltre il limite")
    if diagnostics_truncated:
        remember_diagnostic("diagnostica rsync troncata")
    raw_error = "" if ok else _clean_ssh_stderr("\n".join(other_lines))
    if len(raw_error.encode()) > MAX_TRANSFER_ERROR_BYTES:
        raw_error = raw_error.encode()[:MAX_TRANSFER_ERROR_BYTES].decode(errors="replace")
    if direction == "download" and paths is not None and ok:
        cleanup_local_partial(cfg, paths)
    return TransferResult(
        ok=ok, items=items, raw_error=raw_error,
        cancelled=cancelled, partial_preserved=not ok,
    )


def push(
    cfg: Config, conn: NasConnection, run_ts: str,
    on_item: Optional[Callable[[TransferItem], None]] = None,
    on_item_started: Optional[Callable[[TransferItem], None]] = None,
    on_item_progress: Optional[Callable[[TransferItem, int], None]] = None,
    on_progress: Optional[Callable[[int, float], None]] = None,
    on_start: Optional[Callable[[subprocess.Popen], None]] = None,
    paths: set[str] | None = None,
    cancel_check: Optional[Callable[[], bool]] = None,
) -> TransferResult:
    return _run_transfer(
        cfg, conn, "upload", run_ts,
        on_item=on_item, on_item_started=on_item_started,
        on_item_progress=on_item_progress, on_progress=on_progress,
        on_start=on_start, paths=paths, cancel_check=cancel_check,
    )


def push_to_staging(
    cfg: Config, conn: NasConnection, run_ts: str, staging_dir: str,
    on_item: Optional[Callable[[TransferItem], None]] = None,
    on_item_started: Optional[Callable[[TransferItem], None]] = None,
    on_item_progress: Optional[Callable[[TransferItem, int], None]] = None,
    on_progress: Optional[Callable[[int, float], None]] = None,
    on_start: Optional[Callable[[subprocess.Popen], None]] = None,
    paths: set[str] | None = None,
    cancel_check: Optional[Callable[[], bool]] = None,
) -> TransferResult:
    user = cfg.get("nas_user")
    return _run_transfer(
        cfg, conn, "upload", run_ts,
        on_item=on_item, on_item_started=on_item_started,
        on_item_progress=on_item_progress, on_progress=on_progress,
        on_start=on_start, paths=paths, append_verify=True, cancel_check=cancel_check,
        remote_destination=f"{user}@{conn.host}:{staging_dir.rstrip('/')}/",
        keep_backups=False,
    )


def pull(
    cfg: Config, conn: NasConnection, run_ts: str,
    on_item: Optional[Callable[[TransferItem], None]] = None,
    on_item_started: Optional[Callable[[TransferItem], None]] = None,
    on_item_progress: Optional[Callable[[TransferItem, int], None]] = None,
    on_progress: Optional[Callable[[int, float], None]] = None,
    on_start: Optional[Callable[[subprocess.Popen], None]] = None,
    paths: set[str] | None = None,
    cancel_check: Optional[Callable[[], bool]] = None,
) -> TransferResult:
    return _run_transfer(
        cfg, conn, "download", run_ts,
        on_item=on_item, on_item_started=on_item_started,
        on_item_progress=on_item_progress, on_progress=on_progress,
        on_start=on_start, paths=paths, cancel_check=cancel_check,
    )


def build_remote_journal_payload(
    cfg: Config, device_id: str, items: list[TransferItem],
    fingerprints: dict[str, object] | None = None,
    causal_versions: dict[str, CausalVersion | None] | None = None,
) -> tuple[bytes | None, str]:
    """Build the binary-safe payload sent to ``--journal-append``."""
    script_path = (cfg.get("remote_server_script") or "").strip()
    if not script_path:
        return None, "script server NAS non configurato"
    if not items:
        return b"", ""

    payload = bytearray()

    def field(value: str | bytes) -> None:
        payload.extend(os.fsencode(value))
        payload.append(0)

    repository_id = str(cfg.get("repository_id") or "")
    if not repository_id:
        return None, "repository NAS non identificato"
    include_causal = bool(cfg.get("remote_causal_versions_available")) and causal_versions is not None
    field("JOURNAL_V3" if include_causal else "JOURNAL_V2")
    field(repository_id)
    field(new_run_ts())
    field(device_id)
    field(str(int(time.time())))
    field(str(len(items)))

    for item in items:
        if not item.path or item.path.startswith("/") or ".." in item.path.split("/"):
            return None, f"percorso non valido nel journal: {item.path!r}"
        action = "DELETE" if item.direction.startswith("delete_") else "PUT"
        digest = ""
        size = 0
        mtime = 0
        if action == "PUT":
            fp = fingerprints.get(item.path) if fingerprints is not None else None
            if fp is not None:
                digest = str(getattr(fp, "digest"))
                size = int(getattr(fp, "size"))
                mtime = int(getattr(fp, "mtime_ns"))
            else:
                local_path = Path(cfg.local_root(), item.path)
                try:
                    stat = local_path.stat()
                    if not local_path.is_file():
                        return None, f"file trasferito non disponibile per il journal: {item.path}"
                    with local_path.open("rb") as stream:
                        digest = hashlib.file_digest(stream, "sha256").hexdigest()
                    size = stat.st_size
                    mtime = stat.st_mtime_ns
                except OSError as exc:
                    return None, f"impossibile calcolare il journal di {item.path}: {exc}"
        field(action)
        field(item.path)
        field(digest)
        field(str(size))
        field(str(mtime))
        if include_causal:
            causal = causal_versions.get(item.path)
            field(causal.encode() if causal is not None else "")

    return bytes(payload), ""


def append_remote_journal(
    cfg: Config, conn: NasConnection, device_id: str, items: list[TransferItem],
    fingerprints: dict[str, object] | None = None,
    causal_versions: dict[str, CausalVersion | None] | None = None,
) -> tuple[bool, str]:
    """Append completed transfer results to the NAS journal.

    The payload is NUL-delimited so filenames containing whitespace, tabs or
    newlines cannot change the record boundaries on the NAS.
    """
    payload, error = build_remote_journal_payload(
        cfg, device_id, items, fingerprints, causal_versions,
    )
    if payload is None:
        return False, error
    if not payload:
        return True, ""
    script_path = (cfg.get("remote_server_script") or "").strip()
    ok, stdout, stderr = run_remote_script_input(
        cfg, conn, script_path, ["--journal-append"], payload, timeout=120,
    )
    detail = (stdout + stderr).strip()
    if not ok and ("opzione sconosciuta" in detail.lower() or "unknown option" in detail.lower()
                   or "protocollo non riconosciuto" in detail.lower()):
        if payload.startswith(b"JOURNAL_V3\0"):
            # V3 is an additive capability. An older server rejected the
            # header before appending anything, so the V2 retry is safe.
            legacy, legacy_error = build_remote_journal_payload(cfg, device_id, items, fingerprints)
            if legacy:
                legacy_ok, legacy_stdout, legacy_stderr = run_remote_script_input(
                    cfg, conn, script_path, ["--journal-append"], legacy, timeout=120,
                )
                if legacy_ok:
                    return True, (legacy_stdout + legacy_stderr).strip()
                return False, (legacy_stdout + legacy_stderr).strip() or legacy_error
        return True, "journal non supportato dal server remoto"
    return ok, detail


def save_pending_journal(payload: bytes) -> None:
    from . import paths
    paths.ensure_dirs()
    target = paths.journal_pending_file()
    temporary = target.with_suffix(".bin.tmp")
    temporary.write_bytes(payload)
    temporary.replace(target)


def retry_pending_journal(cfg: Config, conn: NasConnection) -> tuple[bool, str]:
    from . import paths
    target = paths.journal_pending_file()
    if not target.exists():
        return True, ""
    try:
        payload = target.read_bytes()
    except OSError as exc:
        return False, str(exc)
    script_path = (cfg.get("remote_server_script") or "").strip()
    if not script_path:
        return False, "script server NAS non configurato"
    ok, stdout, stderr = run_remote_script_input(
        cfg, conn, script_path, ["--journal-append"], payload, timeout=120,
    )
    detail = (stdout + stderr).strip()
    if not ok and ("opzione sconosciuta" in detail.lower() or "unknown option" in detail.lower()):
        try:
            target.unlink()
        except OSError:
            pass
        return True, "journal non supportato dal server remoto"
    if ok:
        try:
            target.unlink()
        except OSError:
            pass
    return ok, detail


RUN_TS_FORMAT = "%Y-%m-%d--%H-%M-%S"
UTC_RUN_TS_FORMAT = "%Y-%m-%d--%H-%M-%S-%fZ"


def new_run_ts() -> str:
    # UTC avoids retention depending on each client's timezone; microseconds
    # prevent two clients overwriting a file in the same second from sharing a
    # backup filename.
    return datetime.now(timezone.utc).strftime(UTC_RUN_TS_FORMAT)
