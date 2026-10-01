"""Tests for the Add Field dialog: typed inputs, descriptions, and routing.

The user-facing problem: the datasheets document nothing, so adding a field
meant guessing both its name and the shape of its value. These tests pin the two
things that answer that -- a description sourced from the Go struct or the loaded
data, and a value control that matches the field's shape -- plus the routing rule
that keeps a ``model_parameters`` entry from being written as a top-level key Go
never reads.
"""

from __future__ import annotations

import pytest

pytest.importorskip("PySide6", reason="PySide6 not installed")

from PySide6.QtCore import Qt  # noqa: E402
from PySide6.QtWidgets import (  # noqa: E402
    QApplication,
    QCheckBox,
    QComboBox,
    QDialogButtonBox,
    QLineEdit,
    QPlainTextEdit,
)

from datasheet_editor.fieldinfo import build_catalog  # noqa: E402
from datasheet_editor.gui.adddialogs import (  # noqa: E402
    TARGET_DESCRIPTOR,
    TARGET_FIELD,
    AddFieldDialog,
    ValueSetEditor,
)
from datasheet_editor.gui.main_window import (  # noqa: E402
    _descriptor_ids,
)
from datasheet_editor.merge import PARAMS_FIELD, merge, merge_param_array  # noqa: E402


@pytest.fixture(scope="session")
def app():
    return QApplication.instance() or QApplication([])


DATASET = {
    "m": {
        "mode": "chat",
        "provider": "openai",
        "max_input_tokens": 1000,
        "supports_vision": True,
        "reasoning_effort_levels": ["low", "high"],
        "service_tiers": ["priority"],
        "model_parameters": [
            {
                "id": "temperature",
                "type": "number",
                "label": "Temperature",
                "helpText": "What sampling temperature to use.",
                "range": {"min": 0, "max": 2},
            },
            {
                "id": "reasoning_effort",
                "type": "select",
                "label": "Reasoning Effort",
                "helpText": "How much reasoning to do before answering.",
                "options": [{"label": "Low", "value": "low"}, {"label": "High", "value": "high"}],
                "default": "low",
            },
        ],
    }
}

PRICING = {"m": {"provider": "openai", "max_input_tokens": 1000}}


@pytest.fixture
def catalog():
    return build_catalog(DATASET, PRICING)


@pytest.fixture
def params_dialog(app, catalog):
    return AddFieldDialog(
        section="parameters",
        model_id="m",
        known_fields=["mode", "max_input_tokens", "supports_vision", "reasoning_effort_levels"],
        existing=set(),
        catalog=catalog,
        descriptor_names=catalog.descriptor_names(),
        descriptors_present=_descriptor_ids(DATASET["m"]),
    )


@pytest.fixture
def pricing_dialog(app):
    catalog = build_catalog(PRICING, PRICING, source="pricing")
    return AddFieldDialog(
        section="pricing",
        model_id="m",
        known_fields=["max_input_tokens"],
        existing=set(),
        catalog=catalog,
    )


def _select(dialog, name, target=TARGET_FIELD):
    if target == TARGET_DESCRIPTOR and dialog.target_combo.count() > 1:
        dialog.target_combo.setCurrentIndex(1)
    dialog.name_combo.setCurrentText(name)


# --------------------------------------------------------------------------- #
# the control matches the field's shape
# --------------------------------------------------------------------------- #


def test_true_or_false_field_gets_a_checkbox(params_dialog):
    """A flag is a flag: the dialog should not ask the user to spell out `true`."""
    _select(params_dialog, "supports_vision")
    assert isinstance(params_dialog._editor, QCheckBox)


def test_checkbox_starts_from_the_most_common_value(params_dialog):
    _select(params_dialog, "supports_vision")
    params_dialog._checkbox.setChecked(False)
    assert params_dialog.values() == ("supports_vision", False)


def test_closed_vocabulary_gets_a_dropdown(app):
    """A select for a field with a small controlled set, so the value cannot be mistyped."""
    catalog = build_catalog({"m": {"mode": "chat", "reasoning_effort_levels": ["low"]}})
    dialog = AddFieldDialog(
        section="parameters", model_id="m", known_fields=["mode"], existing=set(), catalog=catalog
    )
    _select(dialog, "mode")
    assert isinstance(dialog._editor, QComboBox)
    assert dialog._combo.isEditable(), "a value the datasheet has not caught up with must stay typeable"


def test_known_string_array_gets_a_tick_list(params_dialog):
    """reasoning_effort_levels is the case the user asked about: pick which levels apply."""
    _select(params_dialog, "reasoning_effort_levels")
    editor = params_dialog._editor
    assert isinstance(editor, ValueSetEditor)
    labels = [editor.list.item(i).text() for i in range(editor.list.count())]
    assert labels == ["high", "low"]


