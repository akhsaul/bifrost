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

Writes `out/model_parameters.json` and `out/model_pricing.json`.

### Output names that are already taken

If the output folder already holds a file of the same name, the merge **stops
and asks** rather than picking for you. Neither answer is a safe default:
overwriting destroys the previous result, and quietly writing a numbered file
leaves two files that both look canonical.

```bash
error: the output folder already contains model_parameters.json.
  --overwrite     replace the existing file
  --add-number    keep them and write model_parameters_1.json, model_pricing_1.json instead
  (both files move together: parameters and pricing are read as a pair)
```

Exit code is `3`, distinct from `1` (validation) and `2` (usage), so a wrapper
can tell "retry with a flag" from "you called me wrong". The check runs *before*
the merge, so a refusal costs nothing instead of loading 20MB first.

In the GUI, **Save Output…** shows the same question as a dialog with
**Overwrite** and **Add Number** buttons, listing the size and modification time
of what would be lost. Cancel is the default button, so a stray Return cannot
destroy a file. Escape and Cancel both write nothing.

Two details:

- **The two files move as a pair.** They are read together, so a mismatched pair
  — `model_parameters_2.json` beside a stale `model_pricing.json` — is a silent
  data bug. If either name is taken, both are numbered, with the first suffix
  free for both.
- **`--overwrite` is not `--force`.** `--overwrite` replaces a previous *output*;
  `--force` is still required to write over an *input*, because the merge just
  read that file and the run could not be repeated. The GUI refuses the input
  case outright, with no flag to bypass it.

| Command | Purpose |
|---|---|
| `merge` | apply the overlay and write both files |
| `diff` | show what the overlay would change, plus cross-file disagreements |
| `validate` | check an overlay; exits `1` on errors |
| `info` | summarize a datasheet file |
| `import-zed` | convert a Zed `/models` file into `zed/<id>` overlay entries |
| `gui` | launch the Qt editor |

Useful flags: `--dry-run` (report without writing), `--report <path>` (full
change list as JSON), `--indent N` (pretty output), `--sort-keys`,
`--param-array-mode merge|replace`, `--pricing-fields preserve|strict`.

Exit codes: `0` ok, `1` validation failure, `2` usage/IO error, `3` an output
name is already taken in `--output` (retry with `--overwrite` or `--add-number`).

### Importing Zed models

```bash
.venv/bin/python -m datasheet_editor import-zed \
  --zed-models path/to/zed-models.json \
  --custom custom_model_metadata.json
```

