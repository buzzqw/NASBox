from __future__ import annotations

import hashlib
import shutil
import subprocess
import tempfile
import unittest
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]
SCRIPT = ROOT / "sync-daemon-server.sh"


class FileStatesLookupTests(unittest.TestCase):
    def test_batch_reuses_manifest_entry_and_reports_absent(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            sandbox = Path(directory)
            script = sandbox / "server.sh"
            share = sandbox / "share"
            share.mkdir()
            shutil.copy2(SCRIPT, script)
            script.chmod(0o755)
            config = sandbox / "server.conf"
            config.write_text(f"SHARE_ROOT={share}\nRETENTION_DAYS=30\n")

            known = share / "known.txt"
            known.write_text("known content")
            digest = hashlib.sha256(known.read_bytes()).hexdigest()
            size = known.stat().st_size
            mtime_ns = int(known.stat().st_mtime) * 1_000_000_000

            state = sandbox / "state"
            state.mkdir()
            (state / "manifest.tsv").write_text(
                "NASBOX_MANIFEST_V1\n"
                f"known.txt\t{digest}\t{size}\t{mtime_ns}\tdevice-a\t1\n"
                f"gone.txt\t{'0' * 64}\t10\t1\tdevice-a\t2\n"
            )

            # Fake sha256sum fails: a reused manifest digest must not need it, and
            # the absent path must not hash anything either.
            fake_bin = sandbox / "bin"
            fake_bin.mkdir()
            fake_sha = fake_bin / "sha256sum"
            fake_sha.write_text("#!/bin/sh\nexit 1\n")
            fake_sha.chmod(0o755)
            env = dict(__import__("os").environ)
            env["PATH"] = f"{fake_bin}:{env['PATH']}"

            payload = (
                b"FILE_STATES_V1\0" b"3\0"
                b"known.txt\0" b"missing.txt\0" b"gone.txt\0"
            )
            result = subprocess.run(
                [str(script), "-c", str(config), "--file-states"],
                input=payload, capture_output=True, env=env,
            )

            self.assertEqual(result.returncode, 0, result.stderr.decode())
            out = result.stdout
            self.assertIn(b"known.txt\0FILE\0" + digest.encode(), out)
            self.assertIn(b"missing.txt\0ABSENT\0", out)
            self.assertIn(b"gone.txt\0TOMBSTONE\0", out)


if __name__ == "__main__":
    unittest.main()
