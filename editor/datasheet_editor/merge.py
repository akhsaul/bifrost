"""The merge engine.

Pure functions, no Qt and no IO, so the GUI and the CLI cannot disagree about
merge semantics. The pipeline is:

1. Apply the overlay's ``parameters`` section over the original parameters.
2. Apply the overlay's ``pricing`` section over the original pricing.
3. Split back into two files -- parameters verbatim, pricing narrowed or
   preserved per ``pricing_fields``.

Merge rules (add/override only -- nothing is ever deleted by omission):

- A model absent from the original is appended whole.
- Fields the overlay does not mention are preserved.
- Nested objects deep-merge (``tiered_pricing``, a param's ``range``).
- Lists replace wholesale (``supported_regions``, ``model_parameters``).
- ``model_parameters`` is an array of objects keyed by ``id``; by default it
  merges by that id so a single parameter can be overridden in isolation.
- ``null`` is a real value, not a deletion. The originals contain legitimate
  nulls (``rpm``, ``tpm``).
"""

from __future__ import annotations

from copy import deepcopy
from dataclasses import dataclass, field
from enum import Enum
from typing import Any, Iterable

#: Field holding the parameter-form descriptors. Merged by ``id``, not position.
PARAMS_FIELD = "model_parameters"
#: The key inside each descriptor that identifies it.
PARAM_ID_FIELD = "id"


class ParamArrayMode(str, Enum):
    """How to combine a ``model_parameters`` array from original and overlay."""

    #: Match descriptors by ``id`` and deep-merge them; append new ids.
    MERGE = "merge"
    #: Overlay array replaces the original array wholesale.
    REPLACE = "replace"


class PricingFieldPolicy(str, Enum):
    """How to treat pricing fields Bifrost does not read."""

    #: Keep every field. No data loss; recommended.
    PRESERVE = "preserve"
    #: Drop fields absent from the Go ``datasheet.Entry`` read-set.
    STRICT = "strict"


@dataclass(frozen=True)
class Change:
    """One field-level difference introduced by the overlay."""

    model: str
    section: str
    """``"parameters"`` or ``"pricing"``."""
    path: str
    """Field name, or ``model_parameters[id=temperature].default`` for a nested one."""
    kind: str
    """``added``, ``overridden``, or ``model_added``."""
    before: Any = None
    after: Any = None

    @property
    def field_name(self) -> str:
        """The trailing field of :attr:`path`, used for value formatting."""
        return self.path.rsplit(".", 1)[-1]

    def describe(self) -> str:
        from .format import explain

        if self.kind == "model_added":
            return f"{self.model}: new model ({len(self.after)} fields)"
        if self.kind == "added":
            return f"{self.model}.{self.path} = {explain(self.field_name, self.after)} (added)"
        return (
            f"{self.model}.{self.path}: "
            f"{explain(self.field_name, self.before)} -> {explain(self.field_name, self.after)}"
        )


@dataclass
class MergeResult:
    """Merged data plus an audit trail of what changed."""

    parameters: dict[str, dict[str, Any]] = field(default_factory=dict)
    pricing: dict[str, dict[str, Any]] = field(default_factory=dict)
    changes: list[Change] = field(default_factory=list)
    #: Fields pricing-strict mode dropped, with how many entries they appeared in.
    dropped_pricing_fields: dict[str, int] = field(default_factory=dict)
    #: Models present in the overlay but in neither original.
    new_models: list[str] = field(default_factory=list)

    @property
    def changed_models(self) -> set[str]:
        return {c.model for c in self.changes}

    def changes_for(self, model: str) -> list[Change]:
        return [c for c in self.changes if c.model == model]

    def summary(self) -> dict[str, int]:
        counts: dict[str, int] = {}
        for change in self.changes:
            counts[change.kind] = counts.get(change.kind, 0) + 1
        counts["changed_models"] = len(self.changed_models)
        counts["new_models"] = len(self.new_models)
        if self.dropped_pricing_fields:
            counts["dropped_pricing_fields"] = sum(self.dropped_pricing_fields.values())
        return counts


