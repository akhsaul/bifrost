import { describe, expect, it } from "vitest";
import {
	EXTRA_DETECTION_DEFAULT_COUNT_TIMEOUT_MS,
	blankExtraDetectionCountingRule,
	defaultExtraDetectionConfig,
	endpointLabel,
	extraDetectionCelPreview,
	extraDetectionCountingRuleSummary,
	extraDetectionMatchSummary,
	normalizeExtraDetectionTokenCounting,
} from "./extraDetection";

describe("extraDetection types", () => {
	it("seeds git-commit and question rules", () => {
		const cfg = defaultExtraDetectionConfig();
		expect(cfg.enabled).toBe(true);
		expect(cfg.estimate_tokens).toBe(true);
		expect(cfg.rules).toHaveLength(2);
		expect(cfg.rules[0].match.all_of).toEqual(["git", "commit"]);
		expect(cfg.rules[1].match.starts_with).toEqual(["question"]);
	});

	it("renders CEL preview", () => {
		const cfg = defaultExtraDetectionConfig();
		expect(extraDetectionCelPreview(cfg.rules[0])).toBe('headers["complexity_tier"] == "simple"');
	});

	it("summarizes single and multi-message windows", () => {
		const cfg = defaultExtraDetectionConfig();
		expect(extraDetectionMatchSummary(cfg.rules[0])).toContain("last user message");
		expect(extraDetectionMatchSummary(cfg.rules[0])).toContain("git, commit");
		expect(extraDetectionMatchSummary({ ...cfg.rules[0], max_messages_to_scan: 3 })).toContain("last 3 user messages");
	});

	it("seeds a counting block with no rules, so nothing is counted until configured", () => {
		const tc = normalizeExtraDetectionTokenCounting(defaultExtraDetectionConfig().token_counting);
		expect(tc.timeout_ms).toBe(EXTRA_DETECTION_DEFAULT_COUNT_TIMEOUT_MS);
		expect(tc.padding_ratio).toBe(0);
		expect(tc.rules).toEqual([]);
	});

	it("normalizes an absent counting block to safe defaults", () => {
		const tc = normalizeExtraDetectionTokenCounting(undefined);
		expect(tc.timeout_ms).toBe(EXTRA_DETECTION_DEFAULT_COUNT_TIMEOUT_MS);
		expect(tc.api_keys).toEqual({});
		expect(tc.base_urls).toEqual({});
		expect(tc.rules).toEqual([]);
	});

	it("preserves a configured timeout and padding", () => {
		const tc = normalizeExtraDetectionTokenCounting({ timeout_ms: 500, padding_ratio: 0.1 });
		expect(tc.timeout_ms).toBe(500);
		expect(tc.padding_ratio).toBe(0.1);
	});

	it("blanks a counting rule for create mode", () => {
		const rule = blankExtraDetectionCountingRule(3);
		expect(rule).toMatchObject({
			id: 3,
			provider: "*",
			model_pattern: "*",
			model_pattern_type: "glob",
			endpoint: "openai",
			count_model: "",
			enabled: true,
		});
	});

	it("summarizes a counting rule as match plus upstream model", () => {
		const rule = {
			...blankExtraDetectionCountingRule(1),
			name: "r",
			provider: "openrouter",
			model_pattern: "gpt-5*",
			count_model: "gpt-5",
		};
		const summary = extraDetectionCountingRuleSummary(rule);
		expect(summary).toContain("openrouter");
		expect(summary).toContain("gpt-5*");
		expect(summary).toContain('counts as "gpt-5"');
		expect(summary).toContain("OpenAI");
	});

	it("defaults an empty provider to the wildcard in the summary", () => {
		const rule = {
			...blankExtraDetectionCountingRule(1),
			name: "r",
			provider: "",
			count_model: "gemini-3-flash",
			endpoint: "gemini" as const,
		};
		expect(extraDetectionCountingRuleSummary(rule)).toContain("*");
		expect(extraDetectionCountingRuleSummary(rule)).toContain("Gemini");
	});

	it("labels both endpoints", () => {
		expect(endpointLabel("openai")).toBe("OpenAI");
		expect(endpointLabel("gemini")).toBe("Gemini");
	});
});