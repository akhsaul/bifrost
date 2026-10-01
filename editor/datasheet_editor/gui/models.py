"""Qt item models for the datasheet editor."""

from __future__ import annotations

from typing import Any

from PySide6.QtCore import (
    QAbstractTableModel,
    QModelIndex,
    QObject,
    QSortFilterProxyModel,
    Qt,
    Signal,
)

from ..merge import PARAMS_FIELD

#: Columns in the model list.
COL_ID = 0
COL_PROVIDER = 1
COL_MODE = 2
COL_BASE_MODEL = 3
COL_STATUS = 4
COLUMN_HEADERS = ["Model ID", "Provider", "Mode", "Base model", "Status"]
COLUMN_COUNT = len(COLUMN_HEADERS)

#: Status values shown in the status column.
STATUS_ORIGINAL = "original"
STATUS_OVERRIDDEN = "overridden"
STATUS_ADDED = "added"
STATUS_CONFLICT = "conflict"


class ModelListModel(QAbstractTableModel):
    """Flat, read-only list of every model across both datasets plus the overlay.

    Rows are built once per load. Searchable fields are casefolded up front so
    the filter proxy does not lowercase 12,595 strings per keystroke.
    """

    def __init__(self, parent: QObject | None = None) -> None:
        super().__init__(parent)
        self._rows: list[dict[str, Any]] = []
        self._index: dict[str, int] = {}
        self._providers: list[str] = []
        self._modes: list[str] = []
        self._provider_counts: dict[str, int] = {}
        self._mode_counts: dict[str, int] = {}

    # -- population ------------------------------------------------------- #

    def set_rows(self, rows: list[dict[str, Any]]) -> None:
        self.beginResetModel()
        self._rows = rows
        self._index = {row["id"]: i for i, row in enumerate(rows)}
        self._providers = sorted({r["provider"] for r in rows if r["provider"]})
        self._modes = sorted({r["mode"] for r in rows if r["mode"]})
        self._provider_counts = {}
        for row in rows:
            if row["provider"]:
                self._provider_counts[row["provider"]] = self._provider_counts.get(row["provider"], 0) + 1
            if row["mode"]:
                self._mode_counts[row["mode"]] = self._mode_counts.get(row["mode"], 0) + 1
        self.endResetModel()

    # -- accessors -------------------------------------------------------- #

    def row(self, index: int) -> dict[str, Any] | None:
        if 0 <= index < len(self._rows):
            return self._rows[index]
        return None

    def row_for_id(self, model_id: str) -> int:
        return self._index.get(model_id, -1)

    @property
    def providers(self) -> list[tuple[str, int]]:
        return [(p, self._provider_counts[p]) for p in self._providers]

    @property
    def modes(self) -> list[tuple[str, int]]:
        return [(m, self._mode_counts[m]) for m in self._modes]

    def provider_counts(self) -> dict[str, int]:
        return dict(self._provider_counts)

    # -- QAbstractTableModel ---------------------------------------------- #

    def rowCount(self, parent: QModelIndex | None = None) -> int:  # noqa: N802
        # Qt passes an *invalid* QModelIndex() (not None) from C++ when asking
        # for the root count. Testing `is not None` here would make every table
        # report zero rows and leave the filter proxy empty.
        if parent is not None and parent.isValid():
            return 0
        return len(self._rows)

    def columnCount(self, parent: QModelIndex | None = None) -> int:  # noqa: N802
        if parent is not None and parent.isValid():
            return 0
        return COLUMN_COUNT

    def headerData(self, section: int, orientation: Qt.Orientation, role: int = Qt.DisplayRole) -> Any:  # noqa: N802
        if role == Qt.DisplayRole and orientation == Qt.Horizontal:
            return COLUMN_HEADERS[section]
        return None

    def data(self, index: QModelIndex, role: int = Qt.DisplayRole) -> Any:
        if not index.isValid():
            return None
        row = self._rows[index.row()]
        col = index.column()

        if role == Qt.DisplayRole:
            if col == COL_STATUS:
                return _status_label(row)
            return row[_COLUMN_FIELDS[col]]

        if role == Qt.ToolTipRole:
            return row["id"] if col == COL_ID else None

        if role == Qt.TextAlignmentRole and col in (COL_ID, COL_BASE_MODEL):
            return int(Qt.AlignLeft | Qt.AlignVCenter)

        if role == Qt.ForegroundRole and col == COL_STATUS:
            return _status_color(row)
        return None


