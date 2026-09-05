package guardrails

import (
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
)

func celCtx(headers map[string]string) *schemas.BifrostContext {
	ctx := schemas.NewBifrostContext(nil, time.Now().Add(time.Minute))
	if headers != nil {
		ctx.SetValue(schemas.BifrostContextKeyRequestHeaders, headers)
	}
	return ctx
}

func TestRuleMatches_TrueExpression(t *testing.T) {
	p := &Plugin{config: compiledConfig{programs: map[int]celProgram{201: mustCompileCEL(t, "true")}}}
	rule := &Rule{ID: 201, CELExpression: "true"}
	if !p.ruleMatches(rule, celCtx(nil), chatReq("hi"), nil) {
		t.Fatal("true expression should match")
	}
}

func TestRuleMatches_FalseExpression(t *testing.T) {
	p := &Plugin{config: compiledConfig{programs: map[int]celProgram{201: mustCompileCEL(t, "false")}}}
	rule := &Rule{ID: 201, CELExpression: "false"}
	if p.ruleMatches(rule, celCtx(nil), chatReq("hi"), nil) {
		t.Fatal("false expression should not match")
	}
}

func TestRuleMatches_HeaderCondition(t *testing.T) {
	p := &Plugin{config: compiledConfig{programs: map[int]celProgram{
		201: mustCompileCEL(t, `headers["x-bf-tenant"] == "external"`),
	}}}
	rule := &Rule{ID: 201, CELExpression: `headers["x-bf-tenant"] == "external"`}

	if !p.ruleMatches(rule, celCtx(map[string]string{"x-bf-tenant": "external"}), chatReq("hi"), nil) {
		t.Fatal("matching header should pass")
	}
	if p.ruleMatches(rule, celCtx(map[string]string{"x-bf-tenant": "internal"}), chatReq("hi"), nil) {
		t.Fatal("non-matching header should fail")
	}
	if p.ruleMatches(rule, celCtx(nil), chatReq("hi"), nil) {
		t.Fatal("missing headers map should fail the condition, not error")
	}
}

func TestRuleMatches_ModelCondition(t *testing.T) {
	p := &Plugin{config: compiledConfig{programs: map[int]celProgram{
		201: mustCompileCEL(t, `model == "gpt-4o"`),
	}}}
	rule := &Rule{ID: 201, CELExpression: `model == "gpt-4o"`}
	if !p.ruleMatches(rule, celCtx(nil), chatReq("hi"), nil) {
		t.Fatal("gpt-4o request should match")
	}
	other := chatReq("hi")
	other.ChatRequest.Model = "claude-3"
	if p.ruleMatches(rule, celCtx(nil), other, nil) {
		t.Fatal("other model should not match")
	}
}

func TestRuleMatches_MissingProgramFailsClosed(t *testing.T) {
	p := &Plugin{config: compiledConfig{programs: map[int]celProgram{}}}
	rule := &Rule{ID: 999, CELExpression: "true"}
	if p.ruleMatches(rule, celCtx(nil), chatReq("hi"), nil) {
		t.Fatal("missing compiled program should fail closed")
	}
}

func TestValidateConfigCEL_BadExpression(t *testing.T) {
	cfg := validConfig()
	cfg.GuardrailRules[0].CELExpression = `model == ` // syntax error
	if err := validateConfigCEL(cfg); err == nil {
		t.Fatal("invalid CEL expression should be rejected")
	}
}

func TestValidateConfigCEL_UnknownVariable(t *testing.T) {
	cfg := validConfig()
	cfg.GuardrailRules[0].CELExpression = `foo == "bar"` // foo is not declared
	if err := validateConfigCEL(cfg); err == nil {
		t.Fatal("undeclared variable should be rejected")
	}
}

func TestValidateConfigCEL_EmptyExpressionIsValid(t *testing.T) {
	cfg := validConfig()
	cfg.GuardrailRules[0].CELExpression = ""
	if err := validateConfigCEL(cfg); err != nil {
		t.Fatalf("empty CEL should be valid, got %v", err)
	}
}

func TestValidateConfigCEL_TypeMismatchRejected(t *testing.T) {
	cfg := validConfig()
	cfg.GuardrailRules[0].CELExpression = `headers + 1` // map + int is a type error
	if err := validateConfigCEL(cfg); err == nil {
		t.Fatal("type-mismatched expression should be rejected")
	}
}
