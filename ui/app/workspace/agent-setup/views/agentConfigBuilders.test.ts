import { describe, expect, it } from "vitest";
import { buildAgentConfig } from "./agentConfigBuilders";
import { extractModelTrigger } from "./agentSetupTypes";

function multiInput(extra = {}) {
	return {
		baseUrl: "http://127.0.0.1:8080",
		models: [
			{ id: "opencode-zen/glm-5.3", kind: "direct" as const, provider: "opencode-zen", model: "glm-5.3", limit: { context: 200000 } },
			{
				id: "test-route",
				kind: "rule" as const,
				ruleId: "r1",
				ruleTargets: "opencode-zen/space-bunny-free",
				manualMetadata: { contextWindow: "1000000", maxInputTokens: "", maxOutputTokens: "128000", reasoningEffort: "" },
			},
		],
		extraParams: {},
		envVar: "BIFROST_API_KEY",
		...extra,
	};
}

describe("extractModelTrigger", () => {
	it("extracts the literal from a simple model equality CEL", () => {
		expect(extractModelTrigger('model == "test-route"')).toBe("test-route");
		expect(extractModelTrigger("model == 'bifrost-smart-1m'")).toBe("bifrost-smart-1m");
	});

	it("returns null for non-triggerable CEL shapes", () => {
		expect(extractModelTrigger('model == "a" || model == "b"')).toBeNull();
		expect(extractModelTrigger("model.matches('^gpt.*')")).toBeNull();
		expect(extractModelTrigger("headers['x-tier'] == 'premium'")).toBeNull();
		expect(extractModelTrigger("")).toBeNull();
		expect(extractModelTrigger(undefined)).toBeNull();
	});
});

describe("buildAgentConfig", () => {
	it("builds an opencode provider block with every selected model and {env:} auth only with a VK", () => {
		const withVk = buildAgentConfig("opencode", multiInput({ virtualKeyName: "evaluate-test-route" }), "linux");
		expect(withVk.config).toContain("{env:BIFROST_API_KEY}");
		expect(withVk.config).toContain('"baseURL": "http://127.0.0.1:8080/v1"');
		expect(withVk.config).toContain("opencode-zen/glm-5.3");
		expect(withVk.config).toContain("test-route");
		expect(withVk.config).toContain("evaluate-test-route");
		expect(withVk.config).toContain('"context": 200000');
		expect(withVk.config).toContain('"output": 128000');
		expect(withVk.config).not.toContain("sk-bf-");
		expect(withVk.shellExports).toContain('export BIFROST_API_KEY="<paste-your-virtual-key-value>"');

		const withoutVk = buildAgentConfig("opencode", multiInput(), "linux");
		expect(withoutVk.config).toContain("opencode-zen/glm-5.3");
		expect(withoutVk.config).not.toContain("apiKey");
		expect(withoutVk.config).not.toContain('"env"');
		expect(withoutVk.config).not.toContain("<paste-your-virtual-key-value>");
		expect(withoutVk.shellExports).toBe("");
		expect(withoutVk.sections).toEqual([]);
	});

	it("builds a codex TOML block with env_key only with a VK", () => {
		const withVk = buildAgentConfig("codex", multiInput({ virtualKeyName: "k" }), "macos");
		expect(withVk.config).toContain('model = "opencode-zen/glm-5.3"');
		expect(withVk.config).toContain("# Models: opencode-zen/glm-5.3, test-route (rule → opencode-zen/space-bunny-free)");
		expect(withVk.config).toContain('env_key = "BIFROST_API_KEY"');
		expect(withVk.config).toContain('wire_api = "responses"');
		expect(withVk.config).not.toContain("sk-bf-");

		const withoutVk = buildAgentConfig("codex", multiInput(), "macos");
		expect(withoutVk.config).not.toContain("env_key");
		expect(withoutVk.config).not.toContain("<paste-your-virtual-key-value>");
		expect(withoutVk.shellExports).toBe("");
	});

	it("builds claude code output with credential fields only with a VK", () => {
		const withVk = buildAgentConfig("claude-code", multiInput({ virtualKeyName: "k", envVar: "ANTHROPIC_AUTH_TOKEN" }), "linux");
		expect(withVk.shellExports).toContain('export ANTHROPIC_BASE_URL="http://127.0.0.1:8080/anthropic"');
		expect(withVk.shellExports).toContain('export ANTHROPIC_AUTH_TOKEN="<paste-your-virtual-key-value>"');
		expect(withVk.config).toContain('"model": "opencode-zen/glm-5.3"');

		const withoutVk = buildAgentConfig("claude-code", multiInput({ envVar: "ANTHROPIC_AUTH_TOKEN" }), "linux");
		expect(withoutVk.config).not.toContain("ANTHROPIC_AUTH_TOKEN");
		expect(withoutVk.config).not.toContain("<paste-your-virtual-key-value>");
		expect(withoutVk.shellExports).toBe('export ANTHROPIC_BASE_URL="http://127.0.0.1:8080/anthropic"');
	});

	it("emits windows-style shell exports on windows", () => {
		const out = buildAgentConfig("codex", multiInput({ virtualKeyName: "k" }), "windows");
		expect(out.shellExports).toContain('$env:BIFROST_API_KEY = "<paste-your-virtual-key-value>"');
	});
});