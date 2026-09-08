package guardrails

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
)

func TestMCPHooks_ToolResultRedaction(t *testing.T) {
	ctx := schemas.NewBifrostContext(context.Background(), time.Now().Add(time.Minute))
	p := secretsRedactPlugin(t, RedactionReplace, RedactionModeRuntimeReversible)

	resp := &schemas.BifrostMCPResponse{
		ChatMessage: &schemas.ChatMessage{
			Role:    schemas.ChatMessageRoleTool,
			Content: &schemas.ChatMessageContent{ContentStr: ptr("file read: DB_PASSWORD=" + sampleGitHubPAT)},
		},
	}
	gotResp, _, err := p.PostMCPHook(ctx, resp, nil)
	if err != nil {
		t.Fatalf("PostMCPHook: %v", err)
	}
	got := *gotResp.ChatMessage.Content.ContentStr
	if strings.Contains(got, sampleGitHubPAT) {
		t.Fatalf("tool result secret not redacted: %q", got)
	}
	if !strings.Contains(got, "[SECRET-1]") {
		t.Fatalf("expected [SECRET-1] in tool result: %q", got)
	}

	// Reveal mapping must be stored on context for runtime_reversible.
	data, ok := schemas.RedactionDataFromContext(ctx)
	if !ok {
		t.Fatal("expected redaction data on context")
	}
	found := false
	for _, v := range data.ReversibleMappings.Output {
		if v == sampleGitHubPAT {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected reveal mapping in output phase, got %v", data.ReversibleMappings.Output)
	}
}

func TestMCPHooks_ToolArgumentsRedaction(t *testing.T) {
	ctx := schemas.NewBifrostContext(context.Background(), time.Now().Add(time.Minute))
	p := secretsRedactPlugin(t, RedactionReplace, RedactionModeRuntimeReversible)

	req := &schemas.BifrostMCPRequest{
		ChatAssistantMessageToolCall: &schemas.ChatAssistantMessageToolCall{
			Function: schemas.ChatAssistantMessageToolCallFunction{
				Name:      ptr("bash"),
				Arguments: `{"command":"echo 'key ` + sampleGitHubPAT + `'";}`,
			},
		},
	}
	gotReq, sc, err := p.PreMCPHook(ctx, req)
	if err != nil || sc != nil {
		t.Fatalf("PreMCPHook should pass, got sc=%v err=%v", sc, err)
	}
	got := gotReq.ChatAssistantMessageToolCall.Function.Arguments
	if strings.Contains(got, sampleGitHubPAT) {
		t.Fatalf("tool arguments secret not redacted: %q", got)
	}
	if !strings.Contains(got, "[SECRET-1]") {
		t.Fatalf("expected [SECRET-1] in tool arguments: %q", got)
	}
}
