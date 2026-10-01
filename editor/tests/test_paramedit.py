"""Regression tests for editing an existing ``model_parameters`` descriptor.

Reported: changing a descriptor's ``default`` in the "model_parameters (merged by
id)" container did nothing -- the typed number reverted. Four separate defects
lined up to produce that, and each one is pinned here because each was invisible
on its own:

1. The table held the *original* dicts, so ``item[key] = value`` rewrote the
   loaded dataset.
2. The baseline was then read back from that same rewritten object, so every edit
   compared equal to itself and was discarded as a no-op.
3. Discarding it called the revert path, which reset the table from the overlay --
   the visible "it snapped back".
4. ``ParamDescriptorModel`` had no ``setData``, so Qt rejected cell edits outright
   and the view restored the displayed value with no error at all.

The fix routes every edit through the overlay and shows the engine's own merged
view, so the table is decoupled from the dataset and the overlay is the only
thing that changes.
"""

from __future__ import annotations

import pytest

pytest.importorskip("PySide6", reason="PySide6 not installed")

from PySide6.QtCore import Qt  # noqa: E402
from PySide6.QtWidgets import QApplication, QMessageBox  # noqa: E402

from datasheet_editor.fieldinfo import build_catalog  # noqa: E402
from datasheet_editor.gui.main_window import MainWindow  # noqa: E402
from datasheet_editor.gui.models import ParamDescriptorModel  # noqa: E402
from datasheet_editor.merge import PARAMS_FIELD, merge  # noqa: E402

MID = "grok-4.20"

PARAMS = {
    MID: {
        "mode": "chat",
        "max_output_tokens": 1000000,
        "model_parameters": [
            {"id": "temperature", "type": "number", "range": {"min": 0, "max": 2}},
            {"id": "max_output_tokens", "type": "number", "default": 8192, "label": "Max Tokens"},
        ],
    }
}
PRICING = {MID: {"provider": "xai"}}


@pytest.fixture(scope="session")
def app():
    return QApplication.instance() or QApplication([])


@pytest.fixture
def window(app, monkeypatch):
    """A window on a model that has never been overridden."""
    monkeypatch.setattr(QMessageBox, "information", staticmethod(lambda *a, **k: QMessageBox.Ok))
    from datasheet_editor.gui.models import build_rows

    w = MainWindow()
    w.parameters = PARAMS
    w.pricing = PRICING
    w.overlay = {}
    w.conflicts = {}
    w.param_catalog = build_catalog(PARAMS, PRICING)
    w.pricing_catalog = build_catalog(PRICING, PRICING, source="pricing")
    w._populate_rows(build_rows(PARAMS, PRICING, w.overlay))
    w._select_model(MID)
    return w


def _type_default(window, text):
    """What Qt does when a user edits the ``default`` cell."""
    row = window.param_model.row_for("max_output_tokens")
    assert row >= 0, "descriptor row missing from the table"
    return window.param_model.setData(window.param_model.index(row, 3), text, Qt.EditRole)


def _shown(window):
    return window.param_model.item_at(window.param_model.row_for("max_output_tokens")).get("default")


# --------------------------------------------------------------------------- #
# the reported symptom
# --------------------------------------------------------------------------- #


def test_typing_a_new_default_sticks(window):
    assert _shown(window) == 8192
    assert _type_default(window, "32768") is True
    assert _shown(window) == 32768


def test_the_edit_reaches_the_overlay(window):
    _type_default(window, "32768")
    assert window.overlay[MID]["parameters"][PARAMS_FIELD] == [
        {"id": "max_output_tokens", "default": 32768}
    ]


def test_the_edit_reaches_the_merged_output(window):
    _type_default(window, "32768")
    result = merge(PARAMS, PRICING, window.overlay)
    added = next(
        d for d in result.parameters[MID][PARAMS_FIELD] if d["id"] == "max_output_tokens"
    )
    assert added["default"] == 32768


def test_the_change_report_shows_before_and_after(window):
    _type_default(window, "32768")
    lines = [c.describe() for c in merge(PARAMS, PRICING, window.overlay).changes]
    assert len(lines) == 1
    assert "model_parameters[id=max_output_tokens].default" in lines[0]
    assert "8192 -> 32768" in lines[0]


def test_the_typed_value_is_a_number_not_a_string(window):
    """A quoted default would serialise into the datasheet as a string and change
    what the playground sends."""
    _type_default(window, "32768")
    value = window.overlay[MID]["parameters"][PARAMS_FIELD][0]["default"]
    assert isinstance(value, int)
    assert value == 32768


def test_editing_does_not_vanish_when_an_override_already_existed(window):
    """The reported case: a user who had already added something to the overlay.

    The revert branch fired on every edit and reset the table from the overlay, so
    the row lost its value -- and for a descriptor whose only override was that one
    key, it lost the whole row.
    """
    window.overlay[MID] = {"parameters": {PARAMS_FIELD: [
        {"id": "max_output_tokens", "default": 8192}]}}
    window._rebuild_field_panes(MID)
    assert _shown(window) == 8192
    assert _type_default(window, "32768") is True
    assert _shown(window) == 32768
    assert window.overlay[MID]["parameters"][PARAMS_FIELD] == [
        {"id": "max_output_tokens", "default": 32768}
    ]


# --------------------------------------------------------------------------- #
# the original must not be touched
# --------------------------------------------------------------------------- #


def test_the_loaded_dataset_is_not_mutated(window):
    _type_default(window, "32768")
    entry = next(
        d for d in PARAMS[MID][PARAMS_FIELD] if d["id"] == "max_output_tokens"
    )
    assert entry["default"] == 8192, "editing the table rewrote the original in memory"


