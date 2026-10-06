"""Tests for the Zed /models importer.

The importer converts a Zed ``GET /models`` file into ``zed/<id>`` custom
overlay entries. The contract being pinned:

- every written field comes from the Zed file (verbatim or via a documented
  rename); nothing is invented, and no base-provider datasheet data is mixed in;
- models with no ``supported_effort_levels`` get an explicit empty ladder and
  no ``reasoning_effort`` descriptor (so the playground offers no effort
  control), rather than inheriting one;
- effort values keep their Zed casing (Gemini's ``MINIMAL``/``LOW``/...);
- fields with no ``ModelCapabilities`` counterpart are omitted, not persisted
  as dead data Go drops on load;
- existing overlay entries are never touched unless overwrite is asked for;
- the CLI writes the custom file (or dry-runs) with the same exit-code
  contract as the other commands.
"""

from __future__ import annotations

import json

import pytest

from datasheet_editor import cli
from datasheet_editor.zed_import import (
    ZedImportError,
    apply_import,
    import_zed_models,
    parse_zed_models,
    zed_model_to_entry,
)

ZED_ANTHROPIC = {
    "provider": "anthropic",
    "id": "claude-sonnet-5-5",
    "display_name": "Claude Sonnet 5.5",
    "is_latest": True,
    "max_token_count": 1000000,
    "max_token_count_in_max_mode": None,
    "max_output_tokens": 128000,
    "supports_tools": True,
    "supports_images": True,
    "supports_thinking": True,
    "supports_disabling_thinking": False,
    "supported_effort_levels": [
        {"name": "Low", "value": "low"},
        {"name": "Medium", "value": "medium"},
        {"name": "High", "value": "high", "is_default": True},
        {"name": "Extra High", "value": "xhigh"},
        {"name": "Max", "value": "max"},
    ],
    "supports_max_mode": False,
    "supports_fast_mode": False,
    "supports_server_side_compaction": True,
    "supports_streaming_tools": True,
    "supports_parallel_tool_calls": True,
    "is_disabled": False,
}

ZED_NANO = {
    "provider": "open_ai",
    "id": "gpt-5-nano",
    "display_name": "GPT-5 nano",
    "is_latest": False,
    "max_token_count": 400000,
    "max_token_count_in_max_mode": None,
    "max_output_tokens": 128000,
    "supports_tools": True,
    "supports_images": True,
    "supports_thinking": False,
    "supports_disabling_thinking": False,
    "supported_effort_levels": [],
    "supports_max_mode": False,
    "supports_fast_mode": False,
    "supports_server_side_compaction": False,
    "supports_streaming_tools": True,
    "supports_parallel_tool_calls": True,
    "is_disabled": False,
}

ZED_GEMINI = {
    "provider": "google",
    "id": "gemini-3.5-flash",
    "display_name": "Gemini 3.5 Flash",
    "is_latest": False,
    "max_token_count": 1048576,
    "max_token_count_in_max_mode": None,
    "max_output_tokens": 65535,
    "supports_tools": True,
    "supports_images": True,
    "supports_thinking": True,
    "supports_disabling_thinking": True,
    "supported_effort_levels": [
        {"name": "Minimal", "value": "MINIMAL"},
        {"name": "Low", "value": "LOW"},
        {"name": "Medium", "value": "MEDIUM", "is_default": True},
        {"name": "High", "value": "HIGH"},
    ],
    "supports_max_mode": False,
    "supports_fast_mode": False,
    "supports_server_side_compaction": False,
    "supports_streaming_tools": False,
    "supports_parallel_tool_calls": False,
    "is_disabled": False,
}


def zed_payload(*models):
    return {"models": list(models)}


# --------------------------------------------------------------------------- #
# payload validation
# --------------------------------------------------------------------------- #


def test_non_catalog_shape_is_rejected():
    with pytest.raises(ZedImportError):
        parse_zed_models({"data": []})
    with pytest.raises(ZedImportError):
        parse_zed_models([])


def test_missing_required_field_is_rejected():
    bad = dict(ZED_NANO)
    del bad["max_token_count"]
    with pytest.raises(ZedImportError, match="max_token_count"):
        parse_zed_models(zed_payload(bad))


def test_duplicate_id_is_rejected():
    with pytest.raises(ZedImportError, match="duplicate"):
        parse_zed_models(zed_payload(ZED_NANO, ZED_NANO))


