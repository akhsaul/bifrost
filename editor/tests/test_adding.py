"""Tests for adding new models and fields, and for the both-files-required dialog.

Adding data was reachable only by hand-editing the overlay JSON, so the editor
gets real dialogs. The field-placement rule inside those dialogs comes from
``merge.check_field_placement`` so the GUI can never offer a placement the merge
would then reject.
"""

from __future__ import annotations

import json

import pytest

pytest.importorskip("PySide6", reason="PySide6 not installed")

from PySide6.QtWidgets import QApplication, QMessageBox  # noqa: E402

from datasheet_editor.gui.adddialogs import (  # noqa: E402
    AddFieldDialog,
    AddModelDialog,
    _parse_value,
)
from datasheet_editor.merge import MergeError, check_field_placement, merge  # noqa: E402


@pytest.fixture(scope="session")
def app():
    return QApplication.instance() or QApplication([])


@pytest.fixture
def model_dialog(app):
    return AddModelDialog(
        providers=["openai", "anthropic"],
        modes=["chat", "embedding"],
        existing_ids={"gpt-4o", "claude-sonnet-4-5"},
    )


@pytest.fixture
def field_dialog(app):
    return AddFieldDialog(
        section="parameters",
        model_id="gpt-4o",
        known_fields=["max_input_tokens", "supports_vision", "provider"],
        existing={"provider"},
    )


# --------------------------------------------------------------------------- #
# check_field_placement -- the shared rule
# --------------------------------------------------------------------------- #


def test_cost_field_cannot_go_in_parameters():
    problem = check_field_placement("m", "parameters", "input_cost_per_token", set())
    assert problem and "pricing" in problem


def test_capability_field_may_go_in_parameters():
    assert check_field_placement("m", "parameters", "supports_vision", set()) is None


def test_cost_field_is_fine_in_pricing():
    assert check_field_placement("m", "pricing", "input_cost_per_token", set()) is None


def test_capability_field_may_go_in_pricing():
    assert check_field_placement("m", "pricing", "max_input_tokens", set()) is None


def test_param_array_cannot_go_in_pricing():
    problem = check_field_placement("m", "pricing", "model_parameters", set())
    assert problem and "parameters" in problem


def test_param_array_is_fine_in_parameters():
    assert check_field_placement("m", "parameters", "model_parameters", set()) is None


def test_non_cost_field_is_refused_in_pricing():
    problem = check_field_placement("m", "pricing", "some_random_field", set())
    assert problem and "parameters" in problem


def test_known_dataset_field_is_allowed_in_pricing():
    """tiered_pricing is not in the Go read-set but is a real pricing field."""
    assert check_field_placement("m", "pricing", "tiered_pricing", {"tiered_pricing"}) is None


def test_unknown_section_is_rejected():
    assert check_field_placement("m", "nonsense", "x", set()) is not None


# --------------------------------------------------------------------------- #
# AddModelDialog
# --------------------------------------------------------------------------- #


def test_model_id_is_required(model_dialog):
    assert model_dialog.error_label.text() == "A model ID is required."


def test_duplicate_model_id_is_refused(model_dialog):
    model_dialog.id_edit.setText("gpt-4o")
    assert "already exists" in model_dialog.error_label.text()


def test_new_model_id_is_accepted(model_dialog):
    model_dialog.id_edit.setText("acme/new-model")
    assert model_dialog.error_label.text() == ""


def test_add_model_builds_both_sections(model_dialog):
    model_dialog.id_edit.setText("acme/new-model")
    model_dialog.provider_combo.setCurrentText("acme")
    model_dialog.mode_combo.setCurrentText("chat")
    values = model_dialog.values()
    assert values["id"] == "acme/new-model"
    assert values["sections"]["parameters"] == {"provider": "acme", "mode": "chat"}
    assert values["sections"]["pricing"] == {"provider": "acme", "mode": "chat"}


