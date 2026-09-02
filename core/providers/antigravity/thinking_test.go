package antigravity

import (
	"testing"

	schemas "github.com/maximhq/bifrost/core/schemas"
)

// RED test: the Antigravity endpoint expects the agy CLI default thinking
// config (includeThoughts=true, thinkingBudget=-1 dynamic) whenever the client
// did not send any reasoning parameters. See captured data-agy-gemini.json
// generationConfig.
func TestToAntigravityChatRequest_DefaultThinkingConfig(t *testing.T) {
	newReq := func(params *schemas.ChatParameters) *schemas.BifrostChatRequest {
		return &schemas.BifrostChatRequest{
			Model: "gemini-3.6-flash-high",
			Input: []schemas.ChatMessage{
				{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: strPtr("hi")}},
			},
			Params: params,
		}
	}
	ctx := schemas.NewBifrostContext(nil, schemas.NoDeadline)

	t.Run("no reasoning params -> agy default thinkingConfig injected", func(t *testing.T) {
		envelope, jsonBytes, err := ToAntigravityChatRequest(ctx, newReq(nil), "proj-123")
		if err != nil {
			t.Fatalf("ToAntigravityChatRequest failed: %v", err)
		}
		if envelope == nil || envelope.Request == nil || envelope.Request.GenerationConfig == nil {
			t.Fatalf("expected GenerationConfig to be present, envelope: %+v", envelope)
		}
		tc := envelope.Request.GenerationConfig.ThinkingConfig
		if tc == nil {
			t.Fatalf("expected default ThinkingConfig {includeThoughts:true, thinkingBudget:-1}, got nil; body: %s", jsonBytes)
		}
		if !tc.IncludeThoughts {
			t.Errorf("includeThoughts = false, want true")
		}
		if tc.ThinkingBudget == nil || *tc.ThinkingBudget != -1 {
			t.Errorf("thinkingBudget = %v, want -1 (dynamic)", tc.ThinkingBudget)
		}
	})

	t.Run("client-provided reasoning effort must NOT be overwritten", func(t *testing.T) {
		effort := "low"
		req := newReq(&schemas.ChatParameters{
			Reasoning: &schemas.ChatReasoning{Effort: &effort},
		})
		envelope, _, err := ToAntigravityChatRequest(ctx, req, "proj-123")
		if err != nil {
			t.Fatalf("ToAntigravityChatRequest failed: %v", err)
		}
		if envelope == nil || envelope.Request == nil || envelope.Request.GenerationConfig == nil ||
			envelope.Request.GenerationConfig.ThinkingConfig == nil {
			t.Fatalf("expected client reasoning to produce ThinkingConfig, got none")
		}
		tc := envelope.Request.GenerationConfig.ThinkingConfig
		if tc.ThinkingLevel == nil && tc.ThinkingBudget != nil && *tc.ThinkingBudget == -1 {
			t.Errorf("client effort was overwritten with the dynamic default (budget=-1, no level)")
		}
	})

	t.Run("client-provided max reasoning tokens must NOT be overwritten", func(t *testing.T) {
		mt := 4096
		req := newReq(&schemas.ChatParameters{
			Reasoning: &schemas.ChatReasoning{MaxTokens: &mt},
		})
		envelope, _, err := ToAntigravityChatRequest(ctx, req, "proj-123")
		if err != nil {
			t.Fatalf("ToAntigravityChatRequest failed: %v", err)
		}
		if envelope == nil || envelope.Request == nil || envelope.Request.GenerationConfig == nil {
			t.Fatalf("expected GenerationConfig from client max_tokens")
		}
		tc := envelope.Request.GenerationConfig.ThinkingConfig
		if tc == nil {
			t.Fatalf("expected ThinkingConfig from client max_tokens, got nil")
		}
		if tc.ThinkingBudget == nil {
			t.Fatalf("expected explicit thinkingBudget from client max_tokens, got nil")
		}
		if *tc.ThinkingBudget != 4096 {
			t.Errorf("thinkingBudget = %d, want 4096 (client value preserved)", *tc.ThinkingBudget)
		}
	})
}