def test_unknown_inner_provider_is_rejected_not_guessed():
    bad = dict(ZED_NANO, provider="mistral")
    with pytest.raises(ZedImportError, match="unknown inner provider"):
        parse_zed_models(zed_payload(bad))


# --------------------------------------------------------------------------- #
# entry mapping
# --------------------------------------------------------------------------- #


def test_anthropic_maps_thinking_and_effort():
    entry = zed_model_to_entry(ZED_ANTHROPIC)
    params = entry["parameters"]
    assert params["provider"] == "zed"
    assert params["mode"] == "chat"
    assert params["max_input_tokens"] == 1000000
    assert params["max_output_tokens"] == 128000
    assert params["supports_reasoning"] is True
    assert params["supports_reasoning_effort"] is True
    assert params["reasoning_effort_levels"] == ["low", "medium", "high", "xhigh", "max"]
    assert params["default_reasoning_effort"] == "high"
    assert params["supports_reasoning_disable"] is False
    assert params["supports_compaction"] is True
    assert params["supports_speed"] is False
    assert params["supports_function_calling"] is True
    assert params["supports_tool_choice"] is True
    assert params["supports_parallel_function_calling"] is True
    assert params["supports_vision"] is True
    ids = [d["id"] for d in params["model_parameters"]]
    assert ids == ["reasoning_effort", "thinking"]
    effort = next(d for d in params["model_parameters"] if d["id"] == "reasoning_effort")
    assert [o["value"] for o in effort["options"]] == ["low", "medium", "high", "xhigh", "max"]
    assert effort["default"] == "high"


def test_effort_empty_model_gets_explicit_empty_and_no_descriptor():
    """The gpt-5-nano case: no ladder, no descriptor, no inherited default."""
    entry = zed_model_to_entry(ZED_NANO)
    params = entry["parameters"]
    assert params["supports_reasoning"] is False
    assert params["supports_reasoning_effort"] is False
    assert params["reasoning_effort_levels"] == []
    assert "default_reasoning_effort" not in params
    assert "model_parameters" not in params


def test_gemini_keeps_uppercase_levels_verbatim():
    entry = zed_model_to_entry(ZED_GEMINI)
    assert entry["parameters"]["provider"] == "zed"
    assert entry["parameters"]["reasoning_effort_levels"] == [
        "MINIMAL", "LOW", "MEDIUM", "HIGH",
    ]
    assert entry["parameters"]["default_reasoning_effort"] == "MEDIUM"
    assert entry["parameters"]["supports_parallel_function_calling"] is False


def test_no_base_model_and_no_omitted_fields():
    """The key is the identity; dead fields Go would drop are not persisted."""
    entry = zed_model_to_entry(ZED_ANTHROPIC)
    for section in ("parameters", "pricing"):
        for dead in (
            "base_model",
            "display_name",
            "is_latest",
            "max_token_count_in_max_mode",
            "supports_max_mode",
            "is_disabled",
            "supports_streaming_tools",
            "supports_native_streaming",
        ):
            assert dead not in entry[section], f"{section}.{dead}"
    assert set(entry) == {"parameters", "pricing"}


def test_pricing_section_carries_identity_and_limits_only():
    """Descriptors and capability flags live in parameters; pricing carries
    what the merge engine's placement rule accepts in that section."""
    entry = zed_model_to_entry(ZED_ANTHROPIC)
    assert "model_parameters" not in entry["pricing"]
    assert not any("cost" in name for name in entry["pricing"])
    assert entry["pricing"] == {
        "mode": "chat",
        "provider": "zed",
        "max_input_tokens": 1000000,
        "max_output_tokens": 128000,
    }


def test_model_without_default_gets_no_default_key():
    model = dict(ZED_ANTHROPIC)
    model["supported_effort_levels"] = [
        {"name": "Low", "value": "low"},
        {"name": "High", "value": "high"},
    ]
    entry = zed_model_to_entry(model)
    assert "default_reasoning_effort" not in entry["parameters"]
    effort = next(
        d for d in entry["parameters"]["model_parameters"] if d["id"] == "reasoning_effort"
    )
    assert "default" not in effort


