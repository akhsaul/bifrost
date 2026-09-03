package guardrails

import (
	"fmt"
	"sync"

	"github.com/google/cel-go/cel"
	"github.com/maximhq/bifrost/core/schemas"
)

// celProgram wraps a compiled, ready-to-eval CEL program.
type celProgram struct {
	prg cel.Program
}

// celEnvOnce guards the one-time construction of the shared CEL environment.
var (
	celEnvOnce sync.Once
	celEnv     *cel.Env
	celEnvErr  error
)

// getCELEnv returns the shared CEL environment declaring every variable a
// guardrail rule may reference. All variables are always populated at
// evaluation time (empty string when absent) so evaluation cannot fail with
// "no such attribute".
func getCELEnv() (*cel.Env, error) {
	celEnvOnce.Do(func() {
		celEnv, celEnvErr = cel.NewEnv(
			cel.Variable("model", cel.StringType),
			cel.Variable("provider", cel.StringType),
			cel.Variable("headers", cel.MapType(cel.StringType, cel.StringType)),
			cel.Variable("query", cel.MapType(cel.StringType, cel.StringType)),
			cel.Variable("virtual_key_id", cel.StringType),
			cel.Variable("virtual_key_name", cel.StringType),
		)
	})
	return celEnv, celEnvErr
}

// compileCEL parses, type-checks and compiles one expression.
func compileCEL(expr string) (celProgram, error) {
	if expr == "" {
		expr = "true"
	}
	env, err := getCELEnv()
	if err != nil {
		return celProgram{}, fmt.Errorf("failed to initialize CEL environment: %w", err)
	}
	ast, issues := env.Compile(expr)
	if issues != nil && issues.Err() != nil {
		return celProgram{}, fmt.Errorf("CEL compile error: %s", issues.Err().Error())
	}
	prg, err := env.Program(ast)
	if err != nil {
		return celProgram{}, fmt.Errorf("failed to assemble CEL program: %w", err)
	}
	return celProgram{prg: prg}, nil
}

// validateConfigCEL compiles every enabled rule's CEL expression so malformed
// expressions are rejected at config load / plugin update time.
func validateConfigCEL(cfg *Config) error {
	for i := range cfg.GuardrailRules {
		r := &cfg.GuardrailRules[i]
		if !r.Enabled {
			continue
		}
		if _, err := compileCEL(r.CELExpression); err != nil {
			return fmt.Errorf("guardrail_rules[%d] (id %d): %w", i, r.ID, err)
		}
	}
	return nil
}

// evaluateCEL runs a compiled program over the given variables. Returns an
// error when the result is not a boolean.
func evaluateCEL(p celProgram, vars map[string]any) (bool, error) {
	out, _, err := p.prg.Eval(vars)
	if err != nil {
		return false, err
	}
	matched, ok := out.Value().(bool)
	if !ok {
		return false, fmt.Errorf("CEL expression did not return boolean, got: %T", out.Value())
	}
	return matched, nil
}

// ruleMatches reports whether the rule's CEL gate passes for this request.
// Fails closed (false) on any evaluation error, logging a warning.
func (p *Plugin) ruleMatches(rule *Rule, ctx *schemas.BifrostContext, req *schemas.BifrostRequest) bool {
	prog, ok := p.config.programs[rule.ID]
	if !ok {
		if p.logger != nil {
			p.logger.Warn("guardrails: no compiled CEL program for rule %d, skipping", rule.ID)
		}
		return false
	}
	vars := buildCELVars(ctx, req)
	matched, err := evaluateCEL(prog, vars)
	if err != nil {
		if p.logger != nil {
			p.logger.Warn("guardrails: CEL evaluation failed for rule %d: %v", rule.ID, err)
		}
		return false
	}
	return matched
}

// buildCELVars gathers the CEL variables from the request context. Every
// declared variable is always set (empty values when absent) so expressions
// referencing them evaluate instead of erroring.
func buildCELVars(ctx *schemas.BifrostContext, req *schemas.BifrostRequest) map[string]any {
	vars := map[string]any{
		"model":            "",
		"provider":         "",
		"headers":          map[string]string{},
		"query":            map[string]string{},
		"virtual_key_id":   "",
		"virtual_key_name": "",
	}
	if req != nil && req.ChatRequest != nil {
		vars["model"] = req.ChatRequest.Model
		vars["provider"] = string(req.ChatRequest.Provider)
	}
	if ctx != nil {
		if h, ok := ctx.Value(schemas.BifrostContextKeyRequestHeaders).(map[string]string); ok && h != nil {
			vars["headers"] = h
		}
		if q, ok := ctx.Value(schemas.BifrostContextKeyRequestQuery).(map[string]string); ok && q != nil {
			vars["query"] = q
		}
		if vk, ok := ctx.Value(schemas.BifrostContextKeyVirtualKey).(string); ok {
			vars["virtual_key_id"] = vk
		}
		if vkn, ok := ctx.Value(schemas.BifrostContextKeyGovernanceVirtualKeyName).(string); ok {
			vars["virtual_key_name"] = vkn
		}
	}
	return vars
}