def test_add_model_can_skip_pricing_section(model_dialog):
    model_dialog.id_edit.setText("acme/params-only")
    model_dialog.with_pricing.setCurrentIndex(1)
    assert "pricing" not in model_dialog.values()["sections"]


def test_add_model_result_actually_merges(model_dialog):
    """The dialog's output must be accepted by the merge, not just well-formed."""
    model_dialog.id_edit.setText("acme/new-model")
    model_dialog.provider_combo.setCurrentText("acme")
    values = model_dialog.values()
    res = merge({}, {}, {values["id"]: values["sections"]})
    assert values["id"] in res.parameters
    assert res.new_models == [values["id"]]


def test_model_providers_are_offered_for_filtering(model_dialog):
    assert model_dialog.provider_combo.isEditable()
    texts = [model_dialog.provider_combo.itemText(i) for i in range(model_dialog.provider_combo.count())]
    assert "openai" in texts and "anthropic" in texts


# --------------------------------------------------------------------------- #
# AddFieldDialog
# --------------------------------------------------------------------------- #


def test_field_name_is_required(field_dialog):
    assert field_dialog.error_label.text() == "A field name is required."


def test_duplicate_field_is_refused(field_dialog):
    field_dialog.name_combo.setCurrentText("provider")
    assert "already present" in field_dialog.error_label.text()


def test_new_field_is_accepted(field_dialog):
    field_dialog.name_combo.setCurrentText("max_input_tokens")
    assert field_dialog.error_label.text() == ""


def test_add_field_refuses_a_cost_field_in_parameters(field_dialog):
    field_dialog.name_combo.setCurrentText("input_cost_per_token")
    assert "cost field" in field_dialog.error_label.text()


def test_add_field_placement_uses_dataset_vocabulary(app):
    """The check is about the dataset, not the fields this one model happens to have."""
    dialog = AddFieldDialog(
        section="parameters",
        model_id="m",
        known_fields=["max_input_tokens"],
        existing=set(),
    )
    dialog.name_combo.setCurrentText("input_cost_per_token")
    assert dialog.error_label.text()


def test_add_field_refuses_param_array_in_pricing(app):
    dialog = AddFieldDialog(
        section="pricing", model_id="m", known_fields=["input_cost_per_token"], existing=set()
    )
    dialog.name_combo.setCurrentText("model_parameters")
    assert "parameters" in dialog.error_label.text()


@pytest.mark.parametrize(
    "raw,expected",
    [
        ("0.0000025", 2.5e-06),
        ("8192", 8192),
        ("true", True),
        ("null", None),
        ('"chat"', "chat"),
        ('["us-east-1"]', ["us-east-1"]),
        ('{"min": 0, "max": 2}', {"min": 0, "max": 2}),
    ],
)
def test_value_is_parsed_as_json(raw, expected):
    assert _parse_value(raw) == expected


def test_unparseable_value_falls_back_to_the_literal_string():
    assert _parse_value("not json at all") == "not json at all"
    assert _parse_value("") == ""


def test_added_field_reaches_the_output(field_dialog):
    field_dialog.name_combo.setCurrentText("supports_reasoning")
    assert field_dialog.set_value_text("true")
    name, value = field_dialog.values()
    res = merge({"m": {"provider": "openai"}}, {}, {"m": {"parameters": {name: value}}})
    assert res.parameters["m"]["supports_reasoning"] is True
    assert res.parameters["m"]["provider"] == "openai"


def test_added_pricing_field_reaches_the_output(app):
    dialog = AddFieldDialog(
        section="pricing",
        model_id="m",
        known_fields=["input_cost_per_token"],
        existing={"provider"},
    )
    dialog.name_combo.setCurrentText("input_cost_per_token")
    assert dialog.set_value_text("0.000002")
    name, value = dialog.values()
    res = merge({}, {"m": {"provider": "openai"}}, {"m": {"pricing": {name: value}}})
    assert res.pricing["m"]["input_cost_per_token"] == 2e-06


