"""Dialogs for adding a new model or a new field.

Adding data is a first-class part of editing these files, so it gets real input
dialogs rather than being reachable only by hand-editing the overlay JSON.

Field placement is checked with :func:`merge.check_field_placement` -- the same
function the merge itself uses -- so the editor cannot offer a placement that the
merge would later reject.

The Add Field dialog is built around one problem: **the datasheets carry no
documentation.** Nothing in the JSON says what ``reasoning_effort_levels`` means
or that it holds a list of strings, so a user adding a field has to guess both the
name and the value shape. :mod:`datasheet_editor.fieldinfo` assembles what is
known from the Go source and the loaded data, and this dialog shows it, then picks
a control that fits:

===========================  =========================================
Shape                        Control
===========================  =========================================
true or false                checkbox
text, closed vocabulary      dropdown (editable, so a value the datasheet
                             has not caught up with is still typeable)
list of text, closed set     tick-list of the known values
anything else                JSON text area, as before
===========================  =========================================

The parameters dialog also routes: ``model_parameters`` array entries are a
different vocabulary from top-level columns, and writing ``reasoning_effort`` as
a top-level key would produce a field Go never reads. The target selector keeps
the two apart.
"""

from __future__ import annotations

import json
from typing import Any

from PySide6.QtWidgets import (
    QCheckBox,
    QComboBox,
    QDialog,
    QDialogButtonBox,
    QFormLayout,
    QGroupBox,
    QLabel,
    QLineEdit,
    QListWidget,
    QListWidgetItem,
    QPlainTextEdit,
    QScrollArea,
    QSizePolicy,
    QVBoxLayout,
    QWidget,
)

from ..fields import KNOWN_MODES
from ..fieldinfo import DescriptorInfo, FieldInfo, ParameterCatalog
from ..merge import PARAMS_FIELD, check_field_placement

#: Which kind of thing the field dialog is writing.
TARGET_FIELD = "field"
TARGET_DESCRIPTOR = "descriptor"


class AddModelDialog(QDialog):
    """Collect the identity of a brand-new model.

    ``provider`` and ``mode`` are offered as editable combo boxes seeded from the
    values already present, since those two drive the provider/mode filters and
    are what make a new model findable.
    """

    def __init__(
        self,
        providers: list[str],
        modes: list[str],
        existing_ids: set[str],
        parent=None,
    ) -> None:
        super().__init__(parent)
        self.setWindowTitle("Add model")
        self.setMinimumWidth(460)
        self._existing = existing_ids
        self._result: dict[str, Any] = {}

        layout = QVBoxLayout(self)
        intro = QLabel(
            "Adds the model to your custom overlay. It will appear in the merged "
            "output files, but the originals are never changed."
        )
        intro.setWordWrap(True)
        layout.addWidget(intro)

        form = QFormLayout()
        self.id_edit = QLineEdit()
        self.id_edit.setPlaceholderText("e.g. my-org/my-model-v1")
        self.id_edit.textChanged.connect(self._validate)
        form.addRow("Model ID", self.id_edit)

        self.provider_combo = QComboBox()
        self.provider_combo.setEditable(True)
        self.provider_combo.addItem("")
        self.provider_combo.addItems(providers)
        form.addRow("Provider", self.provider_combo)

        self.mode_combo = QComboBox()
        self.mode_combo.setEditable(True)
        self.mode_combo.addItem("")
        self.mode_combo.addItems(sorted(KNOWN_MODES))
        self.mode_combo.setCurrentText("chat")
        form.addRow("Mode", self.mode_combo)

        self.with_pricing = QComboBox()
        self.with_pricing.addItem("Yes - also add a pricing section", True)
        self.with_pricing.addItem("No - parameters only", False)
        form.addRow("Pricing section", self.with_pricing)

        layout.addLayout(form)

        self.error_label = QLabel("")
        self.error_label.setWordWrap(True)
        self.error_label.setStyleSheet("color:#b91c1c;")
        layout.addWidget(self.error_label)

        buttons = QDialogButtonBox(QDialogButtonBox.Ok | QDialogButtonBox.Cancel)
        buttons.accepted.connect(self._accept)
        buttons.rejected.connect(self.reject)
        layout.addWidget(buttons)
        self._buttons = buttons
        self._validate()

    def _validate(self) -> None:
        model_id = self.id_edit.text().strip()
        if not model_id:
            self._set_error("A model ID is required.")
        elif model_id in self._existing:
            self._set_error(
                f"{model_id} already exists. Use a different ID, or edit the existing entry."
            )
        else:
            self._set_error("")

    def _set_error(self, message: str) -> None:
        self.error_label.setText(message)
        ok = self._buttons.button(QDialogButtonBox.Ok)
        if ok is not None:
            ok.setEnabled(not message)

    def _accept(self) -> None:
        # Re-check at accept time: the ID field is free text, so it can still
        # hold a duplicate by time OK is pressed.
        self._validate()
        if self.error_label.text():
            return
        super().accept()

    def values(self) -> dict[str, Any]:
        model_id = self.id_edit.text().strip()
        provider = self.provider_combo.currentText().strip()
        mode = self.mode_combo.currentText().strip() or "chat"
        include_pricing = bool(self.with_pricing.currentData())

        sections: dict[str, dict[str, Any]] = {
            "parameters": {"mode": mode},
        }
        if provider:
            sections["parameters"]["provider"] = provider
        if include_pricing:
            sections["pricing"] = {"mode": mode}
            if provider:
                sections["pricing"]["provider"] = provider
        return {"id": model_id, "sections": sections}


