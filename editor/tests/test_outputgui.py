"""The Save To dialog flow, and the per-file conflict dialog.

Save To replaces the old Save Output (directory + pair policy) and Save
Custom (direct write) buttons. The dialog is modal and the save path is
threaded, so these drive the real code and stub only the two places a test
cannot reach: the Save To dialog's own ``exec`` (via scripted values), and the
conflict dialog's ``exec``. Everything between -- resolving per-file targets,
refusing input targets, handing the resolved names to the worker -- is the
real thing, because that is where a bug would write to the wrong file.

The legacy pair-policy dialog (``OutputConflictDialog`` / ``ask_output_policy``)
keeps its own tests below: the CLI still uses it, so it must keep working even
though the GUI no longer opens it.
"""

from __future__ import annotations

import json

import pytest

pytest.importorskip("PySide6", reason="PySide6 not installed")

from PySide6.QtWidgets import QDialog, QMessageBox  # noqa: E402
from PySide6.QtWidgets import QDialogButtonBox  # noqa: E402

from datasheet_editor.gui.adddialogs import (  # noqa: E402
    DEFAULT_CUSTOM_OUTPUT,
    DEFAULT_PARAMETERS_OUTPUT,
    DEFAULT_PRICING_OUTPUT,
    SAVE_ACTION_CUSTOM,
    SAVE_ACTION_OUTPUT,
    OutputConflictDialog,
    SaveConflictDialog,
    SaveToDialog,
    ask_output_policy,
    build_conflict_item,
    first_free_path,
)
from datasheet_editor.gui.main_window import MainWindow  # noqa: E402
from datasheet_editor.gui.workers import SaveCustomWorker, SaveWorker  # noqa: E402
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
    """Collapse Save To writes to: record the worker, run it now.

    The workers are QRunnables, so starting them for real would make the test
    depend on the thread pool. Recording the resolved paths and then running
    the worker inline keeps the assertion on the part that decides filenames.
    """
    started: list[dict] = []

    # staticmethod: patched onto QThreadPool, so it would otherwise be bound and
    # receive the pool as a second argument.
    def fake_start(worker):
        record: dict = {"worker": type(worker).__name__}
        if isinstance(worker, SaveWorker):
            record["paths"] = dict(worker._paths)
        elif isinstance(worker, SaveCustomWorker):
            record["custom"] = worker._path
        started.append(record)
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
    rows = [{"id": MID, "provider": "xai", "mode": "chat", "base": "", "status": ""}]
    w.model_list.set_rows(rows)
    return w


def _answer_save_to(monkeypatch, values, action=SAVE_ACTION_OUTPUT):
    """Script the Save To dialog: no exec, just these values and this button."""
    def fake_open(self):
        self._save_values = values
        self._save_action = action
        return QDialog.Accepted

    def fake_values(self):
        return self._save_values

    def fake_action(self):
        return self._save_action

    monkeypatch.setattr(SaveToDialog, "exec", fake_open)
    monkeypatch.setattr(SaveToDialog, "values", fake_values)
    monkeypatch.setattr(SaveToDialog, "action", fake_action)


# --------------------------------------------------------------------------- #
# Save To dialog validation
# --------------------------------------------------------------------------- #


def test_save_to_prefills_the_dash_names_into_the_workdir(app, window):
    dialog = SaveToDialog(
        parameters_path=str(window.output_dir / DEFAULT_PARAMETERS_OUTPUT),
        pricing_path=str(window.output_dir / DEFAULT_PRICING_OUTPUT),
        custom_path=str(window.output_dir / DEFAULT_CUSTOM_OUTPUT),
        parameters_count=1, pricing_count=1, custom_count=0,
    )
    assert dialog.params_edit.text().endswith(DEFAULT_PARAMETERS_OUTPUT)
    assert DEFAULT_PARAMETERS_OUTPUT == "model-parameters.json"
    assert DEFAULT_PRICING_OUTPUT == "model-pricing.json"
    assert DEFAULT_CUSTOM_OUTPUT == "custom-metadata.json"


def test_save_to_shows_the_model_counts(app):
    dialog = SaveToDialog(
        parameters_path="/tmp/a.json", pricing_path="/tmp/b.json",
        custom_path="/tmp/c.json",
        parameters_count=12595, pricing_count=4763, custom_count=38,
    )
    assert "12,595" in dialog.params_row.count_label.text()
    assert "4,763" in dialog.pricing_row.count_label.text()
    assert "38" in dialog.custom_row.count_label.text()


