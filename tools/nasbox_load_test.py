#!/usr/bin/env python3
"""Repeatable local load test for the NASBox shell protocol.

The test uses a disposable repository and never touches the configured NAS.
It exercises journal append, manifest export, the read-only metrics endpoint,
and the revision wait endpoint. Real rsync/SSH throughput must be measured
separately on a lab NAS with the commands documented in tests/LOAD_TESTS.md.
"""
from __future__ import annotations

import argparse
import fcntl
import hashlib
import json
import shutil
import subprocess
import tempfile
import time
from pathlib import Path


def percentile(values: list[float], fraction: float) -> float:
    ordered = sorted(values)
    index = min(len(ordered) - 1, int(round((len(ordered) - 1) * fraction)))
    return ordered[index]


class LoadSandbox:
    def __init__(self) -> None:
        self.directory = tempfile.TemporaryDirectory(prefix="nasbox-load-")
        self.root = Path(self.directory.name)
        self.share = self.root / "share"
        self.share.mkdir()
        source = Path(__file__).resolve().parents[1] / "server" / "sync-daemon-server.sh"
        self.script = self.root / "server.sh"
        shutil.copy2(source, self.script)
        self.script.chmod(0o755)
        self.config = self.root / "server.conf"
        self.config.write_text(f"SHARE_ROOT={self.share}\nRETENTION_DAYS=30\n", encoding="utf-8")

    @property
    def state(self) -> Path:
        return self.root / "state"

    def run(self, *args: str, input: bytes = b"", timeout: float = 120) -> subprocess.CompletedProcess[bytes]:
        return subprocess.run(
            [str(self.script), "-c", str(self.config), *args],
            input=input, capture_output=True, timeout=timeout,
        )

    def append_as_device(self, payload: bytes) -> subprocess.CompletedProcess[bytes]:
        lock_path = self.state / "sync-transfer.lock"
        owner_path = self.state / "sync-transfer.lock.owner"
        lock = lock_path.open("w")
        fcntl.flock(lock, fcntl.LOCK_EX)
        owner_path.write_text("load-test|local|0|testing|0|0\n", encoding="utf-8")
        try:
            return self.run("--journal-append", input=payload)
        finally:
            owner_path.unlink(missing_ok=True)
            fcntl.flock(lock, fcntl.LOCK_UN)
            lock.close()

    def close(self) -> None:
        self.directory.cleanup()


def build_journal_payload(sandbox: LoadSandbox, count: int, payload_size: int) -> bytes:
    repository = next(
        line.partition("=")[2]
        for line in sandbox.run("--print-config").stdout.decode().splitlines()
        if line.startswith("REPOSITORY_ID=")
    )
    fields = [b"JOURNAL_V2", repository.encode(), b"load-test-tx", b"load-test", b"1700000000", str(count).encode()]
    for index in range(count):
        relative = f"load/{index:06d}.bin"
        path = sandbox.share / relative
        content = bytes([index % 251]) * payload_size
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_bytes(content)
        fields.extend((
            b"PUT", relative.encode(), hashlib.sha256(content).hexdigest().encode(),
            str(len(content)).encode(), str(path.stat().st_mtime_ns).encode(),
        ))
    return b"\0".join(fields) + b"\0"


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--files", type=int, default=1000)
    parser.add_argument("--size", type=int, default=4096, help="bytes per generated file")
    parser.add_argument("--metrics-samples", type=int, default=5)
    args = parser.parse_args()
    if args.files < 1 or args.files > 100000 or args.size < 0 or args.size > 100 * 1024 * 1024:
        parser.error("files deve essere 1..100000 e size 0..104857600")
    if args.metrics_samples < 1 or args.metrics_samples > 100:
        parser.error("metrics-samples deve essere 1..100")

    sandbox = LoadSandbox()
    waiter: subprocess.Popen[bytes] | None = None
    try:
        initialized = sandbox.run("--init-repository")
        if initialized.returncode != 0:
            raise RuntimeError(initialized.stderr.decode(errors="replace"))

        payload = build_journal_payload(sandbox, args.files, args.size)
        wait_started = time.monotonic()
        waiter = subprocess.Popen(
            [str(sandbox.script), "-c", str(sandbox.config), "--wait-for-revision", "0", "60"],
            stdout=subprocess.PIPE, stderr=subprocess.PIPE,
        )
        time.sleep(0.1)
        started = time.monotonic()
        appended = sandbox.append_as_device(payload)
        append_seconds = time.monotonic() - started
        if appended.returncode != 0:
            raise RuntimeError(appended.stderr.decode(errors="replace"))
        wait_output, wait_error = waiter.communicate(timeout=65)
        wait_seconds = time.monotonic() - wait_started
        if waiter.returncode != 0:
            raise RuntimeError(wait_error.decode(errors="replace"))

        export_started = time.monotonic()
        exported = sandbox.run("--manifest-export")
        export_seconds = time.monotonic() - export_started
        if exported.returncode != 0:
            raise RuntimeError(exported.stderr.decode(errors="replace"))
        manifest_entries = max(0, len(exported.stdout.splitlines()) - 1)

        metric_times: list[float] = []
        metric_keys: set[str] = set()
        for _ in range(args.metrics_samples):
            started = time.monotonic()
            metrics = sandbox.run("--metrics", timeout=10)
            metric_times.append(time.monotonic() - started)
            if metrics.returncode != 0:
                raise RuntimeError(metrics.stderr.decode(errors="replace"))
            metric_keys.update(line.partition("=")[0] for line in metrics.stdout.decode().splitlines()[1:])

        result = {
            "files": args.files,
            "bytes": args.files * args.size,
            "append_seconds": round(append_seconds, 4),
            "manifest_export_seconds": round(export_seconds, 4),
            "manifest_entries": manifest_entries,
            "change_wait_seconds": round(wait_seconds, 4),
            "metrics_p50_seconds": round(percentile(metric_times, 0.50), 4),
            "metrics_p95_seconds": round(percentile(metric_times, 0.95), 4),
            "metrics_keys": sorted(metric_keys),
            "revision": (sandbox.state / "manifest.revision").read_text(encoding="utf-8").strip(),
            "wait_response": wait_output.decode(errors="replace").strip().splitlines(),
        }
        print(json.dumps(result, indent=2, sort_keys=True))
        return 0
    finally:
        if waiter is not None and waiter.poll() is None:
            waiter.kill()
            waiter.communicate()
        sandbox.close()


if __name__ == "__main__":
    raise SystemExit(main())
