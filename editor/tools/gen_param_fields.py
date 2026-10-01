#!/usr/bin/env python3
"""Generate the Bifrost parameters read-set from the Go capabilities struct.

``model_parameters.json`` is deserialized into ``schemas.ModelCapabilities``
(``core/schemas/modelcapabilities.go``). Every field that struct does not declare
is dropped when Go parses the row. This script parses the struct and writes a
snapshot of its JSON tags, doc comments and Go types, so the editor can tell the
user whether a field they are about to add is one Bifrost actually reads.

The doc comments are the point. The Go source is the only place in the repo that
says what ``reasoning_effort_levels`` *means*; the JSON files do not. Carrying
the prose across is what lets the Add Field dialog describe a field instead of
leaving the user to guess.

The snapshot is checked in and must be regenerated whenever the struct changes::

    python3 tools/gen_param_fields.py

Usage:
    python3 tools/gen_param_fields.py [--check] [--source PATH] [--out PATH]
"""

from __future__ import annotations

import argparse
import json
import re
import sys
from pathlib import Path

from gen_pricing_fields import _FIELD_RE, _struct_body, display_path as _display_path, repo_root

#: The struct ``model_parameters.json`` is parsed into.
READ_SET_STRUCT = "ModelCapabilities"

#: The struct describing one ``model_parameters[]`` array element.
DESCRIPTOR_STRUCT = "ModelParameterDescriptor"

ROOT = repo_root()

DEFAULT_SOURCE = ROOT / "core" / "schemas" / "modelcapabilities.go"
DEFAULT_OUT = ROOT / "editor" / "datasheet_editor" / "param_fields.json"

# Go type -> (kind, element_kind). Mirrors fields.py's FieldKind vocabulary so
# the GUI has one set of editor kinds to reason about. Anything unrecognised
# becomes "unknown" and the GUI falls back to a JSON text box rather than guessing.
_SCALAR_KINDS = {
    "float64": "float",
    "float32": "float",
    "int": "int",
    "int64": "int",
    "int32": "int",
    "bool": "bool",
    "string": "string",
}

#: Map value type -> kind of each value. ``map[string]T`` is an object whose
#: values are all of kind T.
_MAP_VALUE_KINDS = {
    "string": "string",
    "bool": "bool",
    "int": "int",
    "[]string": "array",
}


def _go_kind(go_type: str) -> tuple[str, str]:
    """Return ``(kind, element_kind)`` for a Go type as it appears on a struct field."""
    base = go_type.lstrip("*")

    if base in _SCALAR_KINDS:
        return _SCALAR_KINDS[base], ""

    if base.startswith("[]"):
        inner = base[2:]
        if inner in _SCALAR_KINDS:
            return "array", _SCALAR_KINDS[inner]
        # []SomeStruct -- an array of objects. Element kind "object" is enough
        # for the editor; the members come from the JSON, not from Go.
        return "array", "object"

    map_match = re.match(r"map\[string\](.+)$", base)
    if map_match:
        value_kind = _MAP_VALUE_KINDS.get(map_match.group(1), "object")
        return "object", value_kind

    if base in _SCALAR_KINDS or base[:1].isupper():
        # A named struct (BudgetControl, BedrockReasoningShape, ...) is an object.
        return ("object", "") if base[:1].isupper() else ("unknown", "")

    return "unknown", ""


def _doc_comment(body: list[str], index: int) -> str:
    """Collect the ``//`` comment block immediately above ``body[index]``.

    Returns the comment with its markers and leading asterisks stripped and its
    lines joined, which is what a reader wants in a tooltip. Blank ``//`` lines
    become paragraph breaks so multi-paragraph comments stay readable.
    """
    collected: list[str] = []
    for line in reversed(body[:index]):
        stripped = line.strip()
        if not stripped.startswith("//"):
            break
        text = stripped[2:].strip()
        if text.startswith("---"):  # section ruler, not prose
            return ""
        collected.append(text)
    if not collected:
        return ""

    paragraphs: list[list[str]] = [[]]
    for text in reversed(collected):
        if text:
            paragraphs[-1].append(text)
        else:
            paragraphs.append([])
    return "\n\n".join(" ".join(p) for p in paragraphs if p)


def parse_struct(lines: list[str], name: str) -> dict[str, dict[str, str]]:
    """Map each JSON tag on ``name`` to its Go metadata plus doc comment."""
    found = _struct_body(lines, name)
    if found is None:
        raise SystemExit(f"error: struct {name!r} not found in source")
    body, lineno = found

    fields: dict[str, dict[str, str]] = {}
    for offset, line in enumerate(body):
        match = _FIELD_RE.match(line)
        if not match:
            continue
        tag_match = re.search(r'json:"([A-Za-z0-9_]+)', match.group("tag"))
        if not tag_match:
            continue
        tag = tag_match.group(1)
        if tag == "-":
            continue
        go_type = match.group("type").strip()
        kind, element_kind = _go_kind(go_type)
        fields[tag] = {
            "go_name": match.group("name"),
            "go_type": go_type,
            "kind": kind,
            "element_kind": element_kind,
            "doc": _doc_comment(body, offset),
            "line": str(lineno + offset + 1),
        }
    return dict(sorted(fields.items()))




def build(source: Path) -> dict[str, object]:
    lines = source.read_text(encoding="utf-8").split("\n")
    fields = parse_struct(lines, READ_SET_STRUCT)
    descriptors = parse_struct(lines, DESCRIPTOR_STRUCT)

    return {
        "_comment": (
            "Generated by tools/gen_param_fields.py from schemas.ModelCapabilities. "
            "Do not edit by hand -- regenerate after changing modelcapabilities.go."
        ),
        "generated_from": _display_path(source),
        "struct": READ_SET_STRUCT,
        "count": len(fields),
        "fields": fields,
        "descriptor_struct": DESCRIPTOR_STRUCT,
        "descriptor_count": len(descriptors),
        "descriptor_fields": descriptors,
    }


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__.split("\n")[0])
    parser.add_argument("--source", type=Path, default=DEFAULT_SOURCE, help="path to modelcapabilities.go")
    parser.add_argument("--out", type=Path, default=DEFAULT_OUT, help="snapshot output path")
    parser.add_argument("--check", action="store_true", help="verify the snapshot is current; do not write")
    args = parser.parse_args(argv)

    if not args.source.exists():
        print(f"error: source not found: {args.source}", file=sys.stderr)
        return 2

    payload = build(args.source)
    rendered = json.dumps(payload, indent=2, sort_keys=False) + "\n"

    if args.check:
        if not args.out.exists():
            print(f"error: snapshot missing: {args.out}", file=sys.stderr)
            return 1
        if args.out.read_text(encoding="utf-8") != rendered:
            print(f"error: {args.out} is stale; rerun tools/gen_param_fields.py", file=sys.stderr)
            return 1
        print(f"ok: {args.out} is current ({payload['count']} fields)")
        return 0

    args.out.parent.mkdir(parents=True, exist_ok=True)
    args.out.write_text(rendered, encoding="utf-8")

    fields = payload["fields"]
    documented = sum(1 for meta in fields.values() if meta["doc"])  # type: ignore[union-attr]
    kinds: dict[str, int] = {}
    for meta in fields.values():  # type: ignore[union-attr]
        kinds[meta["kind"]] = kinds.get(meta["kind"], 0) + 1
    print(
        f"wrote {args.out} ({payload['count']} fields, {documented} documented: "
        + ", ".join(f"{k}={v}" for k, v in sorted(kinds.items()))
        + ")"
    )
    return 0


if __name__ == "__main__":
    raise SystemExit(main())