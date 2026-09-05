package guardrails

import (
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

// TestPostLLMHook_ModelConditionOnResponse reproduces the integration gap where
// PostLLMHook passed req=nil to the CEL evaluator, so rules gating on
// model/provider never matched for output-side scans even though the response
// carries the model that actually served it.
func TestPostLLMHook_ModelConditionOnResponse(t *testing.T) {
	cfg := &Config{
		GuardrailProviders: []RegexProviderConfig{{
			ID: 20, ProviderName: "regex", PolicyName: "pii", Enabled: true,
		}},
		GuardrailRules: []Rule{{
			ID: 201, Name: "gpt-only", Enabled: true, ApplyTo: ApplyToOutput,
			CELExpression: `model == "gpt-4o"`, ProviderConfigIDs: []int{20},
		}},
	}
	cfg.GuardrailProviders[0].Config.Patterns = []Pattern{
		{Pattern: "a@b.com", Description: "Email address", Action: PatternActionBlock},
	}
	p, err := Init(cfg, nil)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}

	resp := chatResp("your email a@b.com was noted")
	resp.ChatResponse.Model = "gpt-4o"

	_, bifrostErr, err := p.PostLLMHook(celCtx(nil), resp, nil)
	if err != nil {
		t.Fatalf("unexpected hook error: %v", err)
	}
	if bifrostErr == nil {
		t.Fatal("rule gated on model == \"gpt-4o\" must match when the response model is gpt-4o")
	}
}

// TestPostLLMHook_ProviderConditionOnResponse mirrors the model test for the
// provider variable, sourced from the response ExtraFields.
func TestPostLLMHook_ProviderConditionOnResponse(t *testing.T) {
	cfg := &Config{
		GuardrailProviders: []RegexProviderConfig{{
			ID: 20, ProviderName: "regex", PolicyName: "pii", Enabled: true,
		}},
		GuardrailRules: []Rule{{
			ID: 201, Name: "anthropic-only", Enabled: true, ApplyTo: ApplyToOutput,
			CELExpression: `provider == "anthropic"`, ProviderConfigIDs: []int{20},
		}},
	}
	cfg.GuardrailProviders[0].Config.Patterns = []Pattern{
		{Pattern: "a@b.com", Description: "Email address", Action: PatternActionBlock},
	}
	p, err := Init(cfg, nil)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}

	resp := chatResp("your email a@b.com was noted")
	resp.ChatResponse.ExtraFields.Provider = schemas.Anthropic

	_, bifrostErr, err := p.PostLLMHook(celCtx(nil), resp, nil)
	if err != nil {
		t.Fatalf("unexpected hook error: %v", err)
	}
	if bifrostErr == nil {
		t.Fatal("rule gated on provider == \"anthropic\" must match when the response provider is anthropic")
	}
}

// TestBuildCELVars_VirtualKeyIDPrefersGovernanceID pins the variable source
// order: the governance-published virtual key ID is the canonical id; the raw
// x-bf-vk header value is only a fallback for non-governance deployments.
func TestBuildCELVars_VirtualKeyIDPrefersGovernanceID(t *testing.T) {
	ctx := celCtx(nil)
	ctx.SetValue(schemas.BifrostContextKeyGovernanceVirtualKeyID, "vk-123")
	ctx.SetValue(schemas.BifrostContextKeyVirtualKey, "raw-header-value")

	vars := buildCELVars(ctx, nil, nil)
	if vars["virtual_key_id"] != "vk-123" {
		t.Fatalf("virtual_key_id must prefer the governance-published ID, got %v", vars["virtual_key_id"])
	}
}

// TestBuildCELVars_VirtualKeyIDFallsBackToHeader covers deployments without
// governance (e.g. plain OSS without the governance plugin): the x-bf-vk
// header value is used verbatim.
func TestBuildCELVars_VirtualKeyIDFallsBackToHeader(t *testing.T) {
	ctx := celCtx(nil)
	ctx.SetValue(schemas.BifrostContextKeyVirtualKey, "sk-bf-header")

	vars := buildCELVars(ctx, nil, nil)
	if vars["virtual_key_id"] != "sk-bf-header" {
		t.Fatalf("expected header fallback, got %v", vars["virtual_key_id"])
	}
}

// TestBuildCELVars_VirtualKeyNameFromGovernance pins the name source.
func TestBuildCELVars_VirtualKeyNameFromGovernance(t *testing.T) {
	ctx := celCtx(nil)
	ctx.SetValue(schemas.BifrostContextKeyGovernanceVirtualKeyName, "prod-vk")

	vars := buildCELVars(ctx, nil, nil)
	if vars["virtual_key_name"] != "prod-vk" {
		t.Fatalf("got %v", vars["virtual_key_name"])
	}
}
