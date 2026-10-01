"""Where the merged files get written, and what happens when the name is taken.

Bifrost looks the two datasheets up by fixed name, so a second merge into the
same directory has to either replace them or step aside. Both defaults are bad,
so neither is taken silently:

- Replacing would destroy the previous result -- and, when the output directory
  is also where the inputs live, the input itself. ``--force`` already guards
  that specific case, but only for files named on the command line.
- Stepping aside without saying so leaves two files that both look canonical, and
  the stale one is the one an older consumer picks up.

So a name collision is always a question for the user: overwrite, add a number,
or cancel. The CLI cannot ask, so it refuses and names the two flags.

**The two files are moved as a pair.** They are read together and a mismatched
pair is a silent data bug -- ``model_parameters_2.json`` next to a stale
``model_pricing.json`` describes two different datasheets. So if either name is
taken, numbering applies to both, and the suffix is the first number free for
both. When nothing collides, no number is added even if numbered variants happen
to exist: ``model_parameters_1.json`` from an earlier run does not make
``model_parameters.json`` occupied.
"""

from __future__ import annotations

from pathlib import Path

PARAMETERS_FILENAME = "model_parameters.json"
PRICING_FILENAME = "model_pricing.json"

#: Both output names, in the order they are reported.
OUTPUT_FILENAMES = (PARAMETERS_FILENAME, PRICING_FILENAME)

#: Policy names, shared by the GUI buttons and the CLI flags.
OVERWRITE = "overwrite"
ADD_NUMBER = "add_number"
CANCEL = "cancel"

#: Ceiling on the generated suffix, so a folder full of numbered files fails with
#: a clear message instead of scanning until the path length check kills it.
MAX_NUMBERED_SUFFIX = 9999


class OutputConflict(Exception):
    """A file is already at an output name, and no policy was chosen.

    Carries the alternatives so the caller can present them rather than re-derive
    them: :attr:`conflicts` is what is in the way, :attr:`numbered` is where the
    files would go instead.
    """

    def __init__(self, conflicts: list[Path], numbered: dict[str, Path]) -> None:
        self.conflicts = list(conflicts)
        self.numbered = dict(numbered)
        super().__init__(self._describe())

    def _describe(self) -> str:
        names = ", ".join(p.name for p in self.conflicts)
        targets = ", ".join(
            f"{ORIGINAL_NAMES[key]} -> {self.numbered[key].name}" for key in sorted(self.numbered)
        )
        return f"{names} already exist in the output folder; or write {targets}"


#: Reverse lookup for a nicer message, kept next to the conflict so the two
#: filename lists cannot drift apart.
ORIGINAL_NAMES = {PARAMETERS_FILENAME: PARAMETERS_FILENAME, PRICING_FILENAME: PRICING_FILENAME}


def numbered_name(name: str, number: int) -> str:
    """``model_parameters.json`` at 2 -> ``model_parameters_2.json``.

    Only the final extension is treated as the extension, so a dotted stem keeps
    its dots.
    """
    dot = name.rfind(".")
    if dot <= 0:
        return f"{name}_{number}"
    return f"{name[:dot]}_{number}{name[dot:]}"


def _first_free_number(output_dir: Path, filenames: tuple[str, ...]) -> int:
    """Lowest suffix at which every name in *filenames* is free.

    A suffix free for only some of the names is not usable: the pair has to move
    together, so ``model_parameters_2.json`` beside an occupied
    ``model_pricing.json`` is never offered.
    """
    for number in range(1, MAX_NUMBERED_SUFFIX + 1):
        if not any((output_dir / numbered_name(name, number)).exists() for name in filenames):
            return number
    raise OutputConflict(
        conflicts=[output_dir / name for name in filenames if (output_dir / name).exists()],
        numbered={},
    )


def find_conflicts(output_dir: str | Path) -> list[Path]:
    """Output files already present, in reporting order."""
    out = Path(output_dir)
    return [out / name for name in OUTPUT_FILENAMES if (out / name).exists()]


def plan_output(
    output_dir: str | Path,
    *,
    overwrite: bool = False,
    add_number: bool = False,
) -> dict[str, Path]:
    """Resolve where the merged files will be written, keyed by output filename.

    With nothing in the way, returns the plain names. Otherwise one of
    *overwrite* or *add_number* decides, and with neither this raises
    :class:`OutputConflict` so the caller can ask.

    Both policies are rejected together: they are opposite answers to the same
    question, and honouring one while the other was also passed would make the
    command's meaning depend on flag order.
    """
    if overwrite and add_number:
        raise ValueError("overwrite and add_number are mutually exclusive")

    out = Path(output_dir)
    plain = {name: out / name for name in OUTPUT_FILENAMES}
    conflicts = [path for path in plain.values() if path.exists()]

    if not conflicts:
        return plain
    if overwrite:
        return plain

    number = _first_free_number(out, OUTPUT_FILENAMES)
    numbered = {name: out / numbered_name(name, number) for name in OUTPUT_FILENAMES}
    if not add_number:
        raise OutputConflict(conflicts=conflicts, numbered=numbered)
    return numbered


def format_written(paths: dict[str, Path]) -> str:
    """One line per written file, for the CLI's closing summary."""
    return "\n".join(f"wrote {paths[name]}" for name in OUTPUT_FILENAMES)
