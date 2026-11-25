"""BROWSE_RENAME_V3: move a whole folder in one request, with the digests the
client already knows, so the NAS only checks size and mtime instead of hashing
every file. Also covers V2's mtime check, which compared seconds against
nanoseconds and refused files whose manifest mtime has a sub-second part."""
from __future__ import annotations

import hashlib
import os
import unittest

from tests.support import ServerSandbox

RUN_TS = "2026-11-25--12-00-00-000001Z"


def _file(sandbox: ServerSandbox, rel: str, body: bytes) -> tuple[str, str, int, int]:
    target = sandbox.share / rel
    target.parent.mkdir(parents=True, exist_ok=True)
    target.write_bytes(body)
    stat = target.stat()
    return rel, hashlib.sha256(body).hexdigest(), len(body), int(stat.st_mtime) * 1_000_000_000


def _v3(src: str, dst: str, files: list[tuple[str, str, int, int]]) -> bytes:
    fields = [b"BROWSE_RENAME_V3", RUN_TS.encode(), b"device-a", src.encode(), dst.encode(),
              b"DIR", b"", b"0", b"0", str(len(files)).encode()]
    for rel, digest, size, mtime in files:
        fields += [rel.encode(), digest.encode(), str(size).encode(), str(mtime).encode()]
    return b"\0".join(fields) + b"\0"


def _v2_file(src: str, dst: str, digest: str, size: int, mtime_ns: int) -> bytes:
    return b"\0".join([b"BROWSE_RENAME_V2", RUN_TS.encode(), b"device-a", src.encode(), dst.encode(),
                       b"FILE", digest.encode(), str(size).encode(), str(mtime_ns).encode()]) + b"\0"


class RenameDirV3Tests(unittest.TestCase):
    def test_folder_moves_in_one_request_and_journals_every_file(self) -> None:
        with ServerSandbox() as sandbox:
            sandbox.init_repository()
            self.assertEqual(sandbox.run("--manifest-export").returncode, 0)
            made = [_file(sandbox, f"old/sub/f{i}.txt", f"content {i}".encode()) for i in range(5)]
            files = [(rel.removeprefix("old/"), d, s, m) for rel, d, s, m in made]

            done = sandbox.run_with_lease("--browse-rename", input=_v3("old", "new", files))
            self.assertEqual(done.returncode, 0, done.stderr.decode())
            self.assertEqual(done.stdout.split(b"\0")[:2], [b"BROWSE_RENAME_V3", b"OK"])
            self.assertFalse((sandbox.share / "old").exists())
            for i in range(5):
                self.assertEqual((sandbox.share / f"new/sub/f{i}.txt").read_bytes(), f"content {i}".encode())
            journal = (sandbox.state / "transfer-journal.tsv").read_text()
            self.assertEqual(journal.count("BEGIN\t"), 1)
            self.assertEqual(journal.count("DELETE\told/sub/f"), 5)
            self.assertEqual(journal.count("PUT\tnew/sub/f"), 5)
            self.assertIn(made[0][1], journal)  # the client's digest, not a recomputed one
            self.assertEqual(list((sandbox.state / "transactions").glob("*")), [])

    def test_extra_or_changed_files_make_the_server_refuse(self) -> None:
        with ServerSandbox() as sandbox:
            sandbox.init_repository()
            self.assertEqual(sandbox.run("--manifest-export").returncode, 0)
            made = [_file(sandbox, f"dir/f{i}.txt", b"x" * (i + 1)) for i in range(3)]
            files = [(rel.removeprefix("dir/"), d, s, m) for rel, d, s, m in made]
            (sandbox.share / "dir" / "untracked.txt").write_bytes(b"not in the manifest")

            refused = sandbox.run_with_lease("--browse-rename", input=_v3("dir", "moved", files))
            self.assertNotEqual(refused.returncode, 0)
            self.assertIn(b"la directory e cambiata", refused.stdout)
            self.assertTrue((sandbox.share / "dir").is_dir())
            self.assertFalse((sandbox.share / "moved").exists())

            (sandbox.share / "dir" / "untracked.txt").unlink()
            (sandbox.share / "dir" / "f0.txt").write_bytes(b"different size now!!")
            refused = sandbox.run_with_lease("--browse-rename", input=_v3("dir", "moved", files))
            self.assertNotEqual(refused.returncode, 0)
            self.assertTrue((sandbox.share / "dir").is_dir())

    def test_existing_destination_is_refused(self) -> None:
        with ServerSandbox() as sandbox:
            sandbox.init_repository()
            self.assertEqual(sandbox.run("--manifest-export").returncode, 0)
            made = [_file(sandbox, f"a/f{i}.txt", b"data") for i in range(2)]
            (sandbox.share / "b").mkdir()
            files = [(rel.removeprefix("a/"), d, s, m) for rel, d, s, m in made]
            refused = sandbox.run_with_lease("--browse-rename", input=_v3("a", "b", files))
            self.assertNotEqual(refused.returncode, 0)
            self.assertTrue((sandbox.share / "a").is_dir())

    def test_v2_file_rename_accepts_a_manifest_mtime_with_a_subsecond_part(self) -> None:
        with ServerSandbox() as sandbox:
            sandbox.init_repository()
            self.assertEqual(sandbox.run("--manifest-export").returncode, 0)
            rel, digest, size, mtime = _file(sandbox, "single.txt", b"one file")
            precise = mtime + 123_456_789  # what a client-uploaded file may carry
            done = sandbox.run_with_lease("--browse-rename", input=_v2_file(rel, "renamed.txt", digest, size, precise))
            self.assertEqual(done.returncode, 0, done.stderr.decode())
            self.assertEqual(done.stdout.split(b"\0")[:2], [b"BROWSE_RENAME_V2", b"OK"])
            self.assertTrue((sandbox.share / "renamed.txt").exists())


if __name__ == "__main__":
    unittest.main()