def test_tick_list_produces_a_json_list(params_dialog):
    _select(params_dialog, "reasoning_effort_levels")
    editor = params_dialog._editor
    editor.list.item(0).setCheckState(Qt.CheckState.Checked)
    assert params_dialog.values() == ("reasoning_effort_levels", ["high"])


def test_tick_list_accepts_a_value_the_datasheet_does_not_record(params_dialog):
    _select(params_dialog, "reasoning_effort_levels")
    editor = params_dialog._editor
    editor.custom_edit.setText("xhigh, ultra")
    editor._add_custom()
    assert "xhigh" in params_dialog.values()[1]
    assert "ultra" in params_dialog.values()[1]


def test_tick_list_defaults_to_nothing_selected(params_dialog):
    """Empty means \"the row says nothing\", not \"the model supports no levels\"."""
    _select(params_dialog, "reasoning_effort_levels")
    assert params_dialog.values()[1] == []


def test_number_field_keeps_a_text_box_with_the_observed_range(params_dialog):
    """Numbers are not clamped: a widget that silently rewrites out-of-range input
    would corrupt the datasheet. The range is advice, not a constraint."""
    _select(params_dialog, "max_input_tokens")
    assert isinstance(params_dialog.value_edit, QLineEdit)
    assert "1,000" in params_dialog.value_edit.placeholderText()


def test_unknown_field_falls_back_to_a_json_text_area(params_dialog):
    """Nothing is known about a name that appears nowhere, so nothing is assumed."""
    _select(params_dialog, "something_brand_new")
    assert isinstance(params_dialog.value_edit, QPlainTextEdit)
    assert "JSON" in params_dialog.description.body.text()


def test_pricing_dialog_offers_a_checkbox_for_a_flag(app):
    catalog = build_catalog({"m": {"is_deprecated": True}}, {"m": {"is_deprecated": True}},
                            source="pricing")
    dialog = AddFieldDialog(section="pricing", model_id="m", known_fields=["is_deprecated"],
                            existing=set(), catalog=catalog)
    _select(dialog, "is_deprecated")
    assert isinstance(dialog._editor, QCheckBox)


def test_swapping_the_field_swaps_the_control(params_dialog):
    _select(params_dialog, "supports_vision")
    first = params_dialog._editor
    _select(params_dialog, "reasoning_effort_levels")
    second = params_dialog._editor
    assert first is not second
    assert isinstance(second, ValueSetEditor)
    # The stale control must be detached, or it stays visible and editable.
    assert first.parent() is None


def test_the_value_control_is_actually_shown(params_dialog, app):
    """Regression: the control was parented correctly but had been detached from
    the form along with its container, so the Value row rendered empty while
    every widget-level assertion still passed. Only geometry catches that."""
    params_dialog.show()
    for name, expected in (
        ("supports_vision", QCheckBox),
        ("reasoning_effort_levels", ValueSetEditor),
        ("max_input_tokens", QLineEdit),
        ("something_new", QPlainTextEdit),
    ):
        _select(params_dialog, name)
        app.processEvents()  # the layout is not applied until the event loop runs
        editor = params_dialog._editor
        assert isinstance(editor, expected), name
        assert editor.isVisibleTo(params_dialog), f"{name} control is not shown"
        assert editor.height() > 0, f"{name} control has no height"


def test_the_value_row_survives_rebuilding(params_dialog):
    """Repeated swaps must not accumulate controls or lose the row."""
    for name in ("supports_vision", "reasoning_effort_levels", "max_input_tokens",
                 "supports_vision", "reasoning_effort_levels"):
        _select(params_dialog, name)
    assert params_dialog.value_layout.count() == 1


# --------------------------------------------------------------------------- #
# the description
# --------------------------------------------------------------------------- #


def test_description_names_the_shape(params_dialog):
    _select(params_dialog, "reasoning_effort_levels")
    assert "list of text" in params_dialog.description.heading.text()


def test_description_quotes_the_go_doc_comment(params_dialog):
    """The Go source is the only place this is written down anywhere."""
    _select(params_dialog, "reasoning_effort_levels")
    assert "Effort labels the model accepts" in params_dialog.description.body.text()


def test_description_says_which_file_bifrost_reads_the_field(params_dialog):
    _select(params_dialog, "reasoning_effort_levels")
    assert "Read by Bifrost from model_parameters.json" in params_dialog.description.read_note.text()


