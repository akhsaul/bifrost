"""Tests for the Antigravity fetchAvailableModels importer.

The importer converts the user-facing id lists of an Antigravity
``v1internal:fetchAvailableModels`` capture into ``antigravity/<id>``
custom overlay entries. The contract being pinned:

- only ids from ``agentModelSorts`` / ``commandModelIds`` /
  ``imageGenerationModelIds`` / ``mqueryModelIds`` / ``webSearchModelIds`` /
  ``commitMessageModelIds`` are converted; everything else in the file is
  discarded, not guessed at;
- a model with ``isInternal: true`` is excluded and reported as a warning;
- the mode comes from the source list (image ids -> ``image_generation``,
  every other list -> ``chat``); the same id in two lists with disagreeing
  modes is an error;
- token limits are optional (some capture rows carry none) but validated
  when present; flags are written explicitly only when the capture reports
  the key; no prices are invented;
- ``supported_endpoints`` is always ``/v1internal:generateContent`` (the
  single upstream wire);
- existing overlay entries are never touched unless overwrite is asked for;
- the CLI writes the custom file (or dry-runs) with the same exit-code
  contract as the other commands.
"""

from __future__ import annotations

import json

import pytest

from datasheet_editor import cli
from datasheet_editor.antigravity_import import (
    AntigravityImportError,
    apply_antigravity_import,
    antigravity_model_to_entry,
    import_antigravity_models,
    parse_agy_models,
)

AGENT_MODEL = {
    "displayName": "Gemini 3.8 Flash (High)",
    "supportsImages": True,
    "supportsThinking": True,
    "supportsAdaptiveThinking": True,
    "thinkingBudget": -1,
    "minThinkingBudget": 128,
    "recommended": True,
    "maxTokens": 1048576,
    "maxOutputTokens": 65536,
    "quotaInfo": {"remainingFraction": 0.5},
    "model": "MODEL_GOOGLE_GEMINI_3_8_FLASH",
    "apiProvider": "API_PROVIDER_GOOGLE_GEMINI",
    "modelProvider": "MODEL_PROVIDER_GOOGLE",
}

COMMAND_MODEL = {
    "displayName": "Gemini 3 Flash",
    "supportsImages": True,
    "supportsThinking": True,
    "thinkingBudget": 10001,
    "minThinkingBudget": 128,
    "recommended": True,
    "maxTokens": 1048576,
    "maxOutputTokens": 65536,
}

IMAGE_MODEL = {
    "displayName": "Gemini 3.1 Flash Image",
    "quotaInfo": {"remainingFraction": 1},
    "model": "MODEL_GOOGLE_GEMINI_3_1_FLASH_IMAGE",
    "apiProvider": "API_PROVIDER_GOOGLE_GEMINI",
    "modelProvider": "MODEL_PROVIDER_GOOGLE",
}

INTERNAL_TAB = {
    "maxTokens": 16384,
    "quotaInfo": {"remainingFraction": 1},
    "model": "MODEL_CHAT_20706",
    "apiProvider": "API_PROVIDER_INTERNAL",
    "modelProvider": "MODEL_PROVIDER_GOOGLE",
    "isInternal": True,
}


def _capture(**overrides):
    data = {
        "models": {
            "gemini-3.8-flash-high": dict(AGENT_MODEL),
            "gemini-3-flash": dict(COMMAND_MODEL),
            "gemini-3.1-flash-image": dict(IMAGE_MODEL),
            "chat_20706": dict(INTERNAL_TAB),
            "chat-internal-commit": dict(INTERNAL_TAB, isInternal=True),
        },
        "defaultAgentModelId": "gemini-3.8-flash-high",
        "agentModelSorts": [
            {"displayName": "Recommended", "groups": [{"modelIds": ["gemini-3.8-flash-high"]}]}
        ],
        "commandModelIds": ["gemini-3-flash"],
        "tabModelIds": ["chat_20706"],
        "imageGenerationModelIds": ["gemini-3.1-flash-image"],
        "mqueryModelIds": [],
        "webSearchModelIds": [],
        "deprecatedModelIds": {},
        "commitMessageModelIds": ["chat-internal-commit"],
        "audioTranscriptionModelIds": [],
        "experimentIds": [],
        "tieredModelIds": {},
    }
    data.update(overrides)
    return data


