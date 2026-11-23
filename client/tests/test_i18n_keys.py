"""Translation-table integrity: both languages must always stay in sync."""
from __future__ import annotations

import string
from types import SimpleNamespace

from sync_client import i18n
from sync_client.i18n import STRINGS, SUPPORTED, t


def test_all_translation_keys_have_both_languages():
    assert set(SUPPORTED) == {"it", "en"}
    for key, entry in STRINGS.items():
        assert set(entry) == set(SUPPORTED), key
        for lang in SUPPORTED:
            assert isinstance(entry[lang], str) and entry[lang].strip(), (key, lang)


def test_placeholders_match_between_languages():
    for key, entry in STRINGS.items():
        per_language = []
        for lang in SUPPORTED:
            names = {
                name for _literal, name, _format, _conversion
                in string.Formatter().parse(entry[lang]) if name
            }
            per_language.append(names)
        assert per_language[0] == per_language[1], key


def test_lookup_uses_the_active_language():
    i18n.set_language("it")
    italian = t("status.group_title")
    i18n.set_language("en")
    english = t("status.group_title")
    i18n.set_language(i18n.DEFAULT_LANG)
    assert italian == "Stato"
    assert english == "Status"
    assert italian != english


def test_new_status_and_conflict_keys_are_translated():
    for key in (
        "status.nas_overview_title",
        "status.storage_summary",
        "conflicts.detail_title",
        "conflicts.keep_original_btn",
        "settings.theme_dark",
        "a11y.version_badge",
        "tray.sync_now",
    ):
        for lang in SUPPORTED:
            i18n.set_language(lang)
            assert t(key) == STRINGS[key][lang]
            assert t(key) != key
    i18n.set_language(i18n.DEFAULT_LANG)


def test_system_language_uses_qlocale(monkeypatch):
    class FakeQLocale:
        @staticmethod
        def system():
            return SimpleNamespace(name=lambda: "it_IT")

    monkeypatch.setattr(i18n, "QLocale", FakeQLocale)
    monkeypatch.setattr(
        i18n.locale, "getdefaultlocale",
        lambda: (_ for _ in ()).throw(AssertionError("deprecated API called")),
    )
    assert i18n.detect_system_language() == "it"


def test_system_language_falls_back_without_qlocale(monkeypatch):
    monkeypatch.setattr(i18n, "QLocale", None)
    monkeypatch.setattr(i18n.locale, "getlocale", lambda: ("en_US", "UTF-8"))
    assert i18n.detect_system_language() == "en"
