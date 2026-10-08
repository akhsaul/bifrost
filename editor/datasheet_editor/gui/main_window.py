"""Main editor window."""

from __future__ import annotations

import json
from pathlib import Path
from typing import Any

from PySide6.QtCore import QSettings, Qt, QThreadPool, QTimer
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

from ..fields import is_cost_field
from ..merge import (
    PARAM_ID_FIELD as PARAMS_ID_FIELD,
)
from ..merge import (
    PARAMS_FIELD,
    ParamArrayMode,
    PricingFieldPolicy,
    merge,
    merge_param_array,
)
from ..validate import validate_overlay
from ..zed_import import apply_import, import_zed_models
from ..opencode_import import apply_opencode_import, import_opencode_models
from .adddialogs import (
    AddFieldDialog,
    AddModelDialog,
    DEFAULT_CUSTOM_OUTPUT,
    DEFAULT_PARAMETERS_OUTPUT,
    DEFAULT_PRICING_OUTPUT,
    LoadDataDialog,
    OpencodeImportDialog,
    SAVE_ACTION_CUSTOM,
    SAVE_ACTION_OUTPUT,
    SaveConflictDialog,
    SaveToDialog,
    ZedImportDialog,
    build_conflict_item,
    first_free_path,
)
from .fieldeditor import FieldRow
from .models import (
    ChangeListModel,
    ModelFilterProxy,
    ModelListModel,
    ParamDescriptorModel,
    build_rows,
)
from .workers import (
    BUILD_STEP,
    TOTAL_STEPS,
    LoadWorker,
    MergeWorker,
    SaveCustomWorker,
    SaveWorker,
)