def test_description_warns_when_the_field_is_read_from_the_other_file(params_dialog):
    """Editing max_input_tokens in the parameters section is accepted by the merge
    and then does nothing. The dialog has to say so before the user finds out."""
    _select(params_dialog, "max_input_tokens")
    assert "model_pricing.json" in params_dialog.description.read_note.text()


def test_description_warns_when_nothing_reads_the_field(params_dialog):
    _select(params_dialog, "supports_vision")
    assert "Not read by Bifrost" in params_dialog.description.read_note.text()


def test_description_reports_how_widely_a_field_is_used(params_dialog):
    _select(params_dialog, "max_input_tokens")
    assert "1 of 1" in params_dialog.description.body.text()


def test_description_starts_empty_without_a_name(params_dialog):
    _select(params_dialog, "")
    assert params_dialog.description.body.text()
    assert not params_dialog.description.heading.text()


# --------------------------------------------------------------------------- #
# descriptors are a different vocabulary
# --------------------------------------------------------------------------- #


def test_descriptor_target_is_offered_for_parameters(params_dialog):
    assert params_dialog.target_combo.count() == 2
    assert params_dialog.target == TARGET_FIELD


def test_descriptor_target_is_not_offered_for_pricing(pricing_dialog):
    """model_parameters is not part of the pricing entry; the merge rejects it."""
    assert pricing_dialog.target_combo.count() == 0
    assert pricing_dialog.target == TARGET_FIELD


def test_switching_target_swaps_the_name_list(params_dialog):
    _select(params_dialog, "supports_vision")
    _select(params_dialog, "reasoning_effort", target=TARGET_DESCRIPTOR)
    listed = [params_dialog.name_combo.itemText(i)
              for i in range(params_dialog.name_combo.count())]
    assert "reasoning_effort" in listed
    assert "supports_vision" not in listed


def test_descriptor_description_uses_its_own_help_text(params_dialog):
    _select(params_dialog, "reasoning_effort", target=TARGET_DESCRIPTOR)
    body = params_dialog.description.body.text()
    assert "Reasoning Effort" in body
    assert "How much reasoning to do" in body


def test_descriptor_description_explains_what_go_reads(params_dialog):
    _select(params_dialog, "reasoning_effort", target=TARGET_DESCRIPTOR)
    note = params_dialog.description.read_note.text()
    assert "only the descriptor's id" in note


def test_select_descriptor_gets_a_dropdown_of_its_options(params_dialog):
    _select(params_dialog, "reasoning_effort", target=TARGET_DESCRIPTOR)
    assert isinstance(params_dialog._editor, QComboBox)
    listed = [params_dialog._combo.itemText(i) for i in range(params_dialog._combo.count())]
    assert {"low", "high"} <= set(listed)


def test_boolean_descriptor_gets_a_checkbox(app, catalog):
    data = {"m": {"model_parameters": [{"id": "logprobs", "type": "boolean",
                                        "helpText": "Return log probabilities."}]}}
    cat = build_catalog(data)
    dialog = AddFieldDialog(section="parameters", model_id="m", known_fields=[],
                            existing=set(), catalog=cat,
                            descriptor_names=cat.descriptor_names(), descriptors_present=set())
    _select(dialog, "logprobs", target=TARGET_DESCRIPTOR)
    assert isinstance(dialog._editor, QCheckBox)


def test_descriptor_the_model_already_has_is_refused(params_dialog):
    _select(params_dialog, "temperature", target=TARGET_DESCRIPTOR)
    assert "already a parameter" in params_dialog.error_label.text()
    ok = params_dialog._buttons.button(QDialogButtonBox.Ok)
    assert not ok.isEnabled()


def test_unknown_descriptor_is_still_addable(app):
    """A parameter no other model publishes has no type, but adding the id is
    exactly what makes Bifrost accept it."""
    dialog = AddFieldDialog(section="parameters", model_id="m", known_fields=[],
                            existing=set(), catalog=None,
                            descriptor_names=["brand_new_param"], descriptors_present=set())
    _select(dialog, "brand_new_param", target=TARGET_DESCRIPTOR)
    assert dialog.error_label.text() == ""
    assert dialog.values() == ("brand_new_param", {"id": "brand_new_param"})


def test_adding_a_descriptor_produces_a_complete_element(params_dialog):
    """A bare {"id": x} renders as no control at all in the playground."""
    cat_dialog = params_dialog
    _select(cat_dialog, "reasoning_effort", target=TARGET_DESCRIPTOR)
    cat_dialog._descriptors_present = set()  # pretend this model lacks it
    cat_dialog._validate()
    cat_dialog._combo.setCurrentText("high")
    name, descriptor = cat_dialog.values()
    assert descriptor["id"] == "reasoning_effort"
    assert descriptor["type"] == "select"
    assert descriptor["label"] == "Reasoning Effort"
    assert "How much reasoning" in descriptor["helpText"]
    assert {"value": "high"} in [{"value": o["value"]} for o in descriptor["options"]]
    assert descriptor["default"] == "high"


