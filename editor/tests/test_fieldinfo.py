"""Tests for the field catalog that backs the Add Field dialog's descriptions.

The catalog answers the three questions the user cannot answer by looking at the
JSON: what shape does this field's value take, what does it mean, and does
Bifrost read it at all. Each answer comes from a source that can silently rot --
the generated Go read-set, or the datasheets themselves -- so the tests pin both
the classification rules and the generated snapshot.
"""

from __future__ import annotations

import json
import subprocess
import sys
from pathlib import Path

import pytest

from datasheet_editor.fieldinfo import (
    MAX_ENUM_VALUES,
    ParameterCatalog,
    build_catalog,
    params_read_set,
)

REPO_ROOT = Path(__file__).resolve().parents[2]
TOOLS = Path(__file__).resolve().parents[1] / "tools"


def _dataset(**fields):
    return {"m": dict(fields)}


# --------------------------------------------------------------------------- #
# the generated read-set
# --------------------------------------------------------------------------- #


def test_snapshot_matches_the_go_source():
    """The checked-in snapshot must be what the generator produces today."""
    proc = subprocess.run(
        [sys.executable, str(TOOLS / "gen_param_fields.py"), "--check"],
        cwd=str(REPO_ROOT),
        capture_output=True,
        text=True,
    )
    assert proc.returncode == 0, proc.stderr


def test_read_set_comes_from_model_capabilities():
    read = params_read_set()
    # A capability flag, a list, and a map -- one of each declared shape.
    assert "supports_vision" not in read  # declared by no Go struct
    assert "reasoning_effort_levels" in read
    assert "unsupported_fields" in read
    assert "model_parameters" in read


def test_read_set_is_generated_not_hardcoded():
    """Regenerating from a doctored source must change the snapshot."""
    from datasheet_editor.fieldinfo import _snapshot

    snapshot = TOOLS.parent / "datasheet_editor" / "param_fields.json"
    original = snapshot.read_text(encoding="utf-8")
    payload = json.loads(original)
    try:
        payload["fields"]["an_invented_field"] = {"kind": "bool", "doc": "nope"}
        snapshot.write_text(json.dumps(payload, indent=2) + "\n", encoding="utf-8")
        # Both caches: params_read_set memoises over _snapshot, so clearing only
        # the outer one would re-run against the stale inner value.
        params_read_set.cache_clear()
        _snapshot.cache_clear()
        assert "an_invented_field" in params_read_set()
    finally:
        snapshot.write_text(original, encoding="utf-8")
        params_read_set.cache_clear()
        _snapshot.cache_clear()
    assert "an_invented_field" not in params_read_set()


# --------------------------------------------------------------------------- #
# kind resolution
# --------------------------------------------------------------------------- #


def test_kind_comes_from_the_go_type_when_declared():
    cat = build_catalog(_dataset(reasoning_effort_levels=["low"]))
    info = cat.field("reasoning_effort_levels")
    assert info.kind == "array"
    assert info.kind_origin == "go"


def test_kind_is_inferred_when_go_does_not_declare_the_field():
    """``supports_vision`` is not on ModelCapabilities, so its shape comes from data."""
    cat = build_catalog(_dataset(supports_vision=True))
    info = cat.field("supports_vision")
    assert info.kind == "bool"
    assert info.kind_origin == "observed"


def test_int_and_float_both_resolve_to_float():
    """JSON has one number type; int-versus-float is Go's distinction, not the file's."""
    cat = build_catalog({"a": {"n": 1}, "b": {"n": 1.5}})
    assert cat.field("n").kind == "float"


def test_a_genuinely_mixed_field_is_unknown():
    """A widget for either shape would silently rewrite the other."""
    cat = build_catalog({"a": {"mixed": 1}, "b": {"mixed": "text"}})
    assert cat.field("mixed").kind == "unknown"


def test_null_only_field_is_unknown():
    cat = build_catalog(_dataset(only_null=None))
    assert cat.field("only_null").kind == "unknown"


# --------------------------------------------------------------------------- #
# which file Bifrost reads the field from
# --------------------------------------------------------------------------- #


def test_capability_flag_is_read_from_the_parameters_file():
    cat = build_catalog(_dataset(reasoning_effort_levels=["low"]))
    assert cat.field("reasoning_effort_levels").read_from == "parameters"
    assert cat.field("reasoning_effort_levels").read is True


def test_field_go_reads_from_pricing_is_flagged_as_such():
    """``max_input_tokens`` is on datasheet.Entry, not ModelCapabilities.

    Editing it in the parameters section is accepted by the merge and then has no
    effect, which is exactly the confusion this classification exists to prevent.
    """
    cat = build_catalog(_dataset(max_input_tokens=1000), {"m": {"max_input_tokens": 1000}})
    info = cat.field("max_input_tokens")
    assert info.read_from == "pricing"
    assert "model_pricing.json" in info.read_sentence()


