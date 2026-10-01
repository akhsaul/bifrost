"""Typed editors for a single field.

Deliberately widget-based rather than an editable table: entries are sparse (2-48
fields) and values range from bool to nested JSON, so a form of one row per field
is both simpler and safer than in-place table editing.

Values are never silently coerced. ``int``/``float`` use a line edit with a
validator rather than a spin box, because a spin box clamps out-of-range input
and that would quietly rewrite data. Unparseable input shows an inline error and
is not committed.
"""

from __future__ import annotations

import json
from typing import Any, Callable

from PySide6.QtCore import Qt, Signal
from PySide6.QtWidgets import (
    QCheckBox,
    QComboBox,
    QHBoxLayout,
    QLabel,
    QLineEdit,
    QPlainTextEdit,
    QPushButton,
    QSizePolicy,
    QWidget,
)

from ..fields import KNOWN_MODES, classify, is_pricing_read, value_kind
from ..format import number, scaled
from ..merge import PARAMS_FIELD as PARAMS_ARRAY_FIELD

_BADGE_COLORS = {
    "cost": ("#7c3aed", "#ede9fe"),
    "capability": ("#0891b2", "#cffafe"),
    "ignored": ("#9ca3af", "#f3f4f6"),
    "unknown": ("#64748b", "#f1f5f9"),
}
ORIGIN_COLORS = {
    "added": ("#15803d", "#dcfce7"),
    "overridden": ("#1d4ed8", "#dbeafe"),
    "original": ("#475569", "#f8fafc"),
}


class FieldRow(QWidget):
    """One field: label, classification badge, editor, origin badge, reset."""

    changed = Signal(object)  # new value, or _UNSET when reverted

    def __init__(
        self,
        name: str,
        value: Any,
        *,
        section: str,
        origin: str,
        on_edit: Callable[[str, Any], None],
        on_revert: Callable[[str], None],
        parent: QWidget | None = None,
    ) -> None:
        super().__init__(parent)
        self.name = name
        self.section = section
        self._origin = origin
        self._baseline = value
        self._on_edit = on_edit
        self._on_revert = on_revert
        self._current = value

        layout = QHBoxLayout(self)
        layout.setContentsMargins(0, 2, 0, 2)
        layout.setSpacing(8)

        name_label = QLabel(name)
        name_label.setMinimumWidth(230)
        name_label.setToolTip(name)
        layout.addWidget(name_label)

        kind = value_kind(value)
        classification = "unknown"
        if section == "pricing":
            classification = classify(name)
            if classification == "unknown" and not is_pricing_read(name):
                # Surfaced rather than hidden: 222 of the 328 fields in the
                # pricing file are outside Bifrost's read-set, and an editor
                # should make that visible instead of implying the edit counts.
                classification = "ignored"
        if classification != "unknown":
            fg, bg = _BADGE_COLORS[classification]
            badge = QLabel(classification)
            badge.setStyleSheet(f"color:{fg};background:{bg};border-radius:3px;padding:1px 5px;font-size:10px;")
            badge.setToolTip(_badge_tooltip(name, section))
            layout.addWidget(badge)

        self.hint_label: QLabel | None = None
        self.editor = self._build_editor(name, value, kind, section)
        layout.addWidget(self.editor, 1)
        if self.hint_label is not None:
            layout.addWidget(self.hint_label)

        self.origin_label = QLabel(_origin_text(origin))
        fg, bg = ORIGIN_COLORS.get(origin, ORIGIN_COLORS["original"])
        self.origin_label.setStyleSheet(
            f"color:{fg};background:{bg};border-radius:3px;padding:1px 5px;font-size:10px;"
        )
        layout.addWidget(self.origin_label)

        self.reset_button = QPushButton("reset")
        self.reset_button.setFixedWidth(56)
        self.reset_button.setEnabled(origin != "original")
        self.reset_button.clicked.connect(self._revert)
        layout.addWidget(self.reset_button)

        self.error_label = QLabel("")
        self.error_label.setStyleSheet("color:#b91c1c;font-size:11px;")
        self.error_label.hide()
        self._layout = layout

    # -- construction ----------------------------------------------------- #

    def _build_editor(self, name: str, value: Any, kind: str, section: str) -> QWidget:
        if kind == "bool":
            box = QCheckBox()
            box.setChecked(bool(value))
            box.toggled.connect(lambda checked: self._commit(bool(checked)))
            return box

        if kind in {"int", "float"}:
            edit = QLineEdit(_format_number(value))
            edit.setMaximumWidth(200)
            quoted = scaled(value, name)
            if quoted:
                # The edit holds the exact value that gets written to the file;
                # the hint carries the reading a human can actually compare.
                # `0.000003` is exact but hard to reason about, whereas
                # "$3.00 per 1M tokens" is what you compare across models.
                self.hint_label = QLabel(f"~ {quoted}")
                self.hint_label.setStyleSheet("color:#64748b;font-size:11px;")
                self.hint_label.setToolTip(
                    "Scaled for readability. The exact value is what gets saved."
                )
                edit.textChanged.connect(
                    lambda text, lbl=self.hint_label, field=name: lbl.setText(
                        "~ " + (_rescale(text, field) or "not a number")
                    )
                )
            validator = _NumberValidator(kind, self)
            edit.setValidator(validator)
            edit.editingFinished.connect(lambda: self._commit_number(edit.text(), kind))
            return edit

        if name == "mode":
            combo = QComboBox()
            combo.addItems(sorted(KNOWN_MODES))
            combo.setEditable(True)
            current = value if isinstance(value, str) else ""
            if current:
                combo.setCurrentText(current)
            combo.setFixedWidth(180)
            combo.currentTextChanged.connect(lambda text: self._commit(text))
            return combo

        if kind == "string":
            edit = QLineEdit(value if isinstance(value, str) else "")
            edit.editingFinished.connect(lambda: self._commit(edit.text()))
            return edit

        if kind in {"array", "object"}:
            area = QPlainTextEdit(json.dumps(value, indent=2))
            area.setMaximumHeight(110)
            area.setSizePolicy(QSizePolicy.Expanding, QSizePolicy.Preferred)
            area.textChanged.connect(lambda: self._commit_json(area.toPlainText()))
            return area

        edit = QLineEdit(str(value))
        edit.editingFinished.connect(lambda: self._commit(edit.text()))
        return edit

    # -- commit ----------------------------------------------------------- #

    def _commit(self, value: Any) -> None:
        if value == self._baseline:
            return
        self._current = value
        self._set_origin("added" if self._baseline is _MISSING else "overridden")
        self._on_edit(self.name, value)

    def _commit_number(self, text: str, kind: str) -> None:
        parsed = _parse_number(text, kind)
        if parsed is None:
            self._show_error(f"not a valid {kind}")
            return
        self._hide_error()
        if parsed == self._baseline:
            return
        self._current = parsed
        self._set_origin("added" if self._baseline is _MISSING else "overridden")
        self._on_edit(self.name, parsed)

    def _commit_json(self, text: str) -> None:
        try:
            parsed = json.loads(text)
        except json.JSONDecodeError as exc:
            self._show_error(f"invalid JSON: {exc.msg} (line {exc.lineno})")
            return
        self._hide_error()
        if parsed == self._baseline:
            return
        self._current = parsed
        self._set_origin("overridden")
        self._on_edit(self.name, parsed)

    def _revert(self) -> None:
        if self._baseline is _MISSING:
            self._on_revert(self.name)
            return
        self._set_origin("original")
        self.editor.blockSignals(True)
        if isinstance(self.editor, QCheckBox):
            self.editor.setChecked(bool(self._baseline))
        elif isinstance(self.editor, QPlainTextEdit):
            self.editor.setPlainText(json.dumps(self._baseline, indent=2))
        elif isinstance(self.editor, QComboBox):
            self.editor.setCurrentText(str(self._baseline))
        elif isinstance(self.editor, QLineEdit):
            self.editor.setText(_format_number(self._baseline) if isinstance(self._baseline, (int, float))
                                and not isinstance(self._baseline, bool) else str(self._baseline))
        self.editor.blockSignals(False)
        self._on_revert(self.name)

    # -- helpers ---------------------------------------------------------- #

    def _set_origin(self, origin: str) -> None:
        self._origin = origin
        fg, bg = ORIGIN_COLORS[origin]
        self.origin_label.setText(_origin_text(origin))
        self.origin_label.setStyleSheet(
            f"color:{fg};background:{bg};border-radius:3px;padding:1px 5px;font-size:10px;"
        )
        self.reset_button.setEnabled(origin != "original")

    def _show_error(self, message: str) -> None:
        self.error_label.setText(message)
        self.error_label.show()

    def _hide_error(self) -> None:
        self.error_label.hide()