# --------------------------------------------------------------------------- #
# payload validation
# --------------------------------------------------------------------------- #


def test_non_catalog_shape_is_rejected():
    with pytest.raises(AntigravityImportError):
        parse_agy_models({"hello": "world"})
    with pytest.raises(AntigravityImportError):
        parse_agy_models(["not", "a", "dict"])


def test_missing_models_object_is_rejected():
    with pytest.raises(AntigravityImportError):
        parse_agy_models({"agentModelSorts": []})


def test_no_selected_ids_is_rejected():
    with pytest.raises(AntigravityImportError):
        parse_agy_models({"models": {}, "agentModelSorts": [], "commandModelIds": []})


def test_bad_agent_sorts_shape_is_rejected():
    bad = _capture(agentModelSorts=[{"groups": "nope"}])
    with pytest.raises(AntigravityImportError):
        parse_agy_models(bad)


def test_bad_flat_list_is_rejected():
    bad = _capture(commandModelIds="gemini-3-flash")
    with pytest.raises(AntigravityImportError):
        parse_agy_models(bad)


def test_selected_id_missing_from_models_is_rejected():
    bad = _capture(commandModelIds=["gemini-3-flash", "ghost-model"])
    with pytest.raises(AntigravityImportError):
        parse_agy_models(bad)


def test_non_integer_token_limit_is_rejected():
    bad = _capture()
    bad["models"]["gemini-3-flash"]["maxTokens"] = "1048576"
    with pytest.raises(AntigravityImportError):
        parse_agy_models(bad)


def test_boolean_token_limit_is_rejected():
    bad = _capture()
    bad["models"]["gemini-3-flash"]["maxOutputTokens"] = True
    with pytest.raises(AntigravityImportError):
        parse_agy_models(bad)


def test_conflicting_modes_for_one_id_are_rejected():
    bad = _capture(imageGenerationModelIds=["gemini-3-flash"])
    with pytest.raises(AntigravityImportError) as excinfo:
        parse_agy_models(bad)
    assert "gemini-3-flash" in str(excinfo.value)


# --------------------------------------------------------------------------- #
# selection rules
# --------------------------------------------------------------------------- #


def test_only_the_six_lists_are_converted():
    result = import_antigravity_models(_capture(), {})
    assert set(result.entries) == {
        "antigravity/gemini-3.8-flash-high",
        "antigravity/gemini-3-flash",
        "antigravity/gemini-3.1-flash-image",
    }
    # tabModelIds and other lists never enter the overlay.
    assert "antigravity/chat_20706" not in result.entries


def test_is_internal_models_are_excluded_with_a_warning():
    result = import_antigravity_models(_capture(), {})
    assert "antigravity/chat-internal-commit" not in result.entries
    assert any("chat-internal-commit" in w and "isInternal" in w for w in result.warnings)


def test_same_id_in_two_chat_lists_converts_once():
    data = _capture(webSearchModelIds=["gemini-3-flash"])
    result = import_antigravity_models(data, {})
    assert list(result.entries).count("antigravity/gemini-3-flash") == 1


# --------------------------------------------------------------------------- #
# field mapping
# --------------------------------------------------------------------------- #


