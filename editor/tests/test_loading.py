"""Tests for file loading: the three Load buttons, progress, and cancellation.

The editor deliberately does not auto-discover files, so these pin that the
starting state is empty, that a partial load does not fire, and that cancelling
leaves nothing half-loaded.
"""

from __future__ import annotations

import json
import threading
from pathlib import Path

import pytest

pytest.importorskip("PySide6", reason="PySide6 not installed")

from PySide6.QtCore import QEventLoop, QThreadPool, QTimer  # noqa: E402
from PySide6.QtWidgets import QApplication  # noqa: E402

from datasheet_editor.gui.main_window import MainWindow  # noqa: E402
from datasheet_editor.gui.workers import LOAD_STEPS, LoadSignals, LoadWorker  # noqa: E402

REPO_EDITOR = Path(__file__).resolve().parents[1]
PARAMS = REPO_EDITOR / "model_parameters.json"
PRICING = REPO_EDITOR / "model_pricing.json"
PARAMS_BEAUTY = REPO_EDITOR / "model_parameters_beauty.json"
PRICING_BEAUTY = REPO_EDITOR / "model_pricing_beauty.json"

#: Any real copy of the two originals will do; the compact ones are not tracked.
def _existing() -> tuple[Path, Path]:
    for params, pricing in ((PARAMS, PRICING), (PARAMS_BEAUTY, PRICING_BEAUTY)):
        if params.exists() and pricing.exists():
            return params, pricing
    pytest.skip("no datasheet files present")


needs_files = pytest.mark.skipif(
    not (
        (PARAMS.exists() and PRICING.exists())
        or (PARAMS_BEAUTY.exists() and PRICING_BEAUTY.exists())
    ),
    reason="datasheet files not present",
)


@pytest.fixture(scope="session")
def app():
    return QApplication.instance() or QApplication([])


@pytest.fixture
def window(app):
    return MainWindow()


def _run_worker(worker: LoadWorker, timeout_ms: int = 15000) -> dict:
    """Run a LoadWorker to completion on the global pool and collect the outcome."""
    loop = QEventLoop()
    out: dict = {}

    worker.signals.finished.connect(lambda p: (out.update(payload=p, kind="finished"), loop.quit()))
    worker.signals.failed.connect(lambda m: (out.update(message=m, kind="failed"), loop.quit()))
    worker.signals.cancelled.connect(lambda: (out.update(kind="cancelled"), loop.quit()))

    QTimer.singleShot(0, lambda: QThreadPool.globalInstance().start(worker))
    QTimer.singleShot(timeout_ms, loop.quit)
    loop.exec()
    return out


# --------------------------------------------------------------------------- #
# worker phases and cancellation
# --------------------------------------------------------------------------- #


@needs_files
def test_worker_reports_every_phase_in_order(app):
    params, pricing = _existing()
    steps: list[tuple[int, int, str]] = []
    worker = LoadWorker(params, pricing, None)
    worker.signals.progress.connect(lambda l, v, t: steps.append((v, t, l)))

    out = _run_worker(worker)
    assert out.get("kind") == "finished", out
    assert [v for v, _, _ in steps] == list(range(LOAD_STEPS + 1))
    assert all(t == LOAD_STEPS for _, t, _ in steps)
    # Each phase names the file it is working on.
    assert params.name in steps[0][2]
    assert pricing.name in steps[1][2]


@needs_files
def test_worker_returns_both_datasets_and_conflicts(app):
    params, pricing = _existing()
    out = _run_worker(LoadWorker(params, pricing, None))
    assert out["kind"] == "finished"
    payload = out["payload"]
    assert len(payload["parameters"]) > 1000
    assert len(payload["pricing"]) > 100
    assert isinstance(payload["conflicts"], dict)
    assert payload["overlay"] == {}


@needs_files
def test_worker_reads_the_custom_overlay(app, tmp_path):
    params, pricing = _existing()
    custom = tmp_path / "custom_model_metadata.json"
    custom.write_text(json.dumps({"m": {"pricing": {"input_cost_per_token": 1.0}}}))
    out = _run_worker(LoadWorker(params, pricing, custom))
    assert out["payload"]["overlay"] == {"m": {"pricing": {"input_cost_per_token": 1.0}}}


def test_worker_tolerates_a_missing_custom_overlay(app, tmp_path):
    """A chosen-but-absent custom file is not an error; you may have none yet."""
    params, pricing = _existing() if (PARAMS.exists() or PARAMS_BEAUTY.exists()) else (None, None)
    if params is None:
        pytest.skip("no datasheet files present")
    out = _run_worker(LoadWorker(params, pricing, tmp_path / "nope.json"))
    assert out["kind"] == "finished"
    assert out["payload"]["overlay"] == {}


def test_cancelled_worker_stops_before_loading(app):
    """A pre-cancelled worker must never report finished."""
    if not (PARAMS.exists() or PARAMS_BEAUTY.exists()):
        pytest.skip("datasheet files not present")
    params = PARAMS if PARAMS.exists() else PARAMS_BEAUTY
    pricing = PRICING if PRICING.exists() else PRICING_BEAUTY

    event = threading.Event()
    event.set()
    worker = LoadWorker(params, pricing, None, cancel_event=event)
    out = _run_worker(worker)
    assert out.get("kind") == "cancelled"
    assert "payload" not in out


def test_missing_parameters_file_is_reported_as_failure(app, tmp_path):
    worker = LoadWorker(None, tmp_path / "x.json", None)
    out = _run_worker(worker)
    assert out.get("kind") == "failed"
    assert "parameters" in out["message"]


