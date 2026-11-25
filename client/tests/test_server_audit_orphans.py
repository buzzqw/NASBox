"""Files present on the share with no live manifest row.

The manifest is written only by the journal, so a file that arrived without a
transaction (restored backup, manual copy, an append lost in a crash) is
invisible: no client downloads it and a fresh client never sees it. The server
can now list them and, on request, journal them with the digest they have.
"""
from __future__ import annotations

import os
import time
import unittest

from tests.support import ServerSandbox


def _put(sandbox: ServerSandbox, name: str, body: bytes, age_seconds: int = 3600) -> None:
    target = sandbox.share / name
    target.parent.mkdir(parents=True, exist_ok=True)
    target.write_bytes(body)
    when = time.time() - age_seconds
    os.utime(target, (when, when))


def _values(raw: str) -> dict[str, str]:
    return dict(
        line.split("=", 1) for line in raw.splitlines() if "=" in line
    )


class AuditOrphansTests(unittest.TestCase):
    def test_orphans_are_listed_and_can_be_adopted(self) -> None:
        with ServerSandbox() as sandbox:
            sandbox.init_repository()
            self.assertEqual(sandbox.run("--manifest-export").returncode, 0)
            _put(sandbox, "progetto/nuovo.txt", b"mai registrato")
            _put(sandbox, "progetto/sotto/altro.bin", b"\x00\x01\x02")
            (sandbox.share / ".sync-trash").mkdir()
            _put(sandbox, ".sync-trash/progetto/vecchio.txt-2026-01-01", b"storico")
            (sandbox.share / ".nasbox-client-update-go").mkdir()
            _put(sandbox, ".nasbox-client-update-go/nasbox-go-1.0.0", b"binario")
            (sandbox.share / "progetto" / "@eaDir").mkdir()
            _put(sandbox, "progetto/@eaDir/miniatura", b"thumb")

            listed = sandbox.run("--audit-orphans", "--list")
            self.assertEqual(listed.returncode, 0, listed.stderr.decode())
            parts = listed.stdout.split(b"\0")
            values = _values(parts[0].decode())
            self.assertEqual(values["COUNT"], "2")
            paths = [part.decode() for part in parts[1:] if part]
            self.assertEqual(sorted(paths), ["progetto/nuovo.txt", "progetto/sotto/altro.bin"])

            dry = sandbox.run("--audit-adopt", "--dry-run")
            self.assertEqual(dry.returncode, 0, dry.stderr.decode())
            self.assertEqual(_values(dry.stdout.decode())[ "WOULD_ADOPT"], "2")

            adopted = sandbox.run("--audit-adopt")
            self.assertEqual(adopted.returncode, 0, adopted.stderr.decode())
            self.assertEqual(_values(adopted.stdout.decode())["ADOPTED"], "2")

            # Now they are ordinary tracked files, with the real digests.
            manifest = sandbox.run("--manifest-export").stdout.decode()
            self.assertIn("progetto/nuovo.txt", manifest)
            self.assertIn("progetto/sotto/altro.bin", manifest)
            import hashlib

            digest = hashlib.sha256(b"mai registrato").hexdigest()
            self.assertIn(digest, manifest)
            # Nothing else was adopted (trash, update channel, @eaDir).
            self.assertNotIn(".sync-trash/progetto/vecchio.txt", manifest)
            self.assertNotIn("nasbox-go-1.0.0", manifest)
            self.assertNotIn("@eaDir", manifest)

            again = sandbox.run("--audit-orphans")
            self.assertEqual(_values(again.stdout.decode())["COUNT"], "0")

    def test_a_file_still_being_written_is_left_for_the_next_run(self) -> None:
        with ServerSandbox() as sandbox:
            sandbox.init_repository()
            self.assertEqual(sandbox.run("--manifest-export").returncode, 0)
            _put(sandbox, "progetto/in-corso.bin", b"parziale", age_seconds=5)

            adopted = sandbox.run("--audit-adopt")
            self.assertEqual(adopted.returncode, 0, adopted.stderr.decode())
            self.assertEqual(_values(adopted.stdout.decode())["ADOPTED"], "0")
            self.assertEqual(
                _values(sandbox.run("--audit-orphans").stdout.decode())["COUNT"], "1"
            )

    def test_adoption_is_idempotent(self) -> None:
        with ServerSandbox() as sandbox:
            sandbox.init_repository()
            self.assertEqual(sandbox.run("--manifest-export").returncode, 0)
            _put(sandbox, "progetto/uno.txt", b"uno")
            first = sandbox.run("--audit-adopt")
            self.assertEqual(_values(first.stdout.decode())["ADOPTED"], "1")
            second = sandbox.run("--audit-adopt")
            self.assertEqual(second.returncode, 0, second.stderr.decode())
            self.assertEqual(_values(second.stdout.decode())["ADOPTED"], "0")
            journal = (sandbox.state / "transfer-journal.tsv").read_text()
            self.assertEqual(journal.count("PUT\tprogetto/uno.txt\t"), 1)


