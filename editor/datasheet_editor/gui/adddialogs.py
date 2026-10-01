"""Dialogs for adding a new model or a new field.

Adding data is a first-class part of editing these files, so it gets real input
dialogs rather than being reachable only by hand-editing the overlay JSON.

Field placement is checked with :func:`merge.check_field_placement` -- the same
function the merge itself uses -- so the editor cannot offer a placement that the
merge would later reject.
"""

from __future__ import annotations

import json
from typing import Any

from PySide6.QtWidgets import (
    QComboBox,
    QDialog,
    QDialogButtonBox,
    QFormLayout,
    QLabel,
    QLineEdit,
    QPlainTextEdit,
    QVBoxLayout,
)

from ..fields import KNOWN_MODES
from ..merge import check_field_placement


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
        # hold a duplicate by the time OK is pressed.
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


class AddFieldDialog(QDialog):
    """Collect a single new field for one section of one model.

    The field name is an editable combo box seeded from the fields that already
    appear in the target dataset, so the common case is a pick rather than a
    typo. The value is parsed as JSON when it looks like JSON, which makes
    numbers, booleans, arrays and objects all reachable from one field.
    """

    def __init__(
        self,
        section: str,
        model_id: str,
        known_fields: list[str],
        existing: set[str],
        parent=None,
    ) -> None:
        super().__init__(parent)
        self.setWindowTitle(f"Add {section} field")
        self.setMinimumWidth(520)
        self._section = section
        self._existing = existing
        #: Fields present anywhere in the target dataset. This is what decides
        #: whether a cost-looking name is a genuine cost field or just a name
        #: that happens to appear in the parameters dataset.
        self._known = set(known_fields)

        layout = QVBoxLayout(self)
        layout.addWidget(QLabel(f"New field for {model_id} → {section}"))

        form = QFormLayout()
        self.name_combo = QComboBox()
        self.name_combo.setEditable(True)
        self.name_combo.addItem("")
        self.name_combo.addItems(known_fields)
        self.name_combo.currentTextChanged.connect(self._validate)
        form.addRow("Field name", self.name_combo)

        self.value_edit = QPlainTextEdit()
        self.value_edit.setPlaceholderText(
            "JSON is parsed when valid: 0.0000025, true, \"chat\", [\"us-east-1\"], {\"min\": 0}"
        )
        self.value_edit.setMaximumHeight(90)
        form.addRow("Value", self.value_edit)

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
        name = self.name_combo.currentText().strip()
        if not name:
            self._set_error("A field name is required.")
        elif name in self._existing:
            self._set_error(f"{name} is already present on this model. Edit its value instead.")
        else:
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

    def values(self) -> tuple[str, Any]:
        name = self.name_combo.currentText().strip()
        raw = self.value_edit.toPlainText().strip()
        return name, _parse_value(raw)


def _parse_value(raw: str) -> Any:
    """Parse the entered text as JSON, falling back to the literal string."""
    if raw == "":
        return ""
    try:
        return json.loads(raw)
    except json.JSONDecodeError:
        return raw