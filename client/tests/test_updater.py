from __future__ import annotations

import hashlib
import io
import json
import tarfile
from pathlib import Path

import pytest

from sync_client import rsync_ops, updater


class _Cfg:
    """Minimal Config stand-in: only the keys the updater reads."""

    def __init__(self, **values) -> None:
        self._values = {"nas_user": "nas", **values}

    def get(self, key: str, default=None):
        return self._values.get(key, default)


def _bundle(parent: Path, version: str, name: str = "client") -> Path:
    root = parent / name
    (root / "sync_client").mkdir(parents=True)
    (root / "main.py").write_text("print('nasbox')\n", encoding="utf-8")
    (root / "sync_client" / "version.py").write_text(
        f'APP_VERSION = "{version}"\n', encoding="utf-8",
    )
    return root


def _tarball(parent: Path, version: str) -> Path:
    bundle = _bundle(parent / "src", version)
    archive = parent / f"client-{version}.tar.gz"
    with tarfile.open(archive, "w:gz") as tar:
        tar.add(bundle, arcname="client")
    return archive


def _manifest_for(archive: Path, version: str) -> updater.Manifest:
    digest = hashlib.sha256(archive.read_bytes()).hexdigest()
    return updater.Manifest(
        version=version, artifact=archive.name, sha256=digest, size=archive.stat().st_size,
    )


# --- manifest parsing -------------------------------------------------------

def test_manifest_round_trip():
    manifest = updater.Manifest(version="1.26.8", artifact="client-1.26.8.tar.gz", sha256="a" * 64)
    parsed = updater.parse_manifest(manifest.to_json())
    assert parsed is not None
    assert parsed.version == "1.26.8"
    assert parsed.sha256 == "a" * 64
    assert parsed.channel == "stable"


@pytest.mark.parametrize("payload", [
    "non json",
    "[]",
    json.dumps({"version": "1.0.0"}),                       # artifact/digest mancanti
    json.dumps({"version": "1.0.0", "artifact": "a.tar.gz", "sha256": "corto"}),
    json.dumps({"version": "1.0.0", "artifact": "a.tar.gz", "sha256": "z" * 64}),
    json.dumps({"version": "1.0.0", "artifact": "../evil.tar.gz", "sha256": "a" * 64}),
    json.dumps({"version": "1.0.0", "artifact": "/etc/passwd", "sha256": "a" * 64}),
])
def test_manifest_rejects_invalid_payloads(payload):
    assert updater.parse_manifest(payload) is None


# --- download + verify ------------------------------------------------------

def test_materialize_verifies_and_unpacks(tmp_path, monkeypatch):
    archive = _tarball(tmp_path, "1.26.8")
    manifest = _manifest_for(archive, "1.26.8")

    def fake_run(command, **_kwargs):
        # emulate `rsync remote:/dir/artifact dest`
        source = command[-2].split(":", 1)[1]
        Path(command[-1]).write_bytes(Path(str(tmp_path / Path(source).name)).read_bytes())

        class _Completed:
            returncode = 0
            stderr = ""
        return _Completed()

    monkeypatch.setattr(updater.subprocess, "run", fake_run)
    candidate = updater.UpdateCandidate(
        version="1.26.8", origin="test",
        remote_path=str(tmp_path), cfg=_Cfg(),
        connection=rsync_ops.NasConnection("nas"), manifest=manifest,
    )
    try:
        root = candidate.materialize()
        assert updater._read_version(root) == "1.26.8"
        assert (root / "main.py").is_file()
    finally:
        candidate.cleanup()


def test_materialize_rejects_tampered_archive(tmp_path, monkeypatch):
    archive = _tarball(tmp_path, "1.26.8")
    manifest = _manifest_for(archive, "1.26.8")

    def fake_run(command, **_kwargs):
        # same name, different bytes: digest must not match
        Path(command[-1]).write_bytes(b"not the archive you published")

        class _Completed:
            returncode = 0
            stderr = ""
        return _Completed()

    monkeypatch.setattr(updater.subprocess, "run", fake_run)
    candidate = updater.UpdateCandidate(
        version="1.26.8", origin="test",
        remote_path=str(tmp_path), cfg=_Cfg(),
        connection=rsync_ops.NasConnection("nas"), manifest=manifest,
    )
    with pytest.raises(RuntimeError, match="corrotto o manomesso"):
        candidate.materialize()
    assert candidate._temporary is None  # cleaned up after the failure


# --- archive safety ---------------------------------------------------------