_COLUMN_FIELDS = ("id", "provider", "mode", "base_model", "status")


def _status_label(row: dict[str, Any]) -> str:
    if row.get("status") == STATUS_ADDED:
        return "added"
    if row.get("conflicts"):
        return f"conflict ({row['conflicts']})"
    if row.get("status") == STATUS_OVERRIDDEN:
        return "overridden"
    return ""


def _status_color(row: dict[str, Any]):
    from PySide6.QtGui import QBrush, QColor

    if row.get("status") == STATUS_ADDED:
        return QBrush(QColor("#16a34a"))
    if row.get("conflicts"):
        return QBrush(QColor("#d97706"))
    if row.get("status") == STATUS_OVERRIDDEN:
        return QBrush(QColor("#2563eb"))
    return None


class ModelFilterProxy(QSortFilterProxyModel):
    """Case-insensitive substring search plus provider/mode facets.

    The search is a plain ``contains`` on folded text -- never a regex and never
    a prefix match, so a mid-string fragment like ``sonnet`` finds
    ``claude-3-5-sonnet``.
    """

    def __init__(self, parent: QObject | None = None) -> None:
        super().__init__(parent)
        self._needle = ""
        self._providers: set[str] = set()
        self._modes: set[str] = set()
        self.setDynamicSortFilter(True)

    def set_search(self, text: str) -> None:
        self._needle = text.strip().casefold()
        self._invalidate()

    def set_providers(self, providers: set[str]) -> None:
        self._providers = providers
        self._invalidate()

    def set_modes(self, modes: set[str]) -> None:
        self._modes = modes
        self._invalidate()

    def _invalidate(self) -> None:
        """Re-evaluate the filter.

        ``invalidateFilter()`` and ``invalidateRowsFilter()`` are both deprecated
        in Qt 6, leaving only ``invalidate()`` -- which also clears the sort. The
        active sort column and order are captured and re-applied so typing in the
        search box does not throw away the user's chosen column order.
        """
        column = self.sortColumn()
        order = self.sortOrder()
        self.invalidate()
        if column >= 0:
            self.sort(column, order)

    @property
    def needle(self) -> str:
        return self._needle

    @property
    def active(self) -> bool:
        return bool(self._needle or self._providers or self._modes)

    def filterAcceptsRow(self, source_row: int, source_parent: QModelIndex) -> bool:  # noqa: N802
        model = self.sourceModel()
        if not isinstance(model, ModelListModel):
            return True
        row = model.row(source_row)
        if row is None:
            return False

        if self._providers and row.get("provider") not in self._providers:
            return False
        if self._modes and row.get("mode") not in self._modes:
            return False
        if self._needle and self._needle not in row.get("search_blob", ""):
            return False
        return True


class ParamDescriptorModel(QAbstractTableModel):
    """The ``model_parameters`` array of one model, one row per descriptor."""

    COL_ID = 0
    COL_LABEL = 1
    COL_TYPE = 2
    COL_DEFAULT = 3
    COL_RANGE = 4
    HEADERS = ["id", "label", "type", "default", "range"]

    def __init__(self, parent: QObject | None = None) -> None:
        super().__init__(parent)
        self._items: list[dict[str, Any]] = []

    def set_items(self, items: list[dict[str, Any]]) -> None:
        self.beginResetModel()
        self._items = [i for i in items if isinstance(i, dict)]
        self.endResetModel()

    def items(self) -> list[dict[str, Any]]:
        return self._items

    def item_at(self, row: int) -> dict[str, Any] | None:
        return self._items[row] if 0 <= row < len(self._items) else None

    def rowCount(self, parent: QModelIndex | None = None) -> int:  # noqa: N802
        if parent is not None and parent.isValid():
            return 0
        return len(self._items)

    def columnCount(self, parent: QModelIndex | None = None) -> int:  # noqa: N802
        if parent is not None and parent.isValid():
            return 0
        return len(self.HEADERS)

    def headerData(self, section: int, orientation: Qt.Orientation, role: int = Qt.DisplayRole) -> Any:  # noqa: N802
        if role == Qt.DisplayRole and orientation == Qt.Horizontal:
            return self.HEADERS[section]
        return None

    def data(self, index: QModelIndex, role: int = Qt.DisplayRole) -> Any:
        if not index.isValid():
            return None
        item = self._items[index.row()]
        key = ("id", "label", "type", "default", "range")[index.column()]
        value = item.get(key)
        if role == Qt.DisplayRole:
            if isinstance(value, (dict, list)):
                import json

                return json.dumps(value, separators=(",", ":"))
            return "" if value is None else str(value)
        if role == Qt.EditRole:
            return value
        return None

    def flags(self, index: QModelIndex) -> Qt.ItemFlag:  # noqa: N802
        base = Qt.ItemIsEnabled | Qt.ItemIsSelectable
        if index.column() in (0, 1, 2, 3):
            return base | Qt.ItemIsEditable
        return base


