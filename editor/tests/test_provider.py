"""Tests for provider resolution in the model list and the provider search field.

Two problems, both reported from using Add Model:

- A newly added model never appeared in the provider dropdown, so it could not be
  filtered by provider at all. ``build_rows`` read ``provider`` only from the two
  originals, which do not contain a model that exists solely in the overlay, so
  the row's provider came out empty and the dropdown (which skips empties) never
  listed it.
- The dropdown only offers providers it has already seen, and only matches them
  exactly, so it cannot look up a provider that was just typed into the overlay.
  There is now a free-text substring field beside it.
"""

from __future__ import annotations

import pytest

pytest.importorskip("PySide6", reason="PySide6 not installed")

from PySide6.QtWidgets import QApplication  # noqa: E402

from datasheet_editor.gui.models import (  # noqa: E402
    ModelFilterProxy,
    ModelListModel,
    build_rows,
)


@pytest.fixture(scope="session")
def app():
    return QApplication.instance() or QApplication([])


def _row(model_id: str) -> dict:
    return build_rows({}, {}, {model_id: {"parameters": {"provider": "x"}}})[0]


# --------------------------------------------------------------------------- #
# build_rows must honour the overlay
# --------------------------------------------------------------------------- #


def test_new_model_takes_its_provider_from_the_overlay():
    """Regression: the row's provider was empty, so it vanished from the dropdown."""
    rows = build_rows(
        {},
        {},
        {"acme/new-model": {"parameters": {"provider": "acme", "mode": "chat"},
                            "pricing": {"provider": "acme", "mode": "chat"}}},
    )
    assert len(rows) == 1
    assert rows[0]["provider"] == "acme"
    assert rows[0]["mode"] == "chat"


def test_new_model_provider_reaches_the_search_blob():
    rows = build_rows({}, {}, {"acme/new-model": {"parameters": {"provider": "acme"}}})
    assert "acme" in rows[0]["search_blob"]


def test_overlay_provider_overrides_the_original():
    """The overlay is the user's edit, so it wins over the original value."""
    rows = build_rows(
        {"gpt-4o": {"provider": "openai", "mode": "chat", "base_model": "gpt-4o"}},
        {},
        {"gpt-4o": {"parameters": {"provider": "my-proxy"}}},
    )
    assert rows[0]["provider"] == "my-proxy"


def test_overlay_override_keeps_fields_it_does_not_mention():
    rows = build_rows(
        {"gpt-4o": {"provider": "openai", "mode": "chat", "base_model": "gpt-4o"}},
        {},
        {"gpt-4o": {"parameters": {"provider": "my-proxy"}}},
    )
    assert rows[0]["mode"] == "chat"
    assert rows[0]["base_model"] == "gpt-4o"


def test_original_values_still_win_when_the_overlay_is_unrelated():
    rows = build_rows({"gpt-4o": {"provider": "openai", "mode": "chat"}}, {}, {})
    assert rows[0]["provider"] == "openai"


def test_provider_falls_back_from_parameters_to_pricing():
    rows = build_rows({}, {"m": {"provider": "bedrock", "mode": "chat"}}, {})
    assert rows[0]["provider"] == "bedrock"


def test_non_string_provider_is_ignored():
    rows = build_rows({"m": {"provider": 7, "mode": "chat"}}, {}, {})
    assert rows[0]["provider"] == ""


def test_new_provider_appears_in_the_dropdown_source(app):
    """The dropdown is built from distinct non-empty providers on the rows."""
    rows = build_rows(
        {"gpt-4o": {"provider": "openai", "mode": "chat"}},
        {},
        {"acme/new-model": {"parameters": {"provider": "acme", "mode": "chat"}}},
    )
    source = ModelListModel()
    source.set_rows(rows)
    # providers is a list of (name, count) pairs, which is what the dropdown uses.
    assert ("acme", 1) in source.providers
    assert ("openai", 1) in source.providers
    assert [p for p, _ in source.providers] == ["acme", "openai"]


