from __future__ import annotations

import difflib
import time
from dataclasses import dataclass
from pathlib import Path

from PyQt6.QtCore import Qt, QTimer, QUrl, pyqtSignal
from PyQt6.QtGui import QColor, QDesktopServices
from PyQt6.QtWidgets import (
    QAbstractItemView, QComboBox, QGroupBox, QHBoxLayout, QHeaderView, QLabel,
    QLineEdit, QMessageBox, QPlainTextEdit, QPushButton, QSplitter,
    QTreeWidget, QTreeWidgetItem, QVBoxLayout, QWidget,
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
        self.tree.header().setStretchLastSection(False)
        self.tree.header().setSectionResizeMode(0, QHeaderView.ResizeMode.Stretch)
        self.tree.header().setSectionResizeMode(1, QHeaderView.ResizeMode.ResizeToContents)
        self.tree.header().setSectionResizeMode(2, QHeaderView.ResizeMode.ResizeToContents)
        self.tree.header().setSectionResizeMode(3, QHeaderView.ResizeMode.ResizeToContents)
        self.tree.header().setSectionResizeMode(4, QHeaderView.ResizeMode.ResizeToContents)
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
            item = QTreeWidgetItem([
                relative,
                str(len(group.candidates)),
                human_size(info.total_alt_size),
                time.strftime("%Y-%m-%d %H:%M", time.localtime(info.newest_mtime)) if info.newest_mtime else "—",
                info.origin_device or "—",
            ])
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
        self.open_btn.setEnabled(selected and can_act)
        self.keep_btn.setEnabled(selected and can_act)
        self.restore_btn.setEnabled(self._selected_group() is not None and can_act)
        self.discard_btn.setEnabled(self._selected_group() is not None and can_act)
        self.resolve_all_btn.setEnabled(bool(self._selected_groups()) and can_act)
        self.search_edit.setEnabled(can_act)
        self.refresh_btn.setEnabled(not self._busy and not self._action_busy)

    def _open_selected(self) -> None:
        path = self._selected_candidate()
        if path is not None:
            QDesktopServices.openUrl(QUrl.fromLocalFile(str(path)))

    # --- actions (always off the GUI thread) ---

    def _set_action_busy(self, busy: bool, message: str = "") -> None:
        self._action_busy = busy
        if message:
            self.status_label.setText(message)
        self._update_buttons()

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

        def worker():
            return self._resolve_and_purge([(group, chosen, remote_paths)], local_root)

        run_in_background(self, "_conflict_action_call", worker, self._on_action_done)

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

        def worker():
            return self._discard_and_purge(group, remote_paths, local_root)

        run_in_background(self, "_conflict_action_call", worker, self._on_action_done)

    def _resolve_selected_keep_original(self) -> None:
        groups = self._selected_groups()
        local_root = self.cfg.local_root()
        if not groups or not local_root:
            return
        if QMessageBox.question(
            self, t("conflicts.resolve_all_confirm_title"),
            t("conflicts.resolve_all_confirm_body", count=len(groups)),
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

        def worker():
            return self._resolve_and_purge(plans, local_root)

        run_in_background(self, "_conflict_action_call", worker, self._on_action_done)

    def _resolve_and_purge(self, plans, local_root: str):
        """Run in a BackgroundCall: acquire the local gate, resolve, purge."""
        problems: list[str] = []
        resolved = 0
        scheduler = getattr(self.engine, "transfer_scheduler", None)
        permit = scheduler.permit("conflict") if scheduler is not None else None
        try:
            if permit is not None and not permit.acquire(timeout=300):
                return {
                    "ok": False, "resolved": 0,
                    "problems": [t("conflicts.scheduler_timeout")],
                }
            for group, chosen, remote_paths in plans:
                ok, detail = conflicts.resolve_conflict(
                    group, chosen, local_root, self.engine.sync_state,
                )
                if not ok:
                    problems.append(f"{self._relative(group.original)}: {detail}")
                    continue
                resolved += 1
                problems.extend(self._purge_remote(remote_paths))
            if resolved:
                self.engine.wake()
            return {"ok": resolved > 0, "resolved": resolved, "problems": problems}
        finally:
            if permit is not None:
                permit.release()

    def _discard_and_purge(self, group, remote_paths, local_root: str):
        problems: list[str] = []
        scheduler = getattr(self.engine, "transfer_scheduler", None)
        permit = scheduler.permit("conflict") if scheduler is not None else None
        try:
            if permit is not None and not permit.acquire(timeout=300):
                return {"ok": False, "resolved": 0, "problems": [t("conflicts.scheduler_timeout")]}
            ok, detail = conflicts.discard_conflict_copies(
                group, local_root, self.engine.sync_state,
            )
            if not ok:
                return {"ok": False, "resolved": 0, "problems": [detail]}
            problems.extend(self._purge_remote(remote_paths))
            self.engine.wake()
            return {"ok": True, "resolved": 1, "problems": problems}
        finally:
            if permit is not None:
                permit.release()

    def _purge_remote(self, relatives: list[str]) -> list[str]:
        """Remove conflict copies from the NAS too, under a single lease."""
        if not relatives:
            return []
        conn = getattr(self.engine, "connection", None)
        if conn is None:
            conn = rsync_ops.resolve_connection(self.cfg)
        if conn is None:
            return [t("conflicts.remote_unreachable")]
        device_id = self.engine.sync_state.device_id()
        results = rsync_ops.browse_delete_many(self.cfg, conn, relatives, device_id)
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

        def worker():
            scheduler = getattr(self.engine, "transfer_scheduler", None)
            permit = scheduler.permit("conflict") if scheduler is not None else None
            try:
                if permit is not None and not permit.acquire(timeout=300):
                    return {"ok": False, "resolved": 0, "problems": [t("conflicts.scheduler_timeout")]}
                ok, detail = conflicts.resolve_conflict(
                    group, group.original, local_root, self.engine.sync_state,
                )
                if not ok:
                    return {"ok": False, "resolved": 0, "problems": [detail]}
                try:
                    self.engine.sync_state.mark_pending({relative})
                except Exception:
                    pass
                problems = self._purge_remote(remote_paths)
                self.engine.wake()
                return {"ok": True, "resolved": 1, "problems": problems}
            finally:
                if permit is not None:
                    permit.release()

        run_in_background(self, "_conflict_action_call", worker, self._on_action_done)
