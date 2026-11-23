"""The server sandbox must skip cleanly when the server script is absent."""
from __future__ import annotations

import pytest

from tests import support


def test_server_script_is_found_in_the_default_layout():
    script = support.server_script_path()
    assert script is not None
    assert script.name == "sync-daemon-server.sh"
    assert script.is_file()


def test_server_script_env_override(monkeypatch, tmp_path):
    fake = tmp_path / "custom-server.sh"
    fake.write_text("#!/bin/sh\n", encoding="utf-8")
    monkeypatch.setenv(support.SERVER_SCRIPT_ENV, str(fake))
    assert support.server_script_path() == fake.resolve()
    fake.unlink()
    assert support.server_script_path() is None


def test_missing_server_script_skips_instead_of_failing(monkeypatch):
    monkeypatch.setattr(support, "server_script_path", lambda: None)
    with pytest.raises(pytest.skip.Exception) as excinfo:
        support.ServerSandbox()
    message = str(excinfo.value)
    assert "sync-daemon-server.sh" in message
    assert support.SERVER_SCRIPT_ENV in message
