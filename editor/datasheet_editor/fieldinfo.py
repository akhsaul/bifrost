"""What each field in the datasheets means and what shape its value takes.

The user-facing problem this solves: the two JSON files carry no documentation.
Nothing in ``model_parameters.json`` says what ``reasoning_effort_levels`` is, or
that it is a list of strings, or which effort labels Bifrost knows about. Go's
source is the only place that knows, so this module builds a catalog from two
sources and hands the editor something to show:

- **Go source, generated** (``param_fields.json`` / ``pricing_fields.json``): the
  declared field name, Go type and doc comment. Generated rather than hand-kept
  so a field added to the Go struct shows up here instead of going stale.
- **The loaded data**: which fields actually appear, in how many models, with
  what shape, and -- for small controlled vocabularies -- which values occur.

Everything here is pure: no Qt, no IO beyond reading the generated snapshots.
The GUI turns a :class:`FieldInfo` into widgets; the CLI can print the same
information as text.

Two vocabularies, because the datasheet has two:

``fields``
    The top-level columns of an entry -- ``supports_vision``, ``max_tokens``,
    ``reasoning_effort_levels``. Read by Go only if declared on
    ``schemas.ModelCapabilities`` (for the parameters file) or ``datasheet.Entry``
    (for the pricing file).

``descriptors``
    The entries of the ``model_parameters`` array -- ``reasoning_effort``,
    ``temperature``, ``top_p``. These describe how Bifrost's prompt playground
    renders a control. Go models only the ``id``; ``label``, ``helpText``,
    ``type``, ``default``, ``range`` and ``options`` are served to the UI
    verbatim from the stored row, so editing them is still meaningful even though
    Go never inspects them.
"""

from __future__ import annotations

import json
from collections import Counter
from dataclasses import dataclass, field
from functools import lru_cache
from pathlib import Path
from typing import Any

from .fields import FieldKind, is_pricing_read, pricing_read_set, value_kind

_SNAPSHOT = Path(__file__).with_name("param_fields.json")

#: Above this many distinct values a vocabulary stops being a controlled set and
#: starts being free text -- ``base_model`` has 9,265. Offering a dropdown of
#: thousands of entries would be worse than the text box it replaces, so the
#: cutoff is what decides "enum" versus "type it yourself".
MAX_ENUM_VALUES = 20


class ParamFieldSetUnavailable(RuntimeError):
    """Raised when the generated parameters read-set snapshot cannot be loaded."""


# --------------------------------------------------------------------------- #
# generated snapshot
# --------------------------------------------------------------------------- #


@lru_cache(maxsize=1)
def _snapshot() -> dict[str, Any]:
    try:
        with _SNAPSHOT.open(encoding="utf-8") as fh:
            return json.load(fh)
    except FileNotFoundError as exc:  # pragma: no cover - packaging error
        raise ParamFieldSetUnavailable(
            f"parameters read-set snapshot missing at {_SNAPSHOT}; "
            "regenerate with tools/gen_param_fields.py"
        ) from exc
    except json.JSONDecodeError as exc:  # pragma: no cover - packaging error
        raise ParamFieldSetUnavailable(f"parameters read-set snapshot is not valid JSON: {exc}") from exc


@lru_cache(maxsize=1)
def params_read_set() -> frozenset[str]:
    """JSON field names ``schemas.ModelCapabilities`` declares."""
    return frozenset(_snapshot().get("fields", {}))


@lru_cache(maxsize=1)
def _params_field_meta() -> dict[str, dict[str, str]]:
    return {name: dict(meta) for name, meta in _snapshot().get("fields", {}).items()}


@lru_cache(maxsize=1)
def descriptor_read_set() -> frozenset[str]:
    """Keys of one ``model_parameters[]`` descriptor that Go models.

    Only ``id``. The array's other keys ride along to the UI untouched, which is
    why :class:`DescriptorInfo` reports them separately from the Go read-set.
    """
    return frozenset(_snapshot().get("descriptor_fields", {}))


def params_field_kind(field: str) -> FieldKind:
    """Declared kind of a ``ModelCapabilities`` field, or ``"unknown"``."""
    return _params_field_meta().get(field, {}).get("kind", "unknown")  # type: ignore[return-value]


def params_field_doc(field: str) -> str:
    """The Go doc comment for a field -- the only prose description that exists."""
    return _params_field_meta().get(field, {}).get("doc", "")


# --------------------------------------------------------------------------- #
# catalog entries
# --------------------------------------------------------------------------- #