def merge(
    original_parameters: dict[str, dict[str, Any]],
    original_pricing: dict[str, dict[str, Any]],
    overlay: dict[str, dict[str, Any]],
    *,
    param_array_mode: ParamArrayMode = ParamArrayMode.MERGE,
    pricing_fields: PricingFieldPolicy = PricingFieldPolicy.PRESERVE,
) -> MergeResult:
    """Merge a custom overlay over both originals.

    Neither input is mutated; ``deepcopy`` is used per touched entry so a
    12,595-model dataset is not copied wholesale on every edit.
    """
    result = MergeResult()
    params = dict(original_parameters)
    pricing = dict(original_pricing)

    # The observed field vocabulary of each target dataset. Used by the section
    # validators so a real field like `tiered_pricing` -- which is neither in
    # the Go read-set nor named like a cost -- is not mistaken for a mistake.
    known_param_fields = {name for entry in original_parameters.values() for name in entry}
    known_pricing_fields = {name for entry in original_pricing.values() for name in entry}

    for model, sections in overlay.items():
        if not isinstance(sections, dict):
            raise MergeError(f"overlay entry {model!r} must be an object with 'parameters'/'pricing' keys")

        unknown = set(sections) - {"parameters", "pricing"}
        if unknown:
            raise MergeError(
                f"overlay entry {model!r} has unknown section(s): {', '.join(sorted(unknown))}; "
                "expected 'parameters' and/or 'pricing'"
            )

        params_section = sections.get("parameters")
        pricing_section = sections.get("pricing")

        if isinstance(params_section, dict) or isinstance(pricing_section, dict):
            if model not in params and model not in pricing and (params_section or pricing_section):
                result.new_models.append(model)

        if isinstance(params_section, dict):
            params[model] = _merge_entry(
                model,
                "parameters",
                params_section,
                params.get(model),
                result.changes,
                param_array_mode,
                _params_validator(model, params_section, known_param_fields),
            )

        if isinstance(pricing_section, dict):
            pricing[model] = _merge_entry(
                model,
                "pricing",
                pricing_section,
                pricing.get(model),
                result.changes,
                param_array_mode,
                _pricing_validator(model, pricing_section, known_pricing_fields),
            )

    result.parameters = params

    if pricing_fields is PricingFieldPolicy.STRICT:
        from .fields import pricing_read_set

        allowed = pricing_read_set()
        result.pricing = {}
        result.dropped_pricing_fields = {}
        for model, entry in pricing.items():
            kept = {k: v for k, v in entry.items() if k in allowed}
            for name in entry.keys() - kept.keys():
                result.dropped_pricing_fields[name] = result.dropped_pricing_fields.get(name, 0) + 1
            result.pricing[model] = kept
    else:
        result.pricing = pricing

    return result


class MergeError(ValueError):
    """Raised when the overlay is structurally wrong (a hard validation error)."""


def _diff_param_array(
    before: list[Any], after: list[Any]
) -> list[tuple[str, str, Any, Any, str]]:
    """Yield ``(id, field, old, new, kind)`` for each descriptor-level change.

    Descriptors are matched on their ``id`` so a one-field override reads as one
    change line rather than a whole-array diff.
    """
    def index_by_id(items: list[Any]) -> tuple[dict[str, dict[str, Any]], list[Any]]:
        by_id: dict[str, dict[str, Any]] = {}
        loose: list[Any] = []
        for item in items:
            if isinstance(item, dict) and isinstance(item.get(PARAM_ID_FIELD), str):
                by_id[item[PARAM_ID_FIELD]] = item
            else:
                loose.append(item)
        return by_id, loose

    old_by_id, old_loose = index_by_id(before)
    new_by_id, new_loose = index_by_id(after)

    out: list[tuple[str, str, Any, Any, str]] = []
    for pid, new_item in new_by_id.items():
        old_item = old_by_id.get(pid)
        if old_item is None:
            out.append((pid, "*", None, new_item, "added"))
            continue
        for field in new_item:
            if field == PARAM_ID_FIELD:
                continue
            old_value = old_item.get(field)
            if old_value != new_item[field]:
                out.append((pid, field, old_value, new_item[field], "overridden"))
    for item in old_loose:
        if item not in new_loose:
            out.append((str(item.get(PARAM_ID_FIELD, "?")), "*", item, None, "removed"))
    for item in new_loose:
        if item not in old_loose:
            out.append((str(item.get(PARAM_ID_FIELD, "?")), "*", None, item, "added"))
    return out


