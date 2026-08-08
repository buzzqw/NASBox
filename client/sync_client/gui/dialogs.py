from __future__ import annotations

from pathlib import Path

from PyQt6.QtWidgets import (
    QComboBox, QDialog, QDialogButtonBox, QFileDialog, QFormLayout,
    QHBoxLayout, QLabel, QLineEdit, QMessageBox, QPushButton, QSpinBox,
    QVBoxLayout,
)

from ..i18n import t

PAUSE_PRESETS = [
    ("dialogs.pause.preset_15m", 15 * 60),
    ("dialogs.pause.preset_30m", 30 * 60),
    ("dialogs.pause.preset_1h", 60 * 60),
    ("dialogs.pause.preset_4h", 4 * 60 * 60),
    ("dialogs.pause.preset_8h", 8 * 60 * 60),
    ("dialogs.pause.preset_custom", None),
]


class PauseForDialog(QDialog):
    """Let the user pause sync for a preset duration or a custom number of minutes."""

    def __init__(self, parent=None) -> None:
        super().__init__(parent)
        self.setWindowTitle(t("dialogs.pause.title"))
        layout = QVBoxLayout(self)

        form = QFormLayout()
        self.combo = QComboBox()
        for key, _ in PAUSE_PRESETS:
            self.combo.addItem(t(key))
        self.combo.setToolTip(t("dialogs.pause.duration_tooltip"))
        self.combo.currentIndexChanged.connect(self._on_preset_changed)
        form.addRow(t("dialogs.pause.duration_label"), self.combo)

        self.custom_minutes = QSpinBox()
        self.custom_minutes.setRange(1, 60 * 24 * 30)
        self.custom_minutes.setValue(60)
        self.custom_minutes.setSuffix(" min")
        self.custom_minutes.setEnabled(False)
        self.custom_minutes.setToolTip(t("dialogs.pause.custom_minutes_tooltip"))
        form.addRow(t("dialogs.pause.custom_minutes_label"), self.custom_minutes)

        layout.addLayout(form)

        buttons = QDialogButtonBox(
            QDialogButtonBox.StandardButton.Ok | QDialogButtonBox.StandardButton.Cancel
        )
        buttons.accepted.connect(self.accept)
        buttons.rejected.connect(self.reject)
        layout.addWidget(buttons)

    def _on_preset_changed(self, index: int) -> None:
        is_custom = PAUSE_PRESETS[index][1] is None
        self.custom_minutes.setEnabled(is_custom)

    def seconds(self) -> int:
        index = self.combo.currentIndex()
        preset_seconds = PAUSE_PRESETS[index][1]
        if preset_seconds is not None:
            return preset_seconds
        return self.custom_minutes.value() * 60


class FolderSetupDialog(QDialog):
    """Choose where the single NASBox folder lives on this PC.

    Used both for the very first run (no local_root set yet) and later, if the
    user wants to relocate it. There is nothing to "add" or "remove" here --
    NASBox always syncs exactly one folder, the way Dropbox does; this dialog
    only ever picks *where that one folder is*.
    """

    def __init__(self, parent=None, current: str = "", relocating: bool = False) -> None:
        super().__init__(parent)
        self.setWindowTitle(t("dialogs.folder.title_relocate" if relocating else "dialogs.folder.title_welcome"))
        layout = QVBoxLayout(self)

        intro_key = "dialogs.folder.intro_relocate" if relocating else "dialogs.folder.intro_welcome"
        intro = QLabel(t(intro_key))
        intro.setWordWrap(True)
        layout.addWidget(intro)

        row = QHBoxLayout()
        default_path = current or str(Path.home() / "NASBox")
        self.path_edit = QLineEdit(default_path)
        self.path_edit.setToolTip(t("dialogs.folder.path_tooltip"))
        browse_btn = QPushButton(t("dialogs.folder.browse_btn"))
        browse_btn.setToolTip(t("dialogs.folder.browse_tooltip"))
        browse_btn.clicked.connect(self._browse)
        row.addWidget(self.path_edit)
        row.addWidget(browse_btn)
        layout.addLayout(row)

        buttons = QDialogButtonBox(
            QDialogButtonBox.StandardButton.Ok | QDialogButtonBox.StandardButton.Cancel
        )
        buttons.accepted.connect(self._on_accept)
        buttons.rejected.connect(self.reject)
        layout.addWidget(buttons)

        self._chosen = ""

    def _browse(self) -> None:
        path = QFileDialog.getExistingDirectory(
            self, t("dialogs.folder.browse_dialog_title"), self.path_edit.text()
        )
        if path:
            self.path_edit.setText(path)

    def _on_accept(self) -> None:
        text = self.path_edit.text().strip()
        if not text:
            QMessageBox.warning(self, t("dialogs.folder.missing_path_title"), t("dialogs.folder.missing_path_body"))
            return
        target = Path(text).expanduser()
        try:
            target.mkdir(parents=True, exist_ok=True)
        except OSError as exc:
            QMessageBox.warning(self, t("dialogs.folder.mkdir_failed_title"), str(exc))
            return
        self._chosen = str(target)
        self.accept()

    def chosen_path(self) -> str:
        return self._chosen
