"""Validation for datasheet files and custom overlays.

Two severities:

- **errors** abort the merge. They mean the overlay is structurally wrong and the
  output would be silently incorrect.
- **warnings** are advisory. They are reported and, for cross-file conflicts, are
  the main output -- the tool surfaces disagreements rather than picking a winner.
"""

from __future__ import annotations

import json
from dataclasses import dataclass, field
from pathlib import Path
from typing import Any, Literal

from .fields import (
    KNOWN_MODES,
    KNOWN_PARAM_TYPES,
    is_capability_field,
    is_cost_field,
    is_pricing_read,
    looks_like_cost,
    value_kind,
)

Severity = Literal["error", "warning", "info"]

#: Fields whose values must be non-negative numbers when present.
_COST_SUFFIXES = ("cost", "price")
_INT_FIELDS = ("max_input_tokens", "max_output_tokens", "max_tokens", "context_length")
#: Capability fields whose values must be booleans.
_BOOL_PREFIX = "supports_"


@dataclass
class Finding:
    severity: Severity
    model: str
    field_name: str
    message: str

    def __str__(self) -> str:
        where = f"{self.model}.{self.field_name}" if self.field_name else self.model
        return f"[{self.severity}] {where}: {self.message}"


@dataclass
class ValidationReport:
    findings: list[Finding] = field(default_factory=list)

    def add(self, severity: Severity, model: str, field_name: str, message: str) -> None:
        self.findings.append(Finding(severity, model, field_name, message))

    def extend(self, other: ValidationReport) -> None:
        self.findings.extend(other.findings)

    def errors(self) -> list[Finding]:
        return [f for f in self.findings if f.severity == "error"]

    def warnings(self) -> list[Finding]:
        return [f for f in self.findings if f.severity == "warning"]

    @property
    def ok(self) -> bool:
        return not self.errors()

    def __iter__(self):
        return iter(self.findings)

    def __bool__(self) -> bool:
        # A report with only warnings still means "usable, but look at this".
        return bool(self.findings)

    def __len__(self) -> int:
        return len(self.findings)

    def render(self, limit: int | None = None) -> str:
        if not self.findings:
            return "no findings"
        order = {"error": 0, "warning": 1, "info": 2}
        rows = sorted(self.findings, key=lambda f: (order[f.severity], f.model, f.field_name))
        shown = rows if limit is None else rows[:limit]
        text = "\n".join(str(f) for f in shown)
        if limit is not None and len(rows) > limit:
            text += f"\n... and {len(rows) - limit} more"
        return text


def validate_entry(model: str, entry: dict[str, Any], *, section: str) -> ValidationReport:
    """Type-check one model entry's fields."""
    report = ValidationReport()

    mode = entry.get("mode")
    if mode is not None:
        if not isinstance(mode, str):
            report.add("error", model, "mode", f"must be a string, got {value_kind(mode)}")
        elif mode not in KNOWN_MODES:
            report.add("warning", model, "mode", f"unknown mode {mode!r}")

    for name, value in entry.items():
        kind = value_kind(value)

        if name in _INT_FIELDS and kind not in {"int", "null"}:
            report.add("warning", model, name, f"expected an integer, got {kind}")
        elif name in _INT_FIELDS and isinstance(value, int) and not isinstance(value, bool) and value < 0:
            report.add("error", model, name, "must not be negative")

        if _is_cost_name(name) and kind not in {"float", "int", "null", "object"}:
            report.add("error", model, name, f"cost field must be a number, got {kind}")
        elif _is_cost_name(name) and isinstance(value, (int, float)) and not isinstance(value, bool):
            if value < 0:
                report.add("error", model, name, "cost must not be negative")

        if name.startswith(_BOOL_PREFIX) and kind not in {"bool", "null"}:
            report.add("warning", model, name, f"capability flag should be a boolean, got {kind}")

        if section == "parameters" and looks_like_cost(name) and is_cost_field(name):
            report.add("error", model, name, "cost field in the parameters dataset")

    if section == "parameters":
        report.extend(_validate_param_array(model, entry.get("model_parameters")))

    return report


def _is_cost_name(name: str) -> bool:
    if is_capability_field(name):
        return False
    return any(s in name for s in _COST_SUFFIXES)