# --------------------------------------------------------------------------- #
# value controls
# --------------------------------------------------------------------------- #


class ValueSetEditor(QWidget):
    """A tick-list of known values for a list-of-text field.

    For a field like ``reasoning_effort_levels`` the value is a list, and the
    datasheet already enumerates the vocabulary. A plain multi-select dropdown
    would mean opening and closing a popup per entry, so the options are shown as
    a scrollable tick-list with an escape hatch for anything not yet recorded.
    """

    def __init__(self, options: list[str], parent=None) -> None:
        super().__init__(parent)
        layout = QVBoxLayout(self)
        layout.setContentsMargins(0, 0, 0, 0)
        layout.setSpacing(4)

        self.list = QListWidget()
        self.list.setSelectionMode(QListWidget.NoSelection)
        for option in options:
            item = QListWidgetItem(option)
            item.setFlags(item.flags() | _ITEM_CHECKABLE)
            item.setCheckState(_UNCHECKED)
            self.list.addItem(item)
        self.list.setFixedHeight(min(150, 12 + 22 * len(options)))

        scroll = QScrollArea()
        scroll.setWidget(self.list)
        scroll.setWidgetResizable(True)
        scroll.setFrameShape(_NO_FRAME)
        # A scroll area expands vertically by default, which stretches the tick
        # list until the "add your own" box underneath is pushed to the bottom
        # of the dialog, far from the list it belongs to.
        scroll.setFixedHeight(self.list.height() + 2)
        layout.addWidget(scroll)

        row = QWidget()
        row_layout = QVBoxLayout(row)
        row_layout.setContentsMargins(0, 0, 0, 0)
        row_layout.setSpacing(2)
        row_layout.addWidget(_caption("Not in the list above? Add it here:"))
        self.custom_edit = QLineEdit()
        self.custom_edit.setPlaceholderText("comma-separated, then press Enter")
        self.custom_edit.returnPressed.connect(self._add_custom)
        row_layout.addWidget(self.custom_edit)
        layout.addWidget(row)
        self._row = row
        self.setSizePolicy(QSizePolicy.Policy.Expanding, QSizePolicy.Policy.Fixed)

    def _add_custom(self) -> None:
        text = self.custom_edit.text().strip()
        if not text:
            return
        existing = {self.list.item(i).text() for i in range(self.list.count())}
        for part in (p.strip() for p in text.split(",")):
            if not part or part in existing:
                continue
            item = QListWidgetItem(part)
            item.setFlags(item.flags() | _ITEM_CHECKABLE)
            item.setCheckState(_CHECKED)
            self.list.addItem(item)
            existing.add(part)
        self.custom_edit.clear()

    def values(self) -> list[str]:
        out: list[str] = []
        for i in range(self.list.count()):
            item = self.list.item(i)
            if item.checkState() == _CHECKED:
                out.append(item.text())
        extra = [p.strip() for p in self.custom_edit.text().split(",") if p.strip()]
        return out + [v for v in extra if v not in out]


def _caption(text: str) -> QLabel:
    label = QLabel(text)
    label.setStyleSheet("color:#64748b;font-size:11px;")
    return label


