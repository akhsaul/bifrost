"""Regression tests for load responsiveness.

The reported symptom was: the progress dialog hit 100% within a second, the
window stayed empty and unresponsive for several more seconds, and only then did
the data appear.

Two independent faults produced that, and both are pinned here behaviourally
rather than by wall-clock, so the tests cannot go flaky on a slow machine:

1. The worker claimed its last progress step before the window had done anything
   with the data, so 100% meant "files read", not "data on screen".
2. The main-thread work took ~13.6 seconds for 12,595 models, of which ~8.6s was
   clearing filters that were already empty (three full proxy re-mappings) and
   ~2.4s was a redundant sort of already-sorted rows.
"""

from __future__ import annotations

from pathlib import Path

import pytest

pytest.importorskip("PySide6", reason="PySide6 not installed")

from PySide6.QtWidgets import QApplication  # noqa: E402

from datasheet_editor.gui.models import (  # noqa: E402
    ModelFilterProxy,
    ModelListModel,
    build_rows,
)
from datasheet_editor.gui.workers import BUILD_STEP, LOAD_STEPS, TOTAL_STEPS  # noqa: E402

REPO_EDITOR = Path(__file__).resolve().parents[1]


def _rows(n: int = 3000) -> list[dict]:
    return [
        {
            "id": f"prov-{i}",
            "provider": "openai" if i % 2 else "anthropic",
            "mode": "chat",
            "base_model": f"m{i}",
            "status": "original",
            "conflicts": 0,
            "has_params": True,
            "has_pricing": True,
            "has_param_array": False,
            "search_blob": f"prov-{i} openai chat m{i}",
        }
        for i in range(n)
    ]


@pytest.fixture(scope="session")
def app():
    return QApplication.instance() or QApplication([])


# --------------------------------------------------------------------------- #
# progress honesty
# --------------------------------------------------------------------------- #


def test_final_step_is_reserved_for_the_window():
    """The worker must not own the last step, or 100% arrives before the data."""
    assert BUILD_STEP == LOAD_STEPS
    assert TOTAL_STEPS == LOAD_STEPS + 1


def test_build_rows_emits_models_in_ascending_id_order():
    """Why the redundant sort was dropped: the rows are already ordered."""
    rows = build_rows({"b-model": {}, "a-model": {}, "c-model": {}}, {}, {})
    assert [r["id"] for r in rows] == ["a-model", "b-model", "c-model"]


# --------------------------------------------------------------------------- #
# the redundant sort
# --------------------------------------------------------------------------- #


def test_reset_source_does_not_force_a_sort(app):
    """reset_source leaves sortColumn() at -1, so no re-mapping pass is paid."""
    source = ModelListModel()
    proxy = ModelFilterProxy()
    proxy.setSourceModel(source)
    source.attach_proxy(proxy)

    source.reset_source(_rows())
    assert proxy.rowCount() == len(_rows())
    assert proxy.sortColumn() == -1  # nothing was sorted


# --------------------------------------------------------------------------- #
# the proxy re-mapping storm
# --------------------------------------------------------------------------- #


def test_reset_source_detaches_the_proxy_while_resetting(app):
    """Detaching during set_rows is what turns ~2.4s into ~30ms."""
    source = ModelListModel()
    proxy = ModelFilterProxy()
    proxy.setSourceModel(source)
    source.attach_proxy(proxy)

    seen: list[object] = []
    original_set_rows = source.set_rows

    def spy(rows):
        # While the proxy is detached the source model is the only watcher, so
        # endResetModel cannot trigger a full re-mapping.
        seen.append(proxy.sourceModel())
        original_set_rows(rows)

    source.set_rows = spy
    source.reset_source(_rows())

    assert seen == [None], "proxy should be detached for the duration of set_rows"
    assert proxy.sourceModel() is source, "proxy must be re-attached afterwards"


