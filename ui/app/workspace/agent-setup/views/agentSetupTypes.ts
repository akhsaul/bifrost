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
 * Auth model (from the agent's point of view a virtual key IS the api key):
 *  - No virtual key picked → the config carries no credential at all (no
 *    apiKey / env_key / auth token, no shell exports for secrets).
 *  - Virtual key picked (at most one) → provider/model/rule lists are
 *    narrowed to what that key may reach, and the output references the
 *    credential by env var only. The secret itself is never written into
 *    the config — only the key name as a comment plus a placeholder the
 *    user pastes into their own environment.
 */

export type AgentId = "opencode" | "claude-code" | "codex";
export type AgentPlatform = "macos" | "windows" | "linux";

export interface AgentDefinition {
	id: AgentId;
	label: string;
	logoSrc: string;
	configPath: Record<AgentPlatform, string>;
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
		configPath: {
			macos: "~/.config/opencode/opencode.json",
			linux: "~/.config/opencode/opencode.json",
			windows: "%APPDATA%/opencode/opencode.json",
		},
		downloadFileName: "opencode-bifrost.json",
		previewLang: "json",
	},
	{
		id: "claude-code",
		label: "Claude Code",
		logoSrc: "/images/harness/claudecode.svg",
		configPath: {
			macos: "~/.claude/settings.json",
			linux: "~/.claude/settings.json",
			windows: "%USERPROFILE%/.claude/settings.json",
		},
		downloadFileName: "claude-settings-bifrost.json",
		previewLang: "json",
	},
	{
		id: "codex",
		label: "Codex",
		logoSrc: "/images/harness/codex.svg",
		configPath: {
			macos: "~/.codex/config.toml",
			linux: "~/.codex/config.toml",
			windows: "%USERPROFILE%/.codex/config.toml",
		},
		downloadFileName: "bifrost-config.toml",
		previewLang: "toml",
	},
];

export const AGENT_PLATFORMS: AgentPlatform[] = ["macos", "windows", "linux"];

/** Env var name carrying the Bifrost credential for opencode/codex snippets. */
export const DEFAULT_BIFROST_ENV_VAR = "BIFROST_API_KEY";
/** Env var name carrying the Bifrost credential for Claude Code shell exports. */
export const DEFAULT_CLAUDE_ENV_VAR = "ANTHROPIC_AUTH_TOKEN";

/** Stand-in for the virtual-key secret the user pastes into their own environment. */
export const VK_VALUE_PLACEHOLDER = "<paste-your-virtual-key-value>";

export interface ManualModelMetadata {
	contextWindow: string;
	maxInputTokens: string;
	maxOutputTokens: string;
	reasoningEffort: string;
}

export const EMPTY_MANUAL_METADATA: ManualModelMetadata = {
	contextWindow: "",
	maxInputTokens: "",
	maxOutputTokens: "",
	reasoningEffort: "",
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
	/** Direct-model limits from the datasheet (feeds opencode `limit`). */
	limit?: { context?: number; output?: number };
	/** Rule-entry manual metadata (empty values omitted from output). */
	manualMetadata?: ManualModelMetadata;
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