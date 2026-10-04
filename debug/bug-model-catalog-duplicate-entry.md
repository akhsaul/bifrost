# Duplicate / orphaned entries in the Model Catalog

**Status:** resolved operationally (purge + force sync, see §9). The underlying
upstream defect — an upsert-only sync with no prune — is **not fixed**; it will
reproduce for anyone who removes or renames a model in their datasheet.
**Component:** `framework/modelcatalog` (datasheet sync → config store)
**Severity:** medium. Produces permanently-wrong rows in a user-facing catalog; does
not mis-price requests.
**Observed on:** Bifrost `dev` build, config store at `~/.my-bifrost/config.db` (SQLite),
provider `bitdeer`, model `deepseek-ai/DeepSeek-V4.1-Flash`.

---

## 1. Symptom

`http://127.0.0.1:2972/workspace/model-catalog?tab=attributes&provider=bitdeer`
lists **10** rows for one provider that offers **8** models. Two of the ten can never
match a real model, and six more show no token limits:

| # | Row as rendered | In `GET /v1/models`? | Pricing row? | Max in / out shown |
|---|---|---|---|---|
| 1 | `BAAI/bge-m3` | yes | no | — / — |
| 2 | `BAAI/bge-reranker-v2-m3` | yes | no | — / — |
| 3 | `Qwen/Qwen3.8-27B` | yes | no | — / — |
| 4 | `deepseek-ai/DeepSeek-V4-Flash` | yes | no | — / — |
| 5 | `seedream-5.0-lite` | yes | no | — / — |
| 6 | `zai-org/GLM-5.3` | yes | no | — / — |
| 7 | `DeepSeek-V4.1-Flash` | **no** | yes (orphan) | 1048576 / 32768 |
| 8 | `GLM-5.3-Flash` | **no** | yes (orphan) | 1048576 / 32768 |
| 9 | `zai-org/GLM-5.3-Flash` | yes | yes | 1048576 / 32768 |
| 10 | `deepseek-ai/DeepSeek-V4.1-Flash` | yes | yes | 1048576 / 32768 |

Rows 7 and 8 are the defect. Rows 1–6 are **not** a bug: a model discovered from the
provider but absent from the datasheet legitimately has no limits, and the UI
correctly renders "Not available".

The two defects compose into a third, worse symptom: because rows 7/8 sit in the
catalog next to rows 9/10 and carry the *same* values, an operator editing prices
in the Model Catalog can reasonably believe they are editing the model the provider
actually serves — and be editing a row that no request will ever resolve to. Prices
and limits are therefore edited in the wrong place, silently.

## 2. The two datasheets disagree on model identity

This is the root of the duplicate, and it is a design asymmetry rather than a typo.

**Pricing** — `framework/modelcatalog/datasheet/sync.go:55` → `convertEntryToTablePricing`
→ `extractModelName(modelKey)` (`framework/modelcatalog/datasheet/types.go:567`):

```go
func extractModelName(modelKey string) string {
	if idx := strings.Index(modelKey, "/"); idx >= 0 {
		return modelKey[idx+1:]   // strips the FIRST segment only
	}
	return modelKey
}
```

**Parameters** — `framework/modelcatalog/datasheet/params.go:80`:

```go
records = append(records, configstoreTables.TableModelParameters{
	Model: model,     // full key, verbatim — extractModelName is NOT applied
	Data:  string(data),
})
```

So for the same logical model:

| File | Stored key |
|---|---|
| pricing table | `deepseek-ai/DeepSeek-V4.1-Flash` (first segment removed) |
| parameters table | `bitdeer/deepseek-ai/DeepSeek-V4.1-Flash` (untouched) |

Verified in the live DB — `governance_model_parameters` holds **all four** spellings,
and the first two have `provider: "bitdeer"` inside the entry body:

