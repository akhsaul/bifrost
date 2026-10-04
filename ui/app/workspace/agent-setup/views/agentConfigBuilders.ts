import { quoteTomlString } from "@/app/workspace/mcp-registry/views/mcpUsageGuide/utils";
import {
	DEFAULT_BIFROST_ENV_VAR,
	DEFAULT_CLAUDE_ENV_VAR,
	VK_VALUE_PLACEHOLDER,
	type AgentId,
	type ManualModelMetadata,
	type ModelSelectionItem,
} from "./agentSetupTypes";

export interface AgentConfigInput {
	/** Bifrost origin, e.g. http://127.0.0.1:8080 (no trailing slash). */
	baseUrl: string;
	/** Every model entry that lands in the generated config. Empty → no output. */
	models: ModelSelectionItem[];
	/**
	 * Virtual key in play, if any. No VK → the config carries no credential
	 * at all. A VK → the credential is referenced by env var only; the
	 * secret itself never lands in the output.
	 */
	virtualKeyName?: string;
	/** Extra user-defined parameters merged into the request payload. */
	extraParams: Record<string, string>;
	/** Env var name carrying the credential (editable, ignored without a VK). */
	envVar: string;
}

export interface AgentConfigOutput {
	/** Main config snippet shown in the preview + used for Download. */
	config: string;
	/** Shell export block, or "" when no virtual key is picked. */
	shellExports: string;
	/** Copyable per-section blocks: [label, content]. */
	sections: { label: string; content: string }[];
}

function shellExportLines(envVar: string): string[] {
	return [`export ${envVar}="${VK_VALUE_PLACEHOLDER}"`];
}

function shellExportLinesWindows(envVar: string): string[] {
	return [`$env:${envVar} = "${VK_VALUE_PLACEHOLDER}"`];
}

function extraParamsBlock(params: Record<string, string>): string {
	const entries = Object.entries(params);
	if (entries.length === 0) return "";
	return entries.map(([k, v]) => `#   ${k}: ${v}`).join("\n");
}

function resolveManualLimit(manual?: ManualModelMetadata): { context?: number; output?: number } | undefined {
	if (!manual) return undefined;
	const context = Number.parseInt(manual.contextWindow, 10);
	const output = Number.parseInt(manual.maxOutputTokens || manual.maxInputTokens, 10);
	const limit: { context?: number; output?: number } = {};
	if (Number.isFinite(context) && context > 0) limit.context = context;
	if (Number.isFinite(output) && output > 0) limit.output = output;
	return Object.keys(limit).length > 0 ? limit : undefined;
}

function buildShellExports(envVar: string, platform: "macos" | "windows" | "linux", extraComment?: string): string {
	const comment = extraComment ? `# ${extraComment}\n` : "";
	const lines = platform === "windows" ? shellExportLinesWindows(envVar) : shellExportLines(envVar);
	return `${comment}${lines.join("\n")}`;
}

// ── OpenCode ─────────────────────────────────────────────────────────────
// One Bifrost provider entry; every selected model (direct or rule trigger)
// becomes a key under `models`. Auth only exists with a VK: env-var
// indirection via "env" + {env:} so no literal secret lands in the file.

function buildOpencode(input: AgentConfigInput, platform: "macos" | "windows" | "linux"): AgentConfigOutput {
	const hasAuth = !!input.virtualKeyName;
	const envVar = input.envVar || DEFAULT_BIFROST_ENV_VAR;

	const models: Record<string, { name: string; limit?: { context?: number; output?: number } }> = {};
	for (const item of input.models) {
		const limit = item.limit ?? resolveManualLimit(item.manualMetadata);
		models[item.id] = {
			name: item.id,
			...(limit && Object.keys(limit).length > 0 ? { limit } : {}),
		};
	}

	const providerBlock = {
		name: "Bifrost",
		...(hasAuth
			? { env: [envVar], settings: { baseURL: `${input.baseUrl}/v1`, apiKey: `{env:${envVar}}` } }
			: { settings: { baseURL: `${input.baseUrl}/v1` } }),
		models,
		package: "@opencode-ai/ai/providers/openai-compatible",
	};

	const vkComment = hasAuth ? `// Virtual key: ${input.virtualKeyName} — set ${envVar} in your shell.\n` : "";
	const extraComment = extraParamsBlock(input.extraParams);
	const header = `${vkComment}${extraComment ? `// Extra params (merge into your request):\n${extraComment}\n` : ""}`;
	const config = `${header}${JSON.stringify({ providers: { bifrost: providerBlock } }, null, 2)}`;
	const shellExports = hasAuth ? buildShellExports(envVar, platform, `Set once, then launch opencode from the same shell`) : "";

	return {
		config,
		shellExports,
		sections: [...(hasAuth ? [{ label: "Shell exports", content: shellExports }] : [])],
	};
}