@dataclass(frozen=True)
class FieldInfo:
    """Everything known about one top-level field."""

    name: str
    kind: str
    """Resolved editor kind: bool, int, float, string, array, object, unknown."""
    kind_origin: str
    """Where ``kind`` came from: ``go`` (declared type), ``observed`` (inferred
    from the data), ``descriptor`` (a playground descriptor of the same name),
    or ``unknown``."""
    description: str = ""
    """Best prose available: the Go doc comment, else a generated summary."""
    enum_values: tuple[str, ...] = ()
    """Legal values for a single-value string field, when the vocabulary is closed."""
    enum_elements: tuple[str, ...] = ()
    """Legal values for each element of a string array, when closed."""
    default: Any = None
    minimum: float | None = None
    maximum: float | None = None
    present_in: int = 0
    """How many entries in the dataset carry this field."""
    total: int = 0
    """How many entries the dataset holds, for the "present in N of M" line."""
    read_from: str = "none"
    """Which file Go reads it from: ``parameters``, ``pricing`` or ``none``.

    A parameters field can be declared on ``datasheet.Entry`` instead, because
    the capability lookup runs against the pricing file. Telling those apart is
    the difference between an edit that takes effect and one that does not.
    """
    descriptor: "DescriptorInfo | None" = None
    """A ``model_parameters[]`` descriptor sharing this name, when one exists."""

    @property
    def read(self) -> bool:
        return self.read_from != "none"

    def type_sentence(self) -> str:
        """Human phrasing of the value shape, e.g. ``list of strings``."""
        base = {
            "bool": "true or false",
            "int": "whole number",
            "float": "number",
            "string": "text",
            "object": "object (JSON)",
            "unknown": "any value",
        }.get(self.kind, "any value")
        if self.kind == "array":
            element = {
                "string": "list of text",
                "bool": "list of true/false",
                "int": "list of whole numbers",
                "float": "list of numbers",
                "object": "list of objects",
            }.get(self.kind_element(), "list")
            return element
        return base

    def kind_element(self) -> str:
        """Element kind of an array field, from the declared Go type."""
        return _params_field_meta().get(self.name, {}).get("element_kind", "")

    def read_sentence(self) -> str:
        return {
            "parameters": "Read by Bifrost from model_parameters.json.",
            "pricing": (
                "Not read from model_parameters.json -- Bifrost reads this field from "
                "model_pricing.json instead, so edit it in the pricing section."
            ),
            "none": (
                "Not read by Bifrost from either file. It stays in the merged JSON, "
                "but nothing in Go consumes it."
            ),
        }[self.read_from]

    def summary(self) -> str:
        """One-paragraph description for the dialog."""
        parts: list[str] = [self.type_sentence()]
        if self.present_in:
            parts.append(f"present in {self.present_in:,} of {self.total:,} models")
        if self.minimum is not None and self.maximum is not None:
            parts.append(f"observed {_bound(self.minimum)} to {_bound(self.maximum)}")
        if self.default is not None:
            parts.append(f"most common value {_short(self.default)}")
        if self.enum_values:
            parts.append("choose from the dropdown")
        elif self.enum_elements:
            parts.append("tick the values that apply")
        return "; ".join(parts) + "."


@dataclass(frozen=True)
class DescriptorInfo:
    """Everything known about one ``model_parameters[]`` descriptor."""

    param_id: str
    kind: str
    """Declared playground type: number, select, boolean, text, array, object."""
    label: str = ""
    description: str = ""
    """``helpText`` from the datasheet -- the playground's own description."""
    enum_values: tuple[str, ...] = ()
    """Option values when ``type`` is ``select``."""
    default: Any = None
    minimum: float | None = None
    maximum: float | None = None
    element_kind: str = ""
    min_elements: int | None = None
    """Lower bound on a list's length, from ``array.minElements``.

    Kept apart from :attr:`minimum` on purpose: for ``stop`` the two are "1 to 4
    entries", not "values between 1 and 4", and showing one as the other would
    read as a constraint on the contents.
    """
    max_elements: int | None = None
    models: int = 0
    """How many models publish this parameter."""
    conflicts: tuple[str, ...] = ()
    """Aspects where models disagree, so the dialog can say so rather than guess."""

    def type_sentence(self) -> str:
        if self.kind == "select" and self.enum_values:
            return "one of: " + ", ".join(self.enum_values)
        return {
            "boolean": "true or false",
            "number": "number",
            "text": "text",
            "array": "list",
            "object": "object (JSON)",
            "select": "text (no options recorded)",
        }.get(self.kind, "any value")

    def editor_kind(self) -> str:
        """Map the playground type onto the editor's kind vocabulary."""
        return {
            "boolean": "bool",
            "number": "float",
            "text": "string",
            "select": "string",
            "array": "array",
            "object": "object",
        }.get(self.kind, "unknown")


