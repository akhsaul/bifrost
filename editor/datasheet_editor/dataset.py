"""Loading and saving the datasheet JSON files.

Both files share one shape: a JSON object keyed by model ID, each value an
object of fields. ``model_parameters.json`` is 20MB / 12,595 entries;
``model_pricing.json`` is 2.5MB / 4,763. Both are written compact (no
whitespace) in the upstream datasheet, so that is the default here too.

Key order is preserved throughout -- the originals are ordered, and reordering
them would produce a needlessly large diff against upstream.
"""

from __future__ import annotations

import json
from dataclasses import dataclass, field
from enum import Enum
from pathlib import Path
from typing import Any

#: Compact separators matching the upstream datasheet files.
_COMPACT = (",", ":")


class DatasetKind(str, Enum):
    """Which of the two datasheets a file holds."""

    PARAMETERS = "parameters"
    PRICING = "pricing"


@dataclass
class Dataset:
    """A loaded datasheet plus the metadata needed to write it back faithfully."""

    kind: DatasetKind
    path: Path | None
    data: dict[str, dict[str, Any]] = field(default_factory=dict)
    #: Fields present anywhere in the file, used for GUI column suggestions.
    known_fields: frozenset[str] = frozenset()

    def __len__(self) -> int:
        return len(self.data)

    def __contains__(self, model: str) -> bool:
        return model in self.data

    def entry(self, model: str) -> dict[str, Any] | None:
        return self.data.get(model)

    def providers(self) -> list[str]:
        """Distinct ``provider`` values, sorted, for the GUI filter."""
        return sorted({v["provider"] for v in self.data.values() if isinstance(v.get("provider"), str)})

    def modes(self) -> list[str]:
        """Distinct ``mode`` values, sorted, for the GUI filter."""
        return sorted({v["mode"] for v in self.data.values() if isinstance(v.get("mode"), str)})


class DataSetError(ValueError):
    """Raised when a datasheet file is malformed."""


def load_dataset(path: str | Path, kind: DatasetKind | None = None) -> Dataset:
    """Load one datasheet file.

    ``kind`` is inferred from the filename when not given (``*pricing*`` ->
    pricing, ``*param*`` -> parameters); it defaults to parameters, since an
    unrecognized name is more likely a params file in this workflow.
    """
    p = Path(path)
    if not p.exists():
        raise DataSetError(f"file not found: {p}")

    try:
        with p.open(encoding="utf-8") as fh:
            raw = json.load(fh)
    except json.JSONDecodeError as exc:
        raise DataSetError(f"{p} is not valid JSON: {exc}") from exc

    if not isinstance(raw, dict):
        raise DataSetError(f"{p}: expected a JSON object keyed by model ID, got {type(raw).__name__}")

    resolved = kind or _infer_kind(p)

    data: dict[str, dict[str, Any]] = {}
    known: set[str] = set()
    for model, entry in raw.items():
        if not isinstance(entry, dict):
            raise DataSetError(
                f"{p}: entry {model!r} must be a JSON object, got {type(entry).__name__}"
            )
        data[model] = entry
        known.update(entry)

    return Dataset(kind=resolved, path=p, data=data, known_fields=frozenset(known))


def _infer_kind(path: Path) -> DatasetKind:
    stem = path.name.lower()
    if "pricing" in stem:
        return DatasetKind.PRICING
    return DatasetKind.PARAMETERS


def save_dataset(
    data: dict[str, Any],
    path: str | Path,
    *,
    indent: int | None = None,
    sort_keys: bool = False,
) -> Path:
    """Write a datasheet, defaulting to the compact upstream format."""
    return write_json_atomic(data, path, indent=indent, sort_keys=sort_keys)


def write_json_atomic(data: Any, path: str | Path, *, indent: int | None = None, sort_keys: bool = False) -> Path:
    """Write JSON via a temp file + rename, so a crash never truncates the output.

    ``mkstemp`` creates the file 0600; these are data files meant to be read by
    other processes, so the mode is reset to the normal ``0666 & ~umask``.
    """
    import os
    import stat
    import tempfile

    out = Path(path)
    out.parent.mkdir(parents=True, exist_ok=True)
    fd, tmp_name = tempfile.mkstemp(dir=str(out.parent), prefix=f".{out.name}.", suffix=".tmp")
    tmp = Path(tmp_name)
    try:
        with os.fdopen(fd, "w", encoding="utf-8") as fh:
            if indent:
                json.dump(data, fh, indent=indent, sort_keys=sort_keys, ensure_ascii=False)
                fh.write("\n")
            else:
                json.dump(data, fh, separators=_COMPACT, sort_keys=sort_keys, ensure_ascii=False)
            fh.flush()
            os.fsync(fh.fileno())
        umask = os.umask(0)
        os.umask(umask)
        os.chmod(tmp, 0o666 & ~umask)
        tmp.replace(out)
    except BaseException:
        tmp.unlink(missing_ok=True)
        raise
    return out
