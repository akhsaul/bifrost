"""Import Zed ``/models`` catalog into a custom overlay.

Zed's ``GET /models`` response (``{"models": [...]}``) is the single source of
truth here: every capability value written comes from that file. Nothing is
guessed, and nothing is mixed in from the base providers' own datasheet rows.

Two different "providers" appear in this flow -- do not confuse them:

- the ``provider`` field inside the Zed file (``anthropic`` | ``open_ai`` |
  ``google``) is Zed's *inner* routing discriminator. It is consumed only for
  validation (unknown values are rejected, not guessed) and never written.
- the ``provider`` field in the overlay is the *Bifrost* provider serving the
  model. Bifrost's capability lookup matches rows on it, so every imported
  entry carries ``"provider": "zed"`` -- otherwise a ``zed/gpt-5-nano``
  request would never match its own row.

Mapping (Zed field -> overlay field):

- ``id`` -> overlay key ``zed/<id>``. The prefix is hardcoded so an imported
  model can never collide with a base-provider row.
- ``max_token_count`` / ``max_output_tokens`` -> ``max_input_tokens`` /
  ``max_output_tokens`` (in both sections).
- ``supports_tools`` -> ``supports_function_calling`` + ``supports_tool_choice``.
- ``supports_parallel_tool_calls`` -> ``supports_parallel_function_calling``
  (explicit both ways: several Gemini rows report ``false``).
- ``supports_images`` -> ``supports_vision``.
- ``supports_thinking`` or a non-empty effort list -> ``supports_reasoning``.
- ``supported_effort_levels`` non-empty -> ``supports_reasoning_effort: true``
  plus ``reasoning_effort_levels`` verbatim (Gemini's ``MINIMAL``/``LOW``/...
  uppercase included) and a ``reasoning_effort`` select descriptor in
  ``model_parameters``. Empty -> explicit ``[]`` with
  ``supports_reasoning_effort: false`` and no descriptor, so the prompt
  playground offers no effort control.
- the ``is_default`` entry -> ``default_reasoning_effort`` (+ descriptor
  default). Absent -> no default key at all, never an invented one.
- ``supports_thinking: true`` -> a ``thinking`` select descriptor
  (disabled/enabled). The Zed vocabulary has no adaptive tier, so none is
  emitted.
- ``supports_disabling_thinking`` -> ``supports_reasoning_disable``.
- ``supports_fast_mode`` -> ``supports_speed``.
- ``supports_server_side_compaction`` -> ``supports_compaction``.

Section placement follows the merge engine's own rule
(:func:`merge.check_field_placement`): the capability flags above live in the
``parameters`` section only. The ``pricing`` section carries just ``mode``,
``provider`` and the token limits -- the fields that section accepts -- so the
imported entries merge cleanly. No cost fields: Zed publishes none.

Deliberately omitted (no datasheet counterpart, so they would be dead data):
``display_name``, ``is_latest``, ``max_token_count_in_max_mode``,
``supports_max_mode``, ``is_disabled``, ``supports_streaming_tools``. No
``base_model``: the ``zed/<id>`` key is the identity.

Pure functions, no Qt and no IO -- same rule as :mod:`datasheet_editor.merge`,
so the CLI and the GUI cannot disagree about import semantics.
"""

from __future__ import annotations

from dataclasses import dataclass, field
from typing import Any

#: Overlay key prefix for imported models. Hardcoded so an import can never
#: collide with a base-provider row.
ZED_KEY_PREFIX = "zed/"

#: The Bifrost provider serving every imported model. Constant: the inner
#: family in the Zed file is routing metadata, not the serving provider.
ZED_PROVIDER = "zed"

#: Zed's inner provider vocabulary. Validated only -- an unknown value is an
#: error, not a guess, and the value itself is never written to the overlay.
KNOWN_INNER_PROVIDERS = frozenset({"anthropic", "open_ai", "google"})

#: Zed fields with no datasheet counterpart. Listed so a future reader can
#: tell "dropped on purpose" from "forgotten".
OMITTED_ZED_FIELDS = (
    "display_name",
    "is_latest",
    "max_token_count_in_max_mode",
    "supports_max_mode",
    "is_disabled",
    "supports_streaming_tools",
)


class ZedImportError(ValueError):
    """Raised when the input is not a usable Zed /models payload."""


@dataclass
class ImportResult:
    """What an import did. ``entries`` holds the built overlay sections."""

    entries: dict[str, dict[str, Any]] = field(default_factory=dict)
    added: list[str] = field(default_factory=list)
    skipped: list[str] = field(default_factory=list)
    overwritten: list[str] = field(default_factory=list)


