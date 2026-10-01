"""Output-filename collision handling, and the refusal that precedes a merge.

The rule being pinned: a merge never picks between overwriting and numbering on
the user's behalf. The CLI asks with an error and names both flags; the GUI asks
with two buttons. And the two files move as a pair, because parameters and
pricing read as one datasheet -- ``model_parameters_2.json`` beside a stale
``model_pricing.json`` is two different datasheets wearing one name.

There was no test_cli.py, so the CLI's exit codes and flag parsing were never
covered; these exercise ``main()`` end to end rather than calling ``cmd_merge``
directly, because the contract that matters is the exit code a script sees.
"""

from __future__ import annotations

import json

import pytest

from datasheet_editor import cli
from datasheet_editor.output import (
    ADD_NUMBER,
    MAX_NUMBERED_SUFFIX,
    OVERWRITE,
    OUTPUT_FILENAMES,
    OutputConflict,
    find_conflicts,
    numbered_name,
    plan_output,
)

PARAMS = {
    "grok-4.20": {
        "mode": "chat",
        "max_output_tokens": 1000000,
        "model_parameters": [
            {"id": "temperature", "type": "number", "default": 1, "label": "Temp"},
        ],
    }
}
PRICING = {
    "grok-4.20": {
        "provider": "xai",
        "input_cost_per_token": 3e-06,
        "output_cost_per_token": 1.5e-05,
    }
}
OVERLAY = {"grok-4.20": {"parameters": {"max_output_tokens": 32768}}}


@pytest.fixture
def sheets(tmp_path):
    """An original pair, an overlay, and a distinct output folder."""
    params = tmp_path / "in_model_parameters.json"
    pricing = tmp_path / "in_model_pricing.json"
    custom = tmp_path / "custom_model_metadata.json"
    params.write_text(json.dumps(PARAMS), encoding="utf-8")
    pricing.write_text(json.dumps(PRICING), encoding="utf-8")
    custom.write_text(json.dumps(OVERLAY), encoding="utf-8")
    out = tmp_path / "out"
    out.mkdir()
    return params, pricing, custom, out


def run_merge(sheets, out, *extra):
    params, pricing, custom, _ = sheets
    return cli.main([
        "merge",
        "--original-parameters", str(params),
        "--original-pricing", str(pricing),
        "--custom", str(custom),
        "--output", str(out),
        "--quiet",
        *extra,
    ])


# --------------------------------------------------------------------------- #
# naming
# --------------------------------------------------------------------------- #


@pytest.mark.parametrize(
    "name, number, expected",
    [
        ("model_parameters.json", 1, "model_parameters_1.json"),
        ("model_pricing.json", 12, "model_pricing_12.json"),
        ("a.b.c.json", 3, "a.b.c_3.json"),
        ("noext", 2, "noext_2"),
        (".hidden", 1, ".hidden_1"),
    ],
)
def test_the_number_goes_before_the_extension(name, number, expected):
    assert numbered_name(name, number) == expected


def test_numbering_keeps_the_pair_together(tmp_path):
    """The whole point: one number, both files, always."""
    (tmp_path / "model_parameters.json").write_text("{}", encoding="utf-8")
    paths = plan_output(tmp_path, add_number=True)
    assert paths["model_parameters.json"].name == "model_parameters_1.json"
    assert paths["model_pricing.json"].name == "model_pricing_1.json"


def test_a_partial_collision_still_moves_both_files(tmp_path):
    """Only pricing is taken; parameters must not stay behind unmoved.

    Half-moved is the failure that leaves parameters_1.json describing a
    different model set than the pricing.json beside it.
    """
    (tmp_path / "model_pricing.json").write_text("{}", encoding="utf-8")
    paths = plan_output(tmp_path, add_number=True)
    assert [paths[n].name for n in OUTPUT_FILENAMES] == [
        "model_parameters_1.json", "model_pricing_1.json"
    ]


def test_the_number_skips_suffixes_already_taken(tmp_path):
    (tmp_path / "model_parameters.json").write_text("{}", encoding="utf-8")
    (tmp_path / "model_pricing.json").write_text("{}", encoding="utf-8")
    for n in (1, 2):
        (tmp_path / f"model_parameters_{n}.json").write_text("{}", encoding="utf-8")
    paths = plan_output(tmp_path, add_number=True)
    assert paths["model_parameters.json"].name == "model_parameters_3.json"


