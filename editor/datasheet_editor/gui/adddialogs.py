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
from dataclasses import dataclass
from pathlib import Path
from typing import Any

from PySide6.QtCore import Qt
from PySide6.QtWidgets import (
    QCheckBox,
    QComboBox,
    QDialog,
    QDialogButtonBox,
    QFileDialog,
    QFormLayout,
    QGroupBox,
    QHBoxLayout,
    QLabel,
    QLineEdit,
    QListWidget,
    QListWidgetItem,
    QPlainTextEdit,
    QPushButton,
    QScrollArea,
    QSizePolicy,
    QVBoxLayout,
    QWidget,
)

from ..fields import KNOWN_MODES
from ..fieldinfo import DescriptorInfo, FieldInfo, ParameterCatalog
from ..merge import PARAMS_FIELD, check_field_placement
from ..output import ADD_NUMBER, MAX_NUMBERED_SUFFIX, OVERWRITE, OutputConflict, numbered_name

#: Which kind of thing the field dialog is writing.
TARGET_FIELD = "field"
TARGET_DESCRIPTOR = "descriptor"

#: Default output filenames prefilling the Save To dialog (dash form, written
#: into the current workdir). Deliberately distinct from the canonical
#: underscore names the CLI and Bifrost read, so a default save never silently
#: poses as a canonical datasheet.
DEFAULT_PARAMETERS_OUTPUT = "model-parameters.json"
DEFAULT_PRICING_OUTPUT = "model-pricing.json"
DEFAULT_CUSTOM_OUTPUT = "custom-metadata.json"

#: Which save button closed the Save To dialog.
SAVE_ACTION_OUTPUT = "output"
SAVE_ACTION_CUSTOM = "custom"


class OutputConflictDialog(QDialog):
    """Ask what to do about output files that are already there.

    Two buttons, because those are the two real answers: replace the existing
    files, or keep them and write numbered ones beside them. Escape and Cancel
    both mean "do not write", which is the safe reading of a dialog that appears
    only when something is about to be destroyed.

    The dialog states the size of what would be overwritten. "model_pricing.json
    exists" is easy to click through; "you are replacing the 28 MB file you loaded
    an hour ago" is not.
    """

    def __init__(self, conflict: OutputConflict, parent: QWidget | None = None) -> None:
        super().__init__(parent)
        self.setWindowTitle("Output files already exist")
        self.setModal(True)
        self._policy: str | None = None

        layout = QVBoxLayout(self)
        layout.setSpacing(10)

        plural = len(conflict.conflicts) > 1
        headline = QLabel(
            f"{'These files' if plural else 'This file'} already "
            f"{'exist' if plural else 'exists'} in the output folder:"
        )
        headline.setWordWrap(True)
        layout.addWidget(headline)

        listing = QGroupBox()
        listing_layout = QVBoxLayout(listing)
        for path in conflict.conflicts:
            row = QLabel(f"  {path.name}  —  {_describe_file(path)}")
            row.setStyleSheet("font-family: monospace;")
            listing_layout.addWidget(row)
        numbered = conflict.numbered
        if numbered:
            keep = QLabel("  keeping them writes:  " + ",  ".join(
                sorted(p.name for p in numbered.values())
            ))
            keep.setStyleSheet("color:#475569; font-family: monospace;")
            listing_layout.addWidget(keep)
        layout.addWidget(listing)

        note = QLabel(
            "Both files are named together: parameters and pricing are read as a "
            "pair, so the numbered names apply to both."
        )
        note.setWordWrap(True)
        note.setStyleSheet("color:#475569;")
        layout.addWidget(note)

        buttons = QDialogButtonBox()
        self.btn_overwrite = QPushButton("&Overwrite")
        self.btn_overwrite.setToolTip("Replace the existing file(s) with the merged result")
        self.btn_add_number = QPushButton("Add &Number")
        self.btn_add_number.setToolTip("Keep the existing file(s) and write numbered ones beside them")
        cancel = buttons.addButton(QDialogButtonBox.Cancel)
        # AcceptRole, not ActionRole: both close the dialog, and the policy is
        # carried in _policy rather than inferred from which button was pressed.
        buttons.addButton(self.btn_overwrite, QDialogButtonBox.AcceptRole)
        buttons.addButton(self.btn_add_number, QDialogButtonBox.AcceptRole)
        cancel.clicked.connect(self.reject)
        # Cancel is the default so that a stray Return, or a dialog that appears
        # while the user is typing elsewhere, cannot destroy a file.
        cancel.setDefault(True)
        layout.addWidget(buttons)

    def policy(self) -> str | None:
        """``OVERWRITE``, ``ADD_NUMBER``, or None if the user backed out."""
        return self._policy

    def accept_overwrite(self) -> None:  # noqa: N802
        self._policy = OVERWRITE
        self.accept()

    def accept_add_number(self) -> None:  # noqa: N802
        self._policy = ADD_NUMBER
        self.accept()


