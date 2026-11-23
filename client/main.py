#!/usr/bin/env python3
"""Entry point for the NASBox graphical client (PyQt6)."""
from __future__ import annotations

import os
import sys
import traceback
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))

from PyQt6.QtCore import QLibraryInfo, QLocale, QTimer, QTranslator
from PyQt6.QtWidgets import QApplication, QMessageBox

from sync_client import config as config_module, i18n, instance_lock, logger as logger_module, paths, updater
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


def _report_fatal_startup_error(exc: Exception, show_dialog: bool = True) -> None:
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
    if show_dialog:
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
        updater.restart(current_root)
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
    current_root = Path(__file__).resolve().parent
    # Only the instance that actually runs counts as a start attempt: a second
    # launch refused by the lock must not push a healthy install towards rollback.
    if not instance_lock.acquire():
        print("NASBox è già in esecuzione per questo utente.", file=sys.stderr)
        return 0
    # A previous update may have installed a build that never managed to run:
    # put the working one back before anything else touches the GUI.
    restored = updater.rollback_if_needed(current_root)
    if restored:
        logger_module.shared().log(
            "ERROR", "-",
            f"aggiornamento client non riuscito: ripristinata la versione {restored}",
        )
    updater.mark_start_attempt(current_root)

    # Language must be resolved before any widget is built -- they read
    # translated strings once, at construction time (see i18n.py).
    cfg = config_module.shared()
    i18n.set_language(_resolve_language(cfg))

    service_mode = os.environ.get("NASBOX_SERVICE") == "1"
    if service_mode and not (os.environ.get("DISPLAY") or os.environ.get("WAYLAND_DISPLAY")):
        os.environ.setdefault("QT_QPA_PLATFORM", "offscreen")
    app = QApplication(sys.argv)
    app.setApplicationName(APP_NAME)
    app.setApplicationVersion(APP_VERSION)
    _load_qt_base_translations(app, i18n.current_language())

    update_allowed = updater.auto_update_allowed(cfg, current_root)
    if not update_allowed:
        logger_module.shared().log(
            "INFO", "-",
            "aggiornamento automatico disattivato: questa cartella è un checkout git "
            "(usa NASBOX_UPDATE_FORCE=1 per forzarlo)",
        )
    if update_allowed and service_mode:
        candidate = updater.find_update(cfg, current_root, sys.argv[0])
        if candidate is not None:
            try:
                source_root = candidate.materialize()
                updater.install_update(source_root, current_root)
                candidate.cleanup()
                # Let systemd start a fresh process instead of exec'ing while
                # the Qt application and its worker threads are still alive.
                return 75
            except Exception as exc:
                candidate.cleanup()
                logger_module.shared().log(
                    "ERROR", "-", f"aggiornamento automatico client fallito: {exc}",
                )
    elif update_allowed and _offer_update(cfg, current_root):
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
        _report_fatal_startup_error(exc, show_dialog=not service_mode)
        return 1

    if service_mode:
        window.hide()
    else:
        window.show()

    # The update is confirmed only once the window is actually up: if this build
    # crashes during startup, the marker stays and the next start rolls back.
    # A slow machine can need more than 15 s to finish its first reconciliation;
    # scaling the window with the queued work avoids rolling back a healthy
    # build. A clean exit is also a successful start, whatever its duration.
    try:
        pending_count = len(window.sync_state.pending_paths()) if window.sync_state else 0
    except Exception:
        pending_count = 0
    confirm_after_ms = min(120_000, max(15_000, 5_000 + pending_count * 20))
    QTimer.singleShot(confirm_after_ms, lambda: updater.confirm_started_ok(current_root))
    app.aboutToQuit.connect(lambda: updater.confirm_started_ok(current_root))

    return app.exec()


if __name__ == "__main__":
    raise SystemExit(main())
