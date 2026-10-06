package zed_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/providers/zed"
	"github.com/maximhq/bifrost/core/schemas"
)

func loadCapture(t *testing.T, name string) map[string]interface{} {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	var v map[string]interface{}
	if err := json.Unmarshal(data, &v); err != nil {
		t.Fatal(err)
	}
	pr, ok := v["provider_request"].(map[string]interface{})
	if !ok {
		t.Fatalf("%s has no provider_request object", name)
	}
	return pr
}

func testCtx() *schemas.BifrostContext {
	return schemas.NewBifrostContext(context.Background(), time.Time{})
}

func zedTestProvider(t *testing.T) *zed.ZedProvider {
	t.Helper()
	p, err := zed.NewZedProvider(&schemas.ProviderConfig{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// TestGoldenAnthropicEnvelope compares the built envelope against the live
// Zed haiku capture: same top-level shape, same inner keys, thinking +
// temperature preserved, system as content blocks.
func TestGoldenAnthropicEnvelope(t *testing.T) {
	golden := loadCapture(t, "zed-req-anthropic.json")
	provider := zedTestProvider(t)

	req := &schemas.BifrostChatRequest{
		Provider: schemas.Zed,
		Model:    "claude-haiku-4-5",
		Input: []schemas.ChatMessage{
			{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("hello")}},
		},
		Params: &schemas.ChatParameters{
			Temperature: schemas.Ptr(1.0),
			Reasoning:   &schemas.ChatReasoning{MaxTokens: schemas.Ptr(4096)},
		},
	}
	env := provider.BuildEnvelopeForTest(testCtx(), req, zed.ZedInnerAnthropic)
	if env == nil {
		t.Fatal("nil envelope")
	}
	if env.Provider != zed.ZedInnerAnthropic || env.Model != "claude-haiku-4-5" {
		t.Fatalf("envelope routing = %q/%q", env.Provider, env.Model)
	}
	if env.ThreadID == "" || env.PromptID == "" {
		t.Fatal("thread_id and prompt_id must both be set")
	}
	raw, _ := json.Marshal(env.ProviderRequest)
	var inner map[string]interface{}
	if err := json.Unmarshal(raw, &inner); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"model", "max_tokens", "messages", "thinking", "temperature"} {
		if _, ok := inner[k]; !ok {
			t.Errorf("inner request missing key %q (golden has %v)", k, keysOf(golden))
		}
		if _, ok := golden[k]; !ok {
			t.Errorf("golden missing key %q — capture drift?", k)
		}
	}
	thinking, _ := inner["thinking"].(map[string]interface{})
	if thinking["type"] != "enabled" {
		t.Errorf("thinking.type = %v, want enabled", thinking["type"])
	}
}

// TestGoldenGeminiEnvelope compares against the live gemini capture:
// models/-prefixed model, contents/systemInstruction/generationConfig keys.
func TestGoldenGeminiEnvelope(t *testing.T) {
	golden := loadCapture(t, "zed-req-google.json")
	provider := zedTestProvider(t)

	req := &schemas.BifrostChatRequest{
		Provider: schemas.Zed,
		Model:    "gemini-3.5-flash",
		Input: []schemas.ChatMessage{
			{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("hello")}},
		},
	}
	env := provider.BuildEnvelopeForTest(testCtx(), req, zed.ZedInnerGoogle)
	if env == nil {
		t.Fatal("nil envelope")
	}
	raw, _ := json.Marshal(env.ProviderRequest)
	var inner map[string]interface{}
	if err := json.Unmarshal(raw, &inner); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"model", "contents", "generationConfig"} {
		if _, ok := inner[k]; !ok {
			t.Errorf("inner request missing key %q (golden has %v)", k, keysOf(golden))
		}
	}
	if model, _ := inner["model"].(string); !strings.HasPrefix(model, "models/") {
		t.Errorf("gemini model = %q, want models/ prefix (golden %v)", model, golden["model"])
	}
}

