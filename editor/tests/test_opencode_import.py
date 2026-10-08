"""Tests for the Opencode catalog importer.

The importer converts the ``opencode`` and ``opencode-go`` slices of an
Opencode models file into ``opencode-zen/<id>`` and ``opencode-go/<id>``
custom overlay entries. The contract being pinned:

- every written field comes from the source file (verbatim or via a
  documented rename/derivation); nothing is invented, and no base-provider
  datasheet data is mixed in;
- prices are quoted in $/1M tokens upstream and converted to Bifrost's
  $/token (``input: 0.8`` -> ``8e-07``), including free-tier zeros, which are
  written explicitly rather than treated as unknown;
- the endpoint comes from ``provider.npm`` (model-level, else the slice-level
  default), with ``openai-compatible`` checked before ``openai``; unknown npm
  values and models with no npm anywhere are rejected, not guessed;
- models with no effort ladder get an explicit empty list and no
  ``reasoning_effort`` descriptor (so the playground offers no effort
  control); the default is the smallest level (first in file order, kept
  verbatim -- never sorted);
- a ``none`` effort level sets ``supports_none_reasoning_effort``;
  ``toggle`` sets ``supports_reasoning_disable`` plus a ``thinking``
  descriptor; ``budget_tokens`` bounds map to ``reasoning_budget``;
- tiers of context size 200000/272000 map to the matching ``..._above_...``
  fields; any other tier size is dropped with a warning, and
  ``context_over_200k`` is ignored (it mirrors the 200000 tier entry);
- fields with no datasheet counterpart are omitted, not persisted as dead
  data Go drops on load;
- existing overlay entries are never touched unless overwrite is asked for;
- the CLI writes the custom file (or dry-runs) with the same exit-code
  contract as the other commands.
"""

from __future__ import annotations

import json

import pytest

from datasheet_editor import cli
from datasheet_editor.opencode_import import (
    OpencodeImportError,
    apply_opencode_import,
    import_opencode_models,
    opencode_model_to_entry,
    parse_opencode_models,
)

ANTHROPIC_MODEL = {
    "id": "claude-3-5-haiku",
    "limit": {"context": 200000, "output": 8192},
    "modalities": {"input": ["text", "image", "pdf"], "output": ["text"]},
    "cost": {"cache_read": 0.08, "cache_write": 1, "input": 0.8, "output": 4},
    "provider": {"npm": "@ai-sdk/anthropic"},
    "reasoning": False,
    "status": "deprecated",
    "temperature": True,
    "tool_call": True,
}

GEMINI_MODEL = {
    "id": "gemini-3-flash",
    "limit": {"context": 1048576, "output": 65536},
    "modalities": {"input": ["text", "image"], "output": ["text"]},
    "cost": {"cache_read": 0.05, "input": 0.5, "output": 3},
    "provider": {"npm": "@ai-sdk/google"},
    "reasoning": True,
    "reasoning_options": [
        {"type": "effort", "values": ["minimal", "low", "medium", "high"]}
    ],
    "temperature": True,
    "tool_call": True,
}

OPENAI_MODEL = {
    "id": "gpt-5",
    "limit": {"context": 400000, "input": 272000, "output": 128000},
    "modalities": {"input": ["text", "image"], "output": ["text"]},
    "cost": {"cache_read": 0.107, "input": 1.07, "output": 8.5},
    "provider": {"npm": "@ai-sdk/openai"},
    "reasoning": True,
    "reasoning_options": [
        {"type": "effort", "values": ["minimal", "low", "medium", "high"]}
    ],
    "structured_output": True,
    "temperature": False,
    "tool_call": True,
}

CHAT_MODEL = {
    "id": "big-pickle",
    "limit": {"context": 200000, "input": 160000, "output": 32000},
    "modalities": {"input": ["text"], "output": ["text"]},
    "cost": {"cache_read": 0, "cache_write": 0, "input": 0, "output": 0},
    "reasoning": True,
    "reasoning_options": [],
    "structured_output": True,
    "temperature": True,
    "tool_call": True,
}

