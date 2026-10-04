import { quoteTomlString } from "@/app/workspace/mcp-registry/views/mcpUsageGuide/utils";
import {
	DEFAULT_BIFROST_ENV_VAR,
	DEFAULT_CLAUDE_ENV_VAR,
	VK_VALUE_PLACEHOLDER,
	type AgentId,
	type AgentModelMetadata,
} from "./agentSetupTypes";

export interface AgentOpencodeModelInput {
	/** Agent-sendable model string: `provider/model` or rule trigger. */
	id: string;
	/** Agent-native metadata for this entry (v2 keys only). */
	metadata: AgentModelMetadata;
	/** Per-token costs; opencode wants per-1M so the builder multiplies by 1e6. */
	costPerToken?: { input?: number; output?: number; cacheRead?: number; cacheWrite?: number };
	/** Rule entries note their targets in a comment. */
	ruleTargets?: string;
}

export interface AgentConfigInput {
	/** Bifrost origin, e.g. http://127.0.0.1:8080 (no trailing slash). */
	baseUrl: string;
	/** Every model entry that lands in the generated config. Empty → no output. */
	models: AgentOpencodeModelInput[];
	/**
	 * Virtual key in play, if any. No VK → the config carries no credential
	 * at all. A VK → the credential is referenced by env var only; the
	 * secret itself never lands in the output.
	 */
	virtualKeyName?: string;
	/** Env var name carrying the credential (editable, ignored without a VK). */
	envVar: string;
	/** Codex/claude only: which selected model is the default. Falls back to the first selection. */
	defaultModelId?: string;
	/** Codex only: `model_context_window` / `model_max_output_tokens` globals (default model's datasheet, editable). */
	codexContextWindow?: string;
	codexMaxOutputTokens?: string;
	/** Codex only: `model_reasoning_effort` global (preference knob, not datasheet). */
	codexReasoningEffort?: string;
}

export interface AgentConfigOutput {
	/** Main config snippet shown in the preview + used for Download. */
	config: string;
	/** POSIX shell export hint, or "" when no virtual key is picked. */
	shellExports: string;
	/** Copyable per-section blocks: [label, content]. */
	sections: { label: string; content: string }[];
}

function posixShellExports(envVar: string, extraComment?: string): string {
	const comment = extraComment ? `# ${extraComment}\n` : "";
	return `${comment}export ${envVar}="${VK_VALUE_PLACEHOLDER}"`;
}

function parsePositiveInt(raw: string): number | undefined {
	const n = Number.parseInt(raw, 10);
	return Number.isFinite(n) && n > 0 ? n : undefined;
}

function perMillion(perToken: number | undefined): number | undefined {
	if (perToken === undefined || !Number.isFinite(perToken)) return undefined;
	return perToken * 1_000_000;
}

// ── OpenCode (v2) ──────────────────────────────────────────────────────
// One Bifrost provider entry under `providers`; every selected model (direct
// or rule trigger) becomes a key under `models`. Auth only exists with a VK:
// env-var indirection via `env` + {env:} so no literal secret lands in the
// file. Per-model v2 keys only: `limit`, `capabilities`, `cost` (per-1M),
// `variants` (checked reasoning efforts only; omitted when empty).

interface OpencodeModelBlock {
	name: string;
	limit: { context: number; output: number };
	capabilities: { tools: boolean; input?: string[]; output?: string[] };
	cost?: { input?: number; output?: number; cache_read?: number; cache_write?: number };
	variants?: { id: string; settings: { reasoningEffort: string } }[];
}

function buildOpencodeModelBlock(item: AgentOpencodeModelInput): OpencodeModelBlock | null {
	const context = parsePositiveInt(item.metadata.limitContext);
	const output = parsePositiveInt(item.metadata.limitOutput);
	// v2 requires limit.context/output; without them the model entry is invalid.
	if (context === undefined || output === undefined) return null;
	const block: OpencodeModelBlock = {
		name: item.id,
		limit: { context, output },
		capabilities: {
			tools: item.metadata.tools,
			...(item.metadata.inputModalities.length > 0 ? { input: item.metadata.inputModalities } : {}),
			...(item.metadata.outputModalities.length > 0 ? { output: item.metadata.outputModalities } : {}),
		},
	};
	const cost = {
		...(perMillion(item.costPerToken?.input) !== undefined ? { input: perMillion(item.costPerToken?.input) as number } : {}),
		...(perMillion(item.costPerToken?.output) !== undefined ? { output: perMillion(item.costPerToken?.output) as number } : {}),
		...(perMillion(item.costPerToken?.cacheRead) !== undefined ? { cache_read: perMillion(item.costPerToken?.cacheRead) as number } : {}),
		...(perMillion(item.costPerToken?.cacheWrite) !== undefined
			? { cache_write: perMillion(item.costPerToken?.cacheWrite) as number }
			: {}),
	};
	if (Object.keys(cost).length > 0) block.cost = cost;
	const variants = item.metadata.variantEfforts.map((effort) => ({ id: effort, settings: { reasoningEffort: effort } }));
	if (variants.length > 0) block.variants = variants;
	return block;
}