Converts a Zed `GET /models` file (`{"models": [...]}`) into `zed/<id>`
overlay entries. Every capability value comes from that file — nothing is
guessed and no base-provider data is mixed in. The overlay `provider` is
always `zed` (the Bifrost provider serving the model, which is what the
capability lookup matches on); Zed's inner `provider` field
(`anthropic`/`open_ai`/`google`) is only validated, never written. Models with no `supported_effort_levels` get
an explicit empty ladder and no `reasoning_effort` descriptor, so the prompt
playground offers no effort control for them. Effort values keep their Zed
casing (Gemini's `MINIMAL`/`LOW`/...) as-is. The `pricing` section carries identity + token limits
only; capability flags live in `parameters`, following the merge engine's own
placement rule. Fields with no datasheet counterpart (`display_name`,
`is_latest`, `supports_max_mode`, `is_disabled`, ...) are omitted.

Entries already in the custom file are skipped and reported; `--overwrite-existing`
replaces them, `--dry-run` reports without writing. The GUI has the same flow
behind the **Import Zed…** button (file picker → confirm dialog with
overwrite checkbox → apply, then Save Custom as usual).

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

## Reading prices

Every float cost in these datasheets is stored **per token**, so the raw JSON is
scientific notation: `3e-06` means three millionths of a cent. 1,009 of the 1,255
distinct float cost values render that way, so every display path expands them:

```
cache_read_input_token_cost   0.00000003   ~ $0.3000 per 1M tokens
input_cost_per_token          0.000003     ~ $3.00 per 1M tokens
output_cost_per_token         0.000015     ~ $15.00 per 1M tokens
```

The exact decimal is what gets saved; the `~ $X per 1M tokens` reading is display
only. Editing, merging, and writing are byte-identical to before — `2.5e-06`
still goes into the file as `2.5e-06`.

The per-1M reading appears only for fields genuinely priced per token, matched by
name (`input_cost_per_token`, `cache_read_input_token_cost_above_32k_tokens`,
`output_cost_per_reasoning_token`, …). Per-image, per-second, per-page, and
multiplier fields are left unscaled, since multiplying those by a million would
be nonsense. Integer costs are also left alone: the pricing file uses `0` for
"free" and `-1` as an "unknown" sentinel.

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

### Field descriptions and typed inputs

`model_parameters.json` carries no documentation: nothing in it says what
`reasoning_effort_levels` means, that it holds a list of strings, or which effort
labels Bifrost knows. The Go source is the only place that says, so the Add Field
dialog assembles it from there and from the loaded data.

`model_parameters.json` is parsed into `schemas.ModelCapabilities`
(`core/schemas/modelcapabilities.go`), and that struct's **doc comments** are
carried across into the snapshot:

```bash
python3 tools/gen_param_fields.py          # regenerate
python3 tools/gen_param_fields.py --check  # CI: fail if stale
```

For a selected field the dialog reports:

- **the shape** — `true or false`, `list of text`, `whole number`;
- **what it means** — the Go doc comment, or a summary derived from the data
  (`true on 1,997 models, false on 518`) when the struct has none;
- **where it is used** — present in N of 12,595 models, and the observed range;
- **which file Bifrost reads it from.** Only 40 of the 121 top-level parameters
  fields are declared on `ModelCapabilities`. The rest either come from
  `model_pricing.json` (`max_input_tokens`, `base_model`, `is_deprecated` — the
  capability lookup runs against the pricing file) or are read by nothing at all
  (`supports_vision`, `comment`, `deprecation_date`). Editing the wrong file is
  accepted by the merge and then silently does nothing, so the dialog says which
  one before you type.

The value control follows the shape:

| Shape | Control |
|---|---|
| `true or false` | checkbox |
| text, closed vocabulary (≤ 20 distinct values) | dropdown, **editable** |
| list of text, closed vocabulary | tick-list, plus a box for a value the datasheet has not caught up with |
| anything else | JSON text area, as before |

Enums are only offered when **Go consumes the field** and its observed vocabulary
is small. That is a deliberate limit, not an omission: `base_model` has 9,265
distinct values and presenting them as a dropdown would be a worse editor than the
text box it replaced. Dropdowns stay editable so a provider can add a tier or an
effort level before the datasheet follows.

For `model_parameters` descriptors the same panel uses the playground's own
`label` and `helpText`, plus the `options` a `select` declares. Worth knowing:
Bifrost models **only the descriptor's `id`** (`ModelParameterDescriptor`), which
feeds the request-parameter allowlist. `label`, `helpText`, `type`, `default`,
`range` and `options` are served to the UI verbatim from the stored row — editing
them is still meaningful, Go just never inspects them.

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

With no arguments nothing is opened. Three buttons load the three files, each
labelled with what it wants:

| Button | File | Needed |
|---|---|---|
| **Load Parameters…** | `model_parameters.json` | required |
| **Load Pricing…** | `model_pricing.json` | required |
| **Load Custom…** | `custom_model_metadata.json` | optional |

The toolbar states which are chosen and which are still missing, in red. Files
are never auto-discovered: a working directory here routinely holds several
datasheet copies (`*_beauty.json`, generated output, an unrelated model's sheet),
and silently opening the wrong one is worse than asking.

Choosing only one of the two originals raises a dialog saying so, and explaining
why: the editor lists both side by side and reports where they disagree, and
`GetCapabilityEntry` reads the pricing copy — so a single file would be a
misleading half-view, not a convenient shortcut.

- **Add Model…** creates a model that does not exist yet. It asks for an ID
  (refusing one already present), a provider, and a mode, both offered as
  editable combos seeded from the values already in the data so the new model
  shows up under the right filters. You can include a pricing section or not.
- **Add Parameter Field…** / **Add Pricing Field…** add a single field to the
  selected model. Picking a field name shows **what it is** and swaps the value
  control to match its shape, because the JSON files document nothing on their
  own (see *Field descriptions and typed inputs* below). Putting a field in the
  wrong section is refused with the reason — the dialog asks the merge engine's
  own placement rule, so the editor cannot offer something the merge would
  reject.
- **Where the value goes is chosen, not guessed.** The parameters dialog asks
  whether you are adding a **top-level field** (a column of the entry) or a
  **parameter descriptor** (an entry of the `model_parameters` array). These are
  different vocabularies: `reasoning_effort` exists only as a descriptor, and
  writing it as a top-level key would produce a field Go never reads. Choosing the
  descriptor target writes `model_parameters[id=…]`, which the merge folds in by
  id and reports as a single change line. The pricing dialog does not offer the
  choice — `model_parameters` is not part of a pricing entry.
- **Search** is plain text — no regex, case-insensitive **contains** across model
  ID, provider, base model, and mode. So `onnet` finds `claude-sonnet`. `Ctrl+F`
  focuses it, `Esc` clears.
- **Provider** and **mode** facets show counts, and a separate **Provider
  contains…** field does free-text substring matching (so `bed` finds `bedrock`,
  and a provider you just typed into the overlay is findable before the dropdown
  has caught up). Every filter ANDs together, and the status line spells out which
  are active.
- A model's provider, mode and base model are read from your overlay first, so a
  model added through **Add Model…** appears under its provider immediately and
  can be filtered by it.
- **Filtering is view-only.** Merging always covers every model, never just the
  filtered subset — the status line says so whenever a filter is active.
- Four tabs: **Parameters**, **Pricing**, **Conflicts**, **model_parameters**.
  The last one holds the playground descriptors — a different vocabulary from the
  entry's own columns (an array of controls keyed by `id`), and given its own tab
  because squeezed into a splitter a model's fifteen descriptors showed five rows.
  It is disabled for models that publish none.
- In that table only **label** and **default** are editable. `id` is the key the
  merge matches on, so renaming one is really a delete plus an add. `type` picks
  which control the playground renders and is only coherent together with the keys
  it implies — `options`, `range`, `array` — which that table does not edit, so a
  cell that could be flipped on its own would be editable but wrong; change it
  through **Add Parameter Field…** instead. Each read-only column's header says why
  on hover, and a row's own tooltip carries the playground's `helpText`.
- **Delete Descriptor** removes a descriptor *you added* — one that is in your
  overlay but not in the original. The original file is never touched, and the row
  leaves the merged output until you add it back. A descriptor from the original is
  deliberately not deletable: the overlay has no delete channel (`merge._deep_merge`
  is explicit that absence never removes anything), so "removing" one would either
  do nothing at merge time or force a tombstone into the custom file's format. The
  button is disabled for those and says why, because a permanently greyed-out
  control with no reason reads as a bug. Deleting asks first, and prunes the empty
  containers it leaves, so the custom file never accumulates
  `{"model": {"parameters": {"model_parameters": []}}}`. Right-clicking a row offers
  the same action.
- Editing a descriptor records a minimal overlay entry (`{"id": …, "default": …}`)
  and typing the original value back drops it again, so the custom file never
  accumulates hollow records or a copy of the model's whole array.
- **Parameters** and **Pricing** tabs. Every pricing field is badged `cost`,
  `capability`, or `ignored`, with a tooltip explaining whether Bifrost's
  `datasheet.Entry` reads it — so the 222 fields outside the read-set are visible
  before you spend time editing one.
- Numeric fields use a validating line edit rather than a spin box, because a
  spin box clamps out-of-range input and would quietly rewrite your data.
  Unparseable input shows an inline error and is not committed.
- `null` is editable as a real value.
- Loading, merging, and saving run on background threads, so the 20MB file never
  freezes the window. Loading shows a progress dialog naming the phase it is on
  ("Reading model_parameters.json", "Comparing both datasets") with a working
  **Cancel** — the phases are separable, so cancelling skips the conflict scan
  rather than pretending to abort a `json.load` already in flight.
- **The dialog never reads 100% before the data is on screen.** Reading the files
  is only part of it; the last step belongs to building the model list, so 100%
  means the rows are actually visible rather than "the files finished parsing".
- Getting from "files loaded" to "12,595 rows displayed" is the expensive part,
  and three things dominated it, all fixed:
  - Clearing filters re-mapped all rows **three times** (once per condition), and
    did so even when the filters were already empty. Both are now coalesced into
    one call that is skipped entirely when nothing changed — ~11s saved.
  - Populating the model re-mapped it inside `endResetModel`, costing ~2.4s. The
    source is now detached from the proxy for the duration of the reset — ~30ms.
  - A `sort()` was forced after every load, but `build_rows` already emits models
    in ascending ID order, so it was a second full re-mapping for an identical
    result. Column sorting still works when a header is clicked.
- **Merge**, **Save Output**, and **Save Custom** stay disabled until there is
  actually data, not merely until paths have been chosen. A cancelled load
  leaves both paths set and nothing loaded, and must not leave Merge clickable.
- Merging writes nowhere until you choose an output directory, and the written
  files are re-read and verified before the status bar reports success.
- **Save Output…** asks before replacing or numbering — see
  [Output names that are already taken](#output-names-that-are-already-taken).
  The dialog shows what would be lost, and **Cancel** is the default button. It
  also refuses, outright, to write over the file this session was loaded from,
  which the CLI needs `--force` for and the GUI offers no way to bypass.

## Tests

```bash
QT_QPA_PLATFORM=offscreen .venv/bin/python -m pytest tests/ -q
```

370 tests. The GUI tests are skipped without PySide6. They cover the merge rules
against both hand-built cases and slices of the real 20MB file, and pin two Qt
interop and lifetime traps that make a window render nothing while looking
healthy:

- Qt passes an **invalid** `QModelIndex()` (not `None`) into Python overrides, so
  `if parent is not None` silently makes every table report zero rows.
- A `QSortFilterProxyModel` over a Python model stays empty unless the root
  row/column counts answer correctly.
- `QThreadPool.start()` keeps only the C++ worker, so a worker whose last Python
  reference is dropped is collected mid-run and its `QObject` signals are
  destroyed with it. Closing the window during a load then raised "Signal source
  has been deleted" on the worker thread.
- Qt does not apply a layout until the event loop runs, and a widget parented to
  the right container still renders nothing if that container was detached from
  its form. The Add Field dialog's Value row rendered empty while every
  parent-level assertion passed; only geometry catches that.

There is also a regression test for a subtler merge bug: the by-id
`model_parameters` merge used to mutate the original descriptors in place, so the
output value was right but the change report compared the merged array against
itself and reported no change at all.

## Layout

```
editor/
├── datasheet_editor/
│   ├── fields.py        # read-set + cost/capability classification
│   ├── fieldinfo.py     # what each field means and what shape its value takes
│   ├── format.py        # exponent-free numbers + per-1M price readings
│   ├── pricing_fields.json   # GENERATED from types.go — do not edit
│   ├── param_fields.json     # GENERATED from modelcapabilities.go — do not edit
│   ├── dataset.py       # load/save, key order, atomic writes
│   ├── merge.py         # the merge engine (pure, no Qt, no IO)
│   ├── zed_import.py    # Zed /models → overlay entries (pure, no Qt, no IO)
│   ├── validate.py      # errors, warnings, conflict detection
│   ├── cli.py           # merge/diff/validate/info/import-zed/gui subcommands
│   └── gui/              # models, workers, typed field editor, add dialogs
├── tools/gen_pricing_fields.py
├── tools/gen_param_fields.py
└── tests/
    └── test_zed_import.py  # importer mapping + CLI + GUI wiring
```

`merge.py` is deliberately free of Qt and IO so the GUI and CLI cannot drift
apart on merge semantics.
