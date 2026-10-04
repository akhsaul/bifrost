/**
 * Agent Setup — shared types and constants.
 *
 * Generates agent-side model configs (opencode / claude code / codex) that
 * point at Bifrost. The agent config can carry many models at once:
 *  - Direct models: qualified `provider/model` names (e.g.
 *    `opencode-zen/glm-5.3`), exactly as `/v1/models` lists them.
 *  - Routing rules: each selected rule contributes the CEL model trigger
 *    from its simple `model == "<trigger>"` expression (e.g. `test-route`),
 *    which the rule matches and rewrites to its target. Triggers on other
 *    CEL variables (headers, etc.) cannot be driven by an agent's model
 *    field, so rules whose CEL is not a simple `model == "<literal>"` are
 *    hidden from the picker.
 *
 * Metadata rule: only keys the target agent's own schema supports are ever
 * emitted, and every value is datasheet-prefilled but user-editable ("empty
 * filled by datasheet" — datasheet value lands in the input, empty input
 * when the datasheet has nothing). Per-agent sources of truth:
 *  - opencode v2 docs (https://opencode.ai/v2/docs/models): per-model
 *    `capabilities { tools, input, output }`, `limit { context, output }`,
 *    `cost` per-1M tokens, `variants [{ id, settings: { reasoningEffort } }]`.
 *    NOTE: https://opencode.ai/config.json still describes the v1 schema
 *    (`provider` singular, `tool_call`, `modalities`); the v2 docs shape is
 *    what current opencode loads, so builders emit v2.
 *  - codex config reference: `[model_providers.<id>]` + top-level globals
 *    `model_context_window`, `model_max_output_tokens`, `model_reasoning_effort`.
 *  - claude code gateway docs: settings.json `env` fragment carries
 *    `ANTHROPIC_BASE_URL` + `ANTHROPIC_DEFAULT_HAIKU_MODEL` /
 *    `ANTHROPIC_DEFAULT_SONNET_MODEL` (both the chosen default model) plus
 *    the auth-token key with the `<paste-your-virtual-key-value>`
 *    placeholder when a VK is picked — the VK secret never lands in the
 *    file itself (top-level `model` is kept alongside).
 *
 * Auth model (from the agent's point of view a virtual key IS the api key):
 *  - No virtual key picked → the config carries no credential at all (no
 *    apiKey / env_key / auth token, no shell exports for secrets).
 *  - Virtual key picked (at most one) → provider/model/rule lists are
 *    narrowed to what that key may reach, and the output references the
 *    credential without ever writing the real secret: opencode uses
 *    env-var indirection (`env` + `{env:}`), codex uses `env_key`, and
 *    claude code inlines the env-var key with the
 *    `<paste-your-virtual-key-value>` placeholder (plus the VK name as a
 *    comment). A POSIX shell-export hint is always included alongside.
 */

export type AgentId = "opencode" | "claude-code" | "codex";

/**
 * Static config-path hint shown per agent (no platform selector — the paths
 * differ trivially and shell syntax is always POSIX `export ...`).
 */
export interface AgentDefinition {
	id: AgentId;
	label: string;
	logoSrc: string;
	configPathNote: string;
	/** Filename used for the Download button. */
	downloadFileName: string;
	/** Monaco language for the preview block. */
	previewLang: string;
}

export const AGENTS: AgentDefinition[] = [
	{
		id: "opencode",
		label: "OpenCode",
		logoSrc: "/images/harness/opencode.svg",
		configPathNote: "macOS/Linux: ~/.config/opencode/opencode.json · Windows: %APPDATA%/opencode/opencode.json",
		downloadFileName: "opencode-bifrost.json",
		previewLang: "json",
	},
	{
		id: "claude-code",
		label: "Claude Code",
		logoSrc: "/images/harness/claudecode.svg",
		configPathNote: "macOS/Linux: ~/.claude/settings.json · Windows: %USERPROFILE%/.claude/settings.json",
		downloadFileName: "claude-settings-bifrost.json",
		previewLang: "json",
	},
	{
		id: "codex",
		label: "Codex",
		logoSrc: "/images/harness/codex.svg",
		configPathNote: "macOS/Linux: ~/.codex/config.toml · Windows: %USERPROFILE%/.codex/config.toml",
		downloadFileName: "bifrost-config.toml",
		previewLang: "toml",
	},
];

