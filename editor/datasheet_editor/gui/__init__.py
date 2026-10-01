"""PySide6 GUI for the datasheet editor.

Requires PySide6; the merge engine and CLI do not.
"""

from __future__ import annotations

__all__ = ["run_gui"]


def run_gui(**kwargs) -> int:
    from .app import run_gui as _run_gui

    return _run_gui(**kwargs)
