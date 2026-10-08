"""Import Opencode Zen/Go catalog into a custom overlay.

The debug capture ``debug/opencode-zen-free/opencode-zen-models.json`` holds
one ``models`` dict per upstream slice (``{"opencode": {"models": {...},
"npm": ...}, "opencode-go": {...}, ...}``). Only the ``opencode`` and
``opencode-go`` slices are converted; the other ~224 slices are discarded, not
guessed at. Every capability value written comes from that file -- nothing is
invented, and no base-provider datasheet data is mixed in.

Slice to Bifrost provider (the overlay ``provider``, which is what the
capability lookup matches on):

- ``opencode`` (OpenCode Zen, pay-as-you-go) -> ``opencode-zen``,
  keys ``opencode-zen/<id>``.
- ``opencode-go`` (OpenCode Go, subscription) -> ``opencode-go``,
  keys ``opencode-go/<id>``.

The same model id may appear in both slices with different prices (23 do);
the provider prefix keeps those rows apart, so that is kept, not an error.
Free-tier models (``*-free``) are filed under the base provider like every
other row -- Bifrost folds the ``-free`` runtime suffix away on lookup, so a
separate ``opencode-zen-free`` row would never match.

Endpoint (Opencode never translates formats server-side, so Bifrost must pick
the wire per model). Resolved per model from ``provider.npm``, falling back to
the slice-level ``npm`` when the model carries none (55 ``opencode`` models and
22 ``opencode-go`` models have none; both slices default to
``@ai-sdk/openai-compatible``):

- contains ``google`` -> ``/v1/models/<id>`` (per-model Generative Language path)
- contains ``anthropic`` -> ``/v1/messages``
- contains ``openai-compatible`` -> ``/v1/chat/completions``
- contains ``openai`` -> ``/v1/responses``
- anything else (or npm missing on both levels) -> error, never guessed.

The ``openai-compatible`` check runs before ``openai`` because the former
contains the latter as a substring. Unknown ``reasoning_options`` types and
unknown ``cost`` keys are likewise errors; a ``tiers`` entry whose context
size has no datasheet counterpart (100000, 256000, 512000) is dropped with a
warning carried on the result, while ``context_over_200k`` is always ignored
because it mirrors the 200000 tier entry.

Mapping (Opencode field -> overlay field):

- ``id`` -> overlay key ``<provider>/<id>``.
- ``limit.context`` / ``limit.output`` -> ``max_input_tokens`` /
  ``max_output_tokens`` (in both sections). ``limit.input`` has no counterpart
  and is omitted.
- ``provider.npm`` -> ``supported_endpoints`` (parameters only), per above.
- ``tool_call`` -> ``supports_function_calling`` + ``supports_tool_choice``.
- ``modalities.input`` containing ``image`` -> ``supports_vision`` (explicit
  both ways when ``modalities`` is present).
- ``reasoning`` -> ``supports_reasoning`` (explicit; several models report
  ``false``).
- an ``effort`` entry in ``reasoning_options`` -> ``supports_reasoning_effort:
  true`` plus ``reasoning_effort_levels`` verbatim and a ``reasoning_effort``
  select descriptor whose default is the first (= smallest) level. Absent ->
  explicit ``[]`` with ``supports_reasoning_effort: false`` and no descriptor,
  so the prompt playground offers no effort control.
- ``none`` among the effort values -> ``supports_none_reasoning_effort: true``.
- ``toggle`` in ``reasoning_options`` -> ``supports_reasoning_disable: true``
  plus a ``thinking`` select descriptor (disabled/enabled).
- ``budget_tokens`` ``min``/``max`` -> ``reasoning_budget`` ``{"min": ...,
  "max": ...}`` (whichever bounds are present).
- ``temperature: false`` -> ``supports_sampling_params: false``.
- ``structured_output`` -> ``supports_response_schema`` (explicit both ways).
- ``status: "deprecated"`` -> ``is_deprecated: true`` (pricing only, which is
  what ``GetCapabilityEntry`` reads).
- ``cost`` (quoted in $/1M tokens) -> per-token pricing fields (pricing only):
  ``input``/``output`` -> ``input_cost_per_token``/``output_cost_per_token``,
  ``cache_read``/``cache_write`` -> ``cache_read_input_token_cost``/
  ``cache_creation_input_token_cost``, ``input_audio`` ->
  ``input_cost_per_audio_token``, and ``tiers`` entries of size 200000/272000
  -> the matching ``..._above_200k_tokens``/``..._above_272k_tokens`` fields.
  Free-tier zeros are written explicitly (``0`` means free, not unknown).

Section placement follows the merge engine's own rule
(:func:`merge.check_field_placement`).

Deliberately omitted (no datasheet counterpart, so they would be dead data):
``description``, ``family``, ``name``, ``open_weights``, ``knowledge``,
``release_date``, ``last_updated``, ``canonical_model_id``, ``attachment``,
``interleaved``, ``limit.input``, video/audio/pdf modalities beyond ``image``.
No ``base_model``: the ``<provider>/<id>`` key is the identity.

Pure functions, no Qt and no IO -- same rule as :mod:`datasheet_editor.merge`
and :mod:`datasheet_editor.zed_import`, so the CLI and the GUI cannot disagree
about import semantics.
"""

