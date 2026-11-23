"""UX of the Impostazioni tab: search, collapsible sections and diagnostics.

The new strings live in i18n.py (added by the parent change), but this module
must also be runnable against a checkout where they are not there yet: it
injects the translations at test time instead of editing i18n.py here.
"""
from __future__ import annotations

import zipfile
from unittest.mock import MagicMock, patch

import pytest
from PyQt6.QtGui import QKeySequence
from PyQt6.QtWidgets import QGroupBox, QWidget

from sync_client import i18n, paths
from sync_client.config import Config
from sync_client.gui.settings_tab import SettingsTab
from sync_client.version import APP_VERSION
from tests.support import ClientEnvironment

# it/en pairs that the parent session adds to i18n.py. Placeholders must match
# across languages (see test_i18n_keys.py).
_NEW_STRINGS = {
    "settings.search_placeholder": {"it": "Cerca impostazioni…", "en": "Search settings…"},
    "settings.search_tooltip": {
        "it": "Filtra le sezioni per parola chiave (Ctrl+K).",
        "en": "Filter sections by keyword (Ctrl+K).",
    },
    "settings.export_diagnostics_btn": {
        "it": "Esporta bundle diagnostico…", "en": "Export diagnostic bundle…",
    },
    "settings.export_diagnostics_tooltip": {
        "it": "Salva versioni, configurazione senza segreti e le ultime righe di log.",
        "en": "Save versions, redacted configuration and the last log lines.",
    },
    "settings.nas_facts_title": {
        "it": "Valori rilevati dal NAS", "en": "NAS-reported values",
    },
    "settings.nas_fact_retention_label": {"it": "Retention NAS:", "en": "NAS retention:"},
    "settings.nas_fact_server_version_label": {
        "it": "Versione server NAS:", "en": "NAS server version:",
    },
    "settings.nas_fact_lock_label": {"it": "Lock di trasferimento:", "en": "Transfer lock:"},
    "settings.nas_facts_note": {
        "it": "Valori letti dalla configurazione: usa «Rileva dal NAS» per aggiornarli.",
        "en": "Values read from configuration: use “Detect from NAS” to refresh them.",
    },
    "settings.nas_reported_value": {
        "it": "rilevato dal NAS: {value}", "en": "detected from NAS: {value}",
    },
    "settings.nas_not_reported": {
        "it": "non rilevato", "en": "not detected",
    },
    "settings.nas_lock_held": {
        "it": "occupato da {owner} da {age} s", "en": "held by {owner} for {age} s",
    },
    "settings.nas_lock_free": {"it": "libero", "en": "free"},
    "settings.export_bundle_title": {
        "it": "Bundle diagnostico NASBox", "en": "NASBox diagnostic bundle",
    },
    "settings.export_generated": {
        "it": "Generato: {timestamp}", "en": "Generated: {timestamp}",
    },
    "settings.export_section_versions": {"it": "Versioni", "en": "Versions"},
    "settings.export_section_config": {
        "it": "Configurazione (segreti rimossi)", "en": "Configuration (secrets redacted)",
    },
    "settings.export_section_nas_stats": {
        "it": "Stato NAS / manifest / lock (da configurazione)",
        "en": "NAS / manifest / lock state (from configuration)",
    },
    "settings.export_no_nas_stats": {
        "it": "(nessun dato NAS rilevato)", "en": "(no NAS data detected)",
    },
    "settings.export_section_log": {
        "it": "Ultimi {count} righe di client.log", "en": "Last {count} client.log lines",
    },
    "settings.export_no_log": {"it": "(nessun log disponibile)", "en": "(no log available)"},
    "settings.export_redacted": {"it": "[RIMOSSO]", "en": "[REDACTED]"},
    "settings.export_dialog_title": {
        "it": "Salva bundle diagnostico", "en": "Save diagnostic bundle",
    },
    "settings.export_filter": {
        "it": "File di testo (*.txt);;Archivio ZIP (*.zip)",
        "en": "Text file (*.txt);;ZIP archive (*.zip)",
    },
    "settings.export_done_title": {"it": "Bundle esportato", "en": "Bundle exported"},
    "settings.export_done_body": {
        "it": "Bundle salvato in:\n{path}", "en": "Bundle saved to:\n{path}",
    },
    "settings.export_failed_title": {
        "it": "Esportazione non riuscita", "en": "Export failed",
    },
    "settings.export_failed_body": {
        "it": "Impossibile scrivere il bundle: {detail}",
        "en": "Could not write the bundle: {detail}",
    },
}
_REDACTED = _NEW_STRINGS["settings.export_redacted"]["it"]


@pytest.fixture(autouse=True)
def _new_translations(monkeypatch):
    for key, entry in _NEW_STRINGS.items():
        monkeypatch.setitem(i18n.STRINGS, key, entry)
    i18n.set_language("it")
    yield
    i18n.set_language(i18n.DEFAULT_LANG)