TIERED_MODEL = {
    "id": "claude-sonnet-4",
    "limit": {"context": 1000000, "output": 200000},
    "modalities": {"input": ["text", "image", "pdf"], "output": ["text"]},
    "cost": {
        "cache_read": 0.3,
        "cache_write": 3.75,
        "context_over_200k": {
            "cache_read": 0.6,
            "cache_write": 7.5,
            "input": 6,
            "output": 22.5,
        },
        "input": 3,
        "output": 15,
        "tiers": [
            {
                "cache_read": 0.6,
                "cache_write": 7.5,
                "input": 6,
                "output": 22.5,
                "tier": {"size": 200000, "type": "context"},
            }
        ],
    },
    "provider": {"npm": "@ai-sdk/anthropic"},
    "reasoning": False,
    "temperature": True,
    "tool_call": True,
}

AUDIO_MODEL = {
    "id": "gemini-3.5-flash",
    "limit": {"context": 1048576, "output": 65536},
    "modalities": {"input": ["text", "audio"], "output": ["text"]},
    "cost": {"cache_read": 0.15, "input": 1.5, "input_audio": 1.5, "output": 9},
    "provider": {"npm": "@ai-sdk/google"},
    "reasoning": True,
    "reasoning_options": [{"type": "toggle"}],
    "temperature": True,
    "tool_call": True,
}


def opencode_payload(*slices):
    """Build a catalog payload from (slice_name, slice_dict) pairs."""
    return {name: slice_dict for name, slice_dict in slices}


def opencode_slice(*models, npm="@ai-sdk/openai-compatible"):
    return {"id": "opencode", "npm": npm, "models": {m["id"]: m for m in models}}


# --------------------------------------------------------------------------- #
# payload validation
# --------------------------------------------------------------------------- #


def test_non_catalog_shape_is_rejected():
    with pytest.raises(OpencodeImportError):
        parse_opencode_models({"models": []})
    with pytest.raises(OpencodeImportError):
        parse_opencode_models([])


def test_missing_slices_are_rejected_not_guessed():
    with pytest.raises(OpencodeImportError, match="no 'opencode'"):
        parse_opencode_models({"302ai": {"models": {}}})


def test_missing_limits_are_rejected():
    bad = dict(CHAT_MODEL)
    del bad["limit"]
    with pytest.raises(OpencodeImportError, match="limit"):
        parse_opencode_models(opencode_payload(("opencode", opencode_slice(bad))))


def test_non_integer_limits_are_rejected():
    bad = dict(CHAT_MODEL, limit={"context": "200000", "output": 32000})
    with pytest.raises(OpencodeImportError, match="must be an integer"):
        parse_opencode_models(opencode_payload(("opencode", opencode_slice(bad))))


def test_duplicate_id_within_a_slice_is_rejected():
    dup = dict(CHAT_MODEL)
    data = opencode_payload(
        ("opencode", {"id": "opencode", "npm": "@ai-sdk/openai", "models": {
            "a": dup, "b": dict(dup),
        }})
    )
    with pytest.raises(OpencodeImportError, match="duplicate"):
        parse_opencode_models(data)


def test_same_id_across_slices_is_kept_not_an_error():
    rows = parse_opencode_models(
        opencode_payload(
            ("opencode", opencode_slice(CHAT_MODEL)),
            ("opencode-go", {"id": "opencode-go", "npm": "@ai-sdk/openai",
                             "models": {CHAT_MODEL["id"]: dict(CHAT_MODEL)}}),
        )
    )
    assert [provider for provider, _, _ in rows] == ["opencode-zen", "opencode-go"]


def test_slices_convert_in_fixed_order_regardless_of_file_order():
    data = opencode_payload(
        ("opencode-go", {"id": "opencode-go", "npm": "@ai-sdk/openai",
                         "models": {CHAT_MODEL["id"]: dict(CHAT_MODEL)}}),
        ("opencode", opencode_slice(CHAT_MODEL)),
    )
    rows = parse_opencode_models(data)
    assert [provider for provider, _, _ in rows] == ["opencode-zen", "opencode-go"]