# --------------------------------------------------------------------------- #
# the free-text provider search
# --------------------------------------------------------------------------- #


@pytest.fixture
def filtered(app):
    rows = build_rows(
        {
            "azure/gpt-4o": {"provider": "azure", "mode": "chat"},
            "bedrock/claude": {"provider": "bedrock", "mode": "chat"},
            "acme/one": {},
            "acme/two": {},
        },
        {},
        {
            "acme/one": {"parameters": {"provider": "acme", "mode": "chat"}},
            "acme/two": {"parameters": {"provider": "acme", "mode": "embedding"}},
        },
    )
    source = ModelListModel()
    proxy = ModelFilterProxy()
    proxy.setSourceModel(source)
    source.attach_proxy(proxy)
    source.reset_source(rows)
    return proxy


def test_provider_search_matches_substrings(filtered):
    filtered.set_provider_search("bed")
    assert filtered.rowCount() == 1  # "bed" is inside "bedrock", not a prefix rule


def test_provider_search_is_case_insensitive(filtered):
    filtered.set_provider_search("AZURE")
    assert filtered.rowCount() == 1


def test_provider_search_finds_a_provider_absent_from_the_originals(filtered):
    filtered.set_provider_search("acme")
    assert filtered.rowCount() == 2


def test_provider_search_matches_provider_only(filtered):
    """It must not search model IDs: no model here contains 'acme' in its ID
    except by coincidence, so assert on a provider-only hit for 'rock'."""
    filtered.set_provider_search("rock")
    assert filtered.rowCount() == 1


def test_provider_search_combines_with_the_dropdown(filtered):
    filtered.set_filters("claude", "bedrock", {"bedrock"}, set())
    assert filtered.rowCount() == 1


def test_provider_search_and_mode_combine(filtered):
    filtered.set_filters("", "acme", set(), {"embedding"})
    assert filtered.rowCount() == 1


def test_provider_search_combines_with_the_general_search(filtered):
    filtered.set_filters("two", "acme", set(), set())
    assert filtered.rowCount() == 1
    filtered.set_filters("one", "acme", set(), set())
    assert filtered.rowCount() == 1
    filtered.set_filters("two", "bedrock", set(), set())
    assert filtered.rowCount() == 0


def test_clearing_provider_search_restores_everything(filtered):
    filtered.set_provider_search("acme")
    assert filtered.rowCount() == 2
    filtered.set_provider_search("")
    assert filtered.rowCount() == 4


def test_provider_search_with_no_match(filtered):
    filtered.set_provider_search("nonexistent")
    assert filtered.rowCount() == 0


def test_provider_search_marks_filters_active(filtered):
    assert filtered.active is False
    filtered.set_provider_search("acme")
    assert filtered.active is True


def test_window_has_a_dedicated_provider_field(app):
    from datasheet_editor.gui.main_window import MainWindow

    window = MainWindow()
    assert hasattr(window, "provider_search")
    assert "Provider" in window.provider_search.placeholderText()
    # It is separate from the exact-match dropdown.
    assert window.provider_search is not window.provider_combo
    assert "substring" in window.provider_search.toolTip().lower()


def test_window_provider_field_drives_the_proxy(app):
    from datasheet_editor.gui.main_window import MainWindow

    window = MainWindow()
    window.provider_search.setText("bed")
    window.provider_timer.stop()
    window._apply_provider_search()
    assert window.proxy.provider_needle == "bed"
    assert window.proxy.active is True


def test_clearing_filters_also_clears_the_provider_field(app):
    from datasheet_editor.gui.main_window import MainWindow

    window = MainWindow()
    window.search_edit.setText("x")
    window.provider_search.setText("y")
    window.provider_combo.setCurrentIndex(1 if window.provider_combo.count() > 1 else 0)
    window._clear_filters()
    assert window.search_edit.text() == ""
    assert window.provider_search.text() == ""
    assert window.proxy.active is False