# --------------------------------------------------------------------------- #
# window wiring
# --------------------------------------------------------------------------- #

#: "m" is the model being edited and deliberately carries almost nothing, so a
#: field can be added to it. "other" exists only to give the catalog something to
#: learn the field types from -- which is how the real datasheet works, since the
#: shape of a field is known from the thousands of other models that have it.
PARAMS = {
    "m": {"mode": "chat", "model_parameters": [
        {"id": "temperature", "type": "number", "range": {"min": 0, "max": 2}}]},
    "other": {
        "mode": "chat",
        "supports_vision": True,
        "model_parameters": [
            {"id": "temperature", "type": "number", "range": {"min": 0, "max": 2}},
            {"id": "reasoning_effort", "type": "select", "label": "Reasoning Effort",
             "options": [{"value": "low"}, {"value": "high"}], "default": "low"},
        ],
    },
}
PRICING = {"m": {"provider": "openai"}, "other": {"provider": "openai"}}


def _window(app, monkeypatch, overlay=None):
    """A MainWindow with data loaded but no worker thread."""
    from datasheet_editor.fieldinfo import build_catalog
    from datasheet_editor.gui.main_window import MainWindow
    from datasheet_editor.gui.models import build_rows

    # Constructing the window with nothing loaded raises the both-files-required
    # modal, which would block the test run headless.
    monkeypatch.setattr(QMessageBox, "information", staticmethod(lambda *a, **k: QMessageBox.Ok))

    window = MainWindow()
    window.parameters = PARAMS
    window.pricing = PRICING
    window.overlay = overlay or {}
    window.conflicts = {}
    window.param_catalog = build_catalog(PARAMS, PRICING)
    window.pricing_catalog = build_catalog(PRICING, PRICING, source="pricing")
    # The row list drives _selected_model(), so it has to be populated before a
    # model can be selected -- otherwise _on_add_field bails out at "no model
    # selected" and never opens the dialog under test.
    window._populate_rows(build_rows(PARAMS, PRICING, window.overlay))
    window._select_model("m")
    return window


def _drive(monkeypatch, script):
    """Answer the Add Field dialog with ``script(dialog)``.

    Returns Rejected when the dialog is showing an error, mirroring the real
    flow: OK is disabled while validation fails, so a refused dialog can never
    reach the caller as Accepted.
    """
    from PySide6.QtWidgets import QDialog

    def fake_exec(self):
        script(self)
        return QDialog.Rejected if self.error_label.text() else QDialog.Accepted

    monkeypatch.setattr(AddFieldDialog, "exec", fake_exec)


def test_window_adds_a_top_level_field(app, monkeypatch):
    window = _window(app, monkeypatch)
    _drive(monkeypatch, lambda d: (d.name_combo.setCurrentText("supports_vision"),
                                   d._checkbox.setChecked(False)))
    window._on_add_field("parameters")
    assert window.overlay["m"]["parameters"]["supports_vision"] is False
    assert "Added m.parameters.supports_vision" in window.status_label.text()


def test_window_routes_a_descriptor_into_the_param_array(app, monkeypatch):
    """The whole point of the target selector: no bogus top-level key."""
    window = _window(app, monkeypatch)
    window.overlay = {"m": {"parameters": {"model_parameters": [
        {"id": "already_added", "type": "boolean"}]}}}

    def script(d):
        d.target_combo.setCurrentIndex(1)
        d.name_combo.setCurrentText("reasoning_effort")
        d._combo.setCurrentText("high")

    _drive(monkeypatch, script)
    window._on_add_field("parameters")

    section = window.overlay["m"]["parameters"]
    assert "reasoning_effort" not in section, "descriptor leaked into a top-level key"
    # The overlay carries only what the user added. The engine matches on id, so
    # restating the model's own descriptors would bloat the custom file and put
    # original data where an override belongs.
    assert [i["id"] for i in section["model_parameters"]] == ["already_added", "reasoning_effort"]
    assert "model_parameters[id=reasoning_effort]" in window.status_label.text()

    result = merge(PARAMS, PRICING, window.overlay)
    ids = [i["id"] for i in result.parameters["m"]["model_parameters"]]
    # Everything survives: the overlay's own entry, the original's temperature,
    # and the one just added.
    assert ids == ["temperature", "already_added", "reasoning_effort"]


