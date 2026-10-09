"""Import the Antigravity ``fetchAvailableModels`` capture into a custom overlay.

The debug capture ``debug/antigravity/agy-models.json`` is the response of
``POST v1internal:fetchAvailableModels``. Besides the ``models`` dict (keyed
by model id) it carries the routing id lists that define the user-facing
surface:

- ``agentModelSorts`` -- ``[{"displayName": ..., "groups": [{"modelIds":
  [...]}]}]`` (the agent picker ordering);
- ``commandModelIds`` -- ids usable as one-shot commands;
- ``imageGenerationModelIds`` -- image-generation models;
- ``mqueryModelIds`` -- M-query (fast ask) models;
- ``webSearchModelIds`` -- models usable for web search;
- ``commitMessageModelIds`` -- models usable for commit messages.

Only ids from those six lists are converted -- that is what Antigravity
exposes to users. Other lists in the capture (``tabModelIds``,
``deprecatedModelIds``, ``audioTranscriptionModelIds``, ``tieredModelIds``)
and every model never referenced by those six lists are discarded, not
guessed at. Within the selection, any model with ``isInternal: true`` is
skipped and reported as a warning. ``tabModelIds`` (``chat_20706`` /
``chat_23310``) are internal tab-completion models and are deliberately not
part of the selection.

Two different "providers" appear in this flow -- do not confuse them:

- the ``modelProvider`` / ``apiProvider`` fields inside the capture
  (``MODEL_PROVIDER_GOOGLE`` etc.) are upstream routing metadata. They are
  consumed only for presence validation and never written.
- the ``provider`` field in the overlay is the *Bifrost* provider serving the
  model. Every row carries ``"provider": "antigravity"`` -- otherwise a
  ``antigravity/<id>`` request would never match its own row.

Entry key: ``antigravity/<id>``.

Mode, per source list:

- ``imageGenerationModelIds`` -> ``"image_generation"`` (native image models
  use that mode across the catalog; the wire is still generateContent);
- the other five lists -> ``"chat"``.

An id appearing in two lists with disagreeing modes is an error -- the mode
cannot be guessed. Same mode in several lists is fine: the id converts once,
in the order its lists are checked.

Mapping (agy field -> overlay field):

- ``maxTokens`` / ``maxOutputTokens`` -> ``max_input_tokens`` /
  ``max_output_tokens`` (in both sections, when present). ``maxTokens`` is
  also written as ``context_length`` (same value as ``max_input_tokens``),
  and ``max_tokens`` is ``max_input_tokens + max_output_tokens`` when both
  limits are present (falls back to ``maxTokens`` alone when the output
  limit is absent). Unlike the other
  importers the limits are *optional*: some capture rows (e.g.
  ``gemini-3.1-flash-image``) carry no token limits, and omitting the fields
  is what the Antigravity static fallback list does for the same model.
- ``supportsImages`` -> ``supports_vision``.
- ``supportsThinking`` -> ``supports_reasoning``.
- ``supportsAdaptiveThinking`` -> ``supports_adaptive_thinking``.
- ``thinkingBudget``: ``-1`` (dynamic) -> ``supports_dynamic_reasoning_budget:
  true``; a value ``> 0`` -> ``reasoning_budget.max``. ``minThinkingBudget``
  -> ``reasoning_budget.min``. Either bound may appear alone.
- ``supported_endpoints``: always ``["/v1internal:generateContent"]`` -- the
  single wire the Antigravity provider uses
  (``core/providers/antigravity/auth.go`` ``GenerateContentPath``).
- capability flags are written explicitly **only when the source key is
  present**: absent keys stay absent, because an explicit ``false`` would
  claim support the capture simply did not report. No ``base_model``: the
  ``antigravity/<id>`` key is the identity.

Field placement follows the merge engine's own rule
(:func:`merge.check_field_placement`): capability flags live in the
``parameters`` section only. The ``pricing`` section carries just ``mode``,
``provider`` and the token limits -- the fields that section accepts -- so the
imported entries merge cleanly. No cost fields: the capture carries only
``quotaInfo``, no prices.

Deliberately omitted (no datasheet counterpart, so they would be dead data):
``quotaInfo``, ``isInternal``, ``recommended``, ``displayName``, ``model``,
``apiProvider``, ``modelProvider``, ``vertexModelId``, ``tagTitle``,
``tagDescription``, ``modelExperiments``, ``supportedMimeTypes``,
``supportsVideo``, ``supportsCumulativeContext``,
``supportsEstimateTokenCounter``, ``tabJumpPrintLineRange``,
``addCursorToFindReplaceTarget``, ``promptTemplaterType``,
``toolFormatterType``, ``requiresLeadInGeneration``,
``requiresNoXmlToolExamples``, ``requiresImageOutputOutsideFunctionResponses``,
``thinkingLevel`` (a 1-3 ladder with no proven datasheet vocabulary),
``minThinkingBudget`` without a ``thinkingBudget`` context,
top-level lists other than the six converted.

Pure functions, no Qt and no IO -- same rule as :mod:`datasheet_editor.merge`,
:mod:`datasheet_editor.zed_import` and :mod:`datasheet_editor.opencode_import`,
so the CLI and the GUI cannot disagree about import semantics.
"""

