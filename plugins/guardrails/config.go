package guardrails

import (
	"fmt"
	"regexp"
	"strings"
)

// applyTo values for a rule.
const (
	ApplyToInput  = "input"
	ApplyToOutput = "output"
	ApplyToBoth   = "both"
)

// supportedProviderName is the only guardrail provider type implemented in OSS.
const supportedProviderName = "regex"

// validFlags is the set of allowed RE2 inline flag characters.
const validFlags = "ims"

// compilePattern compiles one pattern with its inline flags prefix.
// Returns nil when the pattern is empty.
func compilePattern(p *Pattern) (*regexp.Regexp, error) {
	if strings.TrimSpace(p.Pattern) == "" {
		return nil, fmt.Errorf("pattern is required")
	}
	if err := validateFlags(p.Flags); err != nil {
		return nil, err
	}
	expr := p.Pattern
	if p.Flags != "" {
		expr = "(?" + p.Flags + ")" + expr
	}
	re, err := regexp.Compile(expr)
	if err != nil {
		return nil, fmt.Errorf("invalid pattern %q: %w", p.Pattern, err)
	}
	return re, nil
}

func validateFlags(flags string) error {
	seen := map[rune]bool{}
	for _, r := range flags {
		if !strings.ContainsRune(validFlags, r) {
			return fmt.Errorf("unsupported regex flag %q (allowed: %q)", r, validFlags)
		}
		if seen[r] {
			return fmt.Errorf("duplicate regex flag %q", r)
		}
		seen[r] = true
	}
	return nil
}

// validateConfig validates cfg and applies defaults in place:
// action "" -> block, redaction_strategy "" -> replace, sampling_rate 0 -> 100.
// CEL expressions are validated separately (see validateConfigCEL).
func validateConfig(cfg *Config) error {
	if cfg == nil {
		return fmt.Errorf("config is required")
	}

	providerIDs := make(map[int]bool, len(cfg.GuardrailProviders))
	for i := range cfg.GuardrailProviders {
		p := &cfg.GuardrailProviders[i]
		if p.ID == 0 {
			return fmt.Errorf("guardrail_providers[%d]: id is required", i)
		}
		if providerIDs[p.ID] {
			return fmt.Errorf("guardrail_providers[%d]: duplicate id %d", i, p.ID)
		}
		providerIDs[p.ID] = true
		if p.ProviderName != supportedProviderName {
			return fmt.Errorf("guardrail_providers[%d] (id %d): only provider_name %q is supported in OSS guardrails, got %q",
				i, p.ID, supportedProviderName, p.ProviderName)
		}
		if strings.TrimSpace(p.PolicyName) == "" {
			return fmt.Errorf("guardrail_providers[%d] (id %d): policy_name is required", i, p.ID)
		}
		if !p.Enabled {
			// Disabled providers never compile patterns at runtime; skip strict checks
			// so half-edited configs do not block startup.
			continue
		}
		for j := range p.Config.Patterns {
			pat := &p.Config.Patterns[j]
			if pat.Action == "" {
				pat.Action = PatternActionBlock
			}
			switch pat.Action {
			case PatternActionDetectOnly, PatternActionBlock, PatternActionRedact:
			default:
				return fmt.Errorf("guardrail_providers[%d] pattern[%d]: unknown action %q (allowed: detect_only, block, redact)", i, j, pat.Action)
			}
			if pat.RedactionStrategy == "" {
				pat.RedactionStrategy = RedactionReplace
			}
			switch pat.RedactionStrategy {
			case RedactionReplace, RedactionMask, RedactionHash:
			default:
				return fmt.Errorf("guardrail_providers[%d] pattern[%d]: unknown redaction_strategy %q (allowed: replace, mask, hash)", i, j, pat.RedactionStrategy)
			}
			if _, err := compilePattern(pat); err != nil {
				return fmt.Errorf("guardrail_providers[%d] pattern[%d]: %w", i, j, err)
			}
		}
	}

	ruleIDs := make(map[int]bool, len(cfg.GuardrailRules))
	for i := range cfg.GuardrailRules {
		r := &cfg.GuardrailRules[i]
		if r.ID == 0 {
			return fmt.Errorf("guardrail_rules[%d]: id is required", i)
		}
		if ruleIDs[r.ID] {
			return fmt.Errorf("guardrail_rules[%d]: duplicate id %d", i, r.ID)
		}
		ruleIDs[r.ID] = true
		if strings.TrimSpace(r.Name) == "" {
			return fmt.Errorf("guardrail_rules[%d] (id %d): name is required", i, r.ID)
		}
		if r.Target == "" {
			r.Target = "llm"
		}
		if r.Target != "llm" && r.Target != "mcp" {
			return fmt.Errorf("guardrail_rules[%d] (id %d): target must be 'llm' or 'mcp', got %q", i, r.ID, r.Target)
		}
		switch r.ApplyTo {
		case ApplyToInput, ApplyToOutput, ApplyToBoth:
		default:
			return fmt.Errorf("guardrail_rules[%d] (id %d): apply_to must be one of input, output, both, got %q", i, r.ID, r.ApplyTo)
		}
		if r.SamplingRate < 0 || r.SamplingRate > 100 {
			return fmt.Errorf("guardrail_rules[%d] (id %d): sampling_rate must be 0-100, got %d", i, r.ID, r.SamplingRate)
		}
		for _, pid := range r.ProviderConfigIDs {
			if !providerIDs[pid] {
				return fmt.Errorf("guardrail_rules[%d] (id %d): provider_config_ids references unknown provider id %d", i, r.ID, pid)
			}
		}
		if r.SamplingRate == 0 {
			r.SamplingRate = 100
		}
	}
	return nil
}