// ── Codex ────────────────────────────────────────────────────────────────
// One [model_providers.bifrost] block; `model` is the default the agent
// starts with (first selection). With a VK the credential arrives via
// env_key; without one no credential fields are emitted at all.

function buildCodex(input: AgentConfigInput, platform: "macos" | "windows" | "linux"): AgentConfigOutput {
	const hasAuth = !!input.virtualKeyName;
	const envVar = input.envVar || DEFAULT_BIFROST_ENV_VAR;
	const vkComment = hasAuth ? `# Virtual key: ${input.virtualKeyName} — set ${envVar} in your shell.\n` : "";
	const extraComment = extraParamsBlock(input.extraParams);
	const selectedLabels = input.models.map((m) => (m.kind === "rule" ? `${m.id} (rule → ${m.ruleTargets})` : m.id));

	const lines = [
		`${vkComment}${extraComment ? `# Extra params (merge into your request):\n${extraComment}\n` : ""}# Models: ${selectedLabels.join(", ")}`,
		`model = ${quoteTomlString(input.models[0]?.id ?? "")}`,
		`model_provider = "bifrost"`,
		"",
		`[model_providers.bifrost]`,
		`name = "Bifrost"`,
		`base_url = ${quoteTomlString(`${input.baseUrl}/v1`)}`,
		...(hasAuth ? [`env_key = ${quoteTomlString(envVar)}`] : []),
		`wire_api = "responses"`,
	];

	const config = lines.join("\n");
	const shellExports = hasAuth ? buildShellExports(envVar, platform, `Set once, then launch codex from the same shell`) : "";

	return {
		config,
		shellExports,
		sections: [...(hasAuth ? [{ label: "Shell exports", content: shellExports }] : [])],
	};
}

// ── Claude Code ──────────────────────────────────────────────────────────
// With a VK the recommended path is shell exports (secret never touches
// disk); the settings.json fragment carries the placeholder for users who
// prefer a file. Without a VK only the base URL (and default model) are
// emitted — no credential fields, no placeholder, no secret exports.

function buildClaudeCode(input: AgentConfigInput, platform: "macos" | "windows" | "linux"): AgentConfigOutput {
	const hasAuth = !!input.virtualKeyName;
	const envVar = input.envVar || DEFAULT_CLAUDE_ENV_VAR;
	const baseUrl = `${input.baseUrl}/anthropic`;

	const baseExport = platform === "windows" ? `$env:ANTHROPIC_BASE_URL = "${baseUrl}"` : `export ANTHROPIC_BASE_URL="${baseUrl}"`;
	const secretExport = platform === "windows" ? `$env:${envVar} = "${VK_VALUE_PLACEHOLDER}"` : `export ${envVar}="${VK_VALUE_PLACEHOLDER}"`;
	const exportsBlock = hasAuth ? [baseExport, secretExport].join("\n") : baseExport;

	const vkComment = hasAuth && input.virtualKeyName ? `// Virtual key: ${input.virtualKeyName}\n` : "";
	const allModelsComment = input.models.length > 1 ? `// All selected models: ${input.models.map((m) => m.id).join(", ")}\n` : "";
	const settingsFragment = `${vkComment}${allModelsComment}${JSON.stringify(
		{
			env: {
				ANTHROPIC_BASE_URL: baseUrl,
				...(hasAuth ? { [envVar]: VK_VALUE_PLACEHOLDER } : {}),
			},
			...(input.models[0] ? { model: input.models[0].id } : {}),
		},
		null,
		2,
	)}`;

	const shellExports = hasAuth ? `# Recommended: secret stays out of the file.\n${exportsBlock}` : exportsBlock;

	return {
		config: settingsFragment,
		shellExports,
		sections: [
			{ label: hasAuth ? "Shell exports (recommended)" : "Shell exports (base URL only — no credential)", content: shellExports },
			{ label: "settings.json fragment", content: settingsFragment },
		],
	};
}

export function buildAgentConfig(agent: AgentId, input: AgentConfigInput, platform: "macos" | "windows" | "linux"): AgentConfigOutput {
	switch (agent) {
		case "codex":
			return buildCodex(input, platform);
		case "claude-code":
			return buildClaudeCode(input, platform);
		case "opencode":
		default:
			return buildOpencode(input, platform);
	}
}