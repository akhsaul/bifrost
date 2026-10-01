# Bifrost Datasheet Editor

Edit, override, and merge the two datasheets Bifrost loads:

| File | Models | Role |
|---|---|---|
| `model_parameters.json` | 12,595 | capability metadata + the `model_parameters` array that builds parameter forms in the UI |
| `model_pricing.json` | 4,763 | cost fields (plus a duplicate copy of some capability fields) |

You never edit those files directly. You write a **custom overlay**
(`custom_model_metadata.json`) holding your edits, and the merge writes a fresh
pair of output files. The originals are never modified.

```
original model_parameters.json  ┐
original model_pricing.json     ├─►  merge  ─►  out/model_parameters.json
custom_model_metadata.json      ┘                out/model_pricing.json
```

## Install

```bash
python3 -m venv --system-site-packages .venv
.venv/bin/pip install pytest            # tests
.venv/bin/pip install PySide6           # only for the GUI
```

The merge engine and CLI need nothing beyond the standard library.

## CLI

```bash
.venv/bin/python -m datasheet_editor merge \
  --original-parameters model_parameters.json \
  --original-pricing    model_pricing.json \
  --custom              custom_model_metadata.json \
  --output              out/
```

Writes `out/model_parameters.json` and `out/model_pricing.json`. The originals
are never written to; pointing `--output` at an input is refused unless you pass
`--force`.

| Command | Purpose |
|---|---|
| `merge` | apply the overlay and write both files |
| `diff` | show what the overlay would change, plus cross-file disagreements |
| `validate` | check an overlay; exits `1` on errors |
| `info` | summarize a datasheet file |
| `gui` | launch the Qt editor |

Useful flags: `--dry-run` (report without writing), `--report <path>` (full
change list as JSON), `--indent N` (pretty output), `--sort-keys`,
`--param-array-mode merge|replace`, `--pricing-fields preserve|strict`.

Exit codes: `0` ok, `1` validation failure, `2` usage/IO error.

## The custom overlay

One model per key, with an explicit `parameters` and/or `pricing` section. Only
fields you actually change need to appear:

```json
{
  "gpt-4o": {
    "pricing": { "input_cost_per_token": 0.0000025 }
  },
  "claude-sonnet-4-5": {
    "parameters": {
      "max_input_tokens": 200000,
      "model_parameters": [{ "id": "temperature", "default": 0.3 }]
    }
  },
  "my-custom-model": {
    "parameters": { "provider": "openai", "mode": "chat", "max_input_tokens": 8000 },
    "pricing":    { "input_cost_per_token": 0.000001 }
  }
}
```

The sections are explicit because the same field name can legitimately belong to
either file. Putting a cost field under `parameters` is a hard error rather than
a silently ignored edit.

## Merge rules

Add/override only — **nothing is ever deleted by omission**.

