package guardrails

import (
	"fmt"
	"math/rand"

	"github.com/maximhq/bifrost/core/schemas"
)

// Init compiles cfg into a ready-to-run Plugin.
func Init(cfg *Config, logger schemas.Logger) (*Plugin, error) {
	if cfg == nil {
		return nil, fmt.Errorf("guardrails: config is required")
	}
	if err := validateConfig(cfg); err != nil {
		return nil, fmt.Errorf("guardrails: %w", err)
	}
	if err := validateConfigCEL(cfg); err != nil {
		return nil, fmt.Errorf("guardrails: %w", err)
	}

	compiled := compiledConfig{
		cfg:      *cfg,
		patterns: make(map[int][]compiledPattern, len(cfg.GuardrailProviders)),
		programs: make(map[int]celProgram, len(cfg.GuardrailRules)),
	}
	for i := range cfg.GuardrailProviders {
		p := &cfg.GuardrailProviders[i]
		if !p.Enabled {
			continue
		}
		for j := range p.Config.Patterns {
			re, err := compilePattern(&p.Config.Patterns[j])
			if err != nil {
				return nil, fmt.Errorf("guardrails: provider %d pattern %d: %w", p.ID, j, err)
			}
			compiled.patterns[p.ID] = append(compiled.patterns[p.ID], compiledPattern{re: re, cfg: p.Config.Patterns[j]})
		}
	}
	for i := range cfg.GuardrailRules {
		r := &cfg.GuardrailRules[i]
		if !r.Enabled {
			continue
		}
		prg, err := compileCEL(r.CELExpression)
		if err != nil {
			return nil, fmt.Errorf("guardrails: rule %d: %w", r.ID, err)
		}
		compiled.programs[r.ID] = prg
	}

	return &Plugin{name: PluginName, config: compiled, logger: logger}, nil
}

// logWarn delegates to logger.Warn if a logger was configured.
func (p *Plugin) logWarn(msg string, args ...any) {
	if p.logger != nil {
		p.logger.Warn(msg, args...)
	}
}

// randSource is the sampling function; injectable for deterministic tests.
var randSource = rand.Intn

// PreRequestHook implements schemas.LLMPlugin (no-op: guardrails evaluate in
// the per-attempt hooks).
func (p *Plugin) PreRequestHook(_ *schemas.BifrostContext, _ *schemas.BifrostRequest) error {
	return nil
}

// PreLLMHook scans the request input for rules with apply_to input|both.
// block short-circuits the request; redact mutates the request in place;
// detect_only logs and passes through.
func (p *Plugin) PreLLMHook(ctx *schemas.BifrostContext, req *schemas.BifrostRequest) (*schemas.BifrostRequest, *schemas.LLMPluginShortCircuit, error) {
	if req == nil || req.ChatRequest == nil {
		return req, nil, nil
	}
	for i := range p.config.cfg.GuardrailRules {
		rule := &p.config.cfg.GuardrailRules[i]
		if !rule.Enabled || (rule.ApplyTo != ApplyToInput && rule.ApplyTo != ApplyToBoth) {
			continue
		}
		if !p.sampled(rule) || !p.ruleMatches(rule, ctx, req, nil) {
			continue
		}
		if shortCircuit := p.applyRulesToRequest(rule, req); shortCircuit != nil {
			return req, shortCircuit, nil
		}
	}
	return req, nil, nil
}

// PostLLMHook scans the response output for rules with apply_to output|both.
// block invalidates the response (returns an error instead); redact rewrites
// the response content in place; detect_only logs. An incoming provider error
// is passed through untouched.
func (p *Plugin) PostLLMHook(ctx *schemas.BifrostContext, resp *schemas.BifrostResponse, bifrostErr *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError, error) {
	if bifrostErr != nil || resp == nil || resp.ChatResponse == nil {
		return resp, bifrostErr, nil
	}
	for i := range p.config.cfg.GuardrailRules {
		rule := &p.config.cfg.GuardrailRules[i]
		if !rule.Enabled || (rule.ApplyTo != ApplyToOutput && rule.ApplyTo != ApplyToBoth) {
			continue
		}
		if !p.sampled(rule) || !p.ruleMatches(rule, ctx, nil, resp) {
			continue
		}
		if shortCircuit := p.applyRulesToResponse(rule, resp); shortCircuit != nil {
			return nil, shortCircuit, nil
		}
	}
	return resp, bifrostErr, nil
}

// sampled reports whether this request is selected by the rule's sampling rate.
func (p *Plugin) sampled(rule *Rule) bool {
	if rule.SamplingRate >= 100 || rule.SamplingRate <= 0 {
		return true
	}
	return randSource(100) < rule.SamplingRate
}