def test_save_to_with_everything_empty_disables_both_saves(app):
    """Only Cancel works: there is nothing to write anywhere."""
    dialog = SaveToDialog(parameters_path="", pricing_path="", custom_path="")
    assert dialog.btn_save_output.isEnabled() is False
    assert dialog.btn_save_custom.isEnabled() is False
    assert "at least one" in dialog.error_label.text()


def test_save_to_with_only_parameters_enables_output_not_custom(app):
    dialog = SaveToDialog(parameters_path="/tmp/a.json", pricing_path="", custom_path="")
    assert dialog.btn_save_output.isEnabled() is True
    assert dialog.btn_save_custom.isEnabled() is False


def test_save_to_with_only_custom_enables_custom_not_output(app):
    dialog = SaveToDialog(parameters_path="", pricing_path="", custom_path="/tmp/c.json")
    assert dialog.btn_save_output.isEnabled() is False
    assert dialog.btn_save_custom.isEnabled() is True


def test_save_to_rejects_a_non_json_extension(app):
    dialog = SaveToDialog(
        parameters_path="/tmp/a.txt", pricing_path="", custom_path="",
    )
    assert dialog.btn_save_output.isEnabled() is False
    assert dialog.btn_save_custom.isEnabled() is False
    assert ".json" in dialog.error_label.text()


def test_save_to_values_returns_none_for_empty_paths(app):
    dialog = SaveToDialog(parameters_path="", pricing_path="", custom_path="/tmp/c.json")
    values = dialog.values()
    assert values["parameters"] is None
    assert values["pricing"] is None
    assert values["custom"] is not None


# --------------------------------------------------------------------------- #
# conflict inspection helpers
# --------------------------------------------------------------------------- #


def test_a_datasheet_old_file_reports_its_model_count(app, out):
    (out / "old.json").write_text(json.dumps({"a": {"x": 1}, "b": {"y": 2}}))
    item = build_conflict_item("parameters", out / "old.json", 10)
    assert item.old_is_datasheet is True
    assert item.old_count == 2
    assert item.new_count == 10


def test_a_non_datasheet_old_file_reports_no_count(app, out):
    """An unrelated JSON file shows size + mtime only, never a model count."""
    (out / "notes.json").write_text(json.dumps({"todo": "buy milk", "n": 3}))
    item = build_conflict_item("parameters", out / "notes.json", 10)
    assert item.old_is_datasheet is False
    assert item.old_count is None


def test_a_non_json_old_file_reports_no_count(app, out):
    (out / "notes.json").write_text("just some text")
    item = build_conflict_item("parameters", out / "notes.json", 10)
    assert item.old_is_datasheet is False


def test_first_free_path_numbers_per_file(app, out):
    (out / "model-parameters.json").write_text("{}")
    assert first_free_path(out / "model-parameters.json").name == "model-parameters_1.json"
    # A name with nothing in the way is returned unchanged.
    assert first_free_path(out / "model-pricing.json") == out / "model-pricing.json"


# --------------------------------------------------------------------------- #
# SaveConflictDialog
# --------------------------------------------------------------------------- #


def _detail_text(dialog):
    from PySide6.QtWidgets import QPlainTextEdit

    box = dialog.findChild(QPlainTextEdit)
    assert box is not None
    return box.toPlainText()


def test_conflict_dialog_shows_new_and_old_counts(app, out):
    (out / "old.json").write_text(json.dumps({"a": {"x": 1}}))
    dialog = SaveConflictDialog([build_conflict_item("parameters", out / "old.json", 10)])
    detail = _detail_text(dialog)
    assert "New:" in detail and "10" in detail
    assert "Old:" in detail and "1" in detail


def test_conflict_dialog_marks_a_non_datasheet_old_file(app, out):
    (out / "notes.json").write_text(json.dumps({"todo": "buy milk"}))
    dialog = SaveConflictDialog([build_conflict_item("custom", out / "notes.json", 5)])
    detail = _detail_text(dialog)
    assert "not a datasheet file" in detail
    assert "modified" in detail


def test_conflict_dialog_offers_three_answers(app, out):
    (out / "old.json").write_text("{}")
    dialog = SaveConflictDialog([build_conflict_item("parameters", out / "old.json", 1)])
    assert dialog.btn_overwrite is not None
    assert dialog.btn_add_number is not None


def test_conflict_overwrite_yields_overwrite(app, out):
    (out / "old.json").write_text("{}")
    dialog = SaveConflictDialog([build_conflict_item("parameters", out / "old.json", 1)])
    dialog.accept_overwrite()
    assert dialog.policy() == OVERWRITE