def test_other_slices_are_discarded():
    rows = parse_opencode_models(
        opencode_payload(
            ("302ai", {"models": {"m": dict(CHAT_MODEL)}}),
            ("opencode", opencode_slice(CHAT_MODEL)),
        )
    )
    assert len(rows) == 1


# --------------------------------------------------------------------------- #
# endpoint resolution
# --------------------------------------------------------------------------- #


def test_anthropic_npm_resolves_to_messages():
    entry = opencode_model_to_entry(ANTHROPIC_MODEL, "opencode-zen",
                                    "@ai-sdk/openai-compatible", [])
    assert entry["parameters"]["supported_endpoints"] == ["/v1/messages"]


def test_google_npm_resolves_to_per_model_path():
    entry = opencode_model_to_entry(GEMINI_MODEL, "opencode-zen",
                                    "@ai-sdk/openai-compatible", [])
    assert entry["parameters"]["supported_endpoints"] == ["/v1/models/gemini-3-flash"]


def test_openai_npm_resolves_to_responses():
    entry = opencode_model_to_entry(OPENAI_MODEL, "opencode-zen",
                                    "@ai-sdk/openai-compatible", [])
    assert entry["parameters"]["supported_endpoints"] == ["/v1/responses"]


def test_openai_compatible_beats_openai_substring():
    entry = opencode_model_to_entry(CHAT_MODEL, "opencode-zen",
                                    "@ai-sdk/openai-compatible", [])
    assert entry["parameters"]["supported_endpoints"] == ["/v1/chat/completions"]


def test_slice_npm_is_the_fallback_when_model_has_none():
    assert "provider" not in CHAT_MODEL
    entry = opencode_model_to_entry(CHAT_MODEL, "opencode-zen",
                                    "@ai-sdk/openai-compatible", [])
    assert entry["parameters"]["supported_endpoints"] == ["/v1/chat/completions"]


def test_model_npm_beats_slice_npm():
    entry = opencode_model_to_entry(ANTHROPIC_MODEL, "opencode-zen",
                                    "@ai-sdk/openai-compatible", [])
    assert entry["parameters"]["supported_endpoints"] == ["/v1/messages"]


def test_unknown_npm_is_rejected_not_guessed():
    bad = dict(CHAT_MODEL, provider={"npm": "@ai-sdk/mistral"})
    with pytest.raises(OpencodeImportError, match="unknown npm"):
        opencode_model_to_entry(bad, "opencode-zen", "@ai-sdk/openai-compatible", [])


def test_missing_npm_everywhere_is_rejected_not_guessed():
    with pytest.raises(OpencodeImportError, match="no usable npm"):
        opencode_model_to_entry(CHAT_MODEL, "opencode-zen", None, [])


# --------------------------------------------------------------------------- #
# entry mapping
# --------------------------------------------------------------------------- #


def test_anthropic_maps_costs_per_token_with_cache_and_deprecated():
    entry = opencode_model_to_entry(ANTHROPIC_MODEL, "opencode-zen",
                                    "@ai-sdk/openai-compatible", [])
    params = entry["parameters"]
    assert params["provider"] == "opencode-zen"
    assert params["mode"] == "chat"
    assert params["max_input_tokens"] == 200000
    assert params["max_output_tokens"] == 8192
    assert params["supports_reasoning"] is False
    assert params["supports_reasoning_effort"] is False
    assert params["reasoning_effort_levels"] == []
    assert "default_reasoning_effort" not in params
    assert "model_parameters" not in params
    assert params["supports_function_calling"] is True
    assert params["supports_tool_choice"] is True
    assert params["supports_vision"] is True
    pricing = entry["pricing"]
    assert pricing["input_cost_per_token"] == 8e-07
    assert pricing["output_cost_per_token"] == 4e-06
    assert pricing["cache_read_input_token_cost"] == 8e-08
    assert pricing["cache_creation_input_token_cost"] == 1e-06
    assert pricing["is_deprecated"] is True
    assert pricing["max_input_tokens"] == 200000