# Qt enum aliases, resolved once so the module reads without dotted names.
from PySide6.QtCore import Qt  # noqa: E402
from PySide6.QtWidgets import QFrame  # noqa: E402

_ITEM_CHECKABLE = Qt.ItemFlag.ItemIsUserCheckable
_UNCHECKED = Qt.CheckState.Unchecked
_CHECKED = Qt.CheckState.Checked
_NO_FRAME = QFrame.Shape.NoFrame


# --------------------------------------------------------------------------- #
# description panel
# --------------------------------------------------------------------------- #


class FieldDescription(QGroupBox):
    """Read-only explanation of the selected field.

    Assembled from the Go doc comment where one exists and from the loaded data
    otherwise, so there is always *something* factual to read: the shape, how many
    models carry the field, which file Bifrost reads it from, and the value
    vocabulary when it is small enough to be a real choice.
    """

    def __init__(self, parent=None) -> None:
        super().__init__("What this field is", parent)
        layout = QVBoxLayout(self)
        layout.setContentsMargins(10, 8, 10, 8)
        layout.setSpacing(4)

        self.heading = QLabel("")
        heading_font = self.heading.font()
        heading_font.setBold(True)
        self.heading.setFont(heading_font)
        self.heading.setWordWrap(True)
        layout.addWidget(self.heading)

        self.body = QLabel("")
        self.body.setWordWrap(True)
        self.body.setTextInteractionFlags(Qt.TextInteractionFlag.TextBrowserInteraction)
        self.body.setStyleSheet("color:#334155;")
        layout.addWidget(self.body)

        self.read_note = QLabel("")
        self.read_note.setWordWrap(True)
        layout.addWidget(self.read_note)

    def clear(self, message: str = "Pick a field name to see what it means.") -> None:
        self.heading.setText("")
        self.body.setText(message)
        self._style_note("")

    def show_field(self, info: FieldInfo) -> None:
        self.heading.setText(f"{info.name} — {info.type_sentence()}")
        lines: list[str] = []
        if info.description:
            lines.append(info.description)
        lines.append(info.summary())
        self.body.setText("<br><br>".join(lines))

        colors = {"parameters": ("#15803d", "#dcfce7"),
                  "pricing": ("#b45309", "#fef3c7"),
                  "none": ("#b91c1c", "#fee2e2")}
        fg, bg = colors[info.read_from]
        self.read_note.setText(info.read_sentence())
        self.read_note.setStyleSheet(f"color:{fg};background:{bg};border-radius:3px;padding:4px 7px;")

    def show_descriptor(self, info: DescriptorInfo) -> None:
        self.heading.setText(f"{info.param_id} — {info.type_sentence()}")
        lines: list[str] = []
        if info.label:
            lines.append(f"<b>{info.label}</b>")
        if info.description:
            lines.append(info.description)
        detail = [f"published by {info.models:,} model(s)"]
        if info.minimum is not None and info.maximum is not None:
            detail.append(f"range {_bound(info.minimum)} to {_bound(info.maximum)}")
        if info.min_elements is not None or info.max_elements is not None:
            low = info.min_elements if info.min_elements is not None else 0
            high = info.max_elements if info.max_elements is not None else "any"
            detail.append(f"{low} to {high} entries")
        if info.enum_values:
            detail.append("allowed: " + ", ".join(info.enum_values))
        lines.append("; ".join(detail) + ".")
        if info.conflicts:
            lines.append("Note: " + "; ".join(info.conflicts) + ".")
        self.body.setText("<br><br>".join(lines))

        self.read_note.setText(
            "Bifrost reads only the descriptor's id, to build the request-parameter "
            "allowlist. The rest is passed to the prompt playground unchanged."
        )
        self.read_note.setStyleSheet(
            "color:#0369a1;background:#e0f2fe;border-radius:3px;padding:4px 7px;"
        )

    def _style_note(self, message: str) -> None:
        self.read_note.setText(message)
        self.read_note.setStyleSheet("")


def _bound(value: float) -> str:
    from ..format import number

    text = number(value)
    head, _, tail = text.partition(".")
    if tail in {"0", "0.0"}:
        tail = ""
    return f"{int(float(head)):,}" + (f".{tail}" if tail else "")


# --------------------------------------------------------------------------- #
# Add field
# --------------------------------------------------------------------------- #


