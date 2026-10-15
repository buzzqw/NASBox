from __future__ import annotations

import unittest

from sync_client import rsync_ops
from sync_client.config import Config

from tests.support import ClientEnvironment


class SshMultiplexingOptionTests(unittest.TestCase):
    def test_control_master_is_enabled_by_default(self) -> None:
        with ClientEnvironment():
            cfg = Config()
            opts = rsync_ops.ssh_opts(cfg, rsync_ops.NasConnection("nas"))
        joined = " ".join(opts)
        self.assertIn("ControlMaster=auto", joined)
        self.assertIn("ControlPath=", joined)
        self.assertIn("ControlPersist=60", joined)

    def test_control_master_can_be_disabled(self) -> None:
        with ClientEnvironment():
            cfg = Config()
            cfg.set("ssh_multiplexing", False, persist=False)
            opts = rsync_ops.ssh_opts(cfg, rsync_ops.NasConnection("nas"))
        self.assertNotIn("ControlMaster=auto", " ".join(opts))

    def test_bastion_and_known_hosts_options_survive_multiplexing(self) -> None:
        with ClientEnvironment():
            cfg = Config()
            cfg.set("ssh_host_key_pinned", True, persist=False)
            cfg.set("ssh_known_hosts", "/tmp/known_hosts", persist=False)
            cfg.set("jump_host", "bastion.example.net", persist=False)
            cfg.set("nas_user", "andres", persist=False)
            conn = rsync_ops.NasConnection("nas-lan", via_jump=True)
            opts = rsync_ops.ssh_opts(cfg, conn)
        joined = " ".join(opts)
        self.assertIn("ProxyJump=andres@bastion.example.net:22", joined)
        self.assertIn("UserKnownHostsFile=/tmp/known_hosts", joined)
        self.assertIn("ControlMaster=auto", joined)


if __name__ == "__main__":
    unittest.main()
