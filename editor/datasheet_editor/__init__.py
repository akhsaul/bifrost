"""Bifrost model datasheet editor: load, overlay, merge, and split these files.

The two datasheets Bifrost consumes are edited through a single custom overlay:

- ``model_parameters.json`` -- capability metadata + the ``model_parameters``
  array that builds parameter forms in the UI.
- ``model_pricing.json`` -- cost fields (plus a duplicated copy of some
  capability fields; Bifrost's ``datasheet.Entry`` struct reads both).

A custom overlay carries a ``parameters`` and/or ``pricing`` section per model,
and the merge writes the two originals back out unchanged in shape.
"""

from __future__ import annotations

__all__ = ["__version__"]

__version__ = "0.1.0"
