# Plan: openrouter-free provider (openai-compatible) + 403 PII detection by message (not HTTP code)

Task: add a new standard OpenAI-compatible provider `openrouter-free`, and change error parsing for the upstream error `{"message":"Request blocked: PII detected (invalid_json_after_redaction)","code":403}` so Bifrost treats it by message content (not just code 403) — avoiding the false "all keys dead" / 502 cascade.

Target: /mnt/data/Projects/bifrost/.hermes/plans/2026-09-14_openrouter-free-provider.md
Plan mode — NO CODE CHANGED yet.

## Goal
Add `core/providers/openrouterfree/` (mirrors openrouter pattern with default headers + `openrouter-free` ModelProvider constant) and refine error parsing so PII-redaction 403 is identified by message substring, not just 403.

## Context / assumptions
- Workspace: /mnt/data/Projects/bifrost (multi-module Go workspace, Go 1.27.0).
- OpenRouter 403 has two shapes: (a) guardrail (PERSON+LOCATION) → byte-redaction breaks double-escaped JSON → `message` = "Request blocked: PII detected ..."; (b) "agentic harnesses" 403 (different message). The user disabled (a) via OR dashboard.
- `openrouter` is the reference; openrouter-free is openai-compatible (delegates to `openai/*.go` helpers), minimal (<150 LOC).
- `core/bifrost.go` line ~6699: 401/402/403 = permanent per-key error (`perKeyFailureStatusCodes` = {401:true,403:true,...}). Changing classification affects all providers; changing openrouter(openrouter-free) parser to not return `BifrostError` with `AllowFallbacks = false` for this specific message avoids the cascade without changing the core rule.
- Key is resolved by gateway via Doppler (`vault_store doppler`, prefix `bf`). Provider config lives in `core/providers/openrouter/` structure (types.go, errors.go, openrouter.go). The new provider also needs registration in `core/schemas/bifrost.go`, `core/bifrost.go`, UI constants, and config.schema.json.

