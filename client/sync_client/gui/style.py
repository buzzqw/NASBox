"""High-contrast application theme for the PyQt desktop client.

The native palette on a few Linux themes resolves Qt's palette(mid) to nearly
white. This stylesheet uses explicit accessible colours so inactive tabs and
secondary actions remain visible instead of blending into the background.

Three themes are supported: ``light`` (the historical default), ``dark`` and
``system`` (no stylesheet, follow the desktop palette). Status indicators also
carry an icon and a distinct border style, so state never depends on colour
alone. See ``set_status_text()`` and ``apply_style()`` below.
"""
from __future__ import annotations

from PyQt6.QtGui import QColor, QPalette

THEME_LIGHT = "light"
THEME_DARK = "dark"
THEME_SYSTEM = "system"
THEMES = (THEME_SYSTEM, THEME_LIGHT, THEME_DARK)
DEFAULT_THEME = THEME_LIGHT

# Global keyboard shortcuts shared by the window, the status card and the tray.
SHORTCUT_SYNC_NOW = "Ctrl+Shift+S"
SHORTCUT_SHOW_WINDOW = "Ctrl+Shift+O"

STYLESHEET = """
QMainWindow, QWidget {
    background-color: #f6f8fb;
    color: #172033;
    font-size: 12px;
}

QFrame#appHeader {
    background-color: #ffffff;
    border: 1px solid #dfe5ee;
    border-radius: 12px;
}
QLabel#appTitle {
    color: #101828;
    font-size: 22px;
    font-weight: 700;
}
QLabel#appSubtitle {
    color: #667085;
    font-size: 12px;
}
QLabel#versionBadge, QToolButton#versionBadge, QToolButton#statusVersion {
    background-color: #eef2f7;
    color: #344054;
    border: 1px solid #d9e0ea;
    border-radius: 10px;
    padding: 3px 8px;
    font-weight: 700;
}

QGroupBox {
    background-color: #ffffff;
    border: 1px solid #dfe5ee;
    border-radius: 8px;
    margin-top: 8px;
    padding: 6px 8px 8px 8px;
    color: #172033;
    font-weight: 700;
}
QGroupBox::title {
    subcontrol-origin: margin;
    subcontrol-position: top left;
    left: 8px;
    padding: 0 4px;
}
QGroupBox#statusCard { border-color: #8ab4ff; }
QGroupBox#assuranceCard {
    border: 2px solid #2f7d55;
    border-radius: 6px;
    background-color: #f1faf5;
    margin-top: 4px;
}
QLabel#assuranceHeadline {
    font-size: 16px;
    font-weight: 700;
    color: #1d6b45;
    padding: 4px 2px 2px 2px;
}
QLabel#assuranceHeadline[state="ok"] { color: #1d6b45; }
QLabel#assuranceHeadline[state="active"] { color: #1d6b45; }
QLabel#assuranceHeadline[state="warning"] {
    color: #9a6200;
    border-bottom: 2px dashed #9a6200;
}
QLabel#assuranceHeadline[state="error"] {
    color: #c12c3d;
    border-bottom: 2px solid #c12c3d;
}
QLabel#assuranceProof {
    font-size: 12px;
    color: #35524a;
    padding: 2px 2px 6px 2px;
}
QLabel#assuranceChecks {
    font-size: 12px;
    color: #2f4c44;
    padding: 2px 2px 6px 2px;
}
QLabel#statusMessage {
    font-size: 15px;
    font-weight: 700;
    padding: 2px 2px 4px 2px;
}
/* Shape as well as colour: the border style differs per state. */
QLabel#statusMessage[state="active"] {
    color: #087443;
    border-left: 4px solid #087443;
    padding-left: 8px;
}
QLabel#statusMessage[state="paused"] {
    color: #9a6200;
    border-left: 4px dashed #9a6200;
    padding-left: 8px;
}
QLabel#statusMessage[state="warning"] {
    color: #9a6200;
    border-left: 4px dashed #9a6200;
    padding-left: 8px;
}
QLabel#statusMessage[state="error"] {
    color: #c12c3d;
    border-left: 4px double #c12c3d;
    padding-left: 8px;
}
QLabel#statusConflictStat, QToolButton#statusConflictStat {
    background-color: #fff4e5;
    color: #9a6200;
    border: 1px solid #f3c98b;
    border-radius: 7px;
    padding: 8px 10px;
    font-weight: 700;
    text-align: left;
}
QLabel#statusConflictStat:hover, QToolButton#statusConflictStat:hover {
    background-color: #ffe9c7;
}
QToolButton#statusUpdateAvailable {
    background-color: transparent;
    color: #175cd3;
    border: none;
    text-align: left;
    font-weight: 600;
}
QLabel#conflictCount { color: #344054; font-weight: 700; }
QLabel#conflictDetailPath { font-size: 15px; font-weight: 700; color: #101828; }
QLabel#conflictDetailMeta { color: #667085; }
QLabel#conflictStatus { color: #475467; }
QLabel#mirrorAuthorityNote {
    background-color: #eef5ff;
    color: #175cd3;
    border: 1px solid #bfd6ff;
    border-radius: 7px;
    padding: 8px 10px;
}
QPlainTextEdit#conflictPreview {
    background-color: #fbfcfe;
    border: 1px solid #e4e7ec;
    border-radius: 6px;
    font-family: "DejaVu Sans Mono", "Liberation Mono", monospace;
    font-size: 12px;
}
QLabel#statusQueue {
    color: #667085;
    background-color: #f8fafc;
    border-radius: 6px;
    padding: 7px 9px;
}
QLabel#statusWatcher {
    background-color: #f8fafc;
    color: #667085;
    border-radius: 6px;
    padding: 7px 9px;
}
QLabel#statusWatcher[state="active"] { color: #087443; border-left: 3px solid #087443; }
QLabel#statusWatcher[state="warning"] {
    color: #9a6200;
    background-color: #fff8e6;
    border-left: 3px dashed #9a6200;
}
QLabel#statusWatcher[state="disabled"] { color: #98a2b3; border-left: 3px dotted #98a2b3; }
QLabel#historyScope {
    background-color: #eef5ff;
    color: #175cd3;
    border: 1px solid #bfd6ff;
    border-radius: 7px;
    padding: 8px 10px;
    font-weight: 600;
}

QTabWidget::pane {
    border: 1px solid #dfe5ee;
    border-radius: 10px;
    top: 0;
    background-color: #ffffff;
}
QTabBar::tab {
    background-color: transparent;
    border: none;
    border-bottom: 3px solid transparent;
    color: #667085;
    padding: 11px 17px 10px 17px;
    margin-right: 5px;
}
QTabBar::tab:selected {
    color: #175cd3;
    border-bottom-color: #2f6fed;
    font-weight: 700;
}
QTabBar::tab:hover { color: #175cd3; }

QPushButton {
    background-color: #ffffff;
    color: #344054;
    padding: 7px 15px;
    border-radius: 7px;
    border: 1px solid #cbd5e1;
    font-weight: 600;
}
QPushButton:hover {
    background-color: #f1f5f9;
    border-color: #94a3b8;
}
QPushButton:pressed { background-color: #e2e8f0; }
QPushButton#primaryButton {
    background-color: #2f6fed;
    border-color: #2f6fed;
    color: #ffffff;
}
QPushButton#primaryButton:hover {
    background-color: #245bd1;
    border-color: #245bd1;
}
QPushButton:disabled {
    background-color: #f1f5f9;
    border-color: #e2e8f0;
    color: #98a2b3;
}

QLineEdit, QSpinBox, QComboBox, QListWidget {
    background-color: #ffffff;
    color: #172033;
    padding: 5px 8px;
    border-radius: 6px;
    border: 1px solid #cbd5e1;
}
QLineEdit:focus, QSpinBox:focus, QComboBox:focus {
    border: 1px solid #2f6fed;
}
QScrollArea { background: transparent; }

QProgressBar {
    background-color: #e8eef7;
    color: #172033;
    border: 1px solid #cbd5e1;
    border-radius: 6px;
    min-height: 20px;
    text-align: center;
    font-weight: 700;
}
QProgressBar::chunk {
    background-color: #2f6fed;
    border-radius: 5px;
}

QTableWidget, QTreeWidget {
    background-color: #ffffff;
    color: #172033;
    border: 1px solid #dfe5ee;
    border-radius: 7px;
    gridline-color: #e9edf3;
}
QHeaderView::section {
    background-color: #f8fafc;
    color: #475467;
    padding: 7px 8px;
    border: none;
    border-bottom: 1px solid #dfe5ee;
    font-weight: 700;
}
QTableWidget::item, QTreeWidget::item { padding: 4px; }
QTableWidget::item:selected, QTreeWidget::item:selected {
    background-color: #dbeafe;
    color: #172033;
}
"""

