// Package guardrails provides an OSS regex-based guardrails plugin for Bifrost.
//
// It evaluates guardrail rules (CEL-gated) whose guardrail providers are
// in-process regex patterns (Go RE2). Rules can detect, block, or redact
// matching text on request input and response output for chat requests.
//
// Configuration follows the enterprise guardrails_config schema shape
// (guardrail_providers + guardrail_rules + provider_config_ids) so configs
// are portable; only provider_name "regex" is supported in OSS.
package guardrails

import "github.com/maximhq/bifrost/core/schemas"

// PluginName is the canonical built-in plugin name.
const PluginName = "guardrails"

// PatternAction is the action applied when a pattern matches.
type PatternAction string

const (
	PatternActionDetectOnly PatternAction = "detect_only"
	PatternActionBlock      PatternAction = "block"
	PatternActionRedact     PatternAction = "redact"
)

// RedactionStrategy is how redact actions rewrite matched text.
type RedactionStrategy string

const (
	RedactionReplace RedactionStrategy = "replace"
	RedactionMask    RedactionStrategy = "mask"
	RedactionHash    RedactionStrategy = "hash"
)

// RedactionMode controls whether redaction is permanent or reversible.
type RedactionMode string

const (
	RedactionModeRuntime           RedactionMode = "runtime"
	RedactionModeRuntimeReversible RedactionMode = "runtime_reversible"
	RedactionModeLogsOnly          RedactionMode = "logs_only"
)

// ProviderType names the guardrail provider implementation.
type ProviderType string

const (
	ProviderTypeRegex           ProviderType = "regex"
	ProviderTypeSecrets         ProviderType = "secrets"
	ProviderTypePromptGuardrail ProviderType = "prompt-guardrail"
	ProviderTypePromptGuardrailAlt ProviderType = "prompt_guardrail"
)

// Pattern is one RE2 regex rule with the action applied on match.
type Pattern struct {
	Pattern           string            `json:"pattern"`
	Description       string            `json:"description,omitempty"`
	EntityType        string            `json:"entity_type,omitempty"`
	Flags             string            `json:"flags,omitempty"` // subset of "ims"
	Action            PatternAction     `json:"action,omitempty"` // default: block
	RedactionStrategy RedactionStrategy `json:"redaction_strategy,omitempty"` // default: replace
	RedactionMode     RedactionMode     `json:"redaction_mode,omitempty"`     // default: runtime
}

// SecretsConfig contains settings for the Betterleaks secrets detection provider.
type SecretsConfig struct {
	IgnoredSecretKeywords []string          `json:"ignored_secret_keywords,omitempty"`
	Action                PatternAction     `json:"action,omitempty"`
	RedactionStrategy     RedactionStrategy `json:"redaction_strategy,omitempty"`
	RedactionMode         RedactionMode     `json:"redaction_mode,omitempty"`
}

// PromptGuardrailConfig contains settings for the LLM-as-a-judge provider.
type PromptGuardrailConfig struct {
	JudgeProvider   string `json:"judge_provider"`
	JudgeModel      string `json:"judge_model"`
	Rule            string `json:"rule"`
	PromptTemplate  string `json:"prompt_template,omitempty"`
	Timeout         int    `json:"timeout,omitempty"` // default 30
	MaxOutputTokens int    `json:"max_output_tokens,omitempty"` // default 200
}

// ProviderConfigItem is the internal unified config object for any provider.
type ProviderConfigItem struct {
	Patterns []Pattern `json:"patterns,omitempty"`
	*SecretsConfig
	*PromptGuardrailConfig
}

// RegexProviderConfig / GuardrailProviderConfig mirrors the enterprise guardrail_providers entry.
type GuardrailProviderConfig struct {
	ID           int                `json:"id"`
	ProviderName ProviderType       `json:"provider_name"` // "regex" | "secrets" | "prompt-guardrail"
	PolicyName   string             `json:"policy_name"`
	Enabled      bool               `json:"enabled"`
	Timeout      int                `json:"timeout,omitempty"`
	Config       ProviderConfigItem `json:"config"`
}

// RegexProviderConfig is kept as an alias for backward compatibility.
type RegexProviderConfig = GuardrailProviderConfig

// Rule mirrors the enterprise guardrail_rules entry.
type Rule struct {
	ID                int    `json:"id"`
	Name              string `json:"name"`
	Description       string `json:"description,omitempty"`
	Enabled           bool   `json:"enabled"`
	Target            string `json:"target,omitempty"` // "llm" | "mcp" (default: "llm")
	CELExpression     string `json:"cel_expression,omitempty"` // empty => always true
	ApplyTo           string `json:"apply_to"`                 // input | output | both
	SamplingRate      int    `json:"sampling_rate,omitempty"`  // default 100
	Timeout           int    `json:"timeout,omitempty"`        // accepted for schema parity, ignored
	MaxTurnsToSend    int    `json:"max_turns_to_send,omitempty"`
	ProviderConfigIDs []int  `json:"provider_config_ids"`
}

// Config is the plugin configuration, shape-compatible with the enterprise
// guardrails_config schema.
type Config struct {
	GuardrailProviders []RegexProviderConfig `json:"guardrail_providers"`
	GuardrailRules     []Rule                `json:"guardrail_rules"`
}

// Plugin implements schemas.LLMPlugin (BasePlugin + PreRequestHook +
// PreLLMHook + PostLLMHook).
type Plugin struct {
	name   string
	config compiledConfig
	logger schemas.Logger
}

// GetName implements schemas.BasePlugin.
func (p *Plugin) GetName() string { return PluginName }

// Cleanup implements schemas.BasePlugin.
func (p *Plugin) Cleanup() error { return nil }