class _Missing:
    """Sentinel for a field that does not exist in the original entry."""

    def __repr__(self) -> str:  # pragma: no cover - debugging aid
        return "<missing>"


_MISSING = _Missing()


def _origin_text(origin: str) -> str:
    return {"added": "added", "overridden": "overridden"}.get(origin, "original")


def _rescale(text: str, field: str) -> str | None:
    """Re-quote a half-typed value so the hint tracks live edits."""
    try:
        return scaled(float(text), field)
    except ValueError:
        return None


def _format_number(value: Any) -> str:
    """Exact, exponent-free text for the edit box.

    The editor must round-trip the literal value in the file, so ``3e-06`` is
    written out as ``0.000003`` rather than prettified into something that would
    re-serialise differently.
    """
    return number(value)


def _parse_number(text: str, kind: str) -> int | float | None:
    text = text.strip()
    if not text:
        return None
    try:
        if kind == "int":
            return int(text)
        value = float(text)
        return int(value) if value.is_integer() and "." not in text and "e" not in text.lower() else value
    except ValueError:
        return None


def _badge_tooltip(name: str, section: str) -> str:
    if section != "pricing":
        return ""
    if name == PARAMS_ARRAY_FIELD:
        return "Parameter-form descriptors; merged into model_parameters.json by id"
    if is_pricing_read(name):
        return "Bifrost reads this field off datasheet.Entry (cost or capability lookup)"
    return ("Bifrost's datasheet.Entry does not declare this field, so Go drops it on load. "
            "It is preserved in the merged file but has no effect until a Go field is added.")


from PySide6.QtGui import QDoubleValidator, QIntValidator, QValidator  # noqa: E402


class _NumberValidator(QValidator):
    """Permissive numeric validator.

    ``intermediate`` values are allowed so a partially typed number (``.``, ``-``)
    does not fight the user; the actual parse happens on commit.
    """

    def __init__(self, kind: str, parent: QWidget | None = None) -> None:
        super().__init__(parent)
        if kind == "int":
            # QIntValidator takes a C++ int, so its range is 32-bit. Every
            # integer in these datasheets (token limits, pixel counts, batch
            # sizes) fits comfortably; wider values simply skip validation
            # rather than being clamped.
            self._delegate = QIntValidator(-(2**31), 2**31 - 1, self)
        else:
            self._delegate = QDoubleValidator(-1e308, 1e308, 12, self)

    def validate(self, text: str, pos: int):  # noqa: ANN001, ANN201
        state = self._delegate.validate(text, pos)
        return (QValidator.Intermediate, text, pos) if state[0] == QValidator.Intermediate else state

    def fixup(self, text: str) -> str:  # noqa: ANN201
        return self._delegate.fixup(text)