function buildOpencode(input: AgentConfigInput): AgentConfigOutput {
	const hasAuth = !!input.virtualKeyName;
	const envVar = input.envVar || DEFAULT_BIFROST_ENV_VAR;

	const models: Record<string, OpencodeModelBlock> = {};
	for (const item of input.models) {
		const block = buildOpencodeModelBlock(item);
		if (block) models[item.id] = block;
	}

	const providerBlock = {
		name: "Bifrost",
		...(hasAuth ? { env: [envVar] } : {}),
		package: "@opencode/ai/providers/openai-compatible",
		settings: {
			baseURL: `${input.baseUrl}/v1`,
			...(hasAuth ? { apiKey: `{env:${envVar}}` } : {}),
		},
		models,
	};

	const vkComment = hasAuth ? `// Virtual key: ${input.virtualKeyName} — set ${envVar} in your shell.\n` : "";
	const config = `${vkComment}${JSON.stringify({ providers: { bifrost: providerBlock } }, null, 2)}`;
	const shellExports = hasAuth ? posixShellExports(envVar, `Set once, then launch opencode from the same shell`) : "";

	return {
		config,
		shellExports,
		sections: [...(hasAuth ? [{ label: "Shell exports", content: shellExports }] : [])],
	};
}

// ── Codex ──────────────────────────────────────────────────────────────
// One [model_providers.bifrost] block; `model` is the user-chosen default.
// Globals `model_context_window` / `model_max_output_tokens` come from the
// default model's datasheet (editable); `model_reasoning_effort` is a
// preference knob. With a VK the credential arrives via env_key; without one
// no credential fields are emitted at all.

function buildCodex(input: AgentConfigInput): AgentConfigOutput {
	const hasAuth = !!input.virtualKeyName;
	const envVar = input.envVar || DEFAULT_BIFROST_ENV_VAR;
	const defaultId = input.defaultModelId ?? input.models[0]?.id ?? "";
	const contextWindow = parsePositiveInt(input.codexContextWindow ?? "");
	const maxOutputTokens = parsePositiveInt(input.codexMaxOutputTokens ?? "");
	const vkComment = hasAuth ? `# Virtual key: ${input.virtualKeyName} — set ${envVar} in your shell.\n` : "";

	const lines = [
		`${vkComment}model = ${quoteTomlString(defaultId)}`,
		`model_provider = "bifrost"`,
		...(contextWindow !== undefined ? [`model_context_window = ${contextWindow}`] : []),
		...(maxOutputTokens !== undefined ? [`model_max_output_tokens = ${maxOutputTokens}`] : []),
		...(input.codexReasoningEffort ? [`model_reasoning_effort = ${quoteTomlString(input.codexReasoningEffort)}`] : []),
		"",
		`[model_providers.bifrost]`,
		`name = "Bifrost"`,
		`base_url = ${quoteTomlString(`${input.baseUrl}/v1`)}`,
		...(hasAuth ? [`env_key = ${quoteTomlString(envVar)}`] : []),
		`wire_api = "responses"`,
	];

	const config = lines.join("\n");
	const shellExports = hasAuth ? posixShellExports(envVar, `Set once, then launch codex from the same shell`) : "";

	return {
		config,
		shellExports,
		sections: [...(hasAuth ? [{ label: "Shell exports", content: shellExports }] : [])],
	};
}

// ── Claude Code ──────────────────────────────────────────────────────────
// settings.json first: the fragment carries only secret-free keys (base URL
// + model). The VK secret lives only in the POSIX shell-export hint, never
// in the JSON. Without a VK the fragment is just base URL + model.

function buildClaudeCode(input: AgentConfigInput): AgentConfigOutput {
	const hasAuth = !!input.virtualKeyName;
	const envVar = input.envVar || DEFAULT_CLAUDE_ENV_VAR;
	const baseUrl = `${input.baseUrl}/anthropic`;
	const defaultId = input.defaultModelId ?? input.models[0]?.id ?? "";

	const settingsFragment = `${hasAuth && input.virtualKeyName ? `// Virtual key: ${input.virtualKeyName}\n` : ""}${JSON.stringify(
		{
			env: {
				ANTHROPIC_BASE_URL: baseUrl,
			},
			...(defaultId ? { model: defaultId } : {}),
		},
		null,
		2,
	)}`;

	const shellExports = hasAuth ? posixShellExports(envVar, `Virtual key ${input.virtualKeyName}: set once in your shell`) : "";

	return {
		config: settingsFragment,
		shellExports,
		sections: [...(hasAuth ? [{ label: "Shell exports (secret lives here, not in the file)", content: shellExports }] : [])],
	};
}

export function buildAgentConfig(agent: AgentId, input: AgentConfigInput): AgentConfigOutput {
	switch (agent) {
		case "codex":
			return buildCodex(input);
		case "claude-code":
			return buildClaudeCode(input);
		case "opencode":
		default:
			return buildOpencode(input);
	}
}