class ZedImportDialog(QDialog):
    """Confirm a Zed /models import into the custom overlay.

    States what would happen before anything is written: how many entries are
    new, how many already exist (skipped unless overwrite is ticked), and which
    keys those are. Cancel leaves the overlay untouched.
    """

    def __init__(
        self,
        source_name: str,
        added: list[str],
        skipped: list[str],
        parent: QWidget | None = None,
    ) -> None:
        super().__init__(parent)
        self.setWindowTitle("Import Zed models")
        self.setMinimumWidth(520)
        self._overwrite = False

        layout = QVBoxLayout(self)

        headline = QLabel(
            f"{source_name} holds {len(added) + len(skipped)} model(s): "
            f"{len(added)} new, {len(skipped)} already in the overlay."
        )
        headline.setWordWrap(True)
        layout.addWidget(headline)

        listing = QGroupBox()
        listing_layout = QVBoxLayout(listing)
        self._list_text = QPlainTextEdit()
        self._list_text.setReadOnly(True)
        self._list_text.setMaximumHeight(220)
        lines = [f"+ {key}" for key in added]
        lines += [f"= {key} (already present)" for key in skipped]
        self._list_text.setPlainText("\n".join(lines) if lines else "nothing to import")
        listing_layout.addWidget(self._list_text)
        layout.addWidget(listing)

        self.overwrite_box = QCheckBox(
            f"Overwrite the {len(skipped)} existing entr{'y' if len(skipped) == 1 else 'ies'} "
            "with the Zed data"
        )
        self.overwrite_box.setEnabled(bool(skipped))
        self.overwrite_box.stateChanged.connect(self._on_overwrite_changed)
        layout.addWidget(self.overwrite_box)

        note = QLabel(
            "New entries are appended as zed/<id>. Existing ones are left "
            "untouched unless overwrite is ticked. Nothing is written until "
            "you press Import, and Save To… (Save Custom) is still needed afterwards."
        )
        note.setWordWrap(True)
        note.setStyleSheet("color:#475569;")
        layout.addWidget(note)

        buttons = QDialogButtonBox(QDialogButtonBox.Ok | QDialogButtonBox.Cancel)
        buttons.button(QDialogButtonBox.Ok).setText("&Import")
        buttons.accepted.connect(self._accept)
        buttons.rejected.connect(self.reject)
        layout.addWidget(buttons)
        self._buttons = buttons
        self._update_ok()

    def _on_overwrite_changed(self) -> None:
        self._overwrite = self.overwrite_box.isChecked()

    def _update_ok(self) -> None:
        ok = self._buttons.button(QDialogButtonBox.Ok)
        if ok is not None:
            ok.setEnabled(True)

    def _accept(self) -> None:
        self._overwrite = self.overwrite_box.isChecked() and self.overwrite_box.isEnabled()
        super().accept()

    def overwrite_existing(self) -> bool:
        """True when existing zed/* entries should be replaced."""
        return self._overwrite