def test_agent_model_maps_reasoning_and_budget():
    entry = antigravity_model_to_entry(dict(AGENT_MODEL), "chat")
    params = entry["parameters"]
    assert params["mode"] == "chat"
    assert params["provider"] == "antigravity"
    assert params["supported_endpoints"] == ["/v1internal:generateContent"]
    assert params["max_input_tokens"] == 1048576
    assert params["max_output_tokens"] == 65536
    assert params["context_length"] == 1048576
    # max_tokens is the full window: input + output.
    assert params["max_tokens"] == 1048576 + 65536
    assert params["supports_vision"] is True
    assert params["supports_reasoning"] is True
    assert params["supports_adaptive_thinking"] is True
    # thinkingBudget: -1 means "dynamic" in Agy, not a literal negative budget.
    assert params["supports_dynamic_reasoning_budget"] is True
    assert params["reasoning_budget"] == {"min": 128}


def test_positive_thinking_budget_maps_to_budget_max():
    entry = antigravity_model_to_entry(dict(COMMAND_MODEL), "chat")
    params = entry["parameters"]
    assert params["reasoning_budget"] == {"min": 128, "max": 10001}
    assert "supports_dynamic_reasoning_budget" not in params


def test_missing_token_limits_leave_the_fields_absent():
    entry = antigravity_model_to_entry(dict(IMAGE_MODEL), "image_generation")
    params = entry["parameters"]
    assert "max_input_tokens" not in params
    assert "max_output_tokens" not in params
    assert "context_length" not in params
    assert "max_tokens" not in params
    # A row that omits every capability key says nothing, not "false".
    assert "supports_vision" not in params
    assert "supports_reasoning" not in params
    assert params["mode"] == "image_generation"


def test_input_only_limits_fall_back_to_max_tokens_without_sum():
    model = {"maxTokens": 1048576}
    entry = antigravity_model_to_entry(dict(model), "chat")
    for section in ("parameters", "pricing"):
        assert entry[section]["max_input_tokens"] == 1048576
        assert entry[section]["context_length"] == 1048576
        assert entry[section]["max_tokens"] == 1048576
        assert "max_output_tokens" not in entry[section]


def test_image_generation_mode_comes_from_the_image_list():
    result = import_antigravity_models(_capture(), {})
    assert (
        result.entries["antigravity/gemini-3.1-flash-image"]["parameters"]["mode"]
        == "image_generation"
    )
    assert result.entries["antigravity/gemini-3-flash"]["parameters"]["mode"] == "chat"


def test_pricing_carries_identity_and_limits_only():
    result = import_antigravity_models(_capture(), {})
    pricing = result.entries["antigravity/gemini-3-flash"]["pricing"]
    assert pricing == {
        "mode": "chat",
        "provider": "antigravity",
        "max_input_tokens": 1048576,
        "max_output_tokens": 65536,
        "context_length": 1048576,
        "max_tokens": 1048576 + 65536,
    }
    # No capability flags, no invented costs.
    for key in pricing:
        assert not key.startswith("supports_")
        assert "cost" not in key


def test_no_cost_fields_anywhere():
    result = import_antigravity_models(_capture(), {})
    for entry in result.entries.values():
        assert "cost" not in entry["pricing"]


# --------------------------------------------------------------------------- #
# overlay merge semantics
# --------------------------------------------------------------------------- #


def test_import_keys_carry_the_antigravity_prefix():
    result = import_antigravity_models(_capture(), {})
    assert all(key.startswith("antigravity/") for key in result.entries)


def test_existing_entry_is_skipped_by_default():
    overlay = {"antigravity/gemini-3-flash": {"parameters": {"mode": "chat"}}}
    result = import_antigravity_models(_capture(), overlay)
    assert result.skipped == ["antigravity/gemini-3-flash"]
    assert "antigravity/gemini-3-flash" not in result.added


def test_existing_entry_is_replaced_with_overwrite():
    overlay = {"antigravity/gemini-3-flash": {"parameters": {"mode": "chat"}}}
    result = import_antigravity_models(_capture(), overlay, overwrite_existing=True)
    assert result.overwritten == ["antigravity/gemini-3-flash"]


