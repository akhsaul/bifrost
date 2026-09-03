package guardrails

import (
	"strings"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

func blockEmailPlugin(t *testing.T, action PatternAction, applyTo string) *Plugin {
	t.Helper()
	cfg := &Config{
		GuardrailProviders: []RegexProviderConfig{{
			ID: 20, ProviderName: "regex", PolicyName: "pii", Enabled: true,
		}},
		GuardrailRules: []Rule{{
			ID: 201, Name: "pii-rule", Enabled: true, ApplyTo: applyTo,
			CELExpression: "true", ProviderConfigIDs: []int{20},
		}},
	}
	cfg.GuardrailProviders[0].Config.Patterns = []Pattern{{
		Pattern:     `\b[A-Z0-9._%+-]+@[A-Z0-9.-]+\.[A-Z]{2,}\b`,
		Description: "Email address",
		EntityType:  "EMAIL",
		Flags:       "i",
		Action:      action,
	}}
	p, err := Init(cfg, nil)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	return p
}

func TestInit_AcceptsEmptyConfig(t *testing.T) {
	if _, err := Init(&Config{}, nil); err != nil {
		t.Fatalf("empty config should init fine, got %v", err)
	}
	bad := &Config{
		GuardrailProviders: []RegexProviderConfig{{ID: 1, ProviderName: "regex", PolicyName: "p", Enabled: true}},
		GuardrailRules:     []Rule{{ID: 1, Name: "r", Enabled: true, ApplyTo: "input", ProviderConfigIDs: []int{1}}},
	}
	bad.GuardrailProviders[0].Config.Patterns = []Pattern{{Pattern: `(?P<`}}
	if _, err := Init(bad, nil); err == nil {
		t.Fatal("invalid regex should fail Init")
	}
}

func TestInit_NilLoggerAndConfig(t *testing.T) {
	if _, err := Init(nil, nil); err == nil {
		t.Fatal("nil config should fail Init")
	}
}

func TestPreLLMHook_Block(t *testing.T) {
	p := blockEmailPlugin(t, PatternActionBlock, ApplyToInput)
	req := chatReq("email me at a@b.com")
	ctx := celCtx(nil)

	_, sc, err := p.PreLLMHook(ctx, req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sc == nil || sc.Error == nil {
		t.Fatal("expected short-circuit error")
	}
	if sc.Error.StatusCode == nil || *sc.Error.StatusCode != 400 {
		t.Fatalf("expected 400, got %v", sc.Error.StatusCode)
	}
	if sc.Error.AllowFallbacks == nil || *sc.Error.AllowFallbacks {
		t.Fatal("guardrail block must not allow fallbacks")
	}
	if !strings.Contains(sc.Error.Error.Message, "pii-rule") || !strings.Contains(sc.Error.Error.Message, "Email address") {
		t.Fatalf("block reason should name rule and pattern, got %q", sc.Error.Error.Message)
	}
}

func TestPreLLMHook_NoMatchPasses(t *testing.T) {
	p := blockEmailPlugin(t, PatternActionBlock, ApplyToInput)
	req := chatReq("totally clean prompt")
	_, sc, err := p.PreLLMHook(celCtx(nil), req)
	if err != nil || sc != nil {
		t.Fatalf("clean request should pass, got sc=%v err=%v", sc, err)
	}
}

func TestPreLLMHook_RedactMutatesRequest(t *testing.T) {
	p := blockEmailPlugin(t, PatternActionRedact, ApplyToInput)
	req := chatReq("email me at a@b.com")
	_, sc, err := p.PreLLMHook(celCtx(nil), req)
	if err != nil || sc != nil {
		t.Fatalf("redact should not short-circuit, got sc=%v err=%v", sc, err)
	}
	if got := *req.ChatRequest.Input[0].Content.ContentStr; got != "email me at [EMAIL]" {
		t.Fatalf("request not redacted, got %q", got)
	}
}

func TestPreLLMHook_DetectOnlyKeepsContent(t *testing.T) {
	p := blockEmailPlugin(t, PatternActionDetectOnly, ApplyToInput)
	req := chatReq("email me at a@b.com")
	_, sc, err := p.PreLLMHook(celCtx(nil), req)
	if err != nil || sc != nil {
		t.Fatalf("detect_only should not short-circuit, got sc=%v err=%v", sc, err)
	}
	if got := *req.ChatRequest.Input[0].Content.ContentStr; got != "email me at a@b.com" {
		t.Fatalf("detect_only must not mutate, got %q", got)
	}
}

func TestPreLLMHook_OutputRuleIgnoredOnInput(t *testing.T) {
	p := blockEmailPlugin(t, PatternActionBlock, ApplyToOutput)
	req := chatReq("email me at a@b.com")
	_, sc, err := p.PreLLMHook(celCtx(nil), req)
	if err != nil || sc != nil {
		t.Fatalf("output rule must not fire on input, got sc=%v err=%v", sc, err)
	}
}

func TestPreLLMHook_DisabledRuleIgnored(t *testing.T) {
	p := blockEmailPlugin(t, PatternActionBlock, ApplyToInput)
	p.config.cfg.GuardrailRules[0].Enabled = false
	req := chatReq("email me at a@b.com")
	_, sc, err := p.PreLLMHook(celCtx(nil), req)
	if err != nil || sc != nil {
		t.Fatalf("disabled rule must not fire, got sc=%v err=%v", sc, err)
	}
}

func TestPreLLMHook_CELGateBlocksRule(t *testing.T) {
	p := blockEmailPlugin(t, PatternActionBlock, ApplyToInput)
	// Rebuild the plugin with a CEL gate that never matches.
	cfg := &Config{
		GuardrailProviders: p.config.cfg.GuardrailProviders,
		GuardrailRules:     []Rule{{ID: 201, Name: "pii-rule", Enabled: true, ApplyTo: ApplyToInput, CELExpression: `headers["x-never"] == "set"`, ProviderConfigIDs: []int{20}}},
	}
	p2, err := Init(cfg, nil)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	_, sc, err := p2.PreLLMHook(celCtx(nil), chatReq("email me at a@b.com"))
	if err != nil || sc != nil {
		t.Fatal("CEL-gated-off rule must not fire")
	}
}

func TestPreLLMHook_SamplingRateFull(t *testing.T) {
	p := blockEmailPlugin(t, PatternActionBlock, ApplyToInput)
	p.config.cfg.GuardrailRules[0].SamplingRate = 100
	req := chatReq("email me at a@b.com")
	_, sc, _ := p.PreLLMHook(celCtx(nil), req)
	if sc == nil {
		t.Fatal("sampling_rate 100 must always evaluate")
	}
}

func TestPreLLMHook_SamplingRateZeroAlwaysEvaluates(t *testing.T) {
	p := blockEmailPlugin(t, PatternActionBlock, ApplyToInput)
	// validateConfig defaults 0 -> 100, so a zero rate cannot survive Init;
	// assert the compiled config honors that invariant.
	if p.config.cfg.GuardrailRules[0].SamplingRate != 100 {
		t.Fatal("sampling_rate 0 should default to 100 at Init")
	}
}

func TestPostLLMHook_BlockInvalidatesResponse(t *testing.T) {
	p := blockEmailPlugin(t, PatternActionBlock, ApplyToOutput)
	resp := chatResp("your email a@b.com was noted")
	gotResp, bifrostErr, err := p.PostLLMHook(celCtx(nil), resp, nil)
	if err != nil {
		t.Fatalf("unexpected hook error: %v", err)
	}
	if gotResp != nil {
		t.Fatal("response must be invalidated on output block")
	}
	if bifrostErr == nil || bifrostErr.StatusCode == nil || *bifrostErr.StatusCode != 400 {
		t.Fatalf("expected 400 BifrostError, got %+v", bifrostErr)
	}
	if bifrostErr.AllowFallbacks == nil || *bifrostErr.AllowFallbacks {
		t.Fatal("output block must not allow fallbacks")
	}
}

func TestPostLLMHook_Redact(t *testing.T) {
	p := blockEmailPlugin(t, PatternActionRedact, ApplyToOutput)
	resp := chatResp("your email a@b.com was noted")
	gotResp, bifrostErr, err := p.PostLLMHook(celCtx(nil), resp, nil)
	if err != nil || bifrostErr != nil {
		t.Fatalf("redact should pass, got err=%v bifrostErr=%v", err, bifrostErr)
	}
	if gotResp == nil {
		t.Fatal("response should be returned")
	}
	got := *gotResp.ChatResponse.Choices[0].ChatNonStreamResponseChoice.Message.Content.ContentStr
	if got != "your email [EMAIL] was noted" {
		t.Fatalf("got %q", got)
	}
}

func TestPostLLMHook_NilResponsePassthrough(t *testing.T) {
	p := blockEmailPlugin(t, PatternActionBlock, ApplyToOutput)
	gotResp, bifrostErr, err := p.PostLLMHook(celCtx(nil), nil, nil)
	if err != nil || gotResp != nil || bifrostErr != nil {
		t.Fatalf("nil response should pass through untouched, got %v %v %v", gotResp, bifrostErr, err)
	}
}

func TestPostLLMHook_PreservesExistingError(t *testing.T) {
	p := blockEmailPlugin(t, PatternActionBlock, ApplyToOutput)
	existing := &schemas.BifrostError{Error: &schemas.ErrorField{Message: "provider down"}}
	gotResp, bifrostErr, err := p.PostLLMHook(celCtx(nil), nil, existing)
	if err != nil || gotResp != nil || bifrostErr != existing {
		t.Fatal("existing provider error must be passed through unchanged")
	}
}

func TestPreRequestHook_Noop(t *testing.T) {
	p := blockEmailPlugin(t, PatternActionBlock, ApplyToInput)
	if err := p.PreRequestHook(celCtx(nil), chatReq("a@b.com")); err != nil {
		t.Fatalf("PreRequestHook should be a no-op, got %v", err)
	}
}
