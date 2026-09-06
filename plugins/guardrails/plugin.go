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

// Pattern is one RE2 regex rule with the action applied on match.
type Pattern struct {
	Pattern           string            `json:"pattern"`
	Description       string            `json:"description,omitempty"`
	EntityType        string            `json:"entity_type,omitempty"`
	Flags             string            `json:"flags,omitempty"` // subset of "ims"
	Action            PatternAction     `json:"action,omitempty"` // default: block
	RedactionStrategy RedactionStrategy `json:"redaction_strategy,omitempty"` // default: replace
}

// RegexProviderConfig mirrors the enterprise guardrail_providers entry for provider_name "regex".
type RegexProviderConfig struct {
	ID           int    `json:"id"`
	ProviderName string `json:"provider_name"` // only "regex" is supported
	PolicyName   string `json:"policy_name"`
	Enabled      bool   `json:"enabled"`
	Timeout      int    `json:"timeout,omitempty"` // accepted for schema parity, ignored (in-process)
	Config       struct {
		Patterns []Pattern `json:"patterns"`
	} `json:"config"`
}

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