// TestGoldenOpenAIEnvelope compares against the live gpt-5-nano capture:
// input[]/stream/prompt_cache_key/store keys, no temperature/max_tokens.
func TestGoldenOpenAIEnvelope(t *testing.T) {
	golden := loadCapture(t, "zed-req-openai.json")
	provider := zedTestProvider(t)

	req := &schemas.BifrostResponsesRequest{
		Provider: schemas.Zed,
		Model:    "gpt-5-nano",
		Input: []schemas.ResponsesMessage{
			{
				Type: schemas.Ptr(schemas.ResponsesMessageTypeMessage),
				Role: ptrRole(schemas.ResponsesInputMessageRoleUser),
				Content: &schemas.ResponsesMessageContent{
					ContentBlocks: []schemas.ResponsesMessageContentBlock{{Type: "input_text", Text: schemas.Ptr("hello")}},
				},
			},
		},
	}
	env := provider.BuildResponsesEnvelopeForTest(testCtx(), req)
	if env == nil {
		t.Fatal("nil envelope")
	}
	raw, _ := json.Marshal(env.ProviderRequest)
	var inner map[string]interface{}
	if err := json.Unmarshal(raw, &inner); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"model", "input", "stream", "prompt_cache_key", "store"} {
		if _, ok := inner[k]; !ok {
			t.Errorf("inner request missing key %q (golden has %v)", k, keysOf(golden))
		}
	}
	for _, k := range []string{"temperature", "max_tokens", "max_output_tokens"} {
		if _, ok := inner[k]; ok {
			t.Errorf("inner request must not carry %q (golden has none)", k)
		}
	}
	if inner["prompt_cache_key"] != env.ThreadID {
		t.Errorf("prompt_cache_key = %v, want thread_id %q", inner["prompt_cache_key"], env.ThreadID)
	}
}

func keysOf(m map[string]interface{}) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// TestDefaultSystemPromptInjected verifies the red case behind the fix: a
// bare user-only transcript (no system prompt from the client) must still
// carry a system instruction in every Zed inner family, since Zed's cloud API
// rejects requests with no system content at all.
func TestDefaultSystemPromptInjected(t *testing.T) {
	provider := zedTestProvider(t)

	anthropicEnv := provider.BuildEnvelopeForTest(testCtx(), &schemas.BifrostChatRequest{
		Provider: schemas.Zed,
		Model:    "claude-haiku-4-5",
		Input: []schemas.ChatMessage{
			{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("hello")}},
		},
	}, zed.ZedInnerAnthropic)
	if anthropicEnv == nil {
		t.Fatal("nil anthropic envelope")
	}
	raw, _ := json.Marshal(anthropicEnv.ProviderRequest)
	var anthropicInner map[string]interface{}
	if err := json.Unmarshal(raw, &anthropicInner); err != nil {
		t.Fatal(err)
	}
	sys, ok := anthropicInner["system"]
	if !ok {
		t.Fatal("anthropic inner request must carry system when the client sent none")
	}
	if !strings.Contains(jsonString(t, sys), "You are helpfull assistant") {
		t.Errorf("anthropic system = %v, want default prompt", sys)
	}

	geminiEnv := provider.BuildEnvelopeForTest(testCtx(), &schemas.BifrostChatRequest{
		Provider: schemas.Zed,
		Model:    "gemini-3.5-flash",
		Input: []schemas.ChatMessage{
			{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("hello")}},
		},
	}, zed.ZedInnerGoogle)
	if geminiEnv == nil {
		t.Fatal("nil gemini envelope")
	}
	raw, _ = json.Marshal(geminiEnv.ProviderRequest)
	var geminiInner map[string]interface{}
	if err := json.Unmarshal(raw, &geminiInner); err != nil {
		t.Fatal(err)
	}
	si, ok := geminiInner["systemInstruction"]
	if !ok {
		t.Fatal("gemini inner request must carry systemInstruction when the client sent none")
	}
	if !strings.Contains(jsonString(t, si), "You are helpfull assistant") {
		t.Errorf("gemini systemInstruction = %v, want default prompt", si)
	}

	responsesEnv := provider.BuildResponsesEnvelopeForTest(testCtx(), &schemas.BifrostResponsesRequest{
		Provider: schemas.Zed,
		Model:    "gpt-5-nano",
		Input: []schemas.ResponsesMessage{
			{
				Type: schemas.Ptr(schemas.ResponsesMessageTypeMessage),
				Role: ptrRole(schemas.ResponsesInputMessageRoleUser),
				Content: &schemas.ResponsesMessageContent{
					ContentBlocks: []schemas.ResponsesMessageContentBlock{{Type: "input_text", Text: schemas.Ptr("hello")}},
				},
			},
		},
	})
	if responsesEnv == nil {
		t.Fatal("nil responses envelope")
	}
	raw, _ = json.Marshal(responsesEnv.ProviderRequest)
	var responsesInner map[string]interface{}
	if err := json.Unmarshal(raw, &responsesInner); err != nil {
		t.Fatal(err)
	}
	input, _ := responsesInner["input"].([]interface{})
	if len(input) == 0 {
		t.Fatal("openai inner input must not be empty")
	}
	first, _ := input[0].(map[string]interface{})
	if first["role"] != string(schemas.ResponsesInputMessageRoleSystem) {
		t.Fatalf("openai inner input[0].role = %v, want system", first["role"])
	}
	if !strings.Contains(jsonString(t, first["content"]), "You are helpfull assistant") {
		t.Errorf("openai inner input[0].content = %v, want default prompt", first["content"])
	}
}