def test_serving_provider_is_always_zed_regardless_of_inner_family():
    """The overlay provider is the Bifrost provider, not the model's origin.

    Zed's inner discriminator (open_ai/anthropic/google) is routing metadata
    validated on the way in; the row must answer for provider "zed" or the
    capability lookup never matches it.
    """
    assert zed_model_to_entry(ZED_NANO)["parameters"]["provider"] == "zed"
    assert zed_model_to_entry(ZED_NANO)["pricing"]["provider"] == "zed"
    assert zed_model_to_entry(ZED_ANTHROPIC)["parameters"]["provider"] == "zed"
    assert zed_model_to_entry(ZED_GEMINI)["parameters"]["provider"] == "zed"


# --------------------------------------------------------------------------- #
# overlay application
# --------------------------------------------------------------------------- #


def test_import_keys_are_prefixed_and_reported():
    result = import_zed_models(zed_payload(ZED_ANTHROPIC, ZED_NANO), {})
    assert set(result.entries) == {"zed/claude-sonnet-5-5", "zed/gpt-5-nano"}
    assert result.added == ["zed/claude-sonnet-5-5", "zed/gpt-5-nano"]
    assert result.skipped == [] and result.overwritten == []


def test_existing_entry_is_skipped_by_default():
    overlay = {"zed/gpt-5-nano": {"parameters": {"mode": "chat"}}}
    result = import_zed_models(zed_payload(ZED_NANO), overlay)
    assert result.added == []
    assert result.skipped == ["zed/gpt-5-nano"]
    merged = apply_import(dict(overlay), result)
    assert merged["zed/gpt-5-nano"] == {"parameters": {"mode": "chat"}}


def test_existing_entry_is_replaced_with_overwrite():
    overlay = {"zed/gpt-5-nano": {"parameters": {"mode": "chat"}}}
    result = import_zed_models(zed_payload(ZED_NANO), overlay, overwrite_existing=True)
    assert result.overwritten == ["zed/gpt-5-nano"]
    merged = apply_import(dict(overlay), result, overwrite_existing=True)
    assert merged["zed/gpt-5-nano"]["parameters"]["supports_reasoning"] is False


def test_import_result_merges_against_the_originals():
    """What the importer builds must be accepted by the merge."""
    from datasheet_editor.merge import merge

    result = import_zed_models(zed_payload(ZED_NANO), {})
    overlay = apply_import({}, result)
    res = merge({}, {}, overlay)
    assert "zed/gpt-5-nano" in res.parameters
    assert "zed/gpt-5-nano" in res.pricing
    assert res.new_models == ["zed/gpt-5-nano"]


# --------------------------------------------------------------------------- #
# CLI: import-zed
# --------------------------------------------------------------------------- #


def _write(path, payload):
    path.write_text(json.dumps(payload), encoding="utf-8")


def test_cli_import_creates_custom_file(tmp_path):
    zed = tmp_path / "zed-models.json"
    custom = tmp_path / "custom.json"
    _write(zed, zed_payload(ZED_NANO))
    code = cli.main([
        "import-zed", "--zed-models", str(zed), "--custom", str(custom),
    ])
    assert code == cli.EXIT_OK
    saved = json.loads(custom.read_text(encoding="utf-8"))
    assert saved["zed/gpt-5-nano"]["parameters"]["supports_reasoning"] is False


def test_cli_import_skips_existing_without_flag(tmp_path, capsys):
    zed = tmp_path / "zed-models.json"
    custom = tmp_path / "custom.json"
    _write(zed, zed_payload(ZED_NANO))
    _write(custom, {"zed/gpt-5-nano": {"parameters": {"mode": "chat"}}})
    code = cli.main([
        "import-zed", "--zed-models", str(zed), "--custom", str(custom),
    ])
    assert code == cli.EXIT_OK
    saved = json.loads(custom.read_text(encoding="utf-8"))
    assert saved["zed/gpt-5-nano"] == {"parameters": {"mode": "chat"}}
    assert "already" in capsys.readouterr().out


def test_cli_import_overwrites_with_flag(tmp_path):
    zed = tmp_path / "zed-models.json"
    custom = tmp_path / "custom.json"
    _write(zed, zed_payload(ZED_NANO))
    _write(custom, {"zed/gpt-5-nano": {"parameters": {"mode": "chat"}}})
    code = cli.main([
        "import-zed", "--zed-models", str(zed), "--custom", str(custom),
        "--overwrite-existing",
    ])
    assert code == cli.EXIT_OK
    saved = json.loads(custom.read_text(encoding="utf-8"))
    assert saved["zed/gpt-5-nano"]["parameters"]["supports_reasoning"] is False