def _merge_entry(
    model: str,
    section: str,
    overlay_entry: dict[str, Any],
    original_entry: dict[str, Any] | None,
    changes: list[Change],
    param_array_mode: ParamArrayMode,
    validator: Any,
) -> dict[str, Any]:
    """Overlay one entry's fields onto its original."""
    validator()

    base = deepcopy(original_entry) if original_entry is not None else {}
    for name, new_value in overlay_entry.items():
        # The parameter array is nested and large; report the individual
        # descriptor edits rather than dumping thousands of characters of
        # before/after JSON.
        if name == PARAMS_FIELD and isinstance(new_value, list) and isinstance(base.get(name), list):
            before = base[name]
            merged = _merge_param_array(before, new_value, param_array_mode)
            if merged != before:
                changes.extend(
                    Change(
                        model=model,
                        section=section,
                        path=f"{PARAMS_FIELD}[{PARAM_ID_FIELD}={pid}].{field}",
                        kind=kind,
                        before=old,
                        after=new,
                    )
                    for pid, field, old, new, kind in _diff_param_array(before, merged)
                )
            base[name] = merged
            continue

        if name in base:
            merged = _deep_merge(base[name], new_value)
        else:
            merged = deepcopy(new_value)

        if merged != base.get(name):
            changes.append(
                Change(
                    model=model,
                    section=section,
                    path=name,
                    kind="added" if name not in base else "overridden",
                    before=base.get(name),
                    after=merged,
                )
            )
        base[name] = merged

    if original_entry is None and overlay_entry:
        changes.append(
            Change(model=model, section=section, path="*", kind="model_added", after=base)
        )
    return base


def _merge_param_array(
    original: Any,
    overlay: list[Any],
    mode: ParamArrayMode,
) -> Any:
    """Combine two ``model_parameters`` arrays.

    Defaults to matching descriptors on their ``id`` so one parameter can be
    overridden without restating the rest, preserving original order and
    appending genuinely new ids.
    """
    if not isinstance(original, list) or mode is ParamArrayMode.REPLACE:
        return deepcopy(overlay)

    # Copy before mutating: the caller compares the result against the entry it
    # started from, so editing the original dicts in place would make every
    # override look like a no-op in the change report.
    by_id: dict[str, dict[str, Any]] = {}
    merged: list[Any] = []
    for item in original:
        if isinstance(item, dict) and isinstance(item.get(PARAM_ID_FIELD), str):
            copied = deepcopy(item)
            by_id[copied[PARAM_ID_FIELD]] = copied
            merged.append(copied)
        else:
            merged.append(deepcopy(item))

    for item in overlay:
        if not isinstance(item, dict):
            merged.append(deepcopy(item))
            continue
        item_id = item.get(PARAM_ID_FIELD)
        if isinstance(item_id, str) and item_id in by_id:
            target = by_id[item_id]
            for name, value in item.items():
                if name == PARAM_ID_FIELD:
                    continue
                target[name] = _deep_merge(target.get(name), deepcopy(value))
        else:
            merged.append(deepcopy(item))
    return merged


