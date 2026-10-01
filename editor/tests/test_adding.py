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
    field_dialog.value_edit.setPlainText("true")
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
    dialog.value_edit.setPlainText("0.000002")
    name, value = dialog.values()
    res = merge({}, {"m": {"provider": "openai"}}, {"m": {"pricing": {name: value}}})
    assert res.pricing["m"]["input_cost_per_token"] == 2e-06


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