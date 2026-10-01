"""pytest suite for the merge engine.

The engine is pure, so these run without Qt. Fixtures slice the real 20MB
``model_parameters.json`` so merge is proven at realistic scale.
"""

from __future__ import annotations

import json
from pathlib import Path

import pytest

from datasheet_editor.dataset import DatasetKind, load_dataset, save_dataset
from datasheet_editor.fields import (
    is_capability_field,
    is_cost_field,
    is_pricing_read,
    looks_like_cost,
)
from datasheet_editor.merge import (
    MergeError,
    ParamArrayMode,
    PricingFieldPolicy,
    merge,
)
from datasheet_editor.validate import (
    find_cross_file_conflicts,
    validate_entry,
    validate_overlay,
)

REPO = Path(__file__).resolve().parents[2]
PARAMS_PATH = REPO / "editor" / "model_parameters.json"
PRICING_PATH = REPO / "editor" / "model_pricing.json"


# --------------------------------------------------------------------------- #
# field classification
# --------------------------------------------------------------------------- #


def test_generated_read_set_is_loaded():
    from datasheet_editor.fields import pricing_read_set

    read_set = pricing_read_set()
    assert len(read_set) > 100
    assert "input_cost_per_token" in read_set
    assert "supports_computer_use" not in read_set


def test_capability_fields_are_not_costs():
    for name in ("provider", "mode", "base_model", "max_input_tokens", "is_deprecated"):
        assert is_capability_field(name)
        assert not is_cost_field(name)


def test_multiplier_is_treated_as_cost_not_capability():
    # Named like capability, but it is a billing construct.
    assert not is_capability_field("inference_geo_us_multiplier")
    assert is_cost_field("inference_geo_us_multiplier")


def test_looks_like_cost_catches_unread_cost_fields():
    # Not in the Go read-set yet, but a cost by name.
    assert not is_pricing_read("cache_read_input_token_cost_above_32k_tokens")
    assert looks_like_cost("cache_read_input_token_cost_above_32k_tokens")


# --------------------------------------------------------------------------- #
# core merge rules
# --------------------------------------------------------------------------- #


def test_adds_new_model_whole():
    original = {"a": {"provider": "openai"}}
    overlay = {"new": {"parameters": {"provider": "x", "max_input_tokens": 10}}}
    res = merge(original, {}, overlay)
    assert res.parameters["new"] == {"provider": "x", "max_input_tokens": 10}
    assert "new" in res.new_models
    assert any(c.kind == "model_added" for c in res.changes)


def test_override_preserves_unmentioned_fields():
    original = {"a": {"provider": "openai", "max_input_tokens": 100}}
    overlay = {"a": {"parameters": {"max_input_tokens": 200}}}
    res = merge(original, {}, overlay)
    assert res.parameters["a"] == {"provider": "openai", "max_input_tokens": 200}


def test_nothing_is_deleted_by_omission():
    original = {"a": {"x": 1, "y": 2}}
    res = merge(original, {}, {"a": {"parameters": {"x": 9}}})
    assert res.parameters["a"]["y"] == 2


def test_null_is_a_value_not_a_delete():
    original = {"a": {"rpm": 100}}
    res = merge(original, {}, {"a": {"parameters": {"rpm": None}}})
    assert res.parameters["a"]["rpm"] is None


def test_nested_objects_deep_merge():
    original = {"a": {"tiered_pricing": {"low": {"input": 1}, "high": {"input": 2}}}}
    overlay = {"a": {"pricing": {"tiered_pricing": {"low": {"input": 5}}}}}
    res = merge({}, original, overlay)
    got = res.pricing["a"]["tiered_pricing"]
    assert got == {"low": {"input": 5}, "high": {"input": 2}}


def test_lists_replace_wholesale():
    original = {"a": {"supported_regions": ["us-east-1", "us-west-2"]}}
    overlay = {"a": {"parameters": {"supported_regions": ["eu-west-1"]}}}
    res = merge(original, {}, overlay)
    assert res.parameters["a"]["supported_regions"] == ["eu-west-1"]


def test_key_order_of_original_is_preserved():
    original = {"a": {"z": 1, "m": 2, "b": 3}}
    res = merge(original, {}, {"a": {"parameters": {"z": 9}}})
    assert list(res.parameters["a"]) == ["z", "m", "b"]


def test_inputs_are_not_mutated():
    original = {"a": {"range": {"min": 0}}}
    snapshot = json.dumps(original, sort_keys=True)
    merge(original, {}, {"a": {"parameters": {"range": {"min": 5}}}})
    assert json.dumps(original, sort_keys=True) == snapshot


# --------------------------------------------------------------------------- #
# model_parameters array merging
# --------------------------------------------------------------------------- #


