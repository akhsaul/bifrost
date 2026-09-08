package guardrails

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
)

// secretsRedactPlugin builds a guardrails plugin with one secrets provider (replace strategy) and one rule scanning input+output.
func secretsRedactPlugin(t *testing.T, strategy RedactionStrategy, mode RedactionMode) *Plugin {
	t.Helper()
	p, err := Init(&Config{
		GuardrailProviders: []GuardrailProviderConfig{
			{
				ID:           1,
				ProviderName: ProviderTypeSecrets,
				PolicyName:   "secrets",
				Enabled:      true,
				Config: ProviderConfigItem{
					SecretsConfig: &SecretsConfig{
						Action:            PatternActionRedact,
						RedactionStrategy: strategy,
						RedactionMode:     mode,
					},
				},
			},
		},
		GuardrailRules: []Rule{
			{
				ID:                1,
				Name:              "secrets-rule",
				Enabled:           true,
				Target:            "llm",
				ApplyTo:           ApplyToBoth,
				ProviderConfigIDs: []int{1},
			},
		},
	}, nil)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	return p
}

func toolCallResp(args string) *schemas.BifrostResponse {
	return &schemas.BifrostResponse{
		ChatResponse: &schemas.BifrostChatResponse{
			Choices: []schemas.BifrostResponseChoice{
				{
					ChatNonStreamResponseChoice: &schemas.ChatNonStreamResponseChoice{
						Message: &schemas.ChatMessage{
							Role: schemas.ChatMessageRoleAssistant,
							ChatAssistantMessage: &schemas.ChatAssistantMessage{
								ToolCalls: []schemas.ChatAssistantMessageToolCall{
									{
										Function: schemas.ChatAssistantMessageToolCallFunction{
											Name:      ptr("bash"),
											Arguments: args,
										},
									},
								},
							},
						},
					},
				},
			},
		},
	}
}

func textResp(text string) *schemas.BifrostResponse {
	return &schemas.BifrostResponse{
		ChatResponse: &schemas.BifrostChatResponse{
			Choices: []schemas.BifrostResponseChoice{
				{
					ChatNonStreamResponseChoice: &schemas.ChatNonStreamResponseChoice{
						Message: &schemas.ChatMessage{
							Role:    schemas.ChatMessageRoleAssistant,
							Content: &schemas.ChatMessageContent{ContentStr: ptr(text)},
						},
					},
				},
			},
		},
	}
}

func TestHooks_EgressRestoration_TextPartialMask(t *testing.T) {
	ctx := schemas.NewBifrostContext(context.Background(), time.Now().Add(time.Minute))
	p := secretsRedactPlugin(t, RedactionReplace, RedactionModeRuntimeReversible)

	// Output response echoes the secret; PostLLMHook redacts then egress-restores to partially masked.
	resp := textResp("I received token " + sampleGitHubPAT)
	gotResp, _, err := p.PostLLMHook(ctx, resp, nil)
	if err != nil {
		t.Fatalf("PostLLMHook: %v", err)
	}
	got := *gotResp.ChatResponse.Choices[0].ChatNonStreamResponseChoice.Message.Content.ContentStr
	if strings.Contains(got, sampleGitHubPAT) {
		t.Fatalf("full secret leaked to client text: %q", got)
	}
	if !strings.Contains(got, maskPartially(sampleGitHubPAT)) {
		t.Fatalf("expected partially masked value in text: %q", got)
	}
}

func TestHooks_EgressRestoration_ToolCallsFullSecret(t *testing.T) {
	ctx := schemas.NewBifrostContext(context.Background(), time.Now().Add(time.Minute))
	p := secretsRedactPlugin(t, RedactionReplace, RedactionModeRuntimeReversible)

	// Input redacts the secret -> [SECRET-1]. Output tool call echoes the placeholder.
	req := &schemas.BifrostRequest{
		ChatRequest: &schemas.BifrostChatRequest{
			Input: []schemas.ChatMessage{
				{
					Role:    schemas.ChatMessageRoleUser,
					Content: &schemas.ChatMessageContent{ContentStr: ptr("use token " + sampleGitHubPAT)},
				},
			},
		},
	}
	_, sc, err := p.PreLLMHook(ctx, req)
	if err != nil || sc != nil {
		t.Fatalf("PreLLMHook should pass, got sc=%v err=%v", sc, err)
	}
	redactedInput := *req.ChatRequest.Input[0].Content.ContentStr
	if strings.Contains(redactedInput, sampleGitHubPAT) {
		t.Fatalf("input not redacted: %q", redactedInput)
	}
	placeholder := strings.TrimSpace(strings.Split(redactedInput, " ")[2])

	// Model echoes the placeholder in tool call args
	resp := toolCallResp(`{"command":"echo 'token is ` + placeholder + `'"}`)
	gotResp, _, err := p.PostLLMHook(ctx, resp, nil)
	if err != nil {
		t.Fatalf("PostLLMHook: %v", err)
	}
	tc := gotResp.ChatResponse.Choices[0].ChatNonStreamResponseChoice.Message.ChatAssistantMessage.ToolCalls[0]
	if strings.Contains(tc.Function.Arguments, placeholder) {
		t.Fatalf("placeholder not restored in tool args: %q", tc.Function.Arguments)
	}
	if !strings.Contains(tc.Function.Arguments, sampleGitHubPAT) {
		t.Fatalf("full secret expected in tool args: %q", tc.Function.Arguments)
	}
}

