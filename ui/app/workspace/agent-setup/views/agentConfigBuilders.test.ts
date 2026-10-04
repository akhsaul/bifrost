import { describe, expect, it } from "vitest";
import { buildAgentConfig } from "./agentConfigBuilders";
import { extractModelTrigger } from "./agentSetupTypes";

const BASE = {
	baseUrl: "http://127.0.0.1:8080",
	model: "test-route",
	providerName: "bifrost",
	extraParams: {},
	envVar: "BIFROST_API_KEY",
};

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
	it("builds an opencode provider block with {env:} substitution and no literal secret", () => {
		const out = buildAgentConfig("opencode", { ...BASE, virtualKeyName: "evaluate-test-route" }, "linux");
		expect(out.config).toContain("{env:BIFROST_API_KEY}");
		expect(out.config).toContain('"baseURL": "http://127.0.0.1:8080/v1"');
		expect(out.config).toContain("test-route");
		expect(out.config).toContain("evaluate-test-route");
		expect(out.config).not.toContain("sk-bf-");
		expect(out.shellExports).toContain('export BIFROST_API_KEY="<paste-your-virtual-key-value>"');
	});

	it("builds a codex TOML block with env_key and responses wire api", () => {
		const out = buildAgentConfig("codex", BASE, "macos");
		expect(out.config).toContain('model = "test-route"');
		expect(out.config).toContain('env_key = "BIFROST_API_KEY"');
		expect(out.config).toContain('wire_api = "responses"');
		expect(out.config).toContain('base_url = "http://127.0.0.1:8080/v1"');
		expect(out.config).not.toContain("sk-bf-");
	});

	it("builds claude code shell exports against /anthropic with literal placeholder fragment", () => {
		const out = buildAgentConfig("claude-code", { ...BASE, envVar: "ANTHROPIC_AUTH_TOKEN" }, "linux");
		expect(out.shellExports).toContain('export ANTHROPIC_BASE_URL="http://127.0.0.1:8080/anthropic"');
		expect(out.shellExports).toContain('export ANTHROPIC_AUTH_TOKEN="<paste-your-virtual-key-value>"');
		expect(out.config).toContain('"model": "test-route"');
	});

	it("omits empty manual metadata and applies provided limits to opencode", () => {
		const empty = buildAgentConfig(
			"opencode",
			{ ...BASE, manualMetadata: { contextWindow: "", maxInputTokens: "", maxOutputTokens: "", reasoningEffort: "" } },
			"linux",
		);
		expect(empty.config).not.toContain('"limit"');

		const withLimits = buildAgentConfig(
			"opencode",
			{ ...BASE, manualMetadata: { contextWindow: "1000000", maxInputTokens: "", maxOutputTokens: "128000", reasoningEffort: "max" } },
			"linux",
		);
		expect(withLimits.config).toContain('"context": 1000000');
		expect(withLimits.config).toContain('"output": 128000');
	});

	it("emits windows-style shell exports on windows", () => {
		const out = buildAgentConfig("codex", BASE, "windows");
		expect(out.shellExports).toContain('$env:BIFROST_API_KEY = "<paste-your-virtual-key-value>"');
	});
});