## Proposed approach
1. Create `core/providers/openrouterfree/openrouterfree.go` — openai-compatible, delegates to `openai` helpers; add default HTTP headers per user spec in request build (not via fasthttp SetHeader globally — per-request is safer for multi-provider isolation).
2. Add `openrouterfree` constant in schemas + register in core init + add tests.
3. In openrouter (or new free provider's errors parser): detect `Request blocked: PII detected` in message/body; return error but either (preferred) set `AllowFallbacks = true` / not mark as permanent per-key, OR return a special `BifrostError` that doesn't trigger the deadKeyIDs logic. The user explicitly asks: "detect it from message not from HTTP code" — do that in `errors.go` parser, not in core.
4. Keep change minimal; do not modify `core/bifrost.go` perKey rules globally (too risky).

NOTE — open question: the user said "bukan dari http code tetapi dari message". That implies parser-level change. The cleanest without touching core is: in `openrouterfree` (or openrouter) error parser, when message matches, return error with `ExtraFields.IsContentRejection = true` and set fallback behavior locally. Since the core only checks code=401/402/403, matching by message alone doesn't help unless we also ensure code isn't 403 — but upstream IS 403. To honor user's request exactly (detect by message, not code), the only path is to modify `core/bifrost.go` retry logic OR to have the parser return 200-level error (not realistic). Recommendation captured in open questions below.

## Step-by-step (bite-sized, 2-5 min each)

### 1. Inspect current openrouter provider structure (read-only)
- Read: `core/providers/openrouter/openrouterfree` (should NOT exist yet — verify); `core/schemas/bifrost.go` (find `ModelProvider` enum); `core/providers/openai/openai.go` lines 1-80 (delegate pattern); `core/providers/openrouter/openrouter.go` full.
- Command: `ls core/providers/openrouter/`; `grep -n "ModelProvider\|StandardProviders" core/schemas/bifrost.go`.
- Expected: openrouterfree dir missing; enum contains `openrouter`, `openai`; openrouter delegates to openai helpers.
- Commit: none (read-only).

### 2. Create provider skeleton (new dir, files)
- Create `core/providers/openrouterfree/openrouterfree.go`:
  - `package openrouterfree`
  - `func NewProvider(...) (*openai-like Provider, error)` — copy `openrouter/openrouter.go` constructor; delegate all methods to `openai.HandleOpenAI...`.
  - Add `DefaultHeaders` map (per user spec: http-referer, user-agent, x-openrouter-cache=false, x-openrouter-cache-ttl=0, x-openrouter-categories, x-stainless-*).
- Create `core/providers/openrouterfree/openrouterfree_test.go` — minimal `TestNewProvider` (same pattern as openrouter).
- Create `core/providers/openrouterfree/types.go` if needed (likely empty for openai-compatible; reuse openai types).
- Reference exact file paths in code.
- Verification: `go build ./core/providers/openrouterfree/` (should compile).
- Commit.

### 3. Add default headers per user spec (per-request, not global)
- Modify `core/providers/openrouterfree/openrouterfree.go`: before `client.Do(req)` in streaming/unary paths, inject headers from user list. Since openai-compatible path uses `openai/*.go`, either (a) wrap the `fasthttp.Client` request before Do, or (b) pass headers in the `BifrostChatRequest` and have the openrouterfree wrapper inject them. Simplest: in `openrouterfree.go`, override the function that builds the fasthttp request (`openai.ToOpenAIRequest` equivalent) — but that requires editing openai package. Alternative: set `client := fasthttp.Client{...}` and before every `client.Do(req)`, call `req.Header` injection — this requires a thin wrapper around all Handle* functions (too big).
- BETTER approach (matches user's ask): since openrouterfree is openai-compatible, inject the default headers in the provider's `Configure` or as `ExtraHeaders` in config/network. The user's spec is essentially the standard headers that distinguish this provider. Add them as `NetworkConfig.ExtraHeaders` by default in constructor. This avoids touching openai package.
- Code (inside `NewProvider`): `defaults := map[string]string{...}`; merge into config; set `client.SetHeaders(defaults)` isn't safe. Instead, before `Do(req)`, do `for k,v := range defaults { req.Header.Set(k,v) }`. This requires intercepting all calls — easiest: add a helper `doWithDefaults(req, resp) (*fasthttp.Response, error)` that calls `client.Do(req, resp)` after injecting.
- If too invasive, fall back to: set default headers globally on the `fasthttp.Client` via `client.Do(req, resp)` — fasthttp supports per-request headers via `req.Header`, and the `client.Do` doesn't modify them. So the simplest working approach: in openrouterfree, before each `client.Do(req)`, loop default headers. Only 4 call sites (chat/stream/chat+stream/embedding etc). For minimal change, only implement for chat (user's scope).
- Verification: `go test ./core/providers/openrouterfree/` passes.
- Commit.

### 4. Register provider in schemas + core + UI + docs
- `core/schemas/bifrost.go`: add `ModelProvider` enum value (`openrouterfree`); add to `StandardProviders` list and `AllowedRequests` mapping if needed; add constant.
- `core/bifrost.go`: import + case in provider init switch.
- `core/providers/openrouterfree/openrouterfree.go`: register with correct provider name.
- `transports/config.schema.json`: add `openrouterfree` block (copy openrouter entry).
- `ui/lib/constants/config.ts`: add placeholder + key requirement; `icons.tsx`: icon; `logs.ts`: display name.
- `docs/openapi/openapi.json`: add reference.
- `docs/providers/supported-providers/openrouterfree.mdx`: minimal doc page.
- Verification: `go build ./core/`; `make build`; `grep openrouterfree ui/lib/constants/config.ts`.
- Commit per file group.

### 5. Change 403 detection from code to message (user's core ask)
Open question needs resolution (see below). Two options:

**Option A — Parser-only (recommended, minimal):**
In `core/providers/openrouterfree/openrouterfree.go` (and optionally `core/providers/openrouter/errors.go`), when parsing the provider response:
- Read body bytes.
- Try `providerUtils.ParseErrorResponse` / openai `ParseOpenAIError`.
- After parsing: inspect `bifrostErr.Message` and `bifrostErr.ExtraFields`.
- If message contains `"PII detected"` (case-insensitive substring check), set `bifrostErr.AllowFallbacks = true` (do not treat as permanent key failure) OR set a new extra field (`ExtraFields.IsContentRejection = true`).
- IMPORTANT: since the core logic (`core/bifrost.go`) only checks status code 401/402/403 for `permanentPerKey`, changing message alone doesn't prevent the cascade. To honor "detect by message not by code" EXACTLY, we must either (i) change core logic, or (ii) have parser not return the upstream 403 code but instead return a synthetic 200-level error (not realistic — user wants real response). Option (i) is the only truthful path.

**Option B — Modify `core/bifrost.go` (scoped to openrouter/openrouterfree ONLY, per user approval):**
In retry/fallback logic (`core/bifrost.go`, ~line 6699), when checking `IsPermanentPerKeyError`: add a guard so that IF the provider is `ModelProviderOpenRouter` or `ModelProviderOpenRouterFree` AND the error message contains the PII-redaction substring (`"PII detected"`), then return `false` (not permanent). This limits impact strictly to openrouter family, matching user's instruction "modify core/bifrost.go but limit only for openrouter". Verify: `grep -n 'openrouter'` in the changed function; no other provider name referenced.
- Exact file: `core/bifrost.go` ~line 6699 (`CheckIfPermanentPerKeyError` or retry logic).
- Code: add conditional: `if bifrostErr != nil && (bifrostErr.Message contains "PII detected") { return false }` (i.e., not permanent).
- Verification: `make test-core` passes; add test in `core/internal/llmtests/` that sends payload triggering PII 403 and asserts retry continues (not 502 cascade).
- Commit.

## Tests / validation (TDD cycle per task above)
For each step: write failing test, run, implement, verify pass, commit. Example for step 5:
- Create `core/providers/openrouterfree/openrouterfree_pii_test.go`: mock fasthttp server returning 403 with body `{"message":"Request blocked: PII detected (invalid_json_after_redaction)"}`; assert parsed `BifrostError` has `Message` containing "PII detected" and `ExtraFields` set appropriately; if using Option B, assert that `IsPermanentPerKeyError` returns false for this error.
- Run: `go test ./core/providers/openrouterfree/ -v` (should fail before fix, pass after).
- Commit: `feat(openrouterfree): detect PII 403 by message, not code; prevent key-death cascade`.

## Risks, tradeoffs, open questions
1. **Risk — modifying core retry logic (`core/bifrost.go`)**: affects ALL providers. Must be guarded by message check only (not blanket), with test coverage.
2. **Risk — header defaults**: user wants 11 specific default headers. Setting globally on `fasthttp.Client` isn't safe (other requests from same provider instance may inherit). Per-request injection requires wrapping 4+ Handle* call sites. Tradeoff: use config-level `ExtraHeaders` (already supports deep copy) to inject defaults, which applies to every request safely without wrapping. RECOMMENDED: implement defaults via `NetworkConfig.ExtraHeaders` in provider constructor.
3. **Open question (must resolve before implementing)**: Should `core/bifrost.go` retry logic change (Option B)? The user said explicitly "detect by message, not code" — that requires either (a) changing the classification logic in core, or (b) not caring about the cascade (just log the message). Since the user's context is avoiding the false 502 cascade, Option B is necessary. Confirm: does user approve a 3-line change in `core/bifrost.go` retry logic?
4. **UI/integration**: new provider requires `openrouterfree` entries in `ui/lib/constants/`; if skipped, the gateway works but UI won't show the provider. Confirm: is full UI integration required?
5. **CI**: `.github/workflows/pr-tests.yml` and `release-pipeline.yml` need env vars added (same pattern as openrouter).

## Next step (after plan approval)
Confirm resolution to open questions (#3 — core retry logic change; #4 — UI integration required) and approve. Once confirmed, execute step 1 (read context) and proceed with TDD cycle per step.

Plan saved at: .hermes/plans/2026-09-14_openrouter-free-provider.md
Not implemented yet — plan mode only.