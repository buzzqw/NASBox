from __future__ import annotations

from pathlib import Path

from PyQt6.QtWidgets import (
    QComboBox, QDialog, QDialogButtonBox, QFileDialog, QFormLayout,
    QHBoxLayout, QLabel, QLineEdit, QMessageBox, QPushButton, QSpinBox,
    QVBoxLayout, QWizard, QWizardPage,
)

from .. import rsync_ops
from ..i18n import t
from .async_utils import run_in_background

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


class FirstRunSetupWizard(QWizard):
    """Stage a complete PC-side setup and expose values only after Finish."""

    def __init__(self, cfg, parent=None) -> None:
        super().__init__(parent)
        self.cfg = cfg
        self._detected_values: dict = {}
        self.setWindowTitle(t("setup.title"))
        self.setOption(QWizard.WizardOption.NoBackButtonOnStartPage)
        self.resize(680, 470)

        self.local_page = _LocalFolderPage(cfg.local_root(), self)
        self.connection_page = _ConnectionPage(cfg, self)
        self.server_page = _ServerPage(cfg, self)
        self.addPage(self.local_page)
        self.addPage(self.connection_page)
        self.addPage(self.server_page)

    def staged_config(self):
        values = {
            "nas_lan": self.connection_page.host.text().strip(),
            "nas_wan": "",
            "nas_user": self.connection_page.user.text().strip(),
            "ssh_port": self.connection_page.port.value(),
            "jump_host": "",
            "jump_port": 22,
            "jump_user": "",
            "remote_server_script": self.server_page.script.text().strip(),
            "remote_prefix": self.server_page.remote_root.text().strip(),
        }
        return _StagedConfig(self.cfg, values)

    def setup_values(self) -> dict:
        values = {
            "local_root": str(Path(self.local_page.path_edit.text().strip()).expanduser()),
            "nas_lan": self.connection_page.host.text().strip(),
            "nas_user": self.connection_page.user.text().strip(),
            "ssh_port": self.connection_page.port.value(),
            "remote_server_script": self.server_page.script.text().strip(),
            "remote_prefix": self.server_page.remote_root.text().strip(),
        }
        detected = self._detected_values
        values.update({
            "retention_days_remote": _integer_or_default(
                detected.get("RETENTION_DAYS"), self.cfg.get("retention_days_remote")
            ),
            "repository_id": detected.get("REPOSITORY_ID", self.cfg.get("repository_id") or ""),
            "remote_repository_ready": (
                detected.get("REPOSITORY_READY", "").lower() == "true"
                if "REPOSITORY_READY" in detected else bool(self.cfg.get("remote_repository_ready"))
            ),
            "remote_journal_ready": (
                detected.get("JOURNAL_READY", "").lower() == "true"
                if "JOURNAL_READY" in detected else bool(self.cfg.get("remote_journal_ready"))
            ),
            "server_state_dir_remote": detected.get(
                "STATE_DIR", self.cfg.get("server_state_dir_remote") or ""
            ),
            "server_lock_file_remote": detected.get(
                "SYNC_LOCK_FILE", self.cfg.get("server_lock_file_remote") or ""
            ),
        })
        return values


class _LocalFolderPage(QWizardPage):
    def __init__(self, current: str, parent=None) -> None:
        super().__init__(parent)
        self.setTitle(t("setup.local_title"))
        self.setSubTitle(t("setup.local_intro"))
        row = QHBoxLayout(self)
        self.path_edit = QLineEdit(current or str(Path.home() / "NASBox"))
        self.path_edit.setToolTip(t("dialogs.folder.path_tooltip"))
        self.path_edit.textChanged.connect(self.completeChanged)
        browse = QPushButton(t("dialogs.folder.browse_btn"))
        browse.clicked.connect(self._browse)
        row.addWidget(self.path_edit, 1)
        row.addWidget(browse)

    def isComplete(self) -> bool:
        return bool(self.path_edit.text().strip())

    def _browse(self) -> None:
        path = QFileDialog.getExistingDirectory(
            self, t("dialogs.folder.browse_dialog_title"), self.path_edit.text()
        )
        if path:
            self.path_edit.setText(path)