def _deep_merge(base: Any, overlay: Any) -> Any:
    """Deep-merge two JSON values.

    Dicts merge key-by-key; every other type (including lists) is replaced by the
    overlay. ``None`` is a value, not a delete marker -- there is no delete
    channel, because absence of a field never removes anything.
    """
    if isinstance(base, dict) and isinstance(overlay, dict):
        out = dict(base)
        for key, value in overlay.items():
            out[key] = _deep_merge(out[key], value) if key in out else deepcopy(value)
        return out
    return deepcopy(overlay)


def check_field_placement(model: str, section: str, name: str, known: set[str]) -> str | None:
    """Return an error message if ``name`` cannot go in ``section``, else ``None``.

    The single source of truth for which fields belong in which section, shared by
    the merge-time validators and the GUI's "Add Field" dialog so the editor
    cannot offer a placement the merge would then reject.
    """
    from .fields import is_capability_field, is_cost_field, is_pricing_read, looks_like_cost

    if section == "parameters":
        if name == PARAMS_FIELD:
            return None
        if is_cost_field(name):
            return (
                f"{name} is a cost field (read by Bifrost's pricing entry) -- "
                "put it in the 'pricing' section instead"
            )
        if looks_like_cost(name) and not is_capability_field(name) and name not in known:
            return (
                f"{name} looks like a cost field and does not appear in the parameters "
                "dataset -- move it to the 'pricing' section"
            )
        # Capability fields are deliberately allowed here: they appear in both
        # files by design (the pricing copy is what GetCapabilityEntry reads, the
        # params copy is what the UI builds forms from).
        return None

    if section == "pricing":
        if name == PARAMS_FIELD:
            return (
                f"{PARAMS_FIELD} belongs in the 'parameters' section -- it is not part "
                "of the pricing entry"
            )
        if is_capability_field(name) or is_pricing_read(name):
            return None
        if looks_like_cost(name) or name in known:
            return None
        return (
            f"{name} is neither a pricing nor a capability field -- "
            "did you mean to put it in the 'parameters' section?"
        )

    return f"unknown section {section!r} (expected 'parameters' or 'pricing')"


def _params_validator(model: str, section: dict[str, Any], known: set[str]):
    """Reject cost fields routed into the parameters section.

    Such a field would land in ``model_parameters.json``, which Bifrost never
    reads costs from -- so it would be accepted silently and then do nothing.
    """

    def validate() -> None:
        for name in section:
            problem = check_field_placement(model, "parameters", name, known)
            if problem:
                raise MergeError(problem)

    return validate


def _pricing_validator(model: str, section: dict[str, Any], known: set[str]):
    """Reject non-cost, non-capability fields routed into the pricing section.

    A field is accepted when it is a known cost (in the Go read-set, named like
    one, or already present in the pricing dataset) or a known capability field.
    Anything else almost certainly belongs in the params section, and would be
    dropped on load anyway because ``datasheet.Entry`` does not declare it.
    """

    def validate() -> None:
        for name in section:
            problem = check_field_placement(model, "pricing", name, known)
            if problem:
                raise MergeError(problem)

    return validate


def merge_datasets(
    original_parameters: dict[str, dict[str, Any]],
    original_pricing: dict[str, dict[str, Any]],
    overlay: dict[str, dict[str, Any]],
    *,
    param_array_mode: ParamArrayMode = ParamArrayMode.MERGE,
    pricing_fields: PricingFieldPolicy = PricingFieldPolicy.PRESERVE,
) -> MergeResult:
    """Alias of :func:`merge` for callers that think in datasets rather than dicts."""
    return merge(
        original_parameters,
        original_pricing,
        overlay,
        param_array_mode=param_array_mode,
        pricing_fields=pricing_fields,
    )


def iter_entry_fields(entry: dict[str, Any]) -> Iterable[tuple[str, Any]]:
    """Yield ``(name, value)`` for every top-level field of an entry."""
    return entry.items()