def test_effort_default_is_the_smallest_level_and_kept_verbatim():
    entry = opencode_model_to_entry(GEMINI_MODEL, "opencode-zen",
                                    "@ai-sdk/openai-compatible", [])
    params = entry["parameters"]
    assert params["reasoning_effort_levels"] == ["minimal", "low", "medium", "high"]
    assert params["default_reasoning_effort"] == "minimal"
    effort = next(d for d in params["model_parameters"] if d["id"] == "reasoning_effort")
    assert [o["value"] for o in effort["options"]] == ["minimal", "low", "medium", "high"]
    assert effort["default"] == "minimal"


def test_effort_empty_model_gets_explicit_empty_and_no_descriptor():
    """The big-pickle case: no ladder, no descriptor, no invented default."""
    entry = opencode_model_to_entry(CHAT_MODEL, "opencode-zen",
                                    "@ai-sdk/openai-compatible", [])
    params = entry["parameters"]
    assert params["supports_reasoning"] is True
    assert params["supports_reasoning_effort"] is False
    assert params["reasoning_effort_levels"] == []
    assert "default_reasoning_effort" not in params
    assert "model_parameters" not in params


def test_none_effort_level_sets_none_flag_not_default():
    model = dict(CHAT_MODEL)
    model["reasoning_options"] = [{"type": "effort", "values": ["none", "high"]}]
    entry = opencode_model_to_entry(model, "opencode-zen",
                                    "@ai-sdk/openai-compatible", [])
    params = entry["parameters"]
    assert params["reasoning_effort_levels"] == ["none", "high"]
    assert "default_reasoning_effort" not in params
    assert params["supports_none_reasoning_effort"] is True
    effort = next(d for d in params["model_parameters"] if d["id"] == "reasoning_effort")
    assert "default" not in effort


def test_toggle_maps_to_disable_flag_and_thinking_descriptor():
    entry = opencode_model_to_entry(AUDIO_MODEL, "opencode-zen",
                                    "@ai-sdk/openai-compatible", [])
    params = entry["parameters"]
    assert params["supports_reasoning_disable"] is True
    assert [d["id"] for d in params["model_parameters"]] == ["thinking"]


def test_budget_bounds_map_to_reasoning_budget():
    model = dict(CHAT_MODEL)
    model["reasoning_options"] = [{"type": "budget_tokens", "max": 81920}]
    entry = opencode_model_to_entry(model, "opencode-zen",
                                    "@ai-sdk/openai-compatible", [])
    assert entry["parameters"]["reasoning_budget"] == {"max": 81920}


def test_budget_bounds_require_integers():
    model = dict(CHAT_MODEL)
    model["reasoning_options"] = [{"type": "budget_tokens", "max": "a lot"}]
    with pytest.raises(OpencodeImportError, match="non-negative integer"):
        opencode_model_to_entry(model, "opencode-zen", "@ai-sdk/openai-compatible", [])


def test_unknown_reasoning_option_type_is_rejected():
    model = dict(CHAT_MODEL)
    model["reasoning_options"] = [{"type": "slider"}]
    with pytest.raises(OpencodeImportError, match="unknown reasoning option"):
        opencode_model_to_entry(model, "opencode-zen", "@ai-sdk/openai-compatible", [])


def test_temperature_false_maps_to_no_sampling_params():
    entry = opencode_model_to_entry(OPENAI_MODEL, "opencode-zen",
                                    "@ai-sdk/openai-compatible", [])
    assert entry["parameters"]["supports_sampling_params"] is False


def test_temperature_true_leaves_sampling_params_unset():
    entry = opencode_model_to_entry(ANTHROPIC_MODEL, "opencode-zen",
                                    "@ai-sdk/openai-compatible", [])
    assert "supports_sampling_params" not in entry["parameters"]