class AddFieldDialog(QDialog):
    """Collect a single new field for one section of one model.

    The field name is an editable combo box seeded from the names that already
    appear in the target dataset, so the common case is a pick rather than a
    typo. Selecting one shows what the field is and swaps the value control to
    match its shape.
    """

    def __init__(
        self,
        section: str,
        model_id: str,
        known_fields: list[str],
        existing: set[str],
        catalog: ParameterCatalog | None = None,
        descriptor_names: list[str] | None = None,
        descriptors_present: set[str] | None = None,
        parent=None,
    ) -> None:
        super().__init__(parent)
        self.setWindowTitle(f"Add {section} field")
        self.setMinimumWidth(560)
        self._section = section
        self._model_id = model_id
        self._existing = existing
        #: Fields present anywhere in the target dataset. This is what decides
        #: whether a cost-looking name is a genuine cost field or just a name
        #: that happens to appear in the parameters dataset.
        self._known = set(known_fields)
        self._catalog = catalog
        self._descriptors_present = set(descriptors_present or ())

        # Descriptors only exist in the parameters file, and routing them into
        # the pricing section is rejected by the merge, so the choice is only
        # offered where it is legal.
        self._descriptor_names = list(descriptor_names or ())
        self._can_target_descriptor = section == "parameters" and bool(self._descriptor_names)

        layout = QVBoxLayout(self)
        intro = QLabel(f"New field for {model_id} → {section}")
        layout.addWidget(intro)

        form = QFormLayout()

        self.target_combo = QComboBox()
        if self._can_target_descriptor:
            self.target_combo.addItem("Top-level field (a column of the entry)", TARGET_FIELD)
            self.target_combo.addItem(
                f"Parameter descriptor (an entry of {PARAMS_FIELD}[])", TARGET_DESCRIPTOR
            )
            self.target_combo.currentIndexChanged.connect(self._on_target_changed)
            form.addRow("Add to", self.target_combo)

        self.name_combo = QComboBox()
        self.name_combo.setEditable(True)
        self.name_combo.addItem("")
        self.name_combo.currentTextChanged.connect(self._on_name_changed)
        form.addRow("Field name", self.name_combo)

        layout.addLayout(form)

        self.description = FieldDescription()
        layout.addWidget(self.description)

        form2 = QFormLayout()
        self.value_host = QWidget()
        self.value_layout = QVBoxLayout(self.value_host)
        self.value_layout.setContentsMargins(0, 0, 0, 0)
        form2.addRow("Value", self.value_host)
        layout.addLayout(form2)

        self.error_label = QLabel("")
        self.error_label.setWordWrap(True)
        self.error_label.setStyleSheet("color:#b91c1c;")
        layout.addWidget(self.error_label)

        buttons = QDialogButtonBox(QDialogButtonBox.Ok | QDialogButtonBox.Cancel)
        buttons.accepted.connect(self._accept)
        buttons.rejected.connect(self.reject)
        layout.addWidget(buttons)
        self._buttons = buttons

        self._editor: QWidget | None = None
        self._checkbox: QCheckBox | None = None
        self._combo: QComboBox | None = None
        self._area: QPlainTextEdit | None = None
        self._set_editor_choices()

    # -- target / name ----------------------------------------------------- #

    @property
    def target(self) -> str:
        """``"field"`` for a top-level column, ``"descriptor"`` for an array entry."""
        if not self._can_target_descriptor:
            return TARGET_FIELD
        return self.target_combo.currentData() or TARGET_FIELD

    def is_descriptor(self) -> bool:
        return self.target == TARGET_DESCRIPTOR

    def _on_target_changed(self) -> None:
        self._set_editor_choices()
        self._rebuild_value_editor()
        self._validate()

    def _set_editor_choices(self) -> None:
        """Refill the name list for the chosen target, keeping the typed text."""
        current = self.name_combo.currentText().strip()
        names = self._descriptor_names if self.is_descriptor() else sorted(self._known)
        self.name_combo.blockSignals(True)
        self.name_combo.clear()
        self.name_combo.addItem("")
        self.name_combo.addItems(names)
        if current:
            self.name_combo.setCurrentText(current)
        self.name_combo.blockSignals(False)
        self._on_name_changed(current)

    def _on_name_changed(self, _text: str = "") -> None:
        self._rebuild_value_editor()
        self._validate()

    # -- description + value control ---------------------------------------- #

    def _clear_value_area(self) -> None:
        """Empty the value row, keeping the row itself in the form.

        Only the control inside the row is removed. Tearing down ``value_host``
        as well would detach the row from the form layout, and the new control
        would land somewhere the user cannot see it.
        """
        while self.value_layout.count():
            widget = self.value_layout.takeAt(0).widget()
            if widget is not None:
                widget.setParent(None)
        self._editor = self._checkbox = self._combo = self._area = None

    def _install(self, widget: QWidget) -> None:
        self.value_layout.addWidget(widget)
        self._editor = widget

    def _rebuild_value_editor(self) -> None:
        """Swap the value control to match the selected field's shape."""
        self._clear_value_area()
        name = self.name_combo.currentText().strip()

        if self.is_descriptor():
            self._install_descriptor_editor(name)
            return
        self._install_field_editor(name)

    def _install_field_editor(self, name: str) -> None:
        info = self._catalog.field(name) if (self._catalog and name) else None
        if info is None:
            self.description.clear(
                "No record of this field. Nothing is known about its type, so type the "
                "value as JSON."
                if name
                else "Pick a field name to see what it means."
            )
            self._install_text_area(_free_text_hint())
            return

        self.description.show_field(info)

        if info.kind == "bool":
            box = QCheckBox("true")
            box.setChecked(bool(info.default))
            self._checkbox = box
            self._install(box)
            return

        if info.enum_values:
            combo = QComboBox()
            combo.setEditable(True)  # a value the datasheet has not caught up with
            combo.addItem("")
            combo.addItems(info.enum_values)
            if isinstance(info.default, str):
                combo.setCurrentText(info.default)
            self._combo = combo
            self._install(combo)
            return

        if info.enum_elements:
            self._install(ValueSetEditor(list(info.enum_elements)))
            return

        hint = _field_hint(info)
        if info.kind in {"int", "float"}:
            self._install_text_area(hint, single_line=True)
        else:
            self._install_text_area(hint)

    def _install_descriptor_editor(self, param_id: str) -> None:
        info = self._catalog.descriptor(param_id) if (self._catalog and param_id) else None
        if info is None:
            self.description.clear(
                "No other model publishes this parameter, so its type is unknown. "
                "It will be added with just an id, which is all Bifrost reads."
                if param_id
                else "Pick a parameter to see its type."
            )
            self._install_text_area(_free_text_hint())
            return

        self.description.show_descriptor(info)

        if info.kind == "boolean":
            box = QCheckBox("true")
            box.setChecked(bool(info.default))
            self._checkbox = box
            self._install(box)
            return

        if info.enum_values:
            combo = QComboBox()
            combo.setEditable(True)
            combo.addItem("")
            combo.addItems(info.enum_values)
            if isinstance(info.default, str):
                combo.setCurrentText(info.default)
            self._combo = combo
            self._install(combo)
            return

        self._install_text_area(_descriptor_hint(info))

    def _install_text_area(self, hint: str, *, single_line: bool = False) -> None:
        if single_line:
            edit = QLineEdit()
            edit.setPlaceholderText(hint)
            self._area = edit  # type: ignore[assignment]
            self._install(edit)
            return
        area = QPlainTextEdit()
        area.setPlaceholderText(hint)
        area.setMaximumHeight(90)
        self._area = area
        self._install(area)

    # -- validation -------------------------------------------------------- #

    def _validate(self) -> None:
        name = self.name_combo.currentText().strip()
        if not name:
            self._set_error("A field name is required.")
            return

        if self.is_descriptor():
            if name in self._descriptors_present:
                self._set_error(
                    f"{name} is already a parameter of this model. Edit its value instead."
                )
            else:
                self._set_error("")
            return

        if name in self._existing:
            self._set_error(f"{name} is already present on this model. Edit its value instead.")
            return
        problem = check_field_placement("(new)", self._section, name, self._known)
        self._set_error(problem or "")

    def _set_error(self, message: str) -> None:
        self.error_label.setText(message)
        ok = self._buttons.button(QDialogButtonBox.Ok)
        if ok is not None:
            ok.setEnabled(not message)

    def _accept(self) -> None:
        # Re-check at accept time: the combo is editable, so the name can change
        # after validation ran.
        self._validate()
        if self.error_label.text():
            return
        super().accept()

    # -- text access for tests and scripted use ---------------------------- #

    @property
    def value_edit(self) -> QLineEdit | QPlainTextEdit | None:
        """The current text-based value control, or ``None`` for a typed control.

        The control is swapped as the field changes, so callers that need to set a
        value programmatically should go through :meth:`set_value_text` rather
        than caching a widget.
        """
        if isinstance(self._area, (QLineEdit, QPlainTextEdit)):
            return self._area
        return None

    def set_value_text(self, text: str) -> bool:
        """Type ``text`` into the current control. False when there is no text box."""
        editor = self.value_edit
        if editor is None:
            return False
        if isinstance(editor, QPlainTextEdit):
            editor.setPlainText(text)
        else:
            editor.setText(text)
        return True

    # -- result ------------------------------------------------------------ #

    def values(self) -> tuple[str, Any]:
        """``(name, value)`` for a top-level field, or ``(id, descriptor)``.

        The descriptor form is the whole ``model_parameters`` element, because
        the merge keys that array on ``id``: supplying one merges into the
        existing descriptor of the same id and reports a one-line change, instead
        of replacing the model's entire parameter array.
        """
        name = self.name_combo.currentText().strip()
        if self.is_descriptor():
            return name, self._descriptor_value(name)
        return name, self._field_value()

    def _field_value(self) -> Any:
        if self._checkbox is not None:
            return bool(self._checkbox.isChecked())
        if self._combo is not None:
            return self._combo.currentText().strip()
        if isinstance(self._area, QPlainTextEdit):
            return _parse_value(self._area.toPlainText().strip())
        if isinstance(self._area, QLineEdit):
            return _parse_value(self._area.text().strip())
        if isinstance(self._editor, ValueSetEditor):
            return self._editor.values()
        return ""

    def _descriptor_value(self, param_id: str) -> dict[str, Any]:
        """Build a ``model_parameters`` element.

        Carries the metadata every other model records for this id, so the
        playground renders a real control instead of a bare entry. Bifrost only
        reads ``id``; the rest is passed through to the UI.
        """
        info = self._catalog.descriptor(param_id) if self._catalog else None
        value: dict[str, Any] = {"id": param_id}

        if info is not None:
            if info.label:
                value["label"] = info.label
            if info.kind and info.kind != "unknown":
                value["type"] = info.kind
            if info.description:
                value["helpText"] = info.description
            if info.enum_values:
                value["options"] = [{"label": v, "value": v} for v in info.enum_values]
            if info.kind == "number" and (info.minimum is not None or info.maximum is not None):
                bounds: dict[str, Any] = {}
                if info.minimum is not None:
                    bounds["min"] = info.minimum
                if info.maximum is not None:
                    bounds["max"] = info.maximum
                value["range"] = bounds

        default = self._field_value()
        if default != "":
            value["default"] = default
        return value


