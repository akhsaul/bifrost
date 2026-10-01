"""Qt application bootstrap."""

from __future__ import annotations

from pathlib import Path

from PySide6.QtWidgets import QApplication

from .main_window import MainWindow


def run_gui(
    *,
    original_parameters: str | Path | None = None,
    original_pricing: str | Path | None = None,
    custom: str | Path | None = None,
) -> int:
    """Launch the editor. Returns a process exit code."""
    app = QApplication.instance() or QApplication([])
    window = MainWindow()
    window.show()

    if original_parameters and original_pricing:
        window.load_paths(original_parameters, original_pricing, custom)
    else:
        _auto_discover(window)

    return int(app.exec())


def _auto_discover(window: MainWindow) -> None:
    """Prefer the datasheets sitting next to the working directory."""
    cwd = Path.cwd()
    params = next((p for p in (cwd / "model_parameters.json",) if p.exists()), None)
    pricing = next((p for p in (cwd / "model_pricing.json",) if p.exists()), None)
    custom = next(
        (p for p in (cwd / "custom_model_metadata.json",) if p.exists()),
        None,
    )
    if params and pricing:
        window.load_paths(params, pricing, custom)