def test_structured_output_maps_explicitly_both_ways():
    on_model = dict(CHAT_MODEL, structured_output=True)
    on = opencode_model_to_entry(on_model, "opencode-zen",
                                 "@ai-sdk/openai-compatible", [])
    assert on["parameters"]["supports_response_schema"] is True
    off_model = dict(CHAT_MODEL)
    off_model.pop("structured_output", None)
    off = opencode_model_to_entry(off_model, "opencode-zen",
                                  "@ai-sdk/openai-compatible", [])
    assert "supports_response_schema" not in off["parameters"]


def test_tier_200k_maps_to_above_200k_fields():
    warnings: list[str] = []
    entry = opencode_model_to_entry(TIERED_MODEL, "opencode-zen",
                                    "@ai-sdk/openai-compatible", warnings)
    pricing = entry["pricing"]
    assert pricing["input_cost_per_token_above_200k_tokens"] == 6e-06
    assert pricing["output_cost_per_token_above_200k_tokens"] == 2.25e-05
    assert pricing["cache_read_input_token_cost_above_200k_tokens"] == 6e-07
    assert pricing["cache_creation_input_token_cost_above_200k_tokens"] == 7.5e-06
    assert warnings == []
    assert "context_over_200k" not in str(pricing)


def test_tier_without_counterpart_warns_and_drops():
    warnings: list[str] = []
    model = dict(TIERED_MODEL)
    model["cost"] = {
        "input": 0.12,
        "output": 0.48,
        "tiers": [{"input": 0.24, "output": 0.96,
                   "tier": {"size": 256000, "type": "context"}}],
    }
    entry = opencode_model_to_entry(model, "opencode-zen",
                                    "@ai-sdk/openai-compatible", warnings)
    assert entry["pricing"]["input_cost_per_token"] == 1.2e-07
    assert "input_cost_per_token_above_200k_tokens" not in entry["pricing"]
    assert len(warnings) == 1 and "256000" in warnings[0]


def test_unknown_cost_field_is_rejected():
    model = dict(CHAT_MODEL)
    model["cost"] = {"input": 0, "output": 0, "per_request": 1}
    with pytest.raises(OpencodeImportError, match="unknown cost"):
        opencode_model_to_entry(model, "opencode-zen", "@ai-sdk/openai-compatible", [])


def test_audio_cost_maps_to_audio_token_field():
    entry = opencode_model_to_entry(AUDIO_MODEL, "opencode-zen",
                                    "@ai-sdk/openai-compatible", [])
    assert entry["pricing"]["input_cost_per_audio_token"] == 1.5e-06


def test_free_tier_zero_is_written_not_omitted():
    entry = opencode_model_to_entry(CHAT_MODEL, "opencode-zen",
                                    "@ai-sdk/openai-compatible", [])
    assert entry["pricing"]["input_cost_per_token"] == 0
    assert entry["pricing"]["output_cost_per_token"] == 0


def test_float_division_noise_is_rounded_off():
    # 0.8/1e6 is 8.000000000000001e-07 unrounded; the file must say 8e-07.
    assert opencode_model_to_entry(
        ANTHROPIC_MODEL, "opencode-zen", "@ai-sdk/openai-compatible", []
    )["pricing"]["input_cost_per_token"] == 8e-07


def test_no_base_model_and_no_omitted_fields():
    """The key is the identity; dead fields Go would drop are not persisted."""
    entry = opencode_model_to_entry(OPENAI_MODEL, "opencode-zen",
                                    "@ai-sdk/openai-compatible", [])
    for section in ("parameters", "pricing"):
        for dead in (
            "base_model",
            "description",
            "family",
            "name",
            "open_weights",
            "knowledge",
            "release_date",
            "last_updated",
            "canonical_model_id",
            "attachment",
            "interleaved",
            "status",
            "input_audio",
            "context_over_200k",
        ):
            assert dead not in entry[section], f"{section}.{dead}"
    assert set(entry) == {"parameters", "pricing"}


