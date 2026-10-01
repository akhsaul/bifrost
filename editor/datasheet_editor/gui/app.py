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
    """Launch the editor. Returns a process exit code.

    Files are never auto-discovered. Matching on a filename is not safe here: the
    working directory routinely holds several datasheet copies (``*_beauty.json``,
    generated output, an unrelated model's sheet), and silently opening the wrong
    one would be worse than asking. Only paths passed explicitly on the command
    line are loaded; otherwise the user picks each file with its own button.
    """
    app = QApplication.instance() or QApplication([])
    window = MainWindow()
    window.show()

    if original_parameters and original_pricing:
        window.load_paths(original_parameters, original_pricing, custom)
    else:
        window.show_start_hint()

    return int(app.exec())
