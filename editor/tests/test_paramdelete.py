"""Deleting a descriptor the user added.

Deleting is scoped to descriptors that exist **only in the overlay**. A descriptor
that came from the original dataset cannot be removed: the overlay has no delete
channel (``merge._deep_merge`` is explicit that absence never removes anything),
and inventing a tombstone would change the custom file's format for every
existing one. So "remove" here means "forget the addition", which is exactly
reversible in intent and needs no new syntax.

What is pinned:

- The button is enabled only for an overlay-only descriptor, and says why when it
  is not. A permanently greyed-out control with no reason reads as a bug.
- Deleting prunes the empty container all the way up, so the custom file does not
  accumulate ``{"grok": {"parameters": {"model_parameters": []}}}``.
- The merge result no longer carries the descriptor, which is the behaviour that
  actually matters -- the overlay is only a means.
"""

from __future__ import annotations

import pytest

pytest.importorskip("PySide6", reason="PySide6 not installed")

from PySide6.QtWidgets import QApplication, QMessageBox  # noqa: E402

from datasheet_editor.fieldinfo import build_catalog  # noqa: E402
from datasheet_editor.gui.main_window import MainWindow  # noqa: E402
from datasheet_editor.gui.models import build_rows  # noqa: E402
from datasheet_editor.merge import (  # noqa: E402
    PARAM_ID_FIELD,
    PARAMS_FIELD,
    merge_param_array,
)

MID = "grok-4.20"

#: 'temperature' ships in the original; 'top_k' does not, so adding it puts a
#: descriptor in the overlay alone and makes it deletable.
PARAMS = {
    MID: {
        "mode": "chat",
        # Only 'temperature' ships in the original. 'top_k' is what the tests add,
        # which is the only kind of descriptor that can be removed again.
        PARAMS_FIELD: [
            {"id": "temperature", "type": "number", "default": 1, "label": "Temp"},
        ],
    }
}
PRICING = {MID: {"provider": "xai"}}


@pytest.fixture(scope="session")
def app():
    return QApplication.instance() or QApplication([])


@pytest.fixture
def window(app, monkeypatch):
    monkeypatch.setattr(QMessageBox, "information", staticmethod(lambda *a, **k: QMessageBox.Ok))
    # Both answers accepted by default; individual tests override to test refusal.
    monkeypatch.setattr(QMessageBox, "question", staticmethod(lambda *a, **k: QMessageBox.Yes))
    w = MainWindow()
    w.parameters = PARAMS
    w.pricing = PRICING
    w.overlay = {}
    w.conflicts = {}
    w.param_catalog = build_catalog(PARAMS, PRICING)
    w.pricing_catalog = build_catalog(PRICING, PRICING, source="pricing")
    w._populate_rows(build_rows(PARAMS, PRICING, w.overlay))
    w._select_model(MID)
    return w


def _ids(win):
    """The descriptor ids currently shown in the table, in row order."""
    return [item.get(PARAM_ID_FIELD) for item in win.param_model._items]


@pytest.fixture
def added(window):
    """A window where top_k exists in the overlay but not in the original.

    Every deletable case needs that, and it has to be produced the way the app
    produces it -- by editing a new id into the overlay -- so a test cannot
    accidentally set up a state the user has no path to.
    """
    window._on_param_cell_edited("top_k", "default", 40)
    return window


# --------------------------------------------------------------------------- #
# which descriptors may be deleted
# --------------------------------------------------------------------------- #


def test_an_original_descriptor_cannot_be_deleted(window):
    window._select_descriptor("temperature")
    assert window.btn_delete_param.isEnabled() is False


def test_an_added_descriptor_can_be_deleted(added):
    """top_k is in the overlay only, so removing it is just forgetting it."""
    added._select_descriptor("top_k")
    assert added.btn_delete_param.isEnabled() is True


def test_nothing_selected_means_nothing_deletable(window):
    window._param_focus_id = None
    window._update_delete_param_enabled()
    assert window.btn_delete_param.isEnabled() is False


def test_the_button_explains_why_a_read_only_descriptor_is_kept(window):
    """A greyed-out control with no reason reads as a bug, not as a rule."""
    window._select_descriptor("temperature")
    assert "original" in window.btn_delete_param.toolTip().lower()


def test_the_button_explains_itself_when_it_is_armed(window):
    window._select_descriptor("top_k")
    assert "remove" in window.btn_delete_param.toolTip().lower()


