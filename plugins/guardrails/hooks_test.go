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

func streamToolCallResp(args string) *schemas.BifrostResponse {
	return &schemas.BifrostResponse{
		ChatResponse: &schemas.BifrostChatResponse{
			Choices: []schemas.BifrostResponseChoice{
				{
					ChatStreamResponseChoice: &schemas.ChatStreamResponseChoice{
						Delta: &schemas.ChatStreamResponseChoiceDelta{
							ToolCalls: []schemas.ChatAssistantMessageToolCall{
								{
									Index: 0,
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
	}
}

func streamTextResp(text string) *schemas.BifrostResponse {
	return &schemas.BifrostResponse{
		ChatResponse: &schemas.BifrostChatResponse{
			Choices: []schemas.BifrostResponseChoice{
				{
					ChatStreamResponseChoice: &schemas.ChatStreamResponseChoice{
						Delta: &schemas.ChatStreamResponseChoiceDelta{
							Content: ptr(text),
						},
					},
				},
			},
		},
	}
}

func TestHooks_EgressRestoration_StreamingToolCallsFullSecret(t *testing.T) {
	ctx := schemas.NewBifrostContext(context.Background(), time.Now().Add(time.Minute))
	p := secretsRedactPlugin(t, RedactionReplace, RedactionModeRuntimeReversible)

	// Ingress: secret -> [SECRET-1]
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

	// Model streams tool call containing [SECRET-1]
	resp := streamToolCallResp(`{"command":"echo 'token is [SECRET-1]'"}`)
	gotResp, _, err := p.PostLLMHook(ctx, resp, nil)
	if err != nil {
		t.Fatalf("PostLLMHook: %v", err)
	}
	delta := gotResp.ChatResponse.Choices[0].ChatStreamResponseChoice.Delta
	if len(delta.ToolCalls) == 0 {
		t.Fatal("expected tool calls in delta")
	}
	tcArgs := delta.ToolCalls[0].Function.Arguments
	if strings.Contains(tcArgs, "[SECRET-1]") {
		t.Fatalf("placeholder [SECRET-1] not restored in streaming tool args: %q", tcArgs)
	}
	if !strings.Contains(tcArgs, sampleGitHubPAT) {
		t.Fatalf("full secret expected in streaming tool args, got %q", tcArgs)
	}
}

func TestHooks_EgressRestoration_StreamingTextPartialMask(t *testing.T) {
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

	// Model streams message text containing [SECRET-1]
	resp := streamTextResp("my secret is [SECRET-1]")
	gotResp, _, err := p.PostLLMHook(ctx, resp, nil)
	if err != nil {
		t.Fatalf("PostLLMHook: %v", err)
	}
	delta := gotResp.ChatResponse.Choices[0].ChatStreamResponseChoice.Delta
	if delta.Content == nil {
		t.Fatal("expected content in delta")
	}
	gotText := *delta.Content
	if strings.Contains(gotText, "[SECRET-1]") {
		t.Fatalf("placeholder [SECRET-1] still in streaming text: %q", gotText)
	}
	if strings.Contains(gotText, sampleGitHubPAT) {
		t.Fatalf("full secret leaked to streaming text: %q", gotText)
	}
	if !strings.Contains(gotText, maskPartially(sampleGitHubPAT)) {
		t.Fatalf("expected partially masked value in streaming text, got %q", gotText)
	}
}

func TestHooks_EgressRestoration_StreamingSplitChunks(t *testing.T) {
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

	// Chunk 1 has split prefix: {"command":"echo [SEC
	chunk1 := streamToolCallResp(`{"command":"echo [SEC`)
	got1, _, err := p.PostLLMHook(ctx, chunk1, nil)
	if err != nil {
		t.Fatalf("chunk1: %v", err)
	}
	args1 := got1.ChatResponse.Choices[0].ChatStreamResponseChoice.Delta.ToolCalls[0].Function.Arguments

	// Chunk 2 completes the token: RET-1]"}
	chunk2 := streamToolCallResp(`RET-1]"}`)
	// Mark choice final or finish_reason
	chunk2.ChatResponse.Choices[0].FinishReason = ptr("tool_calls")
	got2, _, err := p.PostLLMHook(ctx, chunk2, nil)
	if err != nil {
		t.Fatalf("chunk2: %v", err)
	}
	args2 := got2.ChatResponse.Choices[0].ChatStreamResponseChoice.Delta.ToolCalls[0].Function.Arguments

	combined := args1 + args2
	if strings.Contains(combined, "[SECRET-1]") || strings.Contains(combined, "[SEC") {
		t.Fatalf("split placeholder not detokenized in combined stream: %q", combined)
	}
	if !strings.Contains(combined, sampleGitHubPAT) {
		t.Fatalf("expected secret in combined stream, got: %q", combined)
	}
}

func TestHooks_AssistantMessageToolCallsScannedOnInput(t *testing.T) {
	ctx := schemas.NewBifrostContext(context.Background(), time.Now().Add(time.Minute))
	p := secretsRedactPlugin(t, RedactionReplace, RedactionModeRuntimeReversible)

	// Turn 2 request: conversation history contains assistant message with original secret in tool call arguments
	req := &schemas.BifrostRequest{
		ChatRequest: &schemas.BifrostChatRequest{
			Input: []schemas.ChatMessage{
				{
					Role:    schemas.ChatMessageRoleUser,
					Content: &schemas.ChatMessageContent{ContentStr: ptr("run test with " + sampleGitHubPAT)},
				},
				{
					Role: schemas.ChatMessageRoleAssistant,
					ChatAssistantMessage: &schemas.ChatAssistantMessage{
						ToolCalls: []schemas.ChatAssistantMessageToolCall{
							{
								Index: 0,
								Function: schemas.ChatAssistantMessageToolCallFunction{
									Name:      ptr("bash"),
									Arguments: `{"command":"cat << EOF >> test.txt\n` + sampleGitHubPAT + `\nEOF"}`,
								},
							},
						},
					},
				},
				{
					Role:            schemas.ChatMessageRoleTool,
					ChatToolMessage: &schemas.ChatToolMessage{ToolCallID: ptr("call_1")},
					Content:         &schemas.ChatMessageContent{ContentStr: ptr("(no output)")},
				},
			},
		},
	}
	_, sc, err := p.PreLLMHook(ctx, req)
	if err != nil || sc != nil {
		t.Fatalf("PreLLMHook should pass, got sc=%v err=%v", sc, err)
	}

	tcArgs := req.ChatRequest.Input[1].ChatAssistantMessage.ToolCalls[0].Function.Arguments
	if strings.Contains(tcArgs, sampleGitHubPAT) {
		t.Fatalf("assistant tool arguments leaked original secret to LLM: %q", tcArgs)
	}
	if !strings.Contains(tcArgs, "[SECRET-1]") {
		t.Fatalf("expected [SECRET-1] in assistant tool arguments: %q", tcArgs)
	}
}

