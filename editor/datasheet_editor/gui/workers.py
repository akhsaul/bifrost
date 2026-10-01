"""Background workers.

Loading a 20MB datasheet, merging 12,595 entries, and writing the result all
take long enough to freeze the UI, so each runs on a ``QThreadPool`` thread and
reports back through signals.

Loading reports discrete progress steps rather than an indeterminate spinner:
the work is four separable phases, and naming them ("Reading
model_parameters.json", "Comparing both datasets") tells the user what is
happening and whether the app is stuck.
"""

from __future__ import annotations

import json
import threading
from pathlib import Path
from typing import Any

from PySide6.QtCore import QObject, QRunnable, Signal, Slot

from ..dataset import DatasetKind, load_dataset, write_json_atomic
from ..fieldinfo import build_catalog
from ..merge import ParamArrayMode, PricingFieldPolicy, merge
from ..validate import find_cross_file_conflicts

PARAMETERS_FILENAME = "model_parameters.json"
PRICING_FILENAME = "model_pricing.json"

#: Phases performed off-thread by LoadWorker (reported as steps 0..3).
LOAD_STEPS = 4
#: Main-thread phase that builds the model list (step 4). Reported by the window
#: so the dialog cannot reach 100% while the list is still empty.
BUILD_STEP = 4
#: Dialog maximum. Reaching this value means the data is genuinely displayed.
TOTAL_STEPS = 5


#: Strong references to running workers.
#:
#: ``QThreadPool.start()`` does not keep the *Python* wrapper alive, so a worker
#: whose last Python reference is dropped gets collected mid-run. Its
#: ``QObject``-based signals go with it, and the worker thread then emits on a
#: deleted object ("Signal source has been deleted") -- reproducible by closing
#: the window while a 20MB file is still loading.
_LIVE_WORKERS: set["QRunnable"] = set()


def _keep_alive(worker: "QRunnable") -> None:
    _LIVE_WORKERS.add(worker)


def _release(worker: "QRunnable") -> None:
    _LIVE_WORKERS.discard(worker)


def _emit(signal: Signal, *args: Any) -> None:
    """Emit, tolerating a receiver (window) that was destroyed mid-flight.

    Closing the window during a load is legitimate; the worker must not raise on
    its way out just because nobody is listening any more.
    """
    try:
        signal.emit(*args)
    except RuntimeError:
        pass


class _Signals(QObject):
    finished = Signal(object)
    failed = Signal(str)


class LoadSignals(QObject):
    """Progress and completion signals for :class:`LoadWorker`."""

    #: label, current step, total steps
    progress = Signal(str, int, int)
    finished = Signal(object)
    failed = Signal(str)
    cancelled = Signal()


class LoadWorker(QRunnable):
    """Load both datasets, the overlay, and cross-file conflicts off-thread.

    Cancellation is cooperative and checked between phases: a phase already inside
    ``json.load`` cannot be interrupted, but stopping before the expensive
    conflict scan is still worth it.
    """

    def __init__(
        self,
        parameters_path: str | Path | None,
        pricing_path: str | Path | None,
        custom_path: str | Path | None,
        *,
        cancel_event: threading.Event | None = None,
    ) -> None:
        super().__init__()
        self.signals = LoadSignals()
        self._parameters_path = Path(parameters_path) if parameters_path else None
        self._pricing_path = Path(pricing_path) if pricing_path else None
        self._custom_path = Path(custom_path) if custom_path else None
        self._cancel = cancel_event or threading.Event()
        _keep_alive(self)

    def cancel(self) -> None:
        """Ask the worker to stop at the next phase boundary."""
        self._cancel.set()

    def _cancelled(self) -> bool:
        return self._cancel.is_set()

    def _abort(self) -> None:
        _emit(self.signals.cancelled)

    @Slot()
    def run(self) -> None:
        try:
            total = TOTAL_STEPS

            _emit(
                self.signals.progress,
                f"Reading {_display(self._parameters_path, PARAMETERS_FILENAME)}",
                0,
                total,
            )
            if self._parameters_path is None:
                raise FileNotFoundError("no parameters file selected")
            params = load_dataset(self._parameters_path, DatasetKind.PARAMETERS)
            if self._cancelled():
                self._abort()
                return

            _emit(
                self.signals.progress,
                f"Reading {_display(self._pricing_path, PRICING_FILENAME)}",
                1,
                total,
            )
            if self._pricing_path is None:
                raise FileNotFoundError("no pricing file selected")
            pricing = load_dataset(self._pricing_path, DatasetKind.PRICING)
            if self._cancelled():
                self._abort()
                return

            _emit(self.signals.progress, "Reading custom overlay", 2, total)
            overlay: dict[str, dict[str, Any]] = {}
            if self._custom_path and self._custom_path.exists():
                with self._custom_path.open(encoding="utf-8") as fh:
                    loaded = json.load(fh)
                if isinstance(loaded, dict):
                    overlay = loaded
            if self._cancelled():
                self._abort()
                return

            _emit(self.signals.progress, "Comparing both datasets", 3, total)
            conflicts = find_cross_file_conflicts(params.data, pricing.data)
            grouped: dict[str, list[str]] = {}
            for conflict in conflicts:
                grouped.setdefault(conflict["model"], []).append(conflict["field"])

            # The field catalogs scan every entry, which is a couple of seconds
            # over 12,595 models. Built here so opening "Add Field" later is
            # instant instead of freezing the window at the moment the user is
            # trying to read a description.
            param_catalog = build_catalog(params.data, pricing.data)
            pricing_catalog = build_catalog(pricing.data, pricing.data, source="pricing")
            if self._cancelled():
                self._abort()
                return

            _emit(
                self.signals.finished,
                {
                    "parameters": params,
                    "pricing": pricing,
                    "overlay": overlay,
                    "conflicts": grouped,
                    "conflict_count": len(conflicts),
                    "param_catalog": param_catalog,
                    "pricing_catalog": pricing_catalog,
                },
            )
        except Exception as exc:  # surfaced in the UI, not swallowed
            _emit(self.signals.failed, str(exc))
        finally:
            _release(self)


def _display(path: Path | None, fallback: str) -> str:
    return path.name if path else fallback


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
        _keep_alive(self)

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
            _emit(self.signals.finished, result)
        except Exception as exc:
            _emit(self.signals.failed, str(exc))
        finally:
            _release(self)


class SaveWorker(QRunnable):
    """Write both merged output files off-thread, then re-read to verify."""

    def __init__(
        self,
        result: Any,
        output_dir: str | Path,
        *,
        indent: int | None = None,
        sort_keys: bool = False,
    ) -> None:
        super().__init__()
        self.signals = _Signals()
        self._result = result
        self._output_dir = Path(output_dir)
        self._indent = indent
        self._sort_keys = sort_keys
        _keep_alive(self)

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
            _emit(
                self.signals.finished,
                {"parameters": str(params_out), "pricing": str(pricing_out), "problems": problems},
            )
        except Exception as exc:
            _emit(self.signals.failed, str(exc))
        finally:
            _release(self)