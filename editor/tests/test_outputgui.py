"""The output-collision dialog, and Save Output honouring the answer.

The dialog is modal and the save path is threaded, so these drive the real code
and stub only the two places a test cannot reach: the file chooser, and the
dialog's own ``exec``. Everything between -- resolving the paths, choosing the
policy, handing the resolved names to the worker -- is the real thing, because
that is where a bug would write to the wrong file.
"""

from __future__ import annotations

import json

import pytest

pytest.importorskip("PySide6", reason="PySide6 not installed")

from PySide6.QtWidgets import QDialog, QFileDialog, QLabel, QMessageBox  # noqa: E402

from datasheet_editor.gui.adddialogs import (  # noqa: E402
    OutputConflictDialog,
    ask_output_policy,
)
from datasheet_editor.gui.main_window import MainWindow  # noqa: E402
from datasheet_editor.gui.workers import SaveWorker  # noqa: E402
from datasheet_editor.output import (  # noqa: E402
    ADD_NUMBER,
    OVERWRITE,
    OUTPUT_FILENAMES,
    OutputConflict,
    plan_output,
)

MID = "grok-4.20"


@pytest.fixture(scope="session")
def app():
    from PySide6.QtWidgets import QApplication

    return QApplication.instance() or QApplication([])


@pytest.fixture
def out(tmp_path):
    d = tmp_path / "out"
    d.mkdir()
    return d


@pytest.fixture
def busy(monkeypatch):
    """Collapse Save Output to: choose this dir, record the worker, run it now.

    The worker is a QRunnable, so starting it for real would make the test
    depend on the thread pool. Recording the resolved paths and then running the
    worker inline keeps the assertion on the part that decides filenames.
    """
    started: list[dict] = []

    # staticmethod: patched onto QThreadPool, so it would otherwise be bound and
    # receive the pool as a second argument.
    def fake_start(worker):
        started.append({"paths": dict(worker._paths)})
        worker.run()
        return worker

    fake_start = staticmethod(fake_start)

    monkeypatch.setattr("datasheet_editor.gui.main_window.QThreadPool.start", fake_start)
    return started


@pytest.fixture
def window(app, monkeypatch):
    monkeypatch.setattr(QMessageBox, "information", staticmethod(lambda *a, **k: QMessageBox.Ok))
    w = MainWindow()
    w.parameters = {MID: {"mode": "chat", "max_output_tokens": 1000000}}
    w.pricing = {MID: {"provider": "xai", "input_cost_per_token": 3e-06}}
    w.overlay = {}
    w.merge_result = type(
        "R", (),
        {
            "parameters": {MID: {"mode": "chat", "max_output_tokens": 32768}},
            "pricing": {MID: {"provider": "xai", "input_cost_per_token": 3e-06}},
            "summary": lambda self: {"changed_models": 1},
        },
    )()
    return w


def choose_dir(monkeypatch, path):
    monkeypatch.setattr(
        QFileDialog, "getExistingDirectory", staticmethod(lambda *a, **k: str(path))
    )


# --------------------------------------------------------------------------- #
# the dialog
# --------------------------------------------------------------------------- #


def _conflict(out):
    try:
        plan_output(out)
    except OutputConflict as exc:
        return exc
    raise AssertionError("expected a conflict")


def test_the_dialog_offers_exactly_the_two_answers(app, out):
    (out / "model_parameters.json").write_text("{}", encoding="utf-8")
    dialog = OutputConflictDialog(_conflict(out))
    assert dialog.btn_overwrite.isVisible() is not None
    assert dialog.btn_add_number.isVisible() is not None


def test_overwrite_yields_the_overwrite_policy(app, out):
    (out / "model_parameters.json").write_text("{}", encoding="utf-8")
    dialog = OutputConflictDialog(_conflict(out))
    dialog.accept_overwrite()
    assert dialog.policy() == OVERWRITE


def test_add_number_yields_the_add_number_policy(app, out):
    (out / "model_parameters.json").write_text("{}", encoding="utf-8")
    dialog = OutputConflictDialog(_conflict(out))
    dialog.accept_add_number()
    assert dialog.policy() == ADD_NUMBER


def test_backing_out_means_no_policy(app, out):
    """Cancelling must not default to overwriting -- that is the whole point."""
    (out / "model_parameters.json").write_text("{}", encoding="utf-8")
    dialog = OutputConflictDialog(_conflict(out))
    dialog.reject()
    assert dialog.policy() is None