from __future__ import annotations

from dataclasses import dataclass, field
from typing import Any

#: Overlay key prefix. Hardcoded so an import can never collide with a
#: base-provider row.
AGY_KEY_PREFIX = "antigravity/"

#: The Bifrost provider serving every imported model.
AGY_PROVIDER = "antigravity"

#: The single upstream wire path (``GenerateContentPath`` in
#: ``core/providers/antigravity/auth.go``).
AGY_ENDPOINT = "/v1internal:generateContent"

#: The six selection lists, in fixed conversion order, paired with the mode
#: the rows from that list take.
AGENT_LIST_MODE = "chat"
IMAGE_LIST_MODE = "image_generation"

#: Flat id lists, each paired with its mode.
_FLAT_ID_LISTS: tuple[tuple[str, str], ...] = (
    ("commandModelIds", AGENT_LIST_MODE),
    ("imageGenerationModelIds", IMAGE_LIST_MODE),
    ("mqueryModelIds", AGENT_LIST_MODE),
    ("webSearchModelIds", AGENT_LIST_MODE),
    ("commitMessageModelIds", AGENT_LIST_MODE),
)

#: Agy fields with no datasheet counterpart. Listed so a future reader can
#: tell "dropped on purpose" from "forgotten".
OMITTED_AGY_FIELDS = (
    "quotaInfo",
    "isInternal",
    "recommended",
    "displayName",
    "model",
    "apiProvider",
    "modelProvider",
    "vertexModelId",
    "tagTitle",
    "tagDescription",
    "modelExperiments",
    "supportedMimeTypes",
    "supportsVideo",
    "supportsCumulativeContext",
    "supportsEstimateTokenCounter",
    "tabJumpPrintLineRange",
    "addCursorToFindReplaceTarget",
    "promptTemplaterType",
    "toolFormatterType",
    "requiresLeadInGeneration",
    "requiresNoXmlToolExamples",
    "requiresImageOutputOutsideFunctionResponses",
    "thinkingLevel",
    "tabModelIds",
    "deprecatedModelIds",
    "audioTranscriptionModelIds",
    "tieredModelIds",
    "experimentIds",
    "defaultAgentModelId",
)


class AntigravityImportError(ValueError):
    """Raised when the input is not a usable fetchAvailableModels capture."""


@dataclass
class ImportResult:
    """What an import did. ``entries`` holds the built overlay sections."""

    entries: dict[str, dict[str, Any]] = field(default_factory=dict)
    added: list[str] = field(default_factory=list)
    skipped: list[str] = field(default_factory=list)
    overwritten: list[str] = field(default_factory=list)
    #: Non-fatal drops (e.g. an isInternal model excluded from the selection).
    #: The rest of the entry set is still imported; nothing silently vanishes
    #: without a trace.
    warnings: list[str] = field(default_factory=list)