class OpencodeImportDialog(QDialog):
    """Confirm an Opencode catalog import into the custom overlay.

    States what would happen before anything is written: how many entries are
    new, how many already exist (skipped unless overwrite is ticked), how many
    tier drops the import would carry, and which keys those are. Cancel leaves
    the overlay untouched.
    """

    def __init__(
        self,
        source_name: str,
        added: list[str],
        skipped: list[str],
        warnings: list[str] | None = None,
        parent: QWidget | None = None,
    ) -> None:
        super().__init__(parent)
        self.setWindowTitle("Import Opencode models")
        self.setMinimumWidth(520)
        self._overwrite = False

        layout = QVBoxLayout(self)

        headline = QLabel(
            f"{source_name} holds {len(added) + len(skipped)} model(s): "
            f"{len(added)} new, {len(skipped)} already in the overlay."
        )
        headline.setWordWrap(True)
        layout.addWidget(headline)

        listing = QGroupBox()
        listing_layout = QVBoxLayout(listing)
        self._list_text = QPlainTextEdit()
        self._list_text.setReadOnly(True)
        self._list_text.setMaximumHeight(220)
        lines = [f"+ {key}" for key in added]
        lines += [f"= {key} (already present)" for key in skipped]
        for warning in warnings or []:
            lines.append(f"! {warning}")
        self._list_text.setPlainText("\n".join(lines) if lines else "nothing to import")
        listing_layout.addWidget(self._list_text)
        layout.addWidget(listing)

        self.overwrite_box = QCheckBox(
            f"Overwrite the {len(skipped)} existing entr{'y' if len(skipped) == 1 else 'ies'} "
            "with the Opencode data"
        )
        self.overwrite_box.setEnabled(bool(skipped))
        self.overwrite_box.stateChanged.connect(self._on_overwrite_changed)
        layout.addWidget(self.overwrite_box)

        note = QLabel(
            "New entries are appended as opencode-zen/<id> or opencode-go/<id>. "
            "Existing ones are left untouched unless overwrite is ticked. Nothing "
            "is written until you press Import, and Save To… (Save Custom) is "
            "still needed afterwards."
        )
        note.setWordWrap(True)
        note.setStyleSheet("color:#475569;")
        layout.addWidget(note)

        buttons = QDialogButtonBox(QDialogButtonBox.Ok | QDialogButtonBox.Cancel)
        buttons.button(QDialogButtonBox.Ok).setText("&Import")
        buttons.accepted.connect(self._accept)
        buttons.rejected.connect(self.reject)
        layout.addWidget(buttons)
        self._buttons = buttons
        self._update_ok()

    def _on_overwrite_changed(self) -> None:
        self._overwrite = self.overwrite_box.isChecked()

    def _update_ok(self) -> None:
        ok = self._buttons.button(QDialogButtonBox.Ok)
        if ok is not None:
            ok.setEnabled(True)

    def _accept(self) -> None:
        self._overwrite = self.overwrite_box.isChecked() and self.overwrite_box.isEnabled()
        super().accept()

    def overwrite_existing(self) -> bool:
        """True when existing opencode entries should be replaced."""
        return self._overwrite


class AntigravityImportDialog(QDialog):
    """Confirm an Antigravity fetchAvailableModels import into the custom overlay.

    States what would happen before anything is written: how many entries are
    new, how many already exist (skipped unless overwrite is ticked), how many
    models were excluded (isInternal), and which keys those are. Cancel leaves
    the overlay untouched.
    """

    def __init__(
        self,
        source_name: str,
        added: list[str],
        skipped: list[str],
        warnings: list[str] | None = None,
        parent: QWidget | None = None,
    ) -> None:
        super().__init__(parent)
        self.setWindowTitle("Import Antigravity models")
        self.setMinimumWidth(520)
        self._overwrite = False

        layout = QVBoxLayout(self)

        headline = QLabel(
            f"{source_name} holds {len(added) + len(skipped)} model(s): "
            f"{len(added)} new, {len(skipped)} already in the overlay."
        )
        headline.setWordWrap(True)
        layout.addWidget(headline)

        listing = QGroupBox()
        listing_layout = QVBoxLayout(listing)
        self._list_text = QPlainTextEdit()
        self._list_text.setReadOnly(True)
        self._list_text.setMaximumHeight(220)
        lines = [f"+ {key}" for key in added]
        lines += [f"= {key} (already present)" for key in skipped]
        for warning in warnings or []:
            lines.append(f"! {warning}")
        self._list_text.setPlainText("\n".join(lines) if lines else "nothing to import")
        listing_layout.addWidget(self._list_text)
        layout.addWidget(listing)

        self.overwrite_box = QCheckBox(
            f"Overwrite the {len(skipped)} existing entr{'y' if len(skipped) == 1 else 'ies'} "
            "with the Antigravity data"
        )
        self.overwrite_box.setEnabled(bool(skipped))
        self.overwrite_box.stateChanged.connect(self._on_overwrite_changed)
        layout.addWidget(self.overwrite_box)

        note = QLabel(
            "New entries are appended as antigravity/<id>. Existing ones are "
            "left untouched unless overwrite is ticked. Nothing is written "
            "until you press Import, and Save To… (Save Custom) is still "
            "needed afterwards."
        )
        note.setWordWrap(True)
        note.setStyleSheet("color:#475569;")
        layout.addWidget(note)

        buttons = QDialogButtonBox(QDialogButtonBox.Ok | QDialogButtonBox.Cancel)
        buttons.button(QDialogButtonBox.Ok).setText("&Import")
        buttons.accepted.connect(self._accept)
        buttons.rejected.connect(self.reject)
        layout.addWidget(buttons)
        self._buttons = buttons
        self._update_ok()

    def _on_overwrite_changed(self) -> None:
        self._overwrite = self.overwrite_box.isChecked()

    def _update_ok(self) -> None:
        ok = self._buttons.button(QDialogButtonBox.Ok)
        if ok is not None:
            ok.setEnabled(True)

    def _accept(self) -> None:
        self._overwrite = self.overwrite_box.isChecked() and self.overwrite_box.isEnabled()
        super().accept()

    def overwrite_existing(self) -> bool:
        """True when existing antigravity/* entries should be replaced."""
        return self._overwrite