// TestResponsesStringContentNormalizedToBlocks is the red test for the second
// live failure: user content arriving as a bare ContentStr serializes to
// "content":"..." on the wire, which Zed's Responses parser rejects
// ("invalid type: string ..., expected a sequence"). Every debug/zed-dev
// capture carries content as [{"type":"input_text","text":...}], so the
// converter must normalize string content into that block shape.
func TestResponsesStringContentNormalizedToBlocks(t *testing.T) {
	provider := zedTestProvider(t)
	env := provider.BuildResponsesEnvelopeForTest(testCtx(), &schemas.BifrostResponsesRequest{
		Provider: schemas.Zed,
		Model:    "gpt-5-nano",
		Input: []schemas.ResponsesMessage{
			{
				Type:    schemas.Ptr(schemas.ResponsesMessageTypeMessage),
				Role:    ptrRole(schemas.ResponsesInputMessageRoleSystem),
				Content: &schemas.ResponsesMessageContent{ContentStr: schemas.Ptr("remember NYF is 528901")},
			},
			{
				Type:    schemas.Ptr(schemas.ResponsesMessageTypeMessage),
				Role:    ptrRole(schemas.ResponsesInputMessageRoleUser),
				Content: &schemas.ResponsesMessageContent{ContentStr: schemas.Ptr("1+8= ? also remember NYF is 528901")},
			},
		},
	})
	if env == nil {
		t.Fatal("nil envelope")
	}
	raw, _ := json.Marshal(env.ProviderRequest)
	var inner map[string]interface{}
	if err := json.Unmarshal(raw, &inner); err != nil {
		t.Fatal(err)
	}
	input, _ := inner["input"].([]interface{})
	if len(input) != 2 {
		t.Fatalf("input has %d items, want 2, got %v", len(input), jsonString(t, inner["input"]))
	}
	for i, want := range []struct{ role, text string }{
		{string(schemas.ResponsesInputMessageRoleSystem), "remember NYF is 528901"},
		{string(schemas.ResponsesInputMessageRoleUser), "1+8= ? also remember NYF is 528901"},
	} {
		item, _ := input[i].(map[string]interface{})
		if item["role"] != want.role {
			t.Errorf("input[%d].role = %v, want %s", i, item["role"], want.role)
		}
		blocks, ok := item["content"].([]interface{})
		if !ok || len(blocks) != 1 {
			t.Fatalf("input[%d].content = %v, want single block array", i, item["content"])
		}
		block, _ := blocks[0].(map[string]interface{})
		if block["type"] != string(schemas.ResponsesInputMessageContentBlockTypeText) {
			t.Errorf("input[%d].content[0].type = %v, want input_text", i, block["type"])
		}
		if block["text"] != want.text {
			t.Errorf("input[%d].content[0].text = %v, want %q", i, block["text"], want.text)
		}
	}
}