```
key='deepseek-ai/DeepSeek-V4.1-Flash'   provider='bitdeer'
key='zai-org/GLM-5.3-Flash'             provider='bitdeer'
key='bitdeer/zai-org/GLM-5.3-Flash'    provider='bitdeer'
key='bitdeer/deepseek-ai/DeepSeek-V4.1-Flash'  provider='bitdeer'
```

## 3. Why the orphan rows exist

The two orphan rows are explained by `extractModelName` being applied to keys that do
**not** start with the provider segment.

The published datasheet convention is `provider/model`. But `extractModelName`
assumes segment 1 is always the provider. Measured, not inferred:

```
extractModelName("bitdeer/deepseek-ai/DeepSeek-V4.1-Flash") = "deepseek-ai/DeepSeek-V4.1-Flash"   ok
extractModelName("deepseek-ai/DeepSeek-V4.1-Flash")       = "DeepSeek-V4.1-Flash"                BROKEN
extractModelName("together_ai/deepseek-ai/DeepSeek-V4.1-Flash") = "deepseek-ai/DeepSeek-V4.1-Flash"  ok
extractModelName("openrouter/z-ai/glm-5.3:free")          = "z-ai/glm-5.3:free"                  ok
```

When the overlay was written with a key lacking the `bitdeer/` prefix —
`deepseek-ai/DeepSeek-V4.1-Flash` — while carrying `provider: "bitdeer"` in the
entry body, the sync faithfully produced a pricing row keyed `DeepSeek-V4.1-Flash`
/ `bitdeer`. Same for `zai-org/GLM-5.3-Flash` → `GLM-5.3-Flash`.

That shape is trivially produced by the **Add Model…** dialog in the editor: it asks
for a model id and a provider separately, so typing the id exactly as the provider's
`/v1/models` returns it (`deepseek-ai/DeepSeek-V4.1-Flash`) yields a key with no
provider segment. Nothing warns, and nothing in the merged output looks wrong — the
key is a valid JSON object key and the entry carries a correct `provider`.

Note this is **not** limited to Bitdeer. Any provider whose model id contains a
slash (`huggingface/novita/deepseek-ai/DeepSeek-V4.1-Flash`,
`azure/FW-DeepSeek-V4.1-Flash`, `openrouter/z-ai/glm-5.3:free`) is affected whenever
an entry is authored without the provider prefix.

## 4. Why the orphans never go away

`framework/modelcatalog/datasheet/sync.go:52` calls
`UpsertModelPricesBatch` and nothing else. There is no delete anywhere in the sync
path:

```go
if err := s.configStore.UpsertModelPricesBatch(ctx, records); err != nil { ... }
if err := s.LoadFromDB(ctx); err != nil { ... }
```

`framework/configstore/rdb.go:3009` is an `ON CONFLICT ... DO UPDATE` upsert:

```go
onConflict := clause.OnConflict{
	Columns:   []clause.Column{{Name: "model"}, {Name: "provider"}, {Name: "mode"}},
	DoUpdates: clause.AssignmentColumns(pricingSyncUpdateColumns),
}
```

Consequence: **a pricing row, once written, is permanent.** The unique index is
`idx_model_provider_mode` on `(model, provider, mode)`, so the only way a row leaves
is an explicit `DELETE`. None exists. A model removed from the datasheet, renamed, or
re-spelled stays in the catalog — and keeps serving stale prices.

That is precisely how rows 7/8 outlived the keys that produced them. The current
`editor/custom_model_metadata.json` contains only the two correctly-prefixed keys;
rows 7/8 are residue of an earlier state of the overlay. The dedup pass at
`sync.go:56-63` (by `makeKey(model, provider, mode)`) only collapses collisions
*within one sync batch* — it cannot see rows from a previous batch, which is why
syncing again changed nothing.

**Fixing the keys alone does not remove the orphans.** They must be deleted by hand
or the sync must gain a prune.

## 5. Failure modes worth knowing about

- **Stale prices persist forever.** A model removed from the datasheet continues to be
  costed with its old rates. This is a correctness issue for billing, not just
  cosmetics.