@pytest.fixture
def settings_tab(qapp, _new_translations):
    with ClientEnvironment():
        cfg = Config()
        tab = SettingsTab(cfg, MagicMock())
        yield tab, cfg


def _section(tab: SettingsTab, key: str) -> QGroupBox:
    box = tab.findChild(QGroupBox, f"settingsSection_{key}")
    assert box is not None, key
    return box


# --- search ---------------------------------------------------------------

def test_search_bar_uses_translated_placeholder(settings_tab):
    tab, _cfg = settings_tab
    assert tab.search_edit.placeholderText() == _NEW_STRINGS["settings.search_placeholder"]["it"]
    assert tab.search_edit.toolTip().strip()


def test_ctrl_k_shortcut_focuses_search(settings_tab, monkeypatch):
    tab, _cfg = settings_tab
    assert tab.search_shortcut.key() == QKeySequence("Ctrl+K")
    focused: list[bool] = []
    monkeypatch.setattr(
        tab.search_edit, "setFocus", lambda *_args, **_kwargs: focused.append(True)
    )
    tab._focus_search()
    assert focused == [True]


def test_search_hides_non_matching_sections_and_restores_them(settings_tab):
    tab, _cfg = settings_tab
    nas = _section(tab, "nas")
    bandwidth = _section(tab, "bandwidth")
    assert not nas.isHidden() and not bandwidth.isHidden()

    # "KB/s" is present in the bandwidth title in both it and en.
    tab.search_edit.setText("KB/s")
    assert bandwidth.isHidden() is False
    assert nas.isHidden() is True

    tab.search_edit.setText("")
    assert nas.isHidden() is False
    assert bandwidth.isHidden() is False
    assert all(not section.isHidden() for section in tab._sections)


def test_search_highlights_matching_fields_then_clears(settings_tab):
    tab, _cfg = settings_tab
    nas = _section(tab, "nas")
    # The LAN host placeholder contains "nas.local" in both languages.
    tab.search_edit.setText("nas.local")
    assert tab.nas_lan.property("settingsSearchMatch") is True
    marked = [
        widget for widget in nas.findChildren(QWidget)
        if widget.property("settingsSearchMatch") is True
    ]
    assert marked
    assert tab._highlighted
    tab.search_edit.setText("")
    assert not tab._highlighted
    assert not [
        widget for widget in nas.findChildren(QWidget)
        if widget.property("settingsSearchMatch") is True
    ]


def test_search_does_not_destroy_user_collapse_state(settings_tab):
    tab, _cfg = settings_tab
    nas = _section(tab, "nas")             # stays collapsed: does not match
    bandwidth = _section(tab, "bandwidth")  # must be expanded while matching
    nas.setChecked(False)
    bandwidth.setChecked(False)

    tab.search_edit.setText("KB/s")
    assert nas.isChecked() is False
    assert bandwidth.isChecked() is True
    tab.search_edit.setText("")
    assert nas.isChecked() is False
    assert bandwidth.isChecked() is False


# --- collapsible sections -------------------------------------------------

def test_sections_are_checkable_and_collapse_their_content(settings_tab):
    tab, _cfg = settings_tab
    nas = _section(tab, "nas")
    assert nas.isCheckable() and nas.isChecked()
    fields = tab._section_fields(nas)
    assert tab.nas_lan in fields and tab.ssh_port in fields
    assert all(not field.isHidden() for field in fields)

    nas.setChecked(False)
    assert all(field.isHidden() for field in fields)
    nas.setChecked(True)
    assert all(not field.isHidden() for field in fields)


def test_collapsing_a_section_does_not_change_values(settings_tab):
    tab, cfg = settings_tab
    nas = _section(tab, "nas")
    before = cfg.get("ssh_port")
    tab.ssh_port.setValue(2222)
    nas.setChecked(False)
    tab._save_settings()
    nas.setChecked(True)
    assert cfg.get("ssh_port") == 2222
    assert before == 22


# --- NAS-reported values --------------------------------------------------

def test_nas_facts_come_from_cfg_without_new_network_calls(settings_tab):
    tab, cfg = settings_tab
    cfg.set("retention_days_remote", 45, persist=False)
    cfg.set("server_version", "3.16.0", persist=False)
    cfg.set("server_lock_held", True, persist=False)
    cfg.set("server_lock_owner_id", "pc-122", persist=False)
    cfg.set("server_lock_age_seconds", 125, persist=False)

    with patch(
        "sync_client.gui.settings_tab.rsync_ops.resolve_connection",
        side_effect=AssertionError("no network call allowed"),
    ):
        tab._refresh_nas_facts()

    assert "45" in tab.nas_fact_retention.text()
    assert "3.16.0" in tab.nas_fact_server_version.text()
    assert "pc-122" in tab.nas_fact_lock.text()
    assert "125" in tab.nas_fact_lock.text()
    reported = tab.nas_fact_retention.text().casefold()
    assert "rilevato dal nas" in reported or "detected from nas" in reported