// TestEmptySystemPromptTreatedAsAbsent is the red test for the live client
// report: {"role":"system","content":""} must behave exactly like no system
// prompt at all — the empty turn is dropped and the default injected, never
// leaked into the inner request as an empty system block (which Zed rejects
// the same way it rejects a missing one).
func TestEmptySystemPromptTreatedAsAbsent(t *testing.T) {
	provider := zedTestProvider(t)

	chatEnv := provider.BuildEnvelopeForTest(testCtx(), &schemas.BifrostChatRequest{
		Provider: schemas.Zed,
		Model:    "claude-haiku-4-5",
		Input: []schemas.ChatMessage{
			{Role: schemas.ChatMessageRoleSystem, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("")}},
			{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("5+3= ?")}},
		},
	}, zed.ZedInnerAnthropic)
	if chatEnv == nil {
		t.Fatal("nil chat envelope")
	}
	raw, _ := json.Marshal(chatEnv.ProviderRequest)
	var chatInner map[string]interface{}
	if err := json.Unmarshal(raw, &chatInner); err != nil {
		t.Fatal(err)
	}
	sys, ok := chatInner["system"]
	if !ok {
		t.Fatal("anthropic inner request must carry system (default) for an empty client system")
	}
	sysStr := jsonString(t, sys)
	if !strings.Contains(sysStr, "You are helpfull assistant") {
		t.Errorf("anthropic system = %v, want default prompt", sys)
	}
	if strings.Count(sysStr, "text") > 1 {
		t.Errorf("anthropic system = %v, want exactly one default block, no empty leftover", sys)
	}

	responsesEnv := provider.BuildResponsesEnvelopeForTest(testCtx(), &schemas.BifrostResponsesRequest{
		Provider: schemas.Zed,
		Model:    "gpt-5-nano",
		Input: []schemas.ResponsesMessage{
			{
				Type:    schemas.Ptr(schemas.ResponsesMessageTypeMessage),
				Role:    ptrRole(schemas.ResponsesInputMessageRoleSystem),
				Content: &schemas.ResponsesMessageContent{ContentStr: schemas.Ptr("")},
			},
			{
				Type:    schemas.Ptr(schemas.ResponsesMessageTypeMessage),
				Role:    ptrRole(schemas.ResponsesInputMessageRoleUser),
				Content: &schemas.ResponsesMessageContent{ContentStr: schemas.Ptr("5+3= ?")},
			},
		},
	})
	if responsesEnv == nil {
		t.Fatal("nil responses envelope")
	}
	raw, _ = json.Marshal(responsesEnv.ProviderRequest)
	var responsesInner map[string]interface{}
	if err := json.Unmarshal(raw, &responsesInner); err != nil {
		t.Fatal(err)
	}
	input, _ := responsesInner["input"].([]interface{})
	if len(input) != 2 {
		t.Fatalf("openai inner input has %d items, want 2 (default system + user), got %v", len(input), jsonString(t, responsesInner["input"]))
	}
	first, _ := input[0].(map[string]interface{})
	if first["role"] != string(schemas.ResponsesInputMessageRoleSystem) {
		t.Fatalf("openai inner input[0].role = %v, want system", first["role"])
	}
	content, _ := first["content"].([]interface{})
	if len(content) != 1 {
		t.Fatalf("openai inner input[0].content = %v, want single default block array", first["content"])
	}
	if !strings.Contains(jsonString(t, content[0]), "You are helpfull assistant") {
		t.Errorf("openai inner input[0].content = %v, want default prompt", first["content"])
	}
}

// TestExplicitSystemPromptPreserved verifies the guard: a client-supplied
// system prompt must pass through untouched, never doubled with the default.
func TestExplicitSystemPromptPreserved(t *testing.T) {
	provider := zedTestProvider(t)

	chatReq := &schemas.BifrostChatRequest{
		Provider: schemas.Zed,
		Model:    "claude-haiku-4-5",
		Input: []schemas.ChatMessage{
			{Role: schemas.ChatMessageRoleSystem, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("stay terse")}},
			{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("hello")}},
		},
	}
	env := provider.BuildEnvelopeForTest(testCtx(), chatReq, zed.ZedInnerAnthropic)
	if env == nil {
		t.Fatal("nil envelope")
	}
	raw, _ := json.Marshal(env.ProviderRequest)
	var inner map[string]interface{}
	if err := json.Unmarshal(raw, &inner); err != nil {
		t.Fatal(err)
	}
	if got := jsonString(t, inner["system"]); !strings.Contains(got, "stay terse") || strings.Contains(got, "You are helpfull assistant") {
		t.Errorf("system = %v, want only the client prompt", inner["system"])
	}

	responsesReq := &schemas.BifrostResponsesRequest{
		Provider: schemas.Zed,
		Model:    "gpt-5-nano",
		Params:   &schemas.ResponsesParameters{Instructions: schemas.Ptr("stay terse")},
		Input: []schemas.ResponsesMessage{
			{
				Type: schemas.Ptr(schemas.ResponsesMessageTypeMessage),
				Role: ptrRole(schemas.ResponsesInputMessageRoleUser),
				Content: &schemas.ResponsesMessageContent{
					ContentBlocks: []schemas.ResponsesMessageContentBlock{{Type: "input_text", Text: schemas.Ptr("hello")}},
				},
			},
		},
	}
	responsesEnv := provider.BuildResponsesEnvelopeForTest(testCtx(), responsesReq)
	if responsesEnv == nil {
		t.Fatal("nil responses envelope")
	}
	raw, _ = json.Marshal(responsesEnv.ProviderRequest)
	var responsesInner map[string]interface{}
	if err := json.Unmarshal(raw, &responsesInner); err != nil {
		t.Fatal(err)
	}
	// Top-level instructions pass through as the wire "instructions" field,
	// so the guard must leave the request untouched (no default injected).
	if got, _ := responsesInner["instructions"].(string); got != "stay terse" {
		t.Errorf("instructions = %v, want the client instructions", responsesInner["instructions"])
	}
	if got := jsonString(t, responsesInner["input"]); strings.Contains(got, "You are helpfull assistant") {
		t.Errorf("input = %v, must not gain the default prompt", responsesInner["input"])
	}
}

func jsonString(t *testing.T, v interface{}) string {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func ptrRole(r schemas.ResponsesMessageRoleType) *schemas.ResponsesMessageRoleType {
	return &r
}