def test_param_array_merges_by_id_preserving_order():
    original = {
        "a": {
            "model_parameters": [
                {"id": "temperature", "default": 1},
                {"id": "max_tokens", "default": 2048},
            ]
        }
    }
    overlay = {"a": {"parameters": {"model_parameters": [{"id": "temperature", "default": 0.5}]}}}
    res = merge(original, {}, overlay)
    got = res.parameters["a"]["model_parameters"]
    assert [p["id"] for p in got] == ["temperature", "max_tokens"]
    assert got[0]["default"] == 0.5
    assert got[1]["default"] == 2048


def test_param_array_appends_new_ids_at_end():
    original = {"a": {"model_parameters": [{"id": "temperature", "default": 1}]}}
    overlay = {"a": {"parameters": {"model_parameters": [{"id": "brandNew", "type": "number"}]}}}
    res = merge(original, {}, overlay)
    got = res.parameters["a"]["model_parameters"]
    assert [p["id"] for p in got] == ["temperature", "brandNew"]


def test_param_array_replace_mode():
    original = {"a": {"model_parameters": [{"id": "temperature", "default": 1}]}}
    overlay = {"a": {"parameters": {"model_parameters": [{"id": "only", "type": "text"}]}}}
    res = merge(original, {}, overlay, param_array_mode=ParamArrayMode.REPLACE)
    assert res.parameters["a"]["model_parameters"] == [{"id": "only", "type": "text"}]


def test_param_array_override_is_reported_as_a_change():
    """Regression: the by-id merge used to mutate the original dicts in place.

    The output value was correct, but the change list compared the merged array
    against the same (already-mutated) dicts and so reported no change at all --
    making a real override invisible in the merge preview.
    """
    original = {"a": {"model_parameters": [{"id": "temperature", "default": 1, "label": "Temp"}]}}
    overlay = {"a": {"parameters": {"model_parameters": [{"id": "temperature", "default": 0.3}]}}}
    res = merge(original, {}, overlay)

    assert res.parameters["a"]["model_parameters"][0]["default"] == 0.3
    changes = [c for c in res.changes if c.path.startswith("model_parameters")]
    assert len(changes) == 1
    assert changes[0].path == "model_parameters[id=temperature].default"
    assert changes[0].kind == "overridden"
    assert changes[0].before == 1
    assert changes[0].after == 0.3


def test_param_array_new_id_is_reported_as_a_change():
    original = {"a": {"model_parameters": [{"id": "temperature", "default": 1}]}}
    overlay = {"a": {"parameters": {"model_parameters": [{"id": "brandNew", "type": "number"}]}}}
    res = merge(original, {}, overlay)
    assert [c.path for c in res.changes] == ["model_parameters[id=brandNew].*"]
    assert res.changes[0].kind == "added"


def test_param_array_merge_does_not_mutate_the_original():
    original = {"a": {"model_parameters": [{"id": "temperature", "default": 1}]}}
    snapshot = json.dumps(original, sort_keys=True)
    merge(original, {}, {"a": {"parameters": {"model_parameters": [{"id": "temperature", "default": 0.3}]}}})
    assert json.dumps(original, sort_keys=True) == snapshot


def test_param_array_noop_reports_nothing():
    original = {"a": {"model_parameters": [{"id": "temperature", "default": 1}]}}
    overlay = {"a": {"parameters": {"model_parameters": [{"id": "temperature", "default": 1}]}}}
    res = merge(original, {}, overlay)
    assert [c.path for c in res.changes] == []


# --------------------------------------------------------------------------- #
# hard validation errors
# --------------------------------------------------------------------------- #


def test_cost_field_in_parameters_is_rejected():
    with pytest.raises(MergeError, match="cost field"):
        merge({}, {}, {"a": {"parameters": {"input_cost_per_token": 1.0}}})


def test_unread_cost_field_in_parameters_is_rejected():
    with pytest.raises(MergeError, match="cost field"):
        merge({}, {}, {"a": {"parameters": {"citation_cost_per_token": 1.0}}})


def test_model_parameters_in_pricing_is_rejected():
    with pytest.raises(MergeError, match="parameters"):
        merge({}, {}, {"a": {"pricing": {"model_parameters": []}}})


def test_unknown_section_is_rejected():
    with pytest.raises(MergeError, match="unknown section"):
        merge({}, {}, {"a": {"cost": {"input_cost_per_token": 1}}})


# --------------------------------------------------------------------------- #
# pricing field policy
# --------------------------------------------------------------------------- #


def test_pricing_preserve_keeps_unread_fields():
    original = {"a": {"input_cost_per_token": 1.0, "supports_computer_use": True}}
    res = merge({}, original, {}, pricing_fields=PricingFieldPolicy.PRESERVE)
    assert res.pricing["a"]["supports_computer_use"] is True
    assert not res.dropped_pricing_fields


