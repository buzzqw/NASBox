"""Icons drawn at runtime with QPainter -- no external asset files to ship,
package, or keep in sync with the code. Covers the app/window icon and a
small set of tray-icon variants that actually reflect what the sync engine
is doing right now (synced, syncing, paused, error, not set up yet) instead
of one static icon that looks the same whether everything's fine or broken.
"""
from __future__ import annotations

from PyQt6.QtCore import QRectF, Qt
from PyQt6.QtGui import QColor, QFont, QIcon, QPainter, QPainterPath, QPen, QPixmap

ACCENT = QColor("#2F6FED")
WHITE = QColor("#FFFFFF")

STATUS_COLORS = {
    "synced": QColor("#2E9E5B"),
    "syncing": QColor("#2F6FED"),
    "paused": QColor("#8A8F98"),
    "error": QColor("#D64545"),
    "unconfigured": QColor("#8A8F98"),
}
STATUS_GLYPHS = {
    "synced": "✓",   # check mark
    "syncing": "⇆",  # ⇆ -- mid-transfer
    "paused": "⏸",   # ⏸
    "error": "!",
    "unconfigured": "?",
}

_ICON_SIZES = (16, 22, 24, 32, 48, 64)


def _draw_base(painter: QPainter, size: int, bg_color: QColor) -> None:
    painter.setRenderHint(QPainter.RenderHint.Antialiasing)
    margin = size * 0.06
    rect = QRectF(margin, margin, size - 2 * margin, size - 2 * margin)
    bg_path = QPainterPath()
    bg_path.addRoundedRect(rect, size * 0.22, size * 0.22)
    painter.fillPath(bg_path, bg_color)

    # Two concentric square outlines -- the "double insulated" symbol, echoing
    # nested boxes (which is the whole NASBox pitch: your box, mirrored into
    # another box). Stroked, not filled, so it stays crisp and reads clearly
    # even at a 16px tray size, unlike a small glyph with fine detail.
    pen = QPen(WHITE)
    pen.setWidthF(max(1.2, size * 0.045))
    pen.setJoinStyle(Qt.PenJoinStyle.MiterJoin)
    painter.setPen(pen)
    painter.setBrush(Qt.BrushStyle.NoBrush)

    center = rect.center()
    outer_size = rect.width() * 0.52
    outer_rect = QRectF(0, 0, outer_size, outer_size)
    outer_rect.moveCenter(center)
    painter.drawRoundedRect(outer_rect, outer_size * 0.18, outer_size * 0.18)

    inner_size = rect.width() * 0.30
    inner_rect = QRectF(0, 0, inner_size, inner_size)
    inner_rect.moveCenter(center)
    painter.drawRoundedRect(inner_rect, inner_size * 0.18, inner_size * 0.18)


def render_base_pixmap(size: int, bg_color: QColor = ACCENT) -> QPixmap:
    pixmap = QPixmap(size, size)
    pixmap.fill(Qt.GlobalColor.transparent)
    painter = QPainter(pixmap)
    _draw_base(painter, size, bg_color)
    painter.end()
    return pixmap


def render_status_pixmap(size: int, state: str) -> QPixmap:
    pixmap = render_base_pixmap(size)
    painter = QPainter(pixmap)
    painter.setRenderHint(QPainter.RenderHint.Antialiasing)

    badge_d = size * 0.52
    badge_rect = QRectF(size - badge_d * 0.95, size - badge_d * 0.95, badge_d, badge_d)
    painter.setPen(Qt.PenStyle.NoPen)
    painter.setBrush(STATUS_COLORS.get(state, STATUS_COLORS["unconfigured"]))
    painter.drawEllipse(badge_rect)

    font = QFont()
    font.setPixelSize(max(1, int(badge_d * 0.62)))
    font.setBold(True)
    painter.setFont(font)
    painter.setPen(WHITE)
    painter.drawText(badge_rect, Qt.AlignmentFlag.AlignCenter, STATUS_GLYPHS.get(state, "?"))
    painter.end()
    return pixmap


def app_icon() -> QIcon:
    """Plain NASBox icon, no status badge -- used for the window/desktop icon."""
    icon = QIcon()
    for size in _ICON_SIZES:
        icon.addPixmap(render_base_pixmap(size))
    return icon


def tray_icon(state: str) -> QIcon:
    """state: one of STATUS_COLORS's keys -- unrecognized values fall back to
    the same look as "unconfigured" rather than raising, since this is called
    from status-signal handlers where a typo shouldn't crash the tray."""
    icon = QIcon()
    for size in _ICON_SIZES:
        icon.addPixmap(render_status_pixmap(size, state))
    return icon