/** Env var name carrying the Bifrost credential for opencode/codex snippets. */
export const DEFAULT_BIFROST_ENV_VAR = "BIFROST_API_KEY";
/** Env var name carrying the Bifrost credential for Claude Code shell exports. */
export const DEFAULT_CLAUDE_ENV_VAR = "ANTHROPIC_AUTH_TOKEN";

/** Stand-in for the virtual-key secret the user pastes into their own environment. */
export const VK_VALUE_PLACEHOLDER = "<paste-your-virtual-key-value>";

/** Fallback reasoning efforts when the datasheet has no reasoning-effort parameter. */
export const DEFAULT_REASONING_EFFORTS = ["low", "medium", "high", "xhigh", "max", "auto"];

/** Codex reasoning-effort select options (config reference). */
export const CODEX_REASONING_EFFORTS = ["minimal", "low", "medium", "high", "xhigh"] as const;

/** Agent-native per-model metadata: every field maps to a key the agent schema supports. */
export interface AgentModelMetadata {
	/** opencode `limit.context` ← datasheet max_input_tokens else context_length; rule entries start empty. */
	limitContext: string;
	/** opencode `limit.output` ← datasheet max_output_tokens; rule entries start empty. */
	limitOutput: string;
	/** opencode `capabilities.tools`; always true, user can disable. */
	tools: boolean;
	/** opencode `capabilities.input`; datasheet-prefilled, editable. */
	inputModalities: string[];
	/** opencode `capabilities.output`; datasheet-prefilled, editable. */
	outputModalities: string[];
	/** opencode `variants` reasoning efforts; datasheet options else DEFAULT_REASONING_EFFORTS, checklist. */
	variantEfforts: string[];
}

export const EMPTY_AGENT_METADATA: AgentModelMetadata = {
	limitContext: "",
	limitOutput: "",
	tools: true,
	inputModalities: [],
	outputModalities: [],
	variantEfforts: [],
};

/** One selectable entry in the Models step: a direct model or a rule trigger. */
export interface ModelSelectionItem {
	/** Agent-sendable model string: `provider/model` or rule trigger. */
	id: string;
	/** Direct provider/model vs routing-rule trigger. */
	kind: "direct" | "rule";
	/** Direct only: provider part of the qualified name. */
	provider?: string;
	/** Direct only: bare model name. */
	model?: string;
	/** Rule only: the routing rule id. */
	ruleId?: string;
	/** Rule only: targets summary, e.g. `opencode-zen/space-bunny-free`. */
	ruleTargets?: string;
	/** Direct-model datasheet snapshot feeding the agent-native metadata form (v2 keys). */
	datasheet?: {
		/** max_input_tokens else context_length. */
		context?: number;
		/** max_output_tokens. */
		output?: number;
		inputModalities?: string[];
		outputModalities?: string[];
		/** Per-token costs; opencode wants per-1M so the builder multiplies by 1e6. */
		costPerToken?: { input?: number; output?: number; cacheRead?: number; cacheWrite?: number };
		/** Reasoning-effort option values from model_parameters (id matching /reasoning.*effort/i). */
		reasoningEfforts?: string[];
	};
}

/**
 * Extract the agent-sendable model trigger from a rule's CEL expression.
 * Only the simple shape `model == "<literal>"` can be triggered through an
 * agent's model field — anything else (header checks, matches(), compound
 * conditions) returns null and the rule is excluded from the picker.
 */
export function extractModelTrigger(celExpression: string | undefined): string | null {
	if (!celExpression) return null;
	const match = celExpression.trim().match(/^model\s*==\s*["']([^"']+)["']$/);
	return match ? match[1] : null;
}

/**
 * Find reasoning-effort option values in a datasheet parameter list: the
 * parameter whose id matches /reasoning.*effort/i, with an options array.
 * Returns null when the datasheet has no such parameter.
 */
export function extractReasoningEfforts(params: { id: string; options?: { value: string }[] }[] | undefined): string[] | null {
	const found = (params ?? []).find((p) => /reasoning.*effort/i.test(p.id) && Array.isArray(p.options));
	if (!found?.options?.length) return null;
	return found.options.map((o) => o.value).filter(Boolean);
}