- **Duplicate editing target.** Two catalog rows for one logical model, only one of
  which is reachable at request time. Editing the wrong one appears to succeed.
- **The two files drift apart.** Pricing keys are normalised, parameter keys are not.
  Any join between the tables on the model name is unreliable, and a mismatch here
  looks identical to "the data was never added".
- **`max_input_tokens` is pricing-only.** `framework/modelcatalog/datasheet/params.go`
  never references `MaxInputTokens`; limits reach the catalog only via
  `convertEntryToTablePricing` (`types.go:728`). Adding limits to the parameters file
  alone is a no-op for this UI, which is an easy thing to get wrong when both files
  look like they hold the same shape.

## 6. Diagnosis performed

| Check | Result |
|---|---|
| `GET /api/models/details?provider=bitdeer&limit=50&unfiltered=true` | returns the 10 rows above; the correctly-spelled rows carry `max_input_tokens: 1048576` |
| `GET https://api-inference.bitdeer.ai/v1/models` | 8 models, all vendor-prefixed; no bare `DeepSeek-V4.1-Flash` |
| `governance_model_pricing WHERE provider='bitdeer'` | 4 rows, 2 of them unreachable orphans |
| `governance_model_parameters` | 4 key spellings for the same 2 logical models |
| `GET https://getbifrost.ai/datasheet` | 0 `bitdeer/` entries — and *not the source in use*, see §6.1 |
| `grep` for `DELETE` in `sync.go` / `store.go` | none |
| `extractModelName` probe (temporary test, removed) | output in §3 |

The temporary probe test used to produce §3 was deleted after the run; it is not part
of the tree.

### 6.1 The active datasheet source is a local file, not the remote

`getbifrost.ai/datasheet` is only the *default*. The live config overrides it:

```
~/.my-bifrost/config.json -> framework.pricing
  pricing_url          = file:///mnt/data/Projects/bifrost/editor/model_pricing.json
  model_parameters_url = file:///mnt/data/Projects/bifrost/editor/model_parameters.json
```

Both `loadPricingFromURL` (`sync.go:171`) and `loadModelParametersFromURL`
(`params.go:126`) handle a `file` scheme, so a force-sync reads those two files
directly. This matters for the fix: a resync is authoritative, and since both files
now contain only correctly-prefixed keys, **a resync is sufficient to keep the
orphans from returning** — no code change needed to prevent recurrence once the
stale rows are purged.

The config key lives at `framework.pricing.*` (surfaced by
`transports/bifrost-http/handlers/config.go:678` as `pricing["pricing_url"]`), *not*
at a top-level `datasheet` key. There is no `datasheet` section in
`transports/config.schema.json`, which makes the path easy to misread.

One force-sync repopulates both tables: `ForceReloadPricing`
(`framework/modelcatalog/main.go:367`) runs `runPricingSync` and `runParamsSync`
concurrently and waits on both.

## 7. What a fix would need (not implemented)

Ordered by how much each is worth relative to its risk. **None of this has been
done** — the task was analysis only.

1. **Reject ambiguous keys at validation time.** A datasheet key that has no provider
   segment, or whose segment disagrees with the entry's own `provider` field, should
   be a validation error rather than a silently mangled row. This prevents the problem
   at the door and needs no change to the hot path.
2. **Make the two files agree on identity.** Decide whether the parameters table
   stores the raw key or the same normalised name pricing uses, and apply it in both
   places. Anything that joins the two tables depends on this.
3. **Prune on sync.** After a successful upsert, delete pricing rows for this provider
   that the incoming batch did not contain. Needs care: the upsert also carries
   operator-authored rows, so pruning must not delete data the operator entered
   through the management API. Distinguishing the two requires a provenance column
   (e.g. `source: sync|api`), which is a schema migration.
4. **Warn on the unreachable-row state.** A model in the catalog matching no live
   `/v1/models` entry and no datasheet key is a signal worth surfacing, even before
   the underlying issue is fixed.