def _archive_with_member(tmp_path: Path, member: tarfile.TarInfo, payload: bytes = b"x") -> Path:
    archive = tmp_path / "evil.tar.gz"
    with tarfile.open(archive, "w:gz") as tar:
        member.size = len(payload)
        tar.addfile(member, io.BytesIO(payload))
    return archive


def test_safe_extract_refuses_path_traversal(tmp_path):
    member = tarfile.TarInfo("../escaped.txt")
    archive = _archive_with_member(tmp_path, member)
    with pytest.raises(RuntimeError, match="non valido"):
        updater._safe_extract(archive, tmp_path / "out")


def test_safe_extract_refuses_absolute_paths(tmp_path):
    member = tarfile.TarInfo("/tmp/escaped.txt")
    archive = _archive_with_member(tmp_path, member)
    with pytest.raises(RuntimeError, match="non valido"):
        updater._safe_extract(archive, tmp_path / "out")


def test_safe_extract_refuses_symlinks(tmp_path):
    member = tarfile.TarInfo("link")
    member.type = tarfile.SYMTYPE
    member.linkname = "/etc/passwd"
    archive = _archive_with_member(tmp_path, member)
    with pytest.raises(RuntimeError, match="non valido"):
        updater._safe_extract(archive, tmp_path / "out")


# --- install / confirm / rollback ------------------------------------------

def test_install_keeps_the_previous_client_until_confirmed(tmp_path):
    destination = _bundle(tmp_path / "installed", "1.21.0")

    candidate_root = _bundle(tmp_path / "new", "1.26.8")
    updater.install_update(candidate_root, destination)

    assert updater._read_version(destination) == "1.26.8"
    pending = updater._pending_file(destination)
    assert pending.is_file()
    backup = Path(json.loads(pending.read_text())["backup"])
    assert updater._read_version(backup) == "1.21.0"  # still there: rollback possible

    assert updater.confirm_started_ok(destination) is True
    assert not pending.exists()
    assert not backup.exists()
    assert updater._read_version(destination) == "1.26.8"


def test_rollback_waits_for_second_failed_start(tmp_path):
    destination = _bundle(tmp_path / "installed", "1.21.0")
    updater.install_update(_bundle(tmp_path / "new", "1.26.8"), destination)

    assert updater.mark_start_attempt(destination) == 1
    assert updater.rollback_if_needed(destination) is None  # first start gets a chance
    assert updater._read_version(destination) == "1.26.8"

    assert updater.mark_start_attempt(destination) == 2
    restored = updater.rollback_if_needed(destination)
    assert restored == "1.21.0"
    assert updater._read_version(destination) == "1.21.0"
    assert not updater._pending_file(destination).exists()


def test_rollback_is_a_no_op_without_a_pending_update(tmp_path):
    destination = _bundle(tmp_path / "installed", "1.26.8")
    assert updater.mark_start_attempt(destination) == 0
    assert updater.rollback_if_needed(destination) is None
    assert updater.confirm_started_ok(destination) is False


# --- dev checkout guard -----------------------------------------------------

def test_development_checkout_is_detected(tmp_path):
    checkout = _bundle(tmp_path / "repo" / "sync-daemon", "1.26.8")
    (tmp_path / "repo" / ".git").mkdir()
    assert updater.is_development_checkout(checkout) is True
    assert updater.auto_update_allowed(_Cfg(), checkout) is False
    assert updater.auto_update_allowed(_Cfg(client_update_allow_dev_tree=True), checkout) is True


def test_plain_install_is_not_a_development_checkout(tmp_path):
    installed = _bundle(tmp_path / "opt", "1.26.8")
    assert updater.is_development_checkout(installed) is False
    assert updater.auto_update_allowed(_Cfg(), installed) is True


# --- discovery --------------------------------------------------------------

def test_find_update_from_local_bundle(tmp_path, monkeypatch):
    published = _bundle(tmp_path / "published", "99.0.0")
    installed = _bundle(tmp_path / "installed", updater.APP_VERSION)
    monkeypatch.setenv("NASBOX_CLIENT_UPDATE_PATH", str(published))
    candidate = updater.find_update(_Cfg(), installed, str(installed / "main.py"))
    assert candidate is not None
    assert candidate.version == "99.0.0"
    assert candidate.source_root == published


def test_find_update_ignores_same_or_older_bundle(tmp_path, monkeypatch):
    published = _bundle(tmp_path / "published", "1.0.0")
    installed = _bundle(tmp_path / "installed", updater.APP_VERSION)
    monkeypatch.setenv("NASBOX_CLIENT_UPDATE_PATH", str(published))
    assert updater.find_update(_Cfg(), installed, str(installed / "main.py")) is None
