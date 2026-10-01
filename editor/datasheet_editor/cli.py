"""Command-line interface for the datasheet editor.

    datasheet merge --original-parameters model_parameters.json \\
                    --original-pricing    model_pricing.json \\
                    --custom              custom_model_metadata.json \\
                    --output              out/

writes ``out/model_parameters.json`` and ``out/model_pricing.json``. The
originals are never modified.
"""

from __future__ import annotations

import argparse
import json
import sys
from pathlib import Path
from typing import Any

from .dataset import DatasetKind, load_dataset, write_json_atomic
from .merge import (
    MergeError,
    MergeResult,
    ParamArrayMode,
    PricingFieldPolicy,
    merge,
)
from .output import (
    OUTPUT_FILENAMES,
    PARAMETERS_FILENAME,
    PRICING_FILENAME,
    find_conflicts,
    format_written,
    plan_output,
)
from .validate import (
    ValidationReport,
    find_cross_file_conflicts,
    format_conflicts,
    validate_overlay,
)

EXIT_OK = 0
EXIT_VALIDATION = 1
EXIT_USAGE = 2
#: A name is already taken in the output folder. Distinct from EXIT_USAGE because
#: the merge itself was fine -- the run is safe to repeat with a flag, which is
#: what a script wrapping this needs to tell apart from a bad invocation.
EXIT_CONFLICT = 3

PARAMETERS_FILENAME = "model_parameters.json"
PRICING_FILENAME = "model_pricing.json"


def _load_overlay(path: Path) -> dict[str, dict[str, Any]]:
    if not path.exists():
        raise MergeError(f"custom overlay not found: {path}")
    try:
        with path.open(encoding="utf-8") as fh:
            raw = json.load(fh)
    except json.JSONDecodeError as exc:
        raise MergeError(f"{path} is not valid JSON: {exc}") from exc
    if not isinstance(raw, dict):
        raise MergeError(f"{path}: expected a JSON object keyed by model ID")
    for model, sections in raw.items():
        if not isinstance(sections, dict):
            raise MergeError(f"{path}: entry {model!r} must be an object")
    return raw


def _guard_output(output: Path, sources: list[Path], force: bool) -> None:
    """Refuse to write an output file on top of one of its own inputs.

    Distinct from the name-collision check in :mod:`datasheet_editor.output`: this
    one protects an *input*, and ``--overwrite`` does not stand in for ``--force``
    there. Overwriting is fine for a previous *output*; it is not fine for the
    file the merge just read, because the run cannot be repeated afterwards.
    """
    out = output.resolve()
    for src in sources:
        if src.exists() and src.resolve() == out:
            if not force:
                raise MergeError(
                    f"refusing to overwrite the input {src} with merged output; "
                    "choose a different --output or pass --force"
                )


def _resolve_output_paths(
    args: argparse.Namespace,
    output_dir: Path,
    params_path: Path,
    pricing_path: Path,
    custom_path: Path,
) -> dict[str, Path] | None:
    """Where the merged pair will be written, or None if the run must stop.

    Returns None after printing the reason, so the caller just returns
    :data:`EXIT_CONFLICT`. Raising ``SystemExit`` here would exit correctly from a
    terminal but make ``main()`` unusable as a callable -- a wrapper importing it
    would be killed rather than handed an exit code.

    Resolved before the merge runs, not after, so a collision is reported in
    milliseconds instead of after loading 20MB and diffing 12,595 models. A
    command that would refuse should not do the work first.
    """
    # Checked before the plan because with --add-number it picks the filenames,
    # and those are the paths --force has to be checked against.
    conflicts = find_conflicts(output_dir)
    if conflicts and not (args.overwrite or args.add_number):
        numbered = plan_output(output_dir, add_number=True)
        print(
            f"error: the output folder already contains {', '.join(p.name for p in conflicts)}.",
            file=sys.stderr,
        )
        print(
            "  --overwrite     replace the existing "
            f"{'file' if len(conflicts) == 1 else 'files'}",
            file=sys.stderr,
        )
        print(
            "  --add-number    keep them and write "
            f"{', '.join(numbered[n].name for n in OUTPUT_FILENAMES)} instead",
            file=sys.stderr,
        )
        print(
            "  (both files move together: parameters and pricing are read as a pair)",
            file=sys.stderr,
        )
        return None

    paths = plan_output(output_dir, overwrite=args.overwrite, add_number=args.add_number)
    _guard_output(paths[PARAMETERS_FILENAME], [params_path, custom_path], args.force)
    _guard_output(paths[PRICING_FILENAME], [pricing_path, custom_path], args.force)
    return paths


