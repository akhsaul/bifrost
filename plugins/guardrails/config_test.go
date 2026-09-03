package guardrails

import (
	"strings"
	"testing"
)

func validConfig() *Config {
	cfg := &Config{
		GuardrailProviders: []RegexProviderConfig{
			{
				ID:           20,
				ProviderName: "regex",
				PolicyName:   "pii",
				Enabled:      true,
			},
		},
	}
	cfg.GuardrailProviders[0].Config.Patterns = []Pattern{
		{Pattern: `abc`, Description: "literal"},
	}
	cfg.GuardrailRules = []Rule{
		{
			ID:                201,
			Name:              "rule-1",
			Enabled:           true,
			CELExpression:     "true",
			ApplyTo:           "input",
			ProviderConfigIDs: []int{20},
		},
	}
	return cfg
}

func TestValidateConfig_EmptyIsValid(t *testing.T) {
	if err := validateConfig(&Config{}); err != nil {
		t.Fatalf("empty config should be valid, got: %v", err)
	}
}

func TestValidateConfig_ValidConfig(t *testing.T) {
	if err := validateConfig(validConfig()); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}
}

func TestValidateConfig_MissingApplyTo(t *testing.T) {
	cfg := validConfig()
	cfg.GuardrailRules[0].ApplyTo = ""
	if err := validateConfig(cfg); err == nil {
		t.Fatal("rule without apply_to should be invalid")
	}
}

func TestValidateConfig_BadApplyTo(t *testing.T) {
	cfg := validConfig()
	cfg.GuardrailRules[0].ApplyTo = "sideways"
	if err := validateConfig(cfg); err == nil {
		t.Fatal("invalid apply_to should be rejected")
	}
}

func TestValidateConfig_UnknownProviderID(t *testing.T) {
	cfg := validConfig()
	cfg.GuardrailRules[0].ProviderConfigIDs = []int{99}
	if err := validateConfig(cfg); err == nil {
		t.Fatal("rule referencing unknown provider id should be rejected")
	}
}

func TestValidateConfig_UnsupportedProviderName(t *testing.T) {
	cfg := validConfig()
	cfg.GuardrailProviders[0].ProviderName = "bedrock"
	err := validateConfig(cfg)
	if err == nil {
		t.Fatal("non-regex provider should be rejected")
	}
	if !strings.Contains(err.Error(), `"regex"`) {
		t.Fatalf("error should mention only regex is supported, got: %v", err)
	}
}
