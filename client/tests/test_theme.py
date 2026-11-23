"""Light stays the default; dark and system themes are selectable at runtime."""
from __future__ import annotations

from unittest.mock import MagicMock

import pytest

from sync_client.config import Config
from sync_client.gui import style
from sync_client.gui.settings_tab import SettingsTab
from tests.support import ClientEnvironment


def test_light_is_the_default_theme():
    assert style.DEFAULT_THEME == "light"
    assert style.stylesheet_for("light") == style.STYLESHEET
    assert style.current_theme() in style.THEMES


def test_dark_theme_has_its_own_stylesheet():
    assert style.DARK_STYLESHEET != style.STYLESHEET
    assert "QMainWindow, QWidget" in style.DARK_STYLESHEET
    assert "#141a24" in style.DARK_STYLESHEET


def test_system_theme_follows_the_native_palette():
    assert style.stylesheet_for("system") == ""


def test_apply_style_switches_themes(qapp):
    style.apply_style(qapp, "light")
    assert qapp.styleSheet() == style.STYLESHEET
    style.apply_style(qapp, "dark")
    assert qapp.styleSheet() == style.DARK_STYLESHEET
    style.apply_style(qapp, "system")
    assert qapp.styleSheet() == ""
    # Leave the shared test app in its original state.
    style.apply_style(qapp, "light")
    assert qapp.styleSheet() == style.STYLESHEET


def test_settings_exposes_theme_selector_and_persists_it(qapp):
    with ClientEnvironment():
        cfg = Config()
        tab = SettingsTab(cfg, MagicMock())
        assert tab.theme_combo.currentData() == "light"
        tab.theme_combo.setCurrentIndex(tab.theme_combo.findData("dark"))
        tab._save_settings()
        assert cfg.get("theme") == "dark"
        style.apply_style(qapp, "light")