def _print_change_report(result: MergeResult, *, verbose: bool, limit: int = 200) -> None:
    print(f"models changed: {len(result.changed_models)}   new models: {len(result.new_models)}")
    counts = result.summary()
    print(
        f"fields added: {counts.get('added', 0)}   "
        f"overridden: {counts.get('overridden', 0)}   "
        f"entries added: {counts.get('model_added', 0)}"
    )
    if result.new_models:
        for model in result.new_models[:20]:
            print(f"  + new model: {model}")
        if len(result.new_models) > 20:
            print(f"  ... and {len(result.new_models) - 20} more")

    if result.changes:
        print("\nchanges:")
        for change in result.changes[:limit]:
            print(f"  {change.describe()}")
        if len(result.changes) > limit:
            print(f"  ... and {len(result.changes) - limit} more (use --report for all)")

    if result.dropped_pricing_fields:
        total = sum(result.dropped_pricing_fields.values())
        print(f"\nWARNING: strict mode dropped {total} field value(s) across "
              f"{len(result.dropped_pricing_fields)} distinct fields that Bifrost's "
              "datasheet.Entry struct does not read:")
        for name, count in sorted(result.dropped_pricing_fields.items(), key=lambda kv: -kv[1])[:20]:
            print(f"  - {name} ({count} entries)")
        if len(result.dropped_pricing_fields) > 20:
            print(f"  ... and {len(result.dropped_pricing_fields) - 20} more")


def _write_report(path: Path, result: MergeResult) -> None:
    payload = {
        "summary": result.summary(),
        "new_models": result.new_models,
        "dropped_pricing_fields": result.dropped_pricing_fields,
        "changes": [
            {
                "model": c.model,
                "section": c.section,
                "path": c.path,
                "kind": c.kind,
                "before": c.before,
                "after": c.after if c.kind != "model_added" else list(c.after),
            }
            for c in result.changes
        ],
    }
    write_json_atomic(payload, path, indent=2)


def cmd_merge(args: argparse.Namespace) -> int:
    params_path = Path(args.original_parameters)
    pricing_path = Path(args.original_pricing)
    custom_path = Path(args.custom)
    output_dir = Path(args.output)
    paths = _resolve_output_paths(args, output_dir, params_path, pricing_path, custom_path)
    if paths is None:
        return EXIT_CONFLICT
    params_out = paths[PARAMETERS_FILENAME]
    pricing_out = paths[PRICING_FILENAME]

    overlay = _load_overlay(custom_path)
    params = load_dataset(params_path, DatasetKind.PARAMETERS)
    pricing = load_dataset(pricing_path, DatasetKind.PRICING)

    print(
        f"loaded {len(params)} parameter models, {len(pricing)} pricing models, "
        f"{len(overlay)} custom overlay entries"
    )

    validation = validate_overlay(overlay, params.data, pricing.data)
    if not args.quiet and len(validation):
        print("\nvalidation findings:")
        print(validation.render(limit=100))
    if not validation.ok:
        print(f"\n{len(validation.errors())} error(s); refusing to merge.", file=sys.stderr)
        return EXIT_VALIDATION

    policy = PricingFieldPolicy(args.pricing_fields)
    if policy is PricingFieldPolicy.STRICT:
        from .fields import pricing_read_set

        unread = {n for e in pricing.data.values() for n in e} - pricing_read_set()
        print(
            f"\nstrict mode: {len(unread)} pricing field(s) are not read by Bifrost's "
            "datasheet.Entry struct and would be removed from the output."
        )
        if unread and not args.strict_confirm:
            print("Re-run with --strict-confirm to accept that, or use the default "
                  "--pricing-fields preserve.", file=sys.stderr)
            return EXIT_USAGE

    try:
        result = merge(
            params.data,
            pricing.data,
            overlay,
            param_array_mode=ParamArrayMode(args.param_array_mode),
            pricing_fields=policy,
        )
    except MergeError as exc:
        print(f"\nmerge error: {exc}", file=sys.stderr)
        return EXIT_VALIDATION

    if not args.quiet:
        _print_change_report(result, verbose=args.verbose)

    if args.report:
        _write_report(Path(args.report), result)
        print(f"\nreport written to {args.report}")

    if args.dry_run:
        print(f"\ndry run: would write {params_out} and {pricing_out}")
        return EXIT_OK

    write_json_atomic(result.parameters, params_out, indent=args.indent, sort_keys=args.sort_keys)
    write_json_atomic(result.pricing, pricing_out, indent=args.indent, sort_keys=args.sort_keys)
    print(f"\n{format_written(paths)}")
    if params_out.name != PARAMETERS_FILENAME:
        # The pair moved, so the originals in this folder are now stale. Saying so
        # matters: a consumer pointed at model_pricing.json here would keep reading
        # the previous run's data and see no error.
        print(
            f"note: {PARAMETERS_FILENAME} and {PRICING_FILENAME} in this folder were left "
            "as they were; the new output is under the numbered names above."
        )

    failures = _verify_output(params_out, pricing_out, result)
    for message in failures:
        print(f"post-write check FAILED: {message}", file=sys.stderr)
    return EXIT_VALIDATION if failures else EXIT_OK


