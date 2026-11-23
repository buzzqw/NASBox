from __future__ import annotations

import threading
import time
from pathlib import Path

from PyQt6.QtCore import QByteArray, QEvent, Qt, QSettings, QTimer
from PyQt6.QtGui import QKeySequence, QShortcut
from PyQt6.QtWidgets import (
    QApplication, QFrame, QHBoxLayout, QLabel, QMainWindow, QProgressDialog,
    QMessageBox, QScrollArea, QSystemTrayIcon, QTabWidget, QToolButton, QVBoxLayout, QWidget,
)

from .. import config as config_module
from .. import logger as logger_module
from .. import updater
from ..engine import SyncEngine
from ..mirrors import MirrorManager
from ..version import APP_NAME, APP_VERSION

#: How often a running client looks for a newer release on the NAS. Without
#: this, a client left open for days never noticed an update at all.
UPDATE_CHECK_INTERVAL_MS = 6 * 60 * 60 * 1000
#: Let the first scan/sync settle before the first silent check.
UPDATE_NOTICE_DELAY_MS = 60 * 1000
from ..pull_worker import PullWorker
from ..push_worker import PushWorker
from ..scan_worker import ScanWorker
from ..sync_state import SyncStateStore
from ..transfer_scheduler import TransferScheduler
from ..i18n import t
from ..lock_coordinator import LockCoordinator
from ..watcher import WatcherHandle
from .async_utils import run_in_background
from .style import SHORTCUT_SHOW_WINDOW
from . import icons
from .dialogs import FirstRunSetupWizard, FolderSetupDialog
from ..repository_safety import RepositorySafetyError, initialize_local_root
from .browse_tab import BrowseTab
from .conflicts_tab import ConflictsTab
from .history_tab import HistoryTab
from .log_tab import LogTab
from .metrics_tab import MetricsTab
from .mirrors_tab import MirrorsTab
from .settings_tab import SettingsTab
from .status_tab import StatusTab
from .transfers_tab import TransfersTab
from .tray import TrayIcon


def uniform_scrollbars(widget: QWidget) -> None:
    """Reserve the vertical gutter in every scrollable tab (no width jumps)."""
    for area in widget.findChildren(QScrollArea):
        area.setVerticalScrollBarPolicy(Qt.ScrollBarPolicy.ScrollBarAlwaysOn)
        area.setHorizontalScrollBarPolicy(Qt.ScrollBarPolicy.ScrollBarAlwaysOff)


