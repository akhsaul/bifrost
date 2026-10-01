"""Field classification for the Bifrost datasheets.

Two questions come up constantly when editing these files:

1. **Does Bifrost read this field?** ``model_pricing.json`` is deserialized into
   the Go ``datasheet.Entry`` struct, which embeds ``Options``. Anything that
   struct does not declare is silently dropped on load. The authoritative answer
   is generated from the Go source into ``pricing_fields.json`` (see
   ``tools/gen_pricing_fields.py``).
2. **Is this a cost field or a capability field?** Used to keep cost edits in the
   ``pricing`` section of the custom overlay and capability edits in
   ``parameters``.

Note the read-set is not the same as "cost fields": it also carries identity
(``provider``, ``mode``, ``base_model``) and capability (``max_input_tokens``,
``is_deprecated``, ...) fields, because one struct serves both the cost lookup
(``GetPricingEntryForModel``) and the capability lookup (``GetCapabilityEntry``).
"""

from __future__ import annotations

import json
import re
from functools import lru_cache
from pathlib import Path
from typing import Any, Literal

FieldKind = Literal["bool", "int", "float", "string", "object", "array", "null", "unknown"]

#: Fields in the read-set that describe *identity or capability* rather than cost.
#: Everything else the read-set knows about is treated as a cost/pricing field.
#: ``inference_geo_us_multiplier`` and ``peak_hours`` are deliberately absent: they
#: look capability-ish by name but are billing constructs, so they stay costs.
CAPABILITY_IDENTITY_FIELDS: frozenset[str] = frozenset(
    {
        # identity
        "provider",
        "mode",
        "base_model",
        # capability
        "context_length",
        "max_input_tokens",
        "max_output_tokens",
        "is_deprecated",
        "architecture",
    }
)

_SNAPSHOT = Path(__file__).with_name("pricing_fields.json")


class FieldSetUnavailable(RuntimeError):
    """Raised when the generated pricing read-set snapshot cannot be loaded."""


@lru_cache(maxsize=1)
def _load_snapshot() -> dict[str, Any]:
    try:
        with _SNAPSHOT.open(encoding="utf-8") as fh:
            return json.load(fh)
    except FileNotFoundError as exc:  # pragma: no cover - packaging error
        raise FieldSetUnavailable(
            f"pricing read-set snapshot missing at {_SNAPSHOT}; "
            "regenerate with tools/gen_pricing_fields.py"
        ) from exc
    except json.JSONDecodeError as exc:  # pragma: no cover - packaging error
        raise FieldSetUnavailable(f"pricing read-set snapshot is not valid JSON: {exc}") from exc


@lru_cache(maxsize=1)
def pricing_read_set() -> frozenset[str]:
    """JSON field names Bifrost's ``datasheet.Entry`` struct will read."""
    return frozenset(_load_snapshot().get("fields", {}))


@lru_cache(maxsize=1)
def _field_meta() -> dict[str, dict[str, str]]:
    fields = _load_snapshot().get("fields", {})
    return {name: dict(meta) for name, meta in fields.items()}


def is_pricing_read(field: str) -> bool:
    """True when Bifrost reads ``field`` off a pricing entry."""
    return field in pricing_read_set()


def field_kind(field: str) -> FieldKind:
    """Declared Go kind for a read-set field, or ``"unknown"`` if not in the set.

    Only meaningful for pricing fields; params fields are heterogeneous and get
    their kind inferred from the loaded value instead (see :func:`value_kind`).
    """
    meta = _field_meta().get(field)
    if meta is None:
        return "unknown"
    kind = meta.get("kind", "unknown")
    return kind if kind in {"bool", "int", "float", "string"} else "unknown"


def is_capability_field(field: str) -> bool:
    """True for identity/capability fields carried by the pricing entry."""
    return field in CAPABILITY_IDENTITY_FIELDS


def is_cost_field(field: str) -> bool:
    """True for read-set fields that represent a billing/cost construct.

    A cost field that appears in a ``parameters`` section of the custom overlay
    is a mistake -- it is routed to the wrong output file and silently ignored
    downstream, so the merge treats it as a hard error.
    """
    return is_pricing_read(field) and not is_capability_field(field)


def classify(field: str) -> str:
    """Return a short label for a field: ``cost``, ``capability``, or ``unknown``."""
    if is_capability_field(field):
        return "capability"
    if is_pricing_read(field):
        return "cost"
    return "unknown"


def value_kind(value: Any) -> FieldKind:
    """Infer an editor-friendly kind from a loaded JSON value.

    Used for params fields (which have no generated schema) and for pricing
    fields not present in the read-set, so the GUI always has something sensible
    to render.
    """
    if value is None:
        return "null"
    if isinstance(value, bool):
        return "bool"
    if isinstance(value, int):
        return "int"
    if isinstance(value, float):
        return "float"
    if isinstance(value, str):
        return "string"
    if isinstance(value, list):
        return "array"
    if isinstance(value, dict):
        return "object"
    return "unknown"  # pragma: no cover - json.loads yields nothing else


#: ``mode`` values observed across the datasheets. Used to offer a dropdown in the
#: GUI instead of a free-text box, and to warn on unknown modes during validation.
KNOWN_MODES: frozenset[str] = frozenset(
    {
        "chat",
        "responses",
        "completion",
        "embedding",
        "rerank",
        "image_generation",
        "image_edit",
        "image_variation",
        "video_generation",
        "audio_speech",
        "audio_transcription",
        "ocr",
        "moderation",
        "search",
        "realtime",
        "decisions",
        "vector_store",
        "guardrail",
        "3d",
    }
)

#: ``model_parameters[].type`` values seen in the params datasheet.
KNOWN_PARAM_TYPES: frozenset[str] = frozenset(
    {
        "number",
        "text",
        "select",
        "boolean",
        "array",
        "object",
        "slider",
        "code",
    }
)


def looks_like_cost(field: str) -> bool:
    """Heuristic: does this field name *look* like a cost, for warning purposes?

    Broader than :func:`is_cost_field`, which only knows the current read-set.
    This catches cost fields Bifrost does not read yet, so a user who puts one in
    the ``parameters`` section gets told rather than losing the edit.
    """
    if is_capability_field(field):
        return False
    return bool(re.search(r"cost|price", field))