if __name__ == "__main__":
    unittest.main()


class NestedInternalFolderTests(unittest.TestCase):
    """A history folder that was itself synced once (nested .sync-trash) is as
    internal as one at the root. Adopting those paths breaks every client's
    manifest export, so the audit must skip them at any depth -- and the journal
    must be able to forget a row it should never have held."""

    def test_nested_trash_and_staging_are_never_adopted(self) -> None:
        with ServerSandbox() as sandbox:
            sandbox.init_repository()
            self.assertEqual(sandbox.run("--manifest-export").returncode, 0)
            _put(sandbox, "RPG/.sync-trash/mappe/vecchia.jpg-2026-08-06", b"storico annidato")
            _put(sandbox, "progetto/.nasbox-staging/x/y.bin", b"staging annidato")
            _put(sandbox, "progetto/.sync-partial/temp.part", b"parziale")
            _put(sandbox, "progetto/vero.txt", b"contenuto")

            listed = sandbox.run("--audit-orphans", "--list")
            parts = listed.stdout.split(b"\0")
            self.assertEqual(_values(parts[0].decode())["COUNT"], "1")
            self.assertEqual([p.decode() for p in parts[1:] if p], ["progetto/vero.txt"])

    def test_journal_forget_tombstones_without_touching_the_disk(self) -> None:
        with ServerSandbox() as sandbox:
            sandbox.init_repository()
            self.assertEqual(sandbox.run("--manifest-export").returncode, 0)
            _put(sandbox, "progetto/da-dimenticare.txt", b"contenuto")
            self.assertEqual(
                _values(sandbox.run("--audit-adopt").stdout.decode())["ADOPTED"], "1"
            )
            self.assertIn("progetto/da-dimenticare.txt", sandbox.run("--manifest-export").stdout.decode())

            done = sandbox.run("--journal-forget", input=b"progetto/da-dimenticare.txt\0")
            self.assertEqual(done.returncode, 0, done.stderr.decode())
            self.assertEqual(_values(done.stdout.decode())["FORGOTTEN"], "1")

            manifest = sandbox.run("--manifest-export").stdout.decode()
            rows = [
                line for line in manifest.splitlines()
                if line.startswith("progetto/da-dimenticare.txt\t")
            ]
            self.assertEqual(len(rows), 1)
            # Still listed, but as a tombstone (empty digest): no client will
            # treat the path as a live file any more.
            self.assertEqual(rows[0].split("\t")[1], "")
            # The bytes are still there: forgetting a row is not a deletion.
            self.assertTrue((sandbox.share / "progetto" / "da-dimenticare.txt").exists())

    def test_forget_accepts_an_internal_path(self) -> None:
        """The paths a maintenance forget must remove are exactly the internal
        ones, so its validation cannot reuse the one that refuses them."""
        with ServerSandbox() as sandbox:
            sandbox.init_repository()
            _put(sandbox, "RPG/.sync-trash/mappe/vecchia.jpg-2026-08-06", b"storico")
            # Adopted by an older release of the audit (which only knew the root
            # .sync-trash), so the row is in the manifest and must go.
            adopted = sandbox.run("--audit-adopt")
            self.assertEqual(adopted.returncode, 0, adopted.stderr.decode())
            done = sandbox.run(
                "--journal-forget", input=b"RPG/.sync-trash/mappe/vecchia.jpg-2026-08-06\0"
            )
            self.assertEqual(done.returncode, 0, done.stderr.decode())
            self.assertEqual(_values(done.stdout.decode())["FORGOTTEN"], "1")
            rows = [
                line for line in sandbox.run("--manifest-export").stdout.decode().splitlines()
                if line.startswith("RPG/.sync-trash/mappe/vecchia.jpg-2026-08-06\t")
            ]
            self.assertEqual(len(rows), 1)
            self.assertEqual(rows[0].split("\t")[1], "")