# --------------------------------------------------------------------------- #
# catalog
# --------------------------------------------------------------------------- #


@dataclass
class ParameterCatalog:
    """Lookups over the parameters dataset and the generated read-sets."""

    fields: dict[str, FieldInfo] = field(default_factory=dict)
    descriptors: dict[str, DescriptorInfo] = field(default_factory=dict)

    def field(self, name: str) -> FieldInfo | None:
        return self.fields.get(name)

    def descriptor(self, name: str) -> DescriptorInfo | None:
        return self.descriptors.get(name)

    def field_names(self) -> list[str]:
        return sorted(self.fields)

    def descriptor_names(self) -> list[str]:
        return sorted(self.descriptors)


def build_catalog(
    observed: dict[str, dict[str, Any]] | None = None,
    pricing: dict[str, dict[str, Any]] | None = None,
    *,
    source: str = "parameters",
) -> ParameterCatalog:
    """Build the catalog describing ``observed``.

    ``source`` is the section the caller is editing -- ``"parameters"`` or
    ``"pricing"``. It only affects :attr:`FieldInfo.read_from`: a field is read
    from whichever file declares it, and the dialog should name that file so an
    edit lands where it takes effect.

    One pass over the dataset. The parameters file holds 12,595 entries and this
    takes a few seconds, so build it when the data loads and keep the result
    rather than rebuilding per dialog.
    """
    observed = observed or {}
    pricing = pricing or {}
    total = len(observed)

    # name -> observed shapes, so "kind" can be declared or inferred.
    shapes: dict[str, Counter] = {}
    present: Counter[str] = Counter()
    strings: dict[str, Counter[str]] = {}
    elements: dict[str, Counter[str]] = {}
    bounds: dict[str, list[float]] = {}
    bools: dict[str, Counter[bool]] = {}
    #: Fields whose value vocabulary blew past MAX_ENUM_VALUES. Their distinct
    #: values are worthless to us -- the editor will not offer a dropdown for
    #: them -- so recording stops, which is what keeps this pass affordable.
    #: ``base_model`` alone has 9,265 distinct strings across 12,595 models.
    overflowed: set[str] = set()

    for entry in observed.values():
        if not isinstance(entry, dict):
            continue
        for name, value in entry.items():
            present[name] += 1
            shape = shapes.get(name)
            if shape is None:
                shape = shapes[name] = Counter()
            shape[value_kind(value)] += 1
            if value is None:
                continue
            if isinstance(value, bool):
                counter = bools.get(name)
                if counter is None:
                    counter = bools[name] = Counter()
                counter[value] += 1
            elif isinstance(value, str):
                if name in overflowed:
                    continue
                counter = strings.get(name)
                if counter is None:
                    counter = strings[name] = Counter()
                counter[value] += 1
                if len(counter) > MAX_ENUM_VALUES:
                    overflowed.add(name)
            elif isinstance(value, (int, float)):
                slot = bounds.get(name)
                if slot is None:
                    bounds[name] = [float(value), float(value)]
                else:
                    if value < slot[0]:
                        slot[0] = value
                    if value > slot[1]:
                        slot[1] = value
            elif isinstance(value, list) and name not in overflowed:
                counter = elements.get(name)
                if counter is None:
                    counter = elements[name] = Counter()
                for item in value:
                    if not isinstance(item, str):
                        continue
                    counter[item] += 1
                    if len(counter) > MAX_ENUM_VALUES:
                        overflowed.add(name)
                        del elements[name]
                        break

    descriptors = _build_descriptors(observed) if source == "parameters" else {}
    read_set = params_read_set()
    pricing_set = pricing_read_set()

    fields: dict[str, FieldInfo] = {}
    for name in present:
        declared = _params_field_meta().get(name, {})
        kind = declared.get("kind") or _resolve_kind(shapes[name])
        origin = "go" if declared.get("kind") else "observed"

        if source == "parameters" and name in read_set:
            read_from = "parameters"
        elif is_pricing_read(name):
            read_from = "pricing"
        else:
            read_from = "none"

        doc = declared.get("doc", "")
        slot = bounds.get(name)
        if not doc:
            doc = _observed_doc(name, strings.get(name), bools.get(name), slot)

        # A vocabulary is only offered as a choice when Go consumes the field and
        # the observed values are few enough to be a real closed set. Otherwise
        # the text box stays, because presenting 9,265 base models as a dropdown
        # would be a worse editor than the one it replaced.
        closed = read_from != "none"
        enum_values: tuple[str, ...] = ()
        enum_elements: tuple[str, ...] = ()
        if closed and kind == "string" and name not in overflowed:
            values = sorted(strings.get(name, ()))
            if values:
                enum_values = tuple(values)
        if closed and kind == "array":
            values = sorted(elements.get(name, ()))
            if values:
                enum_elements = tuple(values)

        fields[name] = FieldInfo(
            name=name,
            kind=kind,
            kind_origin=origin,
            description=doc,
            enum_values=enum_values,
            enum_elements=enum_elements,
            default=_top(strings.get(name)) if kind == "string" else None,
            minimum=slot[0] if slot else None,
            maximum=slot[1] if slot else None,
            present_in=present[name],
            total=total,
            read_from=read_from,
            descriptor=descriptors.get(name),
        )

    return ParameterCatalog(fields=fields, descriptors=descriptors)


