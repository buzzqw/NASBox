from __future__ import annotations

import unittest
from types import SimpleNamespace
from unittest.mock import patch

from sync_client import rsync_ops
from sync_client.sync_state import CausalVersion, Fingerprint


class StagingPayloadTests(unittest.TestCase):
    def test_publish_staging_encodes_causal_version_as_bytes(self) -> None:
        cfg = SimpleNamespace(get=lambda key: {
            "remote_server_script": "/remote/sync-daemon-server.sh",
            "remote_causal_versions_available": True,
            "nas_user": "user",
        }.get(key))
        causal = CausalVersion((("device-1", 7),))
        fingerprint = Fingerprint("a" * 64, 12, 34, causal)
        captured: list[bytes] = []

        def fake_remote_call(_cfg, _conn, _script, _args, payload, **_kwargs):
            captured.append(payload)
            return True, b"\0".join((b"STAGING_PUBLISH_V2", b"OK", b"1")) + b"\0", ""

        with patch.object(rsync_ops, "run_remote_script_input_bytes", side_effect=fake_remote_call):
            result = rsync_ops.publish_staging(
                cfg,
                rsync_ops.NasConnection("nas"),
                "/remote/.nasbox-staging/publish-test",
                "publish-test",
                "device-1",
                {"folder/file.txt": fingerprint},
                {"folder/file.txt": causal},
            )

        self.assertEqual(result, (True, ""))
        self.assertEqual(
            captured,
            [
                b"\0".join((
                    b"STAGING_PUBLISH_V2",
                    b"/remote/.nasbox-staging/publish-test",
                    b"publish-test",
                    b"device-1",
                    b"1",
                    b"folder/file.txt",
                    b"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
                    b"12",
                    b"34",
                    b"device-1:7",
                )) + b"\0",
            ],
        )


if __name__ == "__main__":
    unittest.main()
