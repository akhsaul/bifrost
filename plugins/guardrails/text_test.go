package guardrails

import (
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

func ptr(s string) *string { return &s }

func chatReq(texts ...string) *schemas.BifrostRequest {
	msgs := make([]schemas.ChatMessage, 0, len(texts))
	for _, s := range texts {
		msgs = append(msgs, schemas.ChatMessage{
			Role:    schemas.ChatMessageRoleUser,
			Content: &schemas.ChatMessageContent{ContentStr: ptr(s)},
		})
	}
	return &schemas.BifrostRequest{
		RequestType: schemas.ChatCompletionRequest,
		ChatRequest: &schemas.BifrostChatRequest{Provider: schemas.OpenAI, Model: "gpt-4o", Input: msgs},
	}
}

func chatResp(text string) *schemas.BifrostResponse {
	return &schemas.BifrostResponse{
		ChatResponse: &schemas.BifrostChatResponse{
			Choices: []schemas.BifrostResponseChoice{
				{ChatNonStreamResponseChoice: &schemas.ChatNonStreamResponseChoice{
					Message: &schemas.ChatMessage{
						Role:    schemas.ChatMessageRoleAssistant,
						Content: &schemas.ChatMessageContent{ContentStr: ptr(text)},
					},
				}},
			},
		},
	}
}

func TestExtractRequestText_ChatStringContent(t *testing.T) {
	req := chatReq("hello", "world")
	got := extractRequestText(req)
	if got != "hello\nworld" {
		t.Fatalf("got %q", got)
	}
}

func TestExtractRequestText_ContentBlocks(t *testing.T) {
	msg := schemas.ChatMessage{
		Role: schemas.ChatMessageRoleUser,
		Content: &schemas.ChatMessageContent{
			ContentBlocks: []schemas.ChatContentBlock{
				{Type: schemas.ChatContentBlockTypeText, Text: ptr("block one")},
				{Type: schemas.ChatContentBlockTypeText, Text: ptr("block two")},
			},
		},
	}
	req := &schemas.BifrostRequest{
		RequestType: schemas.ChatCompletionRequest,
		ChatRequest: &schemas.BifrostChatRequest{Input: []schemas.ChatMessage{msg}},
	}
	got := extractRequestText(req)
	if got != "block one\nblock two" {
		t.Fatalf("got %q", got)
	}
}

func TestExtractRequestText_NilContentSkipped(t *testing.T) {
	req := chatReq("keep me")
	req.ChatRequest.Input = append(req.ChatRequest.Input, schemas.ChatMessage{Role: schemas.ChatMessageRoleUser})
	got := extractRequestText(req)
	if got != "keep me" {
		t.Fatalf("got %q", got)
	}
}

func TestExtractRequestText_NonChatRequest(t *testing.T) {
	req := &schemas.BifrostRequest{RequestType: schemas.EmbeddingRequest}
	if got := extractRequestText(req); got != "" {
		t.Fatalf("non-chat request should yield empty text, got %q", got)
	}
}

func TestRedactRequestInPlace(t *testing.T) {
	req := chatReq("mail me at a@b.com")
	replaced := redactRequestInPlace(req, "a@b.com", "[EMAIL]")
	if !replaced {
		t.Fatal("expected a replacement to happen")
	}
	if got := *req.ChatRequest.Input[0].Content.ContentStr; got != "mail me at [EMAIL]" {
		t.Fatalf("request not redacted in place, got %q", got)
	}
}

func TestRedactRequestInPlace_ContentBlocks(t *testing.T) {
	msg := schemas.ChatMessage{
		Content: &schemas.ChatMessageContent{
			ContentBlocks: []schemas.ChatContentBlock{{Type: schemas.ChatContentBlockTypeText, Text: ptr("say a@b.com")}},
		},
	}
	req := &schemas.BifrostRequest{
		RequestType: schemas.ChatCompletionRequest,
		ChatRequest: &schemas.BifrostChatRequest{Input: []schemas.ChatMessage{msg}},
	}
	if !redactRequestInPlace(req, "a@b.com", "[EMAIL]") {
		t.Fatal("expected replacement")
	}
	if got := *req.ChatRequest.Input[0].Content.ContentBlocks[0].Text; got != "say [EMAIL]" {
		t.Fatalf("block not redacted, got %q", got)
	}
}

func TestExtractResponseText(t *testing.T) {
	if got := extractResponseText(chatResp("the answer")); got != "the answer" {
		t.Fatalf("got %q", got)
	}
}

func TestExtractResponseText_NilChat(t *testing.T) {
	if got := extractResponseText(&schemas.BifrostResponse{}); got != "" {
		t.Fatalf("got %q", got)
	}
}

func TestRedactResponseInPlace(t *testing.T) {
	resp := chatResp("my email is a@b.com ok")
	if !redactResponseInPlace(resp, "a@b.com", "[EMAIL]") {
		t.Fatal("expected replacement")
	}
	got := *resp.ChatResponse.Choices[0].ChatNonStreamResponseChoice.Message.Content.ContentStr
	if got != "my email is [EMAIL] ok" {
		t.Fatalf("got %q", got)
	}
}

func TestRedactResponseInPlace_TextCompletionChoice(t *testing.T) {
	resp := &schemas.BifrostResponse{
		ChatResponse: &schemas.BifrostChatResponse{
			Choices: []schemas.BifrostResponseChoice{
				{TextCompletionResponseChoice: &schemas.TextCompletionResponseChoice{Text: ptr("secret a@b.com")}},
			},
		},
	}
	if !redactResponseInPlace(resp, "a@b.com", "[EMAIL]") {
		t.Fatal("expected replacement")
	}
	if got := *resp.ChatResponse.Choices[0].TextCompletionResponseChoice.Text; got != "secret [EMAIL]" {
		t.Fatalf("got %q", got)
	}
}