def _free_text_hint() -> str:
    return 'JSON is parsed when valid: 0.0000025, true, "chat", ["us-east-1"], {"min": 0}'


def _field_hint(info: FieldInfo) -> str:
    if info.kind in {"int", "float"}:
        if info.minimum is not None and info.maximum is not None:
            return (
                f"Observed between {_bound(info.minimum)} and {_bound(info.maximum)} "
                "-- plain number, no quotes"
            )
        return "Plain number, no quotes"
    if info.kind == "array":
        return f'JSON list, e.g. {json.dumps(["value"])}'
    if info.kind == "object":
        return 'JSON object, e.g. {"min": 0, "max": 10}'
    if info.kind == "string":
        return "Text. Leave unquoted unless the value itself is JSON."
    return _free_text_hint()


def _descriptor_hint(info: DescriptorInfo) -> str:
    if info.kind == "number":
        if info.minimum is not None and info.maximum is not None:
            return f"Between {_bound(info.minimum)} and {_bound(info.maximum)}, plain number"
        return "Plain number, no quotes"
    if info.kind == "array":
        if info.min_elements is not None or info.max_elements is not None:
            low = info.min_elements if info.min_elements is not None else 0
            high = info.max_elements if info.max_elements is not None else "any"
            return f"JSON list of {low} to {high} entries, e.g. [\"value\"]"
        return 'JSON list, e.g. ["value"]'
    if info.kind == "object":
        return "JSON object"
    return _free_text_hint()


def _parse_value(raw: str) -> Any:
    """Parse the entered text as JSON, falling back to the literal string."""
    if raw == "":
        return ""
    try:
        return json.loads(raw)
    except json.JSONDecodeError:
        return raw