def test_pricing_carries_no_capability_flags_or_descriptors():
    entry = opencode_model_to_entry(GEMINI_MODEL, "opencode-zen",
                                    "@ai-sdk/openai-compatible", [])
    pricing = entry["pricing"]
    assert "model_parameters" not in pricing
    assert "supported_endpoints" not in pricing
    assert "supports_reasoning" not in pricing
    assert set(pricing) <= {
        "mode", "provider", "max_input_tokens", "max_output_tokens",
        "is_deprecated", "input_cost_per_token", "output_cost_per_token",
        "cache_read_input_token_cost", "cache_creation_input_token_cost",
        "input_cost_per_audio_token",
        "input_cost_per_token_above_200k_tokens",
        "output_cost_per_token_above_200k_tokens",
        "cache_read_input_token_cost_above_200k_tokens",
        "cache_creation_input_token_cost_above_200k_tokens",
        "input_cost_per_token_above_272k_tokens",
        "output_cost_per_token_above_272k_tokens",
        "cache_read_input_token_cost_above_272k_tokens",
        "cache_creation_input_token_cost_above_272k_tokens",
    }


def test_non_deprecated_model_carries_no_deprecated_flag():
    entry = opencode_model_to_entry(GEMINI_MODEL, "opencode-zen",
                                    "@ai-sdk/openai-compatible", [])
    assert "is_deprecated" not in entry["pricing"]
    assert "is_deprecated" not in entry["parameters"]


def test_modalities_without_image_reports_vision_false():
    entry = opencode_model_to_entry(CHAT_MODEL, "opencode-zen",
                                    "@ai-sdk/openai-compatible", [])
    assert entry["parameters"]["supports_vision"] is False


def test_limit_input_has_no_counterpart_and_is_omitted():
    entry = opencode_model_to_entry(CHAT_MODEL, "opencode-zen",
                                    "@ai-sdk/openai-compatible", [])
    assert entry["parameters"]["max_input_tokens"] == 200000
    assert "input" not in entry["parameters"]


# --------------------------------------------------------------------------- #
# overlay application
# --------------------------------------------------------------------------- #


def _catalog(*models, npm="@ai-sdk/openai-compatible"):
    return opencode_payload(("opencode", opencode_slice(*models, npm=npm)))


def test_import_keys_carry_slice_provider_prefix():
    result = import_opencode_models(_catalog(ANTHROPIC_MODEL, CHAT_MODEL), {})
    assert set(result.entries) == {
        "opencode-zen/claude-3-5-haiku", "opencode-zen/big-pickle",
    }
    assert result.added == ["opencode-zen/claude-3-5-haiku", "opencode-zen/big-pickle"]
    assert result.skipped == [] and result.overwritten == []


def test_opencode_go_slice_maps_to_opencode_go_provider():
    result = import_opencode_models(
        {"opencode-go": {"id": "opencode-go", "npm": "@ai-sdk/openai",
                        "models": {CHAT_MODEL["id"]: dict(CHAT_MODEL)}}},
        {},
    )
    assert set(result.entries) == {"opencode-go/big-pickle"}
    assert result.entries["opencode-go/big-pickle"]["parameters"]["provider"] == "opencode-go"


def test_existing_entry_is_skipped_by_default():
    overlay = {"opencode-zen/big-pickle": {"parameters": {"mode": "chat"}}}
    result = import_opencode_models(_catalog(CHAT_MODEL), overlay)
    assert result.added == []
    assert result.skipped == ["opencode-zen/big-pickle"]
    merged = apply_opencode_import(dict(overlay), result)
    assert merged["opencode-zen/big-pickle"] == {"parameters": {"mode": "chat"}}


def test_existing_entry_is_replaced_with_overwrite():
    overlay = {"opencode-zen/big-pickle": {"parameters": {"mode": "chat"}}}
    result = import_opencode_models(_catalog(CHAT_MODEL), overlay, overwrite_existing=True)
    assert result.overwritten == ["opencode-zen/big-pickle"]
    merged = apply_opencode_import(dict(overlay), result, overwrite_existing=True)
    assert merged["opencode-zen/big-pickle"]["parameters"]["supports_reasoning"] is True


