"""The NAS declares what it can do (--capabilities and the end of --print-config)
so the clients can adapt instead of assuming: change feed, CPU count, memory,
disk type, rsync version. Every key has a safe fallback, so a client that does
not know a capability keeps working as before.
"""
from __future__ import annotations

import unittest

from tests.support import ServerSandbox


def _values(raw: str) -> dict[str, str]:
    result: dict[str, str] = {}
    for line in raw.splitlines():
        if "=" in line:
            key, _, value = line.partition("=")
            result[key] = value
    return result


class CapabilitiesTests(unittest.TestCase):
    def test_capabilities_block_lists_what_the_nas_can_do(self) -> None:
        with ServerSandbox() as sandbox:
            done = sandbox.run("--capabilities")
            self.assertEqual(done.returncode, 0, done.stderr.decode())
            text = done.stdout.decode()
            self.assertTrue(text.startswith("CAPABILITIES_V1\n"))
            values = _values(text)

            for key in (
                "SERVER_VERSION", "CAPABILITIES", "CHANGE_FEED_AVAILABLE",
                "BROWSE_RENAME_PROTOCOLS", "CPU_COUNT", "MEM_TOTAL_MB",
                "DISK_ROTATIONAL", "RSYNC_VERSION", "FS_TYPE",
                "FILE_STATES_PARALLELISM",
            ):
                self.assertIn(key, values, key)

            self.assertIn("change_feed", values["CAPABILITIES"])
            self.assertIn("v3_rename", values["CAPABILITIES"])
            self.assertIn("BROWSE_RENAME_V3", values["BROWSE_RENAME_PROTOCOLS"])
            self.assertEqual(values["CHANGE_FEED_AVAILABLE"], "true")
            self.assertGreaterEqual(int(values["CPU_COUNT"]), 1)
            # One hasher per core, never more than eight, never zero.
            self.assertGreaterEqual(int(values["FILE_STATES_PARALLELISM"]), 1)
            self.assertLessEqual(int(values["FILE_STATES_PARALLELISM"]), 8)

    def test_print_config_carries_the_capabilities_too(self) -> None:
        with ServerSandbox() as sandbox:
            done = sandbox.run("--print-config")
            self.assertEqual(done.returncode, 0, done.stderr.decode())
            values = _values(done.stdout.decode())
            self.assertIn("CAPABILITIES", values)
            self.assertIn("CPU_COUNT", values)
            self.assertIn("FILE_STATES_PARALLELISM", values)
            # The auto-configuration keys the clients already relied on stay.
            self.assertIn("SHARE_ROOT", values)
            self.assertIn("VERSION", values)

    def test_parallelism_can_be_forced_from_the_config(self) -> None:
        with ServerSandbox(FILE_STATES_PARALLELISM="3") as sandbox:
            done = sandbox.run("--capabilities")
            self.assertEqual(done.returncode, 0, done.stderr.decode())
            self.assertEqual(_values(done.stdout.decode())["FILE_STATES_PARALLELISM"], "3")

    def test_invalid_parallelism_is_refused_at_startup(self) -> None:
        with ServerSandbox(FILE_STATES_PARALLELISM="molti") as sandbox:
            done = sandbox.run("--capabilities")
            self.assertNotEqual(done.returncode, 0)
            self.assertIn("FILE_STATES_PARALLELISM", done.stderr.decode())


if __name__ == "__main__":
    unittest.main()


class PrivateRsyncTests(unittest.TestCase):
    """A newer rsync can live next to the server script (server/bin). The system
    one is left alone: the NAS only declares the private one, and a client that
    knows the capability asks for it with --rsync-path."""

    def _fake_rsync(self, sandbox: ServerSandbox, version: str, exit_code: int = 0) -> None:
        bin_dir = sandbox.root / "bin"
        bin_dir.mkdir(exist_ok=True)
        wrapper = bin_dir / "nasbox-rsync"
        wrapper.write_text(
            f"#!/bin/sh\necho 'rsync  version {version}  protocol version 33'\nexit {exit_code}\n",
            encoding="utf-8",
        )
        wrapper.chmod(0o755)

    def test_private_rsync_is_advertised_when_it_runs(self) -> None:
        with ServerSandbox() as sandbox:
            self._fake_rsync(sandbox, "9.9.9")
            done = sandbox.run("--capabilities")
            self.assertEqual(done.returncode, 0, done.stderr.decode())
            values = _values(done.stdout.decode())
            self.assertEqual(values["RSYNC_PATH"], str(sandbox.root / "bin" / "nasbox-rsync"))
            self.assertEqual(values["RSYNC_VERSION"], "9.9.9")
            self.assertIn("private_rsync", values["CAPABILITIES"])
            # The system version stays visible for diagnostics.
            self.assertIn("RSYNC_SYSTEM_VERSION", values)

    def test_broken_private_rsync_falls_back_to_the_system_one(self) -> None:
        with ServerSandbox() as sandbox:
            self._fake_rsync(sandbox, "9.9.9", exit_code=1)
            done = sandbox.run("--capabilities")
            self.assertEqual(done.returncode, 0, done.stderr.decode())
            values = _values(done.stdout.decode())
            self.assertEqual(values["RSYNC_PATH"], "")
            self.assertNotIn("private_rsync", values["CAPABILITIES"])
            self.assertNotEqual(values["RSYNC_VERSION"], "9.9.9")

    def test_print_config_carries_the_rsync_path(self) -> None:
        with ServerSandbox() as sandbox:
            done = sandbox.run("--print-config")
            self.assertEqual(done.returncode, 0, done.stderr.decode())
            self.assertIn("RSYNC_PATH", _values(done.stdout.decode()))
