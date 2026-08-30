from __future__ import annotations

import subprocess
import time
from collections import deque

from PyQt6.QtCore import QPointF, QTimer, Qt
from PyQt6.QtGui import QColor, QPainter, QPen, QPolygonF
from PyQt6.QtWidgets import (
    QFormLayout, QFrame, QGridLayout, QGroupBox, QHBoxLayout, QLabel,
    QPushButton, QScrollArea, QVBoxLayout, QWidget,
)

from ..config import Config
from ..engine import SyncEngine
from ..i18n import t
from ..metrics_worker import MetricsCollector, MetricsFetchResult, NasMetrics
from .async_utils import run_in_background
from .format_utils import human_size


class MetricHistoryPlot(QFrame):
    """Dependency-free rolling plot for one metric."""

    def __init__(self, title: str, color: str, maximum: float | None = None, parent=None) -> None:
        super().__init__(parent)
        self.title = title
        self.color = color
        self.maximum = maximum
        self._values: deque[float] = deque(maxlen=360)
        self.setMinimumHeight(120)
        self.setFrameShape(QFrame.Shape.StyledPanel)
        self.setToolTip(title)

    def clear(self) -> None:
        self._values.clear()
        self.update()

    def add_value(self, value: float | None) -> None:
        if value is not None:
            self._values.append(max(0.0, float(value)))
        self.update()

    def paintEvent(self, _event) -> None:
        painter = QPainter(self)
        painter.setRenderHint(QPainter.RenderHint.Antialiasing)
        painter.fillRect(self.rect(), self.palette().base())
        margin = 12
        plot = self.rect().adjusted(margin, margin, -margin, -margin)
        painter.setPen(QPen(self.palette().mid(), 1, Qt.PenStyle.DotLine))
        for index in range(1, 4):
            y = plot.top() + plot.height() * index / 4
            painter.drawLine(plot.left(), int(y), plot.right(), int(y))
        if len(self._values) < 2:
            painter.setPen(self.palette().text().color())
            painter.drawText(plot, Qt.AlignmentFlag.AlignCenter, "--")
            return
        maximum = self.maximum or max(1.0, max(self._values))
        points = []
        for index, value in enumerate(self._values):
            x = plot.left() + plot.width() * index / max(1, len(self._values) - 1)
            y = plot.bottom() - plot.height() * min(1.0, value / maximum)
            points.append(QPointF(x, y))
        painter.setPen(QPen(QColor(self.color), 2))
        painter.drawPolyline(QPolygonF(points))