def test_warnings_belong_to_imported_entries_only():
    model = dict(TIERED_MODEL)
    model["cost"] = {
        "input": 0.12,
        "output": 0.48,
        "tiers": [{"input": 0.24, "output": 0.96,
                   "tier": {"size": 256000, "type": "context"}}],
    }
    warned_key = "opencode-zen/" + model["id"]
    overlay = {warned_key: {"parameters": {"mode": "chat"}}}
    result = import_opencode_models(_catalog(model), overlay)
    assert result.skipped == [warned_key]
    assert result.warnings == []


def test_import_result_merges_against_the_originals():
    """What the importer builds must be accepted by the merge."""
    from datasheet_editor.merge import merge

    result = import_opencode_models(_catalog(CHAT_MODEL), {})
    overlay = apply_opencode_import({}, result)
    res = merge({}, {}, overlay)
    assert "opencode-zen/big-pickle" in res.parameters
    assert "opencode-zen/big-pickle" in res.pricing
    assert res.new_models == ["opencode-zen/big-pickle"]


# --------------------------------------------------------------------------- #
# CLI: import-opencode
# --------------------------------------------------------------------------- #


def _write(path, payload):
    path.write_text(json.dumps(payload), encoding="utf-8")


def test_cli_import_creates_custom_file(tmp_path):
    src = tmp_path / "opencode-models.json"
    custom = tmp_path / "custom.json"
    _write(src, _catalog(CHAT_MODEL))
    code = cli.main([
        "import-opencode", "--opencode-models", str(src), "--custom", str(custom),
    ])
    assert code == cli.EXIT_OK
    saved = json.loads(custom.read_text(encoding="utf-8"))
    assert saved["opencode-zen/big-pickle"]["parameters"]["supports_reasoning"] is True


def test_cli_import_skips_existing_without_flag(tmp_path, capsys):
    src = tmp_path / "opencode-models.json"
    custom = tmp_path / "custom.json"
    _write(src, _catalog(CHAT_MODEL))
    _write(custom, {"opencode-zen/big-pickle": {"parameters": {"mode": "chat"}}})
    code = cli.main([
        "import-opencode", "--opencode-models", str(src), "--custom", str(custom),
    ])
    assert code == cli.EXIT_OK
    saved = json.loads(custom.read_text(encoding="utf-8"))
    assert saved["opencode-zen/big-pickle"] == {"parameters": {"mode": "chat"}}
    assert "already" in capsys.readouterr().out


def test_cli_import_overwrites_with_flag(tmp_path):
    src = tmp_path / "opencode-models.json"
    custom = tmp_path / "custom.json"
    _write(src, _catalog(CHAT_MODEL))
    _write(custom, {"opencode-zen/big-pickle": {"parameters": {"mode": "chat"}}})
    code = cli.main([
        "import-opencode", "--opencode-models", str(src), "--custom", str(custom),
        "--overwrite-existing",
    ])
    assert code == cli.EXIT_OK
    saved = json.loads(custom.read_text(encoding="utf-8"))
    assert saved["opencode-zen/big-pickle"]["parameters"]["supports_reasoning"] is True


def test_cli_import_dry_run_writes_nothing(tmp_path, capsys):
    src = tmp_path / "opencode-models.json"
    custom = tmp_path / "custom.json"
    _write(src, _catalog(CHAT_MODEL))
    code = cli.main([
        "import-opencode", "--opencode-models", str(src), "--custom", str(custom), "--dry-run",
    ])
    assert code == cli.EXIT_OK
    assert not custom.exists()
    assert "opencode-zen/big-pickle" in capsys.readouterr().out


