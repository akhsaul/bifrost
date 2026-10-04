/**
 * Agent Setup — shared types and constants.
 *
 * Generates agent-side model configs (opencode / claude code / codex) that
 * point at Bifrost. The agent always sends a single `model` string:
 *  - Direct model: the provider model name (e.g. `opencode-zen/glm-5.3`).
 *  - Routing rule: the rule's CEL model trigger (e.g. `test-route`), which a
 *    rule matches via `model == "<trigger>"` and rewrites to its target.
 *    Triggers on other CEL variables (headers, etc.) cannot be driven by an
 *    agent's model field, so rules whose CEL is not a simple
 *    `model == "<literal>"` are hidden from the picker.
 */

export type AgentId = "opencode" | "claude-code" | "codex";
export type AgentPlatform = "macos" | "windows" | "linux";
export type ModelSource = "direct" | "rule";

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