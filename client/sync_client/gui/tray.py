from __future__ import annotations

import os

from PyQt6.QtCore import QUrl
from PyQt6.QtGui import QCursor, QDesktopServices
from PyQt6.QtWidgets import QMenu, QSystemTrayIcon

from .. import trash
from ..config import Config
from ..engine import SyncEngine
from ..i18n import t
from ..logger import EventLogger
from ..rsync_ops import TransferItem
from . import icons
from .dialogs import PauseForDialog

RECENT_ACTIONS = {"UPLOAD", "DOWNLOAD", "DELETE_LOCAL", "DELETE_REMOTE"}
RECENT_ICONS = {
    "UPLOAD": "↑", "DOWNLOAD": "↓", "DELETE_LOCAL": "🗑", "DELETE_REMOTE": "🗑",
}


class TrayIcon(QSystemTrayIcon):
    def __init__(self, cfg: Config, engine: SyncEngine, logger: EventLogger, main_window, parent=None) -> None:
        super().__init__(icons.tray_icon("unconfigured"), parent)
        self.cfg = cfg
        self.engine = engine
        self.logger = logger
        self.main_window = main_window

        self._status_text = t("tray.starting")
        # Drives which of the icons.STATUS_COLORS variants is shown -- see
        # _resolve_state(). Updated independently by on_status_changed (engine
        # tick) and on_queue_updated (scan_worker's periodic dry-run preview),
        # so the icon is re-resolved from whichever changed most recently.
        self._configured = False
        self._connected = False
        self._paused = False
        self._pending_count = 0

        menu = QMenu()
        menu.aboutToShow.connect(self._rebuild_menu)
        self.setContextMenu(menu)
        self._menu = menu

        self.activated.connect(self._on_activated)
        self._apply_icon()

    # --- icon + tooltip (state-driven, see class docstring above) ---

    def _resolve_state(self) -> str:
        if not self._configured:
            return "unconfigured"
        if not self._connected:
            return "error"
        if self._paused:
            return "paused"
        if self._pending_count > 0:
            return "syncing"
        return "synced"

    def _apply_icon(self) -> None:
        self.setIcon(icons.tray_icon(self._resolve_state()))
        suffix = t("tray.pending_suffix", count=self._pending_count) if self._pending_count else ""
        self.setToolTip(f"NASBox — {self._status_text}{suffix}")

    # --- menu construction (rebuilt each time it's opened, so recent files stay current) ---

    def _rebuild_menu(self) -> None:
        menu = self._menu
        menu.clear()

        status_action = menu.addAction(self._status_text)
        status_action.setEnabled(False)
        if self._pending_count:
            pending_action = menu.addAction(t("tray.pending_files", count=self._pending_count))
            pending_action.setEnabled(False)
        menu.addSeparator()

        open_action = menu.addAction(t("tray.open_folder"))
        open_action.setEnabled(bool(self.cfg.local_root()))
        open_action.triggered.connect(self._open_folder)

        recent_menu = menu.addMenu(t("tray.recent_files_menu"))
        self._populate_recent_menu(recent_menu)

        menu.addSeparator()

        toggle_label = t("tray.resume") if self.cfg.is_paused() else t("tray.pause")
        toggle_action = menu.addAction(toggle_label)
        toggle_action.triggered.connect(self._toggle_pause)

        pause_for_action = menu.addAction(t("tray.pause_for"))
        pause_for_action.triggered.connect(self._pause_for)

        menu.addSeparator()

        show_action = menu.addAction(t("tray.show_window"))
        show_action.triggered.connect(self._show_window)

        menu.addSeparator()

        quit_action = menu.addAction(t("tray.quit"))
        quit_action.triggered.connect(self._quit)

    def _populate_recent_menu(self, recent_menu: QMenu) -> None:
        events = [e for e in self.logger.tail(limit=300) if e.action in RECENT_ACTIONS]
        events.reverse()  # most recent first
        events = events[:8]

        if not events:
            empty = recent_menu.addAction(t("tray.no_recent_activity"))
            empty.setEnabled(False)
            return

        local_root = self.cfg.local_root()
        for ev in events:
            icon = RECENT_ICONS.get(ev.action, "")
            label = f"{icon} {ev.path}"
            if len(label) > 60:
                label = label[:57] + "…"
            action = recent_menu.addAction(label)
            if local_root:
                action.triggered.connect(
                    lambda _checked=False, p=ev.path: self._open_containing_folder(local_root, p)
                )
            else:
                action.setEnabled(False)

    # --- actions ---

    def _open_path(self, path: str) -> None:
        QDesktopServices.openUrl(QUrl.fromLocalFile(path))

    def _open_folder(self) -> None:
        local_root = self.cfg.local_root()
        if local_root:
            self._open_path(local_root)

    def _open_containing_folder(self, local_root: str, relative_path: str) -> None:
        full = os.path.join(local_root, relative_path)
        target = full if os.path.exists(full) else os.path.dirname(full) or local_root
        if os.path.isfile(target):
            target = os.path.dirname(target)
        self._open_path(target)

    def _on_activated(self, reason) -> None:
        # Deliberately NOT relying on setContextMenu()'s native right-click
        # handling alone -- several Linux/Wayland tray hosts (seen on this
        # very setup) either don't distinguish a secondary click from a
        # primary one, or never dispatch a native context-menu request at
        # all, so a menu built that way is simply unreachable there: right-
        # click just re-triggers whatever the primary action does, and
        # "Esci" becomes unreachable since closing the window only hides it
        # (see MainWindow.closeEvent). Popping the menu ourselves on every
        # single-click activation (left or right; whichever the host does
        # report) works everywhere, at the cost of the window not reopening
        # on a plain single click -- double-click still does that below.
        if reason == QSystemTrayIcon.ActivationReason.DoubleClick:
            self._show_window()
            return
        if reason in (
            QSystemTrayIcon.ActivationReason.Trigger,
            QSystemTrayIcon.ActivationReason.Context,
            QSystemTrayIcon.ActivationReason.MiddleClick,
        ):
            # popup() itself emits aboutToShow, which is what actually
            # rebuilds the menu's contents (see the connection in __init__) --
            # no need to call _rebuild_menu() here too.
            self._menu.popup(QCursor.pos())

    def _show_window(self) -> None:
        self.main_window.showNormal()
        self.main_window.activateWindow()

    def _quit(self) -> None:
        self._menu.hide()
        self.main_window.hide()
        self.main_window.quit_application()

    def _toggle_pause(self) -> None:
        if self.cfg.is_paused():
            self.cfg.resume()
        else:
            self.cfg.pause_indefinitely()
            self.engine.cancel_current_transfer()
        self.engine.wake()

    def _pause_for(self) -> None:
        dlg = PauseForDialog(self.main_window)
        if dlg.exec():
            self.cfg.pause_for_seconds(dlg.seconds())
            self.engine.cancel_current_transfer()
            self.engine.wake()

    # --- signals from engine / scan_worker ---

    def on_status_changed(self, status: dict) -> None:
        self._paused = status.get("paused", False)
        self._connected = status.get("connected", False)
        self._configured = status.get("configured", True)
        via_jump = status.get("via_jump", False)
        host = status.get("host")

        if not self._configured:
            self._status_text = t("tray.not_configured")
        elif not self._connected:
            self._status_text = t("tray.nas_unreachable")
        elif self._paused:
            self._status_text = t("tray.paused")
        else:
            where = f"{host} {t('tray.via_bastion')}" if via_jump else str(host)
            self._status_text = t("tray.active", where=where)

        self._apply_icon()

    def on_queue_updated(self, items: list[TransferItem]) -> None:
        self._pending_count = len(items)
        self._apply_icon()

    def on_log_event(self, action: str, path_: str, detail: str) -> None:
        if action == "CONFLICT":
            self.showMessage(
                t("tray.conflict_title"),
                t("tray.conflict_body", path=path_, detail=detail),
                QSystemTrayIcon.MessageIcon.Warning,
                15_000,
            )