def test_window_descriptor_result_merges_against_the_original(app, monkeypatch):
    """End of the chain: what the window wrote must land in the merged output."""
    from datasheet_editor.merge import merge

    window = _window(app, monkeypatch)

    def script(d):
        d.target_combo.setCurrentIndex(1)
        d.name_combo.setCurrentText("reasoning_effort")
        d._combo.setCurrentText("high")

    _drive(monkeypatch, script)
    window._on_add_field("parameters")

    result = merge(PARAMS, PRICING, window.overlay)
    added = next(d for d in result.parameters["m"]["model_parameters"] if d["id"] == "reasoning_effort")
    assert added["type"] == "select"
    assert added["default"] == "high"
    # The other model's descriptors are untouched.
    assert len(result.parameters["other"]["model_parameters"]) == 2


def test_window_refuses_to_add_a_descriptor_the_model_already_has(app, monkeypatch):
    window = _window(app, monkeypatch)
    _drive(monkeypatch, lambda d: (d.target_combo.setCurrentIndex(1),
                                   d.name_combo.setCurrentText("temperature")))
    window._on_add_field("parameters")
    assert window.overlay == {}, "a refused dialog must not write to the overlay"


def test_window_passes_the_catalog_to_the_dialog(app, monkeypatch):
    """Without the catalog the dialog has no descriptions and no typed controls."""
    from PySide6.QtWidgets import QDialog

    window = _window(app, monkeypatch)
    seen = {}

    def fake_exec(self):
        seen["catalog"] = self._catalog
        return QDialog.Rejected

    monkeypatch.setattr(AddFieldDialog, "exec", fake_exec)
    window._on_add_field("parameters")
    assert seen["catalog"] is window.param_catalog


# --------------------------------------------------------------------------- #
# both-files-required dialog
# --------------------------------------------------------------------------- #


def _capture_information(monkeypatch):
    captured: dict = {}

    def fake(parent, title, text, *args, **kwargs):
        captured["title"] = title
        captured["text"] = text
        return QMessageBox.Ok

    monkeypatch.setattr(QMessageBox, "information", staticmethod(fake))
    return captured


def test_choosing_only_parameters_shows_a_dialog(app, monkeypatch):
    from datasheet_editor.gui.main_window import MainWindow

    captured = _capture_information(monkeypatch)
    window = MainWindow()
    window.parameters_path = __import__("pathlib").Path("tools/model_parameters.json")
    window._update_path_label()
    window._start_load()

    assert "title" in captured
    assert "model_pricing.json" in captured["text"]
    assert window.model_list.rowCount() == 0


def test_choosing_only_pricing_shows_a_dialog(app, monkeypatch):
    from datasheet_editor.gui.main_window import MainWindow

    captured = _capture_information(monkeypatch)
    window = MainWindow()
    window.pricing_path = __import__("pathlib").Path("tools/model_pricing.json")
    window._update_path_label()
    window._start_load()

    assert "model_parameters.json" in captured["text"]


def test_incomplete_dialog_explains_why_both_are_needed(app, monkeypatch):
    from datasheet_editor.gui.main_window import MainWindow

    captured = _capture_information(monkeypatch)
    window = MainWindow()
    window.parameters_path = __import__("pathlib").Path("tools/model_parameters.json")
    window._update_path_label()
    window._start_load()

    text = captured["text"]
    assert "GetCapabilityEntry" in text  # the actual reason
    assert "Load Pricing" in text  # and what to press