from __future__ import annotations

from dataclasses import dataclass, field
from typing import Any

#: Slices converted, in conversion order, mapped to the Bifrost provider
#: serving them. Fixed order (not file order) so the result is deterministic
#: however the capture lists its 226 top-level keys.
SLICE_PROVIDERS: dict[str, str] = {
    "opencode": "opencode-zen",
    "opencode-go": "opencode-go",
}

#: Upstream endpoint paths, one per model. Opencode serves each model on
#: exactly one wire format; the paths match the ``opencodeZenFree*Path``
#: constants in ``core/providers/opencodezenfree/routing.go``.
OPENCODE_CHAT_PATH = "/v1/chat/completions"
OPENCODE_RESPONSES_PATH = "/v1/responses"
OPENCODE_MESSAGES_PATH = "/v1/messages"
#: Prefix of the per-model Generative Language path (``/v1/models/<id>``).
OPENCODE_GEMINI_PREFIX = "/v1/models/"

#: Opencode ``cost`` keys with a datasheet counterpart. ``tiers`` and
#: ``context_over_200k`` are handled structurally (see below).
_BASE_COST_FIELDS: tuple[tuple[str, str], ...] = (
    ("input", "input_cost_per_token"),
    ("output", "output_cost_per_token"),
    ("cache_read", "cache_read_input_token_cost"),
    ("cache_write", "cache_creation_input_token_cost"),
)

#: Tier context sizes with a datasheet counterpart, each mapped to its
#: (input, output, cache_read, cache_creation) fields. Any other size is
#: dropped with a warning, never rounded to a nearby tier.
_TIER_SIZE_FIELDS: dict[int, tuple[str, str, str, str]] = {
    200000: (
        "input_cost_per_token_above_200k_tokens",
        "output_cost_per_token_above_200k_tokens",
        "cache_read_input_token_cost_above_200k_tokens",
        "cache_creation_input_token_cost_above_200k_tokens",
    ),
    272000: (
        "input_cost_per_token_above_272k_tokens",
        "output_cost_per_token_above_272k_tokens",
        "cache_read_input_token_cost_above_272k_tokens",
        "cache_creation_input_token_cost_above_272k_tokens",
    ),
}

#: Keys a ``tiers[]`` entry may carry besides the nested ``tier`` marker.
_TIER_VALUE_KEYS: tuple[str, ...] = ("input", "output", "cache_read", "cache_write")

#: Top-level ``cost`` keys. ``context_over_200k`` is accepted but ignored: it
#: mirrors the size-200000 ``tiers`` entry, which is what gets mapped.
_KNOWN_COST_KEYS = frozenset(
    {"input", "output", "cache_read", "cache_write", "tiers", "context_over_200k", "input_audio"}
)

#: ``reasoning_options[]`` types with a datasheet counterpart.
_KNOWN_REASONING_OPTION_TYPES = frozenset({"effort", "toggle", "budget_tokens"})

#: Opencode fields with no datasheet counterpart. Listed so a future reader can
#: tell "dropped on purpose" from "forgotten".
OMITTED_OPENCODE_FIELDS = (
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
    "limit.input",
    "modalities.audio",
    "modalities.pdf",
    "modalities.video",
    "cost.context_over_200k",
    "provider.npm",
)


class OpencodeImportError(ValueError):
    """Raised when the input is not a usable Opencode catalog."""