def test_field_no_go_struct_declares_is_flagged_as_unread():
    cat = build_catalog(_dataset(supports_vision=True))
    info = cat.field("supports_vision")
    assert info.read_from == "none"
    assert "Not read" in info.read_sentence()


def test_pricing_source_never_reports_the_parameters_file():
    cat = build_catalog({"m": {"mode": "chat"}}, {"m": {"mode": "chat"}}, source="pricing")
    assert cat.field("mode").read_from == "pricing"


# --------------------------------------------------------------------------- #
# enum detection
# --------------------------------------------------------------------------- #


def test_closed_string_vocabulary_becomes_an_enum():
    cat = build_catalog(_dataset(service_tiers=["priority"]))
    cat.fields["service_tiers"].__class__  # dataclass is frozen; build with real data instead
    real = build_catalog(
        {"m": {"service_tiers": ["priority", "flex"]}},
        {"m": {"service_tiers": ["priority"]}},
    )
    info = real.field("service_tiers")
    assert info.kind == "array"
    assert info.enum_elements == ("flex", "priority")


def test_open_vocabulary_does_not_become_an_enum():
    """A dropdown of thousands of entries is worse than the text box it replaces."""
    many = {f"m{i}": {"base_model": f"model-{i}"} for i in range(MAX_ENUM_VALUES + 5)}
    cat = build_catalog(many)
    assert cat.field("base_model").enum_values == ()


def test_unread_field_does_not_get_an_enum_even_when_small():
    """Without a Go consumer there is no basis for claiming a closed vocabulary."""
    cat = build_catalog(_dataset(shop_size=["small", "large", "medium"]))
    info = cat.field("shop_size")
    assert info.read_from == "none"
    assert info.enum_values == ()


def test_enum_is_reported_in_the_summary():
    cat = build_catalog(_dataset(service_tiers=["priority"]), {"m": {"service_tiers": ["priority"]}})
    assert "tick the values that apply" in cat.field("service_tiers").summary()


# --------------------------------------------------------------------------- #
# observed statistics
# --------------------------------------------------------------------------- #


def test_presence_and_total_are_reported():
    cat = build_catalog({"a": {"mode": "chat"}, "b": {"mode": "chat"}, "c": {}})
    info = cat.field("mode")
    assert info.present_in == 2
    assert info.total == 3
    assert "2 of 3" in info.summary()


def test_numeric_bounds_span_every_model():
    cat = build_catalog({"a": {"max_tokens": 10}, "b": {"max_tokens": 999}, "c": {"max_tokens": 50}})
    info = cat.field("max_tokens")
    assert (info.minimum, info.maximum) == (10.0, 999.0)
    assert "10" in info.summary()
    assert "999" in info.summary()


def test_bounds_avoid_exponent_notation():
    """1e+07 is unreadable next to the other numbers in the dialog."""
    cat = build_catalog({"a": {"max_tokens": 10_000_000}, "b": {"max_tokens": 0}})
    assert "10,000,000" in cat.field("max_tokens").summary()
    assert "e+" not in cat.field("max_tokens").summary()


def test_bool_distribution_is_described():
    cat = build_catalog({"a": {"supports_vision": True}, "b": {"supports_vision": True},
                         "c": {"supports_vision": False}})
    assert "true on 2" in cat.field("supports_vision").description
    assert "false on 1" in cat.field("supports_vision").description


def test_model_parameters_describes_itself():
    """The array is read by Go, but only its ids -- which is the surprising part."""
    cat = build_catalog(_dataset(model_parameters=[]))
    description = cat.field("model_parameters").description
    assert "Only the IDs feed the compat allowlist" in description


# --------------------------------------------------------------------------- #
# descriptors
# --------------------------------------------------------------------------- #


def _descriptor(**overrides):
    base = {"id": "p", "type": "boolean", "label": "P"}
    base.update(overrides)
    return base


def test_descriptor_merges_across_models():
    data = {
        "a": {"model_parameters": [_descriptor(helpText="Whether P.")]},
        "b": {"model_parameters": [_descriptor(helpText="Whether P.")]},
    }
    info = build_catalog(data).descriptor("p")
    assert info.kind == "boolean"
    assert info.description == "Whether P."
    assert info.models == 2
    assert info.conflicts == ()


def test_descriptor_option_values_are_unioned():
    data = {
        "a": {"model_parameters": [_descriptor(id="reasoning_effort", type="select",
                                                options=[{"value": "low"}])]},
        "b": {"model_parameters": [_descriptor(id="reasoning_effort", type="select",
                                                options=[{"value": "high"}])]},
    }
    info = build_catalog(data).descriptor("reasoning_effort")
    assert info.enum_values == ("high", "low")


