from __future__ import annotations

import difflib
import time
from dataclasses import dataclass
from pathlib import Path

from PyQt6.QtCore import Qt, QTimer, QUrl, pyqtSignal
from PyQt6.QtGui import QColor, QDesktopServices
from PyQt6.QtWidgets import (
    QAbstractItemView, QComboBox, QGroupBox, QHBoxLayout, QHeaderView, QLabel,
    QLineEdit, QMessageBox, QPlainTextEdit, QProgressBar, QPushButton,
    QSplitter, QTreeWidget, QTreeWidgetItem, QVBoxLayout, QWidget,
)

from .. import conflicts
from .. import rsync_ops
from ..config import Config
from ..engine import SyncEngine
from ..i18n import t
from .async_utils import run_in_background
from .dialogs import FileVersionDialog
from .format_utils import human_size


GROUP_ROLE = Qt.ItemDataRole.UserRole
PREVIEW_LIMIT_BYTES = 512 * 1024
PREVIEW_LIMIT_LINES = 4000


@dataclass(frozen=True)
class _GroupInfo:
    total_alt_size: int
    newest_alt: Path | None
    newest_mtime: float
    origin_device: str
    recommended: Path | None


def _report_progress(
    progress, phase: str, done: int = 0, total: int = 0, path: str = "",
) -> None:
    """Best-effort progress emission from the worker thread."""
    if progress is None:
        return
    try:
        progress({"phase": phase, "done": done, "total": total, "path": path})
    except Exception:
        pass  # the tab/window may be closing while the worker finishes


class _ConflictItem(QTreeWidgetItem):
    """Row with a numeric sort key per column.

    QTreeWidget sorts by the cell text by default, which would order
    ``"9 KB"`` after ``"10 MB"`` and dates as strings. The parallel key list
    keeps sorting correct while the visible text stays user-friendly.
    """

    def __init__(self, values: list[str], sort_keys: list[object]) -> None:
        super().__init__(values)
        self._sort_keys = sort_keys

    def __lt__(self, other: object) -> bool:
        tree = self.treeWidget()
        column = tree.sortColumn() if tree is not None else 0
        keys = getattr(other, "_sort_keys", None)
        if not isinstance(keys, list):
            return False
        mine = self._sort_keys[column]
        theirs = keys[column]
        if mine == theirs:
            # Stable, predictable order for equal values (e.g. same size).
            mine = self._sort_keys[0]
            theirs = keys[0]
        return bool(mine < theirs)