def _describe_file(path: Any) -> str:
    """Size and mtime, so the risk is legible rather than nominal."""
    try:
        stat = path.stat()
    except OSError:
        return "size unknown"
    size = f"{stat.st_size / (1024 * 1024):.1f} MB" if stat.st_size >= 1024 * 1024 else f"{stat.st_size:,} B"
    from datetime import datetime

    return f"{size}, modified {datetime.fromtimestamp(stat.st_mtime):%Y-%m-%d %H:%M}"


def ask_output_policy(
    output_dir: Any,
    parent: QWidget | None = None,
) -> tuple[str | None, dict[str, Any]]:
    """Resolve the output paths, asking about collisions.

    Returns ``(policy, paths)``. ``policy`` is None when the user cancelled, in
    which case *paths* is empty and nothing should be written.

    The first :func:`plan_output` call deliberately uses no policy: that is what
    raises, and the raise is how the normal path stays a single cheap filesystem
    check rather than a dialog that has to be constructed and torn down on every
    save.
    """
    from ..output import plan_output

    try:
        # No policy: a raise here is the signal that the user has to be asked.
        # Keeping the common case a single stat() means saving never constructs a
        # dialog it will immediately discard.
        return OVERWRITE, plan_output(output_dir)
    except OutputConflict as conflict:
        dialog = OutputConflictDialog(conflict, parent)
        dialog.btn_overwrite.clicked.connect(dialog.accept_overwrite)
        dialog.btn_add_number.clicked.connect(dialog.accept_add_number)
        if dialog.exec() != QDialog.Accepted:
            return None, {}
        policy = dialog.policy()
        assert policy is not None  # accept() only runs through the two handlers
        return policy, plan_output(output_dir, **{policy: True})


# --------------------------------------------------------------------------- #
# Load Data / Save To
# --------------------------------------------------------------------------- #


def _is_json_path(text: str) -> bool:
    """A filled path must name a ``.json`` file (case-insensitive)."""
    return text.strip().lower().endswith(".json")


def _format_size(num_bytes: int) -> str:
    if num_bytes >= 1024 * 1024:
        return f"{num_bytes / (1024 * 1024):.1f} MB"
    return f"{num_bytes:,} B"


def _format_mtime(timestamp: float) -> str:
    from datetime import datetime

    return f"{datetime.fromtimestamp(timestamp):%Y-%m-%d %H:%M}"


def _inspect_datasheet_file(path: Path) -> tuple[bool, int | None]:
    """``(is_datasheet, model_count)`` for an existing file.

    A datasheet here is a JSON object whose values are all objects (the
    parameters/pricing files and the custom overlay all share that shape).
    Anything else -- a list, a scalar, an object with scalar values, or
    unparseable content -- is reported as not-a-datasheet with no count, and
    the caller shows size + mtime only.
    """
    try:
        with path.open(encoding="utf-8") as fh:
            raw = json.load(fh)
    except (OSError, ValueError):
        return False, None
    if not isinstance(raw, dict):
        return False, None
    if all(isinstance(value, dict) for value in raw.values()):
        return True, len(raw)
    return False, None


