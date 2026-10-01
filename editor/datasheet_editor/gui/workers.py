"""Background workers.

Loading a 20MB datasheet, merging 12,595 entries, and writing the result all
take long enough to freeze the UI, so each runs on a ``QThreadPool`` thread and
reports back through signals.
"""

from __future__ import annotations

from pathlib import Path
from typing import Any

from PySide6.QtCore import QObject, QRunnable, Signal, Slot

from ..dataset import DatasetKind, load_dataset, write_json_atomic
from ..merge import ParamArrayMode, PricingFieldPolicy, merge
from ..validate import find_cross_file_conflicts

PARAMETERS_FILENAME = "model_parameters.json"
PRICING_FILENAME = "model_pricing.json"


class _Signals(QObject):
    finished = Signal(object)
    failed = Signal(str)


class LoadWorker(QRunnable):
    """Load both datasets, the overlay, and cross-file conflicts off-thread."""

    def __init__(
        self,
        parameters_path: str | Path,
        pricing_path: str | Path,
        custom_path: str | Path | None,
    ) -> None:
        super().__init__()
        self.signals = _Signals()
        self._parameters_path = Path(parameters_path)
        self._pricing_path = Path(pricing_path)
        self._custom_path = Path(custom_path) if custom_path else None

    @Slot()
    def run(self) -> None:
        try:
            params = load_dataset(self._parameters_path, DatasetKind.PARAMETERS)
            pricing = load_dataset(self._pricing_path, DatasetKind.PRICING)
            overlay = {}
            if self._custom_path and self._custom_path.exists():
                import json

                with self._custom_path.open(encoding="utf-8") as fh:
                    overlay = json.load(fh)
            conflicts = find_cross_file_conflicts(params.data, pricing.data)
            grouped: dict[str, list[str]] = {}
            for conflict in conflicts:
                grouped.setdefault(conflict["model"], []).append(conflict["field"])
            self.signals.finished.emit(
                {
                    "parameters": params,
                    "pricing": pricing,
                    "overlay": overlay,
                    "conflicts": grouped,
                    "conflict_count": len(conflicts),
                }
            )
        except Exception as exc:  # surfaced in the UI, not swallowed
            self.signals.failed.emit(str(exc))


class MergeWorker(QRunnable):
    """Merge the overlay over both originals off-thread."""

    def __init__(
        self,
        parameters: dict[str, dict[str, Any]],
        pricing: dict[str, dict[str, Any]],
        overlay: dict[str, dict[str, Any]],
        *,
        param_array_mode: ParamArrayMode = ParamArrayMode.MERGE,
        pricing_fields: PricingFieldPolicy = PricingFieldPolicy.PRESERVE,
    ) -> None:
        super().__init__()
        self.signals = _Signals()
        self._parameters = parameters
        self._pricing = pricing
        self._overlay = overlay
        self._param_array_mode = param_array_mode
        self._pricing_fields = pricing_fields

    @Slot()
    def run(self) -> None:
        try:
            result = merge(
                self._parameters,
                self._pricing,
                self._overlay,
                param_array_mode=self._param_array_mode,
                pricing_fields=self._pricing_fields,
            )
            self.signals.finished.emit(result)
        except Exception as exc:
            self.signals.failed.emit(str(exc))


class SaveWorker(QRunnable):
    """Write both merged output files off-thread, then re-read to verify."""

    def __init__(self, result: Any, output_dir: str | Path, *, indent: int | None = None,
                 sort_keys: bool = False) -> None:
        super().__init__()
        self.signals = _Signals()
        self._result = result
        self._output_dir = Path(output_dir)
        self._indent = indent
        self._sort_keys = sort_keys

    @Slot()
    def run(self) -> None:
        try:
            params_out = self._output_dir / PARAMETERS_FILENAME
            pricing_out = self._output_dir / PRICING_FILENAME
            write_json_atomic(self._result.parameters, params_out, indent=self._indent,
                              sort_keys=self._sort_keys)
            write_json_atomic(self._result.pricing, pricing_out, indent=self._indent,
                              sort_keys=self._sort_keys)
            problems: list[str] = []
            for path, expected, label in (
                (params_out, self._result.parameters, PARAMETERS_FILENAME),
                (pricing_out, self._result.pricing, PRICING_FILENAME),
            ):
                try:
                    actual = load_dataset(path).data
                except Exception as exc:
                    problems.append(f"{label} could not be re-read: {exc}")
                    continue
                if len(actual) != len(expected):
                    problems.append(f"{label}: wrote {len(actual)} models, expected {len(expected)}")
            self.signals.finished.emit(
                {"parameters": str(params_out), "pricing": str(pricing_out), "problems": problems}
            )
        except Exception as exc:
            self.signals.failed.emit(str(exc))