@dataclass
class ImportResult:
    """What an import did. ``entries`` holds the built overlay sections."""

    entries: dict[str, dict[str, Any]] = field(default_factory=dict)
    added: list[str] = field(default_factory=list)
    skipped: list[str] = field(default_factory=list)
    overwritten: list[str] = field(default_factory=list)
    #: Non-fatal data drops (e.g. a tier size with no datasheet counterpart).
    #: The entry is still imported; nothing silently vanishes without a trace.
    warnings: list[str] = field(default_factory=list)


def parse_opencode_models(data: Any) -> list[tuple[str, dict[str, Any], Any]]:
    """Validate an Opencode catalog payload and return its models.

    Returns ``(provider, model, slice_npm)`` triples in conversion order
    (``opencode`` then ``opencode-go``, file order within a slice). Slices
    other than those two are discarded.

    Raises :class:`OpencodeImportError` when the shape is not an Opencode
    catalog, when neither converted slice is present, or when a model misses
    its identity, limits, or repeats an id within its slice. (The same id in
    both slices is expected -- the provider prefix keeps those rows apart.)
    """
    if not isinstance(data, dict):
        raise OpencodeImportError(
            "expected an Opencode catalog object shaped like "
            '{"opencode": {"models": {...}}, "opencode-go": {"models": {...}}}, '
            f"got {type(data).__name__}"
        )
    out: list[tuple[str, dict[str, Any], Any]] = []
    found = False
    for slice_name, provider in SLICE_PROVIDERS.items():
        if slice_name not in data:
            continue
        found = True
        raw_slice = data[slice_name]
        if not isinstance(raw_slice, dict):
            raise OpencodeImportError(
                f"[{slice_name}]: expected an object, got {type(raw_slice).__name__}"
            )
        models = raw_slice.get("models")
        if not isinstance(models, dict):
            raise OpencodeImportError(
                f"[{slice_name}]: missing required field 'models' "
                f"(expected an object keyed by model id)"
            )
        slice_npm = raw_slice.get("npm")
        seen: set[str] = set()
        for key, model in models.items():
            where = f"{slice_name}/{key}"
            if not isinstance(model, dict):
                raise OpencodeImportError(
                    f"{where}: expected an object, got {type(model).__name__}"
                )
            mid = model.get("id")
            if not isinstance(mid, str) or not mid:
                raise OpencodeImportError(f"{where}: missing required field 'id'")
            if mid in seen:
                raise OpencodeImportError(f"{where}: duplicate model id {mid!r}")
            seen.add(mid)
            limit = model.get("limit")
            if not isinstance(limit, dict):
                raise OpencodeImportError(f"{where}: missing required field 'limit'")
            for name in ("context", "output"):
                value = limit.get(name)
                if isinstance(value, bool) or not isinstance(value, int):
                    raise OpencodeImportError(
                        f"{where}: limit {name!r} must be an integer, "
                        f"got {value!r}"
                    )
            out.append((provider, model, slice_npm))
    if not found:
        raise OpencodeImportError(
            "no 'opencode' or 'opencode-go' slice present; "
            f"top-level keys: {sorted(data)[:5]}"
        )
    return out


def _endpoint_for_npm(npm: Any, model_id: str, where: str) -> str:
    """Resolve the single upstream path serving this model.

    ``openai-compatible`` is checked before ``openai`` because the former
    contains the latter as a substring.
    """
    if not isinstance(npm, str) or not npm.strip():
        raise OpencodeImportError(
            f"{where}: no usable npm (model 'provider.npm' and slice-level "
            "'npm' are both missing); refusing to guess the endpoint"
        )
    text = npm.strip()
    low = text.lower()
    if "google" in low:
        return OPENCODE_GEMINI_PREFIX + model_id
    if "anthropic" in low:
        return OPENCODE_MESSAGES_PATH
    if "openai-compatible" in low:
        return OPENCODE_CHAT_PATH
    if "openai" in low:
        return OPENCODE_RESPONSES_PATH
    raise OpencodeImportError(
        f"{where}: unknown npm {npm!r} "
        "(known: anthropic, google, openai, openai-compatible)"
    )