// applyRulesToRequest evaluates every enabled provider of the rule against the
// request text. Block stops on the first hit; redact rewrites messages in place.
func (p *Plugin) applyRulesToRequest(rule *Rule, req *schemas.BifrostRequest) *schemas.LLMPluginShortCircuit {
	for _, pid := range rule.ProviderConfigIDs {
		patterns := p.config.patterns[pid]
		for i := range req.ChatRequest.Input {
			msg := &req.ChatRequest.Input[i]
			if msg.Content == nil {
				continue
			}
			if msg.Content.ContentStr != nil {
				blocked, newText, detected := evaluatePatterns(patterns, *msg.Content.ContentStr)
				if len(detected) > 0 {
					p.logWarn("guardrails: rule %q detected %d match(es) on input: %v", rule.Name, len(detected), detected)
				}
				if len(blocked) > 0 {
					return blockShortCircuit(rule, blocked)
				}
				if newText != *msg.Content.ContentStr {
					msg.Content.ContentStr = ptr(newText)
				}
			}
			for j := range msg.Content.ContentBlocks {
				blk := &msg.Content.ContentBlocks[j]
				if blk.Text != nil {
					blocked, newText, detected := evaluatePatterns(patterns, *blk.Text)
					if len(detected) > 0 {
						p.logWarn("guardrails: rule %q detected %d match(es) on input block: %v", rule.Name, len(detected), detected)
					}
					if len(blocked) > 0 {
						return blockShortCircuit(rule, blocked)
					}
					if newText != *blk.Text {
						blk.Text = ptr(newText)
					}
				}
			}
		}
	}
	return nil
}

// applyRulesToResponse evaluates every enabled provider of the rule against the response text.
func (p *Plugin) applyRulesToResponse(rule *Rule, resp *schemas.BifrostResponse) *schemas.BifrostError {
	for _, pid := range rule.ProviderConfigIDs {
		patterns := p.config.patterns[pid]
		for i := range resp.ChatResponse.Choices {
			c := &resp.ChatResponse.Choices[i]
			if c.ChatNonStreamResponseChoice != nil && c.ChatNonStreamResponseChoice.Message != nil {
				msg := c.ChatNonStreamResponseChoice.Message
				if msg.Content != nil && msg.Content.ContentStr != nil {
					blocked, newText, detected := evaluatePatterns(patterns, *msg.Content.ContentStr)
					if len(detected) > 0 {
						p.logWarn("guardrails: rule %q detected %d match(es) on output: %v", rule.Name, len(detected), detected)
					}
					if len(blocked) > 0 {
						return blockError(rule, blocked)
					}
					if newText != *msg.Content.ContentStr {
						msg.Content.ContentStr = ptr(newText)
					}
				}
			}
			if c.TextCompletionResponseChoice != nil && c.TextCompletionResponseChoice.Text != nil {
				txt := *c.TextCompletionResponseChoice.Text
				blocked, newText, detected := evaluatePatterns(patterns, txt)
				if len(detected) > 0 {
					p.logWarn("guardrails: rule %q detected %d match(es) on output text: %v", rule.Name, len(detected), detected)
				}
				if len(blocked) > 0 {
					return blockError(rule, blocked)
				}
				if newText != txt {
					c.TextCompletionResponseChoice.Text = ptr(newText)
				}
			}
		}
	}
	return nil
}

// blockShortCircuit builds the PreLLMHook short-circuit for a blocked request.
func blockShortCircuit(rule *Rule, blocked []compiledPattern) *schemas.LLMPluginShortCircuit {
	return &schemas.LLMPluginShortCircuit{Error: blockError(rule, blocked)}
}

// blockError builds the guardrail violation error shared by pre and post hooks.
func blockError(rule *Rule, blocked []compiledPattern) *schemas.BifrostError {
	names := make([]string, 0, len(blocked))
	for _, cp := range blocked {
		names = append(names, cp.label())
	}
	msg := fmt.Sprintf("request blocked by guardrail rule %q: matched %s", rule.Name, names[0])
	if len(names) > 1 {
		msg = fmt.Sprintf("request blocked by guardrail rule %q: matched %d patterns (%s, ...)", rule.Name, len(names), names[0])
	}
	return &schemas.BifrostError{
		IsBifrostError: true,
		StatusCode:     schemas.Ptr(400),
		Error: &schemas.ErrorField{
			Message: msg,
		},
		AllowFallbacks: schemas.Ptr(false),
	}
}