class MetricsTab(QWidget):
    """Small, read-only NAS health dashboard."""

    def __init__(self, cfg: Config, engine: SyncEngine, parent=None) -> None:
        super().__init__(parent)
        self.cfg = cfg
        self.engine = engine
        self._collector = MetricsCollector()
        self._busy = False
        self._connected = False
        self._supported = True
        self._connection = None
        self._connection_initialized = False
        self._requested_connection = None
        self._active = False
        self._stopping = False
        self._metrics_proc = None

        outer = QVBoxLayout(self)
        outer.setContentsMargins(0, 0, 0, 0)
        scroll = QScrollArea()
        scroll.setWidgetResizable(True)
        scroll.setFrameShape(QScrollArea.Shape.NoFrame)
        outer.addWidget(scroll)

        content = QWidget()
        scroll.setWidget(content)
        root = QVBoxLayout(content)
        root.setContentsMargins(4, 8, 4, 4)
        root.setSpacing(12)

        toolbar = QHBoxLayout()
        self.status_label = QLabel(t("metrics.status_unknown"))
        self.status_label.setWordWrap(True)
        toolbar.addWidget(self.status_label, 1)
        self.refresh_btn = QPushButton(t("metrics.refresh_btn"))
        self.refresh_btn.clicked.connect(self._refresh_metrics)
        toolbar.addWidget(self.refresh_btn)
        root.addLayout(toolbar)

        self.sync_state_box = QFrame()
        self.sync_state_box.setObjectName("syncStateBox")
        state_layout = QVBoxLayout(self.sync_state_box)
        state_layout.setContentsMargins(14, 10, 14, 10)
        state_layout.setSpacing(2)
        self.sync_state_title = QLabel()
        self.sync_state_title.setObjectName("syncStateTitle")
        self.sync_state_detail = QLabel()
        self.sync_state_detail.setObjectName("syncStateDetail")
        self.sync_state_detail.setWordWrap(True)
        state_layout.addWidget(self.sync_state_title)
        state_layout.addWidget(self.sync_state_detail)
        root.addWidget(self.sync_state_box)
        self._set_sync_state("unknown", t("metrics.sync_unknown_title"), t("metrics.sync_unknown_detail"))

        cards = QGridLayout()
        cards.setHorizontalSpacing(12)
        cards.setVerticalSpacing(12)
        self.load_label = self._card(cards, 0, 0, t("metrics.load_title"), t("metrics.load_label"))
        self.cpu_label = self._card(cards, 0, 1, t("metrics.cpu_title"), t("metrics.cpu_label"))
        self.memory_label = self._card(cards, 1, 0, t("metrics.memory_title"), t("metrics.memory_label"))
        self.swap_label = self._card(cards, 1, 1, t("metrics.swap_title"), t("metrics.swap_label"))
        self.disk_label = self._card(cards, 2, 0, t("metrics.disk_title"), t("metrics.disk_label"))

        io = QGroupBox(t("metrics.io_title"))
        io_form = QFormLayout(io)
        self.io_read_label = QLabel("--")
        self.io_write_label = QLabel("--")
        io_form.addRow(t("metrics.io_read_label"), self.io_read_label)
        io_form.addRow(t("metrics.io_write_label"), self.io_write_label)
        cards.addWidget(io, 2, 1)

        network = QGroupBox(t("metrics.network_title"))
        network_form = QFormLayout(network)
        self.net_rx_label = QLabel("--")
        self.net_tx_label = QLabel("--")
        self.net_interfaces_label = QLabel("--")
        network_form.addRow(t("metrics.receive_label"), self.net_rx_label)
        network_form.addRow(t("metrics.transmit_label"), self.net_tx_label)
        network_form.addRow(t("metrics.interfaces_label"), self.net_interfaces_label)
        cards.addWidget(network, 3, 0, 1, 2)

        uptime = QGroupBox(t("metrics.uptime_title"))
        uptime_form = QFormLayout(uptime)
        self.uptime_label = QLabel("--")
        uptime_form.addRow(t("metrics.uptime_label"), self.uptime_label)
        cards.addWidget(uptime, 4, 0, 1, 2)
        root.addLayout(cards)

        activity = QGroupBox(t("metrics.nasbox_title"))
        activity_form = QFormLayout(activity)
        self.activity_label = QLabel("--")
        self.queue_label = QLabel("--")
        self.staging_label = QLabel("--")
        self.metadata_label = QLabel("--")
        activity_form.addRow(t("metrics.activity_label"), self.activity_label)
        activity_form.addRow(t("metrics.queue_label"), self.queue_label)
        activity_form.addRow(t("metrics.staging_label"), self.staging_label)
        activity_form.addRow(t("metrics.metadata_label"), self.metadata_label)
        root.addWidget(activity)

        charts = QHBoxLayout()
        load_chart_box = QGroupBox(t("metrics.load_chart_title"))
        load_chart_layout = QVBoxLayout(load_chart_box)
        self.load_plot = MetricHistoryPlot(t("metrics.load_chart_title"), "#3b82f6")
        load_chart_layout.addWidget(self.load_plot)
        charts.addWidget(load_chart_box)
        cpu_chart_box = QGroupBox(t("metrics.cpu_chart_title"))
        cpu_chart_layout = QVBoxLayout(cpu_chart_box)
        self.cpu_plot = MetricHistoryPlot(t("metrics.cpu_chart_title"), "#f97316", maximum=100)
        cpu_chart_layout.addWidget(self.cpu_plot)
        charts.addWidget(cpu_chart_box)
        root.addLayout(charts)
        root.addStretch(1)

        self._timer = QTimer(self)
        self._timer.setSingleShot(True)
        self._timer.timeout.connect(self._refresh_metrics)

    @staticmethod
    def _card(grid: QGridLayout, row: int, column: int, title: str, label: str) -> QLabel:
        box = QGroupBox(title)
        form = QFormLayout(box)
        value = QLabel("--")
        value.setWordWrap(True)
        form.addRow(label, value)
        grid.addWidget(box, row, column)
        return value

    def on_connection_changed(self, connection) -> None:
        if self._connection_initialized and connection == self._connection:
            return
        self._connection_initialized = True
        self._connection = connection
        self._connected = connection is not None
        self._collector.reset()
        self.load_plot.clear()
        self.cpu_plot.clear()
        self._supported = True
        self._timer.stop()
        if not self._connected:
            self.status_label.setText(t("metrics.status_offline"))
            self._set_sync_state("error", t("metrics.sync_offline_title"), t("metrics.sync_offline_detail"))
            self.refresh_btn.setEnabled(True)
            return
        if not self._active:
            self.status_label.setText(t("metrics.status_unknown"))
            self._set_sync_state("unknown", t("metrics.sync_unknown_title"), t("metrics.sync_unknown_detail"))
            return
        self.status_label.setText(t("metrics.status_loading"))
        self._set_sync_state("loading", t("metrics.sync_loading_title"), t("metrics.sync_loading_detail"))
        self._timer.start(0)

    def set_active(self, active: bool) -> None:
        """Only probe the NAS while the diagnostic tab is visible."""
        self._active = active
        if not active:
            self._timer.stop()
            return
        if self._connected and not self._busy:
            self._timer.start(0)

    def stop(self) -> None:
        """Stop timers and interrupt an in-flight diagnostic SSH process."""
        self._stopping = True
        self._active = False
        self._timer.stop()
        process = self._metrics_proc
        if process is not None and process.poll() is None:
            try:
                process.terminate()
                process.wait(timeout=2)
            except (OSError, subprocess.TimeoutExpired):
                try:
                    process.kill()
                except OSError:
                    pass
        call = getattr(self, "_metrics_call", None)
        if call is not None and call.isRunning():
            call.wait(3000)

    def _refresh_interval_ms(self) -> int:
        try:
            seconds = int(self.cfg.get("metrics_refresh_seconds") or 15)
        except (TypeError, ValueError):
            seconds = 15
        return max(5, min(seconds, 3600)) * 1000

    def _refresh_metrics(self) -> None:
        if self._busy or self._stopping or not self._active:
            return
        connection = self.engine.connection
        if connection is None:
            self._connected = False
            self.status_label.setText(t("metrics.status_offline"))
            self._set_sync_state("error", t("metrics.sync_offline_title"), t("metrics.sync_offline_detail"))
            return
        self._connected = True
        self._busy = True
        self._requested_connection = connection
        self._metrics_proc = None
        self.refresh_btn.setEnabled(False)
        self.status_label.setText(t("metrics.status_loading"))
        self._set_sync_state("loading", t("metrics.sync_loading_title"), t("metrics.sync_loading_detail"))
        run_in_background(
            self, "_metrics_call",
            lambda: self._collector.collect(self.cfg, connection, on_start=self._set_metrics_process),
            self._on_metrics_done,
        )

    def _set_metrics_process(self, process) -> None:
        self._metrics_proc = process
        if self._stopping and process.poll() is None:
            try:
                process.terminate()
            except OSError:
                pass

    def _on_metrics_done(self, result: MetricsFetchResult | None, exc: Exception | None) -> None:
        self._busy = False
        self._metrics_proc = None
        self.refresh_btn.setEnabled(True)
        if self._stopping:
            return
        if self._requested_connection != self.engine.connection:
            if self._connected:
                self._timer.start(0)
            return
        if exc is not None:
            self.status_label.setText(t("metrics.status_failed", detail=str(exc)))
            self._set_sync_state("error", t("metrics.sync_error_title"), str(exc))
            self._schedule_next()
            return
        if result is None:
            self.status_label.setText(t("metrics.status_failed", detail=t("metrics.invalid_response")))
            self._set_sync_state("error", t("metrics.sync_error_title"), t("metrics.invalid_response"))
            self._schedule_next()
            return
        if not result.supported:
            self._supported = False
            self.status_label.setText(t("metrics.status_unsupported"))
            self._set_sync_state("error", t("metrics.sync_error_title"), t("metrics.status_unsupported"))
            self._timer.stop()
            return
        if result.metrics is None:
            self.status_label.setText(t("metrics.status_failed", detail=result.detail or t("metrics.invalid_response")))
            self._set_sync_state("error", t("metrics.sync_error_title"), result.detail or t("metrics.invalid_response"))
            self._schedule_next()
            return
        self._supported = True
        self._display(result.metrics)
        self.status_label.setText(
            f"{t('metrics.status_updated', time=time.strftime('%Y-%m-%d %H:%M:%S'))} "
            f"{self._health_text(result.metrics)}"
        )
        self._schedule_next()

    def _schedule_next(self) -> None:
        if self._connected and self._supported:
            self._timer.start(self._refresh_interval_ms())

    def _set_sync_state(self, state: str, title: str, detail: str) -> None:
        colors = {
            "idle": ("#f0fdf4", "#bbf7d0", "#166534", "#15803d"),
            "busy": ("#eff6ff", "#bfdbfe", "#1e40af", "#1d4ed8"),
            "queue": ("#fffbeb", "#fde68a", "#92400e", "#b45309"),
            "loading": ("#eff6ff", "#bfdbfe", "#1e40af", "#1d4ed8"),
            "error": ("#fef2f2", "#fecaca", "#991b1b", "#b91c1c"),
            "unknown": ("#f3f4f6", "#d1d5db", "#374151", "#4b5563"),
        }
        background, border, detail_color, title_color = colors.get(state, colors["unknown"])
        self.sync_state_box.setStyleSheet(
            "QFrame#syncStateBox { "
            f"background-color: {background}; border: 1px solid {border}; border-radius: 8px; "
            "} "
            f"QLabel#syncStateTitle {{ color: {title_color}; font-size: 18px; font-weight: 600; }} "
            f"QLabel#syncStateDetail {{ color: {detail_color}; font-size: 12px; }}"
        )
        self.sync_state_title.setText(title)
        self.sync_state_detail.setText(detail)

    def _display_sync_state(self, metrics: NasMetrics) -> None:
        if metrics.nasbox_lock_held:
            phase_keys = {
                "checking": "metrics.phase_checking",
                "transferring": "metrics.phase_transferring",
                "confirming": "metrics.phase_confirming",
            }
            phase = t(phase_keys.get(metrics.nasbox_lock_phase, "metrics.phase_unknown"))
            progress = ""
            if metrics.nasbox_lock_progress_total:
                progress = t(
                    "metrics.sync_progress",
                    done=metrics.nasbox_lock_progress_done,
                    total=metrics.nasbox_lock_progress_total,
                )
            detail = t("metrics.sync_busy_detail", phase=phase, progress=progress)
            self._set_sync_state("busy", t("metrics.sync_busy_title"), detail)
            return
        if metrics.nasbox_queue_count:
            self._set_sync_state(
                "queue",
                t("metrics.sync_queue_title"),
                t("metrics.sync_queue_detail", count=metrics.nasbox_queue_count),
            )
            return
        if metrics.nasbox_staging_count:
            self._set_sync_state(
                "queue",
                t("metrics.sync_staging_title"),
                t("metrics.sync_staging_detail", count=metrics.nasbox_staging_count),
            )
            return
        self._set_sync_state("idle", t("metrics.sync_idle_title"), t("metrics.sync_idle_detail"))

    def _display(self, metrics: NasMetrics) -> None:
        self._display_sync_state(metrics)
        self.load_label.setText(
            f"{metrics.load_1:.2f} / {metrics.load_5:.2f} / {metrics.load_15:.2f}"
        )
        self.load_plot.add_value(metrics.load_1)
        if metrics.cpu_usage_percent is None:
            self.cpu_label.setText(t("metrics.first_sample"))
        else:
            self.cpu_label.setText(t(
                "metrics.cpu_value", usage=f"{metrics.cpu_usage_percent:.0f}",
                iowait=f"{metrics.cpu_iowait_percent or 0:.0f}",
            ))
        self.cpu_plot.add_value(metrics.cpu_usage_percent)

        if metrics.memory_total_bytes > 0:
            memory_used = max(0, metrics.memory_total_bytes - metrics.memory_available_bytes)
            memory_percent = min(100, memory_used * 100 / metrics.memory_total_bytes)
            self.memory_label.setText(t(
                "metrics.memory_value", used=human_size(memory_used),
                total=human_size(metrics.memory_total_bytes), percent=f"{memory_percent:.0f}",
            ))
        else:
            self.memory_label.setText(t("metrics.not_available"))

        if metrics.swap_total_bytes > 0:
            swap_used = max(0, metrics.swap_total_bytes - metrics.swap_free_bytes)
            self.swap_label.setText(t(
                "metrics.swap_value", used=human_size(swap_used),
                total=human_size(metrics.swap_total_bytes),
            ))
        else:
            self.swap_label.setText(t("metrics.swap_disabled"))

        self.disk_label.setText(t(
            "metrics.disk_value", available=human_size(metrics.disk_available_bytes),
            total=human_size(metrics.disk_total_bytes), percent=f"{metrics.disk_usage_percent:.0f}",
        ))
        self.io_read_label.setText(self._rate_value(metrics.disk_read_rate, metrics.disk_read_bytes))
        self.io_write_label.setText(self._rate_value(metrics.disk_write_rate, metrics.disk_write_bytes))
        self.net_rx_label.setText(self._rate_value(metrics.net_rx_rate, metrics.net_rx_bytes))
        self.net_tx_label.setText(self._rate_value(metrics.net_tx_rate, metrics.net_tx_bytes))
        self.net_interfaces_label.setText(str(metrics.net_interface_count))
        self.uptime_label.setText(self._uptime_value(metrics.uptime_seconds))
        if metrics.nasbox_lock_held:
            owner = metrics.nasbox_lock_owner or t("metrics.unknown_value")
            phase = metrics.nasbox_lock_phase or t("metrics.unknown_value")
            progress = ""
            if metrics.nasbox_lock_progress_total:
                progress = f" {metrics.nasbox_lock_progress_done}/{metrics.nasbox_lock_progress_total}"
            self.activity_label.setText(t(
                "metrics.activity_busy", owner=owner, phase=phase, progress=progress,
            ))
        else:
            self.activity_label.setText(t("metrics.activity_idle"))
        self.queue_label.setText(str(metrics.nasbox_queue_count))
        self.staging_label.setText(str(metrics.nasbox_staging_count))
        self.metadata_label.setText(t(
            "metrics.metadata_value", journal=human_size(metrics.journal_bytes),
            manifest=human_size(metrics.manifest_bytes),
        ))

    @staticmethod
    def _health_text(metrics: NasMetrics) -> str:
        warnings = []
        if metrics.cpu_usage_percent is not None and metrics.cpu_usage_percent >= 85:
            warnings.append("CPU")
        if metrics.cpu_iowait_percent is not None and metrics.cpu_iowait_percent >= 20:
            warnings.append("I/O")
        if metrics.memory_total_bytes and metrics.memory_available_bytes * 100 / metrics.memory_total_bytes < 10:
            warnings.append("RAM")
        if metrics.disk_usage_percent >= 90:
            warnings.append("disco")
        if warnings:
            return t("metrics.health_warning", areas=", ".join(warnings))
        return t("metrics.health_ok")

    @staticmethod
    def _rate_value(rate: float | None, total: int) -> str:
        if rate is None:
            return human_size(total)
        return f"{human_size(rate)}/s ({human_size(total)} total)"

    @staticmethod
    def _uptime_value(seconds: int) -> str:
        days, remainder = divmod(max(0, seconds), 86400)
        hours, remainder = divmod(remainder, 3600)
        minutes, secs = divmod(remainder, 60)
        return f"{days}d {hours:02d}h {minutes:02d}m {secs:02d}s"