class ChangeListModel(QAbstractTableModel):
    """Merge preview: one row per field-level change."""

    HEADERS = ["Model", "Section", "Field", "Change"]

    def __init__(self, parent: QObject | None = None) -> None:
        super().__init__(parent)
        self._changes: list[Any] = []

    def set_changes(self, changes: list[Any]) -> None:
        self.beginResetModel()
        self._changes = changes
        self.endResetModel()

    def rowCount(self, parent: QModelIndex | None = None) -> int:  # noqa: N802
        if parent is not None and parent.isValid():
            return 0
        return len(self._changes)

    def columnCount(self, parent: QModelIndex | None = None) -> int:  # noqa: N802
        if parent is not None and parent.isValid():
            return 0
        return len(self.HEADERS)

    def headerData(self, section: int, orientation: Qt.Orientation, role: int = Qt.DisplayRole) -> Any:  # noqa: N802
        if role == Qt.DisplayRole and orientation == Qt.Horizontal:
            return self.HEADERS[section]
        return None

    def data(self, index: QModelIndex, role: int = Qt.DisplayRole) -> Any:
        if not index.isValid() or role != Qt.DisplayRole:
            return None
        change = self._changes[index.row()]
        col = index.column()
        if col == 0:
            return change.model
        if col == 1:
            return change.section
        if col == 2:
            return change.path
        if col == 3:
            if change.kind == "model_added":
                return f"new model ({len(change.after)} fields)"
            if change.kind == "added":
                return f"= {_compact(change.after)}"
            return f"{_compact(change.before)} → {_compact(change.after)}"
        return None


def _compact(value: Any, limit: int = 60) -> str:
    import json

    try:
        text = json.dumps(value, separators=(",", ":"))
    except (TypeError, ValueError):
        text = str(value)
    return text if len(text) <= limit else text[: limit - 1] + "…"


def build_rows(
    parameters: dict[str, dict[str, Any]],
    pricing: dict[str, dict[str, Any]],
    overlay: dict[str, dict[str, Any]],
    conflicts: dict[str, list[str]] | None = None,
) -> list[dict[str, Any]]:
    """Flatten both datasets and the overlay into display rows."""
    conflicts = conflicts or {}
    rows: list[dict[str, Any]] = []

    for model_id in sorted(set(parameters) | set(pricing) | set(overlay)):
        params_entry = parameters.get(model_id) or {}
        pricing_entry = pricing.get(model_id) or {}
        sections = overlay.get(model_id) or {}

        provider = params_entry.get("provider") or pricing_entry.get("provider") or ""
        mode = params_entry.get("mode") or pricing_entry.get("mode") or ""
        base_model = params_entry.get("base_model") or pricing_entry.get("base_model") or ""

        if model_id not in parameters and model_id not in pricing:
            status = STATUS_ADDED
        elif sections:
            status = STATUS_OVERRIDDEN
        else:
            status = STATUS_ORIGINAL

        row = {
            "id": model_id,
            "provider": provider if isinstance(provider, str) else "",
            "mode": mode if isinstance(mode, str) else "",
            "base_model": base_model if isinstance(base_model, str) else "",
            "status": status,
            "conflicts": len(conflicts.get(model_id, ())),
            "has_params": bool(params_entry),
            "has_pricing": bool(pricing_entry),
            "has_param_array": isinstance(params_entry.get(PARAMS_FIELD), list),
        }
        row["search_blob"] = " ".join(
            (row["id"], row["provider"], row["mode"], row["base_model"])
        ).casefold()
        rows.append(row)

    return rows
