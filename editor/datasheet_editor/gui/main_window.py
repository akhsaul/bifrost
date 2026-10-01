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
from .fieldeditor import FieldRow
from .models import (
    COL_ID,
    ChangeListModel,
    ModelFilterProxy,
    ModelListModel,
    ParamDescriptorModel,
    build_rows,
)
from .workers import LoadWorker, MergeWorker, SaveWorker

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

        self.model_list = ModelListModel(self)
        self.proxy = ModelFilterProxy(self)
        self.proxy.setSourceModel(self.model_list)
        self.param_model = ParamDescriptorModel(self)
        self.change_model = ChangeListModel(self)

        self._build_ui()
        self._wire()

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
        self.path_label = QLabel("parameters: –   pricing: –   custom: –")
        bar.addWidget(self.path_label, 1)

        self.btn_reload = QPushButton("Load…")
        self.btn_reload.clicked.connect(self._on_load_clicked)
        bar.addWidget(self.btn_reload)

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
        self.params_form = QFormLayout(self.params_fields_box)
        self.params_form.setFieldGrowthPolicy(QFormLayout.AllNonFixedFieldsGrow)
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
        self.pricing_form = QFormLayout(self.pricing_fields_box)
        self.pricing_form.setFieldGrowthPolicy(QFormLayout.AllNonFixedFieldsGrow)
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
        self.parameters_path = Path(params)
        self.pricing_path = Path(pricing)
        self.custom_path = Path(custom) if custom else None
        self._update_path_label()
        self._start_load()

    def _start_load(self) -> None:
        if not self.parameters_path or not self.pricing_path:
            return
        self._set_busy(True, "loading datasheets…")
        worker = LoadWorker(self.parameters_path, self.pricing_path, self.custom_path)
        worker.signals.finished.connect(self._on_loaded)
        worker.signals.failed.connect(lambda msg: self._on_failed("load", msg))
        self.pool.start(worker)

    def _on_loaded(self, payload: dict[str, Any]) -> None:
        self.parameters = payload["parameters"].data
        self.pricing = payload["pricing"].data
        self.overlay = payload["overlay"]
        self.conflicts = payload["conflicts"]

        rows = build_rows(self.parameters, self.pricing, self.overlay, self.conflicts)
        self.model_list.set_rows(rows)
        self.proxy.sort(COL_ID, Qt.AscendingOrder)
        self._populate_facet_combos()
        self._clear_filters()
        self._set_busy(False)
        self.status_label.setText(
            f"{len(rows):,} models · {len(self.overlay):,} custom entries · "
            f"{payload['conflict_count']:,} cross-file disagreement(s)"
        )
        if rows:
            self.table.selectRow(0)
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
        self.path_label.setText(
            f"parameters: {self.parameters_path.name if self.parameters_path else '–'}   "
            f"pricing: {self.pricing_path.name if self.pricing_path else '–'}   "
            f"custom: {self.custom_path.name if self.custom_path else '(not saved yet)'}"
        )

    def _set_busy(self, busy: bool, message: str = "") -> None:
        self._loading = busy
        self.progress.setVisible(busy)
        for button in (self.btn_reload, self.btn_merge, self.btn_save_output, self.btn_save_custom):
            button.setEnabled(not busy and button is not self.btn_reload)
        if busy:
            self.status_label.setText(message)

    def _on_failed(self, what: str, message: str) -> None:
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
        for widget, slot in ((self.search_edit, self._apply_search), (self.provider_combo, self._on_provider_changed),
                             (self.mode_combo, self._on_mode_changed)):
            widget.blockSignals(True)
        self.search_edit.clear()
        self.provider_combo.setCurrentIndex(0)
        self.mode_combo.setCurrentIndex(0)
        for widget, slot in ((self.search_edit, self._apply_search), (self.provider_combo, self._on_provider_changed),
                             (self.mode_combo, self._on_mode_changed)):
            widget.blockSignals(False)
        self.proxy.set_search("")
        self.proxy.set_providers(set())
        self.proxy.set_modes(set())
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

    def _on_load_clicked(self) -> None:
        params, _ = QFileDialog.getOpenFileName(self, "Select model_parameters.json", "",
                                                 "JSON files (*.json)")
        if not params:
            return
        pricing, _ = QFileDialog.getOpenFileName(self, "Select model_pricing.json", "", "JSON files (*.json)")
        if not pricing:
            return
        custom = None
        if self.custom_path and self.custom_path.exists():
            custom = str(self.custom_path)
        else:
            chosen, _ = QFileDialog.getOpenFileName(self, "Select custom overlay (optional)", "",
                                                    "JSON files (*.json)")
            custom = chosen or None
        self.load_paths(params, pricing, custom)

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