def test_detect_reuses_fetched_version_for_nas_fact(settings_tab):
    tab, cfg = settings_tab
    result = {
        "connected": True,
        "fetch_ok": True,
        "script_path": "",
        "multiple_found": None,
        "err": "",
        "values": {
            "VERSION": "3.20.0",
            "RETENTION_DAYS": "60",
            "running": True,
            "REPOSITORY_READY": "true",
            "JOURNAL_READY": "true",
            "REPOSITORY_ID": "repo-123",
        },
    }
    with patch("sync_client.gui.settings_tab.QMessageBox.information"):
        tab._on_detect_done(result, None)
    assert cfg.get("server_version") == "3.20.0"
    assert cfg.get("retention_days_remote") == 60
    assert "3.20.0" in tab.nas_fact_server_version.text()


def test_nas_facts_handle_missing_and_free_lock(settings_tab):
    tab, cfg = settings_tab
    cfg.set("server_version", "", persist=False)
    cfg.set("server_lock_held", False, persist=False)
    cfg.set("retention_days_remote", 0, persist=False)
    tab._refresh_nas_facts()
    assert _NEW_STRINGS["settings.nas_not_reported"]["it"] in tab.nas_fact_server_version.text()
    assert _NEW_STRINGS["settings.nas_lock_free"]["it"] in tab.nas_fact_lock.text()
    assert tab.nas_fact_retention.text().strip()


# --- diagnostic bundle ----------------------------------------------------

def test_export_writes_redacted_bundle_with_log_tail(settings_tab, tmp_path):
    tab, cfg = settings_tab
    cfg.set("nas_user", "andres", persist=False)
    cfg.set("ssh_private_key", "TOPSECRETKEY", persist=False)
    cfg.set("auth_token", "TOKEN123", persist=False)
    cfg.set("ssh_known_hosts", "/home/andres/.ssh/known_hosts", persist=False)
    cfg.set("mirrors", [{"source": "/tmp/docs", "password": "NESTEDSECRET"}], persist=False)
    cfg.set("server_lock_held", True, persist=False)
    cfg.set("server_lock_age_seconds", 42, persist=False)
    cfg.set("remote_repository_ready", True, persist=False)

    log = paths.log_file()
    log.parent.mkdir(parents=True, exist_ok=True)
    log.write_text(
        "\n".join(
            [f"logline-{index}" for index in range(205)]
            + [
                "password=hunter2",
                "-----BEGIN RSA PRIVATE KEY-----",
                "MIIEowIBAAKCAQEAverysecretpayload",
                "ssh-rsa AAAAB3NzaC1yc2EAAAADAQAB user@nas",
            ]
        ),
        encoding="utf-8",
    )

    destination = tmp_path / "bundle.txt"
    with patch(
        "sync_client.gui.settings_tab.QFileDialog.getSaveFileName",
        return_value=(str(destination), ""),
    ), patch("sync_client.gui.settings_tab.QMessageBox.information") as info:
        tab._export_diagnostics()

    info.assert_called_once()
    text = destination.read_text(encoding="utf-8")
    assert APP_VERSION in text
    assert "andres" in text               # username is useful, not a secret
    assert "logline-204" in text
    assert "logline-0" not in text         # only the last N lines
    assert "server_lock_age_seconds = 42" in text
    assert "remote_repository_ready = True" in text
    for secret in ("TOPSECRETKEY", "TOKEN123", "NESTEDSECRET", "hunter2", "MIIEowIBAA", "AAAAB3NzaC1yc2E", "/home/andres/.ssh/known_hosts"):
        assert secret not in text
    assert f"ssh_private_key = {_REDACTED}" in text
    assert f"auth_token = {_REDACTED}" in text
    assert "password=" + _REDACTED in text


def test_export_zip_contains_one_text_member(settings_tab, tmp_path):
    tab, _cfg = settings_tab
    destination = tmp_path / "bundle.zip"
    with patch(
        "sync_client.gui.settings_tab.QFileDialog.getSaveFileName",
        return_value=(str(destination), ""),
    ), patch("sync_client.gui.settings_tab.QMessageBox.information"):
        tab._export_diagnostics()

    with zipfile.ZipFile(destination) as archive:
        assert archive.namelist() == ["diagnostics.txt"]
        assert APP_VERSION in archive.read("diagnostics.txt").decode("utf-8")


def test_export_cancel_does_not_write_anything(settings_tab, tmp_path):
    tab, _cfg = settings_tab
    with patch(
        "sync_client.gui.settings_tab.QFileDialog.getSaveFileName",
        return_value=("", ""),
    ), patch("sync_client.gui.settings_tab.QMessageBox.information") as info:
        tab._export_diagnostics()
    info.assert_not_called()
    assert list(tmp_path.iterdir()) == []
