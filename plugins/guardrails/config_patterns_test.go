package guardrails

import (
	"strings"
	"testing"
)

func TestValidateConfig_InvalidPatternRegex(t *testing.T) {
	cfg := validConfig()
	cfg.GuardrailProviders[0].Config.Patterns[0].Pattern = `(?P<`
	if err := validateConfig(cfg); err == nil {
		t.Fatal("invalid RE2 pattern should be rejected")
	}
}

func TestValidateConfig_LookaheadRejected(t *testing.T) {
	cfg := validConfig()
	cfg.GuardrailProviders[0].Config.Patterns[0].Pattern = `(?=.*x)abc`
	if err := validateConfig(cfg); err == nil {
		t.Fatal("lookahead pattern should be rejected (RE2 does not support it)")
	}
}

func TestValidateConfig_BadFlags(t *testing.T) {
	cfg := validConfig()
	cfg.GuardrailProviders[0].Config.Patterns[0].Flags = "x"
	if err := validateConfig(cfg); err == nil {
		t.Fatal("unsupported regex flag should be rejected")
	}
}

func TestValidateConfig_GoodFlags(t *testing.T) {
	cfg := validConfig()
	for _, flags := range []string{"", "i", "m", "s", "im", "is", "ms", "ims"} {
		cfg.GuardrailProviders[0].Config.Patterns[0].Flags = flags
		if err := validateConfig(cfg); err != nil {
			t.Fatalf("flags %q should be valid, got: %v", flags, err)
		}
	}
}

func TestValidateConfig_UnknownAction(t *testing.T) {
	cfg := validConfig()
	cfg.GuardrailProviders[0].Config.Patterns[0].Action = "explode"
	if err := validateConfig(cfg); err == nil {
		t.Fatal("unknown action should be rejected")
	}
}

func TestValidateConfig_UnknownRedactionStrategy(t *testing.T) {
	cfg := validConfig()
	p := &cfg.GuardrailProviders[0].Config.Patterns[0]
	p.Action = PatternActionRedact
	p.RedactionStrategy = "shred"
	if err := validateConfig(cfg); err == nil {
		t.Fatal("unknown redaction strategy should be rejected")
	}
}

func TestValidateConfig_DuplicateProviderID(t *testing.T) {
	cfg := validConfig()
	cfg.GuardrailProviders = append(cfg.GuardrailProviders, cfg.GuardrailProviders[0])
	if err := validateConfig(cfg); err == nil {
		t.Fatal("duplicate provider id should be rejected")
	}
}

func TestValidateConfig_DuplicateRuleID(t *testing.T) {
	cfg := validConfig()
	cfg.GuardrailRules = append(cfg.GuardrailRules, cfg.GuardrailRules[0])
	if err := validateConfig(cfg); err == nil {
		t.Fatal("duplicate rule id should be rejected")
	}
}

func TestValidateConfig_DisabledRuleStillValidated(t *testing.T) {
	// Disabled rules are skipped at runtime, but a config with an invalid
	// disabled rule is still rejected so typos surface at startup/PUT time.
	cfg := validConfig()
	cfg.GuardrailRules[0].Enabled = false
	cfg.GuardrailRules[0].ApplyTo = "everywhere"
	if err := validateConfig(cfg); err == nil {
		t.Fatal("disabled rule with invalid apply_to should still be rejected")
	}
}

func TestValidateConfig_DisabledProviderSkipsPatternCheck(t *testing.T) {
	cfg := validConfig()
	cfg.GuardrailProviders[0].Enabled = false
	cfg.GuardrailProviders[0].Config.Patterns[0].Pattern = `(?P<`
	if err := validateConfig(cfg); err != nil {
		t.Fatalf("disabled provider should skip pattern compilation, got: %v", err)
	}
}

func TestValidateConfig_RuleNameRequired(t *testing.T) {
	cfg := validConfig()
	cfg.GuardrailRules[0].Name = "  "
	if err := validateConfig(cfg); err == nil {
		t.Fatal("rule name is required")
	}
}

func TestValidateConfig_NonLLMTargetNameAllowed(t *testing.T) {
	// Names are free-form; just ensure odd characters don't break validation.
	cfg := validConfig()
	cfg.GuardrailRules[0].Name = strings.Repeat("r", 200)
	if err := validateConfig(cfg); err != nil {
		t.Fatalf("long name should be valid, got: %v", err)
	}
}