def test_missing_pricing_file_is_reported_as_failure(app, tmp_path):
    if not (PARAMS.exists() or PARAMS_BEAUTY.exists()):
        pytest.skip("datasheet files not present")
    params = PARAMS if PARAMS.exists() else PARAMS_BEAUTY
    out = _run_worker(LoadWorker(params, None, None))
    assert out.get("kind") == "failed"
    assert "pricing" in out["message"]


@needs_files
def test_worker_survives_its_signals_being_dropped(app):
    """Regression: closing the window mid-load used to crash the worker thread.

    ``QThreadPool.start()`` keeps only the C++ worker, so a worker whose last
    Python reference is dropped was garbage collected mid-run and its QObject
    signals destroyed with it -- the thread then emitted on a deleted object and
    raised "Signal source has been deleted". The worker now holds itself alive for
    the duration of run(), and emits tolerate a vanished receiver.
    """
    from datasheet_editor.gui.workers import _LIVE_WORKERS

    params, pricing = _existing()
    worker = LoadWorker(params, pricing, None)
    assert worker in _LIVE_WORKERS

    loop = QEventLoop()
    seen: list[str] = []
    worker.signals.finished.connect(lambda p: (seen.append("finished"), loop.quit()))
    worker.signals.failed.connect(lambda m: (seen.append("failed"), loop.quit()))
    worker.signals.cancelled.connect(lambda: (seen.append("cancelled"), loop.quit()))

    # Drop every reference the caller would normally hold, then run.
    QTimer.singleShot(0, lambda: QThreadPool.globalInstance().start(worker))
    QTimer.singleShot(20000, loop.quit)
    loop.exec()

    assert seen == ["finished"]
    assert worker not in _LIVE_WORKERS


@needs_files
def test_emit_tolerates_a_deleted_receiver(app):
    from datasheet_editor.gui.workers import _emit

    signals = LoadSignals()
    _emit(signals.finished, {"ok": True})  # no receiver connected
    signals.deleteLater()  # queued; force the C++ object away
    _emit(signals.finished, {"ok": True})  # must not raise


@needs_files
def test_window_can_be_destroyed_mid_load_without_error(app):
    """Closing the window during a load must not raise in the worker thread."""
    params, pricing = _existing()
    window = MainWindow()
    window.parameters_path = params
    window.pricing_path = pricing
    window._update_path_label()
    window._start_load()

    # Tear the window down while the worker is still reading the 20MB file.
    window.deleteLater()
    window._load_worker = None  # drop the window's reference too

    loop = QEventLoop()
    QTimer.singleShot(2500, loop.quit)
    loop.exec()  # worker keeps running; nothing should raise


# --------------------------------------------------------------------------- #
# window state
# --------------------------------------------------------------------------- #


def test_starts_with_nothing_loaded(window):
    """No auto-discovery: a matching filename must not open on its own."""
    assert window.parameters_path is None
    assert window.pricing_path is None
    assert window.custom_path is None
    assert window.model_list.rowCount() == 0
    assert "not chosen" in window.path_label.text()
    assert "Parameters" in window.path_label.text()
    assert "Pricing" in window.path_label.text()
    assert "Custom" in window.path_label.text()


def test_three_load_buttons_exist(window):
    assert window.btn_load_params.text() == "Load Parameters…"
    assert window.btn_load_pricing.text() == "Load Pricing…"
    assert window.btn_load_custom.text() == "Load Custom…"
    # Each button says which file it wants, in text and in a tooltip.
    for button, expected in (
        (window.btn_load_params, "model_parameters.json"),
        (window.btn_load_pricing, "model_pricing.json"),
        (window.btn_load_custom, "custom_model_metadata.json"),
    ):
        assert expected in button.toolTip()


def test_actions_start_disabled(window):
    assert window.btn_merge.isEnabled() is False
    assert window.btn_save_output.isEnabled() is False
    assert window.btn_save_custom.isEnabled() is False


def test_partial_selection_does_not_load(window, monkeypatch):
    """Choosing only the parameters file must not start a load.

    A missing file raises a modal dialog, so stub it out; the dialog's own
    contents are asserted in test_adding.py.
    """
    from PySide6.QtWidgets import QMessageBox

    monkeypatch.setattr(QMessageBox, "information", staticmethod(lambda *a, **k: QMessageBox.Ok))

    params = PARAMS if PARAMS.exists() else PARAMS_BEAUTY
    if not params.exists():
        pytest.skip("datasheet files not present")
    window.parameters_path = params
    window._update_path_label()
    window._start_load()
    assert window.model_list.rowCount() == 0
    assert "model_pricing.json" in window.status_label.text()
    assert window.btn_merge.isEnabled() is False
    assert window._progress_dialog is None


@needs_files
def test_show_start_hint_names_both_required_files(window):
    window.show_start_hint()
    text = window.status_label.text()
    assert "model_parameters.json" in text
    assert "model_pricing.json" in text
    assert "optional" in text


@needs_files
def test_full_load_populates_and_enables_merge(window):
    params, pricing = _existing()
    loop = QEventLoop()
    # Connect before starting: load_paths wires the worker straight away, so
    # replacing the handler afterwards would never be called.
    window._on_loaded = lambda payload: (MainWindow._on_loaded(window, payload), loop.quit())
    window.load_paths(params, pricing, None)
    QTimer.singleShot(20000, loop.quit)
    loop.exec()
    assert window.model_list.rowCount() > 1000
    assert window.btn_merge.isEnabled() is True
    assert window._progress_dialog is None
    assert "models" in window.status_label.text()