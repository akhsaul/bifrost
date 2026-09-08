package guardrails

import (
	"fmt"
	"strings"

	"github.com/maximhq/bifrost/core/schemas"
)

// PreMCPHook inspects and redacts arguments before an MCP tool executes.
func (p *Plugin) PreMCPHook(ctx *schemas.BifrostContext, req *schemas.BifrostMCPRequest) (*schemas.BifrostMCPRequest, *schemas.MCPPluginShortCircuit, error) {
	if req == nil {
		return req, nil, nil
	}
	if req.ChatAssistantMessageToolCall != nil && req.ChatAssistantMessageToolCall.Function.Arguments != "" {
		args := req.ChatAssistantMessageToolCall.Function.Arguments
		for i := range p.config.cfg.GuardrailRules {
			rule := &p.config.cfg.GuardrailRules[i]
			if !rule.Enabled || (rule.Target != "mcp" && rule.Target != "llm" && rule.Target != "") {
				continue
			}
			if rule.ApplyTo != ApplyToInput && rule.ApplyTo != ApplyToBoth {
				continue
			}
			if !p.sampled(rule) {
				continue
			}
			errResp, newArgs := p.applyRulesToText(ctx, rule, args, schemas.RedactionPhaseInput)
			if errResp != nil {
				return req, &schemas.MCPPluginShortCircuit{Error: errResp}, nil
			}
			req.ChatAssistantMessageToolCall.Function.Arguments = newArgs
		}
	}
	if req.ResponsesToolMessage != nil && req.ResponsesToolMessage.Arguments != nil && *req.ResponsesToolMessage.Arguments != "" {
		args := *req.ResponsesToolMessage.Arguments
		for i := range p.config.cfg.GuardrailRules {
			rule := &p.config.cfg.GuardrailRules[i]
			if !rule.Enabled || (rule.Target != "mcp" && rule.Target != "llm" && rule.Target != "") {
				continue
			}
			if rule.ApplyTo != ApplyToInput && rule.ApplyTo != ApplyToBoth {
				continue
			}
			if !p.sampled(rule) {
				continue
			}
			errResp, newArgs := p.applyRulesToText(ctx, rule, args, schemas.RedactionPhaseInput)
			if errResp != nil {
				return req, &schemas.MCPPluginShortCircuit{Error: errResp}, nil
			}
			req.ResponsesToolMessage.Arguments = ptr(newArgs)
		}
	}
	return req, nil, nil
}

// PostMCPHook inspects and redacts text-bearing tool results after an MCP tool execution.
func (p *Plugin) PostMCPHook(ctx *schemas.BifrostContext, resp *schemas.BifrostMCPResponse, bifrostErr *schemas.BifrostError) (*schemas.BifrostMCPResponse, *schemas.BifrostError, error) {
	if bifrostErr != nil || resp == nil {
		return resp, bifrostErr, nil
	}
	if resp.ChatMessage != nil && resp.ChatMessage.Content != nil {
		for i := range p.config.cfg.GuardrailRules {
			rule := &p.config.cfg.GuardrailRules[i]
			if !rule.Enabled || (rule.Target != "mcp" && rule.Target != "llm" && rule.Target != "") {
				continue
			}
			if rule.ApplyTo != ApplyToOutput && rule.ApplyTo != ApplyToBoth {
				continue
			}
			if !p.sampled(rule) {
				continue
			}
			if errResp := p.applyRulesToMCPContent(ctx, rule, resp.ChatMessage.Content, schemas.RedactionPhaseOutput); errResp != nil {
				return nil, errResp, nil
			}
		}
	}
	return resp, bifrostErr, nil
}

// applyRulesToText evaluates rules on text.
func (p *Plugin) applyRulesToText(ctx *schemas.BifrostContext, rule *Rule, text string, phase schemas.RedactionPhase) (*schemas.BifrostError, string) {
	newText := text
	for _, pid := range rule.ProviderConfigIDs {
		// 1. Regex
		if patterns, ok := p.config.patterns[pid]; ok && len(patterns) > 0 {
			blocked, t, detected := evaluatePatterns(ctx, patterns, newText, phase)
			if len(detected) > 0 {
				p.logWarn("guardrails: mcp rule %q detected %d match(es): %v", rule.Name, len(detected), detected)
			}
			if len(blocked) > 0 {
				return blockError(rule, blocked), text
			}
			newText = t
		}
		// 2. Betterleaks
		if sec, ok := p.config.secrets[pid]; ok && sec != nil {
			blocked, t, detected := sec.evaluate(ctx, newText, phase)
			if len(detected) > 0 {
				p.logWarn("guardrails: mcp rule %q (secrets) detected %d secret(s): %v", rule.Name, len(detected), detected)
			}
			if len(blocked) > 0 {
				return blockErrorSimple(rule, fmt.Sprintf("detected secrets (%s)", strings.Join(blocked, ", "))), text
			}
			newText = t
		}
	}
	return nil, newText
}

// applyRulesToMCPContent evaluates rules on MCP tool content (string and blocks).
func (p *Plugin) applyRulesToMCPContent(ctx *schemas.BifrostContext, rule *Rule, content *schemas.ChatMessageContent, phase schemas.RedactionPhase) *schemas.BifrostError {
	if content == nil {
		return nil
	}
	if content.ContentStr != nil {
		errResp, newText := p.applyRulesToText(ctx, rule, *content.ContentStr, phase)
		if errResp != nil {
			return errResp
		}
		if newText != *content.ContentStr {
			content.ContentStr = ptr(newText)
		}
	}
	for j := range content.ContentBlocks {
		blk := &content.ContentBlocks[j]
		if blk.Text != nil {
			errResp, newText := p.applyRulesToText(ctx, rule, *blk.Text, phase)
			if errResp != nil {
				return errResp
			}
			if newText != *blk.Text {
				blk.Text = ptr(newText)
			}
		}
	}
	return nil
}