def first_free_path(path: str | Path) -> Path:
    """Numbered sibling of *path* that does not exist yet, per file.

    Unlike :func:`output.plan_output` -- which numbers the parameters/pricing
    pair together -- each Save To target is numbered on its own: a conflicting
    ``model-pricing.json`` becomes ``model-pricing_1.json`` while a
    non-conflicting sibling keeps its chosen name.
    """
    target = Path(path)
    if not target.exists():
        return target
    for number in range(1, MAX_NUMBERED_SUFFIX + 1):
        candidate = target.parent / numbered_name(target.name, number)
        if not candidate.exists():
            return candidate
    raise ValueError(f"no free numbered name beside {target}")


@dataclass
class ConflictItem:
    """One existing file a save is about to replace, for the conflict dialog."""

    role: str
    path: Path
    new_count: int | None
    old_count: int | None
    old_is_datasheet: bool
    size: int | None
    mtime: float | None


def build_conflict_item(role: str, path: str | Path, new_count: int | None) -> ConflictItem:
    """Describe an existing *path* next to the *new_count* about to replace it."""
    target = Path(path)
    size: int | None = None
    mtime: float | None = None
    try:
        stat = target.stat()
        size, mtime = stat.st_size, stat.st_mtime
    except OSError:
        pass
    old_is_datasheet, old_count = _inspect_datasheet_file(target)
    return ConflictItem(
        role=role,
        path=target,
        new_count=new_count,
        old_count=old_count,
        old_is_datasheet=old_is_datasheet,
        size=size,
        mtime=mtime,
    )


def _conflict_line(item: ConflictItem) -> str:
    """Two-line new-vs-old summary for one conflicting file."""
    new_part = (
        f"{item.new_count:,} model(s)" if item.new_count is not None
        else "unknown size"
    )
    if item.old_is_datasheet:
        old_part = f"{item.old_count:,} model(s)"
    else:
        old_part = "not a datasheet file"
    meta = ""
    if item.size is not None and item.mtime is not None:
        meta = f" · {_format_size(item.size)}, modified {_format_mtime(item.mtime)}"
    elif item.size is not None:
        meta = f" · {_format_size(item.size)}"
    return (
        f"{item.path.name}\n"
        f"    New:  {new_part} · will be written now\n"
        f"    Old:  {old_part}{meta}"
    )


class _PathRow(QWidget):
    """A labelled path input: line edit plus Browse, with a count line below."""

    def __init__(
        self,
        label: str,
        initial: str,
        browse_title: str,
        count_text: str,
        parent: QWidget | None = None,
    ) -> None:
        super().__init__(parent)
        layout = QVBoxLayout(self)
        layout.setContentsMargins(0, 0, 0, 0)
        layout.setSpacing(2)

        form = QFormLayout()
        row = QWidget()
        row_layout = QHBoxLayout(row)
        row_layout.setContentsMargins(0, 0, 0, 0)
        self.edit = QLineEdit()
        self.edit.setText(initial)
        self.edit.setMinimumWidth(320)
        browse = QPushButton("Browse…")
        browse.clicked.connect(lambda: self._browse(browse_title))
        row_layout.addWidget(self.edit, 1)
        row_layout.addWidget(browse)
        form.addRow(label, row)
        layout.addLayout(form)

        self.count_label = QLabel(count_text)
        self.count_label.setStyleSheet("color:#475569;")
        self.count_label.setWordWrap(True)
        layout.addWidget(self.count_label)

        self.extra_button: QPushButton | None = None

    def _browse(self, title: str) -> None:
        start = self.edit.text().strip() or str(Path.cwd())
        chosen, _ = QFileDialog.getSaveFileName(
            self, title, start, "JSON files (*.json)"
        )
        if chosen:
            self.edit.setText(chosen)

    def set_extra_button(self, text: str, tooltip: str, on_click: Any) -> QPushButton:
        """Append a small button (e.g. Clear) under the row; only one is kept."""
        button = QPushButton(text)
        button.setToolTip(tooltip)
        button.clicked.connect(on_click)
        self.extra_button = button
        layout = self.layout()
        assert layout is not None
        layout.addWidget(button, 0, Qt.AlignLeft)  # type: ignore[attr-defined]
        return button


