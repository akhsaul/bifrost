package guardrails

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
)

func TestReversible_TokenGenerationAndStorage(t *testing.T) {
	ctx := schemas.NewBifrostContext(context.Background(), time.Now().Add(time.Minute))
	secret := "super-secret-token-12345"

	token := generateReversibleToken("TOKEN", secret)
	if !strings.HasPrefix(token, "__BF_REV_TOKEN_") || !strings.HasSuffix(token, "__") {
		t.Fatalf("unexpected token format: %q", token)
	}

	storeReversibleToken(ctx, token, secret)
	tokens := getReversibleTokens(ctx)
	if tokens[token] != secret {
		t.Fatalf("expected stored secret %q, got %q", secret, tokens[token])
	}

	// Detokenize back
	input := "curl -H 'Authorization: Bearer " + token + "' https://api.com"
	restored := detokenizeString(input, tokens)
	expected := "curl -H 'Authorization: Bearer " + secret + "' https://api.com"
	if restored != expected {
		t.Fatalf("detokenize failed: got %q, want %q", restored, expected)
	}
}

func TestPostLLMHook_DetokenizesToolCallArguments(t *testing.T) {
	ctx := schemas.NewBifrostContext(context.Background(), time.Now().Add(time.Minute))
	secret := "ghp_Rs75tB9xWQ8vN3mK2pL7dXcV1yHjF4gT6zAq"
	token := generateReversibleToken("SECRET", secret)
	storeReversibleToken(ctx, token, secret)

	p, err := Init(&Config{}, nil)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}

	// Response with tool calls containing the placeholder
	toolArgs := `{"command":"./script.sh --token ` + token + `"}`
	resp := &schemas.BifrostResponse{
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
											Name:      strPtr("bash"),
											Arguments: toolArgs,
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

	gotResp, _, err := p.PostLLMHook(ctx, resp, nil)
	if err != nil {
		t.Fatalf("PostLLMHook: %v", err)
	}
	if gotResp == nil {
		t.Fatal("expected response to be returned")
	}

	tc := gotResp.ChatResponse.Choices[0].ChatNonStreamResponseChoice.Message.ChatAssistantMessage.ToolCalls[0]
	if strings.Contains(tc.Function.Arguments, token) {
		t.Fatalf("token placeholder was not replaced: %q", tc.Function.Arguments)
	}
	if !strings.Contains(tc.Function.Arguments, secret) {
		t.Fatalf("original secret not restored in tool arguments: %q", tc.Function.Arguments)
	}
}