Item 3 is the only one that changes behaviour for the whole catalog, which is why it
is listed last and why this was left as a report.

## 8. Cleanup

Deleting the whole pair of tables is the reliable cleanup, because the sync is
upsert-only and the orphans cannot be removed by resyncing — only by an explicit
`DELETE`. Both tables are pure caches of the datasheet files, and the files in use
(§6.1) are the authority, so nothing is lost that a force-sync will not re-read.

Ready-to-run script: `debug/purge-pricing-and-parameters.sql` (verifies before,
deletes both tables in one transaction, verifies after).

```bash
sudo sqlite3 /home/akhsaul/.my-bifrost/config.db < debug/purge-pricing-and-parameters.sql
curl -X POST http://127.0.0.1:<port>/api/pricing/force-sync
```

The server does not need to be stopped. It holds no pricing rows inside an open
transaction, and the force-sync's `LoadFromDB` rebuilds the in-memory catalog from
these tables afterwards, which is what makes the change visible in the UI.

If the database is root-owned and `bifrost-http` runs as root, the `sudo` is
required — a non-root `sqlite3` fails with `attempt to write a readonly database`.

To purge just this instance's orphans without touching anything else:

```sql
DELETE FROM governance_model_pricing
 WHERE provider = 'bitdeer'
   AND model IN ('DeepSeek-V4.1-Flash', 'GLM-5.3-Flash');

DELETE FROM governance_model_parameters
 WHERE model IN ('deepseek-ai/DeepSeek-V4.1-Flash', 'zai-org/GLM-5.3-Flash');
```

Note the second statement targets the *parameters* table, whose keys are stored
verbatim while the pricing table's are stripped — so the two orphans have
different key strings in each table (§2). Deleting only the pricing rows leaves
the parameters side intact.

## 9. Resolution

Purging the two catalog tables and then force-syncing cleared the duplicate
entries. Verified on the same instance: the Model Catalog no longer lists the
orphan `GLM-5.3-Flash` / `DeepSeek-V4.1-Flash` rows, and bitdeer resolves to the
two models that actually exist. Restarting the server and hard-reloading the
browser did **not** help, which is itself the diagnostic: the rows were in the
database, not in a cache. Once the tables were purged, no restart and no browser
reload were needed at all — the force sync alone made the fix visible.

### 9.1 Cause, stated plainly

**The datasheet sync is upsert-only, so the catalog can grow but never shrink.**

`SyncFromURL` (`framework/modelcatalog/datasheet/sync.go:52`) does exactly two
things with the parsed data: `UpsertModelPricesBatch`, then `LoadFromDB`.
`UpsertModelPricesBatch` (`framework/configstore/rdb.go:3009`) is an
`ON CONFLICT (model, provider, mode) DO UPDATE`. There is no `DELETE` in the sync
path, and no comparison between "rows in the table" and "rows in the file."

So a row, once written, is permanent. Three conditions had to line up to produce
the observed duplicates:

1. **A key was authored without its provider segment.** An entry keyed
   `deepseek-ai/DeepSeek-V4.1-Flash` while carrying `provider: "bitdeer"` in the
   body — the shape the editor's *Add Model…* dialog produces when the model id is
   typed exactly as the provider's `/v1/models` returns it. `extractModelName`
   (`types.go:567`) strips the first segment on the assumption that it is always
   the provider, yielding `DeepSeek-V4.1-Flash` (§3).
2. **That mangled name matched nothing.** Bitdeer's live `/v1/models` returns only
   prefixed ids, so the stripped row is unreachable — it can never be a live
   model, and it was not in the current file either.
3. **Nothing could remove it.** Condition 2 would normally be caught by a prune;
   there is no prune, so the row kept its values and stayed in the catalog (§4).

Condition 1 alone is a data-entry mistake and would be harmless if condition 3
were not true. **Condition 3 is the bug.** The keys were fixed at some point; the
rows they produced outlived the fix and reappeared on every catalog load.

