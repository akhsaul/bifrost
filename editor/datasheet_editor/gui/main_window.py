"""Main editor window."""

from __future__ import annotations

import json
from pathlib import Path
from typing import Any

from PySide6.QtCore import QSettings, Qt, QThreadPool, QTimer, Signal
from PySide6.QtGui import QAction, QKeySequence
from PySide6.QtWidgets import (
    QAbstractItemView,
    QComboBox,
    QDialog,
    QFileDialog,
    QFormLayout,
    QGroupBox,
    QHBoxLayout,
    QHeaderView,
    QLabel,
    QLineEdit,
    QMainWindow,
    QMessageBox,
    QPlainTextEdit,
    QProgressBar,
    QProgressDialog,
    QPushButton,
    QScrollArea,
    QSplitter,
    QTabWidget,
    QTableView,
    QVBoxLayout,
    QWidget,
)

from ..dataset import write_json_atomic
from ..fields import is_cost_field
from ..merge import PARAMS_FIELD, ParamArrayMode, PricingFieldPolicy, merge
from ..validate import validate_overlay
from .adddialogs import AddFieldDialog, AddModelDialog
from .fieldeditor import FieldRow
from .models import (
    ChangeListModel,
    ModelFilterProxy,
    ModelListModel,
    ParamDescriptorModel,
    build_rows,
)
from .workers import BUILD_STEP, TOTAL_STEPS, LoadWorker, MergeWorker, SaveWorker

PARAMETERS_FILENAME = "model_parameters.json"
PRICING_FILENAME = "model_pricing.json"