def test_cancel_is_the_default_button(app, out):
    """A stray Return must not destroy a file the user never agreed to lose."""
    from PySide6.QtWidgets import QDialogButtonBox

    (out / "model_parameters.json").write_text("{}", encoding="utf-8")
    dialog = OutputConflictDialog(_conflict(out))
    cancel = dialog.findChild(QDialogButtonBox).button(QDialogButtonBox.Cancel)
    assert cancel.isDefault() is True
    assert dialog.btn_overwrite.isDefault() is False
    assert dialog.btn_add_number.isDefault() is False


def _labels(dialog):
    """Every visible text in the dialog, joined, for substring assertions."""
    return " ".join(w.text() for w in dialog.findChildren(QLabel) if w.text())


def test_the_dialog_names_the_files_it_would_replace(app, out):
    (out / "model_pricing.json").write_text("{}", encoding="utf-8")
    assert "model_pricing.json" in _labels(OutputConflictDialog(_conflict(out)))


def test_the_dialog_shows_the_size_of_what_would_be_lost(app, out):
    """'the file exists' is easy to click through; 'the 28 MB file' is not."""
    (out / "model_parameters.json").write_bytes(b"x" * 3 * 1024 * 1024)
    assert "3.0 MB" in _labels(OutputConflictDialog(_conflict(out)))


def test_the_dialog_previews_the_numbered_names(app, out):
    (out / "model_parameters.json").write_text("{}", encoding="utf-8")
    assert "model_parameters_1.json" in _labels(OutputConflictDialog(_conflict(out)))


# --------------------------------------------------------------------------- #
# ask_output_policy
# --------------------------------------------------------------------------- #


def test_a_clear_folder_resolves_without_a_dialog(app, out, monkeypatch):
    """The normal save must not build a dialog it would only discard."""
    def explode(*a, **k):
        raise AssertionError("a dialog was constructed with nothing in the way")

    monkeypatch.setattr("datasheet_editor.gui.adddialogs.OutputConflictDialog", explode)
    policy, paths = ask_output_policy(out)
    assert policy == OVERWRITE
    assert paths["model_parameters.json"].name == "model_parameters.json"


@pytest.mark.parametrize("choice, expected", [(OVERWRITE, OVERWRITE), (ADD_NUMBER, ADD_NUMBER)])
def test_the_answer_is_honoured(app, out, monkeypatch, choice, expected):
    (out / "model_parameters.json").write_text("{}", encoding="utf-8")
    monkeypatch.setattr(
        OutputConflictDialog, "exec", lambda self: (
            self.accept_overwrite() if choice == OVERWRITE else self.accept_add_number()
        ) or QDialog.Accepted
    )
    policy, paths = ask_output_policy(out)
    assert policy == expected
    if choice == ADD_NUMBER:
        assert paths["model_parameters.json"].name == "model_parameters_1.json"


def test_cancelling_resolves_to_nothing_at_all(app, out, monkeypatch):
    """paths must be empty, not a default plan: nothing may be written."""
    (out / "model_parameters.json").write_text("{}", encoding="utf-8")
    monkeypatch.setattr(OutputConflictDialog, "exec", lambda self: QDialog.Rejected)
    policy, paths = ask_output_policy(out)
    assert policy is None
    assert paths == {}


# --------------------------------------------------------------------------- #
# Save Output
# --------------------------------------------------------------------------- #


def test_save_output_into_a_clear_folder_just_writes(window, out, monkeypatch, busy):
    choose_dir(monkeypatch, out)
    window._on_save_output()
    assert busy[0]["paths"]["model_parameters.json"] == out / "model_parameters.json"
    assert (out / "model_parameters.json").exists()


def test_save_output_passes_the_numbered_names_to_the_worker(window, out, monkeypatch, busy):
    """If the worker re-derived the name from the directory it would clobber the
    file the dialog just promised to keep."""
    (out / "model_parameters.json").write_text('{"keep": 1}', encoding="utf-8")
    monkeypatch.setattr(
        OutputConflictDialog, "exec",
        lambda self: (self.accept_add_number() or QDialog.Accepted),
    )
    choose_dir(monkeypatch, out)
    window._on_save_output()
    assert busy[0]["paths"]["model_parameters.json"].name == "model_parameters_1.json"
    assert json.loads((out / "model_parameters.json").read_text()) == {"keep": 1}
    assert (out / "model_parameters_1.json").exists()