def test_reset_source_reattaches_even_if_set_rows_raises(app):
    source = ModelListModel()
    proxy = ModelFilterProxy()
    proxy.setSourceModel(source)
    source.attach_proxy(proxy)

    def boom(rows):
        raise RuntimeError("set_rows failed")

    source.set_rows = boom
    with pytest.raises(RuntimeError):
        source.reset_source(_rows())

    assert proxy.sourceModel() is source, "a failure must not leave the proxy orphaned"


def test_set_filters_with_identical_values_does_no_work(app):
    """Clearing already-empty filters was ~2.6s of re-mapping for no change."""
    source = ModelListModel()
    proxy = ModelFilterProxy()
    proxy.setSourceModel(source)
    source.attach_proxy(proxy)
    source.reset_source(_rows())

    calls = {"n": 0}
    original_invalidate = proxy._invalidate

    def counting_invalidate():
        calls["n"] += 1
        original_invalidate()

    proxy._invalidate = counting_invalidate

    proxy.set_filters("", "", set(), set())  # same as the starting state
    assert calls["n"] == 0

    proxy.set_filters("sonnet", "", set(), set())  # real change
    assert calls["n"] == 1

    proxy.set_filters("sonnet", "", set(), set())  # no change again
    assert calls["n"] == 1


def test_set_search_delegates_to_the_coalesced_call(app):
    source = ModelListModel()
    proxy = ModelFilterProxy()
    proxy.setSourceModel(source)
    source.reset_source(_rows())

    calls: list[tuple] = []
    original = proxy.set_filters

    def spy(needle, provider_needle, providers, modes):
        calls.append((needle, provider_needle, providers, modes))
        original(needle, provider_needle, providers, modes)

    proxy.set_filters = spy
    proxy.set_search("gpt")
    proxy.set_providers({"openai"})
    proxy.set_modes({"chat"})

    assert len(calls) == 3
    # Each single-filter setter must preserve the other two conditions.
    assert calls[0] == ("gpt", "", set(), set())
    assert calls[1] == ("gpt", "", {"openai"}, set())
    assert calls[2] == ("gpt", "", {"openai"}, {"chat"})


def test_unfiltered_accept_is_a_short_circuit(app):
    """With nothing filtered, filterAcceptsRow must not touch the row at all.

    Every row lookup is a Python call across the Qt/C++ boundary; doing 12,595 of
    them measurably delays the first paint.
    """
    source = ModelListModel()
    proxy = ModelFilterProxy()
    proxy.setSourceModel(source)

    calls = {"n": 0}
    original_row = source.row

    def counting_row(index):
        calls["n"] += 1
        return original_row(index)

    source.row = counting_row
    source.reset_source(_rows())

    calls["n"] = 0
    assert proxy.filterAcceptsRow(0, None) is True
    assert calls["n"] == 0, "unfiltered accept must not read the row"

    proxy.set_filters("m1", "", set(), set())
    calls["n"] = 0
    assert proxy.filterAcceptsRow(1, None) is True
    assert calls["n"] == 1, "a real filter must consult the row"


# --------------------------------------------------------------------------- #
# behaviour is unchanged
# --------------------------------------------------------------------------- #


def test_filters_still_work_after_the_optimisations(app):
    rows = build_rows(
        {
            "claude-sonnet-4-5": {"provider": "anthropic", "mode": "chat"},
            "gpt-4o": {"provider": "openai", "mode": "chat"},
            "text-embedding-3": {"provider": "openai", "mode": "embedding"},
        },
        {},
        {},
    )
    source = ModelListModel()
    proxy = ModelFilterProxy()
    proxy.setSourceModel(source)
    source.attach_proxy(proxy)
    source.reset_source(rows)

    assert proxy.rowCount() == 3
    proxy.set_filters("sonnet", "", set(), set())
    assert proxy.rowCount() == 1
    proxy.set_filters("", "", {"openai"}, set())
    assert proxy.rowCount() == 2
    proxy.set_filters("", "", set(), {"embedding"})
    assert proxy.rowCount() == 1
    proxy.set_filters("", "", set(), set())
    assert proxy.rowCount() == 3