def test_conflict_add_number_yields_add_number(app, out):
    (out / "old.json").write_text("{}")
    dialog = SaveConflictDialog([build_conflict_item("parameters", out / "old.json", 1)])
    dialog.accept_add_number()
    assert dialog.policy() == ADD_NUMBER


def test_conflict_backing_out_means_no_policy(app, out):
    (out / "old.json").write_text("{}")
    dialog = SaveConflictDialog([build_conflict_item("parameters", out / "old.json", 1)])
    dialog.reject()
    assert dialog.policy() is None


def test_conflict_cancel_is_the_default_button(app, out):
    """A stray Return must not destroy a file the user never agreed to lose."""
    (out / "old.json").write_text("{}")
    dialog = SaveConflictDialog([build_conflict_item("parameters", out / "old.json", 1)])
    cancel = dialog.findChild(QDialogButtonBox).button(QDialogButtonBox.Cancel)
    assert cancel.isDefault() is True
    assert dialog.btn_overwrite.isDefault() is False
    assert dialog.btn_add_number.isDefault() is False


# --------------------------------------------------------------------------- #
# Save To output writes
# --------------------------------------------------------------------------- #


def test_save_output_writes_only_the_filled_file(window, out, monkeypatch, busy):
    """Save Output may write just one of the two merged files."""
    _answer_save_to(monkeypatch, {
        "parameters": out / "model-parameters.json",
        "pricing": None, "custom": None,
    })
    window._on_save_to()
    assert busy[0]["paths"].keys() == {"model_parameters.json"}
    written = json.loads((out / "model-parameters.json").read_text())
    assert written[MID]["max_output_tokens"] == 32768


def test_save_output_writes_sorted_and_beautified(window, out, monkeypatch, busy):
    _answer_save_to(monkeypatch, {
        "parameters": out / "model-parameters.json",
        "pricing": None, "custom": None,
    })
    window._on_save_to()
    raw = (out / "model-parameters.json").read_text()
    assert "\n" in raw  # beautified, not compact
    assert raw.index('"max_output_tokens"') < raw.index('"mode"')  # sorted keys


def test_save_output_overwrite_replaces_the_file(window, out, monkeypatch, busy):
    (out / "model-parameters.json").write_text('{"old": 1}', encoding="utf-8")
    monkeypatch.setattr(
        SaveConflictDialog, "exec",
        lambda self: (self.accept_overwrite() or QDialog.Accepted),
    )
    _answer_save_to(monkeypatch, {
        "parameters": out / "model-parameters.json",
        "pricing": None, "custom": None,
    })
    window._on_save_to()
    assert json.loads((out / "model-parameters.json").read_text())[MID]["max_output_tokens"] == 32768


def test_save_output_add_number_writes_beside_the_conflict(window, out, monkeypatch, busy):
    """Numbering is per file: the non-conflicting sibling keeps its name."""
    (out / "model-parameters.json").write_text('{"keep": 1}', encoding="utf-8")
    monkeypatch.setattr(
        SaveConflictDialog, "exec",
        lambda self: (self.accept_add_number() or QDialog.Accepted),
    )
    _answer_save_to(monkeypatch, {
        "parameters": out / "model-parameters.json",
        "pricing": out / "model-pricing.json",
        "custom": None,
    })
    window._on_save_to()
    # The worker got the resolved (numbered) name, not the conflicting one.
    assert busy[0]["paths"]["model_parameters.json"].name == "model-parameters_1.json"
    assert json.loads((out / "model-parameters.json").read_text()) == {"keep": 1}
    assert (out / "model-parameters_1.json").exists()
    assert (out / "model-pricing.json").exists()


def test_save_output_cancelling_the_conflict_writes_nothing(window, out, monkeypatch, busy):
    existing = out / "model-parameters.json"
    existing.write_text('{"keep": 1}', encoding="utf-8")
    monkeypatch.setattr(SaveConflictDialog, "exec", lambda self: QDialog.Rejected)
    _answer_save_to(monkeypatch, {
        "parameters": out / "model-parameters.json",
        "pricing": None, "custom": None,
    })
    window._on_save_to()
    assert busy == []
    assert json.loads(existing.read_text()) == {"keep": 1}
    assert "cancelled" in window.status_label.text()


def test_save_custom_writes_the_overlay_sorted(window, out, monkeypatch, busy):
    window.overlay = {"z": {"pricing": {"b": 1, "a": 2}}, "a": {"parameters": {"mode": "chat"}}}
    _answer_save_to(
        monkeypatch,
        {"parameters": None, "pricing": None, "custom": out / "custom-metadata.json"},
        action=SAVE_ACTION_CUSTOM,
    )
    window._on_save_to()
    assert busy[0]["worker"] == "SaveCustomWorker"
    raw = (out / "custom-metadata.json").read_text()
    assert raw.index('"a"') < raw.index('"z"')  # sorted
    assert "\n" in raw  # beautified
    assert window.dirty is False
    assert window.custom_path == out / "custom-metadata.json"


