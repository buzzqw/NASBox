"""Compact, single-screen answer to "is everything synced?".

This tab is deliberately read-only and has no scroll area: the banner, the four
numeric tiles, the two summary rows and the "open details" button always sit in
the same place. All data comes from the status dict already emitted by the sync
engine -- there is no SSH call, no polling timer and no second source of truth.

The four i18n keys used for the banner are ``overview.banner_ok``,
``overview.banner_warning``, ``overview.banner_progress`` and
``overview.banner_offline`` (see the report accompanying this module for the
full it/en key list; i18n.py is intentionally left untouched here).
"""
from __future__ import annotations

import time

from PyQt6.QtCore import Qt, pyqtSignal
from PyQt6.QtGui import QFont
from PyQt6.QtWidgets import (
    QFrame, QHBoxLayout, QLabel, QPushButton, QSizePolicy, QVBoxLayout, QWidget,
)

from ..i18n import t


def _as_int(value) -> int:
    """Status fields arrive from JSON/config: never trust their concrete type."""
    try:
        return int(value or 0)
    except (TypeError, ValueError, OverflowError):
        return 0


def _age(timestamp) -> str:
    """Human, coarse age of a timestamp, using only overview.* translations."""
    ts = _as_int(timestamp)
    if ts <= 0:
        return t("overview.never")
    seconds = max(0, int(time.time()) - ts)
    if seconds < 60:
        return t("overview.age_seconds", count=seconds)
    if seconds < 3600:
        return t("overview.age_minutes", count=seconds // 60)
    if seconds < 86400:
        return t("overview.age_hours", count=seconds // 3600)
    return t("overview.age_days", count=seconds // 86400)


class OverviewTab(QWidget):
    """Fixed one-screen summary; the caller feeds it via :meth:`set_status`."""

    open_details_requested = pyqtSignal()

    def __init__(self, parent=None) -> None:
        super().__init__(parent)
        self.setObjectName("overviewTab")
        self._conflict_count = 0

        root = QVBoxLayout(self)
        root.setContentsMargins(8, 10, 8, 8)
        root.setSpacing(6)

        # --- headline banner: one line, one fixed height, always visible ---
        self.banner = QLabel(t("overview.banner_offline"))
        self.banner.setObjectName("overviewBanner")
        self.banner.setAlignment(Qt.AlignmentFlag.AlignCenter)
        self.banner.setAccessibleName(self.banner.text())
        self.banner.setMinimumHeight(52)
        banner_font = QFont(self.banner.font())
        banner_font.setPointSize(banner_font.pointSize() + 8)
        banner_font.setBold(True)
        self.banner.setFont(banner_font)
        root.addWidget(self.banner)
        self.reason_label = QLabel("")
        self.reason_label.setObjectName("overviewReason")
        self.reason_label.setWordWrap(True)
        self.reason_label.setAlignment(Qt.AlignmentFlag.AlignCenter)
        root.addWidget(self.reason_label)

        # --- four numeric tiles in a single fixed-height row ---
        tiles = QHBoxLayout()
        tiles.setContentsMargins(0, 0, 0, 0)
        tiles.setSpacing(6)
        self.stat_labels: dict[str, QLabel] = {}
        for key, caption_key in (
            ("tracked", "overview.stat_tracked"),
            ("queue", "overview.stat_queue"),
            ("conflicts", "overview.stat_conflicts"),
            ("audit", "overview.stat_audit"),
        ):
            tile = QFrame()
            tile.setObjectName(f"overviewTile_{key}")
            tile.setFrameShape(QFrame.Shape.StyledPanel)
            tile.setSizePolicy(QSizePolicy.Policy.Expanding, QSizePolicy.Policy.Fixed)
            tile_layout = QVBoxLayout(tile)
            tile_layout.setContentsMargins(6, 4, 6, 4)
            tile_layout.setSpacing(2)
            caption = QLabel(t(caption_key))
            caption.setAlignment(Qt.AlignmentFlag.AlignCenter)
            caption.setAccessibleName(caption.text())
            value = QLabel("0")
            value.setObjectName(f"overviewStat_{key}")
            value.setAlignment(Qt.AlignmentFlag.AlignCenter)
            value.setAccessibleName(caption.text())
            value_font = QFont(value.font())
            value_font.setPointSize(value_font.pointSize() + 4)
            value_font.setBold(True)
            value.setFont(value_font)
            value.setMinimumHeight(26)
            tile_layout.addWidget(caption)
            tile_layout.addWidget(value)
            tiles.addWidget(tile, 1)
            self.stat_labels[key] = value
        root.addLayout(tiles)

        # --- two fixed summary rows ---
        self.last_check_label = QLabel(
            t("overview.last_check", value=t("overview.never"))
        )
        self.last_check_label.setObjectName("overviewLastCheck")
        self.last_check_label.setAccessibleName(self.last_check_label.text())
        root.addWidget(self.last_check_label)

        self.devices_label = QLabel(t("overview.devices", value=t("overview.no_devices")))
        self.devices_label.setObjectName("overviewDevices")
        self.devices_label.setAccessibleName(self.devices_label.text())
        root.addWidget(self.devices_label)

        # --- single action; the parent decides which tab "details" means ---
        button_row = QHBoxLayout()
        button_row.setContentsMargins(0, 0, 0, 0)
        button_row.addStretch(1)
        self.details_button = QPushButton(t("overview.open_details"))
        self.details_button.setObjectName("overviewDetailsButton")
        self.details_button.clicked.connect(self.open_details_requested.emit)
        button_row.addWidget(self.details_button)
        button_row.addStretch(1)
        root.addLayout(button_row)

        # Content stays at the top even if the host gives this tab extra room.
        root.addStretch(1)
        self.set_status({})

    # --- public API ---------------------------------------------------

    def set_status(self, status: dict) -> None:
        """Refresh the whole summary from one engine status dict (no I/O)."""
        data = status if isinstance(status, dict) else {}
        assurance = data.get("assurance") or {}
        audit = data.get("audit") or {}
        if not isinstance(assurance, dict):
            assurance = {}
        if not isinstance(audit, dict):
            audit = {}

        connected = bool(data.get("connected", False))
        paused = bool(data.get("paused", False))
        pending = _as_int(assurance.get("pending_count"))
        queued = _as_int(assurance.get("queue_count"))
        mismatches = _as_int(assurance.get("last_integrity_mismatches"))
        last_check = _as_int(assurance.get("last_integrity_check_ts"))
        active = str(assurance.get("active_kind") or "")

        # The engine emits the conflict count on its own signal. Accept it
        # through the status dict too, so a single connection is sufficient.
        if "conflict_count" in data:
            self.on_conflict_count(data["conflict_count"])
        elif "conflicts" in data:
            self.on_conflict_count(data["conflicts"])
        conflicts = self._conflict_count

        last_audit = audit.get("last") or {}
        if not isinstance(last_audit, dict):
            last_audit = {}
        audit_running = bool(audit.get("running"))
        audit_differences = _as_int(last_audit.get("differences"))
        audit_status = str(last_audit.get("status") or "")
        audit_finished = _as_int(last_audit.get("finished_at"))

        clean = (
            connected
            and not paused
            and not pending
            and not queued
            and not conflicts
            and not mismatches
            and not active
            and not audit_running
            and assurance.get("server_health") == "ok"
            and bool(assurance.get("repository_ready"))
            and bool(assurance.get("journal_ready"))
            and audit_status in ("", "ok")
            and audit_differences == 0
        )
        if not connected:
            state, banner_key = "offline", "overview.banner_offline"
        elif (active or audit_running) and not clean:
            state, banner_key = "progress", "overview.banner_progress"
        elif clean:
            state, banner_key = "ok", "overview.banner_ok"
        else:
            state, banner_key = "warning", "overview.banner_warning"

        banner_text = t(banner_key)
        self.banner.setText(banner_text)
        self.banner.setAccessibleName(banner_text)
        self.banner.setProperty("state", state)
        style = self.banner.style()
        style.unpolish(self.banner)
        style.polish(self.banner)

        reasons: list[str] = []
        if paused:
            reasons.append(t("overview.reason_paused"))
        if pending:
            reasons.append(t("overview.reason_pending", count=pending))
        if queued:
            reasons.append(t("overview.reason_queued", count=queued))
        if conflicts:
            reasons.append(t("overview.reason_conflicts", count=conflicts))
        if mismatches:
            reasons.append(t("overview.reason_checksum", count=mismatches))
        if audit_differences:
            reasons.append(t("overview.reason_audit", count=audit_differences))
        if assurance.get("server_health") not in ("ok", None):
            reasons.append(str(assurance.get("server_health")))
        if not assurance.get("journal_ready"):
            reasons.append(t("overview.reason_journal"))
        self.reason_label.setText(
            t("overview.reason", detail=" · ".join(reasons)) if reasons else ""
        )

        if audit_running:
            audit_text = t("overview.audit_running")
        elif not last_audit:
            audit_text = t("overview.never")
        else:
            audit_text = t(
                "overview.audit_summary",
                count=audit_differences,
                when=_age(audit_finished),
            )

        self.stat_labels["tracked"].setText(str(_as_int(assurance.get("tracked_files"))))
        self.stat_labels["queue"].setText(str(queued))
        self.stat_labels["conflicts"].setText(str(conflicts))
        self.stat_labels["audit"].setText(audit_text)

        if last_check:
            check_value = "{} · {}".format(
                t("overview.check_mismatch", count=mismatches), _age(last_check),
            )
        else:
            check_value = t("overview.never")
        self._set_row(self.last_check_label, "overview.last_check", check_value)

        devices = data.get("devices")
        device_count = len(devices) if isinstance(devices, (list, tuple)) else 0
        device_value = str(device_count) if device_count else t("overview.no_devices")
        self._set_row(self.devices_label, "overview.devices", device_value)

    def on_conflict_count(self, count: int) -> None:
        """Keep the conflict tile in sync with the existing conflict signal."""
        self._conflict_count = max(0, _as_int(count))
        self.stat_labels["conflicts"].setText(str(self._conflict_count))

    # --- internals -----------------------------------------------------

    @staticmethod
    def _set_row(label: QLabel, key: str, value: str) -> None:
        text = t(key, value=value)
        label.setText(text)
        label.setAccessibleName(text)
