"""The descriptor table lives in its own tab, and only two of its cells edit.

Two things are pinned here:

- **Layout.** The descriptors are a different vocabulary from the entry's own
  columns, so they get their own tab rather than being squeezed below the fields
  in a splitter where a model's fifteen descriptors showed five rows.
- **Which cells edit.** Only ``label`` and ``default``. ``id`` is the key the merge
  matches on, so renaming one is a delete plus an add. ``type`` selects which
  control the playground renders and is only coherent together with the keys it
  implies (``options``, ``range``, ``array``), which this table does not edit -- so
  a cell that could be flipped on its own would be editable but wrong.
"""

from __future__ import annotations

import pytest

pytest.importorskip("PySide6", reason="PySide6 not installed")

from PySide6.QtCore import Qt  # noqa: E402
from PySide6.QtWidgets import QApplication, QMessageBox  # noqa: E402

from datasheet_editor.fieldinfo import build_catalog  # noqa: E402
from datasheet_editor.gui.main_window import MainWindow  # noqa: E402
from datasheet_editor.gui.models import ParamDescriptorModel, build_rows  # noqa: E402

MID = "grok-4.20"

PARAMS = {
    MID: {
        "mode": "chat",
        "max_output_tokens": 1000000,
        "model_parameters": [
            {"id": "temperature", "type": "number", "range": {"min": 0, "max": 2},
             "label": "Temperature", "default": 1,
             "helpText": "What sampling temperature to use."},
            {"id": "max_output_tokens", "type": "number", "default": 8192,
             "label": "Max Tokens"},
        ],
    },
    "bare": {"mode": "chat"},
}
PRICING = {MID: {"provider": "xai"}, "bare": {"provider": "xai"}}


@pytest.fixture(scope="session")
def app():
    return QApplication.instance() or QApplication([])


@pytest.fixture
def window(app, monkeypatch):
    monkeypatch.setattr(QMessageBox, "information", staticmethod(lambda *a, **k: QMessageBox.Ok))
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


# --------------------------------------------------------------------------- #
# layout
# --------------------------------------------------------------------------- #


def test_there_are_four_tabs(window):
    assert window.tabs.count() == 4


def test_the_tab_order_is_parameters_pricing_conflicts_model_parameters(window):
    titles = [window.tabs.tabText(i) for i in range(window.tabs.count())]
    assert [t.split(" (")[0] for t in titles] == [
        "Parameters", "Pricing", "Conflicts", "model_parameters"
    ]


def test_the_descriptor_table_is_not_inside_the_parameters_tab(window):
    """It was a splitter pane below the fields, which left five visible rows."""
    params_page = window.tabs.widget(0)
    assert window.param_table not in params_page.findChildren(type(window.param_table))
    descriptors_page = window.tabs.widget(window._descriptors_tab)
    assert window.param_table in descriptors_page.findChildren(type(window.param_table))


def test_the_descriptors_tab_is_disabled_for_a_model_with_none(window):
    window._select_model("bare")
    assert window.tabs.isTabEnabled(window._descriptors_tab) is False
    window._select_model(MID)
    assert window.tabs.isTabEnabled(window._descriptors_tab) is True


def test_the_table_starts_with_nothing_selected(window):
    """A stale descriptor from the previous model must not survive the switch."""
    window._select_model("bare")
    assert window.param_model.rowCount() == 0
    window._select_model(MID)
    assert window.param_model.rowCount() == 2


def test_a_read_only_column_still_cannot_be_edited(window):
    row = window.param_model.row_for("temperature")
    assert window.param_model.setData(window.param_model.index(row, 2), "boolean", Qt.EditRole) is False


def test_the_parameters_form_does_not_show_the_descriptor_array(window):
    """model_parameters is skipped there; it has its own tab now."""
    window._rebuild_field_panes(MID)
    names = [window.params_form.itemAt(i).widget() for i in range(window.params_form.count())]
    assert not any(getattr(w, "name", None) == "model_parameters" for w in names)


# --------------------------------------------------------------------------- #
# editability, per column
# --------------------------------------------------------------------------- #


@pytest.mark.parametrize("column, key", [(1, "label"), (3, "default")])
def test_label_and_default_are_editable(app, column, key):
    model = ParamDescriptorModel()
    model.set_items([{"id": "p", "label": "P", "default": 1}])
    assert model.flags(model.index(0, column)) & Qt.ItemIsEditable
    assert model.KEYS[column] == key


@pytest.mark.parametrize(
    "column, why",
    [
        (0, "id is the key descriptors are matched on; renaming is delete plus add"),
        (2, "type picks the playground control and implies options/range/array"),
        (4, "range is a nested object a one-line cell would flatten"),
    ],
)
def test_the_other_columns_are_read_only(app, column, why):
    model = ParamDescriptorModel()
    model.set_items([{"id": "p", "label": "P", "type": "number", "default": 1,
                      "range": {"min": 0}}])
    assert not model.flags(model.index(0, column)) & Qt.ItemIsEditable, why
    assert model.setData(model.index(0, column), "anything", Qt.EditRole) is False


def test_only_two_columns_are_editable_in_total(app):
    assert len(ParamDescriptorModel.EDITABLE_COLUMNS) == 2


def test_every_read_only_column_explains_itself(app):
    """A greyed-out cell with no reason reads as a bug."""
    model = ParamDescriptorModel()
    for column, note in ParamDescriptorModel.COLUMN_NOTES.items():
        assert model.headerData(column, Qt.Horizontal, Qt.ToolTipRole), column
        assert len(note) > 30, note


def test_editable_columns_have_no_excuse_note(app):
    model = ParamDescriptorModel()
    for column in ParamDescriptorModel.EDITABLE_COLUMNS:
        assert column not in ParamDescriptorModel.COLUMN_NOTES


def test_a_row_tooltip_carries_the_playground_help_text(app):
    """The descriptor's own helpText, so hovering explains what the row is."""
    catalog = build_catalog(PARAMS, PRICING)
    model = ParamDescriptorModel(descriptor_info=catalog.descriptor)
    model.set_items([{"id": "temperature", "type": "number"}])
    tip = model.data(model.index(0, 1), Qt.ToolTipRole)
    assert "sampling temperature" in tip


def test_a_row_tooltip_is_absent_for_an_unknown_parameter(app):
    catalog = build_catalog(PARAMS, PRICING)
    model = ParamDescriptorModel(descriptor_info=catalog.descriptor)
    model.set_items([{"id": "never_seen_elsewhere"}])
    assert model.data(model.index(0, 1), Qt.ToolTipRole) is None


def test_a_read_only_columns_tooltip_wins_over_the_row_description(app):
    model = ParamDescriptorModel()
    model.set_items([{"id": "temperature", "type": "number"}])
    assert "read-only" in model.data(model.index(0, 2), Qt.ToolTipRole).lower()


def test_the_hint_explains_what_bifrost_reads(window):
    text = window.descriptors_hint.text()
    assert "only the id" in text
    assert "read-only" in text