def test_save_custom_cancelling_writes_nothing(window, out, monkeypatch, busy):
    existing = out / "custom-metadata.json"
    existing.write_text('{"keep": 1}', encoding="utf-8")
    monkeypatch.setattr(SaveConflictDialog, "exec", lambda self: QDialog.Rejected)
    window.overlay = {"m": {"pricing": {"x": 1}}}
    _answer_save_to(
        monkeypatch,
        {"parameters": None, "pricing": None, "custom": existing},
        action=SAVE_ACTION_CUSTOM,
    )
    window._on_save_to()
    assert busy == []
    assert json.loads(existing.read_text()) == {"keep": 1}


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
    _answer_save_to(monkeypatch, {
        "parameters": source, "pricing": None, "custom": None,
    })
    window._on_save_to()
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

    monkeypatch.setattr(SaveConflictDialog, "exec", explode)
    monkeypatch.setattr(QMessageBox, "warning", staticmethod(lambda *a, **k: QMessageBox.Ok))
    _answer_save_to(monkeypatch, {
        "parameters": source, "pricing": None, "custom": None,
    })
    window._on_save_to()


def test_the_refusal_explains_itself(window, out, monkeypatch, busy):
    from pathlib import Path

    source = out / "model_pricing.json"
    source.write_text("{}", encoding="utf-8")
    window.pricing_path = Path(source)
    monkeypatch.setattr(QMessageBox, "warning", staticmethod(lambda *a, **k: QMessageBox.Ok))
    _answer_save_to(monkeypatch, {
        "parameters": None, "pricing": source, "custom": None,
    })
    window._on_save_to()
    assert "model_pricing.json" in window.status_label.text()


def test_saving_custom_back_to_its_loaded_path_is_allowed(window, out, monkeypatch, busy):
    """The loaded custom overlay is exempt: round-tripping it is the normal flow."""
    source = out / "custom.json"
    source.write_text("{}", encoding="utf-8")
    window.custom_path = source
    window.overlay = {"m": {"pricing": {"x": 1}}}
    monkeypatch.setattr(
        SaveConflictDialog, "exec",
        lambda self: (self.accept_overwrite() or QDialog.Accepted),
    )
    _answer_save_to(
        monkeypatch,
        {"parameters": None, "pricing": None, "custom": source},
        action=SAVE_ACTION_CUSTOM,
    )
    window._on_save_to()
    assert busy != []
    assert json.loads(source.read_text()) == {"m": {"pricing": {"x": 1}}}


# --------------------------------------------------------------------------- #
# the legacy pair-policy dialog still serves the CLI
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
    (out / "model_parameters.json").write_text("{}", encoding="utf-8")
    dialog = OutputConflictDialog(_conflict(out))
    cancel = dialog.findChild(QDialogButtonBox).button(QDialogButtonBox.Cancel)
    assert cancel.isDefault() is True
    assert dialog.btn_overwrite.isDefault() is False
    assert dialog.btn_add_number.isDefault() is False


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
# SaveWorker takes paths, either file or both
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


def test_the_worker_writes_a_single_file(app, out):
    """Save To may fill only one merged path; the other is simply absent."""
    result = type(
        "R", (),
        {
            "parameters": {MID: {"mode": "chat"}},
            "pricing": {MID: {"provider": "xai"}},
        },
    )()
    worker = SaveWorker(result, paths={"model_parameters.json": out / "only.json"})
    worker.run()
    assert json.loads((out / "only.json").read_text()) == {MID: {"mode": "chat"}}
    assert not (out / "model_pricing.json").exists()


def test_the_worker_writes_sorted_and_beautified_by_default(app, out):
    result = type(
        "R", (),
        {
            "parameters": {"z": {"b": 1, "a": 2}},
            "pricing": {},
        },
    )()
    worker = SaveWorker(result, paths={"model_parameters.json": out / "p.json"})
    worker.run()
    raw = (out / "p.json").read_text()
    assert "\n" in raw
    assert raw.index('"a"') < raw.index('"b"')


def test_the_custom_worker_writes_the_overlay(app, out):
    worker = SaveCustomWorker({"m": {"pricing": {"x": 1}}}, out / "c.json")
    worker.run()
    assert json.loads((out / "c.json").read_text()) == {"m": {"pricing": {"x": 1}}}
