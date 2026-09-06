// TypeScript types for the OSS regex guardrails plugin, matching
// plugins/guardrails/plugin.go.

export type PatternAction = "detect_only" | "block" | "redact";
export type RedactionStrategy = "replace" | "mask" | "hash";
export type RedactionMode = "runtime" | "runtime_reversible" | "logs_only";
export type RuleApplyTo = "input" | "output" | "both";
export type RuleTarget = "llm" | "mcp";
export type GuardrailProviderType = "regex" | "secrets" | "prompt-guardrail" | "prompt_guardrail";

export interface GuardrailPattern {
	pattern: string;
	description?: string;
	entity_type?: string;
	flags?: string; // subset of "ims"
	action?: PatternAction;
	redaction_strategy?: RedactionStrategy;
	redaction_mode?: RedactionMode;
}

export interface SecretsProviderConfig {
	ignored_secret_keywords?: string[];
	action?: PatternAction;
	redaction_strategy?: RedactionStrategy;
	redaction_mode?: RedactionMode;
}

export interface PromptGuardrailProviderConfig {
	judge_provider: string;
	judge_model: string;
	rule: string;
	prompt_template?: string;
	timeout?: number;
	max_output_tokens?: number;
}

export interface GuardrailProvider {
	id: number;
	provider_name: GuardrailProviderType;
	policy_name: string;
	enabled: boolean;
	timeout?: number;
	config: {
		patterns?: GuardrailPattern[];
		ignored_secret_keywords?: string[];
		action?: PatternAction;
		redaction_strategy?: RedactionStrategy;
		redaction_mode?: RedactionMode;
		judge_provider?: string;
		judge_model?: string;
		rule?: string;
		prompt_template?: string;
		max_output_tokens?: number;
	};
}

export interface GuardrailRule {
	id: number;
	name: string;
	description?: string;
	enabled: boolean;
	target?: RuleTarget; // default: "llm"
	cel_expression?: string;
	apply_to: RuleApplyTo;
	sampling_rate?: number;
	timeout?: number;
	max_turns_to_send?: number;
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