def _per_token(value: Any, where: str, name: str) -> float:
    """Convert an Opencode $/1M-tokens quote to Bifrost's $/token."""
    if isinstance(value, bool) or not isinstance(value, (int, float)):
        raise OpencodeImportError(
            f"{where}: cost {name!r} must be a number, got {value!r}"
        )
    if value < 0:
        raise OpencodeImportError(f"{where}: cost {name!r} must not be negative")
    # round() keeps the JSON readable: 0.8/1e6 is 8.000000000000001e-07
    # unrounded, 8e-07 rounded. 15 decimals resolve $1e-9 per 1M tokens.
    return round(value / 1_000_000, 15)


def _apply_tier(
    costs: dict[str, float],
    tier: Any,
    where: str,
    model_key: str,
    warnings: list[str],
) -> None:
    """Fold one ``tiers[]`` entry into per-token fields, or warn and drop it."""
    if not isinstance(tier, dict):
        raise OpencodeImportError(f"{where}: tiers[] entries must be objects")
    marker = tier.get("tier")
    size = marker.get("size") if isinstance(marker, dict) else None
    fields = _TIER_SIZE_FIELDS.get(size)
    if fields is None:
        warnings.append(
            f"{model_key}: dropped tier with context size {size!r} "
            "(no datasheet counterpart; kept sizes: 200000, 272000)"
        )
        return
    unknown = set(tier) - set(_TIER_VALUE_KEYS) - {"tier"}
    if unknown:
        raise OpencodeImportError(
            f"{where}: unknown tiers[] field(s) {sorted(unknown)}; "
            "refusing to guess"
        )
    for src, dst in zip(_TIER_VALUE_KEYS, fields):
        if src in tier:
            costs[dst] = _per_token(tier[src], where, f"tiers[{size}].{src}")


def _pricing_costs(
    cost: Any, where: str, model_key: str, warnings: list[str]
) -> dict[str, float]:
    """Convert an Opencode ``cost`` object to per-token pricing fields."""
    if cost is None:
        return {}
    if not isinstance(cost, dict):
        raise OpencodeImportError(f"{where}: 'cost' must be an object")
    unknown = set(cost) - _KNOWN_COST_KEYS
    if unknown:
        raise OpencodeImportError(
            f"{where}: unknown cost field(s) {sorted(unknown)}; refusing to guess"
        )
    out: dict[str, float] = {}
    for src, dst in _BASE_COST_FIELDS:
        if src in cost:
            out[dst] = _per_token(cost[src], where, src)
    if "input_audio" in cost:
        out["input_cost_per_audio_token"] = _per_token(
            cost["input_audio"], where, "input_audio"
        )
    for tier in cost.get("tiers") or []:
        _apply_tier(out, tier, where, model_key, warnings)
    # ``context_over_200k`` mirrors the size-200000 tier entry, which is what
    # gets mapped above; it is never read on its own.
    return out


def _reasoning_parts(model: dict[str, Any], where: str) -> tuple[dict[str, Any], list[dict[str, Any]]]:
    """Split ``reasoning``/``reasoning_options`` into params fields + descriptors."""
    raw_options = model.get("reasoning_options")
    if raw_options is None:
        raw_options = []
    if not isinstance(raw_options, list):
        raise OpencodeImportError(
            f"{where}: 'reasoning_options' must be a list"
        )
    options = [o for o in raw_options if isinstance(o, dict)]
    if len(options) != len(raw_options):
        raise OpencodeImportError(
            f"{where}: 'reasoning_options' entries must be objects"
        )
    for opt in options:
        if opt.get("type") not in _KNOWN_REASONING_OPTION_TYPES:
            raise OpencodeImportError(
                f"{where}: unknown reasoning option type {opt.get('type')!r} "
                f"(known: {sorted(_KNOWN_REASONING_OPTION_TYPES)})"
            )
    levels = next(
        (
            [v for v in (opt.get("values") or []) if isinstance(v, str)]
            for opt in options
            if opt.get("type") == "effort"
        ),
        [],
    )
    has_effort = len(levels) > 0
    toggle = any(opt.get("type") == "toggle" for opt in options)
    budget: dict[str, int] = {}
    for opt in options:
        if opt.get("type") == "budget_tokens":
            for bound in ("min", "max"):
                if bound in opt:
                    value = opt[bound]
                    if isinstance(value, bool) or not isinstance(value, int) or value < 0:
                        raise OpencodeImportError(
                            f"{where}: reasoning budget {bound!r} must be a "
                            f"non-negative integer, got {value!r}"
                        )
                    budget[bound] = value
            break

    params: dict[str, Any] = {
        "supports_reasoning": bool(model.get("reasoning")),
        "supports_reasoning_effort": has_effort,
        "reasoning_effort_levels": levels,
    }
    descriptors: list[dict[str, Any]] = []
    if has_effort:
        # No is_default marker upstream: the first (= smallest) level wins.
        # A leading "none" means "reasoning can be turned off", which is a
        # capability flag, not a playground default -- emitting it as the
        # default would silently change the row's behaviour.
        default = levels[0] if levels[0] != "none" else None
        if default is not None:
            params["default_reasoning_effort"] = default
        if "none" in levels:
            params["supports_none_reasoning_effort"] = True
        descriptors.append(_effort_descriptor(levels, default))
    if toggle:
        params["supports_reasoning_disable"] = True
        descriptors.append(_thinking_descriptor())
    if budget:
        params["reasoning_budget"] = budget
    return params, descriptors