def test_pricing_strict_drops_unread_fields_and_reports_them():
    original = {
        "a": {"input_cost_per_token": 1.0, "supports_computer_use": True},
        "b": {"supports_computer_use": False, "provider": "openai"},
    }
    res = merge({}, original, {}, pricing_fields=PricingFieldPolicy.STRICT)
    assert "supports_computer_use" not in res.pricing["a"]
    assert res.pricing["b"] == {"provider": "openai"}
    assert res.dropped_pricing_fields == {"supports_computer_use": 2}


def test_pricing_strict_keeps_read_fields():
    original = {"a": {"input_cost_per_token": 1.0, "max_input_tokens": 5}}
    res = merge({}, original, {}, pricing_fields=PricingFieldPolicy.STRICT)
    assert res.pricing["a"] == {"input_cost_per_token": 1.0, "max_input_tokens": 5}


# --------------------------------------------------------------------------- #
# round-trip against the real files
# --------------------------------------------------------------------------- #

requires_files = pytest.mark.skipif(
    not (PARAMS_PATH.exists() and PRICING_PATH.exists()),
    reason="real datasheet files not present",
)


@pytest.fixture(scope="module")
def real_datasets():
    return (
        load_dataset(PARAMS_PATH, DatasetKind.PARAMETERS),
        load_dataset(PRICING_PATH, DatasetKind.PRICING),
    )


@requires_files
def test_real_params_has_no_cost_fields(real_datasets):
    params, _ = real_datasets
    offenders = [n for e in params.data.values() for n in e if looks_like_cost(n) and is_cost_field(n)]
    assert offenders == []


@requires_files
def test_real_pricing_read_set_coverage(real_datasets):
    _, pricing = real_datasets
    unread = {n for e in pricing.data.values() for n in e if not is_pricing_read(n)}
    # Bifrost's Entry struct reads a strict subset; the merge must preserve the rest.
    assert 0 < len(unread) < 400


@requires_files
def test_merge_of_real_files_is_lossless_for_untouched_models(real_datasets):
    params, pricing = real_datasets
    sample_key = next(iter(params.data))
    overlay = {sample_key: {"parameters": {"max_input_tokens": 999999}}}
    res = merge(params.data, pricing.data, overlay)
    assert res.parameters[sample_key]["max_input_tokens"] == 999999
    assert len(res.parameters) == len(params.data)
    assert len(res.pricing) == len(pricing.data)
    # every other model is byte-identical
    others = [k for k in params.data if k != sample_key][:500]
    for k in others:
        assert res.parameters[k] == params.data[k]


@requires_files
def test_cross_file_conflicts_are_found(real_datasets):
    params, pricing = real_datasets
    conflicts = find_cross_file_conflicts(params.data, pricing.data)
    assert len(conflicts) > 0
    assert all({"model", "field", "parameters", "pricing"} == set(c) for c in conflicts)


# --------------------------------------------------------------------------- #
# validation layer
# --------------------------------------------------------------------------- #


def test_negative_cost_is_an_error():
    report = validate_entry("m", {"input_cost_per_token": -1.0}, section="pricing")
    assert any(f.severity == "error" for f in report)


def test_unknown_mode_is_a_warning():
    report = validate_entry("m", {"mode": "telepathy"}, section="pricing")
    assert [f.severity for f in report] == ["warning"]


def test_duplicate_param_ids_rejected():
    report = validate_entry("m", {"model_parameters": [{"id": "a"}, {"id": "a"}]}, section="parameters")
    assert any("duplicate" in f.message for f in report.errors())


def test_overlay_warns_on_new_model():
    report = validate_overlay({"brand-new": {"parameters": {"provider": "x"}}}, {}, {})
    assert any("new model" in f.message for f in report.warnings())


# --------------------------------------------------------------------------- #
# dataset IO
# --------------------------------------------------------------------------- #


def test_load_save_roundtrip_preserves_key_order(tmp_path):
    data = {"z-model": {"b": 1, "a": 2}, "a-model": {"x": 1}}
    path = tmp_path / "model_parameters.json"
    save_dataset(data, path)
    assert list(load_dataset(path).data) == ["z-model", "a-model"]
    assert list(load_dataset(path).data["z-model"]) == ["b", "a"]


def test_load_rejects_non_object_entry(tmp_path):
    path = tmp_path / "model_pricing.json"
    path.write_text('{"a": "not-an-object"}')
    with pytest.raises(Exception):
        load_dataset(path)


def test_compact_output_matches_upstream_format(tmp_path):
    path = tmp_path / "m.json"
    save_dataset({"a": {"b": 1}}, path)
    assert path.read_text() == '{"a":{"b":1}}'


@requires_files
def test_atomic_write_does_not_leave_temp_files(tmp_path):
    from datasheet_editor.dataset import write_json_atomic

    out = write_json_atomic({"a": 1}, tmp_path / "x.json")
    assert out.exists()
    assert [p.name for p in tmp_path.iterdir()] == ["x.json"]