class ConflictsTab(QWidget):
    """Review and resolve version conflicts, with metadata and a real preview.

    The tab deliberately keeps the wording user-facing: a conflict is "two
    versions of the same file", not a digest/causal-version lecture. Technical
    details remain available in the row/preview, but the primary actions are
    keep this version, keep the original, or move the alternatives to history.
    """

    conflict_count_changed = pyqtSignal(int)

    def __init__(self, cfg: Config, engine: SyncEngine, parent=None) -> None:
        super().__init__(parent)
        self.cfg = cfg
        self.engine = engine
        self._groups: list[conflicts.ConflictGroup] = []
        self._infos: dict[str, _GroupInfo] = {}
        self._busy = False
        self._action_busy = False
        self._filter = ""

        self._auto_refresh_timer = QTimer(self)
        self._auto_refresh_timer.setInterval(30000)
        self._auto_refresh_timer.timeout.connect(self._on_auto_refresh)
        if hasattr(self.engine, "log_event"):
            self.engine.log_event.connect(self._on_log_event)

        root = QVBoxLayout(self)
        notice = QLabel(t("conflicts.notice"))
        notice.setWordWrap(True)
        notice.setObjectName("conflictNotice")
        root.addWidget(notice)

        action_row = QHBoxLayout()
        self.count_label = QLabel(t("conflicts.count_unknown"))
        self.count_label.setObjectName("conflictCount")
        action_row.addWidget(self.count_label)
        action_row.addStretch(1)
        self.search_edit = QLineEdit()
        self.search_edit.setPlaceholderText(t("conflicts.search_placeholder"))
        self.search_edit.setClearButtonEnabled(True)
        self.search_edit.textChanged.connect(self._on_filter_changed)
        action_row.addWidget(self.search_edit, 1)
        self.refresh_btn = QPushButton(t("conflicts.refresh_btn"))
        self.refresh_btn.clicked.connect(self.refresh)
        action_row.addWidget(self.refresh_btn)
        self.resolve_everything_btn = QPushButton(t("conflicts.resolve_everything_btn"))
        self.resolve_everything_btn.setToolTip(t("conflicts.resolve_everything_tooltip"))
        self.resolve_everything_btn.setObjectName("primaryButton")
        self.resolve_everything_btn.clicked.connect(self._resolve_everything)
        action_row.addWidget(self.resolve_everything_btn)
        root.addLayout(action_row)

        splitter = QSplitter(Qt.Orientation.Horizontal)
        splitter.setChildrenCollapsible(False)

        left = QWidget()
        left_layout = QVBoxLayout(left)
        left_layout.setContentsMargins(0, 0, 0, 0)
        self.tree = QTreeWidget()
        self.tree.setColumnCount(5)
        self.tree.setHeaderLabels([
            t("conflicts.col_original"),
            t("conflicts.col_alternatives"),
            t("conflicts.col_size"),
            t("conflicts.col_modified"),
            t("conflicts.col_origin"),
        ])
        header = self.tree.header()
        header.setStretchLastSection(False)
        # Every column is user-resizable: no Stretch/ResizeToContents section,
        # which would silently override the width the user drags.
        header.setSectionResizeMode(QHeaderView.ResizeMode.Interactive)
        header.setMinimumSectionSize(70)
        for column, width in enumerate((360, 90, 110, 150, 130)):
            self.tree.setColumnWidth(column, width)
        self.tree.setSortingEnabled(True)
        header.setSortIndicatorShown(True)
        header.setSortIndicator(0, Qt.SortOrder.AscendingOrder)
        self.tree.setSelectionMode(QAbstractItemView.SelectionMode.ExtendedSelection)
        self.tree.setRootIsDecorated(False)
        self.tree.setAlternatingRowColors(True)
        self.tree.itemSelectionChanged.connect(self._selection_changed)
        left_layout.addWidget(self.tree, 1)
        splitter.addWidget(left)

        right = QWidget()
        right_layout = QVBoxLayout(right)
        right_layout.setContentsMargins(10, 0, 0, 0)
        self.detail_path = QLabel(t("conflicts.no_selection"))
        self.detail_path.setObjectName("conflictDetailPath")
        self.detail_path.setWordWrap(True)
        right_layout.addWidget(self.detail_path)
        self.detail_meta = QLabel()
        self.detail_meta.setObjectName("conflictDetailMeta")
        self.detail_meta.setWordWrap(True)
        right_layout.addWidget(self.detail_meta)

        self.compare_box = QGroupBox("Dettaglio Versioni in Conflitto")
        compare_layout = QVBoxLayout(self.compare_box)
        self.orig_info_label = QLabel("Originale: -")
        self.orig_info_label.setWordWrap(True)
        compare_layout.addWidget(self.orig_info_label)
        self.alt_info_label = QLabel("Alternativa: -")
        self.alt_info_label.setWordWrap(True)
        compare_layout.addWidget(self.alt_info_label)
        self.recommend_label = QLabel("")
        self.recommend_label.setWordWrap(True)
        compare_layout.addWidget(self.recommend_label)
        right_layout.addWidget(self.compare_box)

        choose_row = QHBoxLayout()
        choose_row.addWidget(QLabel(t("conflicts.keep_label")))
        self.candidate_combo = QComboBox()
        self.candidate_combo.currentIndexChanged.connect(lambda _index: self._update_preview())
        choose_row.addWidget(self.candidate_combo, 1)
        right_layout.addLayout(choose_row)

        preview_box = QGroupBox(t("conflicts.preview_title"))
        preview_layout = QVBoxLayout(preview_box)
        self.preview = QPlainTextEdit()
        self.preview.setReadOnly(True)
        self.preview.setLineWrapMode(QPlainTextEdit.LineWrapMode.NoWrap)
        self.preview.setObjectName("conflictPreview")
        preview_layout.addWidget(self.preview, 1)
        right_layout.addWidget(preview_box, 1)

        direct_actions = QHBoxLayout()
        self.keep_orig_btn = QPushButton("🟢 Mantieni Originale")
        self.keep_orig_btn.setToolTip("Conserva il file originale ed elimina la copia di conflitto")
        self.keep_orig_btn.clicked.connect(self._resolve_selected_keep_original)
        direct_actions.addWidget(self.keep_orig_btn)

        self.keep_alt_btn = QPushButton("🔵 Usa Alternativa")
        self.keep_alt_btn.setToolTip("Sostituisce l'originale con la versione alternativa selezionata")
        self.keep_alt_btn.clicked.connect(self._keep_selected)
        direct_actions.addWidget(self.keep_alt_btn)

        self.keep_both_btn = QPushButton("🟡 Conserva entrambe")
        self.keep_both_btn.setToolTip("Rinomina la copia alternativa come file normale (copia archiviata), senza perdere nulla")
        self.keep_both_btn.clicked.connect(self._keep_both_action)
        direct_actions.addWidget(self.keep_both_btn)
        right_layout.addLayout(direct_actions)

        buttons = QHBoxLayout()
        self.open_btn = QPushButton(t("conflicts.open_btn"))
        self.open_btn.clicked.connect(self._open_selected)
        buttons.addWidget(self.open_btn)
        self.restore_btn = QPushButton(t("conflicts.restore_btn"))
        self.restore_btn.setToolTip(t("conflicts.restore_tooltip"))
        self.restore_btn.clicked.connect(self._restore_version)
        buttons.addWidget(self.restore_btn)
        self.discard_btn = QPushButton(t("conflicts.discard_btn"))
        self.discard_btn.setToolTip(t("conflicts.discard_tooltip"))
        self.discard_btn.clicked.connect(self._discard_copies)
        buttons.addWidget(self.discard_btn)
        self.keep_btn = QPushButton(t("conflicts.keep_btn"))
        self.keep_btn.setObjectName("primaryButton")
        self.keep_btn.clicked.connect(self._keep_selected)
        buttons.addWidget(self.keep_btn)
        right_layout.addLayout(buttons)

        self.resolve_all_btn = QPushButton(t("conflicts.resolve_all_btn"))
        self.resolve_all_btn.setToolTip(t("conflicts.resolve_all_tooltip"))
        self.resolve_all_btn.clicked.connect(self._resolve_selected_keep_original)
        right_layout.addWidget(self.resolve_all_btn)

        splitter.addWidget(right)
        splitter.setStretchFactor(0, 3)
        splitter.setStretchFactor(1, 2)
        splitter.setSizes([620, 480])
        root.addWidget(splitter, 1)

        self.progress_row = QWidget()
        progress_layout = QHBoxLayout(self.progress_row)
        progress_layout.setContentsMargins(0, 0, 0, 0)
        self.progress_label = QLabel()
        self.progress_label.setObjectName("conflictProgressLabel")
        self.progress_label.setMinimumWidth(180)
        self.progress_bar = QProgressBar()
        self.progress_bar.setTextVisible(True)
        self.progress_bar.setMinimumWidth(220)
        progress_layout.addWidget(self.progress_label)
        progress_layout.addWidget(self.progress_bar, 1)
        self.progress_row.hide()
        root.addWidget(self.progress_row)

        self.status_label = QLabel()
        self.status_label.setWordWrap(True)
        self.status_label.setObjectName("conflictStatus")
        root.addWidget(self.status_label)

        self._update_buttons()
        self.refresh()

    # --- refresh lifecycle ---

    def conflict_count(self) -> int:
        return len(self._groups)

    def _on_auto_refresh(self) -> None:
        if self.isVisible() and not self._busy and not self._action_busy:
            self.refresh()

    def _on_log_event(self, action: str, _path: str, _detail: str) -> None:
        if action in ("CONFLICT", "CONFLICT_STORM", "RESOLVE_CONFLICT", "MIRROR_OVERWRITE"):
            if not self._busy and not self._action_busy:
                self.refresh()

    def refresh(self) -> None:
        local_root = self.cfg.local_root()
        if not local_root:
            self._groups = []
            self._infos = {}
            self._populate()
            self.count_label.setText(t("conflicts.no_folder"))
            self.conflict_count_changed.emit(0)
            return
        if self._busy or self._action_busy:
            return
        self._busy = True
        self.refresh_btn.setEnabled(False)
        self.count_label.setText(t("conflicts.scanning"))
        run_in_background(
            self, "_conflicts_scan_call",
            lambda: conflicts.scan_conflict_groups(local_root, self.engine.sync_state),
            self._on_scan_done,
        )

    def showEvent(self, event) -> None:
        super().showEvent(event)
        self._auto_refresh_timer.start()
        self.refresh()

    def hideEvent(self, event) -> None:
        super().hideEvent(event)
        self._auto_refresh_timer.stop()

    def _on_scan_done(self, result, exc: Exception | None) -> None:
        self._busy = False
        self.refresh_btn.setEnabled(True)
        if exc is not None:
            self.count_label.setText(t("conflicts.scan_error", detail=str(exc)))
            return
        self._groups = result or []
        self._infos = {}
        for group in self._groups:
            self._infos[str(group.original)] = self._group_info(group)
        self._populate()
        count = len(self._groups)
        base = (
            t("conflicts.count", groups=count, files=sum(len(group.candidates) for group in self._groups))
            if count else t("conflicts.none")
        )
        self.count_label.setText(f"{base} ({time.strftime('%H:%M:%S')})")
        self.conflict_count_changed.emit(count)

    def _group_info(self, group: conflicts.ConflictGroup) -> _GroupInfo:
        total = 0
        newest: Path | None = None
        newest_mtime = 0.0
        origin = ""
        original_mtime = 0.0
        try:
            if group.original.exists():
                info = group.original.stat()
                original_mtime = info.st_mtime
                total += info.st_size
        except OSError:
            pass
        for candidate in group.candidates:
            try:
                info = candidate.stat()
            except OSError:
                continue
            total += info.st_size
            if info.st_mtime >= newest_mtime:
                newest_mtime = info.st_mtime
                newest = candidate
                match = conflicts.CONFLICT_RE.match(candidate.name)
                origin = match.group("origin") if match else origin
        recommended: Path | None = None
        if newest is not None and newest_mtime > original_mtime:
            recommended = newest
        elif group.original.exists():
            recommended = group.original
        elif newest is not None:
            recommended = newest
        if not origin:
            for candidate in group.candidates:
                match = conflicts.CONFLICT_RE.match(candidate.name)
                if match:
                    origin = match.group("origin")
                    break
        return _GroupInfo(total, newest, newest_mtime, origin, recommended)

    def _on_filter_changed(self, text: str) -> None:
        self._filter = text.strip().lower()
        self._populate()

    def _populate(self) -> None:
        selected = self._selected_group()
        selected_path = str(selected.original) if selected is not None else ""
        self.tree.clear()
        for group in self._groups:
            relative = self._relative(group.original)
            if self._filter and self._filter not in relative.lower():
                continue
            info = self._infos.get(str(group.original)) or self._group_info(group)
            origin = info.origin_device or "—"
            item = _ConflictItem(
                [
                    relative,
                    str(len(group.candidates)),
                    human_size(info.total_alt_size),
                    time.strftime("%Y-%m-%d %H:%M", time.localtime(info.newest_mtime)) if info.newest_mtime else "—",
                    origin,
                ],
                [
                    relative.lower(),
                    len(group.candidates),
                    info.total_alt_size,
                    info.newest_mtime,
                    origin.lower(),
                ],
            )
            item.setData(0, GROUP_ROLE, group)
            for column in range(5):
                item.setBackground(column, QColor("#fff3cd"))
            self.tree.addTopLevelItem(item)
        if self.tree.topLevelItemCount():
            target = None
            if selected_path:
                for index in range(self.tree.topLevelItemCount()):
                    item = self.tree.topLevelItem(index)
                    group = item.data(0, GROUP_ROLE)
                    if group is not None and str(group.original) == selected_path:
                        target = item
                        break
            self.tree.setCurrentItem(target or self.tree.topLevelItem(0))
        else:
            self.candidate_combo.clear()
            self.preview.clear()
            self.detail_path.setText(t("conflicts.no_selection"))
            self.detail_meta.clear()
        self._update_buttons()

    def _selection_changed(self) -> None:
        self.candidate_combo.blockSignals(True)
        self.candidate_combo.clear()
        group = self._selected_group()
        if group is None:
            self.candidate_combo.blockSignals(False)
            self._update_preview()
            self._update_buttons()
            return
        info = self._infos.get(str(group.original)) or self._group_info(group)
        options: list[tuple[Path, str]] = []
        if group.original.exists():
            options.append((group.original, t("conflicts.original_option")))
        for path in group.candidates:
            options.append((path, self._relative(path)))
        recommended = info.recommended
        recommended_index = 0
        for path, label in options:
            marker = f" ★ {t('conflicts.recommended')}" if recommended is not None and path == recommended else ""
            self.candidate_combo.addItem(f"{label}{marker}", path)
            if recommended is not None and path == recommended:
                recommended_index = self.candidate_combo.count() - 1
        # Preselect the version we would recommend (newest content), while
        # still leaving the user free to choose another one.
        self.candidate_combo.setCurrentIndex(recommended_index)
        self.candidate_combo.blockSignals(False)
        self._update_preview()
        self._update_buttons()

    def _update_preview(self) -> None:
        group = self._selected_group()
        candidate = self._selected_candidate()
        if group is None:
            self.detail_path.setText(t("conflicts.no_selection"))
            self.detail_meta.clear()
            self.orig_info_label.setText("Originale: -")
            self.alt_info_label.setText("Alternativa: -")
            self.recommend_label.setText("")
            self.preview.clear()
            return
        info = self._infos.get(str(group.original)) or self._group_info(group)
        self.detail_path.setText(self._relative(group.original))
        alternatives = len(group.candidates)
        self.detail_meta.setText(t(
            "conflicts.detail_meta",
            alternatives=alternatives,
            size=human_size(info.total_alt_size),
            origin=info.origin_device or "—",
            status=(
                t("conflicts.original_present") if group.original.exists()
                else t("conflicts.original_missing")
            ),
        ))

        orig_exists = group.original.exists()
        orig_st = None
        if orig_exists:
            try:
                orig_st = group.original.stat()
            except OSError:
                orig_st = None
        orig_time = time.strftime('%d/%m/%Y %H:%M:%S', time.localtime(orig_st.st_mtime)) if orig_st else "mancante"
        orig_size = human_size(orig_st.st_size) if orig_st else "0 B"
        self.orig_info_label.setText(f"🖥️ Versione Locale (Originale): {orig_size} • Modificato: {orig_time}")

        if candidate is not None and candidate.exists():
            try:
                c_st = candidate.stat()
                c_time = time.strftime('%d/%m/%Y %H:%M:%S', time.localtime(c_st.st_mtime))
                c_size = human_size(c_st.st_size)
            except OSError:
                c_time = "sconosciuto"
                c_size = "0 B"
            origin = info.origin_device or "altro dispositivo"
            self.alt_info_label.setText(f"☁️ Versione Alternativa: {c_size} • Modificato: {c_time} (Dispositivo: {origin})")
            if info.recommended == candidate:
                self.recommend_label.setText("★ Versione consigliata: Alternativa (più recente)")
            elif info.recommended == group.original:
                self.recommend_label.setText("★ Versione consigliata: Originale locale (più recente)")
            else:
                self.recommend_label.setText("")
        else:
            self.alt_info_label.setText("☁️ Versione Alternativa: Seleziona una copia")
            self.recommend_label.setText("")

        if candidate is None:
            self.preview.setPlainText("")
            return
        self.preview.setPlainText(self._preview_text(group, candidate))

    def _preview_text(self, group: conflicts.ConflictGroup, candidate: Path) -> str:
        original = group.original
        try:
            candidate_size = candidate.stat().st_size
        except OSError:
            return t("conflicts.preview_missing")
        if candidate_size > PREVIEW_LIMIT_BYTES:
            return t("conflicts.preview_too_large", size=human_size(candidate_size))
        candidate_text = self._read_text(candidate)
        if candidate_text is None:
            return t("conflicts.preview_binary", size=human_size(candidate_size))
        if candidate == original or not original.exists():
            lines = candidate_text.splitlines()
            if len(lines) > PREVIEW_LIMIT_LINES:
                lines = lines[:PREVIEW_LIMIT_LINES] + ["", t("conflicts.preview_truncated")]
            return "\n".join(lines)
        try:
            if original.stat().st_size > PREVIEW_LIMIT_BYTES:
                return t("conflicts.preview_too_large", size=human_size(original.stat().st_size))
        except OSError:
            return t("conflicts.preview_missing")
        original_text = self._read_text(original)
        if original_text is None:
            return t("conflicts.preview_binary", size=human_size(candidate_size))
        if original_text == candidate_text:
            return t("conflicts.preview_identical")
        diff = list(difflib.unified_diff(
            original_text.splitlines(),
            candidate_text.splitlines(),
            fromfile=t("conflicts.preview_original"),
            tofile=t("conflicts.preview_alternative"),
            lineterm="",
            n=3,
        ))
        if len(diff) > PREVIEW_LIMIT_LINES:
            diff = diff[:PREVIEW_LIMIT_LINES] + ["", t("conflicts.preview_truncated")]
        return "\n".join(diff)

    @staticmethod
    def _read_text(path: Path) -> str | None:
        try:
            data = path.read_bytes()
        except OSError:
            return None
        if b"\0" in data[:4096]:
            return None
        for encoding in ("utf-8", "latin-1"):
            try:
                return data.decode(encoding)
            except UnicodeDecodeError:
                continue
        return None

    # --- selection helpers ---

    def _selected_groups(self) -> list[conflicts.ConflictGroup]:
        groups: list[conflicts.ConflictGroup] = []
        for item in self.tree.selectedItems():
            group = item.data(0, GROUP_ROLE)
            if group is not None and group not in groups:
                groups.append(group)
        return groups

    def _selected_group(self) -> conflicts.ConflictGroup | None:
        groups = self._selected_groups()
        return groups[0] if groups else None

    def _selected_candidate(self) -> Path | None:
        value = self.candidate_combo.currentData()
        return value if isinstance(value, Path) else None

    def _update_buttons(self) -> None:
        selected = self._selected_candidate() is not None
        can_act = not self._busy and not self._action_busy
        has_group = self._selected_group() is not None
        has_groups = bool(self._selected_groups())
        self.open_btn.setEnabled(selected and can_act)
        self.keep_btn.setEnabled(selected and can_act)
        self.keep_orig_btn.setEnabled(has_groups and can_act)
        self.keep_alt_btn.setEnabled(selected and can_act)
        self.keep_both_btn.setEnabled(selected and can_act)
        self.restore_btn.setEnabled(has_group and can_act)
        self.discard_btn.setEnabled(has_group and can_act)
        self.resolve_all_btn.setEnabled(has_groups and can_act)
        self.resolve_everything_btn.setEnabled(bool(self._visible_groups()) and can_act)
        self.search_edit.setEnabled(can_act)
        self.refresh_btn.setEnabled(not self._busy and not self._action_busy)

    def _keep_both_action(self) -> None:
        group = self._selected_group()
        candidate = self._selected_candidate()
        local_root = self.cfg.local_root()
        if group is None or candidate is None or not local_root:
            return
        self._set_action_busy(True, t("conflicts.resolving"))

        def worker():
            ok, detail = conflicts.keep_both(
                group, candidate, local_root, self.engine.sync_state,
            )
            if ok:
                self.engine.wake()
            return {"ok": ok, "resolved": 1 if ok else 0, "problems": [] if ok else [detail]}

        run_in_background(self, "_conflict_action_call", worker, self._on_action_done)

    def _open_selected(self) -> None:
        path = self._selected_candidate()
        if path is not None:
            QDesktopServices.openUrl(QUrl.fromLocalFile(str(path)))

    # --- actions (always off the GUI thread) ---

    def _set_action_busy(self, busy: bool, message: str = "") -> None:
        self._action_busy = busy
        if busy:
            if message:
                self.progress_label.setText(message)
                self.status_label.setText(message)
            self.progress_bar.setRange(0, 0)  # indeterminate until the first step
            self.progress_bar.setValue(0)
            self.progress_label.setToolTip("")
            self.progress_row.show()
        else:
            self.progress_row.hide()
            self.progress_label.setText("")
        self._update_buttons()

    def _on_action_progress(self, payload: object) -> None:
        """GUI-thread slot for progress emitted by the resolver worker."""
        if not self._action_busy or not isinstance(payload, dict):
            return
        phase = str(payload.get("phase") or "")
        done = int(payload.get("done") or 0)
        total = int(payload.get("total") or 0)
        path = str(payload.get("path") or "")
        if phase == "waiting":
            self.progress_label.setText(t("conflicts.progress_waiting"))
            self.progress_bar.setRange(0, 0)
        elif phase == "cleaning":
            self.progress_label.setText(t("conflicts.progress_cleaning", done=done, total=total))
            if done <= 0:
                # Waiting for the first NAS lease/delete: animate instead of
                # showing a frozen 0% bar during the first SSH round-trip.
                self.progress_bar.setRange(0, 0)
            else:
                self.progress_bar.setRange(0, max(total, 1))
                self.progress_bar.setValue(min(done, total))
        elif phase == "done":
            self.progress_label.setText(t("conflicts.progress_done", done=done, total=total))
            self.progress_bar.setRange(0, max(total, 1))
            self.progress_bar.setValue(total)
        else:
            self.progress_label.setText(t("conflicts.progress_resolving", done=done, total=total))
            self.progress_bar.setRange(0, max(total, 1))
            self.progress_bar.setValue(min(done, total))
        self.progress_label.setToolTip(path)
        self.progress_row.show()

    def _keep_selected(self) -> None:
        group = self._selected_group()
        chosen = self._selected_candidate()
        local_root = self.cfg.local_root()
        if group is None or chosen is None or not local_root:
            return
        if QMessageBox.question(
            self, t("conflicts.confirm_title"),
            t("conflicts.confirm_body", path=self._relative(chosen)),
        ) != QMessageBox.StandardButton.Yes:
            return
        remote_paths = self._candidate_relatives(group)
        self._set_action_busy(True, t("conflicts.resolving"))

        def worker(progress):
            return self._resolve_and_purge([(group, chosen, remote_paths)], local_root, progress)

        run_in_background(
            self, "_conflict_action_call", worker, self._on_action_done,
            on_progress=self._on_action_progress,
        )

    def _discard_copies(self) -> None:
        group = self._selected_group()
        local_root = self.cfg.local_root()
        if group is None or not local_root:
            return
        if QMessageBox.question(
            self, t("conflicts.discard_confirm_title"),
            t("conflicts.discard_confirm_body", count=len(group.candidates), path=self._relative(group.original)),
        ) != QMessageBox.StandardButton.Yes:
            return
        remote_paths = self._candidate_relatives(group)
        self._set_action_busy(True, t("conflicts.resolving"))

        def worker(progress):
            return self._discard_and_purge(group, remote_paths, local_root, progress)

        run_in_background(
            self, "_conflict_action_call", worker, self._on_action_done,
            on_progress=self._on_action_progress,
        )

    def _visible_groups(self) -> list[conflicts.ConflictGroup]:
        """Groups currently listed, i.e. after the search filter."""
        groups: list[conflicts.ConflictGroup] = []
        for index in range(self.tree.topLevelItemCount()):
            group = self.tree.topLevelItem(index).data(0, GROUP_ROLE)
            if group is not None and group not in groups:
                groups.append(group)
        return groups

    def _resolve_everything(self) -> None:
        self._resolve_groups(
            self._visible_groups(),
            "conflicts.resolve_everything_confirm_title",
            "conflicts.resolve_everything_confirm_body",
        )

    def _resolve_selected_keep_original(self) -> None:
        self._resolve_groups(
            self._selected_groups(),
            "conflicts.resolve_all_confirm_title",
            "conflicts.resolve_all_confirm_body",
        )

    def _resolve_groups(self, groups: list[conflicts.ConflictGroup], title_key: str, body_key: str) -> None:
        local_root = self.cfg.local_root()
        if not groups or not local_root:
            return
        if QMessageBox.question(
            self, t(title_key), t(body_key, count=len(groups)),
        ) != QMessageBox.StandardButton.Yes:
            return
        plans: list[tuple[conflicts.ConflictGroup, Path, list[str]]] = []
        for group in groups:
            info = self._infos.get(str(group.original)) or self._group_info(group)
            chosen = group.original if group.original.exists() else info.recommended
            if chosen is None:
                continue
            plans.append((group, chosen, self._candidate_relatives(group)))
        if not plans:
            QMessageBox.warning(
                self, t("conflicts.resolve_failed_title"), t("conflicts.no_selection"),
            )
            return
        self._set_action_busy(True, t("conflicts.resolving"))

        def worker(progress):
            return self._resolve_and_purge(plans, local_root, progress)

        run_in_background(
            self, "_conflict_action_call", worker, self._on_action_done,
            on_progress=self._on_action_progress,
        )

    def _resolve_and_purge(self, plans, local_root: str, progress=None):
        """Run in a BackgroundCall: acquire the local gate, resolve, purge."""
        problems: list[str] = []
        resolved = 0
        total_groups = len(plans)
        scheduler = getattr(self.engine, "transfer_scheduler", None)
        permit = scheduler.permit("conflict") if scheduler is not None else None
        try:
            _report_progress(progress, "waiting")
            if permit is not None and not permit.acquire(timeout=300):
                return {
                    "ok": False, "resolved": 0,
                    "problems": [t("conflicts.scheduler_timeout")],
                }
            pending_remote: list[str] = []
            for index, (group, chosen, remote_paths) in enumerate(plans, start=1):
                ok, detail = conflicts.resolve_conflict(
                    group, chosen, local_root, self.engine.sync_state,
                )
                if not ok:
                    problems.append(f"{self._relative(group.original)}: {detail}")
                else:
                    resolved += 1
                    pending_remote.extend(remote_paths)
                _report_progress(
                    progress, "resolving", index, total_groups,
                    self._relative(group.original),
                )
            if pending_remote:
                # One lease for the whole batch: "Risolvi tutto" can touch
                # dozens of groups, and a lock handshake per group is slow.
                relatives = list(dict.fromkeys(pending_remote))
                _report_progress(progress, "cleaning", 0, len(relatives))
                problems.extend(self._purge_remote(relatives, progress=progress))
            if resolved:
                self.engine.wake()
            _report_progress(progress, "done", total_groups, total_groups)
            return {"ok": resolved > 0, "resolved": resolved, "problems": problems}
        finally:
            if permit is not None:
                permit.release()

    def _discard_and_purge(self, group, remote_paths, local_root: str, progress=None):
        problems: list[str] = []
        scheduler = getattr(self.engine, "transfer_scheduler", None)
        permit = scheduler.permit("conflict") if scheduler is not None else None
        try:
            _report_progress(progress, "waiting")
            if permit is not None and not permit.acquire(timeout=300):
                return {"ok": False, "resolved": 0, "problems": [t("conflicts.scheduler_timeout")]}
            relative = self._relative(group.original)
            _report_progress(progress, "resolving", 0, 1, relative)
            ok, detail = conflicts.discard_conflict_copies(
                group, local_root, self.engine.sync_state,
            )
            if not ok:
                return {"ok": False, "resolved": 0, "problems": [detail]}
            _report_progress(progress, "resolving", 1, 1, relative)
            if remote_paths:
                _report_progress(progress, "cleaning", 0, len(remote_paths))
            problems.extend(self._purge_remote(remote_paths, progress=progress))
            self.engine.wake()
            _report_progress(progress, "done", 1, 1)
            return {"ok": True, "resolved": 1, "problems": problems}
        finally:
            if permit is not None:
                permit.release()

    def _purge_remote(self, relatives: list[str], progress=None) -> list[str]:
        """Remove conflict copies from the NAS too, under a single lease."""
        if not relatives:
            return []
        conn = getattr(self.engine, "connection", None)
        if conn is None:
            conn = rsync_ops.resolve_connection(self.cfg)
        if conn is None:
            return [t("conflicts.remote_unreachable")]
        device_id = self.engine.sync_state.device_id()

        def on_remote_progress(done: int, total: int, path: str, _ok: bool) -> None:
            _report_progress(progress, "cleaning", done, total, path)

        results = rsync_ops.browse_delete_many(
            self.cfg, conn, relatives, device_id,
            on_progress=on_remote_progress if progress is not None else None,
        )
        problems: list[str] = []
        for relative, ok, detail in results:
            if ok:
                continue
            if "non trovato" in (detail or "").lower() or "not found" in (detail or "").lower():
                continue
            problems.append(f"{relative}: {detail}")
        return problems

    def _on_action_done(self, result, exc: Exception | None) -> None:
        self._set_action_busy(False)
        if exc is not None:
            self.status_label.setText(t("conflicts.resolve_failed_detail", detail=str(exc)))
            self._update_buttons()
            self.refresh()
            return
        result = result or {}
        problems = result.get("problems") or []
        resolved = int(result.get("resolved") or 0)
        if resolved and not problems:
            self.status_label.setText(t("conflicts.resolve_done_body"))
        elif resolved:
            self.status_label.setText(t(
                "conflicts.partial_done", resolved=resolved, problems=len(problems),
            ))
        else:
            self.status_label.setText(t("conflicts.resolve_failed_title"))
        if problems:
            shown = "\n".join(problems[:10])
            if len(problems) > 10:
                shown += f"\n… (+{len(problems) - 10})"
            self.status_label.setText(t("conflicts.remote_cleanup_failed_body", detail=shown))
            self.status_label.setToolTip(shown)
        else:
            self.status_label.setToolTip("")
        self._update_buttons()
        self.refresh()

    def _candidate_relatives(self, group: conflicts.ConflictGroup) -> list[str]:
        return [self._relative(path) for path in group.candidates]

    def _relative(self, path: Path) -> str:
        try:
            return str(path.relative_to(self.cfg.local_root()))
        except ValueError:
            return str(path)

    def _restore_version(self) -> None:
        group = self._selected_group()
        local_root = self.cfg.local_root()
        if group is None or not local_root:
            return
        relative = self._relative(group.original)
        dialog = FileVersionDialog(
            self, self.cfg, self.engine, relative, local_root,
            on_restored=lambda: self._resolve_after_restore(group, local_root, relative),
        )
        dialog.exec()

    def _resolve_after_restore(self, group, local_root: str, relative: str) -> None:
        remote_paths = self._candidate_relatives(group)
        self._set_action_busy(True, t("conflicts.resolving"))

        def worker(progress):
            scheduler = getattr(self.engine, "transfer_scheduler", None)
            permit = scheduler.permit("conflict") if scheduler is not None else None
            try:
                _report_progress(progress, "waiting")
                if permit is not None and not permit.acquire(timeout=300):
                    return {"ok": False, "resolved": 0, "problems": [t("conflicts.scheduler_timeout")]}
                _report_progress(progress, "resolving", 0, 1, relative)
                ok, detail = conflicts.resolve_conflict(
                    group, group.original, local_root, self.engine.sync_state,
                )
                if not ok:
                    return {"ok": False, "resolved": 0, "problems": [detail]}
                try:
                    self.engine.sync_state.mark_pending({relative})
                except Exception:
                    pass
                _report_progress(progress, "resolving", 1, 1, relative)
                if remote_paths:
                    _report_progress(progress, "cleaning", 0, len(remote_paths))
                problems = self._purge_remote(remote_paths, progress=progress)
                self.engine.wake()
                _report_progress(progress, "done", 1, 1)
                return {"ok": True, "resolved": 1, "problems": problems}
            finally:
                if permit is not None:
                    permit.release()

        run_in_background(
            self, "_conflict_action_call", worker, self._on_action_done,
            on_progress=self._on_action_progress,
        )