def test_import_result_merges_against_the_overlay():
    overlay = {"antigravity/gemini-3-flash": {"parameters": {"mode": "chat"}}}
    result = import_antigravity_models(_capture(), overlay)
    apply_antigravity_import(overlay, result)
    assert overlay["antigravity/gemini-3-flash"] == {"parameters": {"mode": "chat"}}
    assert "antigravity/gemini-3.8-flash-high" in overlay

    result = import_antigravity_models(_capture(), overlay, overwrite_existing=True)
    apply_antigravity_import(overlay, result, overwrite_existing=True)
    assert overlay["antigravity/gemini-3-flash"]["parameters"]["supports_reasoning"] is True


# --------------------------------------------------------------------------- #
# CLI
# --------------------------------------------------------------------------- #


def _write(path, payload):
    path.write_text(json.dumps(payload), encoding="utf-8")


def test_cli_import_creates_custom_file(tmp_path):
    src = tmp_path / "agy-models.json"
    custom = tmp_path / "custom.json"
    _write(src, _capture())
    code = cli.main([
        "import-antigravity", "--agy-models", str(src), "--custom", str(custom),
    ])
    assert code == cli.EXIT_OK
    saved = json.loads(custom.read_text(encoding="utf-8"))
    assert saved["antigravity/gemini-3-flash"]["parameters"]["supports_reasoning"] is True


def test_cli_import_skips_existing_without_flag(tmp_path, capsys):
    src = tmp_path / "agy-models.json"
    custom = tmp_path / "custom.json"
    _write(src, _capture())
    _write(custom, {"antigravity/gemini-3-flash": {"parameters": {"mode": "chat"}}})
    code = cli.main([
        "import-antigravity", "--agy-models", str(src), "--custom", str(custom),
    ])
    assert code == cli.EXIT_OK
    saved = json.loads(custom.read_text(encoding="utf-8"))
    assert saved["antigravity/gemini-3-flash"] == {"parameters": {"mode": "chat"}}
    assert "skipped" in capsys.readouterr().out


def test_cli_import_overwrites_with_flag(tmp_path):
    src = tmp_path / "agy-models.json"
    custom = tmp_path / "custom.json"
    _write(src, _capture())
    _write(custom, {"antigravity/gemini-3-flash": {"parameters": {"mode": "chat"}}})
    code = cli.main([
        "import-antigravity", "--agy-models", str(src), "--custom", str(custom),
        "--overwrite-existing",
    ])
    assert code == cli.EXIT_OK
    saved = json.loads(custom.read_text(encoding="utf-8"))
    assert saved["antigravity/gemini-3-flash"]["parameters"]["supports_reasoning"] is True


def test_cli_import_dry_run_writes_nothing(tmp_path, capsys):
    src = tmp_path / "agy-models.json"
    custom = tmp_path / "custom.json"
    _write(src, _capture())
    code = cli.main([
        "import-antigravity", "--agy-models", str(src), "--custom", str(custom), "--dry-run",
    ])
    assert code == cli.EXIT_OK
    assert not custom.exists()
    assert "antigravity/gemini-3-flash" in capsys.readouterr().out


def test_cli_import_rejects_non_agy_file(tmp_path):
    src = tmp_path / "agy-models.json"
    custom = tmp_path / "custom.json"
    _write(src, {"hello": "world"})
    code = cli.main([
        "import-antigravity", "--agy-models", str(src), "--custom", str(custom),
    ])
    assert code == cli.EXIT_VALIDATION
    assert not custom.exists()


# --------------------------------------------------------------------------- #
# GUI: AntigravityImportDialog + window wiring
# --------------------------------------------------------------------------- #

pytest.importorskip("PySide6", reason="PySide6 not installed")

from PySide6.QtWidgets import QApplication, QDialog  # noqa: E402

from datasheet_editor.gui.adddialogs import AntigravityImportDialog  # noqa: E402


@pytest.fixture(scope="module")
def gui_app():
    return QApplication.instance() or QApplication([])