# The dark theme reuses every light selector with a darker palette. Replacing
# colour literals keeps both stylesheets structurally identical (one set of
# rules to maintain) while still shipping a real, immutable dark theme.
_DARK_COLORS = {
    "#f6f8fb": "#141a24",  # window background
    "#172033": "#e6edf7",  # window text
    "#ffffff": "#1e2634",  # raised surfaces
    "#dfe5ee": "#364153",  # borders
    "#101828": "#f2f6ff",  # strong text
    "#667085": "#a6b3c8",  # muted text
    "#eef2f7": "#273142",  # badge surface
    "#344054": "#cbd5e6",  # secondary text
    "#d9e0ea": "#46536a",  # badge border
    "#8ab4ff": "#3b6fd4",  # status card border
    "#087443": "#4ade80",  # active
    "#9a6200": "#fbbf24",  # paused / warning
    "#c12c3d": "#ff6b7a",  # error
    "#fff4e5": "#3a2f14",  # conflict badge surface
    "#f3c98b": "#a16207",  # conflict badge border
    "#ffe9c7": "#4a3a17",  # conflict badge hover
    "#175cd3": "#7cb0ff",  # link / info text
    "#475467": "#b6c2d4",  # table header text
    "#eef5ff": "#152a45",  # info note surface
    "#bfd6ff": "#2f4d75",  # info note border
    "#fbfcfe": "#111822",  # preview surface
    "#e4e7ec": "#354052",  # preview border
    "#f8fafc": "#1b2330",  # subtle surface
    "#98a2b3": "#7d8ba3",  # disabled text
    "#fff8e6": "#3a3218",  # watcher warning surface
    "#2f6fed": "#5b8cff",  # accent / primary
    "#245bd1": "#3f73e0",  # primary hover
    "#cbd5e1": "#46536a",  # control border
    "#94a3b8": "#7d8ba3",  # hover border
    "#f1f5f9": "#2a3444",  # pressed / disabled surface
    "#e2e8f0": "#333f52",  # pressed / disabled border
    "#e8eef7": "#2a3444",  # progress track
    "#e9edf3": "#2b3646",  # grid lines
    "#dbeafe": "#24406b",  # selection surface
}

