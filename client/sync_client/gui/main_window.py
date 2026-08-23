from __future__ import annotations

import threading
import time
from pathlib import Path

from PyQt6.QtCore import Qt, QTimer
from PyQt6.QtWidgets import (
    QApplication, QFrame, QHBoxLayout, QLabel, QMainWindow, QProgressDialog,
    QMessageBox, QSystemTrayIcon, QTabWidget, QVBoxLayout, QWidget,
)

from .. import config as config_module
from .. import logger as logger_module
from .. import updater
from ..engine import SyncEngine
from ..mirrors import MirrorManager
from ..version import APP_NAME, APP_VERSION
from ..pull_worker import PullWorker
from ..push_worker import PushWorker
from ..scan_worker import ScanWorker
from ..sync_state import SyncStateStore
from ..transfer_scheduler import TransferScheduler
from ..i18n import t
from ..lock_coordinator import LockCoordinator
from ..watcher import WatcherHandle
from .async_utils import run_in_background
from . import icons
from .dialogs import FirstRunSetupWizard, FolderSetupDialog
from ..repository_safety import RepositorySafetyError, initialize_local_root
from .browse_tab import BrowseTab
from .conflicts_tab import ConflictsTab
from .history_tab import HistoryTab
from .log_tab import LogTab
from .mirrors_tab import MirrorsTab
from .settings_tab import SettingsTab
from .status_tab import StatusTab
from .transfers_tab import TransfersTab
from .tray import TrayIcon