class LoadDataDialog(QDialog):
    """Choose the three input files in one place.

    Parameters and pricing are required -- the editor lists both side by side
    and reports where they disagree, so one file alone would be a misleading
    half-view. Custom metadata is optional; leave it empty to start clean.
    """

    def __init__(
        self,
        *,
        parameters: str = "",
        pricing: str = "",
        custom: str = "",
        browse_dir: str = "",
        parent: QWidget | None = None,
    ) -> None:
        super().__init__(parent)
        self.setWindowTitle("Load Data")
        self.setMinimumWidth(600)
        self._browse_dir = browse_dir or str(Path.cwd())

        layout = QVBoxLayout(self)
        intro = QLabel(
            "Choose the datasheets to edit. Parameters holds the capability "
            "metadata and parameter forms, pricing holds the cost fields and is "
            "what Bifrost's GetCapabilityEntry reads -- both are needed before "
            "anything can display."
        )
        intro.setWordWrap(True)
        layout.addWidget(intro)

        self.params_row = _PathRow(
            "Parameters file (required)", parameters, "Select model_parameters file",
            "Capability metadata and the model_parameters form descriptors.",
        )
        self.pricing_row = _PathRow(
            "Pricing file (required)", pricing, "Select model_pricing file",
            "Cost fields, also read by Bifrost's capability lookup.",
        )
        self.custom_row = _PathRow(
            "Custom metadata (optional)", custom, "Select custom metadata file",
            "Your overlay of edits. Leave empty to start from a clean slate.",
        )
        for row in (self.params_row, self.pricing_row, self.custom_row):
            # Rebind the browse buttons to the *open* dialog: _PathRow browses
            # with a save dialog by default, which would suggest creating files.
            for button in row.findChildren(QPushButton):
                if button.text() == "Browse…":
                    button.clicked.disconnect()
                    button.clicked.connect(
                        lambda _=False, r=row: self._browse_open(r)
                    )
            layout.addWidget(row)
        self.custom_row.set_extra_button(
            "Clear", "Leave the custom overlay empty",
            lambda: self.custom_edit.setText(""),
        )

        self.error_label = QLabel("")
        self.error_label.setWordWrap(True)
        self.error_label.setStyleSheet("color:#b91c1c;")
        layout.addWidget(self.error_label)

        buttons = QDialogButtonBox(QDialogButtonBox.Ok | QDialogButtonBox.Cancel)
        buttons.button(QDialogButtonBox.Ok).setText("&Load")
        buttons.accepted.connect(self._accept)
        buttons.rejected.connect(self.reject)
        layout.addWidget(buttons)
        self._buttons = buttons

        for row in (self.params_row, self.pricing_row, self.custom_row):
            row.edit.textChanged.connect(lambda _=None: self._validate())
        self._validate()

    @property
    def params_edit(self) -> QLineEdit:
        return self.params_row.edit

    @property
    def pricing_edit(self) -> QLineEdit:
        return self.pricing_row.edit

    @property
    def custom_edit(self) -> QLineEdit:
        return self.custom_row.edit

    def _browse_open(self, row: _PathRow) -> None:
        start = row.edit.text().strip() or self._browse_dir
        chosen, _ = QFileDialog.getOpenFileName(
            self, "Select JSON file", start, "JSON files (*.json)"
        )
        if chosen:
            row.edit.setText(chosen)

    def _validate(self) -> None:
        params = self.params_edit.text().strip()
        pricing = self.pricing_edit.text().strip()
        custom = self.custom_edit.text().strip()
        error = ""
        if not params:
            error = "A parameters file is required."
        elif not Path(params).exists():
            error = f"Parameters file not found: {params}"
        elif not pricing:
            error = "A pricing file is required."
        elif not Path(pricing).exists():
            error = f"Pricing file not found: {pricing}"
        elif custom and not Path(custom).exists():
            error = f"Custom file not found: {custom}"
        self.error_label.setText(error)
        ok = self._buttons.button(QDialogButtonBox.Ok)
        if ok is not None:
            ok.setEnabled(not error)

    def _accept(self) -> None:
        self._validate()
        if self.error_label.text():
            return
        super().accept()

    def values(self) -> dict[str, Path | None]:
        """``{"parameters": ..., "pricing": ..., "custom": ...}`` (custom None when empty)."""
        custom = self.custom_edit.text().strip()
        return {
            "parameters": Path(self.params_edit.text().strip()),
            "pricing": Path(self.pricing_edit.text().strip()),
            "custom": Path(custom) if custom else None,
        }