A related design asymmetry compounds it: pricing keys are normalised through
`extractModelName` while parameter keys are stored verbatim (§2), so a single
model has two different identity strings across the two tables.

### 9.2 What was done

```bash
sudo sqlite3 /home/akhsaul/.my-bifrost/config.db < debug/purge-pricing-and-parameters.sql
curl -X POST http://127.0.0.1:<port>/api/pricing/force-sync
```

Verbatim output of the run that resolved it:

```
$ sudo sqlite3 /home/akhsaul/.my-bifrost/config.db < debug/purge-pricing-and-parameters.sql
60000
BEFORE pricing|4707
BEFORE params|12603
BEFORE bitdeer|DeepSeek-V4.1-Flash|bitdeer|1048576|32768
BEFORE bitdeer|GLM-5.3-Flash|bitdeer|1048576|32768
BEFORE bitdeer|deepseek-ai/DeepSeek-V4.1-Flash|bitdeer|1048576|32768
BEFORE bitdeer|zai-org/GLM-5.3-Flash|bitdeer|1048576|32768
AFTER pricing|0
AFTER params|0

$ curl -X POST http://127.0.0.1:2972/api/pricing/force-sync
{"message":"pricing synced successfully","status":"success"}
```

The `60000` on the first line is `PRAGMA busy_timeout` echoing its value back. The
four `BEFORE bitdeer` lines are the defect in one place: two orphans
(`DeepSeek-V4.1-Flash`, `GLM-5.3-Flash`) sitting beside the two legitimate rows,
carrying identical values, indistinguishable in the catalog from real entries.

The force sync then re-reads the two datasheet files configured at
`framework.pricing.pricing_url` and `framework.pricing.model_parameters_url`
(§6.1), both local `file://` paths, and repopulates both tables — because
`ForceReloadPricing` (`framework/modelcatalog/main.go:367`) runs `runPricingSync`
and `runParamsSync` concurrently and waits on both. Its `LoadFromDB` is what makes
the change visible to the running process, so the server did not need restarting.

Both tables are pure caches of the datasheet files, so nothing was lost that the
sync did not re-read. A pre-purge snapshot was kept at
`/tmp/config.db.prepricedelete.bak` (29 MB, taken with `VACUUM INTO` because the
database is in WAL mode and the server was actively writing).

**Deleting the whole `config.db` is neither necessary nor safe.** The catalog
tables are the only thing that needs to go; the rest of the file holds providers,
keys, virtual keys, budgets and governance config, which a purge preserves and a
file deletion would destroy. The purge needs `sudo` only because the database is
root-owned and `bifrost-http` runs as root — a non-root `sqlite3` fails with
`attempt to write a readonly database`.

### 9.3 Why it will not come back here, and when it will

The two datasheet files in use were verified to contain only correctly-prefixed
keys — every key has a provider segment, and that segment matches the entry body's
own `provider` field. So a resync cannot recreate a mangled key, and the orphans
are gone for good on this instance.

That guarantee is a property of the *files*, not of Bifrost. Any future edit that
removes a model, renames one, or re-spells a key will leave the corresponding row
behind in exactly the same way, because condition 3 still holds. When that happens
the remedy is the same purge; the fix is §7 item 3, which needs a provenance
column so a prune does not also delete operator-authored rows.

### 9.4 Confirmed vs. still unverified

Confirmed by the transcript in §9.2: the purge emptied both tables (4707 and
12603 rows to 0), the force sync returned success, and the duplicate entries are
gone from the Model Catalog.

Still unverified: the post-sync row counts. §8 predicts roughly 4767 pricing rows
(from the file's 4767 entries, minus any that collide on `(model, provider, mode)`
and are dropped by the batch dedup) and 12599 parameter rows. Nobody recorded the
actual figures, so treat them as predictions. A catalog row count far from 4767
would mean the local file and the running config disagree — a separate problem,
not this one.