def parse_zed_models(data: Any) -> list[dict[str, Any]]:
    """Validate a decoded ``GET /models`` payload and return its model list.

    Raises :class:`ZedImportError` when the shape is not a Zed catalog, when
    a model misses a required field, or when two models share an id.
    """
    if not isinstance(data, dict) or not isinstance(data.get("models"), list):
        raise ZedImportError(
            "expected a Zed /models object shaped like {\"models\": [...]}, "
            f"got {type(data).__name__}"
        )
    models: list[dict[str, Any]] = data["models"]
    seen: set[str] = set()
    for i, model in enumerate(models):
        where = f"models[{i}]"
        if not isinstance(model, dict):
            raise ZedImportError(f"{where}: expected an object, got {type(model).__name__}")
        for name in ("provider", "id", "max_token_count", "max_output_tokens"):
            if model.get(name) in (None, ""):
                raise ZedImportError(f"{where}: missing required field {name!r}")
        if not isinstance(model["id"], str):
            raise ZedImportError(f"{where}: 'id' must be a string")
        if model["id"] in seen:
            raise ZedImportError(f"{where}: duplicate model id {model['id']!r}")
        seen.add(model["id"])
        if model["provider"] not in KNOWN_INNER_PROVIDERS:
            raise ZedImportError(
                f"{where} ({model['id']!r}): unknown inner provider "
                f"{model['provider']!r} (known: {sorted(KNOWN_INNER_PROVIDERS)})"
            )
    return models


def _effort_descriptor(levels: list[str], default: str | None) -> dict[str, Any]:
    descriptor: dict[str, Any] = {
        "id": "reasoning_effort",
        "label": "Reasoning Effort",
        "helpText": "Reasoning effort levels advertised by Zed /models for this model",
        "type": "select",
        "options": [{"label": value, "value": value} for value in levels],
    }
    if default is not None:
        descriptor["default"] = default
    return descriptor


def _thinking_descriptor() -> dict[str, Any]:
    return {
        "id": "thinking",
        "label": "Thinking",
        "helpText": "Enable the extended thinking parameter",
        "type": "select",
        "options": [
            {"label": "disabled", "value": "disabled"},
            {"label": "enabled", "value": "enabled"},
        ],
        "default": "enabled",
    }


def zed_model_to_entry(model: dict[str, Any]) -> dict[str, Any]:
    """Build the ``{"parameters": ..., "pricing": ...}`` overlay entry."""
    levels = [
        e["value"]
        for e in model.get("supported_effort_levels") or []
        if isinstance(e, dict) and isinstance(e.get("value"), str)
    ]
    default = next(
        (
            e["value"]
            for e in model.get("supported_effort_levels") or []
            if isinstance(e, dict) and e.get("is_default") and isinstance(e.get("value"), str)
        ),
        None,
    )
    thinking = bool(model.get("supports_thinking"))
    has_effort = len(levels) > 0
    tools = bool(model.get("supports_tools"))

    parameters: dict[str, Any] = {
        "mode": "chat",
        "provider": ZED_PROVIDER,
        "max_input_tokens": model["max_token_count"],
        "max_output_tokens": model["max_output_tokens"],
        "supports_function_calling": tools,
        "supports_tool_choice": tools,
        "supports_parallel_function_calling": bool(model.get("supports_parallel_tool_calls")),
        "supports_vision": bool(model.get("supports_images")),
        "supports_reasoning": bool(thinking or has_effort),
        "supports_reasoning_effort": has_effort,
        "reasoning_effort_levels": levels,
        "supports_reasoning_disable": bool(model.get("supports_disabling_thinking")),
        "supports_speed": bool(model.get("supports_fast_mode")),
        "supports_compaction": bool(model.get("supports_server_side_compaction")),
    }
    if default is not None:
        parameters["default_reasoning_effort"] = default

    descriptors = []
    if has_effort:
        descriptors.append(_effort_descriptor(levels, default))
    if thinking:
        descriptors.append(_thinking_descriptor())
    if descriptors:
        parameters["model_parameters"] = descriptors

    # Pricing carries identity + limits only: that is what the merge engine's
    # placement rule accepts in this section. The capability flags live in
    # parameters (see module docstring).
    pricing: dict[str, Any] = {
        "mode": "chat",
        "provider": ZED_PROVIDER,
        "max_input_tokens": model["max_token_count"],
        "max_output_tokens": model["max_output_tokens"],
    }

    return {"parameters": parameters, "pricing": pricing}


def import_zed_models(
    zed_data: Any,
    existing_overlay: dict[str, dict[str, Any]] | None = None,
    *,
    overwrite_existing: bool = False,
) -> ImportResult:
    """Convert a Zed ``/models`` payload into overlay entries.

    Keys are ``zed/<id>``. Entries already present are left untouched unless
    ``overwrite_existing`` is set; either way they are reported, so the caller
    (CLI summary, GUI dialog) can say what happened instead of guessing.
    """
    existing = existing_overlay or {}
    result = ImportResult()
    for model in parse_zed_models(zed_data):
        key = ZED_KEY_PREFIX + model["id"]
        result.entries[key] = zed_model_to_entry(model)
        if key in existing:
            if overwrite_existing:
                result.overwritten.append(key)
            else:
                result.skipped.append(key)
        else:
            result.added.append(key)
    return result


def apply_import(
    overlay: dict[str, dict[str, Any]],
    result: ImportResult,
    *,
    overwrite_existing: bool = False,
) -> dict[str, dict[str, Any]]:
    """Fold an :class:`ImportResult` into an overlay dict, in place.

    Returns the same dict, so GUI code holding a reference sees the update
    without rebinding. Skipped entries are never touched.
    """
    for key in result.added:
        overlay[key] = result.entries[key]
    if overwrite_existing:
        for key in result.overwritten:
            overlay[key] = result.entries[key]
    return overlay