class MainWindow(QMainWindow):
    def __init__(self) -> None:
        super().__init__()
        self.setWindowTitle(f"{APP_NAME} {APP_VERSION}")
        self.setWindowIcon(icons.app_icon())
        self.resize(1280, 760)
        self.setMinimumSize(720, 520)
        self._is_shutting_down = False
        self._shutdown_thread: threading.Thread | None = None
        self._shutdown_dialog: QProgressDialog | None = None
        self._shutdown_started_at = 0.0
        self._server_update_busy = False

        self.cfg = config_module.shared()
        self.logger = logger_module.shared()

        if not self.cfg.is_configured():
            self._run_first_time_setup()

        watchers = WatcherHandle()
        transfer_scheduler = TransferScheduler()
        transfer_active = threading.Event()
        lock_coordinator = LockCoordinator()
        sync_state = SyncStateStore(self.cfg)
        self.sync_state = sync_state
        self.scan_worker = ScanWorker(
            self.cfg, transfer_active=transfer_active, sync_state=sync_state,
            transfer_lock=transfer_scheduler.permit("preview"),
            watchers=watchers,
            lock_coordinator=lock_coordinator,
        )
        self.push_worker = PushWorker(
            self.cfg, self.logger, watchers, transfer_scheduler.permit("push"), sync_state,
            scan_worker=self.scan_worker, transfer_active=transfer_active,
            lock_coordinator=lock_coordinator,
        )
        self.pull_worker = PullWorker(
            self.cfg, self.logger, watchers, transfer_scheduler.permit("pull"), sync_state,
            scan_worker=self.scan_worker, transfer_active=transfer_active,
            lock_coordinator=lock_coordinator,
        )
        self.engine = SyncEngine(
            self.cfg, self.logger, watchers,
            push_worker=self.push_worker, pull_worker=self.pull_worker,
            sync_state=sync_state,
        )
        self.mirror_manager = MirrorManager(self.cfg, self.logger, transfer_scheduler.permit("mirror"))

        shell = QWidget()
        shell_layout = QVBoxLayout(shell)
        shell_layout.setContentsMargins(20, 18, 20, 20)
        shell_layout.setSpacing(14)

        header = QFrame()
        header.setObjectName("appHeader")
        header_layout = QHBoxLayout(header)
        header_layout.setContentsMargins(18, 14, 18, 14)
        mark = QLabel()
        mark.setPixmap(icons.app_icon().pixmap(40, 40))
        header_layout.addWidget(mark)
        brand = QVBoxLayout()
        title = QLabel(APP_NAME)
        title.setObjectName("appTitle")
        brand.addWidget(title)
        subtitle = QLabel(t("main_window.brand_tagline"))
        subtitle.setObjectName("appSubtitle")
        brand.addWidget(subtitle)
        header_layout.addLayout(brand)
        header_layout.addStretch(1)
        version = QLabel(f"v{APP_VERSION}")
        version.setObjectName("versionBadge")
        version.setCursor(Qt.CursorShape.PointingHandCursor)
        version.setToolTip(t("main_window.version_tooltip"))
        version.mousePressEvent = lambda _e: self._check_for_client_update()
        header_layout.addWidget(version)
        shell_layout.addWidget(header)

        tabs = QTabWidget()
        tabs.setObjectName("mainTabs")
        tabs.setDocumentMode(True)
        self.tabs = tabs
        self.status_tab = StatusTab(
            self.cfg, self.engine, self.scan_worker, logger=self.logger,
        )
        self.settings_tab = SettingsTab(self.cfg, self.engine)
        self.transfers_tab = TransfersTab()
        self.log_tab = LogTab(self.logger)
        self.history_tab = HistoryTab(self.cfg, self.engine, self.logger)
        self.conflicts_tab = ConflictsTab(self.cfg, self.engine)
        self.browse_tab = BrowseTab(self.cfg, self.engine, self.logger, self.sync_state)
        self.mirrors_tab = MirrorsTab(self.cfg, self.mirror_manager)

        tabs.addTab(self.status_tab, t("main_window.tab_status"))
        tabs.addTab(self.transfers_tab, t("main_window.tab_transfers"))
        tabs.addTab(self.history_tab, t("main_window.tab_history"))
        tabs.addTab(self.conflicts_tab, t("main_window.tab_conflicts"))
        tabs.addTab(self.browse_tab, t("main_window.tab_browse"))
        tabs.addTab(self.log_tab, t("main_window.tab_log"))
        tabs.addTab(self.mirrors_tab, t("main_window.tab_mirrors"))
        tabs.addTab(self.settings_tab, t("main_window.tab_settings"))
        tabs.setTabToolTip(0, t("main_window.tab_status_tooltip"))
        tabs.setTabToolTip(1, t("main_window.tab_transfers_tooltip"))
        tabs.setTabToolTip(2, t("main_window.tab_history_tooltip"))
        tabs.setTabToolTip(3, t("main_window.tab_conflicts_tooltip"))
        tabs.setTabToolTip(4, t("main_window.tab_browse_tooltip"))
        tabs.setTabToolTip(5, t("main_window.tab_log_tooltip"))
        tabs.setTabToolTip(6, t("main_window.tab_mirrors_tooltip"))
        tabs.setTabToolTip(7, t("main_window.tab_settings_tooltip"))
        shell_layout.addWidget(tabs, 1)
        self.setCentralWidget(shell)

        self.engine.status_changed.connect(self.status_tab.on_status_changed)
        self.status_tab.attention_action_requested.connect(self._open_status_action)
        self.engine.connection_changed.connect(self.scan_worker.set_connection)
        self.engine.connection_changed.connect(self.push_worker.set_connection)
        self.engine.connection_changed.connect(self.pull_worker.set_connection)
        self.scan_worker.queue_updated.connect(self.transfers_tab.on_queue_updated)
        self.scan_worker.scan_started.connect(self.transfers_tab.on_queue_scan_started)
        self.scan_worker.scan_finished.connect(self.transfers_tab.on_queue_scan_finished)
        self.scan_worker.queue_updated.connect(self.status_tab.on_queue_updated)

        for worker in (self.push_worker, self.pull_worker):
            worker.transfer_preparing.connect(self.transfers_tab.on_transfer_preparing)
            worker.transfer_preparing.connect(self.status_tab.on_transfer_preparing)
            worker.transfer_phase.connect(self.transfers_tab.on_transfer_phase)
            worker.transfer_waiting_for_lock.connect(self.transfers_tab.on_transfer_waiting_for_lock)
            worker.transfer_waiting_for_lock.connect(self.status_tab.on_transfer_waiting_for_lock)
            worker.transfer_lock_unavailable.connect(self.transfers_tab.on_transfer_lock_unavailable)
            worker.transfer_lock_unavailable.connect(self.status_tab.on_transfer_lock_unavailable)
            worker.transfer_started.connect(self.transfers_tab.on_transfer_started)
            worker.transfer_started.connect(self.status_tab.on_transfer_started)
            worker.transfer_item_started.connect(self.transfers_tab.on_item_started)
            worker.transfer_item_started.connect(self.status_tab.on_transfer_item_started)
            worker.transfer_item_progress.connect(self.transfers_tab.on_item_progress)
            worker.transfer_item_done.connect(self.transfers_tab.on_item_done)
            worker.transfer_item_done.connect(self.status_tab.on_transfer_item_done)
            worker.transfer_speed.connect(self.transfers_tab.on_speed_update)
            worker.transfer_finished.connect(self.transfers_tab.on_transfer_finished)
            worker.transfer_finished.connect(self.status_tab.on_transfer_finished)
            worker.log_event.connect(self.log_tab.on_log_event)
            worker.log_event.connect(self.status_tab.on_log_event)
        self.push_worker.hash_progress.connect(self.status_tab.on_hash_progress)
        self.push_worker.hash_progress.connect(self.transfers_tab.on_preflight_progress)
        self.push_worker.batch_size_known.connect(self.transfers_tab.on_batch_size_known)
        self.push_worker.queue_items_known.connect(self.transfers_tab.on_queue_items_known)
        self.engine.log_event.connect(self.log_tab.on_log_event)
        self.engine.log_event.connect(self.status_tab.on_log_event)
        self.mirror_manager.status_changed.connect(self.mirrors_tab.on_status_changed)
        self.mirror_manager.log_event.connect(self.log_tab.on_log_event)
        self.mirror_manager.log_event.connect(self.status_tab.on_log_event)

        self.tray = TrayIcon(self.cfg, self.engine, self.logger, self)
        for direction, worker in (("upload", self.push_worker), ("download", self.pull_worker)):
            worker.log_event.connect(self.tray.on_log_event)
            worker.transfer_preparing.connect(self.tray.on_transfer_preparing)
            worker.transfer_finished.connect(self.tray.on_transfer_finished)
            item_done_slot = (
                self.tray.on_upload_item_done if direction == "upload"
                else self.tray.on_download_item_done
            )
            worker.transfer_item_done.connect(item_done_slot)
        self.engine.status_changed.connect(self.tray.on_status_changed)
        self.scan_worker.queue_updated.connect(self.tray.on_queue_updated)
        self.engine.server_outdated.connect(self._on_server_outdated)
        self.engine.server_update_available.connect(self._on_server_update_available)
        self.engine.server_restarted.connect(self._on_server_restarted)
        self.tray.show()

        self.engine.start()
        self.scan_worker.start()
        self.push_worker.start()
        self.pull_worker.start()
        self.mirror_manager.start()

    def _open_status_action(self, action: str) -> None:
        destination = {
            "settings": self.settings_tab,
            "log": self.log_tab,
            "history": self.history_tab,
            "conflicts": self.conflicts_tab,
            "transfers": self.transfers_tab,
        }.get(action)
        if destination is not None:
            self.tabs.setCurrentWidget(destination)

    def _run_first_time_setup(self) -> None:
        """Collect the complete PC-side setup before starting any workers."""
        wizard = FirstRunSetupWizard(self.cfg, self)
        if not wizard.exec():
            return
        values = wizard.setup_values()
        chosen = Path(values.pop("local_root")).expanduser().resolve()
        try:
            chosen.mkdir(parents=True, exist_ok=True)
            initialize_local_root(str(chosen), str(values.get("repository_id") or ""))
        except (OSError, RepositorySafetyError) as exc:
            self.logger.log("ERROR", "-", detail=f"verifica cartella locale fallita: {exc}")
            QMessageBox.warning(self, t("setup.local_failed_title"), str(exc))
            return
        for key, value in values.items():
            self.cfg.set(key, value, persist=False)
        self.cfg.set("local_root", str(chosen), persist=False)
        self.cfg.save()

    def _on_server_outdated(self, message: str) -> None:
        self.tray.showMessage(
            f"{APP_NAME} — {t('main_window.server_outdated_suffix')}",
            message,
            QSystemTrayIcon.MessageIcon.Warning,
            15_000,
        )

    def _on_server_update_available(self, message: str, path: str, version: str) -> None:
        if self._server_update_busy:
            return
        if QMessageBox.question(
            self,
            t("main_window.server_update_available_title"),
            message,
            QMessageBox.StandardButton.Yes | QMessageBox.StandardButton.No,
            QMessageBox.StandardButton.Yes,
        ) != QMessageBox.StandardButton.Yes:
            return
        self._server_update_busy = True
        run_in_background(
            self,
            "_server_update_call",
            lambda: self.engine.update_server(path, version),
            lambda result, exc: self._on_server_update_done(result, exc),
        )

    def _on_server_update_done(self, result, exc: Exception | None) -> None:
        self._server_update_busy = False
        if exc is not None:
            QMessageBox.warning(self, t("main_window.server_update_available_title"), str(exc))
            return
        ok, detail = result
        if ok:
            QMessageBox.information(self, t("main_window.server_update_available_title"), detail)
            self.engine.wake()
        else:
            QMessageBox.warning(self, t("main_window.server_update_available_title"), detail)

    def _on_server_restarted(self, message: str) -> None:
        self.tray.showMessage(
            f"{APP_NAME} — {t('main_window.server_restarted_suffix')}",
            message,
            QSystemTrayIcon.MessageIcon.Warning,
            15_000,
        )

    def closeEvent(self, event) -> None:
        # Minimize to tray instead of quitting, like most sync clients.
        if self.tray.isVisible():
            event.ignore()
            self.hide()
            self.tray.showMessage(APP_NAME, t("main_window.tray_running_notice"))
        else:
            event.ignore()
            self.quit_application()

    _update_check_busy = False

    def _check_for_client_update(self) -> None:
        if self._update_check_busy:
            return
        self._update_check_busy = True
        current_root = Path(__file__).resolve().parents[2]
        run_in_background(
            self, "_update_check",
            lambda: {"candidate": updater.find_update(self.cfg, current_root, str(current_root / "main.py"))},
            self._on_client_update_done,
        )

    def _on_client_update_done(self, result, exc: Exception | None) -> None:
        self._update_check_busy = False
        if exc is not None:
            QMessageBox.warning(self, t("main_window.update_check_failed"), str(exc))
            return
        candidate = result.get("candidate") if result else None
        if candidate is None:
            QMessageBox.information(self, t("main_window.update_check_title"), t("main_window.update_none"))
            return
        answer = QMessageBox.question(
            self,
            t("main_window.update_available_title"),
            t("main_window.update_available_body", version=candidate.version, origin=candidate.origin),
            QMessageBox.StandardButton.Yes | QMessageBox.StandardButton.No,
            QMessageBox.StandardButton.Yes,
        )
        if answer != QMessageBox.StandardButton.Yes:
            candidate.cleanup()
            return
        try:
            import os, sys
            source_root = candidate.materialize()
            current_root = Path(__file__).resolve().parents[2]
            updater.install_update(source_root, current_root)
            candidate.cleanup()
            QMessageBox.information(self, t("main_window.update_restart_title"), t("main_window.update_restart_body"))
            os.execv(sys.executable, [sys.executable, str(current_root / "main.py"), *sys.argv[1:]])
        except Exception as exc:
            candidate.cleanup()
            QMessageBox.warning(self, t("main_window.update_failed_title"), str(exc))

    def quit_application(self) -> None:
        """Stop background workers without leaving the UI frozen during exit."""
        if self._is_shutting_down:
            return
        self._is_shutting_down = True
        self.hide()
        # Keep this dialog parentless: exiting from the tray often happens
        # while the main window is hidden, and a child dialog would be hidden
        # with it too, making the app look frozen.
        self._shutdown_dialog = QProgressDialog(t("main_window.shutdown_message"), None, 0, 0)
        self._shutdown_dialog.setWindowTitle(f"{APP_NAME} — {t('main_window.shutdown_title')}")
        self._shutdown_dialog.setWindowModality(Qt.WindowModality.ApplicationModal)
        self._shutdown_dialog.setCancelButton(None)
        self._shutdown_dialog.setMinimumDuration(0)
        self._shutdown_dialog.show()
        self._shutdown_dialog.raise_()
        self._shutdown_dialog.activateWindow()
        self._shutdown_started_at = time.monotonic()
        self._shutdown_thread = threading.Thread(target=self._shutdown, daemon=True)
        self._shutdown_thread.start()
        self._wait_for_shutdown()

    def _wait_for_shutdown(self) -> None:
        if self._shutdown_thread is not None and self._shutdown_thread.is_alive():
            elapsed = int(time.monotonic() - self._shutdown_started_at)
            if elapsed >= 5 and self._shutdown_dialog is not None:
                self._shutdown_dialog.setLabelText(t("main_window.shutdown_waiting", seconds=elapsed))
            QTimer.singleShot(50, self._wait_for_shutdown)
            return
        if self._shutdown_dialog is not None:
            self._shutdown_dialog.close()
            self._shutdown_dialog.deleteLater()
        self.tray.hide()
        QApplication.instance().quit()

    def _shutdown(self) -> None:
        # Stop the workers that actually move files first, then the coordinator
        # (which would otherwise keep reconciling/stopping the watcher while a
        # transfer might still be winding down), then the queue-preview scanner.
        for worker in (self.push_worker, self.pull_worker, self.engine, self.mirror_manager, self.scan_worker):
            try:
                worker.stop()
            except Exception as exc:
                self.logger.log("ERROR", "-", detail=f"errore durante chiusura: {exc}")