def test_a_half_taken_suffix_is_skipped_too(tmp_path):
    """_1 free for pricing but not parameters is not a usable suffix."""
    (tmp_path / "model_parameters.json").write_text("{}", encoding="utf-8")
    (tmp_path / "model_parameters_1.json").write_text("{}", encoding="utf-8")
    paths = plan_output(tmp_path, add_number=True)
    assert paths["model_parameters.json"].name == "model_parameters_2.json"


def test_a_pre_existing_numbered_file_does_not_make_the_plain_name_busy(tmp_path):
    """Only the name actually being written counts as a conflict."""
    (tmp_path / "model_parameters_1.json").write_text("{}", encoding="utf-8")
    assert find_conflicts(tmp_path) == []
    assert plan_output(tmp_path)["model_parameters.json"].name == "model_parameters.json"


def test_a_full_folder_fails_instead_of_scanning_forever(tmp_path, monkeypatch):
    monkeypatch.setattr("datasheet_editor.output.MAX_NUMBERED_SUFFIX", 3)
    (tmp_path / "model_parameters.json").write_text("{}", encoding="utf-8")
    for n in (1, 2, 3):
        (tmp_path / f"model_parameters_{n}.json").write_text("{}", encoding="utf-8")
    with pytest.raises(OutputConflict):
        plan_output(tmp_path, add_number=True)


def test_the_ceiling_is_a_finite_number():
    assert 0 < MAX_NUMBERED_SUFFIX < 10**6


# --------------------------------------------------------------------------- #
# the policy itself
# --------------------------------------------------------------------------- #


def test_no_collision_means_no_number(tmp_path):
    assert plan_output(tmp_path)["model_parameters.json"].name == "model_parameters.json"


def test_a_collision_without_a_policy_raises(tmp_path):
    (tmp_path / "model_parameters.json").write_text("{}", encoding="utf-8")
    with pytest.raises(OutputConflict):
        plan_output(tmp_path)


def test_overwrite_keeps_the_plain_name(tmp_path):
    (tmp_path / "model_parameters.json").write_text("{}", encoding="utf-8")
    paths = plan_output(tmp_path, overwrite=True)
    assert paths["model_parameters.json"].name == "model_parameters.json"


def test_the_two_policies_are_mutually_exclusive(tmp_path):
    """Opposite answers to one question; accepting both would be order-dependent."""
    with pytest.raises(ValueError):
        plan_output(tmp_path, overwrite=True, add_number=True)


def test_the_conflict_carries_its_own_alternatives(tmp_path):
    (tmp_path / "model_parameters.json").write_text("{}", encoding="utf-8")
    with pytest.raises(OutputConflict) as caught:
        plan_output(tmp_path)
    assert [p.name for p in caught.value.conflicts] == ["model_parameters.json"]
    assert caught.value.numbered["model_parameters.json"].name == "model_parameters_1.json"


# --------------------------------------------------------------------------- #
# the CLI refuses
# --------------------------------------------------------------------------- #


def test_the_cli_refuses_when_the_name_is_taken(sheets, tmp_path, capsys):
    (sheets[3] / "model_parameters.json").write_text("{}", encoding="utf-8")
    assert run_merge(sheets, sheets[3]) == cli.EXIT_CONFLICT


def test_the_error_names_both_flags(sheets, capsys):
    (sheets[3] / "model_parameters.json").write_text("{}", encoding="utf-8")
    run_merge(sheets, sheets[3])
    err = capsys.readouterr().err
    assert "--overwrite" in err
    assert "--add-number" in err


def test_the_error_says_which_file_is_in_the_way(sheets, capsys):
    (sheets[3] / "model_pricing.json").write_text("{}", encoding="utf-8")
    run_merge(sheets, sheets[3])
    err = capsys.readouterr().err
    assert "model_pricing.json" in err
    assert "model_parameters.json already" not in err


def test_the_error_shows_the_numbered_names_it_would_have_used(sheets, capsys):
    (sheets[3] / "model_parameters.json").write_text("{}", encoding="utf-8")
    run_merge(sheets, sheets[3])
    err = capsys.readouterr().err
    assert "model_parameters_1.json" in err
    assert "model_pricing_1.json" in err


def test_a_refusal_writes_nothing(sheets):
    """The point of asking first: the existing file is untouched."""
    existing = sheets[3] / "model_parameters.json"
    existing.write_text('{"sentinel": true}', encoding="utf-8")
    run_merge(sheets, sheets[3])
    assert existing.read_text(encoding="utf-8") == '{"sentinel": true}'
    assert not (sheets[3] / "model_parameters_1.json").exists()


