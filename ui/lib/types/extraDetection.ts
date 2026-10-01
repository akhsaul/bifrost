// TypeScript types for the extra-detection plugin, matching
// plugins/extradetection/types.go.

export type ExtraDetectionMatchScope = "last_user_messages" | "full_input";

/** Counting endpoints, matching Go's extradetection.Endpoint enum. */
export const EXTRA_DETECTION_ENDPOINTS = ["openai", "gemini"] as const;
export type ExtraDetectionEndpoint = (typeof EXTRA_DETECTION_ENDPOINTS)[number];

export const EXTRA_DETECTION_MODEL_PATTERN_TYPES = ["glob", "regex", "exact"] as const;
export type ExtraDetectionModelPatternType = (typeof EXTRA_DETECTION_MODEL_PATTERN_TYPES)[number];

export interface ExtraDetectionCountingRule {
	id: number;
	name: string;
	description?: string;
	enabled: boolean;
	/** Provider this rule applies to, or "*" for any. */
	provider: string;
	/** Matched against the provider-prefix-stripped model. */
	model_pattern: string;
	model_pattern_type?: ExtraDetectionModelPatternType;
	endpoint: ExtraDetectionEndpoint;
	/** Model name sent to the counting endpoint, verbatim. */
	count_model: string;
}

export type ExtraDetectionApiKeys = Partial<Record<ExtraDetectionEndpoint, unknown>>;
export type ExtraDetectionBaseUrls = Partial<Record<ExtraDetectionEndpoint, string>>;

export interface ExtraDetectionTokenCounting {
	timeout_ms?: number;
	padding_ratio?: number;
	api_keys?: ExtraDetectionApiKeys;
	base_urls?: ExtraDetectionBaseUrls;
	rules?: ExtraDetectionCountingRule[];
}

export interface ExtraDetectionMatch {
	scope?: ExtraDetectionMatchScope;
	all_of?: string[];
	any_of?: string[];
	starts_with?: string[];
}

export interface ExtraDetectionRule {
	id: number;
	name: string;
	description?: string;
	enabled: boolean;
	header: string;
	value: string;
	max_messages_to_scan?: number;
	match: ExtraDetectionMatch;
}

export interface ExtraDetectionPluginConfig {
	enabled: boolean;
	estimate_tokens: boolean;
	token_header?: string;
	token_counting?: ExtraDetectionTokenCounting;
	rules: ExtraDetectionRule[];
}

export const EXTRA_DETECTION_PLUGIN_NAME = "extra-detection";

export const EXTRA_DETECTION_MAX_MESSAGES_CAP = 20;

export const EXTRA_DETECTION_DEFAULT_TOKEN_HEADER = "estimated_tokens";

export const EXTRA_DETECTION_DEFAULT_COUNT_TIMEOUT_MS = 2000;

export const EXTRA_DETECTION_PROVIDER_WILDCARD = "*";

export function defaultExtraDetectionConfig(): ExtraDetectionPluginConfig {
	return {
		enabled: true,
		estimate_tokens: true,
		token_header: EXTRA_DETECTION_DEFAULT_TOKEN_HEADER,
		token_counting: {
			timeout_ms: EXTRA_DETECTION_DEFAULT_COUNT_TIMEOUT_MS,
			padding_ratio: 0,
			api_keys: {},
			base_urls: {},
			rules: [],
		},
		rules: [
			{
				id: 1,
				name: "git commit → simple",
				description: "Commit-style requests skip embedding",
				enabled: true,
				header: "complexity_tier",
				value: "simple",
				max_messages_to_scan: 1,
				match: { scope: "last_user_messages", all_of: ["git", "commit"] },
			},
			{
				id: 2,
				name: "question → simple",
				description: "Question-prefixed requests skip embedding",
				enabled: true,
				header: "complexity_tier",
				value: "simple",
				max_messages_to_scan: 1,
				match: { scope: "last_user_messages", starts_with: ["question"] },
			},
		],
	};
}

// celPreview renders the routing-rule CEL expression that targets a rule's
// stamped header, e.g. headers["complexity_tier"] == "simple".
export function extraDetectionCelPreview(rule: ExtraDetectionRule): string {
	return `headers["${rule.header.toLowerCase()}"] == "${rule.value}"`;
}

// matchSummary renders a human-readable summary of a rule's match block,
// e.g. "last 3 user messages · contains all of: git, commit".
export function extraDetectionMatchSummary(rule: ExtraDetectionRule): string {
	const parts: string[] = [];
	if (rule.match.scope === "full_input") {
		parts.push("full input");
	} else {
		const n = rule.max_messages_to_scan && rule.max_messages_to_scan > 0 ? rule.max_messages_to_scan : 1;
		parts.push(n === 1 ? "last user message" : `last ${n} user messages`);
	}
	const bits: string[] = [];
	if (rule.match.all_of?.length) bits.push(`contains all of: ${rule.match.all_of.join(", ")}`);
	if (rule.match.any_of?.length) bits.push(`contains any of: ${rule.match.any_of.join(", ")}`);
	if (rule.match.starts_with?.length) bits.push(`starts with: ${rule.match.starts_with.join(", ")}`);
	if (bits.length) parts.push(bits.join(" · "));
	return parts.join(" · ");
}

// blankCountingRule returns a create-mode counting rule.
export function blankExtraDetectionCountingRule(id: number): ExtraDetectionCountingRule {
	return {
		id,
		name: "",
		description: "",
		enabled: true,
		provider: EXTRA_DETECTION_PROVIDER_WILDCARD,
		model_pattern: "*",
		model_pattern_type: "glob",
		endpoint: "openai",
		count_model: "",
	};
}

// countingRuleSummary renders what a counting rule matches and where it sends
// the request, e.g. 'openrouter · gemini-* · counts as "gemini-3-flash" via Gemini'.
export function extraDetectionCountingRuleSummary(rule: ExtraDetectionCountingRule): string {
	const provider = rule.provider || EXTRA_DETECTION_PROVIDER_WILDCARD;
	const type = rule.model_pattern_type || "glob";
	const pattern = `${rule.model_pattern} (${type})`;
	return `${provider} · ${pattern} · counts as "${rule.count_model}" via ${endpointLabel(rule.endpoint)}`;
}

// endpointLabel renders an endpoint id for display.
export function endpointLabel(endpoint: string): string {
	switch (endpoint) {
		case "openai":
			return "OpenAI";
		case "gemini":
			return "Gemini";
		default:
			return endpoint;
	}
}

// normalizeTokenCounting fills defaults for a possibly-absent counting block
// so the UI never renders against undefined.
export function normalizeExtraDetectionTokenCounting(
	tc: ExtraDetectionTokenCounting | undefined,
): Required<Pick<ExtraDetectionTokenCounting, "timeout_ms" | "padding_ratio">> & ExtraDetectionTokenCounting {
	return {
		timeout_ms: tc?.timeout_ms ?? EXTRA_DETECTION_DEFAULT_COUNT_TIMEOUT_MS,
		padding_ratio: tc?.padding_ratio ?? 0,
		api_keys: tc?.api_keys ?? {},
		base_urls: tc?.base_urls ?? {},
		rules: tc?.rules ?? [],
	};
}