def _resolve_kind(observed: Counter) -> str:
    """Pick a kind from what the data shows.

    A field observed with one shape gets that shape. A field seen as both ``int``
    and ``float`` becomes ``float`` -- JSON has one number type and the
    distinction is Go's, not the file's. Anything genuinely mixed becomes
    ``unknown`` so the editor offers a JSON box rather than a widget that would
    reject or silently rewrite the other shape.
    """
    kinds = {k for k, count in observed.items() if k != "null" and count}
    if not kinds:
        return "unknown"
    if kinds == {"int", "float"}:
        return "float"
    if len(kinds) == 1:
        return next(iter(kinds))
    return "unknown"


def _top(counter: Counter | None) -> Any:
    """The most frequent value, or ``None`` when there is no clear one."""
    if not counter:
        return None
    value, count = counter.most_common(1)[0]
    total = sum(counter.values())
    # A field with hundreds of distinct values has no meaningful "default";
    # reporting the single most common one would be noise.
    if len(counter) > MAX_ENUM_VALUES or count * 2 <= total:
        return None
    return value


def _observed_doc(
    name: str,
    strings: Counter | None,
    bools: Counter | None,
    bounds: list | None,
) -> str:
    """Describe a field the Go source does not document, from the data itself."""
    if bools:
        true_count = bools.get(True, 0)
        false_count = bools.get(False, 0)
        if false_count:
            return f"A true/false flag. true on {true_count:,} models, false on {false_count:,}."
        return f"A true/false flag. true on every model that carries it ({true_count:,})."
    if bounds:
        return f"A number. Observed from {_bound(bounds[0])} to {_bound(bounds[1])}."
    if strings:
        return (
            f"Text. {len(strings):,} distinct value(s) across the datasheet, "
            "so the vocabulary is open-ended."
        )
    if name == "model_parameters":
        return (
            "The prompt-playground parameter descriptors for this model. Each entry's "
            "`id` is read by Bifrost to build the request-parameter allowlist; the other "
            "keys are passed through to the UI unchanged."
        )
    return "No description is recorded for this field."


