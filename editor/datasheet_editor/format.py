"""Human-readable number formatting.

Every float cost in these datasheets is per-token, so the JSON form of a typical
price is scientific notation: ``3e-06`` means three millionths of a cent. That is
correct but unreadable, and 1,009 of the 1,255 distinct float cost values in the
pricing file render that way.

Two representations are offered, and both matter:

- **Exact** -- ``0.000003``. What actually goes in the file. Needed for editing,
  so a round-trip never changes a value.
- **Scaled** -- ``$3.00 per 1M tokens``. How pricing is actually reasoned about.
  Bifrost stores per-token rates, but nobody quotes them per token.

Scaled is a *display* aid only; nothing here is ever written to a datasheet.
"""

from __future__ import annotations

import json
import re
from decimal import Decimal
from typing import Any

#: Names that mean "billed per token". Matched with a regex because the family
#: takes arbitrary trailing modifiers:
#:
#:   input_cost_per_token_above_272k_tokens   (suffix rule would miss this)
#:   cache_read_input_token_cost_above_32k_tokens / _batches / _priority
#:   output_cost_per_reasoning_token
#:   input_cost_per_image_token   (per token *of an image*, not per image)
#:
#: This is checked BEFORE the per-unit list, because ``per_image_token`` also
#: contains ``per_image`` and would otherwise be misread as a per-image price.
_TOKEN_COST_RE = re.compile(
    r"cost_per_token"          # input_cost_per_token, ..._above_272k_tokens, _priority
    r"|token_cost"             # cache_read_input_token_cost, ..._batches, _1hr
    r"|dbu_cost"               # cache_read_input_token_dbu_cost (Bedrock DBU)
    r"|per_(?:audio|image|video|reasoning)_token"  # per token of that modality
)

#: Fields expressed per billable unit -- scaling these by 1M would be wrong.
_PER_UNIT_MARKERS = (
    "per_image",
    "per_page",
    "per_pixel",
    "per_credit",
    "per_unit",
    "per_character",
    "per_second",
    "per_audio_second",
    "per_video",
    "per_query",
    "per_request",
    "per_session",
    "multiplier",
)


def number(value: Any) -> str:
    """Render a number with no exponent.

    ``3e-06`` becomes ``0.000003``. Non-numeric input is returned as ``str``.
    """
    if isinstance(value, bool) or not isinstance(value, (int, float, Decimal)):
        return str(value)
    if isinstance(value, int):
        return str(value)
    # Decimal(repr(x)) keeps the shortest round-tripping representation, then
    # plain formatting expands it without inventing precision.
    try:
        return format(Decimal(repr(value)), "f")
    except (ValueError, ArithmeticError):  # pragma: no cover - defensive
        return repr(value)


def _is_token_cost(field: str) -> bool:
    """True only for fields named as per-token prices.

    Deliberately regex-only, with no "assume yes unless per-unit" fallback: such
    a fallback classifies ``max_input_tokens``, ``supported_regions`` and every
    ``supports_*`` flag as per-token priced, since none of them mention a unit.
    """
    return bool(_TOKEN_COST_RE.search(field.lower()))


def scaled(value: Any, field: str = "") -> str | None:
    """Return a quoted price such as ``$3.00 per 1M tokens``, or ``None``.

    ``None`` means no scaling applies: either the field is not a per-token cost,
    or the value is not a float.

    Integers are excluded on purpose. The pricing file uses ``0`` for "free" and
    ``-1`` as an "unknown" sentinel, and quoting those as ``$0.00`` or
    ``-$1.00 per 1M tokens`` would be worse than showing the raw value.
    """
    if isinstance(value, bool) or not isinstance(value, float):
        return None
    if not _is_token_cost(field):
        return None
    per_million = value * 1_000_000
    digits = 2 if per_million >= 1 else 4
    return f"${per_million:,.{digits}f} per 1M tokens"


def explain(field: str, value: Any) -> str:
    """Exact value plus a scaled reading when one is meaningful."""
    exact = number(value)
    quoted = scaled(value, field)
    if quoted is None or quoted == exact:
        return exact
    return f"{exact} (~{quoted})"


def json_dumps(value: Any) -> str:
    """Compact JSON for a single value, with floats expanded out of exponent form."""
    return json.dumps(value, separators=(",", ":"), default=str)


def format_value(value: Any, field: str = "", *, limit: int | None = None) -> str:
    """Format any JSON value for display in a table cell or report line."""
    if isinstance(value, float):
        text = explain(field, value)
    elif isinstance(value, bool) or value is None:
        text = "null" if value is None else json_dumps(value)
    elif isinstance(value, int):
        text = str(value)
    elif isinstance(value, str):
        text = value
    else:
        text = json_dumps(value)
    if limit is not None and len(text) > limit:
        text = text[: limit - 1] + "\u2026"
    return text