DARK_STYLESHEET = STYLESHEET
for _light, _dark in _DARK_COLORS.items():
    DARK_STYLESHEET = DARK_STYLESHEET.replace(_light, _dark)

# Status states are announced with an icon too, never colour alone.
STATE_ICONS = {
    "active": "✅",
    "paused": "⏸️",
    "warning": "⚠️",
    "error": "⛔",
    "starting": "🔄",
    "disabled": "○",
}
# Messages that already begin with one of these already carry a non-colour cue.
_ALREADY_ICONIC = (
    "✅", "⏸", "⚠", "⛔", "🔄", "○", "★", "❌", "💤", "🔒", "⚡", "⏳",
    "🖥", "🗑", "📄", "📁", "⬆", "⬇", "▶",
)


def set_status_text(label, state: str, text: str) -> None:
    """Set a status label's text with an icon plus its coloured state property."""
    icon = STATE_ICONS.get(state)
    if icon and not text.lstrip().startswith(_ALREADY_ICONIC):
        text = f"{icon} {text}"
    label.setText(text)
    if label.property("state") != state:
        label.setProperty("state", state)
        label.style().unpolish(label)
        label.style().polish(label)


def stylesheet_for(theme: str) -> str:
    """Return the stylesheet for a theme (empty for the native system theme)."""
    if theme == THEME_DARK:
        return DARK_STYLESHEET
    if theme == THEME_SYSTEM:
        return ""
    return STYLESHEET


def current_theme() -> str:
    """Read the persisted theme, defaulting to light (the historical look)."""
    try:
        from ..config import shared
        theme = shared().get("theme", DEFAULT_THEME)
    except Exception:
        theme = DEFAULT_THEME
    return theme if theme in THEMES else DEFAULT_THEME


def _dark_palette() -> QPalette:
    palette = QPalette()
    roles = {
        QPalette.ColorRole.Window: "#141a24",
        QPalette.ColorRole.WindowText: "#e6edf7",
        QPalette.ColorRole.Base: "#111822",
        QPalette.ColorRole.AlternateBase: "#1b2330",
        QPalette.ColorRole.Text: "#e6edf7",
        QPalette.ColorRole.ToolTipBase: "#273142",
        QPalette.ColorRole.ToolTipText: "#e6edf7",
        QPalette.ColorRole.Button: "#1e2634",
        QPalette.ColorRole.ButtonText: "#cbd5e6",
        QPalette.ColorRole.Link: "#7cb0ff",
        QPalette.ColorRole.Highlight: "#3b6fd4",
        QPalette.ColorRole.HighlightedText: "#ffffff",
    }
    for role, color in roles.items():
        palette.setColor(role, QColor(color))
    return palette


_original_palettes: dict[int, QPalette | None] = {}


def apply_style(app, theme: str | None = None) -> None:
    """Apply a theme to the application, preserving the original system palette.

    ``theme=None`` reads the persisted preference; callers that just changed the
    setting pass the new value explicitly so the change is visible immediately.
    """
    resolved = current_theme() if theme is None else theme
    if resolved not in THEMES:
        resolved = DEFAULT_THEME
    key = id(app)
    if key not in _original_palettes:
        try:
            _original_palettes[key] = QPalette(app.palette())
        except Exception:  # pragma: no cover - QPalette is always available in Qt
            _original_palettes[key] = None
    app.setStyleSheet(stylesheet_for(resolved))
    original = _original_palettes.get(key)
    if resolved == THEME_DARK:
        app.setPalette(_dark_palette())
    elif original is not None:
        app.setPalette(QPalette(original))