def _agent_model_ids(sorts: Any) -> list[str]:
    """Flatten ``agentModelSorts`` into a deterministic id list."""
    if not isinstance(sorts, list):
        raise AntigravityImportError(
            f"'agentModelSorts': expected a list, got {type(sorts).__name__}"
        )
    out: list[str] = []
    for i, sort in enumerate(sorts):
        where = f"agentModelSorts[{i}]"
        if not isinstance(sort, dict):
            raise AntigravityImportError(f"{where}: expected an object, got {type(sort).__name__}")
        groups = sort.get("groups")
        if not isinstance(groups, list):
            raise AntigravityImportError(f"{where}: missing required field 'groups' (list)")
        for j, group in enumerate(groups):
            gwhere = f"{where}.groups[{j}]"
            if not isinstance(group, dict) or not isinstance(group.get("modelIds"), list):
                raise AntigravityImportError(f"{gwhere}: expected an object with 'modelIds' (list)")
            for mid in group["modelIds"]:
                if not isinstance(mid, str) or not mid:
                    raise AntigravityImportError(f"{gwhere}: 'modelIds' entries must be non-empty strings")
                out.append(mid)
    return out


def _flat_ids(value: Any, name: str) -> list[str]:
    if not isinstance(value, list):
        raise AntigravityImportError(f"{name!r}: expected a list, got {type(value).__name__}")
    out: list[str] = []
    for mid in value:
        if not isinstance(mid, str) or not mid:
            raise AntigravityImportError(f"{name!r}: entries must be non-empty strings, got {mid!r}")
        out.append(mid)
    return out


def parse_agy_models(data: Any) -> list[tuple[str, dict[str, Any], str]]:
    """Validate a fetchAvailableModels capture and return its models.

    Returns ``(model_id, model, mode)`` triples in conversion order: agent
    sorts first, then command/image/mquery/websearch/commit lists, each in
    file order. An id selected by several lists converts once, under the
    first mode seen; conflicting modes for one id are an error.

    Raises :class:`AntigravityImportError` when the shape is not an
    Antigravity catalog, a selected id is missing from ``models``, a model
    entry is not an object, a token limit is not an int, or a model repeats
    with conflicting token data.
    """
    if not isinstance(data, dict) or not isinstance(data.get("models"), dict):
        raise AntigravityImportError(
            'expected an Antigravity catalog object shaped like {"models": {...}, '
            '"agentModelSorts": [...], ...}, '
            f"got {type(data).__name__}"
        )
    models: dict[str, Any] = data["models"]

    mode_by_id: dict[str, str] = {}
    ordered: list[str] = []

    def consider(mid: str, mode: str, where: str) -> None:
        if mid in mode_by_id:
            if mode_by_id[mid] != mode:
                raise AntigravityImportError(
                    f"{where}: model {mid!r} is listed with both modes "
                    f"{mode_by_id[mid]!r} and {mode!r}; refusing to guess"
                )
            return
        mode_by_id[mid] = mode
        ordered.append(mid)

    for i, mid in enumerate(_agent_model_ids(data.get("agentModelSorts", []))):
        consider(mid, AGENT_LIST_MODE, f"agentModelSorts[{i}]")
    for name, mode in _FLAT_ID_LISTS:
        for i, mid in enumerate(_flat_ids(data.get(name, []), name)):
            consider(mid, mode, f"{name}[{i}]")

    if not ordered:
        raise AntigravityImportError(
            "none of the six selection lists (agentModelSorts, commandModelIds, "
            "imageGenerationModelIds, mqueryModelIds, webSearchModelIds, "
            "commitMessageModelIds) yielded a model id"
        )

    out: list[tuple[str, dict[str, Any], str]] = []
    for mid in ordered:
        if mid not in models:
            raise AntigravityImportError(f"selected model {mid!r} missing from 'models'")
        model = models[mid]
        where = f"models[{mid!r}]"
        if not isinstance(model, dict):
            raise AntigravityImportError(f"{where}: expected an object, got {type(model).__name__}")
        for name in ("maxTokens", "maxOutputTokens", "minThinkingBudget", "thinkingBudget"):
            value = model.get(name)
            if value is not None and (isinstance(value, bool) or not isinstance(value, int)):
                raise AntigravityImportError(f"{where}: {name!r} must be an integer, got {value!r}")
            if isinstance(value, int) and value < 0 and name != "thinkingBudget":
                raise AntigravityImportError(f"{where}: {name!r} must not be negative, got {value!r}")
        ib = model.get("isInternal")
        if ib is not None and not isinstance(ib, bool):
            raise AntigravityImportError(f"{where}: 'isInternal' must be a boolean, got {ib!r}")
        out.append((mid, model, mode_by_id[mid]))
    return out


