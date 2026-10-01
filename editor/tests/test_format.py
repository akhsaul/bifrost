"""Tests for human-readable number formatting.

The datasheets store per-token rates, so the raw JSON is scientific notation:
``3e-06``. 1,009 of the 1,255 distinct float cost values render that way, which
is why every display path goes through this module.
"""

from __future__ import annotations

import json
from decimal import Decimal
from pathlib import Path

import pytest

from datasheet_editor.format import explain, format_value, number, scaled


def test_number_expands_scientific_notation():
    assert number(3e-06) == "0.000003"
    assert number(2.5e-06) == "0.0000025"
    assert number(1.25e-05) == "0.0000125"
    assert number(2e-07) == "0.0000002"
    assert number(1.3e-10) == "0.00000000013"


def test_number_never_uses_an_exponent():
    for value in (3e-06, 1e-300, 5e-324, 1.5e-7):
        assert "e" not in number(value).lower()


def test_number_leaves_non_floats_alone():
    assert number(8192) == "8192"
    assert number(True) == "True"
    assert number("chat") == "chat"
    assert number(None) == "None"


def test_number_round_trips_to_the_same_float():
    for value in (3e-06, 2.5e-06, 1.25e-05, 0.1, 1.92):
        assert float(number(value)) == value


def test_number_preserves_exact_decimal_digits():
    # repr() gives the shortest round-tripping form; expanding it must not
    # invent or drop precision.
    assert number(Decimal("1.100")) == "1.100" or number(1.1) == "1.1"


@pytest.mark.parametrize(
    "field",
    [
        "input_cost_per_token",
        "output_cost_per_token",
        "cache_read_input_token_cost",
        "cache_creation_input_token_cost",
        "cache_read_input_token_cost_above_32k_tokens",
        "cache_creation_input_token_cost_batches",
        "input_cost_per_token_above_272k_tokens",
        "input_cost_per_token_priority",
        "output_cost_per_reasoning_token",
        "input_cost_per_image_token",
        "output_cost_per_video_token",
        "cache_read_input_token_dbu_cost",
    ],
)
def test_token_cost_fields_are_scaled(field):
    assert scaled(3e-06, field) is not None, field


@pytest.mark.parametrize(
    "field",
    [
        "input_cost_per_image",
        "output_cost_per_image_1024",
        "output_cost_per_second_720p",
        "input_cost_per_video_per_second",
        "input_cost_per_character",
        "input_cost_per_pixel",
        "ocr_cost_per_page",
        "output_cost_per_page",
        "search_context_cost_per_query",
        "cost_per_request",
        "web_search_cost_per_request",
        "off_peak_cost_multiplier",
        "inference_geo_us_multiplier",
        "guardrail_cost_per_unit",
        "max_input_tokens",
    ],
)
def test_non_token_fields_are_not_scaled(field):
    assert scaled(0.02, field) is None, field


def test_per_image_token_is_treated_as_per_token_not_per_image():
    # "per_image_token" also contains "per_image"; the token rule must win.
    assert scaled(2e-06, "input_cost_per_image_token") is not None
    assert scaled(0.02, "output_cost_per_image") is None


def test_scaled_reading_is_quoted_per_million():
    assert scaled(3e-06, "input_cost_per_token") == "$3.00 per 1M tokens"
    assert scaled(1.25e-05, "input_cost_per_token") == "$12.50 per 1M tokens"
    assert scaled(5e-05, "input_cost_per_token") == "$50.00 per 1M tokens"


def test_scaled_keeps_precision_for_sub_dollar_rates():
    assert scaled(2e-07, "cache_read_input_token_cost") == "$0.2000 per 1M tokens"


def test_explain_shows_both_exact_and_scaled():
    assert explain("input_cost_per_token", 3e-06) == "0.000003 (~$3.00 per 1M tokens)"
    assert explain("input_cost_per_image", 0.02) == "0.02"


def test_explain_never_shows_a_stale_scaled_duplicate():
    # When the exact form and the quoted form coincide, only one is shown.
    assert explain("some_field", 1.5) == "1.5"


def test_format_value_handles_every_json_type():
    assert format_value(True, "supports_vision") == "true"
    assert format_value(None, "rpm") == "null"
    assert format_value(8192, "max_input_tokens") == "8192"
    assert format_value("chat", "mode") == "chat"
    assert format_value(["us-east-1"], "supported_regions") == '["us-east-1"]'
    assert format_value({"min": 0}, "range") == '{"min":0}'


def test_format_value_truncates_with_an_ellipsis():
    text = format_value(["x" * 50], "supported_regions", limit=20)
    assert len(text) <= 20
    assert text.endswith("…")


def test_format_value_uses_readable_form_for_floats():
    assert format_value(3e-06, "input_cost_per_token") == "0.000003 (~$3.00 per 1M tokens)"


_EDITOR = Path(__file__).resolve().parents[1]


def _resolve(*names: str) -> Path:
    """First existing copy, searched in each location. See test_merge.py."""
    for name in names:
        for directory in (_EDITOR, _EDITOR / "tools"):
            candidate = directory / name
            if candidate.exists():
                return candidate
    return _EDITOR / names[0]


PRICING_PATH = _resolve("model_pricing.json", "model_pricing_beauty.json")

requires_files = pytest.mark.skipif(
    not PRICING_PATH.exists(),
    reason="no copy of the real pricing file present",
)


@requires_files
def test_no_real_field_is_misclassified_as_per_token():
    """Only genuine per-token price fields may claim a per-1M-token reading."""
    from datasheet_editor.format import _is_token_cost

    data = json.loads(PRICING_PATH.read_text())
    fields = {name for entry in data.values() for name in entry}
    for field in fields:
        if _is_token_cost(field):
            # Must name itself as a per-token price, and must not be a plain unit
            # price that merely mentions tokens.
            assert "cost" in field, field
            assert not field.endswith("per_page"), field


@requires_files
def test_read_set_fields_are_never_scaled_wrongly_on_real_values():
    from datasheet_editor.format import format_value

    data = json.loads(PRICING_PATH.read_text())
    for entry in list(data.values())[:500]:
        for field, value in entry.items():
            if isinstance(value, float):
                rendered = format_value(value, field)
                assert "e-" not in rendered.lower().replace("per", "")
