"""GUI tests.

Skipped when PySide6 is unavailable. These exist mostly as regression cover for
the Qt/Python interop pitfalls that make a window *look* fine while rendering
nothing:

- Qt passes an invalid ``QModelIndex()`` (not ``None``) into Python overrides,
  so ``if parent is not None`` silently makes every table report zero rows.
- A proxy over a Python model stays empty unless the root row/column counts
  answer correctly.
"""

from __future__ import annotations

import pytest

pytest.importorskip("PySide6", reason="PySide6 not installed")

from PySide6.QtCore import QModelIndex, Qt  # noqa: E402
from PySide6.QtWidgets import QApplication  # noqa: E402

from datasheet_editor.gui.models import (  # noqa: E402
    COL_ID,
    ChangeListModel,
    ModelFilterProxy,
    ModelListModel,
    ParamDescriptorModel,
    build_rows,
)


@pytest.fixture(scope="session")
def app():
    return QApplication.instance() or QApplication([])


@pytest.fixture
def rows():
    return build_rows(
        {
            "claude-sonnet-4-5": {
                "provider": "anthropic",
                "mode": "chat",
                "base_model": "claude-sonnet-4-5",
            },
            "gpt-4o": {"provider": "openai", "mode": "chat", "base_model": "gpt-4o"},
            "vertex.ai/claude-sonnet-4-5": {
                "provider": "vertex_ai",
                "mode": "chat",
                "base_model": "claude-sonnet-4-5",
            },
        },
        {"gpt-4o": {"provider": "openai", "input_cost_per_token": 1.0}},
        {"gpt-4o": {"pricing": {"input_cost_per_token": 2.0}},
         "new-model": {"parameters": {"provider": "custom"}}},
        {"gpt-4o": ["input_cost_per_token"]},
    )


def test_root_rowcount_is_not_confused_by_invalid_parent(app, rows):
    model = ModelListModel()
    model.set_rows(rows)
    assert model.rowCount() == 4
    assert model.columnCount() == 5
    # Qt hands Python overrides an invalid QModelIndex for the root count.
    assert model.rowCount(QModelIndex()) == 4
    assert model.rowCount(None) == 4


def test_proxy_exposes_source_rows(app, rows):
    model = ModelListModel()
    model.set_rows(rows)
    proxy = ModelFilterProxy()
    proxy.setSourceModel(model)
    assert proxy.rowCount() == 4


def test_search_is_contains_and_case_insensitive(app, rows):
    model = ModelListModel()
    model.set_rows(rows)
    proxy = ModelFilterProxy()
    proxy.setSourceModel(model)

    proxy.set_search("SONNET")
    assert proxy.rowCount() == 2

    # mid-string fragment: proves `contains`, not `startswith`
    proxy.set_search("onnet")
    assert proxy.rowCount() == 2

    proxy.set_search("nope-nothing")
    assert proxy.rowCount() == 0


def test_search_matches_provider_and_base_model_not_only_id(app, rows):
    model = ModelListModel()
    model.set_rows(rows)
    proxy = ModelFilterProxy()
    proxy.setSourceModel(model)
    proxy.set_search("vertex_ai")
    assert proxy.rowCount() == 1


def test_provider_and_mode_facets_combine_with_search(app, rows):
    model = ModelListModel()
    model.set_rows(rows)
    proxy = ModelFilterProxy()
    proxy.setSourceModel(model)

    proxy.set_providers({"anthropic"})
    assert proxy.rowCount() == 1
    proxy.set_search("openai")
    assert proxy.rowCount() == 0  # facets AND with the search
    proxy.set_search("")
    proxy.set_modes({"chat"})
    assert proxy.rowCount() == 1
    proxy.set_providers(set())
    proxy.set_modes(set())
    assert proxy.rowCount() == 4


def test_status_column_marks_added_and_overridden(app, rows):
    model = ModelListModel()
    model.set_rows(rows)
    proxy = ModelFilterProxy()
    proxy.setSourceModel(model)
    proxy.sort(COL_ID, Qt.AscendingOrder)
    labels = {
        model.row(proxy.mapToSource(proxy.index(i, 0)).row())["id"]: model.data(
            proxy.mapToSource(proxy.index(i, 4)), Qt.DisplayRole
        )
        for i in range(proxy.rowCount())
    }
    assert labels["new-model"] == "added"
    assert labels["claude-sonnet-4-5"] == ""


def test_conflict_badge_wins_over_overridden(app, rows):
    model = ModelListModel()
    model.set_rows(rows)
    labels = {
        model.row(i)["id"]: model.data(model.index(i, 4), Qt.DisplayRole) for i in range(model.rowCount())
    }
    # gpt-4o is both overridden and in conflict; the disagreement is the more
    # actionable signal, so it is what the badge shows.
    assert labels["gpt-4o"] == "conflict (1)"


def test_param_descriptor_model(app):
    model = ParamDescriptorModel()
    model.set_items([{"id": "temperature", "type": "number", "default": 1, "range": {"max": 2}}])
    assert model.rowCount() == 1
    assert model.data(model.index(0, 0)) == "temperature"
    assert model.data(model.index(0, 3)) == "1"
    assert '"max":2' in model.data(model.index(0, 4))
    # non-dict items are dropped rather than crashing data()
    model.set_items([{"id": "ok"}, "junk", 5])
    assert model.rowCount() == 1


def test_change_list_model_renders_all_kinds(app):
    from datasheet_editor.merge import Change

    model = ChangeListModel()
    model.set_changes(
        [
            Change("m", "pricing", "input_cost_per_token", "overridden", 1.0, 2.0),
            Change("m", "parameters", "x", "added", None, "y"),
            Change("n", "pricing", "*", "model_added", None, {"a": 1, "b": 2}),
        ]
    )
    assert model.rowCount() == 3
    assert "1" in model.data(model.index(0, 3))
    assert "new model (2 fields)" == model.data(model.index(2, 3))