class MainWindow(QMainWindow):
    def __init__(self) -> None:
        super().__init__()
        self.setWindowTitle("Bifrost Datasheet Editor")
        self.resize(1500, 900)
        self.settings = QSettings("bifrost", "datasheet-editor")

        self.parameters_path: Path | None = None
        self.pricing_path: Path | None = None
        self.custom_path: Path | None = None
        self.output_dir: Path = Path.cwd()

        self.parameters: dict[str, dict[str, Any]] = {}
        self.pricing: dict[str, dict[str, Any]] = {}
        self.overlay: dict[str, dict[str, Any]] = {}
        self.conflicts: dict[str, list[str]] = {}
        self.merge_result = None
        self.dirty = False
        self._loading = False
        self._load_worker: LoadWorker | None = None
        self._progress_dialog: QProgressDialog | None = None

        self.model_list = ModelListModel(self)
        self.proxy = ModelFilterProxy(self)
        self.proxy.setSourceModel(self.model_list)
        self.param_model = ParamDescriptorModel(self)
        self.change_model = ChangeListModel(self)

        self._build_ui()
        self._wire()
        self._update_path_label()

        self.pool = QThreadPool.globalInstance()
        self.search_timer = QTimer(self)
        self.search_timer.setSingleShot(True)
        self.search_timer.setInterval(200)
        self.search_timer.timeout.connect(self._apply_search)

    # ---------------------------------------------------------------- UI -- #

    def _build_ui(self) -> None:
        central = QWidget()
        self.setCentralWidget(central)
        root = QVBoxLayout(central)
        root.setContentsMargins(8, 8, 8, 8)
        root.setSpacing(6)

        root.addLayout(self._build_toolbar())
        root.addLayout(self._build_filterbar())

        splitter = QSplitter(Qt.Horizontal)
        splitter.addWidget(self._build_list_pane())
        splitter.addWidget(self._build_editor_pane())
        splitter.setStretchFactor(0, 3)
        splitter.setStretchFactor(1, 4)
        splitter.setSizes([620, 780])
        root.addWidget(splitter, 1)

        root.addWidget(self._build_preview_pane())

        self.status_label = QLabel("no files loaded")
        self.progress = QProgressBar()
        self.progress.setRange(0, 0)
        self.progress.setFixedWidth(140)
        self.progress.hide()
        status_row = QHBoxLayout()
        status_row.addWidget(self.status_label, 1)
        status_row.addWidget(self.progress)
        root.addLayout(status_row)

    def _build_toolbar(self) -> QHBoxLayout:
        bar = QHBoxLayout()
        self.path_label = QLabel()
        bar.addWidget(self.path_label, 1)

        # One button per file rather than a single "Load…". Each names the file
        # it wants, so it is obvious which of the three is still missing and the
        # merge cannot be started with the wrong one.
        self.btn_load_params = QPushButton("Load Parameters…")
        self.btn_load_params.setToolTip(
            f"Choose {PARAMETERS_FILENAME} — capability metadata and the "
            "model_parameters form descriptors"
        )
        self.btn_load_params.clicked.connect(self._on_load_parameters)
        bar.addWidget(self.btn_load_params)

        self.btn_load_pricing = QPushButton("Load Pricing…")
        self.btn_load_pricing.setToolTip(
            f"Choose {PRICING_FILENAME} — cost fields, also read by Bifrost's "
            "capability lookup"
        )
        self.btn_load_pricing.clicked.connect(self._on_load_pricing)
        bar.addWidget(self.btn_load_pricing)

        self.btn_load_custom = QPushButton("Load Custom…")
        self.btn_load_custom.setToolTip(
            "Choose custom_model_metadata.json — your overlay of edits. "
            "Optional; skip it to start from a clean slate."
        )
        self.btn_load_custom.clicked.connect(self._on_load_custom)
        bar.addWidget(self.btn_load_custom)

        self.btn_add_model = QPushButton("Add Model…")
        self.btn_add_model.setToolTip(
            "Add a model that does not exist yet, to your custom overlay"
        )
        self.btn_add_model.clicked.connect(self._on_add_model)
        bar.addWidget(self.btn_add_model)

        self.btn_merge = QPushButton("Merge & Preview")
        self.btn_merge.clicked.connect(self._on_merge)
        self.btn_merge.setEnabled(False)
        bar.addWidget(self.btn_merge)

        self.btn_save_output = QPushButton("Save Output…")
        self.btn_save_output.clicked.connect(self._on_save_output)
        self.btn_save_output.setEnabled(False)
        bar.addWidget(self.btn_save_output)

        self.btn_save_custom = QPushButton("Save Custom")
        self.btn_save_custom.clicked.connect(self._on_save_custom)
        self.btn_save_custom.setEnabled(False)
        bar.addWidget(self.btn_save_custom)

        return bar

    def _build_filterbar(self) -> QHBoxLayout:
        bar = QHBoxLayout()
        bar.setSpacing(8)

        self.search_edit = QLineEdit()
        self.search_edit.setPlaceholderText("Search models… (contains, case-insensitive)")
        self.search_edit.setClearButtonEnabled(True)
        self.search_edit.textChanged.connect(lambda _: self.search_timer.start())
        bar.addWidget(self.search_edit, 2)

        self.provider_combo = QComboBox()
        self.provider_combo.setMinimumWidth(220)
        self.provider_combo.currentIndexChanged.connect(self._on_provider_changed)
        bar.addWidget(self.provider_combo)

        self.mode_combo = QComboBox()
        self.mode_combo.setMinimumWidth(170)
        self.mode_combo.currentIndexChanged.connect(self._on_mode_changed)
        bar.addWidget(self.mode_combo)

        self.btn_clear = QPushButton("Clear filters")
        self.btn_clear.clicked.connect(self._clear_filters)
        self.btn_clear.setEnabled(False)
        bar.addWidget(self.btn_clear)

        focus_search = QAction("Focus search", self)
        focus_search.setShortcut(QKeySequence("Ctrl+F"))
        focus_search.triggered.connect(lambda: (self.search_edit.setFocus(), self.search_edit.selectAll()))
        self.addAction(focus_search)

        clear_search = QAction("Clear search", self)
        clear_search.setShortcut(QKeySequence("Esc"))
        clear_search.triggered.connect(self._on_escape)
        self.addAction(clear_search)

        return bar

    def _build_list_pane(self) -> QWidget:
        pane = QWidget()
        layout = QVBoxLayout(pane)
        layout.setContentsMargins(0, 0, 0, 0)
        layout.addWidget(QLabel("Models"))
        self.table = QTableView()
        self.table.setModel(self.proxy)
        self.table.setSelectionBehavior(QAbstractItemView.SelectRows)
        self.table.setSelectionMode(QAbstractItemView.SingleSelection)
        self.table.setSortingEnabled(True)
        self.table.verticalHeader().setVisible(False)
        self.table.horizontalHeader().setSectionResizeMode(QHeaderView.Interactive)
        self.table.setColumnWidth(0, 330)
        self.table.setColumnWidth(1, 110)
        self.table.setColumnWidth(2, 130)
        self.table.setColumnWidth(3, 150)
        self.table.setColumnWidth(4, 90)
        layout.addWidget(self.table, 1)
        return pane

    def _build_editor_pane(self) -> QWidget:
        self.tabs = QTabWidget()

        params_page = QWidget()
        self.params_layout = QVBoxLayout(params_page)
        self.params_layout.setContentsMargins(6, 6, 6, 6)

        # Entries carry up to 48 fields, so both panes scroll inside a splitter
        # rather than squeezing each other to nothing.
        params_splitter = QSplitter(Qt.Vertical)

        self.params_fields_box = QGroupBox("Parameter fields")
        params_box_layout = QVBoxLayout(self.params_fields_box)
        # Above the form, not below: an entry can have 48 fields, and a button
        # pinned to the bottom of the scroll area is effectively invisible.
        self.btn_add_param_field = QPushButton("Add Parameter Field…")
        self.btn_add_param_field.clicked.connect(lambda: self._on_add_field("parameters"))
        params_box_layout.addWidget(self.btn_add_param_field, 0, Qt.AlignLeft)
        self.params_form = QFormLayout()
        self.params_form.setFieldGrowthPolicy(QFormLayout.AllNonFixedFieldsGrow)
        params_box_layout.addLayout(self.params_form)
        params_scroll = QScrollArea()
        params_scroll.setWidgetResizable(True)
        params_scroll.setMinimumHeight(240)
        params_scroll.setWidget(self.params_fields_box)
        params_splitter.addWidget(params_scroll)

        self.param_array_box = QGroupBox("model_parameters  (merged by id)")
        array_layout = QVBoxLayout(self.param_array_box)
        self.param_table = QTableView()
        self.param_table.setModel(self.param_model)
        self.param_table.verticalHeader().setVisible(False)
        self.param_table.horizontalHeader().setSectionResizeMode(QHeaderView.Interactive)
        self.param_table.setColumnWidth(0, 150)
        self.param_table.setColumnWidth(1, 150)
        self.param_table.setMinimumHeight(150)
        array_layout.addWidget(self.param_table)
        array_layout.addWidget(QLabel(
            "Editing a descriptor's default or range merges into the original by id; "
            "new ids are appended."
        ))
        params_splitter.addWidget(self.param_array_box)
        params_splitter.setStretchFactor(0, 3)
        params_splitter.setStretchFactor(1, 2)
        params_splitter.setSizes([340, 260])
        self.params_layout.addWidget(params_splitter, 1)
        self.tabs.addTab(params_page, "Parameters")

        pricing_page = QWidget()
        pricing_layout = QVBoxLayout(pricing_page)
        pricing_layout.setContentsMargins(6, 6, 6, 6)
        self.pricing_hint = QLabel(
            "Bifrost's datasheet.Entry reads capability fields from this file "
            "(GetCapabilityEntry), so pricing edits here drive server-side behavior."
        )
        self.pricing_hint.setWordWrap(True)
        self.pricing_hint.setStyleSheet("color:#475569;")
        pricing_layout.addWidget(self.pricing_hint)
        self.pricing_fields_box = QGroupBox("Pricing fields")
        pricing_box_layout = QVBoxLayout(self.pricing_fields_box)
        self.btn_add_pricing_field = QPushButton("Add Pricing Field…")
        self.btn_add_pricing_field.clicked.connect(lambda: self._on_add_field("pricing"))
        pricing_box_layout.addWidget(self.btn_add_pricing_field, 0, Qt.AlignLeft)
        self.pricing_form = QFormLayout()
        self.pricing_form.setFieldGrowthPolicy(QFormLayout.AllNonFixedFieldsGrow)
        pricing_box_layout.addLayout(self.pricing_form)
        pricing_scroll = QScrollArea()
        pricing_scroll.setWidgetResizable(True)
        pricing_scroll.setWidget(self.pricing_fields_box)
        pricing_layout.addWidget(pricing_scroll, 1)
        self.tabs.addTab(pricing_page, "Pricing")

        # A QGroupBox used directly as a tab page sizes to its hint and ends up
        # vertically centred, leaving dead space above and below. Wrapping it in
        # a plain page with a filling layout makes it occupy the whole tab.
        conflicts_page = QWidget()
        conflicts_layout = QVBoxLayout(conflicts_page)
        conflicts_layout.setContentsMargins(6, 6, 6, 6)
        self.conflict_hint = QLabel(
            "The same capability facts are maintained in both files. Bifrost's "
            "GetCapabilityEntry reads the pricing copy, so pricing is what drives "
            "server behaviour; the parameters copy is what the UI builds forms from. "
            "Neither is treated as authoritative here."
        )
        self.conflict_hint.setWordWrap(True)
        self.conflict_hint.setStyleSheet("color:#475569;")
        conflicts_layout.addWidget(self.conflict_hint)
        self.conflict_box = QGroupBox("Disagreements")
        self.conflict_layout = QVBoxLayout(self.conflict_box)
        self.conflict_text = QPlainTextEdit()
        self.conflict_text.setReadOnly(True)
        self.conflict_text.setLineWrapMode(QPlainTextEdit.NoWrap)
        self.conflict_layout.addWidget(self.conflict_text, 1)
        conflicts_layout.addWidget(self.conflict_box, 1)
        self._conflicts_tab = self.tabs.addTab(conflicts_page, "Conflicts")

        return self.tabs

    def _build_preview_pane(self) -> QWidget:
        box = QGroupBox("Merge preview")
        layout = QVBoxLayout(box)
        layout.setContentsMargins(6, 6, 6, 6)
        self.preview_table = QTableView()
        self.preview_table.setModel(self.change_model)
        self.preview_table.setMaximumHeight(190)
        self.preview_table.verticalHeader().setVisible(False)
        self.preview_table.setColumnWidth(0, 300)
        self.preview_table.setColumnWidth(1, 90)
        self.preview_table.setColumnWidth(2, 210)
        layout.addWidget(self.preview_table)
        return box

    def _wire(self) -> None:
        self.table.selectionModel().selectionChanged.connect(self._on_selection_changed)
        self.param_table.doubleClicked.connect(self._on_param_double_clicked)

    # ------------------------------------------------------------ loading -- #

    def load_paths(self, params: str | Path, pricing: str | Path, custom: str | Path | None) -> None:
        """Load all three at once. Only used for explicit CLI arguments."""
        self.parameters_path = Path(params)
        self.pricing_path = Path(pricing)
        self.custom_path = Path(custom) if custom else None
        self._update_path_label()
        self._start_load()

    # -- one button per file --------------------------------------------- #

    def show_start_hint(self) -> None:
        """Explain what to load first, instead of opening any file on our own."""
        self._update_path_label()
        self.status_label.setText(
            f"Choose {PARAMETERS_FILENAME} and {PRICING_FILENAME} with the two "
            "Load buttons above. Custom overlay is optional."
        )

    def _on_load_parameters(self) -> None:
        chosen, _ = QFileDialog.getOpenFileName(
            self, f"Select {PARAMETERS_FILENAME}", self._browse_dir(), "JSON files (*.json)"
        )
        if not chosen:
            return
        self.parameters_path = Path(chosen)
        self._update_path_label()
        self._start_load()

    def _on_load_pricing(self) -> None:
        chosen, _ = QFileDialog.getOpenFileName(
            self, f"Select {PRICING_FILENAME}", self._browse_dir(), "JSON files (*.json)"
        )
        if not chosen:
            return
        self.pricing_path = Path(chosen)
        self._update_path_label()
        self._start_load()

    def _on_load_custom(self) -> None:
        chosen, _ = QFileDialog.getOpenFileName(
            self,
            "Select custom_model_metadata.json (optional)",
            self._browse_dir(),
            "JSON files (*.json)",
        )
        if not chosen:
            return
        self.custom_path = Path(chosen)
        self._update_path_label()
        self._start_load()

    def _browse_dir(self) -> str:
        for path in (self.parameters_path, self.pricing_path, self.custom_path):
            if path is not None:
                return str(path.parent)
        return str(self.settings.value("last_dir", str(Path.cwd())))

    def _start_load(self) -> None:
        """Load if both originals are chosen; otherwise say what is missing."""
        missing = [
            name
            for name, path in (
                (PARAMETERS_FILENAME, self.parameters_path),
                (PRICING_FILENAME, self.pricing_path),
            )
            if path is None
        ]
        if missing:
            self._update_path_label()
            self.btn_merge.setEnabled(False)
            # A status-bar line is easy to miss, and the result of clicking one
            # Load button and seeing nothing happen looks broken. Say it plainly.
            self._warn_incomplete_load(missing)
            return

        self._remember_dir()
        self._set_busy(True, "loading…")
        self._show_progress_dialog()
        worker = LoadWorker(self.parameters_path, self.pricing_path, self.custom_path)
        self._load_worker = worker
        worker.signals.progress.connect(self._on_load_progress)
        worker.signals.finished.connect(self._on_loaded)
        worker.signals.failed.connect(lambda msg: self._on_failed("load", msg))
        worker.signals.cancelled.connect(self._on_load_cancelled)
        self.pool.start(worker)

    def _warn_incomplete_load(self, missing: list[str]) -> None:
        """Explain that both originals are required before anything can display."""
        chosen = [
            name
            for name, path in (
                (PARAMETERS_FILENAME, self.parameters_path),
                (PRICING_FILENAME, self.pricing_path),
            )
            if path is not None
        ]
        lines = [
            "Bifrost's datasheet editor needs both files before it can show anything.",
            "",
            f"Chosen so far : {', '.join(chosen) if chosen else 'none'}",
            f"Still needed  : {', '.join(missing)}",
            "",
            "This is not just a convenience: model_parameters.json holds the capability "
            "metadata and parameter forms, while model_pricing.json holds the cost fields "
            "and is also what Bifrost's GetCapabilityEntry reads. The editor lists both "
            "side by side and reports where they disagree, so it needs both.",
            "",
            f"Press \"Load Parameters…\" and \"Load Pricing…\" to choose the missing "
            f"{'file' if len(missing) == 1 else 'files'}.",
        ]
        QMessageBox.information(self, "Choose both files to load", "\n".join(lines))
        self.status_label.setText("Waiting for " + " and ".join(missing))

    def _remember_dir(self) -> None:
        for path in (self.parameters_path, self.pricing_path):
            if path is not None:
                self.settings.setValue("last_dir", str(path.parent))
                break

    # -- progress dialog -------------------------------------------------- #

    def _show_progress_dialog(self) -> None:
        dialog = QProgressDialog("Starting…", "Cancel", 0, TOTAL_STEPS, self)
        dialog.setWindowTitle("Loading datasheets")
        dialog.setWindowModality(Qt.WindowModal)
        dialog.setMinimumDuration(0)
        dialog.setAutoClose(False)
        dialog.setAutoReset(False)
        dialog.setValue(0)
        dialog.canceled.connect(self._cancel_load)
        self._progress_dialog = dialog
        dialog.show()

    def _on_load_progress(self, label: str, value: int, total: int) -> None:
        dialog = self._progress_dialog
        if dialog is None:
            return
        dialog.setLabelText(label)
        dialog.setMaximum(total)
        dialog.setValue(value)

    def _cancel_load(self) -> None:
        worker = self._load_worker
        if worker is not None:
            worker.cancel()
        self.status_label.setText("cancelling…")

    def _close_progress_dialog(self) -> None:
        dialog = self._progress_dialog
        self._progress_dialog = None
        if dialog is not None:
            dialog.close()
            dialog.deleteLater()

    def _on_load_cancelled(self) -> None:
        self._close_progress_dialog()
        self._load_worker = None
        self._set_busy(False)
        self.status_label.setText("load cancelled")

    def _on_loaded(self, payload: dict[str, Any]) -> None:
        self.parameters = payload["parameters"].data
        self.pricing = payload["pricing"].data
        self.overlay = payload["overlay"]
        self.conflicts = payload["conflicts"]

        rows = build_rows(self.parameters, self.pricing, self.overlay, self.conflicts)
        # "Done" is claimed only once the list is actually on screen. Doing this
        # work first and labelling it keeps the progress dialog honest: reaching
        # 100% while the window is still empty (and unresponsive) is worse than
        # no bar at all.
        self._on_load_progress("Building model list…", BUILD_STEP, TOTAL_STEPS)
        self._populate_rows(rows)
        self._close_progress_dialog()
        self._load_worker = None
        self._update_path_label()
        self._set_busy(False)
        self.status_label.setText(
            f"{len(rows):,} models · {len(self.overlay):,} custom entries · "
            f"{payload['conflict_count']:,} cross-file disagreement(s)"
        )
        if rows:
            self.table.selectRow(0)
        self._on_load_progress("Done", TOTAL_STEPS, TOTAL_STEPS)
        if self.conflicts:
            self.conflict_text.setPlainText(self._conflict_summary())
            self.tabs.setTabText(self._conflicts_tab, f"Conflicts ({len(self.conflicts):,})")
        else:
            self.tabs.setTabText(self._conflicts_tab, "Conflicts")
        self.btn_merge.setEnabled(True)

    def _conflict_summary(self) -> str:
        from ..format import explain

        total = sum(len(v) for v in self.conflicts.values())
        lines = [
            f"{len(self.conflicts):,} models, {total:,} field value(s) disagree.",
            "",
        ]
        for model in sorted(self.conflicts):
            lines.append(model)
            for name in self.conflicts[model]:
                p = self.parameters.get(model, {}).get(name)
                q = self.pricing.get(model, {}).get(name)
                lines.append(f"    {name}")
                lines.append(f"        parameters : {explain(name, p)}")
                lines.append(f"        pricing    : {explain(name, q)}")
        return "\n".join(lines)

    def _populate_facet_combos(self) -> None:
        for combo, items, all_label in (
            (self.provider_combo, self.model_list.providers, "All providers"),
            (self.mode_combo, self.model_list.modes, "All modes"),
        ):
            combo.blockSignals(True)
            combo.clear()
            combo.addItem(all_label, None)
            for name, count in items:
                combo.addItem(f"{name} ({count:,})", name)
            combo.blockSignals(False)

    def _update_path_label(self) -> None:
        """Show which of the three files is chosen and which is still missing."""
        parts = []
        for title, path, required in (
            ("Parameters", self.parameters_path, True),
            ("Pricing", self.pricing_path, True),
            ("Custom", self.custom_path, False),
        ):
            if path is not None:
                parts.append(f"<b>{title}</b>: {path.name}")
            elif required:
                parts.append(f"<b>{title}</b>: <span style='color:#b91c1c'>not chosen</span>")
            else:
                parts.append(f"<b>{title}</b>: <span style='color:#64748b'>optional, not chosen</span>")
        self.path_label.setText("   ".join(parts))

    def _set_busy(self, busy: bool, message: str = "") -> None:
        """Toggle button availability.

        Actions are gated on data actually being present, not merely on paths
        having been chosen -- a cancelled load leaves both paths set but nothing
        loaded, and must not leave Merge clickable.
        """
        self._loading = busy
        self.progress.setVisible(busy)
        for button in (
            self.btn_load_params,
            self.btn_load_pricing,
            self.btn_load_custom,
        ):
            button.setEnabled(not busy)
        has_data = bool(self.model_list.rowCount())
        selected = bool(self._selected_model())
        self.btn_add_model.setEnabled(not busy and has_data)
        self.btn_add_param_field.setEnabled(not busy and selected)
        self.btn_add_pricing_field.setEnabled(not busy and selected)
        self.btn_merge.setEnabled(not busy and has_data)
        self.btn_save_output.setEnabled(not busy and self.merge_result is not None)
        self.btn_save_custom.setEnabled(not busy and bool(self.dirty or self.overlay))
        if busy:
            self.status_label.setText(message)

    def _on_failed(self, what: str, message: str) -> None:
        self._close_progress_dialog()
        self._load_worker = None
        self._set_busy(False)
        QMessageBox.critical(self, f"{what.capitalize()} failed", message)
        self.status_label.setText(f"{what} failed")

    # ------------------------------------------------------------ filters -- #

    def _apply_search(self) -> None:
        self.proxy.set_search(self.search_edit.text())
        self._update_filter_state()

    def _on_provider_changed(self, index: int) -> None:
        value = self.provider_combo.itemData(index)
        self.proxy.set_providers({value} if value else set())
        self._update_filter_state()

    def _on_mode_changed(self, index: int) -> None:
        value = self.mode_combo.itemData(index)
        self.proxy.set_modes({value} if value else set())
        self._update_filter_state()

    def _clear_filters(self) -> None:
        for widget in (self.search_edit, self.provider_combo, self.mode_combo):
            widget.blockSignals(True)
        self.search_edit.clear()
        self.provider_combo.setCurrentIndex(0)
        self.mode_combo.setCurrentIndex(0)
        for widget in (self.search_edit, self.provider_combo, self.mode_combo):
            widget.blockSignals(False)
        # One coalesced call: setting the three filters separately re-maps all
        # 12,595 rows three times.
        self.proxy.set_filters("", set(), set())
        self._update_filter_state()

    def _on_escape(self) -> None:
        if self.search_edit.text():
            self.search_edit.clear()
            self.search_timer.stop()
            self._apply_search()
        elif self.proxy.active:
            self._clear_filters()

    def _update_filter_state(self) -> None:
        self.btn_clear.setEnabled(self.proxy.active)
        shown = self.proxy.rowCount()
        total = self.model_list.rowCount()
        parts = [f"showing {shown:,} of {total:,} models"]
        if self.proxy.needle:
            parts.append(f'search "{self.search_edit.text().strip()}"')
        if self.proxy._providers:
            parts.append("provider=" + ",".join(sorted(self.proxy._providers)))
        if self.proxy._modes:
            parts.append("mode=" + ",".join(sorted(self.proxy._modes)))
        if shown != total:
            parts.append("(filtering is view-only — merge always covers every model)")
        self.status_label.setText(" · ".join(parts))

    # ------------------------------------------------------------ editing -- #

    def _selected_model(self) -> str | None:
        indexes = self.table.selectionModel().selectedRows()
        if not indexes:
            return None
        source = self.proxy.mapToSource(indexes[0])
        row = self.model_list.row(source.row())
        return row["id"] if row else None

    def _on_selection_changed(self, *_args: Any) -> None:
        model_id = self._selected_model()
        self._rebuild_field_panes(model_id)

    def _clear_form(self, form: QFormLayout) -> None:
        while form.count():
            item = form.takeAt(0)
            widget = item.widget()
            if widget is not None:
                widget.setParent(None)
                widget.deleteLater()

    def _rebuild_field_panes(self, model_id: str | None) -> None:
        self._clear_form(self.params_form)
        self._clear_form(self.pricing_form)
        self.param_model.set_items([])
        for button in (self.btn_add_param_field, self.btn_add_pricing_field):
            button.setEnabled(bool(model_id) and not self._loading)
        if not model_id:
            return

        params_entry = self.parameters.get(model_id) or {}
        pricing_entry = self.pricing.get(model_id) or {}
        overlay = self.overlay.get(model_id) or {}

        for section, base_entry, form in (
            ("parameters", params_entry, self.params_form),
            ("pricing", pricing_entry, self.pricing_form),
        ):
            names = list(base_entry)
            for name in overlay.get(section, {}):
                if name not in names:
                    names.append(name)
            if section == "pricing":
                names.sort(key=lambda n: (not is_cost_field(n), n))

            if not names:
                label = QLabel(f"no {section} fields for this model" if base_entry else
                               f"new {section} entry — add fields below" if section == "parameters"
                               else "no pricing entry (add one via Save Custom, or use CLI)")
                label.setStyleSheet("color:#64748b;font-style:italic;")
                form.addRow(label)
                continue

            for name in names:
                baseline = base_entry.get(name)
                if name in overlay.get(section, {}):
                    value = overlay[section][name]
                    origin = "overridden"
                elif baseline is None:
                    value = None
                    origin = "added"
                else:
                    value = baseline
                    origin = "original"
                if name == PARAMS_FIELD:
                    continue
                form.addRow(
                    FieldRow(
                        name,
                        value,
                        section=section,
                        origin=origin,
                        on_edit=lambda n, v, s=section: self._on_field_edit(model_id, s, n, v),
                        on_revert=lambda n, s=section: self._on_field_revert(model_id, s, n),
                    )
                )

        array = params_entry.get(PARAMS_FIELD) or overlay.get("parameters", {}).get(PARAMS_FIELD) or []
        if isinstance(array, list):
            self.param_model.set_items(array)
            self.param_array_box.setEnabled(True)
        else:
            self.param_array_box.setEnabled(False)

    # ------------------------------------------------------------ adding -- #

    def _require_loaded(self) -> bool:
        if self.model_list.rowCount():
            return True
        QMessageBox.information(
            self,
            "Nothing loaded yet",
            "Load both datasheets first — press \"Load Parameters…\" and "
            "\"Load Pricing…\".",
        )
        return False

    def _on_add_model(self) -> None:
        if not self._require_loaded():
            return
        existing = set(self.parameters) | set(self.pricing) | set(self.overlay)
        dialog = AddModelDialog(
            providers=self.model_list.providers and [p for p, _ in self.model_list.providers],
            modes=[m for m, _ in self.model_list.modes],
            existing_ids=existing,
            parent=self,
        )
        if dialog.exec() != QDialog.Accepted:
            return

        values = dialog.values()
        model_id = values["id"]
        self.overlay[model_id] = values["sections"]
        self.dirty = True
        self._refresh_rows()
        self._select_model(model_id)
        self._rebuild_field_panes(model_id)
        self.btn_save_custom.setEnabled(True)
        self.status_label.setText(
            f"Added {model_id} to the custom overlay. Save Custom to keep it."
        )

    def _on_add_field(self, section: str) -> None:
        if not self._require_loaded():
            return
        model_id = self._selected_model()
        if not model_id:
            QMessageBox.information(
                self, "No model selected", "Select a model in the list first."
            )
            return

        dataset = self.parameters if section == "parameters" else self.pricing
        base = dataset.get(model_id) or {}
        existing = set(base) | set(self.overlay.get(model_id, {}).get(section, {}))
        known = sorted({name for entry in dataset.values() for name in entry})
        dialog = AddFieldDialog(
            section=section,
            model_id=model_id,
            known_fields=known,
            existing=existing,
            parent=self,
        )
        if dialog.exec() != QDialog.Accepted:
            return

        name, value = dialog.values()
        self.overlay.setdefault(model_id, {}).setdefault(section, {})[name] = value
        self.dirty = True
        self._mark_row(model_id)
        self._rebuild_field_panes(model_id)
        self.btn_save_custom.setEnabled(True)
        self.status_label.setText(
            f"Added {model_id}.{section}.{name}. Save Custom to keep it."
        )

    def _populate_rows(self, rows: list[dict[str, Any]]) -> None:
        """Replace the model list, facets and filters in one pass.

        ``build_rows`` already emits models in ascending ID order, so no explicit
        sort is issued: forcing one costs a further full re-mapping pass, and the
        result is identical. Column sorting still works when the user clicks a
        header.
        """
        self.model_list.attach_proxy(self.proxy)
        self.model_list.reset_source(rows)
        self._populate_facet_combos()
        self._clear_filters()

    def _refresh_rows(self) -> None:
        self._populate_rows(build_rows(self.parameters, self.pricing, self.overlay, self.conflicts))

    def _select_model(self, model_id: str) -> None:
        for i in range(self.proxy.rowCount()):
            source = self.proxy.mapToSource(self.proxy.index(i, 0))
            row = self.model_list.row(source.row())
            if row and row["id"] == model_id:
                self.table.selectRow(i)
                return

    def _on_field_edit(self, model_id: str, section: str, name: str, value: Any) -> None:
        entry = self.overlay.setdefault(model_id, {})
        entry.setdefault(section, {})[name] = value
        self.dirty = True
        self.btn_save_custom.setEnabled(True)
        self._mark_row(model_id)

    def _on_field_revert(self, model_id: str, section: str, name: str) -> None:
        entry = self.overlay.get(model_id, {})
        section_data = entry.get(section)
        if isinstance(section_data, dict):
            section_data.pop(name, None)
            if not section_data:
                entry.pop(section, None)
        if entry and not entry:
            self.overlay.pop(model_id, None)
        self.dirty = True
        self._mark_row(model_id)
        self._rebuild_field_panes(model_id)

    def _mark_row(self, model_id: str) -> None:
        row_index = self.model_list.row_for_id(model_id)
        if row_index < 0:
            return
        row = self.model_list.row(row_index)
        if row is None:
            return
        known = model_id in self.parameters or model_id in self.pricing
        row["status"] = "overridden" if known else "added"
        left = self.model_list.index(row_index, 4)
        right = self.model_list.index(row_index, 4)
        self.model_list.dataChanged.emit(left, right)

    def _on_param_double_clicked(self, index: Any) -> None:
        model_id = self._selected_model()
        if not model_id:
            return
        item = self.param_model.item_at(index.row())
        if not item or index.column() not in (0, 1, 2, 3):
            return
        key = ("id", "label", "type", "default", "role")[index.column()]
        editor = self.param_table.edit(index)
        if editor is None:
            return
        if isinstance(editor, QLineEdit):
            editor.editingFinished.connect(
                lambda item=item, key=key, ed=editor: self._commit_param(model_id, item, key, ed.text())
            )

    def _commit_param(self, model_id: str, item: dict[str, Any], key: str, text: str) -> None:
        original = self.parameters.get(model_id, {}).get(PARAMS_FIELD, [])
        original_item = next(
            (p for p in original if isinstance(p, dict) and p.get("id") == item.get("id")), None
        )
        if key == "default":
            try:
                value: Any = json.loads(text)
            except json.JSONDecodeError:
                try:
                    value = int(text)
                except ValueError:
                    try:
                        value = float(text)
                    except ValueError:
                        value = text
        else:
            value = text
        item[key] = value

        descriptor = {key: value}
        if original_item is not None and key != "id":
            baseline = original_item.get(key)
            if value == baseline:
                item[key] = value
                self._remove_param_override(model_id, item["id"], key)
                return
        self._on_field_edit(model_id, "parameters", PARAMS_FIELD, None)
        entry = self.overlay[model_id]["parameters"]
        entry[PARAMS_FIELD] = _merged_param_array(original, entry.get(PARAMS_FIELD, []))
        self.param_model.set_items(entry[PARAMS_FIELD])
        self.param_table.selectRow(next((i for i, p in enumerate(entry[PARAMS_FIELD]) if p is item), 0))

    def _remove_param_override(self, model_id: str, param_id: str, key: str) -> None:
        entry = self.overlay.get(model_id, {}).get("parameters", {})
        array = entry.get(PARAMS_FIELD)
        if not isinstance(array, list):
            return
        for item in array:
            if isinstance(item, dict) and item.get("id") == param_id:
                item.pop(key, None)
                if set(item) <= {"id"}:
                    array.remove(item)
                    if not array:
                        entry.pop(PARAMS_FIELD, None)
        self.param_model.set_items(entry.get(PARAMS_FIELD) or [])

    # ------------------------------------------------------------- actions -- #

    def _on_merge(self) -> None:
        if self._loading:
            return
        validation = validate_overlay(self.overlay, self.parameters, self.pricing)
        if not validation.ok:
            QMessageBox.warning(
                self,
                "Overlay has errors",
                f"{len(validation.errors())} error(s):\n\n{validation.render(limit=20)}",
            )
            return

        self._set_busy(True, "merging…")
        worker = MergeWorker(self.parameters, self.pricing, self.overlay,
                             param_array_mode=ParamArrayMode.MERGE,
                             pricing_fields=PricingFieldPolicy.PRESERVE)
        worker.signals.finished.connect(self._on_merged)
        worker.signals.failed.connect(lambda msg: self._on_failed("merge", msg))
        self.pool.start(worker)

    def _on_merged(self, result: Any) -> None:
        self.merge_result = result
        self.change_model.set_changes(result.changes)
        self._set_busy(False)
        counts = result.summary()
        self.status_label.setText(
            f"merged · {counts.get('changed_models', 0):,} models changed · "
            f"{counts.get('new_models', 0):,} new · "
            f"{counts.get('added', 0):,} fields added · "
            f"{counts.get('overridden', 0):,} overridden"
        )
        self.btn_save_output.setEnabled(True)

    def _on_save_custom(self) -> None:
        if not self.custom_path:
            path, _ = QFileDialog.getSaveFileName(
                self, "Save custom overlay", "custom_model_metadata.json", "JSON files (*.json)"
            )
            if not path:
                return
            self.custom_path = Path(path)
        write_json_atomic(self.overlay, self.custom_path, indent=2)
        self.dirty = False
        self.btn_save_custom.setEnabled(False)
        self._update_path_label()
        self.status_label.setText(f"wrote {self.custom_path} ({len(self.overlay)} model entries)")

    def _on_save_output(self) -> None:
        if self.merge_result is None:
            return
        directory = QFileDialog.getExistingDirectory(self, "Choose output directory", str(self.output_dir))
        if not directory:
            return
        self.output_dir = Path(directory)
        self._set_busy(True, "writing merged files…")
        worker = SaveWorker(self.merge_result, self.output_dir)
        worker.signals.finished.connect(self._on_saved)
        worker.signals.failed.connect(lambda msg: self._on_failed("save", msg))
        self.pool.start(worker)

    def _on_saved(self, payload: dict[str, Any]) -> None:
        self._set_busy(False)
        problems = payload["problems"]
        if problems:
            QMessageBox.critical(self, "Post-write check failed", "\n".join(problems))
            self.status_label.setText("save completed with verification problems")
            return
        self.status_label.setText(
            f"wrote {payload['parameters']} and {payload['pricing']} "
            f"({self.merge_result.summary().get('changed_models', 0):,} models differ from the originals)"
        )

    def closeEvent(self, event: Any) -> None:  # noqa: N802
        if self.dirty:
            answer = QMessageBox.question(
                self,
                "Unsaved custom overlay",
                "The custom overlay has unsaved changes. Close anyway?",
                QMessageBox.Save | QMessageBox.Discard | QMessageBox.Cancel,
            )
            if answer == QMessageBox.Cancel:
                event.ignore()
                return
            if answer == QMessageBox.Save:
                self._on_save_custom()
        event.accept()


def _merged_param_array(original: list[Any], overlay: list[Any]) -> list[Any]:
    """Local helper mirroring the engine's by-id merge, for live sub-table edits."""
    from copy import deepcopy

    by_id = {}
    merged: list[Any] = []
    for item in original:
        if isinstance(item, dict) and isinstance(item.get("id"), str):
            by_id[item["id"]] = item
            merged.append(item)
    for item in overlay:
        if not isinstance(item, dict):
            merged.append(deepcopy(item))
            continue
        item_id = item.get("id")
        if isinstance(item_id, str) and item_id in by_id:
            target = by_id[item_id]
            for key, value in item.items():
                if key != "id":
                    target[key] = deepcopy(value)
        else:
            merged.append(deepcopy(item))
    return merged