def test_a_refusal_costs_nothing(sheets, capsys, monkeypatch):
    """It must not load 20MB and diff 12,595 models just to refuse.

    A refusal after the merge would cost a user waiting on a 20-second run that
    was never going to write anything.
    """
    (sheets[3] / "model_parameters.json").write_text("{}", encoding="utf-8")

    def explode(*a, **k):
        raise AssertionError("the merge ran despite the collision")

    monkeypatch.setattr(cli, "merge", explode)
    monkeypatch.setattr(cli, "load_dataset", explode)
    assert run_merge(sheets, sheets[3]) == cli.EXIT_CONFLICT


def test_a_conflict_is_distinguishable_from_bad_usage(sheets):
    """A wrapper needs to tell 'retry with a flag' from 'you called me wrong'."""
    (sheets[3] / "model_parameters.json").write_text("{}", encoding="utf-8")
    assert cli.EXIT_CONFLICT != cli.EXIT_USAGE != cli.EXIT_VALIDATION


# --------------------------------------------------------------------------- #
# the CLI proceeds
# --------------------------------------------------------------------------- #


def test_overwrite_replaces_and_still_merges(sheets):
    out = sheets[3]
    (out / "model_parameters.json").write_text("{}", encoding="utf-8")
    (out / "model_pricing.json").write_text("{}", encoding="utf-8")
    assert run_merge(sheets, out, "--overwrite") == cli.EXIT_OK
    assert json.loads((out / "model_parameters.json").read_text())["grok-4.20"][
        "max_output_tokens"
    ] == 32768


def test_add_number_keeps_the_originals(sheets):
    out = sheets[3]
    (out / "model_parameters.json").write_text('{"untouched": 1}', encoding="utf-8")
    assert run_merge(sheets, out, "--add-number") == cli.EXIT_OK
    assert json.loads((out / "model_parameters.json").read_text()) == {"untouched": 1}
    assert (out / "model_parameters_1.json").exists()
    assert (out / "model_pricing_1.json").exists()


def test_add_number_notes_that_the_plain_names_are_now_stale(sheets, capsys):
    """Otherwise a consumer pointed at the old name silently keeps old data."""
    out = sheets[3]
    (out / "model_parameters.json").write_text("{}", encoding="utf-8")
    run_merge(sheets, out, "--add-number")
    assert "left as they were" in capsys.readouterr().out


def test_a_clean_first_run_says_nothing_about_collisions(sheets, capsys):
    assert run_merge(sheets, sheets[3]) == cli.EXIT_OK
    assert "left as they were" not in capsys.readouterr().out


def test_overwrite_still_guards_an_input(sheets, capsys):
    """--overwrite replaces a previous output; it does not authorise eating an input.

    The merge read that file at the start of this very run, so a mistake is not
    repeatable.
    """
    params, pricing, custom, out = sheets
    target = out / "model_parameters.json"
    target.write_text(json.dumps(PARAMS), encoding="utf-8")
    code = cli.main([
        "merge",
        "--original-parameters", str(target),
        "--original-pricing", str(pricing),
        "--custom", str(custom),
        "--output", str(out),
        "--quiet", "--overwrite",
    ])
    assert code == cli.EXIT_USAGE
    assert "--force" in capsys.readouterr().err


def test_force_and_overwrite_together_still_refuse_the_input(sheets, tmp_path, capsys):
    params, pricing, custom, _ = sheets
    target = tmp_path / "model_parameters.json"
    target.write_text(json.dumps(PARAMS), encoding="utf-8")
    code = cli.main([
        "merge",
        "--original-parameters", str(target),
        "--original-pricing", str(pricing),
        "--custom", str(custom),
        "--output", str(tmp_path),
        "--quiet", "--overwrite", "--force",
    ])
    assert code == cli.EXIT_OK


def test_dry_run_also_refuses(sheets, capsys):
    """A dry run that exited 0 would promise a write the real run would not do."""
    (sheets[3] / "model_parameters.json").write_text("{}", encoding="utf-8")
    assert run_merge(sheets, sheets[3], "--dry-run") == cli.EXIT_CONFLICT


def test_dry_run_reports_the_numbered_target(sheets, capsys):
    (sheets[3] / "model_parameters.json").write_text("{}", encoding="utf-8")
    run_merge(sheets, sheets[3], "--dry-run", "--add-number")
    out = capsys.readouterr().out
    assert "model_parameters_1.json" in out