def test_number_descriptor_carries_its_range(app):
    data = {"m": {"model_parameters": [{"id": "temperature", "type": "number",
                                        "range": {"min": 0, "max": 2}}]}}
    cat = build_catalog(data)
    dialog = AddFieldDialog(section="parameters", model_id="m", known_fields=[],
                            existing=set(), catalog=cat,
                            descriptor_names=cat.descriptor_names(), descriptors_present=set())
    _select(dialog, "temperature", target=TARGET_DESCRIPTOR)
    assert dialog.set_value_text("0.7")
    descriptor = dialog.values()[1]
    assert descriptor["default"] == 0.7
    assert descriptor["range"] == {"min": 0.0, "max": 2.0}


# --------------------------------------------------------------------------- #
# routing into the overlay
# --------------------------------------------------------------------------- #


def test_descriptor_ids_are_read_from_an_entry():
    entry = {PARAMS_FIELD: [{"id": "a"}, {"id": "b"}, {"no_id": 1}, "junk"]}
    assert _descriptor_ids(entry) == {"a", "b"}
    assert _descriptor_ids({}) == set()
    assert _descriptor_ids({PARAMS_FIELD: "not a list"}) == set()


def test_new_descriptor_is_appended_without_disturbing_the_rest():
    """A brand-new id lands alongside the originals, which keep every key."""
    original = [{"id": "temperature", "range": {"min": 0, "max": 2}}]
    out = merge_param_array(original, [{"id": "thinking", "type": "boolean"}])
    assert [i["id"] for i in out] == ["temperature", "thinking"]
    assert out[0]["range"] == {"min": 0, "max": 2}


def test_existing_descriptor_is_updated_in_place():
    original = [{"id": "temperature", "type": "number", "label": "Temperature"}]
    out = merge_param_array(original, [{"id": "temperature", "default": 0.4}])
    assert len(out) == 1
    assert out[0]["default"] == 0.4
    assert out[0]["label"] == "Temperature", "an override must not drop the rest of the entry"


def test_overlay_only_needs_the_descriptors_that_were_touched():
    """The engine matches on id, so restating the model's whole array would put
    original data into the custom file and bloat it for nothing."""
    original = [{"id": "a", "label": "A"}, {"id": "b", "label": "B"}, {"id": "c"}]
    overlay = [{"id": "b", "default": 9}]
    out = merge_param_array(original, overlay)
    assert [i["id"] for i in out] == ["a", "b", "c"]
    assert out[1] == {"id": "b", "label": "B", "default": 9}


def test_merging_the_view_never_aliases_the_original():
    """Regression: the GUI table held the original dicts, so an edit rewrote the
    loaded dataset and the baseline it compared against was the value just written.
    """
    original = [{"id": "temperature", "default": 1}]
    view = merge_param_array(original, [{"id": "temperature", "default": 0.4}])
    assert view[0] is not original[0]
    view[0]["default"] = 99
    view[0]["range"] = {"min": 0}
    assert original[0] == {"id": "temperature", "default": 1}


def test_descriptor_merge_reports_one_change_line():
    """A one-descriptor addition should not read as replacing the whole array."""
    result = merge(DATASET, PRICING, {"m": {"parameters": {PARAMS_FIELD: [
        {"id": "thinking", "type": "boolean"}]}}})
    lines = [c.describe() for c in result.changes]
    assert len(lines) == 1
    assert "model_parameters[id=thinking]" in lines[0]
    ids = [i["id"] for i in result.parameters["m"][PARAMS_FIELD]]
    assert ids == ["temperature", "reasoning_effort", "thinking"]


def test_top_level_field_is_still_added_as_a_plain_key(params_dialog):
    _select(params_dialog, "supports_vision")
    params_dialog._checkbox.setChecked(False)
    name, value = params_dialog.values()
    result = merge(DATASET, PRICING, {"m": {"parameters": {name: value}}})
    assert result.parameters["m"]["supports_vision"] is False
    assert PARAMS_FIELD not in result.changes[0].path


def test_placement_rules_still_apply_to_top_level_fields(params_dialog):
    """The typed controls must not bypass the shared placement rule."""
    _select(params_dialog, "input_cost_per_token")
    assert "pricing" in params_dialog.error_label.text()


# Qt enum helpers, kept out of the test bodies above.
from PySide6.QtCore import Qt  # noqa: E402

Qt_Checked = Qt.CheckState.Checked