def test_antigravity_import_dialog_reports_counts(gui_app):
    dialog = AntigravityImportDialog("agy-models.json", ["antigravity/a"], ["antigravity/b"])
    text = dialog._list_text.toPlainText()
    assert "+ antigravity/a" in text
    assert "= antigravity/b (already present)" in text
    assert not dialog.overwrite_box.isChecked()
    assert dialog.overwrite_existing() is False


def test_antigravity_import_dialog_shows_exclusions(gui_app):
    dialog = AntigravityImportDialog(
        "agy-models.json", ["antigravity/a"], [],
        ["antigravity/chat_20706: excluded (isInternal: true)"],
    )
    assert "excluded (isInternal: true)" in dialog._list_text.toPlainText()


def test_antigravity_import_dialog_overwrite_disabled_without_skipped(gui_app):
    dialog = AntigravityImportDialog("agy-models.json", ["antigravity/a"], [])
    assert not dialog.overwrite_box.isEnabled()


def test_antigravity_import_dialog_overwrite_opt_in(gui_app):
    dialog = AntigravityImportDialog("agy-models.json", [], ["antigravity/b"])
    dialog.overwrite_box.setChecked(True)
    dialog._accept()
    assert dialog.overwrite_existing() is True


def test_window_import_applies_to_overlay(gui_app, monkeypatch, tmp_path):
    import json as _json

    from PySide6.QtWidgets import QFileDialog  # noqa: E402

    from datasheet_editor.gui.main_window import MainWindow  # noqa: E402

    src = tmp_path / "agy-models.json"
    src.write_text(_json.dumps(_capture()), encoding="utf-8")

    monkeypatch.setattr(
        QFileDialog, "getOpenFileName",
        staticmethod(lambda *a, **k: (str(src), "JSON files (*.json)")),
    )
    monkeypatch.setattr(
        AntigravityImportDialog, "exec", lambda self: QDialog.Accepted,
    )

    window = MainWindow()
    window.overlay = {}
    window._on_import_antigravity()
    assert "antigravity/gemini-3-flash" in window.overlay
    assert window.overlay["antigravity/gemini-3-flash"]["parameters"]["supports_reasoning"] is True
    assert window.dirty is True
    assert "Save To" in window.status_label.text()


def test_window_import_cancel_leaves_overlay_untouched(gui_app, monkeypatch, tmp_path):
    import json as _json

    from PySide6.QtWidgets import QFileDialog  # noqa: E402

    from datasheet_editor.gui.main_window import MainWindow  # noqa: E402

    src = tmp_path / "agy-models.json"
    src.write_text(_json.dumps(_capture()), encoding="utf-8")

    monkeypatch.setattr(
        QFileDialog, "getOpenFileName",
        staticmethod(lambda *a, **k: (str(src), "JSON files (*.json)")),
    )
    monkeypatch.setattr(AntigravityImportDialog, "exec", lambda self: QDialog.Rejected)

    window = MainWindow()
    before = {"existing": {"parameters": {"mode": "chat"}}}
    window.overlay = dict(before)
    window._on_import_antigravity()
    assert window.overlay == before


def test_window_import_bad_file_shows_error_and_writes_nothing(
    gui_app, monkeypatch, tmp_path
):
    from PySide6.QtWidgets import QFileDialog, QMessageBox  # noqa: E402

    from datasheet_editor.gui.main_window import MainWindow  # noqa: E402

    bad = tmp_path / "bad.json"
    bad.write_text('{"nope": true}', encoding="utf-8")
    monkeypatch.setattr(
        QFileDialog, "getOpenFileName",
        staticmethod(lambda *a, **k: (str(bad), "JSON files (*.json)")),
    )
    seen: dict = {}
    monkeypatch.setattr(
        QMessageBox, "critical",
        staticmethod(lambda *a, **k: seen.setdefault("shown", True)),
    )

    window = MainWindow()
    window.overlay = {}
    window._on_import_antigravity()
    assert seen.get("shown") is True
    assert window.overlay == {}