def _validate_param_array(model: str, value: Any) -> ValidationReport:
    report = ValidationReport()
    if value is None:
        return report
    if not isinstance(value, list):
        report.add("error", model, "model_parameters", f"expected an array, got {value_kind(value)}")
        return report

    seen: set[str] = set()
    for i, item in enumerate(value):
        where = f"model_parameters[{i}]"
        if not isinstance(item, dict):
            report.add("error", model, where, f"expected an object, got {value_kind(item)}")
            continue
        item_id = item.get("id")
        if not isinstance(item_id, str):
            report.add("error", model, where, "missing a string 'id'")
        elif item_id in seen:
            report.add("error", model, where, f"duplicate parameter id {item_id!r}")
        else:
            seen.add(item_id)

        ptype = item.get("type")
        if ptype is not None and not isinstance(ptype, str):
            report.add("error", model, f"{where}.type", f"must be a string, got {value_kind(ptype)}")
        elif isinstance(ptype, str) and ptype not in KNOWN_PARAM_TYPES:
            report.add("warning", model, f"{where}.type", f"unrecognized parameter type {ptype!r}")

    return report


def validate_overlay(
    overlay: dict[str, dict[str, Any]],
    parameters: dict[str, dict[str, Any]],
    pricing: dict[str, dict[str, Any]],
) -> ValidationReport:
    """Check a custom overlay against both originals."""
    report = ValidationReport()

    for model, sections in overlay.items():
        if not isinstance(sections, dict):
            report.add("error", model, "", "entry must be an object")
            continue

        unknown = set(sections) - {"parameters", "pricing"}
        for name in sorted(unknown):
            report.add(
                "error",
                model,
                name,
                "unknown section (expected 'parameters' or 'pricing')",
            )

        if "parameters" in sections or "pricing" in sections:
            if model not in parameters and model not in pricing:
                report.add("warning", model, "", "new model: present in neither original")

        params_section = sections.get("parameters")
        if isinstance(params_section, dict):
            report.extend(validate_entry(model, params_section, section="parameters"))
        elif params_section is not None:
            report.add("error", model, "parameters", f"must be an object, got {value_kind(params_section)}")

        pricing_section = sections.get("pricing")
        if isinstance(pricing_section, dict):
            report.extend(validate_entry(model, pricing_section, section="pricing"))
        elif pricing_section is not None:
            report.add("error", model, "pricing", f"must be an object, got {value_kind(pricing_section)}")

    return report


def find_cross_file_conflicts(
    parameters: dict[str, dict[str, Any]],
    pricing: dict[str, dict[str, Any]],
    *,
    models: set[str] | None = None,
) -> list[dict[str, Any]]:
    """Report capability fields where the two datasheets disagree.

    The same facts are maintained twice: once in the params file (what the UI
    builds forms from) and once in the pricing file (what
    ``datasheet.GetCapabilityEntry`` actually reads). They currently disagree on
    ~1,397 field values. Neither file is treated as authoritative here -- the
    tool reports both so a human decides.
    """
    conflicts: list[dict[str, Any]] = []
    keys = models if models is not None else (set(parameters) & set(pricing))
    for model in sorted(keys):
        a = parameters.get(model)
        b = pricing.get(model)
        if not isinstance(a, dict) or not isinstance(b, dict):
            continue
        for name in sorted(set(a) & set(b)):
            if name == "model_parameters":
                continue
            if a[name] != b[name]:
                conflicts.append({"model": model, "field": name, "parameters": a[name], "pricing": b[name]})
    return conflicts


def validate_file(path: str | Path, section: str) -> ValidationReport:
    """Validate a whole datasheet file."""
    from .dataset import load_dataset, DatasetKind

    kind = DatasetKind.PRICING if section == "pricing" else DatasetKind.PARAMETERS
    dataset = load_dataset(path, kind)
    report = ValidationReport()
    for model, entry in dataset.data.items():
        report.extend(validate_entry(model, entry, section=section))
    report.add(
        "info",
        "",
        "",
        f"{path}: {len(dataset)} models, {len(dataset.known_fields)} distinct fields",
    )
    return report


def format_conflicts(conflicts: list[dict[str, Any]], limit: int = 50) -> str:
    """Render cross-file conflicts for the terminal."""
    if not conflicts:
        return "no cross-file disagreements"
    lines = [f"{len(conflicts)} field(s) disagree between the two datasheets:"]
    for c in conflicts[:limit]:
        lines.append(f"  {c['model']}.{c['field']}: parameters={c['parameters']!r} pricing={c['pricing']!r}")
    if len(conflicts) > limit:
        lines.append(f"  ... and {len(conflicts) - limit} more")
    return "\n".join(lines)


def summarize_unknown_pricing_fields(data: dict[str, dict[str, Any]]) -> dict[str, int]:
    """Count entries carrying pricing fields Bifrost's ``Entry`` struct ignores."""
    counts: dict[str, int] = {}
    for entry in data.values():
        for name in entry:
            if not is_pricing_read(name):
                counts[name] = counts.get(name, 0) + 1
    return dict(sorted(counts.items(), key=lambda kv: -kv[1]))
