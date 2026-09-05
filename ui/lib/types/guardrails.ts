// TypeScript types for the OSS regex guardrails plugin, matching
// plugins/guardrails/plugin.go.

export type PatternAction = "detect_only" | "block" | "redact";
export type RedactionStrategy = "replace" | "mask" | "hash";
export type RuleApplyTo = "input" | "output" | "both";

export interface GuardrailPattern {
	pattern: string;
	description?: string;
	entity_type?: string;
	flags?: string; // subset of "ims"
	action?: PatternAction;
	redaction_strategy?: RedactionStrategy;
}

export interface GuardrailProvider {
	id: number;
	provider_name: "regex";
	policy_name: string;
	enabled: boolean;
	timeout?: number;
	config: {
		patterns: GuardrailPattern[];
	};
}

export interface GuardrailRule {
	id: number;
	name: string;
	enabled: boolean;
	cel_expression?: string;
	apply_to: RuleApplyTo;
	sampling_rate?: number;
	timeout?: number;
	provider_config_ids: number[];
}

export interface GuardrailsPluginConfig {
	guardrail_providers: GuardrailProvider[];
	guardrail_rules: GuardrailRule[];
}

export const GUARDRAILS_PLUGIN_NAME = "guardrails";

export function defaultGuardrailsConfig(): GuardrailsPluginConfig {
	return {
		guardrail_providers: [],
		guardrail_rules: [],
	};
}