def test_overwrite_replaces_the_file(window, out, monkeypatch, busy):
    (out / "model_parameters.json").write_text('{"old": 1}', encoding="utf-8")
    monkeypatch.setattr(
        OutputConflictDialog, "exec",
        lambda self: (self.accept_overwrite() or QDialog.Accepted),
    )
    choose_dir(monkeypatch, out)
    window._on_save_output()
    assert json.loads((out / "model_parameters.json").read_text())[MID]["max_output_tokens"] == 32768


def test_cancelling_writes_nothing_and_says_so(window, out, monkeypatch, busy):
    existing = out / "model_parameters.json"
    existing.write_text('{"keep": 1}', encoding="utf-8")
    monkeypatch.setattr(OutputConflictDialog, "exec", lambda self: QDialog.Rejected)
    choose_dir(monkeypatch, out)
    window._on_save_output()
    assert busy == []
    assert json.loads(existing.read_text()) == {"keep": 1}
    assert "cancelled" in window.status_label.text()


def test_cancelling_does_not_start_a_worker(window, out, monkeypatch, busy):
    (out / "model_parameters.json").write_text("{}", encoding="utf-8")
    monkeypatch.setattr(OutputConflictDialog, "exec", lambda self: QDialog.Rejected)
    choose_dir(monkeypatch, out)

    def explode(*a, **k):
        raise AssertionError("a save started after the user cancelled")

    monkeypatch.setattr("datasheet_editor.gui.workers.SaveWorker", explode)
    window._on_save_output()


# --------------------------------------------------------------------------- #
# the input is not a valid output target
# --------------------------------------------------------------------------- #


def test_the_gui_refuses_to_write_over_the_file_it_loaded(window, out, monkeypatch, busy):
    """The CLI already refuses this; the GUI accepting it would be a data-loss
    path the two front-ends disagree about -- and the merge read that file at the
    start of the run, so a mistake is not repeatable."""
    from pathlib import Path

    source = out / "model_parameters.json"
    source.write_text('{"original": true}', encoding="utf-8")
    window.parameters_path = Path(source)
    monkeypatch.setattr(
        QMessageBox, "warning", staticmethod(lambda *a, **k: QMessageBox.Ok)
    )
    choose_dir(monkeypatch, out)
    window._on_save_output()
    assert busy == []
    assert json.loads(source.read_text()) == {"original": True}


def test_the_input_refusal_does_not_show_the_collision_dialog(window, out, monkeypatch, busy):
    """Asking a question whose every answer is refused is a dead end."""
    from pathlib import Path

    source = out / "model_parameters.json"
    source.write_text("{}", encoding="utf-8")
    window.parameters_path = Path(source)

    def explode(*a, **k):
        raise AssertionError("the collision dialog was shown for an input target")

    monkeypatch.setattr(OutputConflictDialog, "exec", explode)
    monkeypatch.setattr(QMessageBox, "warning", staticmethod(lambda *a, **k: QMessageBox.Ok))
    choose_dir(monkeypatch, out)
    window._on_save_output()


def test_the_refusal_explains_itself(window, out, monkeypatch, busy):
    from pathlib import Path

    source = out / "model_pricing.json"
    source.write_text("{}", encoding="utf-8")
    window.pricing_path = Path(source)
    monkeypatch.setattr(QMessageBox, "warning", staticmethod(lambda *a, **k: QMessageBox.Ok))
    choose_dir(monkeypatch, out)
    window._on_save_output()
    assert "model_pricing.json" in window.status_label.text()


# --------------------------------------------------------------------------- #
# SaveWorker takes paths
# --------------------------------------------------------------------------- #


def test_the_worker_uses_the_paths_it_is_given(app, tmp_path):
    other = tmp_path / "elsewhere"
    other.mkdir()
    worker = SaveWorker(None, paths={
        name: other / name for name in OUTPUT_FILENAMES
    })
    assert worker._paths["model_parameters.json"] == other / "model_parameters.json"


def test_the_worker_still_accepts_a_bare_directory(app, out):
    """Existing callers pass a directory; that must keep working."""
    assert SaveWorker(None, out)._paths["model_parameters.json"] == out / "model_parameters.json"


def test_the_worker_refuses_to_guess_with_nothing(app):
    """Silently defaulting to a directory is how the wrong file gets written."""
    with pytest.raises(ValueError):
        SaveWorker(None)