def _verify_output(params_out: Path, pricing_out: Path, result: MergeResult) -> list[str]:
    """Re-read the written files and confirm they round-trip."""
    problems: list[str] = []
    for path, expected, label in (
        (params_out, result.parameters, PARAMETERS_FILENAME),
        (pricing_out, result.pricing, PRICING_FILENAME),
    ):
        try:
            actual = load_dataset(path).data
        except Exception as exc:  # pragma: no cover - corrupt write
            problems.append(f"{label} could not be re-read: {exc}")
            continue
        if len(actual) != len(expected):
            problems.append(f"{label}: wrote {len(actual)} models, expected {len(expected)}")
        missing = set(expected) - set(actual)
        if missing:
            problems.append(f"{label}: {len(missing)} model(s) missing after write, e.g. {sorted(missing)[:3]}")
    return problems


def cmd_diff(args: argparse.Namespace) -> int:
    overlay = _load_overlay(Path(args.custom))
    params = load_dataset(Path(args.original_parameters), DatasetKind.PARAMETERS)
    pricing = load_dataset(Path(args.original_pricing), DatasetKind.PRICING)

    result = merge(params.data, pricing.data, overlay)
    _print_change_report(result, verbose=True)

    conflicts = find_cross_file_conflicts(params.data, pricing.data)
    print()
    print(format_conflicts(conflicts, limit=args.conflict_limit))
    return EXIT_OK


def cmd_validate(args: argparse.Namespace) -> int:
    report = validate_overlay(
        _load_overlay(Path(args.custom)),
        load_dataset(Path(args.original_parameters), DatasetKind.PARAMETERS).data,
        load_dataset(Path(args.original_pricing), DatasetKind.PRICING).data,
    )
    print(report.render(limit=None))
    errors, warnings = len(report.errors()), len(report.warnings())
    print(f"\n{errors} error(s), {warnings} warning(s)")
    return EXIT_OK if not errors else EXIT_VALIDATION


def cmd_info(args: argparse.Namespace) -> int:
    from .fields import pricing_read_set
    from .validate import summarize_unknown_pricing_fields

    path = Path(args.path)
    kind = DatasetKind.PRICING if "pricing" in path.name.lower() else DatasetKind.PARAMETERS
    dataset = load_dataset(path, kind)
    print(f"{path}")
    print(f"  dataset:     {dataset.kind.value}")
    print(f"  models:      {len(dataset)}")
    print(f"  fields:      {len(dataset.known_fields)} distinct")
    print(f"  providers:   {len(dataset.providers())}")
    print(f"  modes:       {len(dataset.modes())}")
    if kind is DatasetKind.PRICING:
        unknown = summarize_unknown_pricing_fields(dataset.data)
        total = sum(unknown.values())
        print(f"  read by Bifrost: {len(pricing_read_set() & dataset.known_fields)} field(s)")
        print(f"  not read:   {len(unknown)} field(s), {total} value(s) would be dropped in strict mode")
    return EXIT_OK


