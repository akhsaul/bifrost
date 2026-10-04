import { quoteTomlString } from "@/app/workspace/mcp-registry/views/mcpUsageGuide/utils";
import {
	DEFAULT_BIFROST_ENV_VAR,
	DEFAULT_CLAUDE_ENV_VAR,
	VK_VALUE_PLACEHOLDER,
	type AgentId,
	type ManualModelMetadata,
} from "./agentSetupTypes";

export interface AgentConfigInput {
	/** Bifrost origin, e.g. http://127.0.0.1:8080 (no trailing slash). */
	baseUrl: string;
	/** Agent-side model string: direct model name or rule CEL trigger. */
	model: string;
	/** Resolved display/provider name for the opencode provider block. */
	providerName: string;
	/** Direct-model source only: datasheet limits used for opencode `limit`. */
	limit?: { context?: number; output?: number };
	/** Routing-rule source only: user-supplied metadata (empty values omitted). */
	manualMetadata?: ManualModelMetadata;
	/** Extra user-defined parameters merged into the request payload. */
	extraParams: Record<string, string>;
	/** Env var name carrying the credential (editable). */
	envVar: string;
	/** Virtual key display name, rendered as a comment (never the secret). */
	virtualKeyName?: string;
}

export interface AgentConfigOutput {
	/** Main config snippet shown in the preview + used for Download. */
	config: string;
	/** Shell export block (BIFROST_API_KEY / ANTHROPIC_*). */
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
// Custom provider via @opencode-ai/ai/providers/openai-compatible:
// "env" declares the credential variable, settings.apiKey uses the {env:}
// substitution so no literal secret lands in the file.

function buildOpencode(input: AgentConfigInput, platform: "macos" | "windows" | "linux"): AgentConfigOutput {
	const envVar = input.envVar || DEFAULT_BIFROST_ENV_VAR;
	const datasourceLimit = input.limit ?? resolveManualLimit(input.manualMetadata);
	const limit = datasourceLimit
		? {
				...(datasourceLimit.context ? { context: datasourceLimit.context } : {}),
				...(datasourceLimit.output ? { output: datasourceLimit.output } : {}),
			}
		: undefined;

	const providerBlock = {
		name: "Bifrost",
		env: [envVar],
		package: "@opencode-ai/ai/providers/openai-compatible",
		settings: {
			baseURL: `${input.baseUrl}/v1`,
			apiKey: `{env:${envVar}}`,
		},
		models: {
			[input.model]: {
				name: input.model,
				...(limit && Object.keys(limit).length > 0 ? { limit } : {}),
			},
		},
	};

	const vkComment = input.virtualKeyName ? `// Virtual key: ${input.virtualKeyName} — set ${envVar} in your shell.\n` : "";
	const extraComment = extraParamsBlock(input.extraParams);
	const header = `${vkComment}${extraComment ? `// Extra params (merge into your request):\n${extraComment}\n` : ""}`;
	const config = `${header}${JSON.stringify({ providers: { [input.providerName]: providerBlock } }, null, 2)}`;
	const shellExports = buildShellExports(envVar, platform, `Set once, then launch opencode from the same shell`);

	return {
		config,
		shellExports,
		sections: [
			{ label: "Provider block", content: config },
			{ label: "Shell exports", content: shellExports },
		],
	};
}

// ── Codex ────────────────────────────────────────────────────────────────
// Custom provider via [model_providers.<id>]: env_key names the variable
// Codex reads the key from (never a literal in the file), wire_api
// responses is required for custom providers.

function buildCodex(input: AgentConfigInput, platform: "macos" | "windows" | "linux"): AgentConfigOutput {
	const envVar = input.envVar || DEFAULT_BIFROST_ENV_VAR;
	const vkComment = input.virtualKeyName ? `# Virtual key: ${input.virtualKeyName} — set ${envVar} in your shell.\n` : "";
	const extraComment = extraParamsBlock(input.extraParams);

	const lines = [
		`${vkComment}${extraComment ? `# Extra params (merge into your request):\n${extraComment}\n` : ""}model = ${quoteTomlString(input.model)}`,
		`model_provider = ${quoteTomlString(input.providerName)}`,
		"",
		`[model_providers.${input.providerName}]`,
		`name = "Bifrost"`,
		`base_url = ${quoteTomlString(`${input.baseUrl}/v1`)}`,
		`env_key = ${quoteTomlString(envVar)}`,
		`wire_api = "responses"`,
	];

	const config = lines.join("\n");
	const shellExports = buildShellExports(envVar, platform, `Set once, then launch codex from the same shell`);

	return {
		config,
		shellExports,
		sections: [
			{ label: "Provider block", content: config },
			{ label: "Shell exports", content: shellExports },
		],
	};
}

// ── Claude Code ──────────────────────────────────────────────────────────
// settings.json env blocks take literal values only, so the recommended path
// is shell exports (secret never touches disk). The settings.json fragment
// carries the placeholder for users who prefer a file.

function buildClaudeCode(input: AgentConfigInput, platform: "macos" | "windows" | "linux"): AgentConfigOutput {
	const envVar = input.envVar || DEFAULT_CLAUDE_ENV_VAR;
	const baseUrl = `${input.baseUrl}/anthropic`;

	const exportsBlock =
		platform === "windows"
			? [`$env:ANTHROPIC_BASE_URL = "${baseUrl}"`, `$env:${envVar} = "${VK_VALUE_PLACEHOLDER}"`].join("\n")
			: [`export ANTHROPIC_BASE_URL="${baseUrl}"`, `export ${envVar}="${VK_VALUE_PLACEHOLDER}"`].join("\n");

	const vkComment = input.virtualKeyName ? `// Virtual key: ${input.virtualKeyName}\n` : "";
	const settingsFragment = `${vkComment}${JSON.stringify(
		{
			env: {
				ANTHROPIC_BASE_URL: baseUrl,
				[envVar]: VK_VALUE_PLACEHOLDER,
			},
			model: input.model,
		},
		null,
		2,
	)}`;

	const shellExports = `# Recommended: secret stays out of the file.\n${exportsBlock}`;

	return {
		config: settingsFragment,
		shellExports,
		sections: [
			{ label: "Shell exports (recommended)", content: shellExports },
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