class SaveToDialog(QDialog):
    """Choose where the merged files and the custom overlay go.

    All three paths are optional and must name ``.json`` files, but at least
    one must be filled -- otherwise both save buttons stay disabled and only
    Cancel works. The counts below each path say how much data would be
    written: merged models for parameters/pricing, overlay entries for custom.
    """

    def __init__(
        self,
        *,
        parameters_path: str = "",
        pricing_path: str = "",
        custom_path: str = "",
        parameters_count: int = 0,
        pricing_count: int = 0,
        custom_count: int = 0,
        parent: QWidget | None = None,
    ) -> None:
        super().__init__(parent)
        self.setWindowTitle("Save To")
        self.setMinimumWidth(620)
        self._action: str | None = None

        layout = QVBoxLayout(self)
        intro = QLabel(
            "Name each file freely, but keep the .json extension. "
            "Only filled paths are written -- Save Output writes the merged "
            "parameters/pricing files, Save Custom writes just your overlay. "
            "Both are sorted and beautified."
        )
        intro.setWordWrap(True)
        layout.addWidget(intro)

        self.params_row = _PathRow(
            "Parameters output (optional)", parameters_path,
            "Save merged parameters as",
            f"{parameters_count:,} models will be written",
        )
        self.pricing_row = _PathRow(
            "Pricing output (optional)", pricing_path,
            "Save merged pricing as",
            f"{pricing_count:,} models will be written",
        )
        self.custom_row = _PathRow(
            "Custom overlay output (optional)", custom_path,
            "Save custom overlay as",
            f"{custom_count:,} custom entries will be written",
        )
        layout.addWidget(self.params_row)
        layout.addWidget(self.pricing_row)
        layout.addWidget(self.custom_row)

        self.error_label = QLabel("")
        self.error_label.setWordWrap(True)
        self.error_label.setStyleSheet("color:#b91c1c;")
        layout.addWidget(self.error_label)

        buttons = QDialogButtonBox()
        self.btn_save_output = QPushButton("Save &Output")
        self.btn_save_output.setToolTip("Write the filled merged file(s), sorted and beautified")
        self.btn_save_custom = QPushButton("Save &Custom")
        self.btn_save_custom.setToolTip("Write the custom overlay only, sorted and beautified")
        cancel = buttons.addButton(QDialogButtonBox.Cancel)
        buttons.addButton(self.btn_save_output, QDialogButtonBox.AcceptRole)
        buttons.addButton(self.btn_save_custom, QDialogButtonBox.AcceptRole)
        self.btn_save_output.clicked.connect(self._accept_output)
        self.btn_save_custom.clicked.connect(self._accept_custom)
        cancel.clicked.connect(self.reject)
        # Cancel is the default so a stray Return cannot start a write.
        cancel.setDefault(True)
        layout.addWidget(buttons)
        self._buttons = buttons

        for row in (self.params_row, self.pricing_row, self.custom_row):
            row.edit.textChanged.connect(lambda _=None: self._validate())
        self._validate()

    @property
    def params_edit(self) -> QLineEdit:
        return self.params_row.edit

    @property
    def pricing_edit(self) -> QLineEdit:
        return self.pricing_row.edit

    @property
    def custom_edit(self) -> QLineEdit:
        return self.custom_row.edit

    def _validate(self) -> None:
        params = self.params_edit.text().strip()
        pricing = self.pricing_edit.text().strip()
        custom = self.custom_edit.text().strip()
        error = ""
        for label, value in (
            ("Parameters output", params),
            ("Pricing output", pricing),
            ("Custom output", custom),
        ):
            if value and not _is_json_path(value):
                error = f"{label} must be a .json file: {value}"
                break
        if not error and not (params or pricing or custom):
            error = "Fill at least one output path, or Cancel."
        self.error_label.setText(error)
        has_error = bool(error)
        self.btn_save_output.setEnabled(
            not has_error and bool(params or pricing)
        )
        self.btn_save_custom.setEnabled(not has_error and bool(custom))

    def _accept_output(self) -> None:
        self._validate()
        if self.error_label.text() or not (
            self.params_edit.text().strip() or self.pricing_edit.text().strip()
        ):
            return
        self._action = SAVE_ACTION_OUTPUT
        self.accept()

    def _accept_custom(self) -> None:
        self._validate()
        if self.error_label.text() or not self.custom_edit.text().strip():
            return
        self._action = SAVE_ACTION_CUSTOM
        self.accept()

    def action(self) -> str | None:
        """``SAVE_ACTION_OUTPUT``, ``SAVE_ACTION_CUSTOM``, or None if cancelled."""
        return self._action

    def values(self) -> dict[str, Path | None]:
        """``{"parameters": ..., "pricing": ..., "custom": ...}`` (None when empty)."""
        out: dict[str, Path | None] = {}
        for key, edit in (
            ("parameters", self.params_edit),
            ("pricing", self.pricing_edit),
            ("custom", self.custom_edit),
        ):
            text = edit.text().strip()
            out[key] = Path(text) if text else None
        return out


