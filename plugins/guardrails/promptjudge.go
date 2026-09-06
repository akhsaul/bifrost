package guardrails

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
)

// ChatRequestExecutor invokes a chat completion on Bifrost core. Wired from the
// transport layer (mirrors semanticcache's embedding executor pattern).
type ChatRequestExecutor func(ctx *schemas.BifrostContext, req *schemas.BifrostChatRequest) (*schemas.BifrostChatResponse, *schemas.BifrostError)

// promptJudge evaluates text against a natural-language policy using a
// configured LLM judge. Judge calls are marked to skip the plugin pipeline
// so the judge's own request cannot recursively trigger guardrails.
type promptJudge struct {
	cfg    PromptGuardrailConfig
	exec   ChatRequestExecutor
	logger schemas.Logger
}

// defaultJudgeSystemPrompt is the built-in classifier prompt. {{rule}} is
// replaced with the configured policy; evaluated content is sent as a
// separate JSON-encoded user message.
const defaultJudgeSystemPrompt = `You are a strict AI safety and policy compliance judge.
Evaluate the provided content against the policy below.

Policy:
{{rule}}

Respond with ONLY a JSON object (no markdown, no prose) with exactly these fields:
{"action": "ALLOW" | "BLOCK", "reason": "<brief explanation>"}

Guidelines:
- Return BLOCK only for clear policy violations.
- Unrelated, ambiguous, or uncertain content must be ALLOW.`

// newPromptJudge constructs a judge bound to one provider config.
func newPromptJudge(cfg PromptGuardrailConfig, exec ChatRequestExecutor) *promptJudge {
	if cfg.Timeout <= 0 {
		cfg.Timeout = 30
	}
	if cfg.MaxOutputTokens <= 0 {
		cfg.MaxOutputTokens = 200
	}
	return &promptJudge{cfg: cfg, exec: exec}
}

// judgeDecision mirrors the structured response the judge model must return.
type judgeDecision struct {
	Action string `json:"action"`
	Reason string `json:"reason"`
}

// buildSystemPrompt renders the system prompt, substituting the policy.
func (j *promptJudge) buildSystemPrompt() string {
	tmpl := defaultJudgeSystemPrompt
	if strings.TrimSpace(j.cfg.PromptTemplate) != "" {
		tmpl = j.cfg.PromptTemplate
	}
	return strings.ReplaceAll(tmpl, "{{rule}}", j.cfg.Rule)
}

// evaluate sends content to the judge and returns (blocked, reason, err).
// Judge transport errors are reported via err so the caller can fail open.
func (j *promptJudge) evaluate(parent *schemas.BifrostContext, content string) (bool, string, error) {
	if strings.TrimSpace(content) == "" {
		// No text to evaluate: allow without a judge call.
		return false, "", nil
	}
	if j.exec == nil {
		return false, "", fmt.Errorf("prompt judge: chat request executor is not wired")
	}

	// Isolated context: fresh BifrostContext flagged to skip the plugin
	// pipeline, with a deadline from the configured timeout.
	timeout := time.Duration(j.cfg.Timeout) * time.Second
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	ctx := schemas.NewBifrostContext(context.Background(), time.Now().Add(timeout))
	ctx.SetValue(schemas.BifrostContextKeySkipPluginPipeline, true)

	contentJSON, err := json.Marshal(map[string]string{"content": content})
	if err != nil {
		return false, "", fmt.Errorf("prompt judge: failed to encode content: %w", err)
	}

	req := &schemas.BifrostChatRequest{
		Provider: schemas.ModelProvider(j.cfg.JudgeProvider),
		Model:    j.cfg.JudgeModel,
		Input: []schemas.ChatMessage{
			{
				Role:    schemas.ChatMessageRoleSystem,
				Content: &schemas.ChatMessageContent{ContentStr: strPtr(j.buildSystemPrompt())},
			},
			{
				Role:    schemas.ChatMessageRoleUser,
				Content: &schemas.ChatMessageContent{ContentStr: strPtr(string(contentJSON))},
			},
		},
		Params: &schemas.ChatParameters{
			ExtraParams: map[string]interface{}{
				// Structured output hint; providers that don't support
				// response_format ignore unknown extra params.
				"response_format": map[string]interface{}{
					"type": "json_object",
				},
				"max_tokens": j.cfg.MaxOutputTokens,
			},
		},
	}

	resp, bifrostErr := j.exec(ctx, req)
	if bifrostErr != nil {
		return false, "", fmt.Errorf("prompt judge: judge request failed: %s", bifrostErr.GetErrorString())
	}
	if resp == nil || len(resp.Choices) == 0 {
		return false, "", fmt.Errorf("prompt judge: empty judge response")
	}

	// Extract raw text from the first non-stream choice.
	var raw string
	for i := range resp.Choices {
		c := &resp.Choices[i]
		if c.ChatNonStreamResponseChoice != nil && c.ChatNonStreamResponseChoice.Message != nil && c.ChatNonStreamResponseChoice.Message.Content != nil {
			if c.ChatNonStreamResponseChoice.Message.Content.ContentStr != nil {
				raw = *c.ChatNonStreamResponseChoice.Message.Content.ContentStr
				break
			}
		}
	}
	if strings.TrimSpace(raw) == "" {
		return false, "", fmt.Errorf("prompt judge: judge returned no text")
	}

	decision, perr := parseJudgeDecision(raw)
	if perr != nil {
		return false, "", fmt.Errorf("prompt judge: %w", perr)
	}
	if strings.EqualFold(decision.Action, "BLOCK") {
		return true, decision.Reason, nil
	}
	return false, decision.Reason, nil
}

// parseJudgeDecision extracts the ALLOW/BLOCK JSON from judge output,
// tolerating markdown code fences around the JSON.
func parseJudgeDecision(raw string) (judgeDecision, error) {
	trimmed := strings.TrimSpace(raw)
	// Strip ```json ... ``` fences if present.
	if idx := strings.Index(trimmed, "{"); idx > 0 {
		trimmed = trimmed[idx:]
	}
	if end := strings.LastIndex(trimmed, "}"); end >= 0 && end < len(trimmed)-1 {
		trimmed = trimmed[:end+1]
	}
	var d judgeDecision
	if err := json.Unmarshal([]byte(trimmed), &d); err != nil {
		return d, fmt.Errorf("failed to parse judge decision from %q: %w", truncateForLog(raw, 200), err)
	}
	d.Action = strings.ToUpper(strings.TrimSpace(d.Action))
	if d.Action != "ALLOW" && d.Action != "BLOCK" {
		return d, fmt.Errorf("judge returned unknown action %q", d.Action)
	}
	return d, nil
}

func truncateForLog(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

func strPtr(s string) *string { return &s }