def test_cli_import_dry_run_writes_nothing(tmp_path, capsys):
    zed = tmp_path / "zed-models.json"
    custom = tmp_path / "custom.json"
    _write(zed, zed_payload(ZED_NANO))
    code = cli.main([
        "import-zed", "--zed-models", str(zed), "--custom", str(custom), "--dry-run",
    ])
    assert code == cli.EXIT_OK
    assert not custom.exists()
    assert "zed/gpt-5-nano" in capsys.readouterr().out


def test_cli_import_rejects_non_zed_file(tmp_path):
    zed = tmp_path / "zed-models.json"
    custom = tmp_path / "custom.json"
    _write(zed, {"hello": "world"})
    code = cli.main([
        "import-zed", "--zed-models", str(zed), "--custom", str(custom),
    ])
    assert code == cli.EXIT_VALIDATION
    assert not custom.exists()


# --------------------------------------------------------------------------- #
# GUI: ZedImportDialog + window wiring
# --------------------------------------------------------------------------- #

pytest.importorskip("PySide6", reason="PySide6 not installed")

from PySide6.QtWidgets import QApplication, QDialog  # noqa: E402

from datasheet_editor.gui.adddialogs import ZedImportDialog  # noqa: E402


@pytest.fixture(scope="module")
def gui_app():
    return QApplication.instance() or QApplication([])


def test_zed_import_dialog_reports_counts(gui_app):
    dialog = ZedImportDialog("zed-models.json", ["zed/a"], ["zed/b"])
    text = dialog._list_text.toPlainText()
    assert "+ zed/a" in text
    assert "= zed/b (already present)" in text
    assert not dialog.overwrite_box.isChecked()
    assert dialog.overwrite_existing() is False


def test_zed_import_dialog_overwrite_disabled_without_skipped(gui_app):
    dialog = ZedImportDialog("zed-models.json", ["zed/a"], [])
    assert not dialog.overwrite_box.isEnabled()


def test_zed_import_dialog_overwrite_opt_in(gui_app):
    dialog = ZedImportDialog("zed-models.json", [], ["zed/b"])
    dialog.overwrite_box.setChecked(True)
    dialog._accept()
    assert dialog.overwrite_existing() is True


def test_window_import_applies_to_overlay(gui_app, monkeypatch, tmp_path):
    import json as _json

    from PySide6.QtWidgets import QFileDialog, QMessageBox  # noqa: E402

    from datasheet_editor.gui.main_window import MainWindow  # noqa: E402

    zed = tmp_path / "zed-models.json"
    zed.write_text(_json.dumps(zed_payload(ZED_NANO)), encoding="utf-8")

    monkeypatch.setattr(
        QFileDialog, "getOpenFileName",
        staticmethod(lambda *a, **k: (str(zed), "JSON files (*.json)")),
    )
    monkeypatch.setattr(
        ZedImportDialog, "exec", lambda self: QDialog.Accepted,
    )

    window = MainWindow()
    window.overlay = {}
    window._on_import_zed()
    assert "zed/gpt-5-nano" in window.overlay
    assert window.overlay["zed/gpt-5-nano"]["parameters"]["supports_reasoning"] is False
    assert window.dirty is True
    assert "Save Custom" in window.status_label.text()


def test_window_import_cancel_leaves_overlay_untouched(gui_app, monkeypatch, tmp_path):
    import json as _json

    from PySide6.QtWidgets import QFileDialog  # noqa: E402

    from datasheet_editor.gui.main_window import MainWindow  # noqa: E402

    zed = tmp_path / "zed-models.json"
    zed.write_text(_json.dumps(zed_payload(ZED_NANO)), encoding="utf-8")

    monkeypatch.setattr(
        QFileDialog, "getOpenFileName",
        staticmethod(lambda *a, **k: (str(zed), "JSON files (*.json)")),
    )
    monkeypatch.setattr(ZedImportDialog, "exec", lambda self: QDialog.Rejected)

    window = MainWindow()
    before = {"existing": {"parameters": {"mode": "chat"}}}
    window.overlay = dict(before)
    window._on_import_zed()
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
    window._on_import_zed()
    assert seen.get("shown") is True
    assert window.overlay == {}