func TestHooks_RevealMappingStoredOnContext(t *testing.T) {
	ctx := schemas.NewBifrostContext(context.Background(), time.Now().Add(time.Minute))
	p := secretsRedactPlugin(t, RedactionReplace, RedactionModeRuntimeReversible)

	req := &schemas.BifrostRequest{
		ChatRequest: &schemas.BifrostChatRequest{
			Input: []schemas.ChatMessage{
				{
					Role:    schemas.ChatMessageRoleUser,
					Content: &schemas.ChatMessageContent{ContentStr: ptr("use token " + sampleGitHubPAT)},
				},
			},
		},
	}
	_, _, err := p.PreLLMHook(ctx, req)
	if err != nil {
		t.Fatalf("PreLLMHook: %v", err)
	}

	data, ok := schemas.RedactionDataFromContext(ctx)
	if !ok {
		t.Fatal("expected redaction data on context for runtime_reversible")
	}
	found := false
	for _, v := range data.ReversibleMappings.Input {
		if v == sampleGitHubPAT {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected reveal mapping containing original secret, got %v", data.ReversibleMappings.Input)
	}
}

func TestHooks_PermanentModeNoRevealMapping(t *testing.T) {
	ctx := schemas.NewBifrostContext(context.Background(), time.Now().Add(time.Minute))
	p := secretsRedactPlugin(t, RedactionReplace, RedactionModeRuntime)

	req := &schemas.BifrostRequest{
		ChatRequest: &schemas.BifrostChatRequest{
			Input: []schemas.ChatMessage{
				{
					Role:    schemas.ChatMessageRoleUser,
					Content: &schemas.ChatMessageContent{ContentStr: ptr("use token " + sampleGitHubPAT)},
				},
			},
		},
	}
	_, _, err := p.PreLLMHook(ctx, req)
	if err != nil {
		t.Fatalf("PreLLMHook: %v", err)
	}

	_, ok := schemas.RedactionDataFromContext(ctx)
	if ok {
		t.Fatal("permanent runtime mode must NOT store reveal mapping")
	}
}

func TestHooks_ToolResultMessagesScannedOnInput(t *testing.T) {
	ctx := schemas.NewBifrostContext(context.Background(), time.Now().Add(time.Minute))
	p := secretsRedactPlugin(t, RedactionReplace, RedactionModeRuntimeReversible)

	// Tool result message contains a leaked secret.
	req := &schemas.BifrostRequest{
		ChatRequest: &schemas.BifrostChatRequest{
			Input: []schemas.ChatMessage{
				{
					Role:    schemas.ChatMessageRoleUser,
					Content: &schemas.ChatMessageContent{ContentStr: ptr("what did the tool print?")},
				},
				{
					Role:            schemas.ChatMessageRoleTool,
					ChatToolMessage: &schemas.ChatToolMessage{ToolCallID: ptr("call_1")},
					Content:         &schemas.ChatMessageContent{ContentStr: ptr("config: DB_PASSWORD=" + sampleGitHubPAT)},
				},
			},
		},
	}
	_, sc, err := p.PreLLMHook(ctx, req)
	if err != nil || sc != nil {
		t.Fatalf("PreLLMHook should pass, got sc=%v err=%v", sc, err)
	}
	toolResult := *req.ChatRequest.Input[1].Content.ContentStr
	if strings.Contains(toolResult, sampleGitHubPAT) {
		t.Fatalf("tool result secret not redacted before reaching LLM: %q", toolResult)
	}
	if !strings.Contains(toolResult, "[SECRET-1]") {
		t.Fatalf("expected [SECRET-1] in tool result: %q", toolResult)
	}
}