def test_descriptor_range_is_unioned_not_averaged():
    """The widest range any model declares is the useful bound to show."""
    data = {
        "a": {"model_parameters": [_descriptor(id="temperature", type="number",
                                                range={"min": 0, "max": 1})]},
        "b": {"model_parameters": [_descriptor(id="temperature", type="number",
                                                range={"min": 0, "max": 2})]},
    }
    info = build_catalog(data).descriptor("temperature")
    assert (info.minimum, info.maximum) == (0.0, 2.0)


def test_descriptor_type_disagreement_is_reported():
    """A clear majority is fine; a tie is not, so it is surfaced."""
    data = {
        "a": {"model_parameters": [_descriptor(type="boolean")]},
        "b": {"model_parameters": [_descriptor(type="number")]},
    }
    assert "disagree on its type" in " ".join(build_catalog(data).descriptor("p").conflicts)


def test_descriptor_type_with_a_clear_majority_is_not_flagged():
    data = {
        "a": {"model_parameters": [_descriptor(type="boolean")]},
        "b": {"model_parameters": [_descriptor(type="number")]},
        "c": {"model_parameters": [_descriptor(type="number")]},
    }
    info = build_catalog(data).descriptor("p")
    assert info.kind == "number"
    assert info.conflicts == ()


def test_select_without_options_is_flagged():
    """A select with no options cannot be rendered as a dropdown."""
    data = {"a": {"model_parameters": [_descriptor(type="select")]}}
    info = build_catalog(data).descriptor("p")
    assert "no options" in " ".join(info.conflicts)
    assert info.enum_values == ()


def test_descriptor_default_is_dropped_when_models_disagree():
    data = {
        "a": {"model_parameters": [_descriptor(default=True)]},
        "b": {"model_parameters": [_descriptor(default=False)]},
    }
    assert build_catalog(data).descriptor("p").default is None


def test_descriptor_default_is_kept_when_unanimous():
    data = {
        "a": {"model_parameters": [_descriptor(default=True)]},
        "b": {"model_parameters": [_descriptor(default=True)]},
    }
    assert build_catalog(data).descriptor("p").default is True


def test_descriptor_element_bounds_are_separate_from_value_range():
    """For ``stop`` the two are "1 to 4 entries", not "values between 1 and 4"."""
    data = {"a": {"model_parameters": [_descriptor(id="stop", type="array",
                                                    array={"type": "text", "minElements": 1,
                                                           "maxElements": 4})]}}
    info = build_catalog(data).descriptor("stop")
    assert (info.minimum, info.maximum) == (None, None)
    assert (info.min_elements, info.max_elements) == (1, 4)
    assert info.element_kind == "string"


def test_descriptors_are_not_built_for_the_pricing_source():
    """model_parameters is not part of the pricing entry at all."""
    catalog = build_catalog({"m": {"model_parameters": [_descriptor()]}}, source="pricing")
    assert catalog.descriptors == {}


def test_editor_kind_maps_playground_types():
    data = {
        "a": {"model_parameters": [
            _descriptor(id="b", type="boolean"),
            _descriptor(id="n", type="number"),
            _descriptor(id="t", type="text"),
            _descriptor(id="s", type="select", options=[{"value": "x"}]),
            _descriptor(id="a", type="array"),
        ]}
    }
    cat = build_catalog(data)
    assert cat.descriptor("b").editor_kind() == "bool"
    assert cat.descriptor("n").editor_kind() == "float"
    assert cat.descriptor("t").editor_kind() == "string"
    assert cat.descriptor("s").editor_kind() == "string"
    assert cat.descriptor("a").editor_kind() == "array"


def test_malformed_descriptors_are_skipped():
    """A row with a junk array must not take the catalog down with it."""
    data = {
        "a": {"model_parameters": [{"id": "ok"}, "junk", {"no_id": 1}, {}]},
        "b": {"model_parameters": [{"id": "ok", "type": "boolean"}]},
    }
    info = build_catalog(data).descriptor("ok")
    assert info.kind == "boolean"
    assert info.models == 2


# --------------------------------------------------------------------------- #
# catalog plumbing
# --------------------------------------------------------------------------- #


def test_empty_dataset_yields_an_empty_catalog():
    catalog = build_catalog({}, {})
    assert isinstance(catalog, ParameterCatalog)
    assert catalog.fields == {}
    assert catalog.descriptors == {}


def test_non_dict_entries_are_ignored():
    catalog = build_catalog({"m": {"mode": "chat"}, "bad": "not an entry"})
    assert catalog.field("mode") is not None


def test_a_field_named_like_the_param_array_is_still_classified():
    catalog = build_catalog({"m": {"model_parameters": [_descriptor()]}})
    assert catalog.field("model_parameters").read_from == "parameters"


def test_names_are_sorted_for_the_dropdown():
    catalog = build_catalog({"m": {"zeta": 1, "alpha": 1, "mid": 1}})
    assert catalog.field_names() == ["alpha", "mid", "zeta"]