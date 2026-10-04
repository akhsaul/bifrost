import { describe, expect, it } from "vitest";
import { buildAgentConfig } from "./agentConfigBuilders";
import { extractModelTrigger, extractReasoningEfforts } from "./agentSetupTypes";

function opencodeModels(extra = {}) {
	return [
		{
			id: "opencode-zen/glm-5.3",
			metadata: {
				limitContext: "200000",
				limitOutput: "32000",
				tools: true,
				inputModalities: ["text", "image"],
				outputModalities: ["text"],
				variantEfforts: ["low", "medium", "high"],
			},
			costPerToken: { input: 0.000002, output: 0.00001 },
		},
		{
			id: "test-route",
			ruleTargets: "opencode-zen/space-bunny-free",
			metadata: {
				limitContext: "1000000",
				limitOutput: "128000",
				tools: true,
				inputModalities: ["text"],
				outputModalities: ["text"],
				variantEfforts: [],
			},
		},
	];
}

function baseInput(extra = {}) {
	return {
		baseUrl: "http://127.0.0.1:8080",
		models: opencodeModels(),
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

describe("extractReasoningEfforts", () => {
	it("finds reasoning-effort option values by parameter id", () => {
		expect(
			extractReasoningEfforts([
				{ id: "temperature" },
				{ id: "reasoning_effort", options: [{ value: "none" }, { value: "low" }, { value: "xhigh" }] },
			]),
		).toEqual(["none", "low", "xhigh"]);
	});

	it("returns null when no reasoning-effort parameter exists", () => {
		expect(extractReasoningEfforts([{ id: "temperature" }])).toBeNull();
		expect(extractReasoningEfforts(undefined)).toBeNull();
	});
});

describe("buildAgentConfig", () => {
	it("builds an opencode v2 provider block with capabilities/limit/per-1M cost/variants and {env:} auth only with a VK", () => {
		const withVk = buildAgentConfig("opencode", baseInput({ virtualKeyName: "evaluate-test-route" }));
		expect(withVk.config).toContain("{env:BIFROST_API_KEY}");
		expect(withVk.config).toContain('"baseURL": "http://127.0.0.1:8080/v1"');
		expect(withVk.config).toContain('"package": "@opencode/ai/providers/openai-compatible"');
		expect(withVk.config).toContain("opencode-zen/glm-5.3");
		expect(withVk.config).toContain("test-route");
		expect(withVk.config).toContain("evaluate-test-route");
		expect(withVk.config).toContain('"context": 200000');
		expect(withVk.config).toContain('"output": 32000');
		expect(withVk.config).toContain('"tools": true');
		// Per-token 0.000002 input → per-1M 2.
		expect(withVk.config).toContain('"input": 2');
		expect(withVk.config).toContain('"reasoningEffort": "medium"');
		expect(withVk.config).not.toContain("sk-bf-");
		expect(withVk.shellExports).toContain('export BIFROST_API_KEY="<paste-your-virtual-key-value>"');

		const withoutVk = buildAgentConfig("opencode", baseInput());
		expect(withoutVk.config).toContain("opencode-zen/glm-5.3");
		expect(withoutVk.config).not.toContain("apiKey");
		expect(withoutVk.config).not.toContain('"env"');
		expect(withoutVk.config).not.toContain("<paste-your-virtual-key-value>");
		expect(withoutVk.shellExports).toBe("");
		expect(withoutVk.sections).toEqual([]);
	});

	it("omits opencode model entries missing limit context/output and the variants key when none checked", () => {
		const out = buildAgentConfig(
			"opencode",
			baseInput({
				models: [
					{
						id: "bad/model",
						metadata: { limitContext: "", limitOutput: "", tools: true, inputModalities: [], outputModalities: [], variantEfforts: [] },
					},
				],
			}),
		);
		expect(out.config).not.toContain("bad/model");
		expect(out.config).not.toContain("variants");
	});

	it("builds a codex TOML block with env_key only with a VK and the chosen default model", () => {
		const withVk = buildAgentConfig(
			"codex",
			baseInput({
				virtualKeyName: "k",
				defaultModelId: "test-route",
				codexContextWindow: "1000000",
				codexMaxOutputTokens: "128000",
				codexReasoningEffort: "high",
			}),
		);
		expect(withVk.config).toContain('model = "test-route"');
		expect(withVk.config).toContain("model_context_window = 1000000");
		expect(withVk.config).toContain("model_max_output_tokens = 128000");
		expect(withVk.config).toContain('model_reasoning_effort = "high"');
		expect(withVk.config).toContain('env_key = "BIFROST_API_KEY"');
		expect(withVk.config).toContain('wire_api = "responses"');
		expect(withVk.config).not.toContain("sk-bf-");

		const withoutVk = buildAgentConfig("codex", baseInput());
		expect(withoutVk.config).not.toContain("env_key");
		expect(withoutVk.config).not.toContain("<paste-your-virtual-key-value>");
		expect(withoutVk.shellExports).toBe("");
	});

	it("builds a secret-free claude fragment (base URL + model only) with the VK secret only in shell exports", () => {
		const withVk = buildAgentConfig(
			"claude-code",
			baseInput({ virtualKeyName: "k", envVar: "ANTHROPIC_AUTH_TOKEN", defaultModelId: "test-route" }),
		);
		expect(withVk.config).toContain('"ANTHROPIC_BASE_URL": "http://127.0.0.1:8080/anthropic"');
		expect(withVk.config).toContain('"model": "test-route"');
		expect(withVk.config).not.toContain("ANTHROPIC_AUTH_TOKEN");
		expect(withVk.config).not.toContain("<paste-your-virtual-key-value>");
		expect(withVk.shellExports).toContain('export ANTHROPIC_AUTH_TOKEN="<paste-your-virtual-key-value>"');

		const withoutVk = buildAgentConfig("claude-code", baseInput({ envVar: "ANTHROPIC_AUTH_TOKEN" }));
		expect(withoutVk.config).toContain('"ANTHROPIC_BASE_URL"');
		expect(withoutVk.shellExports).toBe("");
	});
});