class _ConnectionPage(QWizardPage):
    def __init__(self, cfg, parent=None) -> None:
        super().__init__(parent)
        self._connection_ok = False
        self._busy = False
        self.setTitle(t("setup.connection_title"))
        self.setSubTitle(t("setup.connection_intro"))
        layout = QVBoxLayout(self)
        form = QFormLayout()
        self.host = QLineEdit(str(cfg.get("nas_lan") or ""))
        self.host.setPlaceholderText(t("settings.nas_lan_placeholder"))
        self.user = QLineEdit(str(cfg.get("nas_user") or ""))
        self.port = QSpinBox()
        self.port.setRange(1, 65535)
        self.port.setValue(int(cfg.get("ssh_port") or 22))
        form.addRow(t("settings.nas_lan_label"), self.host)
        form.addRow(t("settings.nas_user_label"), self.user)
        form.addRow(t("settings.ssh_port_label"), self.port)
        layout.addLayout(form)
        row = QHBoxLayout()
        self.test_btn = QPushButton(t("setup.test_connection"))
        self.test_btn.clicked.connect(self._test_connection)
        self.status = QLabel(t("setup.test_required"))
        self.status.setWordWrap(True)
        row.addWidget(self.test_btn)
        row.addWidget(self.status, 1)
        layout.addLayout(row)
        advanced = QLabel(t("setup.advanced_later"))
        advanced.setWordWrap(True)
        layout.addWidget(advanced)
        self.host.textChanged.connect(self._reset_test)
        self.user.textChanged.connect(self._reset_test)
        self.port.valueChanged.connect(self._reset_test)

    def isComplete(self) -> bool:
        return bool(
            self._connection_ok and self.host.text().strip() and self.user.text().strip()
        )

    def _reset_test(self, *_args) -> None:
        self._connection_ok = False
        self.status.setText(t("setup.test_required"))
        wizard = self.wizard()
        if wizard is not None:
            wizard._detected_values = {}
        self.completeChanged.emit()

    def _test_connection(self) -> None:
        if self._busy:
            return
        if not self.host.text().strip() or not self.user.text().strip():
            self.status.setText(t("setup.connection_missing"))
            return
        self._busy = True
        self.test_btn.setText(t("setup.testing"))
        cfg = self.wizard().staged_config()
        run_in_background(
            self, "_setup_connection_call", lambda: _test_connection_worker(cfg),
            self._on_test_done,
        )

    def _on_test_done(self, result: dict, exc: Exception | None) -> None:
        self._busy = False
        self.test_btn.setText(t("setup.test_connection"))
        self._connection_ok = bool(exc is None and result.get("ok"))
        if exc is not None:
            self.status.setText(t("setup.test_failed", detail=str(exc)))
        elif self._connection_ok:
            self.status.setText(t("setup.test_ok", host=result["host"]))
        else:
            self.status.setText(t("setup.test_failed", detail=result.get("detail", "")))
        self.completeChanged.emit()


class _ServerPage(QWizardPage):
    def __init__(self, cfg, parent=None) -> None:
        super().__init__(parent)
        self._busy = False
        self.setTitle(t("setup.server_title"))
        self.setSubTitle(t("setup.server_intro"))
        layout = QVBoxLayout(self)
        form = QFormLayout()
        self.script = QLineEdit(str(cfg.get("remote_server_script") or ""))
        self.script.setPlaceholderText(t("settings.remote_script_placeholder"))
        self.remote_root = QLineEdit(str(cfg.get("remote_prefix") or "/volume1/NASBox"))
        self.remote_root.setPlaceholderText(t("settings.remote_prefix_placeholder"))
        form.addRow(t("settings.remote_script_label"), self.script)
        form.addRow(t("settings.remote_prefix_label"), self.remote_root)
        layout.addLayout(form)
        row = QHBoxLayout()
        self.detect_btn = QPushButton(t("settings.detect_btn"))
        self.detect_btn.clicked.connect(self._detect)
        self.status = QLabel(t("setup.detect_optional"))
        self.status.setWordWrap(True)
        row.addWidget(self.detect_btn)
        row.addWidget(self.status, 1)
        layout.addLayout(row)
        self.remote_root.textChanged.connect(self.completeChanged)

    def isComplete(self) -> bool:
        return bool(self.remote_root.text().strip())

    def _detect(self) -> None:
        if self._busy:
            return
        self._busy = True
        self.detect_btn.setText(t("settings.detect_running"))
        cfg = self.wizard().staged_config()
        script_hint = self.script.text().strip()
        run_in_background(
            self, "_setup_detect_call", lambda: _setup_detect_worker(cfg, script_hint),
            self._on_detect_done,
        )

    def _on_detect_done(self, result: dict, exc: Exception | None) -> None:
        self._busy = False
        self.detect_btn.setText(t("settings.detect_btn"))
        if exc is not None:
            self.status.setText(t("setup.detect_failed", detail=str(exc)))
            return
        if not result.get("connected"):
            self.status.setText(t("setup.detect_failed", detail=t("settings.nas_unreachable_body")))
            return
        if not result.get("fetch_ok"):
            detail = result.get("err") or t("settings.daemon_not_found_body")
            self.status.setText(t("setup.detect_failed", detail=detail))
            return
        if result.get("script_path"):
            self.script.setText(result["script_path"])
        values = result.get("values", {})
        if values.get("SHARE_ROOT"):
            self.remote_root.setText(values["SHARE_ROOT"])
        self.wizard()._detected_values = values
        self.status.setText(t("setup.detect_ok"))


class _StagedConfig:
    def __init__(self, base, values: dict) -> None:
        self.base = base
        self.values = values

    def get(self, key, default=None):
        return self.values.get(key, self.base.get(key, default))


def _test_connection_worker(cfg) -> dict:
    conn = rsync_ops.resolve_connection(cfg)
    if conn is None:
        return {"ok": False, "detail": t("settings.nas_unreachable_body")}
    ok, _stdout, stderr = rsync_ops.run_remote_script(cfg, conn, "true", [], timeout=20)
    return {"ok": ok, "host": conn.host, "detail": stderr}


def _setup_detect_worker(cfg, script_path_hint: str) -> dict:
    from .settings_tab import _detect_worker

    return _detect_worker(cfg, script_path_hint)


def _integer_or_default(value, default):
    try:
        return int(value)
    except (TypeError, ValueError):
        return default
