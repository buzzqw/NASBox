"""High-contrast, light application theme for the PyQt desktop client.

The native palette on a few Linux themes resolves Qt's palette(mid) to nearly
white. This stylesheet uses explicit accessible colours so inactive tabs and
secondary actions remain visible instead of blending into the background.
"""

STYLESHEET = """
QMainWindow, QWidget {
    background-color: #f6f8fb;
    color: #172033;
    font-size: 13px;
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
QLabel#versionBadge {
    background-color: #eef2f7;
    color: #344054;
    border: 1px solid #d9e0ea;
    border-radius: 10px;
    padding: 5px 10px;
    font-weight: 700;
}

QGroupBox {
    background-color: #ffffff;
    border: 1px solid #dfe5ee;
    border-radius: 10px;
    margin-top: 16px;
    padding: 16px 14px 14px 14px;
    color: #172033;
    font-weight: 700;
}
QGroupBox::title {
    subcontrol-origin: margin;
    subcontrol-position: top left;
    left: 10px;
    padding: 0 6px;
}
QGroupBox#statusCard { border-color: #8ab4ff; }
QLabel#statusMessage {
    font-size: 17px;
    font-weight: 700;
    padding: 5px 2px 12px 2px;
}
QLabel#statusMessage[state="active"] { color: #087443; }
QLabel#statusMessage[state="paused"] { color: #9a6200; }
QLabel#statusMessage[state="warning"] { color: #9a6200; }
QLabel#statusMessage[state="error"] { color: #c12c3d; }
QLabel#statusQueue {
    color: #667085;
    background-color: #f8fafc;
    border-radius: 6px;
    padding: 7px 9px;
}
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


def apply_style(app) -> None:
    app.setStyleSheet(STYLESHEET)