class MainWindow(QMainWindow):
    def __init__(self) -> None:
        super().__init__()
        self.setWindowTitle(f"{APP_NAME} {APP_VERSION}")
        self.setWindowIcon(icons.app_icon())
        self.resize(1100, 680)
        self.setMinimumSize(720, 520)
        self._settings = QSettings("NASBox", "NASBox")
        geometry = self._settings.value("window/geometry")
        if isinstance(geometry, QByteArray):
            self.restoreGeometry(geometry)

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
        sync_state = SyncStateStore(self.cfg)
        transfer_scheduler = TransferScheduler(sync_state)
        transfer_active = threading.Event()
        push_requested = threading.Event()
        lock_coordinator = LockCoordinator()
        self.sync_state = sync_state
        self.scan_worker = ScanWorker(
            self.cfg, transfer_active=transfer_active, sync_state=sync_state,
            transfer_lock=transfer_scheduler.permit("preview"),
            watchers=watchers,
            lock_coordinator=lock_coordinator,
            push_requested=push_requested,
        )
        self.push_worker = PushWorker(
            self.cfg, self.logger, watchers, transfer_scheduler.permit("push"), sync_state,
            scan_worker=self.scan_worker, transfer_active=transfer_active,
            lock_coordinator=lock_coordinator,
            force_sync=push_requested,
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
            transfer_scheduler=transfer_scheduler,
        )
        # A committed push may have been exactly what kept PullWorker deferred
        # ("local changes not uploaded yet"). Wake it only after success so the
        # NAS changes just written are pulled without waiting for the next poll,
        # while a failed push (lock contention, network) keeps the normal backoff.
        self.push_worker.transfer_finished.connect(
            lambda _direction, ok: self.pull_worker.wake() if ok else None
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
        version = QToolButton()
        version.setText(f"v{APP_VERSION}")
        version.setObjectName("versionBadge")
        version.setToolButtonStyle(Qt.ToolButtonStyle.ToolButtonTextOnly)
        version.setAccessibleName(t("a11y.version_badge"))
        version.setToolTip(t("main_window.version_tooltip"))
        version.clicked.connect(self._check_for_client_update)
        header_layout.addWidget(version)
        shell_layout.addWidget(header)

        tabs = QTabWidget()
        tabs.setObjectName("mainTabs")
        tabs.setDocumentMode(True)
        tabs.setAccessibleName(t("a11y.main_tabs"))
        self.tabs = tabs
        self.status_tab = StatusTab(
            self.cfg, self.engine, self.scan_worker, logger=self.logger,
        )
        self.status_tab.assurance_status.connect(self._on_assurance_status)
        self.metrics_tab = MetricsTab(self.cfg, self.engine)
        self.settings_tab = SettingsTab(self.cfg, self.engine)
        self.transfers_tab = TransfersTab()
        self.log_tab = LogTab(self.logger)
        self.history_tab = HistoryTab(self.cfg, self.engine, self.logger)
        self.conflicts_tab = ConflictsTab(self.cfg, self.engine)
        self.browse_tab = BrowseTab(self.cfg, self.engine, self.logger, self.sync_state)
        self.mirrors_tab = MirrorsTab(self.cfg, self.mirror_manager)

        self._tab_labels = {
            self.status_tab: t("main_window.tab_status"),
            self.metrics_tab: t("main_window.tab_metrics"),
            self.transfers_tab: t("main_window.tab_transfers"),
            self.history_tab: t("main_window.tab_history"),
            self.conflicts_tab: t("main_window.tab_conflicts"),
            self.browse_tab: t("main_window.tab_browse"),
            self.log_tab: t("main_window.tab_log"),
            self.mirrors_tab: t("main_window.tab_mirrors"),
            self.settings_tab: t("main_window.tab_settings"),
        }
        for widget, label in self._tab_labels.items():
            tabs.addTab(widget, label)
        # Keep the vertical gutter reserved in every scrollable tab. Without
        # this the content width changes when switching between tabs whose
        # content happens to fit and tabs that need scrolling, which reads as
        # the whole interface jumping sideways.
        uniform_scrollbars(self)
        tabs.currentChanged.connect(lambda _index: self._update_metrics_tab_active())
        tabs.setTabToolTip(0, t("main_window.tab_status_tooltip"))
        tabs.setTabToolTip(1, t("main_window.tab_metrics_tooltip"))
        tabs.setTabToolTip(2, t("main_window.tab_transfers_tooltip"))
        tabs.setTabToolTip(3, t("main_window.tab_history_tooltip"))
        tabs.setTabToolTip(4, t("main_window.tab_conflicts_tooltip"))
        tabs.setTabToolTip(5, t("main_window.tab_browse_tooltip"))
        tabs.setTabToolTip(6, t("main_window.tab_log_tooltip"))
        tabs.setTabToolTip(7, t("main_window.tab_mirrors_tooltip"))
        tabs.setTabToolTip(8, t("main_window.tab_settings_tooltip"))
        shell_layout.addWidget(tabs, 1)
        self.setCentralWidget(shell)

        self.engine.status_changed.connect(self.status_tab.on_status_changed)
        self.status_tab.attention_action_requested.connect(self._open_status_action)
        self.conflicts_tab.conflict_count_changed.connect(self._on_conflict_count)
        self.engine.connection_changed.connect(self.scan_worker.set_connection)
        self.engine.connection_changed.connect(self.push_worker.set_connection)
        self.engine.connection_changed.connect(self.pull_worker.set_connection)
        self.engine.connection_changed.connect(self.metrics_tab.on_connection_changed)
        self._update_metrics_tab_active()
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
            worker.transfer_retry_scheduled.connect(self.transfers_tab.on_transfer_retry_scheduled)
            worker.transfer_retry_scheduled.connect(self.status_tab.on_transfer_retry_scheduled)
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
            worker.log_event.connect(self.conflicts_tab._on_log_event)
        self.push_worker.hash_progress.connect(self.status_tab.on_hash_progress)
        self.push_worker.hash_progress.connect(self.transfers_tab.on_preflight_progress)
        self.push_worker.batch_size_known.connect(self.transfers_tab.on_batch_size_known)
        self.push_worker.batch_size_known.connect(self.status_tab.on_batch_size_known)
        self.push_worker.queue_items_known.connect(self.transfers_tab.on_queue_items_known)
        self.engine.log_event.connect(self.log_tab.on_log_event)
        self.engine.log_event.connect(self.status_tab.on_log_event)
        self.mirror_manager.status_changed.connect(self.mirrors_tab.on_status_changed)
        self.mirror_manager.log_event.connect(self.log_tab.on_log_event)
        self.mirror_manager.log_event.connect(self.status_tab.on_log_event)

        self.tray = TrayIcon(self.cfg, self.engine, self.logger, self)
        self.tray.update_requested.connect(self._check_for_client_update)
        for direction, worker in (("upload", self.push_worker), ("download", self.pull_worker)):
            worker.log_event.connect(self.tray.on_log_event)
            worker.transfer_preparing.connect(self.tray.on_transfer_preparing)
            worker.transfer_finished.connect(self.tray.on_transfer_finished)
            item_done_slot = (
                self.tray.on_upload_item_done if direction == "upload"
                else self.tray.on_download_item_done
            )
            worker.transfer_item_done.connect(item_done_slot)
            worker.transfer_item_started.connect(self.tray.on_item_started)
            worker.transfer_speed.connect(self.tray.on_speed_update)
            worker.transfer_retry_scheduled.connect(self.tray.on_transfer_retry_scheduled)
            worker.transfer_speed.connect(self.status_tab.on_transfer_speed)
        self.engine.status_changed.connect(self.tray.on_status_changed)
        self.engine.server_health_changed.connect(self.tray.on_server_health_changed)
        self.scan_worker.queue_updated.connect(self.tray.on_queue_updated)
        self.engine.server_outdated.connect(self._on_server_outdated)
        self.engine.server_update_available.connect(self._on_server_update_available)
        self.engine.server_restarted.connect(self._on_server_restarted)
        # Global keyboard access to reopening the window from the tray.
        self.show_window_shortcut = QShortcut(QKeySequence(SHORTCUT_SHOW_WINDOW), self)
        self.show_window_shortcut.activated.connect(self._show_window)
        self.tray.show()
        # The first Conflicts scan starts before the signal is connected; push
        # its current count once so badges are right from the first paint.
        self._on_conflict_count(self.conflicts_tab.conflict_count())

        self.engine.start()
        self.push_worker.start()
        self.pull_worker.start()
        self.scan_worker.start()
        self.mirror_manager.start()

        # Updates are no longer only discovered at startup: a client that stays
        # open for days used to never hear about a new release. The periodic
        # check is silent (tray notice) and never interrupts with a modal.
        self._last_notified_version = ""
        QTimer.singleShot(UPDATE_NOTICE_DELAY_MS, self._check_for_client_update_quietly)
        self._update_check_timer = QTimer(self)
        self._update_check_timer.setInterval(UPDATE_CHECK_INTERVAL_MS)
        self._update_check_timer.timeout.connect(self._check_for_client_update_quietly)
        self._update_check_timer.start()
        QTimer.singleShot(1200, self._maybe_show_whats_new)

    def _maybe_show_whats_new(self) -> None:
        """Show release notes once after an update, then remember the version."""
        if not self.cfg.is_configured():
            return
        seen = str(self.cfg.get("last_seen_release_notes_version") or "")
        if seen == APP_VERSION:
            return
        self.cfg.set("last_seen_release_notes_version", APP_VERSION, persist=True)
        notes_file = Path(__file__).resolve().parents[2] / "RELEASE_NOTES.md"
        notes = ""
        try:
            notes = notes_file.read_text(encoding="utf-8").strip()
        except OSError:
            notes = t("main_window.whats_new_fallback")
        QMessageBox.information(
            self, t("main_window.whats_new_title", version=APP_VERSION),
            f"{t('main_window.whats_new_intro')}\n\n{notes[:4000]}",
        )

    def _on_assurance_status(self, headline: str, proof: str) -> None:
        self.tray.set_assurance(headline, proof)

    def _show_window(self) -> None:
        """Bring the main window back (same action as the tray menu item)."""
        self.showNormal()
        self.activateWindow()
        self.raise_()

    def _on_conflict_count(self, count: int) -> None:
        """Keep the Conflicts tab and tray badge honest without a extra scan."""
        index = self.tabs.indexOf(self.conflicts_tab)
        if index >= 0:
            label = self._tab_labels.get(self.conflicts_tab, t("main_window.tab_conflicts"))
            self.tabs.setTabText(index, f"{label} ({count})" if count else label)
        self.status_tab.on_conflict_count(count)
        if hasattr(self, "tray"):
            self.tray.set_conflict_count(count)

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

    def _update_metrics_tab_active(self) -> None:
        if not hasattr(self, "metrics_tab") or not hasattr(self, "tabs"):
            return
        tab_selected = self.tabs.currentIndex() == 1
        window_active = self.isVisible() and not self.isMinimized()
        self.metrics_tab.set_active(tab_selected and window_active)
        # The queue preview is only worth its periodic SSH round-trips while the
        # user is actually looking at it; idle clients fall back to the local
        # pending summary instead of scanning the NAS every minute.
        transfers_selected = self.tabs.currentIndex() == 2
        scan_worker = getattr(self, "scan_worker", None)
        if scan_worker is not None:
            scan_worker.set_ui_active(transfers_selected and window_active)

    def changeEvent(self, event: QEvent) -> None:
        super().changeEvent(event)
        if event.type() == QEvent.Type.WindowStateChange:
            self._update_metrics_tab_active()

    def hideEvent(self, event) -> None:
        super().hideEvent(event)
        self._update_metrics_tab_active()

    def showEvent(self, event) -> None:
        super().showEvent(event)
        self._update_metrics_tab_active()

    def closeEvent(self, event) -> None:
        self._settings.setValue("window/geometry", self.saveGeometry())
        # Minimize to tray instead of quitting, like most sync clients.
        if self.tray.isVisible():
            event.ignore()
            self.hide()
            self._update_metrics_tab_active()
            self.tray.showMessage(APP_NAME, t("main_window.tray_running_notice"))
        else:
            event.ignore()
            self.quit_application()

    _update_check_busy = False

    @staticmethod
    def _current_root() -> Path:
        return Path(__file__).resolve().parents[2]

    def _check_for_client_update_quietly(self) -> None:
        """Periodic check: notify in the tray, never block the user with a dialog."""
        if self._update_check_busy or not updater.auto_update_allowed(self.cfg, self._current_root()):
            return
        self._update_check_busy = True
        current_root = self._current_root()
        run_in_background(
            self, "_update_check",
            lambda: {"candidate": updater.find_update(self.cfg, current_root, str(current_root / "main.py"))},
            self._on_quiet_update_done,
        )

    def _on_quiet_update_done(self, result, exc: Exception | None) -> None:
        self._update_check_busy = False
        if exc is not None:
            return  # a periodic check must never report transient SSH failures
        candidate = result.get("candidate") if result else None
        if candidate is None:
            return
        if candidate.version == self._last_notified_version:
            return  # once per version, not every interval
        self._last_notified_version = candidate.version
        candidate.cleanup()
        self.tray.set_update_available(candidate.version)
        self.tray.showMessage(
            APP_NAME,
            t("main_window.update_notice", version=candidate.version),
            QSystemTrayIcon.MessageIcon.Information,
            15_000,
        )

    def _check_for_client_update(self) -> None:
        if self._update_check_busy:
            return
        self._update_check_busy = True
        current_root = self._current_root()
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
        current_root = self._current_root()
        if not updater.auto_update_allowed(self.cfg, current_root):
            candidate.cleanup()
            QMessageBox.information(
                self,
                t("main_window.update_check_title"),
                t("main_window.update_dev_checkout", path=str(current_root)),
            )
            return
        try:
            source_root = candidate.materialize()
            updater.install_update(source_root, current_root)
            candidate.cleanup()
            QMessageBox.information(self, t("main_window.update_restart_title"), t("main_window.update_restart_body"))
            updater.restart(current_root)
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
        try:
            self.metrics_tab.stop()
        except Exception as exc:
            self.logger.log("ERROR", "-", detail=f"errore durante chiusura monitor NAS: {exc}")