def test_no_model_selected_means_nothing_deletable(window):
    # _selected_model() reads the table's selection, so the list being non-empty
    # is not enough to make a row 'selected'.
    window.table.clearSelection()
    window._update_delete_param_enabled()
    assert window.btn_delete_param.isEnabled() is False


# --------------------------------------------------------------------------- #
# deleting
# --------------------------------------------------------------------------- #


def test_deleting_removes_the_row_from_the_table(added):
    added._select_descriptor("top_k")
    added._on_delete_param()
    assert "top_k" not in _ids(added)
    assert "temperature" in _ids(added)


def test_deleting_empties_the_entire_placeholder_key(added):
    """No leftover {"parameters": {"model_parameters": []}} in the custom file."""
    added._select_descriptor("top_k")
    added._on_delete_param()
    assert MID not in added.overlay


def test_deleting_the_last_one_leaves_no_empty_model_entry(added):
    added._select_descriptor("top_k")
    added._on_delete_param()
    assert added.overlay == {}


def test_deleting_marks_the_work_unsaved(added):
    added._select_descriptor("top_k")
    added.dirty = False
    added._on_delete_param()
    assert added.dirty is True
    assert added.btn_save_custom.isEnabled() is True


def test_deleting_keeps_the_other_descriptors_overrides_intact(added):
    """One delete must not take a sibling's edited default with it."""
    added._on_param_cell_edited("temperature", "default", 5)
    added._select_descriptor("top_k")
    added._on_delete_param()
    assert added.overlay[MID]["parameters"][PARAMS_FIELD] == [
        {"id": "temperature", "default": 5}
    ]


def test_the_merge_result_no_longer_carries_the_deleted_descriptor(added):
    """The overlay is only a means; this is the effect that matters."""
    added._select_descriptor("top_k")
    added._on_delete_param()
    merged = merge_param_array(PARAMS[MID][PARAMS_FIELD], added.overlay.get(MID, {}).get(
        "parameters", {}
    ).get(PARAMS_FIELD, []))
    assert [m["id"] for m in merged] == ["temperature"]


def test_the_row_being_deleted_is_announced_in_the_status_bar(added):
    added._select_descriptor("top_k")
    added._on_delete_param()
    assert "top_k" in added.status_label.text()


# --------------------------------------------------------------------------- #
# confirmation
# --------------------------------------------------------------------------- #


def test_deleting_asks_first(added, monkeypatch):
    added._select_descriptor("top_k")
    monkeypatch.setattr(
        QMessageBox, "question", staticmethod(lambda *a, **k: QMessageBox.No)
    )
    added._on_delete_param()
    assert "top_k" in _ids(added)


def test_saying_no_leaves_the_overlay_untouched(added, monkeypatch):
    added._select_descriptor("top_k")
    before = [dict(e) for e in added.overlay[MID]["parameters"][PARAMS_FIELD]]
    monkeypatch.setattr(
        QMessageBox, "question", staticmethod(lambda *a, **k: QMessageBox.No)
    )
    added._on_delete_param()
    assert added.overlay[MID]["parameters"][PARAMS_FIELD] == before


def test_the_prompt_names_the_descriptor_being_removed(added, monkeypatch):
    """'Delete this item?' is worse than useless when a dozen rows look alike."""
    seen = {}

    def capture(parent, title, text, *a, **k):
        seen["text"] = text
        return QMessageBox.Yes

    monkeypatch.setattr(QMessageBox, "question", staticmethod(capture))
    added._select_descriptor("top_k")
    added._on_delete_param()
    assert "top_k" in seen["text"]


def test_a_refused_delete_does_not_mark_the_work_dirty(added, monkeypatch):
    added._select_descriptor("top_k")
    added.dirty = False
    monkeypatch.setattr(
        QMessageBox, "question", staticmethod(lambda *a, **k: QMessageBox.No)
    )
    added._on_delete_param()
    assert added.dirty is False


# --------------------------------------------------------------------------- #
# re-adding after a delete
# --------------------------------------------------------------------------- #


def test_a_deleted_descriptor_can_be_added_again(added):
    added._select_descriptor("top_k")
    added._on_delete_param()
    added._on_param_cell_edited("top_k", "default", 20)
    assert "top_k" in _ids(added)
    assert added.overlay[MID]["parameters"][PARAMS_FIELD] == [{"id": "top_k", "default": 20}]


def test_deleting_leaves_the_button_disabled_for_the_next_original_row(added):
    added._select_descriptor("top_k")
    added._on_delete_param()
    added._select_descriptor("temperature")
    assert added.btn_delete_param.isEnabled() is False