def antigravity_model_to_entry(model: dict[str, Any], mode: str) -> dict[str, Any]:
    """Build the ``{"parameters": ..., "pricing": ...}`` overlay entry."""
    parameters: dict[str, Any] = {
        "mode": mode,
        "provider": AGY_PROVIDER,
        "supported_endpoints": [AGY_ENDPOINT],
    }
    max_input = model.get("maxTokens")
    max_output = model.get("maxOutputTokens")
    if max_input is not None:
        parameters["max_input_tokens"] = max_input
        parameters["context_length"] = max_input
        # ``max_tokens`` is the full window: input + output. Without an
        # output limit there is nothing to sum, so it falls back to maxTokens.
        if max_output is not None:
            parameters["max_tokens"] = max_input + max_output
        else:
            parameters["max_tokens"] = max_input
    if max_output is not None:
        parameters["max_output_tokens"] = max_output

    # Capability flags are explicit only when the capture reports the key:
    # an explicit false would assert support the file never stated.
    if "supportsImages" in model:
        parameters["supports_vision"] = bool(model["supportsImages"])
    if "supportsThinking" in model:
        parameters["supports_reasoning"] = bool(model["supportsThinking"])
    if "supportsAdaptiveThinking" in model:
        parameters["supports_adaptive_thinking"] = bool(model["supportsAdaptiveThinking"])

    budget: dict[str, int] = {}
    if model.get("minThinkingBudget") is not None:
        budget["min"] = model["minThinkingBudget"]
    thinking_budget = model.get("thinkingBudget")
    if isinstance(thinking_budget, int) and not isinstance(thinking_budget, bool):
        if thinking_budget < 0:
            # Agy reports -1 for "let the model decide" (Gemini's
            # thinkingBudget: -1), which only makes sense for reasoning rows.
            parameters["supports_dynamic_reasoning_budget"] = True
        elif thinking_budget > 0:
            budget["max"] = thinking_budget
    if budget:
        parameters["reasoning_budget"] = budget

    # Pricing carries identity + limits only: that is what the merge engine's
    # placement rule accepts in this section. The capability flags live in
    # parameters (see module docstring). No cost fields: the capture has none.
    pricing: dict[str, Any] = {"mode": mode, "provider": AGY_PROVIDER}
    if max_input is not None:
        pricing["max_input_tokens"] = max_input
        pricing["context_length"] = max_input
        if max_output is not None:
            pricing["max_tokens"] = max_input + max_output
        else:
            pricing["max_tokens"] = max_input
    if max_output is not None:
        pricing["max_output_tokens"] = max_output

    return {"parameters": parameters, "pricing": pricing}


def import_antigravity_models(
    agy_data: Any,
    existing_overlay: dict[str, dict[str, Any]] | None = None,
    *,
    overwrite_existing: bool = False,
) -> ImportResult:
    """Convert a fetchAvailableModels payload into overlay entries.

    Keys are ``antigravity/<id>``. Entries already present are left untouched
    unless ``overwrite_existing`` is set; either way they are reported, so the
    caller (CLI summary, GUI dialog) can say what happened instead of guessing.
    Models with ``isInternal: true`` are excluded and reported as warnings.
    """
    existing = existing_overlay or {}
    result = ImportResult()
    for model_id, model, mode in parse_agy_models(agy_data):
        key = AGY_KEY_PREFIX + model_id
        if model.get("isInternal") is True:
            result.warnings.append(f"antigravity/{model_id}: excluded (isInternal: true)")
            continue
        result.entries[key] = antigravity_model_to_entry(model, mode)
        if key in existing:
            if overwrite_existing:
                result.overwritten.append(key)
            else:
                result.skipped.append(key)
        else:
            result.added.append(key)
    return result


def apply_antigravity_import(
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
