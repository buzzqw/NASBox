"""The conflict policy must be reachable from the UI, not only from client.json.

`conflict_policy` shipped as a documented setting in 1.26.x with no selector in
Impostazioni: the only way to go back to the old "ask me" behaviour was editing
the JSON by hand. These tests pin the selector and its persistence.
"""
from __future__ import annotations

from unittest.mock import MagicMock

import pytest

from sync_client.config import Config
from sync_client.gui.settings_tab import SettingsTab
from tests.support import ClientEnvironment


@pytest.fixture
def client_env():
    with ClientEnvironment():
        yield


def _build(policy: str | None = None) -> tuple[SettingsTab, Config]:
    cfg = Config()
    if policy is not None:
        cfg.set("conflict_policy", policy, persist=False)
    return SettingsTab(cfg, MagicMock()), cfg


def test_selector_defaults_to_the_newest_policy(qapp, client_env):
    tab, _cfg = _build()
    assert tab.conflict_policy_combo.currentData() == "newest"


def test_selector_shows_the_configured_policy(qapp, client_env):
    tab, _cfg = _build("manual")
    assert tab.conflict_policy_combo.currentData() == "manual"


def test_selector_offers_both_policies(qapp, client_env):
    tab, _cfg = _build()
    values = [
        tab.conflict_policy_combo.itemData(index)
        for index in range(tab.conflict_policy_combo.count())
    ]
    assert values == ["newest", "manual"]


def test_save_persists_the_selected_policy(qapp, client_env):
    tab, cfg = _build()
    tab.conflict_policy_combo.setCurrentIndex(
        tab.conflict_policy_combo.findData("manual")
    )
    tab._save_settings()
    assert cfg.get("conflict_policy") == "manual"
    assert cfg.conflict_policy() == "manual"


def test_save_keeps_the_automatic_policy(qapp, client_env):
    tab, cfg = _build("manual")
    tab.conflict_policy_combo.setCurrentIndex(
        tab.conflict_policy_combo.findData("newest")
    )
    tab._save_settings()
    assert cfg.conflict_policy() == "newest"


def test_unknown_policy_falls_back_to_asking(qapp, client_env):
    tab, cfg = _build("bogus")
    # A hand-edited client.json must land on the safe default, not on the
    # automatic one that silently picks a winner.
    assert cfg.conflict_policy() == "manual"
    assert tab.conflict_policy_combo.currentData() == "manual"