# Re-exported from datasheet_editor.output so the filenames, the collision rules
# and the save path cannot drift apart: three copies of these two strings is two
# chances to write somewhere other than where the user was told.
from ..output import PARAMETERS_FILENAME, PRICING_FILENAME  # noqa: F401


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
        #: Open the Save To dialog as soon as a merge triggered from it
        #: finishes. Plain Merge & Preview runs leave this False.
        self._open_save_after_merge = False
        #: What each field means and what shape its value takes, built by the load
        #: worker. Feeds the Add Field dialog's descriptions and typed inputs.
        self.param_catalog = None
        self.pricing_catalog = None
        self._progress_dialog: QProgressDialog | None = None

        self.model_list = ModelListModel(self)
        self.proxy = ModelFilterProxy(self)
        self.proxy.setSourceModel(self.model_list)
        self.param_model = ParamDescriptorModel(
            self, self._on_param_cell_edited, self._descriptor_info
        )
        #: Descriptor whose row the table should re-select after a rebuild, so an
        #: edit does not move the user's selection to the top of the list.
        self._param_focus_id: str | None = None
        self.change_model = ChangeListModel(self)

        self._build_ui()
        self._wire()
        self._update_path_label()

        self.pool = QThreadPool.globalInstance()
        self.search_timer = QTimer(self)
        self.search_timer.setSingleShot(True)
        self.search_timer.setInterval(200)
        self.search_timer.timeout.connect(self._apply_search)

        self.provider_timer = QTimer(self)
        self.provider_timer.setSingleShot(True)
        self.provider_timer.setInterval(200)
        self.provider_timer.timeout.connect(self._apply_provider_search)

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

        # One dialog for all three inputs: parameters and pricing are always
        # needed together, so choosing them one button at a time only produced
        # a "still missing" modal after the first pick.
        self.btn_load_data = QPushButton("Load Data…")
        self.btn_load_data.setToolTip(
            f"Choose {PARAMETERS_FILENAME} and {PRICING_FILENAME} (required) "
            "plus the custom overlay (optional) in one dialog"
        )
        self.btn_load_data.clicked.connect(self._on_load_data)
        bar.addWidget(self.btn_load_data)

        self.btn_add_model = QPushButton("Add Model…")
        self.btn_add_model.setToolTip(
            "Add a model that does not exist yet, to your custom overlay"
        )
        self.btn_add_model.clicked.connect(self._on_add_model)
        bar.addWidget(self.btn_add_model)

        self.btn_import_zed = QPushButton("Import Zed…")
        self.btn_import_zed.setToolTip(
            "Convert a Zed GET /models file into zed/<id> overlay entries"
        )
        self.btn_import_zed.clicked.connect(self._on_import_zed)
        bar.addWidget(self.btn_import_zed)

        self.btn_import_opencode = QPushButton("Import Opencode…")
        self.btn_import_opencode.setToolTip(
            "Convert an Opencode catalog file into opencode-zen/<id> and "
            "opencode-go/<id> overlay entries"
        )
        self.btn_import_opencode.clicked.connect(self._on_import_opencode)
        bar.addWidget(self.btn_import_opencode)

        self.btn_merge = QPushButton("Merge & Preview")
        self.btn_merge.clicked.connect(self._on_merge)
        self.btn_merge.setEnabled(False)
        bar.addWidget(self.btn_merge)

        self.btn_save_to = QPushButton("Save To…")
        self.btn_save_to.setToolTip(
            "Name the merged parameters/pricing files and the custom overlay, "
            "then write them sorted and beautified"
        )
        self.btn_save_to.clicked.connect(self._on_save_to)
        self.btn_save_to.setEnabled(False)
        bar.addWidget(self.btn_save_to)

        return bar

    def _on_import_zed(self) -> None:
        """Import a Zed /models file into the overlay as zed/<id> entries.

        The file is parsed by the same pure importer the CLI uses, so the two
        front-ends cannot disagree. Nothing touches the overlay until the
        confirm dialog is accepted; after that Save To… (Save Custom) is still
        needed, like every other overlay edit.
        """
        chosen, _ = QFileDialog.getOpenFileName(
            self,
            "Select Zed /models file",
            self._browse_dir(),
            "JSON files (*.json)",
        )
        if not chosen:
            return
        try:
            with open(chosen, encoding="utf-8") as fh:
                zed_data = json.load(fh)
        except (OSError, json.JSONDecodeError) as exc:
            QMessageBox.critical(self, "Import failed", f"Could not read {chosen}:\n{exc}")
            return
        try:
            from ..zed_import import ZedImportError

            preview = import_zed_models(zed_data, self.overlay)
        except ZedImportError as exc:
            QMessageBox.critical(self, "Import failed", str(exc))
            return
        if not preview.added and not preview.skipped:
            QMessageBox.information(self, "Import Zed models", "The file holds no models.")
            return
        dialog = ZedImportDialog(
            Path(chosen).name, preview.added, preview.skipped, self
        )
        from PySide6.QtWidgets import QDialog

        if dialog.exec() != QDialog.Accepted:
            self.status_label.setText("zed import cancelled — overlay left as it was")
            return
        result = import_zed_models(
            zed_data, self.overlay,
            overwrite_existing=dialog.overwrite_existing(),
        )
        apply_import(
            self.overlay, result,
            overwrite_existing=dialog.overwrite_existing(),
        )
        self._mark_overlay_dirty()
        self._refresh_rows()
        added, overwritten = len(result.added), len(result.overwritten)
        parts = []
        if added:
            parts.append(f"{added} added")
        if overwritten:
            parts.append(f"{overwritten} overwritten")
        self.status_label.setText(
            f"zed import: {', '.join(parts)}. Save To… (Save Custom) to keep it."
        )

    def _on_import_opencode(self) -> None:
        """Import an Opencode catalog file into the overlay.

        The file is parsed by the same pure importer the CLI uses, so the two
        front-ends cannot disagree. Nothing touches the overlay until the
        confirm dialog is accepted; after that Save To… (Save Custom) is still
        needed, like every other overlay edit.
        """
        chosen, _ = QFileDialog.getOpenFileName(
            self,
            "Select Opencode models file",
            self._browse_dir(),
            "JSON files (*.json)",
        )
        if not chosen:
            return
        try:
            with open(chosen, encoding="utf-8") as fh:
                opencode_data = json.load(fh)
        except (OSError, json.JSONDecodeError) as exc:
            QMessageBox.critical(self, "Import failed", f"Could not read {chosen}:\n{exc}")
            return
        try:
            from ..opencode_import import OpencodeImportError

            preview = import_opencode_models(opencode_data, self.overlay)
        except OpencodeImportError as exc:
            QMessageBox.critical(self, "Import failed", str(exc))
            return
        if not preview.added and not preview.skipped:
            QMessageBox.information(self, "Import Opencode models", "The file holds no models.")
            return
        dialog = OpencodeImportDialog(
            Path(chosen).name, preview.added, preview.skipped, preview.warnings, self
        )
        from PySide6.QtWidgets import QDialog

        if dialog.exec() != QDialog.Accepted:
            self.status_label.setText("opencode import cancelled — overlay left as it was")
            return
        result = import_opencode_models(
            opencode_data, self.overlay,
            overwrite_existing=dialog.overwrite_existing(),
        )
        apply_opencode_import(
            self.overlay, result,
            overwrite_existing=dialog.overwrite_existing(),
        )
        self._mark_overlay_dirty()
        self._refresh_rows()
        added, overwritten = len(result.added), len(result.overwritten)
        parts = []
        if added:
            parts.append(f"{added} added")
        if overwritten:
            parts.append(f"{overwritten} overwritten")
        if result.warnings:
            parts.append(f"{len(result.warnings)} tier(s) dropped")
        self.status_label.setText(
            f"opencode import: {', '.join(parts)}. Save To… (Save Custom) to keep it."
        )

    def _build_filterbar(self) -> QHBoxLayout:
        bar = QHBoxLayout()
        bar.setSpacing(8)

        self.search_edit = QLineEdit()
        self.search_edit.setPlaceholderText("Search models… (contains, case-insensitive)")
        self.search_edit.setClearButtonEnabled(True)
        self.search_edit.textChanged.connect(lambda _: self.search_timer.start())
        bar.addWidget(self.search_edit, 2)

        self.provider_combo = QComboBox()
        self.provider_combo.setMinimumWidth(200)
        self.provider_combo.setToolTip(
            "Pick one provider exactly. Counts refresh when the data changes."
        )
        self.provider_combo.currentIndexChanged.connect(self._on_provider_changed)
        bar.addWidget(self.provider_combo)

        # A free-text provider filter alongside the dropdown. The dropdown only
        # offers providers it has already seen, so it cannot be used to look up a
        # provider that has just been typed into the overlay, and it is useless
        # for partial matches. This does substring matching and updates as you type.
        self.provider_search = QLineEdit()
        self.provider_search.setPlaceholderText("Provider contains…")
        self.provider_search.setClearButtonEnabled(True)
        self.provider_search.setMinimumWidth(170)
        self.provider_search.setToolTip(
            "Substring match on the provider only, e.g. \"bed\" finds bedrock"
        )
        self.provider_search.textChanged.connect(
            lambda _: self.provider_timer.start()
        )
        bar.addWidget(self.provider_search)

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

        # Entries carry up to 48 fields, so the pane scrolls rather than squeezing.
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
        params_scroll.setWidget(self.params_fields_box)
        self.params_layout.addWidget(params_scroll, 1)
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

        # The prompt-playground descriptors live in their own tab rather than below
        # the fields. Two reasons: they are a different vocabulary (an array of
        # parameter controls, keyed by id, versus the entry's own columns), which is
        # the same split the Add Field dialog asks about; and squeezed into a
        # splitter a model's ten-to-fifteen descriptors got five visible rows, so
        # the row being edited was usually below the fold.
        descriptors_page = QWidget()
        descriptors_layout = QVBoxLayout(descriptors_page)
        descriptors_layout.setContentsMargins(6, 6, 6, 6)
        self.descriptors_hint = QLabel(
            "Each row is one prompt-playground control Bifrost renders for this "
            "model. Bifrost reads only the id, which feeds the request-parameter "
            "allowlist; label and default are passed to the playground unchanged. "
            "Editing a value records an override keyed by id — the id and type "
            "columns are read-only."
        )
        self.descriptors_hint.setWordWrap(True)
        self.descriptors_hint.setStyleSheet("color:#475569;")
        descriptors_layout.addWidget(self.descriptors_hint)

        self.param_array_box = QGroupBox("model_parameters  (merged by id)")
        array_layout = QVBoxLayout(self.param_array_box)
        self.param_table = QTableView()
        self.param_table.setModel(self.param_model)
        self.param_table.verticalHeader().setVisible(False)
        self.param_table.horizontalHeader().setSectionResizeMode(QHeaderView.Interactive)
        self.param_table.horizontalHeader().setStretchLastSection(True)
        self.param_table.setColumnWidth(0, 170)
        self.param_table.setColumnWidth(1, 230)
        self.param_table.setColumnWidth(2, 110)
        self.param_table.setColumnWidth(3, 110)
        self.param_table.setAlternatingRowColors(True)
        # A Delete key in the table would be quicker, but the row under the cursor
        # is not the row the user last edited after a rebuild, and a descriptor
        # carries a label and a default that took real typing. An explicit button
        # on the selected row is the one that cannot surprise anyone.
        self.param_table.setContextMenuPolicy(Qt.CustomContextMenu)
        self.param_table.customContextMenuRequested.connect(self._on_param_context_menu)
        array_layout.addWidget(self.param_table)

        descriptor_actions = QHBoxLayout()
        descriptor_actions.setContentsMargins(0, 6, 0, 0)
        self.btn_delete_param = QPushButton("Delete Descriptor")
        self.btn_delete_param.clicked.connect(self._on_delete_param)
        self.btn_delete_param.setEnabled(False)
        descriptor_actions.addWidget(self.btn_delete_param, 0, Qt.AlignLeft)
        self.descriptors_delete_hint = QLabel()
        self.descriptors_delete_hint.setWordWrap(True)
        self.descriptors_delete_hint.setStyleSheet("color:#64748b;")
        descriptor_actions.addWidget(self.descriptors_delete_hint, 1)
        array_layout.addLayout(descriptor_actions)

        self.descriptors_empty = QLabel(
            "This model publishes no model_parameters descriptors."
        )
        self.descriptors_empty.setAlignment(Qt.AlignCenter)
        self.descriptors_empty.setStyleSheet("color:#64748b;font-style:italic;")
        array_layout.addWidget(self.descriptors_empty)
        descriptors_layout.addWidget(self.param_array_box, 1)
        # Added after the conflicts tab below, so the order reads Parameters,
        # Pricing, Conflicts, model_parameters.

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
        self._descriptors_tab = self.tabs.addTab(descriptors_page, "model_parameters")

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
        self.param_table.clicked.connect(self._on_param_clicked)

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
            f"Press Load Data… and choose {PARAMETERS_FILENAME} and "
            f"{PRICING_FILENAME}. Custom overlay is optional."
        )

    def _on_load_data(self) -> None:
        dialog = LoadDataDialog(
            parameters=str(self.parameters_path) if self.parameters_path else "",
            pricing=str(self.pricing_path) if self.pricing_path else "",
            custom=str(self.custom_path) if self.custom_path else "",
            browse_dir=self._browse_dir(),
            parent=self,
        )
        if dialog.exec() != QDialog.Accepted:
            return
        chosen = dialog.values()
        assert chosen["parameters"] is not None and chosen["pricing"] is not None
        self.parameters_path = chosen["parameters"]
        self.pricing_path = chosen["pricing"]
        self.custom_path = chosen["custom"]
        self._update_path_label()
        self._start_load()

    def _browse_dir(self) -> str:
        for path in (self.parameters_path, self.pricing_path, self.custom_path):
            if path is not None:
                return str(path.parent)
        return str(self.settings.value("last_dir", str(Path.cwd())))

    def _start_load(self) -> None:
        """Load once both originals are chosen; the dialog enforces that."""
        if self.parameters_path is None or self.pricing_path is None:
            # Unreachable through the Load Data dialog, which requires both,
            # but load_paths() (CLI args) can still land here with one missing.
            self._update_path_label()
            self.btn_merge.setEnabled(False)
            missing = [
                name
                for name, path in (
                    (PARAMETERS_FILENAME, self.parameters_path),
                    (PRICING_FILENAME, self.pricing_path),
                )
                if path is None
            ]
            self.status_label.setText("Waiting for " + " and ".join(missing))
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
        self.param_catalog = payload.get("param_catalog")
        self.pricing_catalog = payload.get("pricing_catalog")

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
            self.btn_load_data,
            self.btn_import_zed,
            self.btn_import_opencode,
        ):
            button.setEnabled(not busy)
        has_data = bool(self.model_list.rowCount())
        selected = bool(self._selected_model())
        self.btn_add_model.setEnabled(not busy and has_data)
        self.btn_import_zed.setEnabled(not busy)
        self.btn_import_opencode.setEnabled(not busy)
        self.btn_add_param_field.setEnabled(not busy and selected)
        self.btn_add_pricing_field.setEnabled(not busy and selected)
        self.btn_merge.setEnabled(not busy and has_data)
        self.btn_save_to.setEnabled(not busy and has_data)
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

    def _apply_provider_search(self) -> None:
        self.proxy.set_provider_search(self.provider_search.text())
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
        for widget in (self.search_edit, self.provider_search, self.provider_combo, self.mode_combo):
            widget.blockSignals(True)
        self.search_edit.clear()
        self.provider_search.clear()
        self.provider_combo.setCurrentIndex(0)
        self.mode_combo.setCurrentIndex(0)
        for widget in (self.search_edit, self.provider_search, self.provider_combo, self.mode_combo):
            widget.blockSignals(False)
        # One coalesced call: setting the filters separately re-maps all 12,595
        # rows once per condition.
        self.proxy.set_filters("", "", set(), set())
        self._update_filter_state()

    def _on_escape(self) -> None:
        for edit, timer, apply in (
            (self.search_edit, self.search_timer, self._apply_search),
            (self.provider_search, self.provider_timer, self._apply_provider_search),
        ):
            if edit.text():
                edit.clear()
                timer.stop()
                apply()
                return
        if self.proxy.active:
            self._clear_filters()

    def _update_filter_state(self) -> None:
        self.btn_clear.setEnabled(self.proxy.active)
        shown = self.proxy.rowCount()
        total = self.model_list.rowCount()
        parts = [f"showing {shown:,} of {total:,} models"]
        if self.proxy.needle:
            parts.append(f'search "{self.search_edit.text().strip()}"')
        if self.proxy.provider_needle:
            parts.append(f'provider~"{self.provider_search.text().strip()}"')
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
        self.tabs.setTabEnabled(self._descriptors_tab, False)
        self.param_array_box.setEnabled(False)
        self.param_table.setVisible(False)
        self.descriptors_empty.setVisible(False)
        self._param_focus_id = None
        self._update_delete_param_enabled()
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
                               else "no pricing entry (add one via Save To… (Save Custom), or use CLI)")
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

        array = _param_view(model_id, self)
        self.param_model.set_items(array)
        has_array = bool(array)
        self.param_array_box.setEnabled(has_array)
        self.param_table.setVisible(has_array)
        self.descriptors_empty.setVisible(not has_array)
        # Disabling the tab, rather than leaving an empty one, keeps the tab row
        # from carrying a dead entry for the many models with no descriptors.
        self.tabs.setTabEnabled(self._descriptors_tab, has_array)
        # The focus id can name a descriptor that is gone or now read-only, so the
        # Delete button is re-evaluated rather than carried over.
        self._update_delete_param_enabled()

    # ------------------------------------------------------------ adding -- #

    def _require_loaded(self) -> bool:
        if self.model_list.rowCount():
            return True
        QMessageBox.information(
            self,
            "Nothing loaded yet",
            "Load both datasheets first — press \"Load Data…\".",
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
        self._refresh_rows()
        self._select_model(model_id)
        self._rebuild_field_panes(model_id)
        self._mark_overlay_dirty()
        self.status_label.setText(
            f"Added {model_id} to the custom overlay. Save To… (Save Custom) to keep it."
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
        overlay_section = self.overlay.get(model_id, {}).get(section, {})
        existing = set(base) | set(overlay_section)
        known = sorted({name for entry in dataset.values() for name in entry})

        catalog = self.param_catalog if section == "parameters" else self.pricing_catalog
        dialog = AddFieldDialog(
            section=section,
            model_id=model_id,
            known_fields=known,
            existing=existing,
            catalog=catalog,
            descriptor_names=catalog.descriptor_names() if section == "parameters" else None,
            descriptors_present=_descriptor_ids(base) | _descriptor_ids(overlay_section),
            parent=self,
        )
        if dialog.exec() != QDialog.Accepted:
            return

        name, value = dialog.values()
        if dialog.is_descriptor():
            # A descriptor is an element of the model_parameters array, not a new
            # top-level key -- writing a bare `reasoning_effort` key would land a
            # field no Go struct declares. The merge keys the array on `id`, so the
            # overlay carries just this one descriptor and the engine folds it in.
            self._param_focus_id = name
            for key, item in value.items():
                if key != PARAMS_ID_FIELD:
                    _set_param_field(self, model_id, name, key, item)
            described = f"{model_id}.{section}.{PARAMS_FIELD}[id={name}]"
        else:
            self.overlay.setdefault(model_id, {}).setdefault(section, {})[name] = value
            described = f"{model_id}.{section}.{name}"
        self._mark_overlay_dirty()
        self._mark_row(model_id)
        self._rebuild_field_panes(model_id)
        self.status_label.setText(f"Added {described}. Save To… (Save Custom) to keep it.")

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
        self._mark_overlay_dirty()
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
        self._mark_overlay_dirty()
        self._mark_row(model_id)
        self._rebuild_field_panes(model_id)

    def _mark_custom_saved(self, path: Path) -> None:
        """Record a custom-overlay write: clear dirty and refresh the labels."""
        self.custom_path = path
        self.dirty = False
        self._remember_custom_dir(path)
        self._update_path_label()

    def _mark_overlay_dirty(self) -> None:
        """Record an overlay edit. Saving goes through Save To…, so the only
        state to track is the dirty flag -- there is no save button to arm."""
        self.dirty = True

    def _remember_custom_dir(self, path: Path) -> None:
        self.settings.setValue("last_dir", str(path.parent))
        self.output_dir = path.parent

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

    def _descriptor_info(self, param_id: str) -> Any:
        """Catalog entry for a parameter id, for the table's row tooltips."""
        catalog = self.param_catalog
        return catalog.descriptor(param_id) if (catalog and param_id) else None

    def _select_descriptor(self, param_id: str | None) -> None:
        """Make *param_id* the descriptor the Delete button and a rebuild target.

        The single place focus changes, so a selection made by click, by
        keyboard, or by a test all leave the same state behind.
        """
        self._param_focus_id = param_id
        self._update_delete_param_enabled()

    def _on_param_clicked(self, index: Any) -> None:
        """Remember the clicked descriptor so a rebuild can put the selection back.

        Selection, not editing: the cell editors come from the model's ``setData``
        and the view's own edit triggers, which apply to single-click, double-click
        and F2 alike. A bespoke double-click editor meant F2 and typing into a
        selected cell silently did nothing.
        """
        item = self.param_model.item_at(index.row())
        param_id = item.get(PARAMS_ID_FIELD) if isinstance(item, dict) else None
        self._select_descriptor(param_id if isinstance(param_id, str) else None)

    def _update_delete_param_enabled(self) -> None:
        """Arm the Delete button only for a descriptor the user actually added.

        The distinction is the whole point: a descriptor from the original
        dataset is not the user's to remove here. There is no delete channel in
        the overlay, so "delete" on one of those would silently do nothing at
        merge time, or force a tombstone into the custom file's format.
        """
        model_id = self._selected_model()
        param_id = self._param_focus_id
        if not model_id or not param_id:
            self.btn_delete_param.setEnabled(False)
            self.btn_delete_param.setToolTip("Select a descriptor first.")
            self.descriptors_delete_hint.setText("")
            return

        original_ids = _descriptor_ids(self.parameters.get(model_id) or {})
        overlay_ids = _descriptor_ids({"model_parameters": _param_overlay(self, model_id)})
        if param_id in original_ids:
            self.btn_delete_param.setEnabled(False)
            self.btn_delete_param.setToolTip(
                f"'{param_id}' comes from the original model_parameters.json, not from "
                "your custom overlay. Editing its label or default records an override; "
                "the descriptor itself stays, because the overlay has no way to remove it."
            )
            self.descriptors_delete_hint.setText(
                f"'{param_id}' is from the original file — it can be overridden, not removed."
            )
            return

        self.btn_delete_param.setEnabled(True)
        self.btn_delete_param.setToolTip(
            f"Remove '{param_id}' from your custom overlay. The original file is untouched."
        )
        self.descriptors_delete_hint.setText(
            f"'{param_id}' is yours — it can be edited or removed."
        )

    def _on_delete_param(self) -> None:
        model_id = self._selected_model()
        param_id = self._param_focus_id
        if not model_id or not param_id:
            return
        if not _is_deletable(self, model_id, param_id):
            # Reached by keyboard or a stale click; the button was already
            # disabled, so this is belt-and-braces rather than a real path.
            return
        answer = QMessageBox.question(
            self,
            "Remove this descriptor?",
            f"Remove '{param_id}' from the custom overlay?\n\n"
            "The original model_parameters.json is not touched. The descriptor "
            "disappears from the merged output until you add it again.",
            QMessageBox.Yes | QMessageBox.No,
            QMessageBox.No,
        )
        if answer != QMessageBox.Yes:
            return
        _delete_param(self, model_id, param_id)
        self.status_label.setText(
            f"Removed {param_id} from the custom overlay. Save To… (Save Custom) to keep it."
        )
        self._update_delete_param_enabled()

    def _on_param_context_menu(self, pos: Any) -> None:
        """Right-click a descriptor: select it, then offer the same two actions."""
        index = self.param_table.indexAt(pos)
        if not index.isValid():
            return
        item = self.param_model.item_at(index.row())
        param_id = item.get(PARAMS_ID_FIELD) if isinstance(item, dict) else None
        if not isinstance(param_id, str):
            return
        self._select_descriptor(param_id)
        self.btn_delete_param.showMenu(self.param_table.viewport().mapToGlobal(pos))

    def _on_param_cell_edited(self, param_id: str, key: str, value: Any) -> None:
        """A descriptor cell was edited in the table; record it as an override."""
        model_id = self._selected_model()
        if not model_id:
            return
        self._param_focus_id = param_id
        _set_param_field(self, model_id, param_id, key, value)
        kept = bool((self.overlay.get(model_id) or {}).get("parameters", {}).get(PARAMS_FIELD))
        self.status_label.setText(
            f"{model_id}.parameters.{PARAMS_FIELD}[id={param_id}].{key} = "
            f"{_short_json(value)}"
            + (". Save To… (Save Custom) to keep it." if kept else " (matches the original, nothing to keep).")
        )

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
        if self._open_save_after_merge:
            # Save To was pressed before a preview existed: the merge it
            # triggered just finished, so open the dialog now rather than
            # leaving the user to press the button again.
            self._open_save_after_merge = False
            self._open_save_to()

    def _default_save_targets(self) -> dict[str, Path]:
        """Prefilled Save To paths: workdir plus the dash-form default names."""
        workdir = Path(str(self.settings.value("last_dir", str(Path.cwd()))))
        return {
            "parameters": workdir / DEFAULT_PARAMETERS_OUTPUT,
            "pricing": workdir / DEFAULT_PRICING_OUTPUT,
            "custom": workdir / DEFAULT_CUSTOM_OUTPUT,
        }

    def _on_save_to(self) -> None:
        """Open Save To; pressing it counts as having seen the preview.

        When no merge has run yet, trigger one first and open the dialog when
        it finishes -- the per-path counts need merged numbers, and a save
        without them would write files the preview never showed.
        """
        if not self._require_loaded():
            return
        if self.merge_result is None:
            validation = validate_overlay(self.overlay, self.parameters, self.pricing)
            if not validation.ok:
                QMessageBox.warning(
                    self,
                    "Overlay has errors",
                    f"{len(validation.errors())} error(s):\n\n{validation.render(limit=20)}",
                )
                return
            self._open_save_after_merge = True
            self._set_busy(True, "merging…")
            worker = MergeWorker(self.parameters, self.pricing, self.overlay,
                                 param_array_mode=ParamArrayMode.MERGE,
                                 pricing_fields=PricingFieldPolicy.PRESERVE)
            worker.signals.finished.connect(self._on_merged)
            worker.signals.failed.connect(lambda msg: self._on_failed("merge", msg))
            self.pool.start(worker)
            return
        self._open_save_to()

    def _open_save_to(self) -> None:
        assert self.merge_result is not None
        defaults = self._default_save_targets()
        dialog = SaveToDialog(
            parameters_path=str(defaults["parameters"]),
            pricing_path=str(defaults["pricing"]),
            custom_path=str(defaults["custom"]),
            parameters_count=len(self.merge_result.parameters),
            pricing_count=len(self.merge_result.pricing),
            custom_count=len(self.overlay),
            parent=self,
        )
        if dialog.exec() != QDialog.Accepted:
            self.status_label.setText("save cancelled — output files left as they were")
            return
        action = dialog.action()
        targets = dialog.values()
        if action == SAVE_ACTION_OUTPUT:
            self._save_merged_output(
                {k: v for k, v in targets.items() if k in ("parameters", "pricing") and v is not None}
            )
        elif action == SAVE_ACTION_CUSTOM:
            custom = targets.get("custom")
            assert custom is not None
            self._save_custom_overlay(custom)

    def _save_merged_output(self, targets: dict[str, Path]) -> None:
        """Write the filled merged file(s), asking about collisions first."""
        if not targets:
            return
        clash = self._save_targets_an_input(targets)
        if clash is not None:
            QMessageBox.warning(
                self,
                "That is the file you loaded",
                f"{clash.name} is a file this session was loaded from. Writing "
                "the merged result there would replace the original, and the "
                "merge has already read it — the run could not be repeated.\n\n"
                "Choose a different name.",
            )
            self.status_label.setText(f"refused to overwrite the input {clash.name}")
            return
        conflicts = [
            build_conflict_item(
                role, path,
                len(self.merge_result.parameters if role == "parameters"
                    else self.merge_result.pricing),
            )
            for role, path in targets.items() if path.exists()
        ]
        if conflicts:
            # Asked before the write starts, not after: a collision has to be
            # settled on the main thread, and the dialog can be cancelled, so it
            # cannot live inside the worker.
            from ..output import ADD_NUMBER

            dialog = SaveConflictDialog(conflicts, self)
            if dialog.exec() != QDialog.Accepted:
                self.status_label.setText("save cancelled — output files left as they were")
                return
            if dialog.policy() == ADD_NUMBER:
                targets = {
                    role: first_free_path(path) if path.exists() else path
                    for role, path in targets.items()
                }
        paths = {
            (PARAMETERS_FILENAME if role == "parameters" else PRICING_FILENAME): path
            for role, path in targets.items()
        }
        self._remember_save_dir(paths)
        self._set_busy(True, "writing merged files…")
        worker = SaveWorker(self.merge_result, paths=paths)
        worker.signals.finished.connect(self._on_saved)
        worker.signals.failed.connect(lambda msg: self._on_failed("save", msg))
        self.pool.start(worker)

    def _save_custom_overlay(self, path: Path) -> None:
        """Write the overlay only (what the user changed), sorted + beautified."""
        clash = self._save_targets_an_input({"custom": path})
        if clash is not None:
            QMessageBox.warning(
                self,
                "That is the file you loaded",
                f"{clash.name} is a file this session was loaded from. "
                "Overwriting an input with the overlay would mix the two "
                "roles.\n\nChoose a different name.",
            )
            self.status_label.setText(f"refused to overwrite the input {clash.name}")
            return
        if path.exists():
            from ..output import ADD_NUMBER

            dialog = SaveConflictDialog(
                [build_conflict_item("custom", path, len(self.overlay))], self
            )
            if dialog.exec() != QDialog.Accepted:
                self.status_label.setText("save cancelled — output files left as they were")
                return
            if dialog.policy() == ADD_NUMBER:
                path = first_free_path(path)
        self._remember_save_dir({"custom.json": path})
        self._set_busy(True, "writing custom overlay…")
        worker = SaveCustomWorker(self.overlay, path)
        worker.signals.finished.connect(self._on_custom_saved)
        worker.signals.failed.connect(lambda msg: self._on_failed("save", msg))
        self.pool.start(worker)

    def _save_targets_an_input(self, targets: dict[str, Path]) -> Path | None:
        """A save target that is also one of the files this session loaded.

        An input is not a valid output target, even with Overwrite chosen: the
        merge read that file moments ago, so replacing it is not repeatable and
        the originals are the only copy. The CLI refuses the same case via
        --force, and the two front-ends must not disagree about it. Checked
        before the conflict dialog so the user is not asked a question whose
        every answer is refused. The loaded custom overlay is exempt: saving
        the overlay back to where it came from is the normal round-trip.
        """
        sources = [self.parameters_path, self.pricing_path]
        for target in targets.values():
            if not target.exists():
                continue
            try:
                resolved = target.resolve()
            except OSError:
                continue
            for source in sources:
                if source and source.exists() and source.resolve() == resolved:
                    return target
        return None

    def _remember_save_dir(self, paths: dict[str, Path]) -> None:
        for path in paths.values():
            self.settings.setValue("last_dir", str(path.parent))
            self.output_dir = path.parent
            break

    def _on_saved(self, payload: dict[str, Any]) -> None:
        self._set_busy(False)
        problems = payload["problems"]
        if problems:
            QMessageBox.critical(self, "Post-write check failed", "\n".join(problems))
            self.status_label.setText("save completed with verification problems")
            return
        written = [v for v in (payload.get("parameters"), payload.get("pricing")) if v]
        self.status_label.setText(
            f"wrote {' and '.join(written)} "
            f"({self.merge_result.summary().get('changed_models', 0):,} models differ from the originals)"
        )

    def _on_custom_saved(self, payload: dict[str, Any]) -> None:
        self._set_busy(False)
        problems = payload["problems"]
        if problems:
            QMessageBox.critical(self, "Post-write check failed", "\n".join(problems))
            self.status_label.setText("save completed with verification problems")
            return
        self._mark_custom_saved(Path(payload["custom"]))
        self.status_label.setText(
            f"wrote {payload['custom']} ({len(self.overlay)} custom entries, sorted)"
        )

    def closeEvent(self, event: Any) -> None:  # noqa: N802
        if self.dirty:
            answer = QMessageBox.question(
                self,
                "Unsaved custom overlay",
                "The custom overlay has unsaved changes. Save it via Save To… first, "
                "or close without saving?",
                QMessageBox.Discard | QMessageBox.Cancel,
            )
            if answer == QMessageBox.Cancel:
                event.ignore()
                return
        event.accept()


def _short_json(value: Any) -> str:
    """Compact rendering of an edited value for the status line."""
    text = json.dumps(value) if not isinstance(value, str) else json.dumps(value)
    return text if len(text) <= 60 else text[:57] + "..."


def _param_view(model_id: str, window: "MainWindow") -> list[Any]:
    """The effective ``model_parameters`` array: original overlaid with the overlay.

    Uses the engine's own by-id merge, so the table shows what a merge would
    produce and no dict is shared with the loaded dataset.
    """
    original = (window.parameters.get(model_id) or {}).get(PARAMS_FIELD) or []
    overlay = _param_overlay(window, model_id)
    return merge_param_array(original, overlay)


def _param_overlay(window: "MainWindow", model_id: str) -> list[Any]:
    """The overlay's ``model_parameters`` list, or an empty one.

    Only descriptors the user added or touched belong here. The engine matches on
    ``id`` and deep-merges the remaining keys, so restating a model's whole array
    would bloat the custom file and put original data where an override belongs.
    """
    array = (window.overlay.get(model_id) or {}).get("parameters", {}).get(PARAMS_FIELD)
    return array if isinstance(array, list) else []


def _set_param_field(window: "MainWindow", model_id: str, param_id: str, key: str, value: Any) -> None:
    """Record (or clear) one key of one descriptor in the overlay."""
    section = window.overlay.setdefault(model_id, {}).setdefault("parameters", {})
    array = section.get(PARAMS_FIELD)
    if not isinstance(array, list):
        array = []
        section[PARAMS_FIELD] = array

    entry = next(
        (i for i in array if isinstance(i, dict) and i.get(PARAMS_ID_FIELD) == param_id), None
    )

    original_item = next(
        (
            p
            for p in (window.parameters.get(model_id) or {}).get(PARAMS_FIELD, [])
            if isinstance(p, dict) and p.get(PARAMS_ID_FIELD) == param_id
        ),
        None,
    )
    baseline = original_item.get(key) if isinstance(original_item, dict) else None
    # Absent in the original counts as "no value", so typing the original's value
    # back in is a revert rather than an override that changes nothing.
    if key not in (original_item or {}) and value is None:
        baseline = None
        reverting = True
    else:
        reverting = value == baseline

    if reverting:
        if entry is None:
            return
        entry.pop(key, None)
        # A descriptor carrying nothing but its id says nothing; drop it so the
        # overlay does not accumulate hollow entries.
        if set(entry) <= {PARAMS_ID_FIELD}:
            array.remove(entry)
        if not array:
            section.pop(PARAMS_FIELD, None)
        window._mark_overlay_dirty()
        window._mark_row(model_id)
        _refresh_param_table(window, model_id)
        return

    if entry is None:
        entry = {PARAMS_ID_FIELD: param_id}
        array.append(entry)
    entry[key] = value
    window._mark_overlay_dirty()
    window._mark_row(model_id)
    _refresh_param_table(window, model_id)


def _is_deletable(window: "MainWindow", model_id: str, param_id: str) -> bool:
    """Whether *param_id* is a descriptor the user added and may remove again.

    True only when the overlay has an entry for it and the original does not. An
    original descriptor is the source data's, not the custom file's: overriding
    it is an override, and removing it is not something the overlay can express.
    """
    if param_id in _descriptor_ids(window.parameters.get(model_id) or {}):
        return False
    return param_id in _descriptor_ids({"model_parameters": _param_overlay(window, model_id)})


def _delete_param(window: "MainWindow", model_id: str, param_id: str) -> None:
    """Drop an overlay-only descriptor, pruning the empty containers it leaves.

    The pruning is not cosmetic: an empty ``model_parameters: []`` or an empty
    ``parameters: {}`` would be written to the custom file and show up in the
    diff as a change against a model that has nothing to change.
    """
    section = window.overlay.get(model_id, {}).get("parameters")
    if not isinstance(section, dict):
        return
    array = section.get(PARAMS_FIELD)
    if not isinstance(array, list):
        return
    remaining = [
        item for item in array
        if not (isinstance(item, dict) and item.get(PARAMS_ID_FIELD) == param_id)
    ]
    if len(remaining) == len(array):
        return

    entry = window.overlay[model_id]
    if remaining:
        section[PARAMS_FIELD] = remaining
    else:
        section.pop(PARAMS_FIELD, None)
    if not section:
        entry.pop("parameters", None)
    if not entry:
        window.overlay.pop(model_id, None)

    window._mark_overlay_dirty()
    window._mark_row(model_id)
    _refresh_param_table(window, model_id)


def _refresh_param_table(window: "MainWindow", model_id: str) -> None:
    """Re-render the descriptor table after an overlay change, keeping the row."""
    keep = window._param_focus_id
    window.param_model.set_items(_param_view(model_id, window))
    if keep:
        row = window.param_model.row_for(keep)
        if row >= 0:
            # selectRow does not scroll, so on a model with a dozen descriptors the
            # row just edited can sit below the fold while the edit is recorded.
            window.param_table.scrollTo(window.param_model.index(row, 0))
            window.param_table.selectRow(row)
            return
    if window.param_model.rowCount():
        window.param_table.selectRow(0)


def _descriptor_ids(entry: dict[str, Any]) -> set[str]:
    """Ids of the ``model_parameters`` descriptors an entry already carries."""
    raw = entry.get(PARAMS_FIELD)
    if not isinstance(raw, list):
        return set()
    return {
        item[PARAMS_ID_FIELD]
        for item in raw
        if isinstance(item, dict) and isinstance(item.get(PARAMS_ID_FIELD), str)
    }