def test_the_table_rows_are_copies(window):
    """Aliasing the originals is what made the baseline equal to the edit."""
    row = window.param_model.row_for("max_output_tokens")
    original = next(d for d in PARAMS[MID][PARAMS_FIELD] if d["id"] == "max_output_tokens")
    assert window.param_model.item_at(row) is not original


def test_the_baseline_is_read_from_the_original_not_the_row(window):
    """The defect in one assertion: the old code compared the new value against
    the object it had just written, so equality always held."""
    _type_default(window, "32768")
    assert window.overlay[MID]["parameters"][PARAMS_FIELD][0]["default"] != 8192


def test_reverting_to_the_original_value_drops_the_override(window):
    _type_default(window, "32768")
    assert window.overlay[MID]["parameters"][PARAMS_FIELD]
    _type_default(window, "8192")
    assert window.overlay[MID]["parameters"] == {}
    assert _shown(window) == 8192


def test_reverting_drops_a_hollow_descriptor(window):
    """A descriptor left carrying only its id says nothing and must not linger."""
    _type_default(window, "32768")
    _type_default(window, "8192")
    assert window.overlay[MID]["parameters"].get(PARAMS_FIELD) is None


def test_adding_a_key_the_original_lacks_is_an_override_not_a_revert(window):
    row = window.param_model.row_for("temperature")
    ok = window.param_model.setData(window.param_model.index(row, 3), "0.7", Qt.EditRole)
    assert ok is True
    assert window.overlay[MID]["parameters"][PARAMS_FIELD] == [
        {"id": "temperature", "default": 0.7}
    ]


def test_other_descriptors_are_untouched_by_an_edit(window):
    _type_default(window, "32768")
    result = merge(PARAMS, PRICING, window.overlay)
    temperature = next(
        d for d in result.parameters[MID][PARAMS_FIELD] if d["id"] == "temperature"
    )
    assert temperature == {"id": "temperature", "type": "number", "range": {"min": 0, "max": 2}}


# --------------------------------------------------------------------------- #
# the model itself
# --------------------------------------------------------------------------- #


def test_set_data_is_implemented():
    """Qt's default rejects edits and silently restores the displayed value."""
    assert hasattr(ParamDescriptorModel, "setData")


def test_set_data_rejects_an_unchanged_value(app):
    model = ParamDescriptorModel()
    model.set_items([{"id": "p", "default": 1}])
    assert model.setData(model.index(0, 3), "1", Qt.EditRole) is False


def test_set_data_accepts_json_for_non_numeric_defaults(app):
    model = ParamDescriptorModel()
    model.set_items([{"id": "p", "default": "low"}])
    assert model.setData(model.index(0, 3), '"high"', Qt.EditRole) is True
    assert model.item_at(0)["default"] == "high"


def test_the_id_column_is_not_editable(app):
    """id is the key the merge matches on, so renaming one is delete-and-add."""
    model = ParamDescriptorModel()
    model.set_items([{"id": "temperature"}])
    assert model.setData(model.index(0, 0), "temp", Qt.EditRole) is False
    assert not model.flags(model.index(0, 0)) & Qt.ItemIsEditable


def test_the_range_column_is_not_editable(app):
    """range is a nested object; typing JSON into a one-line cell loses its shape."""
    model = ParamDescriptorModel()
    model.set_items([{"id": "temperature", "range": {"min": 0, "max": 2}}])
    assert model.setData(model.index(0, 4), '{"min": 1}', Qt.EditRole) is False


def test_editing_is_routed_out_to_the_callback(app):
    seen = []
    model = ParamDescriptorModel(on_edit=lambda pid, key, value: seen.append((pid, key, value)))
    model.set_items([{"id": "temperature", "default": 1}])
    assert model.setData(model.index(0, 3), "0.4", Qt.EditRole) is True
    assert seen == [("temperature", "default", 0.4)]


def test_the_label_column_is_editable(app):
    seen = []
    model = ParamDescriptorModel(on_edit=lambda *a: seen.append(a))
    model.set_items([{"id": "temperature", "label": "Temperature"}])
    assert model.setData(model.index(0, 1), "Temp", Qt.EditRole) is True
    assert seen == [("temperature", "label", "Temp")]


def test_an_invalid_index_is_ignored(app):
    model = ParamDescriptorModel()
    model.set_items([{"id": "p"}])
    assert model.setData(model.index(9, 3), "1", Qt.EditRole) is False


def test_the_original_role_is_ignored(app):
    seen = []
    model = ParamDescriptorModel(on_edit=lambda *a: seen.append(a))
    model.set_items([{"id": "p", "default": 1}])
    assert model.setData(model.index(0, 3), "2", Qt.DisplayRole) is False
    assert seen == []


# --------------------------------------------------------------------------- #
# the shown view
# --------------------------------------------------------------------------- #


def test_the_table_shows_the_merged_view(window):
    window.overlay[MID] = {"parameters": {PARAMS_FIELD: [
        {"id": "temperature", "default": 0.9}]}}
    window._rebuild_field_panes(MID)
    row = window.param_model.row_for("temperature")
    assert window.param_model.item_at(row)["default"] == 0.9


def test_the_table_shows_a_descriptors_the_user_added(window):
    window.overlay[MID] = {"parameters": {PARAMS_FIELD: [
        {"id": "brand_new", "type": "boolean", "default": True}]}}
    window._rebuild_field_panes(MID)
    assert window.param_model.row_for("brand_new") >= 0


def test_a_model_with_no_descriptors_disables_the_table(window):
    from datasheet_editor.gui.models import build_rows

    window.parameters = {**PARAMS, "bare": {"mode": "chat"}}
    window._populate_rows(build_rows(window.parameters, PRICING, {}))
    window._select_model("bare")
    assert window.param_array_box.isEnabled() is False