class SaveConflictDialog(QDialog):
    """Confirm replacing existing files, per file, with new-vs-old evidence.

    Each conflicting file shows what would be written (new model count) next
    to what is there now (old model count when the file is a datasheet, plus
    size and last-modified in both cases). A file that is not a datasheet --
    any JSON that is not an object of objects -- shows size and mtime only, so
    an unrelated ``notes.json`` is never mistaken for model data.

    Three answers: overwrite the files in place, keep them and write numbered
    siblings beside them (per file, not as a pair), or cancel and write
    nothing. Cancel is the default.
    """

    def __init__(self, items: list[ConflictItem], parent: QWidget | None = None) -> None:
        super().__init__(parent)
        self.setWindowTitle("Files already exist")
        self.setModal(True)
        self.setMinimumWidth(560)
        self._policy: str | None = None
        self._items = list(items)

        layout = QVBoxLayout(self)
        layout.setSpacing(10)

        plural = len(items) > 1
        headline = QLabel(
            f"{'These files' if plural else 'This file'} already "
            f"{'exist' if plural else 'exists'} -- what should happen to "
            f"{'them' if plural else 'it'}?"
        )
        headline.setWordWrap(True)
        layout.addWidget(headline)

        listing = QGroupBox()
        listing_layout = QVBoxLayout(listing)
        detail = QPlainTextEdit()
        detail.setReadOnly(True)
        detail.setMaximumHeight(240)
        detail.setPlainText("\n\n".join(_conflict_line(item) for item in items))
        detail.setStyleSheet("font-family: monospace;")
        listing_layout.addWidget(detail)
        try:
            numbered = ", ".join(
                first_free_path(item.path).name for item in items
            )
            keep = QLabel(f"  keeping them writes:  {numbered}")
            keep.setStyleSheet("color:#475569; font-family: monospace;")
            keep.setWordWrap(True)
            listing_layout.addWidget(keep)
        except ValueError:
            pass
        layout.addWidget(listing)

        note = QLabel(
            "Numbering applies per file: only the conflicting names move, "
            "the rest keep the names you chose."
        )
        note.setWordWrap(True)
        note.setStyleSheet("color:#475569;")
        layout.addWidget(note)

        buttons = QDialogButtonBox()
        self.btn_overwrite = QPushButton("&Overwrite")
        self.btn_overwrite.setToolTip("Replace the existing file(s) with what would be written")
        self.btn_add_number = QPushButton("Add &Number")
        self.btn_add_number.setToolTip("Keep the existing file(s) and write numbered ones beside them")
        cancel = buttons.addButton(QDialogButtonBox.Cancel)
        buttons.addButton(self.btn_overwrite, QDialogButtonBox.AcceptRole)
        buttons.addButton(self.btn_add_number, QDialogButtonBox.AcceptRole)
        self.btn_overwrite.clicked.connect(self.accept_overwrite)
        self.btn_add_number.clicked.connect(self.accept_add_number)
        cancel.clicked.connect(self.reject)
        cancel.setDefault(True)
        layout.addWidget(buttons)

    def policy(self) -> str | None:
        """``OVERWRITE``, ``ADD_NUMBER``, or None if the user backed out."""
        return self._policy

    def accept_overwrite(self) -> None:  # noqa: N802
        self._policy = OVERWRITE
        self.accept()

    def accept_add_number(self) -> None:  # noqa: N802
        self._policy = ADD_NUMBER
        self.accept()

    def resolved_paths(self, targets: dict[str, Path]) -> dict[str, Path]:
        """Targets with conflicts moved to their numbered siblings.

        Only meaningful after :meth:`accept_add_number`; anything else returns
        the targets unchanged.
        """
        if self._policy != ADD_NUMBER:
            return dict(targets)
        conflicted = {item.path.resolve() if item.path.exists() else item.path
                      for item in self._items}
        resolved: dict[str, Path] = {}
        for role, target in targets.items():
            try:
                key = target.resolve() if target.exists() else target
            except OSError:
                key = target
            resolved[role] = first_free_path(target) if key in conflicted else target
        return resolved


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