def _build_descriptors(observed: dict[str, dict[str, Any]]) -> dict[str, DescriptorInfo]:
    """Merge every model's ``model_parameters`` descriptors into one view per id.

    Models describe the same parameter differently -- the same id can be a
    ``boolean`` on one provider and a ``select`` with options on another. Rather
    than pick a winner silently, the disagreements are recorded in
    :attr:`DescriptorInfo.conflicts` so the dialog can tell the user instead of
    showing a control that does not fit.
    """
    variants: dict[str, dict[str, Any]] = {}
    models: Counter[str] = Counter()

    for entry in observed.values():
        if not isinstance(entry, dict):
            continue
        raw = entry.get("model_parameters")
        if not isinstance(raw, list):
            continue
        for item in raw:
            if not isinstance(item, dict):
                continue
            param_id = item.get("id")
            if not isinstance(param_id, str) or not param_id:
                continue
            models[param_id] += 1
            # Explicit get-or-create, not setdefault with an inline literal: this
            # loop runs once per descriptor per model (~70,000 times), and
            # setdefault would build the six Counters and two lists on every
            # iteration even when the slot already exists.
            slot = variants.get(param_id)
            if slot is None:
                slot = variants[param_id] = {
                    "types": Counter(), "labels": Counter(), "help": Counter(),
                    "options": Counter(), "arrays": Counter(),
                    "mins": [], "maxs": [], "element_specs": [],
                    # Kept as the raw first value plus a "did anything differ"
                    # flag rather than a Counter keyed by json.dumps: encoding
                    # 47,000 defaults to find out there are only 92 of them was
                    # the single most expensive thing in this function.
                    "default": _MISSING,
                    "default_differs": False,
                }

            kind = item.get("type")
            if isinstance(kind, str):
                slot["types"][kind] += 1
            label = item.get("label")
            if isinstance(label, str) and label:
                slot["labels"][label] += 1
            help_text = item.get("helpText")
            if isinstance(help_text, str) and help_text:
                slot["help"][help_text] += 1
            options = item.get("options")
            if isinstance(options, list):
                for option in options:
                    if isinstance(option, dict) and isinstance(option.get("value"), str):
                        slot["options"][option["value"]] += 1
            if "default" in item:
                value = item["default"]
                if slot["default"] is _MISSING:
                    slot["default"] = value
                elif slot["default"] != value:
                    slot["default_differs"] = True
            array_spec = item.get("array")
            if isinstance(array_spec, dict):
                slot["arrays"][array_spec.get("type", "")] += 1
                slot["element_specs"].append(array_spec)
            bounds_spec = item.get("range")
            if isinstance(bounds_spec, dict):
                low = bounds_spec.get("min")
                high = bounds_spec.get("max")
                if isinstance(low, (int, float)) and not isinstance(low, bool):
                    slot["mins"].append(float(low))
                if isinstance(high, (int, float)) and not isinstance(high, bool):
                    slot["maxs"].append(float(high))

    out: dict[str, DescriptorInfo] = {}
    for param_id, slot in variants.items():
        conflicts: list[str] = []

        kind, kind_conflict = _majority(slot["types"], "type")
        if kind_conflict:
            conflicts.append(f"models disagree on its type ({', '.join(sorted(slot['types']))})")

        options = tuple(sorted(slot["options"]))
        if not options and kind == "select":
            conflicts.append("select type but no options recorded on any model")

        default = None if slot["default_differs"] else (
            None if slot["default"] is _MISSING else slot["default"]
        )
        array_kind = _majority(slot["arrays"], "array element type")[0] or ""
        if array_kind and kind == "array" and slot["arrays"][array_kind] * 2 <= sum(slot["arrays"].values()):
            conflicts.append("models disagree on its array element type")

        out[param_id] = DescriptorInfo(
            param_id=param_id,
            kind=kind or "unknown",
            label=_majority(slot["labels"], "label")[0] or "",
            description=_majority(slot["help"], "helpText")[0] or "",
            enum_values=options,
            default=default,
            # The union of every model's bounds, not the most common one: the
            # useful answer to "what range does temperature take?" is the widest
            # range any model declares, since a value outside a narrower model's
            # range is still valid for another provider.
            minimum=min(slot["mins"]) if slot["mins"] else None,
            maximum=max(slot["maxs"]) if slot["maxs"] else None,
            element_kind=_ARRAY_ELEMENT_KINDS.get(array_kind, array_kind),
            min_elements=_smallest_int(slot["element_specs"], "minElements"),
            max_elements=_largest_int(slot["element_specs"], "maxElements"),
            models=models[param_id],
            conflicts=tuple(conflicts),
        )
    return out


def _smallest_int(specs: list[Any], key: str) -> int | None:
    values = [v for v in (_as_int(s.get(key)) for s in specs if isinstance(s, dict)) if v is not None]
    return min(values) if values else None


def _largest_int(specs: list[Any], key: str) -> int | None:
    values = [v for v in (_as_int(s.get(key)) for s in specs if isinstance(s, dict)) if v is not None]
    return max(values) if values else None


def _as_int(value: Any) -> int | None:
    if isinstance(value, bool) or not isinstance(value, (int, float)):
        return None
    return int(value)


_MISSING = object()


#: ``model_parameters[].array.type`` spells its element kinds differently from
#: the Go read-set; map them onto the same vocabulary so the editor has one.
_ARRAY_ELEMENT_KINDS = {
    "text": "string",
    "string": "string",
    "number": "float",
    "boolean": "bool",
    "select": "string",
    "object": "object",
}


def _majority(counter: Counter, what: str) -> tuple[str, bool]:
    """Most common entry, plus whether it was actually a majority."""
    if not counter:
        return "", False
    value, count = counter.most_common(1)[0]
    return str(value), count * 2 <= sum(counter.values())


def _short(value: Any) -> str:
    text = json.dumps(value) if not isinstance(value, str) else value
    return text if len(text) <= 24 else text[:21] + "..."


def _bound(value: float) -> str:
    """Format an observed bound for a human: no exponent, no float noise."""
    from .format import number

    text = number(value)
    head, _, tail = text.partition(".")
    if tail in {"0", "0.0"}:
        tail = ""
    return f"{int(float(head)):,}" + (f".{tail}" if tail else "")