def test_cli_import_rejects_non_opencode_file(tmp_path):
    src = tmp_path / "opencode-models.json"
    custom = tmp_path / "custom.json"
    _write(src, {"hello": "world"})
    code = cli.main([
        "import-opencode", "--opencode-models", str(src), "--custom", str(custom),
    ])
    assert code == cli.EXIT_VALIDATION
    assert not custom.exists()


# --------------------------------------------------------------------------- #
# GUI: OpencodeImportDialog + window wiring
# --------------------------------------------------------------------------- #

pytest.importorskip("PySide6", reason="PySide6 not installed")

from PySide6.QtWidgets import QApplication, QDialog  # noqa: E402

from datasheet_editor.gui.adddialogs import OpencodeImportDialog  # noqa: E402


@pytest.fixture(scope="module")
def gui_app():
    return QApplication.instance() or QApplication([])


def test_opencode_import_dialog_reports_counts(gui_app):
    dialog = OpencodeImportDialog("opencode-models.json", ["opencode-zen/a"], ["opencode-go/b"])
    text = dialog._list_text.toPlainText()
    assert "+ opencode-zen/a" in text
    assert "= opencode-go/b (already present)" in text
    assert not dialog.overwrite_box.isChecked()
    assert dialog.overwrite_existing() is False


def test_opencode_import_dialog_shows_warnings(gui_app):
    dialog = OpencodeImportDialog(
        "opencode-models.json", ["opencode-zen/a"], [],
        ["opencode-zen/a: dropped tier with context size 256000"],
    )
    assert "256000" in dialog._list_text.toPlainText()


def test_opencode_import_dialog_overwrite_disabled_without_skipped(gui_app):
    dialog = OpencodeImportDialog("opencode-models.json", ["opencode-zen/a"], [])
    assert not dialog.overwrite_box.isEnabled()


def test_opencode_import_dialog_overwrite_opt_in(gui_app):
    dialog = OpencodeImportDialog("opencode-models.json", [], ["opencode-go/b"])
    dialog.overwrite_box.setChecked(True)
    dialog._accept()
    assert dialog.overwrite_existing() is True


def test_window_import_applies_to_overlay(gui_app, monkeypatch, tmp_path):
    import json as _json

    from PySide6.QtWidgets import QFileDialog  # noqa: E402

    from datasheet_editor.gui.main_window import MainWindow  # noqa: E402

    src = tmp_path / "opencode-models.json"
    src.write_text(_json.dumps(_catalog(CHAT_MODEL)), encoding="utf-8")

    monkeypatch.setattr(
        QFileDialog, "getOpenFileName",
        staticmethod(lambda *a, **k: (str(src), "JSON files (*.json)")),
    )
    monkeypatch.setattr(
        OpencodeImportDialog, "exec", lambda self: QDialog.Accepted,
    )

    window = MainWindow()
    window.overlay = {}
    window._on_import_opencode()
    assert "opencode-zen/big-pickle" in window.overlay
    assert window.overlay["opencode-zen/big-pickle"]["parameters"]["supports_reasoning"] is True
    assert window.dirty is True
    assert "Save Custom" in window.status_label.text()


def test_window_import_cancel_leaves_overlay_untouched(gui_app, monkeypatch, tmp_path):
    import json as _json

    from PySide6.QtWidgets import QFileDialog  # noqa: E402

    from datasheet_editor.gui.main_window import MainWindow  # noqa: E402

    src = tmp_path / "opencode-models.json"
    src.write_text(_json.dumps(_catalog(CHAT_MODEL)), encoding="utf-8")

    monkeypatch.setattr(
        QFileDialog, "getOpenFileName",
        staticmethod(lambda *a, **k: (str(src), "JSON files (*.json)")),
    )
    monkeypatch.setattr(OpencodeImportDialog, "exec", lambda self: QDialog.Rejected)

    window = MainWindow()
    before = {"existing": {"parameters": {"mode": "chat"}}}
    window.overlay = dict(before)
    window._on_import_opencode()
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
    window._on_import_opencode()
    assert seen.get("shown") is True
    assert window.overlay == {}