def cmd_gui(args: argparse.Namespace) -> int:
    try:
        from .gui.app import run_gui
    except ImportError as exc:
        print(f"error: the GUI needs PySide6 ({exc})", file=sys.stderr)
        return EXIT_USAGE
    return run_gui(
        original_parameters=args.original_parameters,
        original_pricing=args.original_pricing,
        custom=args.custom,
    )


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        prog="datasheet",
        description="Edit and merge Bifrost model_parameters.json / model_pricing.json.",
    )
    sub = parser.add_subparsers(dest="command", required=True)

    def add_sources(p: argparse.ArgumentParser) -> None:
        p.add_argument("--original-parameters", required=True, help="path to model_parameters.json")
        p.add_argument("--original-pricing", required=True, help="path to model_pricing.json")
        p.add_argument("--custom", required=True, help="path to custom_model_metadata.json")

    p_merge = sub.add_parser("merge", help="merge a custom overlay and write both output files")
    add_sources(p_merge)
    p_merge.add_argument(
        "--output",
        required=True,
        help="output directory; writes model_parameters.json and model_pricing.json into it",
    )
    p_merge.add_argument(
        "--pricing-fields",
        choices=[p.value for p in PricingFieldPolicy],
        default=PricingFieldPolicy.PRESERVE.value,
        help="preserve (default, no data loss) or strict (drop fields Bifrost does not read)",
    )
    p_merge.add_argument(
        "--strict-confirm",
        action="store_true",
        help="required acknowledgement when --pricing-fields strict would drop fields",
    )
    p_merge.add_argument(
        "--param-array-mode",
        choices=[m.value for m in ParamArrayMode],
        default=ParamArrayMode.MERGE.value,
        help="how to combine model_parameters arrays (default: merge by id)",
    )
    p_merge.add_argument("--indent", type=int, default=None, help="pretty-print with this indent")
    p_merge.add_argument("--sort-keys", action="store_true", help="sort object keys on write")
    p_merge.add_argument("--dry-run", action="store_true", help="report without writing")
    p_merge.add_argument("--report", help="write the full change list as JSON")
    p_merge.add_argument(
        "--force", action="store_true",
        help="allow writing the output over one of the input files (separate from --overwrite)",
    )

    collision = p_merge.add_argument_group(
        "output name collisions",
        "When the output folder already holds a file of the same name, the merge stops "
        "rather than picking for you. These two flags are the choices. They are mutually "
        "exclusive, and neither is the default.",
    )
    # A mutually exclusive group, so passing both is rejected by argparse with a
    # usage line rather than raising out of the resolve step. The engine keeps its
    # own check for callers that bypass argparse.
    exclusive = collision.add_mutually_exclusive_group()
    exclusive.add_argument(
        "--overwrite", action="store_true",
        help="replace the existing output file(s)",
    )
    exclusive.add_argument(
        "--add-number", action="store_true", dest="add_number",
        help="keep the existing file(s) and write numbered ones "
             "(model_parameters_1.json and model_pricing_1.json)",
    )
    p_merge.add_argument("--quiet", action="store_true", help="suppress the change report")
    p_merge.add_argument("--verbose", action="store_true", help="print every change")
    p_merge.set_defaults(func=cmd_merge)

    p_diff = sub.add_parser("diff", help="show what a custom overlay would change")
    add_sources(p_diff)
    p_diff.add_argument("--conflict-limit", type=int, default=40)
    p_diff.set_defaults(func=cmd_diff)

    p_val = sub.add_parser("validate", help="validate a custom overlay")
    add_sources(p_val)
    p_val.set_defaults(func=cmd_validate)

    p_info = sub.add_parser("info", help="summarize a datasheet file")
    p_info.add_argument("path")
    p_info.set_defaults(func=cmd_info)

    p_gui = sub.add_parser("gui", help="launch the Qt editor")
    p_gui.add_argument("--original-parameters")
    p_gui.add_argument("--original-pricing")
    p_gui.add_argument("--custom")
    p_gui.set_defaults(func=cmd_gui)

    return parser


def main(argv: list[str] | None = None) -> int:
    parser = build_parser()
    args = parser.parse_args(argv)
    try:
        return int(args.func(args))
    except MergeError as exc:
        print(f"error: {exc}", file=sys.stderr)
        return EXIT_USAGE
    except FileNotFoundError as exc:
        print(f"error: {exc}", file=sys.stderr)
        return EXIT_USAGE


if __name__ == "__main__":
    raise SystemExit(main())
