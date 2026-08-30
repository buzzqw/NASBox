"""Reusable isolated fixtures for client and server contract tests."""
from __future__ import annotations

import os
import fcntl
import shutil
import subprocess
import tempfile
from pathlib import Path
from unittest.mock import patch


class ClientEnvironment:
    """Temporary XDG directories for tests that instantiate Config/SyncStateStore."""

    def __init__(self) -> None:
        self._temporary = tempfile.TemporaryDirectory(prefix="nasbox-client-test-")
        root = Path(self._temporary.name)
        self.config_home = root / "config"
        self.state_home = root / "state"
        self._patcher = patch.dict(
            os.environ,
            {
                "XDG_CONFIG_HOME": str(self.config_home),
                "XDG_STATE_HOME": str(self.state_home),
            },
        )

    def __enter__(self) -> "ClientEnvironment":
        self._patcher.start()
        return self

    def __exit__(self, _exc_type, _exc, _tb) -> None:
        self._patcher.stop()
        self._temporary.cleanup()


class ServerSandbox:
    """Run the real server script against a disposable share and state directory."""

    def __init__(self, **config: object) -> None:
        self._temporary = tempfile.TemporaryDirectory(prefix="nasbox-server-test-")
        self.root = Path(self._temporary.name)
        self.share = self.root / "share"
        self.share.mkdir()
        source = Path(__file__).resolve().parents[2] / "server" / "sync-daemon-server.sh"
        self.script = self.root / "server.sh"
        shutil.copy2(source, self.script)
        self.script.chmod(0o755)
        self.config = self.root / "server.conf"
        values = {"SHARE_ROOT": str(self.share), "RETENTION_DAYS": "30", **config}
        self.config.write_text(
            "".join(f"{key}={value}\n" for key, value in values.items()),
            encoding="utf-8",
        )

    @property
    def state(self) -> Path:
        return self.root / "state"

    def run(
        self, *arguments: str, input: bytes = b"", env: dict[str, str] | None = None,
        timeout: float = 30,
    ) -> subprocess.CompletedProcess[bytes]:
        process_env = os.environ.copy()
        if env:
            process_env.update(env)
        return subprocess.run(
            [str(self.script), "-c", str(self.config), *arguments],
            input=input,
            capture_output=True,
            env=process_env,
            timeout=timeout,
        )

    def run_with_lease(
        self, *arguments: str, device: str = "device-a", input: bytes = b"",
        env: dict[str, str] | None = None, timeout: float = 30,
    ) -> subprocess.CompletedProcess[bytes]:
        """Run a mutating server command as the owner of the test lease."""
        self.state.mkdir(parents=True, exist_ok=True)
        lock_path = self.state / "sync-transfer.lock"
        owner_path = self.state / "sync-transfer.lock.owner"
        lock = lock_path.open("w")
        fcntl.flock(lock, fcntl.LOCK_EX)
        owner_path.write_text(f"{device}|test-host|0|idle|0|0\n", encoding="utf-8")
        try:
            return self.run(*arguments, input=input, env=env, timeout=timeout)
        finally:
            owner_path.unlink(missing_ok=True)
            fcntl.flock(lock, fcntl.LOCK_UN)
            lock.close()

    def init_repository(self) -> str:
        result = self.run("--init-repository")
        if result.returncode != 0:
            raise AssertionError(result.stderr.decode(errors="replace"))
        values = self.run("--print-config")
        if values.returncode != 0:
            raise AssertionError(values.stderr.decode(errors="replace"))
        for line in values.stdout.decode().splitlines():
            if line.startswith("REPOSITORY_ID="):
                return line.partition("=")[2]
        raise AssertionError("server sandbox did not expose a repository ID")

    def __enter__(self) -> "ServerSandbox":
        return self

    def __exit__(self, _exc_type, _exc, _tb) -> None:
        self._temporary.cleanup()