- A model absent from the originals is appended whole.
- Fields the overlay does not mention are preserved.
- Nested objects deep-merge (`tiered_pricing`, a param's `range`).
- Lists replace wholesale (`supported_regions`).
- `model_parameters` is an array of objects keyed by `id`. It merges **by id**
  (`--param-array-mode replace` to opt out), so you can override one parameter's
  `default` without restating the other descriptors. New ids are appended;
  original order is preserved.
- `null` is a real value, not a deletion. The originals contain legitimate nulls
  (`rpm`, `tpm`).
- Key order is preserved, and output defaults to the compact upstream format.

## Pricing field policy

`model_pricing.json` has **328 distinct fields. Bifrost reads 106 of them.**

Bifrost deserializes the file into the `datasheet.Entry` struct
(`framework/modelcatalog/datasheet/types.go`), which embeds `Options`. Anything
that struct does not declare is dropped on load. The remaining 222 are real
upstream fields that Go simply does not use yet — tier costs like
`cache_read_input_token_cost_above_32k_tokens`, plus editorial keys like
`comment`.

So the default is `--pricing-fields preserve`: no field is ever removed. Adding
`--pricing-fields strict` narrows the output to the 106 read fields, prints
every field it would drop, and requires `--strict-confirm` to proceed.

The read-set is **generated, never hand-maintained**:

```bash
python3 tools/gen_pricing_fields.py          # regenerate
python3 tools/gen_pricing_fields.py --check  # CI: fail if stale
```

Go stays the source of truth. The day someone adds an `Options` field and
regenerates, the tool picks it up — it never silently strips newly added fields.

## Capability fields live in both files

The same facts are maintained twice, and **the two copies currently disagree on
1,397 field values** (`max_tokens`, `source`, `max_output_tokens`, and so on).

This is not an accident of this tool: one Go struct serves both lookups.
`GetPricingEntryForModel` (`store.go:177`) reads costs and
`GetCapabilityEntry` (`store.go:209`) reads capabilities — both from the same
`pricingData` map. Meanwhile `model_parameters.json` is loaded as raw JSON
(`params.go:46`) with only `provider` parsed; the `model_parameters` array is
handed to provider-utils opaque.

Two consequences worth knowing:

- **Capability edits in the `parameters` section do not change server behavior.**
  `GetCapabilityEntry` never consults params. Edit the pricing copy if you want
  it to take effect.
- Some fields in the pricing file are inert: `supports_computer_use` has zero
  hits across the whole Go `datasheet` package.

`datasheet diff` and the GUI's **Conflicts** tab list every disagreement without
picking a winner.

## GUI

```bash
.venv/bin/python -m datasheet_editor gui \
  --original-parameters model_parameters.json \
  --original-pricing    model_pricing.json \
  --custom              custom_model_metadata.json
```

With no arguments it picks up the datasheets in the working directory.

- **Search** is plain text — no regex, case-insensitive **contains** across model
  ID, provider, base model, and mode. So `onnet` finds `claude-sonnet`. `Ctrl+F`
  focuses it, `Esc` clears.
- **Provider** and **mode** facets show counts and combine with the search (all
  filters AND together).
- **Filtering is view-only.** Merging always covers every model, never just the
  filtered subset — the status line says so whenever a filter is active.
- **Parameters** and **Pricing** tabs. Every pricing field is badged `cost`,
  `capability`, or `ignored`, with a tooltip explaining whether Bifrost's
  `datasheet.Entry` reads it — so the 222 fields outside the read-set are visible
  before you spend time editing one.
- Numeric fields use a validating line edit rather than a spin box, because a
  spin box clamps out-of-range input and would quietly rewrite your data.
  Unparseable input shows an inline error and is not committed.
- `null` is editable as a real value.
- Loading, merging, and saving run on background threads, so the 20MB file never
  freezes the window.
- Merging writes nowhere until you choose an output directory, and the written
  files are re-read and verified before the status bar reports success.

## Tests

```bash
QT_QPA_PLATFORM=offscreen .venv/bin/python -m pytest tests/ -q
```

47 tests. The GUI tests are skipped without PySide6. They cover the merge rules
against both hand-built cases and slices of the real 20MB file, and pin two Qt
interop traps that make a window render nothing while looking healthy:

- Qt passes an **invalid** `QModelIndex()` (not `None`) into Python overrides, so
  `if parent is not None` silently makes every table report zero rows.
- A `QSortFilterProxyModel` over a Python model stays empty unless the root
  row/column counts answer correctly.

There is also a regression test for a subtler merge bug: the by-id
`model_parameters` merge used to mutate the original descriptors in place, so the
output value was right but the change report compared the merged array against
itself and reported no change at all.

## Layout

```
editor/
├── datasheet_editor/
│   ├── fields.py        # read-set + cost/capability classification
│   ├── pricing_fields.json   # GENERATED from types.go — do not edit
│   ├── dataset.py       # load/save, key order, atomic writes
│   ├── merge.py         # the merge engine (pure, no Qt, no IO)
│   ├── validate.py      # errors, warnings, conflict detection
│   ├── cli.py
│   └── gui/
├── tools/gen_pricing_fields.py
└── tests/
```

`merge.py` is deliberately free of Qt and IO so the GUI and CLI cannot drift
apart on merge semantics.
