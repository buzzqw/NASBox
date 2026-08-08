#!/usr/bin/env python3
"""Entry point for the NASBox graphical client (PyQt6)."""
from __future__ import annotations

import os
import sys
import traceback
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))

from PyQt6.QtCore import QLibraryInfo, QLocale, QTranslator
from PyQt6.QtWidgets import QApplication, QMessageBox

from sync_client import config as config_module
from sync_client import i18n, paths, updater
from sync_client.i18n import t
from sync_client.version import APP_NAME, APP_VERSION


def _resolve_language(cfg: config_module.Config) -> str:
    code = cfg.language()
    return i18n.detect_system_language() if code == "auto" else code


def _load_qt_base_translations(app: QApplication, lang_code: str) -> None:
    """Best-effort: translate Qt's own standard strings (the "OK"/"Cancel"/
    "Yes"/"No" buttons on QMessageBox/QDialogButtonBox) to match NASBox's own
    language -- otherwise those specific buttons stay in English regardless
    of what the rest of the UI says, since Qt needs its own catalog loaded
    for that. Silently does nothing if PyQt6 wasn't packaged with translation
    files for this Qt version (still leaves the app fully usable, just with
    English standard buttons)."""
    translations_path = QLibraryInfo.path(QLibraryInfo.LibraryPath.TranslationsPath)
    translator = QTranslator(app)
    if translator.load(QLocale(lang_code), "qtbase", "_", translations_path):
        app.installTranslator(translator)


def _report_fatal_startup_error(exc: Exception) -> None:
    """A crash while building the main window (bad config, a missing system
    dependency, ...) would otherwise just dump a traceback to a terminal
    nobody's watching -- this runs as a background app, often started by
    systemd rather than by hand. Write it to the log file too (in case even
    the message box can't show, e.g. no display) and surface it visibly
    instead of failing silently."""
    detail = traceback.format_exc()
    try:
        paths.ensure_dirs()
        with paths.log_file().open("a", encoding="utf-8") as f:
            f.write(f"[FATAL STARTUP ERROR] {exc}\n{detail}\n")
    except OSError:
        pass
    try:
        QMessageBox.critical(
            None, f"{APP_NAME} — {t('main_window.startup_error_title')}",
            f"{APP_NAME}:\n\n{exc}\n\n{paths.log_file()}",
        )
    except Exception:
        pass  # no display available at all -- the log write above is the fallback
    print(detail, file=sys.stderr)


def _offer_update(cfg: config_module.Config, current_root: Path) -> bool:
    """Offer a newer client before importing/building the main window."""
    candidate = updater.find_update(cfg, current_root, sys.argv[0])
    if candidate is None:
        return False
    answer = QMessageBox.question(
        None,
        t("updater.available_title"),
        t("updater.available_body", version=candidate.version, origin=candidate.origin),
        QMessageBox.StandardButton.Yes | QMessageBox.StandardButton.No,
        QMessageBox.StandardButton.Yes,
    )
    if answer != QMessageBox.StandardButton.Yes:
        candidate.cleanup()
        return False
    try:
        source_root = candidate.materialize()
        updater.install_update(source_root, current_root)
        candidate.cleanup()
        QMessageBox.information(None, t("updater.restarting_title"), t("updater.restarting_body"))
        os.execv(sys.executable, [sys.executable, str(current_root / "main.py"), *sys.argv[1:]])
    except Exception as exc:
        candidate.cleanup()
        QMessageBox.warning(
            None,
            t("updater.failed_title"),
            t("updater.failed_body", detail=str(exc)),
        )
        return False
    return True


def main() -> int:
    paths.ensure_dirs()

    # Language must be resolved before any widget is built -- they read
    # translated strings once, at construction time (see i18n.py).
    cfg = config_module.shared()
    i18n.set_language(_resolve_language(cfg))

    app = QApplication(sys.argv)
    app.setApplicationName(APP_NAME)
    app.setApplicationVersion(APP_VERSION)
    _load_qt_base_translations(app, i18n.current_language())
    if _offer_update(cfg, Path(__file__).resolve().parent):
        return 0

    from sync_client.gui import icons
    from sync_client.gui.main_window import MainWindow
    from sync_client.gui.style import apply_style

    app.setQuitOnLastWindowClosed(False)  # keep running in the tray when the window is closed
    app.setWindowIcon(icons.app_icon())
    apply_style(app)

    try:
        window = MainWindow()
    except Exception as exc:  # keep a startup failure visible instead of a silent exit
        _report_fatal_startup_error(exc)
        return 1

    window.show()

    return app.exec()


if __name__ == "__main__":
    raise SystemExit(main())