def _effort_descriptor(levels: list[str], default: str | None) -> dict[str, Any]:
    descriptor: dict[str, Any] = {
        "id": "reasoning_effort",
        "label": "Reasoning Effort",
        "helpText": "Reasoning effort levels advertised by Opencode for this model",
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


def opencode_model_to_entry(
    model: dict[str, Any],
    provider: str,
    slice_npm: Any,
    warnings: list[str],
) -> dict[str, Any]:
    """Build the ``{"parameters": ..., "pricing": ...}`` overlay entry."""
    mid = model["id"]
    where = f"{provider}/{mid}"
    raw_provider = model.get("provider")
    npm = (
        raw_provider.get("npm")
        if isinstance(raw_provider, dict) and raw_provider.get("npm")
        else slice_npm
    )
    endpoint = _endpoint_for_npm(npm, mid, where)
    limit = model["limit"]
    max_input = limit["context"]
    max_output = limit["output"]

    tools = bool(model.get("tool_call"))
    modalities = model.get("modalities")
    inputs = (
        modalities.get("input")
        if isinstance(modalities, dict) and isinstance(modalities.get("input"), list)
        else None
    )

    reasoning_fields, descriptors = _reasoning_parts(model, where)

    parameters: dict[str, Any] = {
        "mode": "chat",
        "provider": provider,
        "max_input_tokens": max_input,
        "max_output_tokens": max_output,
        "supported_endpoints": [endpoint],
        "supports_function_calling": tools,
        "supports_tool_choice": tools,
    }
    if inputs is not None:
        parameters["supports_vision"] = "image" in inputs
    parameters.update(reasoning_fields)
    if model.get("temperature") is False:
        parameters["supports_sampling_params"] = False
    if "structured_output" in model:
        parameters["supports_response_schema"] = bool(model["structured_output"])
    if descriptors:
        parameters["model_parameters"] = descriptors

    pricing: dict[str, Any] = {
        "mode": "chat",
        "provider": provider,
        "max_input_tokens": max_input,
        "max_output_tokens": max_output,
    }
    if model.get("status") == "deprecated":
        pricing["is_deprecated"] = True
    pricing.update(_pricing_costs(model.get("cost"), where, where, warnings))

    return {"parameters": parameters, "pricing": pricing}


def import_opencode_models(
    opencode_data: Any,
    existing_overlay: dict[str, dict[str, Any]] | None = None,
    *,
    overwrite_existing: bool = False,
) -> ImportResult:
    """Convert an Opencode catalog payload into overlay entries.

    Keys are ``<provider>/<id>`` (``opencode-zen/...`` for the ``opencode``
    slice, ``opencode-go/...`` for ``opencode-go``). Entries already present
    are left untouched unless ``overwrite_existing`` is set; either way they
    are reported, so the caller (CLI summary, GUI dialog) can say what happened
    instead of guessing. Warnings cover imported entries only -- skipped ones
    contribute none.
    """
    existing = existing_overlay or {}
    result = ImportResult()
    for provider, model, slice_npm in parse_opencode_models(opencode_data):
        key = provider + "/" + model["id"]
        entry_warnings: list[str] = []
        result.entries[key] = opencode_model_to_entry(
            model, provider, slice_npm, entry_warnings
        )
        if key in existing:
            if overwrite_existing:
                result.overwritten.append(key)
                result.warnings.extend(entry_warnings)
            else:
                result.skipped.append(key)
        else:
            result.added.append(key)
            result.warnings.extend(entry_warnings)
    return result


def apply_opencode_import(
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
