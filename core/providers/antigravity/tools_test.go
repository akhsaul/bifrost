package antigravity

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	schemas "github.com/maximhq/bifrost/core/schemas"
)

// RED test: Antigravity's /v1internal:generateContent endpoint rejects
// parametersJsonSchema (plain JSON Schema) — it only accepts the Gemini
// proto Schema form under "parameters", exactly like the agy CLI sends
// (see captured data-agy-gemini.json: "parameters":{"type":"OBJECT",...}).
func TestToAntigravityChatRequest_ToolsUseProtoParameters(t *testing.T) {
	toolJSON := `{
		"type": "function",
		"function": {
			"name": "view_file",
			"description": "View a file",
			"parameters": {
				"type": "object",
				"properties": {
					"path": {"type": "string", "description": "Path to file"},
					"opts": {
						"type": "object",
						"properties": {"limit": {"type": "integer"}},
						"required": ["limit"]
					}
				},
				"required": ["path"]
			}
		}
	}`

	var chatTool schemas.ChatTool
	if err := json.Unmarshal([]byte(toolJSON), &chatTool); err != nil {
		t.Fatalf("unmarshal tool: %v", err)
	}

	bifrostReq := &schemas.BifrostChatRequest{
		Model: "gemini-3.6-flash-high",
		Input: []schemas.ChatMessage{
			{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: strPtr("hi")}},
		},
		Params: &schemas.ChatParameters{
			Tools: []schemas.ChatTool{chatTool},
		},
	}

	ctx := schemas.NewBifrostContext(nil, schemas.NoDeadline)
	envelope, jsonBytes, err := ToAntigravityChatRequest(ctx, bifrostReq, "proj-123")
	if err != nil {
		t.Fatalf("ToAntigravityChatRequest failed: %v", err)
	}
	if envelope == nil || len(jsonBytes) == 0 {
		t.Fatal("expected envelope and jsonBytes")
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(jsonBytes, &raw); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}

	// The wire body must contain NO parametersJsonSchema anywhere.
	var assertNoParamsJSONSchema func(node interface{}) bool
	assertNoParamsJSONSchema = func(node interface{}) bool {
		switch v := node.(type) {
		case map[string]interface{}:
			if _, exists := v["parametersJsonSchema"]; exists {
				return false
			}
			for _, child := range v {
				if !assertNoParamsJSONSchema(child) {
					return false
				}
			}
		case []interface{}:
			for _, child := range v {
				if !assertNoParamsJSONSchema(child) {
					return false
				}
			}
		}
		return true
	}
	var body map[string]interface{}
	if err := json.Unmarshal(jsonBytes, &body); err != nil {
		t.Fatalf("unmarshal body: %v", err)
	}
	if !assertNoParamsJSONSchema(body) {
		t.Errorf("wire body must not contain parametersJsonSchema; got: %s", string(jsonBytes))
	}

	// Every function declaration must use proto "parameters" with UPPERCASE types.
	var wire struct {
		Request struct {
			Tools []struct {
				FunctionDeclarations []struct {
					Name       string          `json:"name"`
					Parameters json.RawMessage `json:"parameters"`
				} `json:"functionDeclarations"`
			} `json:"tools"`
		} `json:"request"`
	}
	if err := json.Unmarshal(jsonBytes, &wire); err != nil {
		t.Fatalf("unmarshal tools: %v", err)
	}

	var fds int
	for _, tool := range wire.Request.Tools {
		for _, fd := range tool.FunctionDeclarations {
			fds++
			if fd.Name != "view_file" {
				t.Errorf("unexpected function name %q", fd.Name)
			}
			if len(fd.Parameters) == 0 {
				t.Fatalf("functionDeclaration.parameters must be set (proto Schema), got empty for %s", fd.Name)
			}
			var params struct {
				Type       string   `json:"type"`
				Required   []string `json:"required"`
				Properties map[string]struct {
					Type string `json:"type"`
				} `json:"properties"`
			}
			if err := json.Unmarshal(fd.Parameters, &params); err != nil {
				t.Fatalf("unmarshal parameters: %v", err)
			}
			if params.Type != "OBJECT" {
				t.Errorf("parameters.type = %q, want UPPERCASE proto form OBJECT", params.Type)
			}
			if len(params.Required) == 0 || params.Required[0] != "path" {
				t.Errorf("parameters.required = %v, want [path]", params.Required)
			}
			if params.Properties["path"].Type != "STRING" {
				t.Errorf("parameters.properties.path.type = %q, want STRING", params.Properties["path"].Type)
			}
			if params.Properties["opts"].Type != "OBJECT" {
				t.Errorf("parameters.properties.opts.type = %q, want OBJECT (nested conversion)", params.Properties["opts"].Type)
			}
		}
	}
	if fds == 0 {
		t.Fatal("expected at least one function declaration in wire body")
	}
}

func strPtr(s string) *string { return &s }

func TestToAntigravityChatRequest_GoogleSearchTool(t *testing.T) {
	bifrostReq := &schemas.BifrostChatRequest{
		Model: "gemini-3.1-flash-lite",
		Input: []schemas.ChatMessage{
			{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: strPtr("apa itu atria dawn ?")}},
		},
		Params: &schemas.ChatParameters{
			Tools: []schemas.ChatTool{
				{Type: "google_search"},
			},
		},
	}

	ctx := schemas.NewBifrostContext(nil, schemas.NoDeadline)
	envelope, jsonBytes, err := ToAntigravityChatRequest(ctx, bifrostReq, "aicode-consumers")
	if err != nil {
		t.Fatalf("ToAntigravityChatRequest failed: %v", err)
	}

	_ = jsonBytes
	if envelope.RequestType != "web_search" {
		t.Errorf("expected requestType web_search, got %q", envelope.RequestType)
	}
	if envelope.RequestID != "" {
		t.Errorf("expected empty requestID for web_search, got %q", envelope.RequestID)
	}
	if envelope.Request.SessionID != "" {
		t.Errorf("expected empty sessionID for web_search, got %q", envelope.Request.SessionID)
	}
	if envelope.Request.ToolConfig != nil {
		t.Errorf("expected nil ToolConfig for antigravity, got %+v", envelope.Request.ToolConfig)
	}
	if len(envelope.Request.Tools) == 0 || envelope.Request.Tools[0].GoogleSearch == nil {
		t.Fatalf("expected googleSearch tool in request, got %+v", envelope.Request.Tools)
	}
	gs := envelope.Request.Tools[0].GoogleSearch
	if gs.EnhancedContent == nil || gs.EnhancedContent.ImageSearch == nil || gs.EnhancedContent.ImageSearch.MaxResultCount != 5 {
		t.Errorf("expected enhancedContent.imageSearch.maxResultCount = 5, got %+v", gs.EnhancedContent)
	}
	if envelope.Request.GenerationConfig == nil || envelope.Request.GenerationConfig.CandidateCount != 1 {
		t.Errorf("expected candidateCount 1, got %+v", envelope.Request.GenerationConfig)
	}
	if envelope.Request.SystemInstruction == nil || envelope.Request.SystemInstruction.Role != "user" {
		t.Errorf("expected systemInstruction with role user, got %+v", envelope.Request.SystemInstruction)
	}

	// Verify wire JSON has no toolConfig or sessionId or requestId
	var wireMap map[string]interface{}
	if err := json.Unmarshal(jsonBytes, &wireMap); err != nil {
		t.Fatalf("unmarshal wire json: %v", err)
	}
	if _, exists := wireMap["requestId"]; exists {
		t.Errorf("wire JSON must not have requestId for web_search: %s", string(jsonBytes))
	}
	reqObj, _ := wireMap["request"].(map[string]interface{})
	if _, exists := reqObj["sessionId"]; exists {
		t.Errorf("wire JSON request must not have sessionId for web_search: %s", string(jsonBytes))
	}
	if _, exists := reqObj["toolConfig"]; exists {
		t.Errorf("wire JSON must not have toolConfig: %s", string(jsonBytes))
	}
	if _, exists := reqObj["tool_config"]; exists {
		t.Errorf("wire JSON must not have tool_config: %s", string(jsonBytes))
	}
}

func TestToAntigravityChatRequest_GoogleSearchTool_OverrideMaxResults(t *testing.T) {
	bifrostReq := &schemas.BifrostChatRequest{
		Model: "gemini-3.1-flash-lite",
		Input: []schemas.ChatMessage{
			{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: strPtr("test query")}},
		},
		Params: &schemas.ChatParameters{
			Tools: []schemas.ChatTool{
				{Type: "google_search"},
			},
			ExtraParams: map[string]interface{}{
				"max_result_count": 8,
			},
		},
	}

	ctx := schemas.NewBifrostContext(nil, schemas.NoDeadline)
	envelope, _, err := ToAntigravityChatRequest(ctx, bifrostReq, "aicode-consumers")
	if err != nil {
		t.Fatalf("ToAntigravityChatRequest failed: %v", err)
	}

	if len(envelope.Request.Tools) == 0 || envelope.Request.Tools[0].GoogleSearch == nil {
		t.Fatalf("expected googleSearch tool, got %+v", envelope.Request.Tools)
	}
	gs := envelope.Request.Tools[0].GoogleSearch
	if gs.EnhancedContent == nil || gs.EnhancedContent.ImageSearch == nil || gs.EnhancedContent.ImageSearch.MaxResultCount != 8 {
		t.Errorf("expected enhancedContent.imageSearch.maxResultCount = 8, got %+v", gs.EnhancedContent)
	}
}

func TestToAntigravityChatRequest_MixedTools_DropsGoogleSearch(t *testing.T) {
	toolJSON := `{
		"type": "function",
		"function": {
			"name": "get_weather",
			"description": "Get weather",
			"parameters": {"type": "object", "properties": {"city": {"type": "string"}}}
		}
	}`
	var chatTool schemas.ChatTool
	if err := json.Unmarshal([]byte(toolJSON), &chatTool); err != nil {
		t.Fatalf("unmarshal tool: %v", err)
	}

	bifrostReq := &schemas.BifrostChatRequest{
		Model: "gemini-3.1-flash-lite",
		Input: []schemas.ChatMessage{
			{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: strPtr("weather in Tokyo")}},
		},
		Params: &schemas.ChatParameters{
			Tools: []schemas.ChatTool{
				chatTool,
				{Type: "google_search"},
			},
		},
	}

	ctx := schemas.NewBifrostContext(nil, schemas.NoDeadline)
	envelope, jsonBytes, err := ToAntigravityChatRequest(ctx, bifrostReq, "aicode-consumers")
	if err != nil {
		t.Fatalf("ToAntigravityChatRequest failed: %v", err)
	}

	if envelope.RequestType != "agent" {
		t.Errorf("expected mixed tools to fall back to agent, got %q", envelope.RequestType)
	}
	// GoogleSearch must be dropped to prevent 400
	for _, tool := range envelope.Request.Tools {
		if tool.GoogleSearch != nil {
			t.Errorf("expected GoogleSearch to be dropped when function tools present, got %+v", tool)
		}
	}
	// ToolConfig must NEVER be emitted
	var wireMap map[string]interface{}
	if err := json.Unmarshal(jsonBytes, &wireMap); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	reqObj, _ := wireMap["request"].(map[string]interface{})
	if _, exists := reqObj["toolConfig"]; exists {
		t.Errorf("wire JSON must not have toolConfig: %s", string(jsonBytes))
	}
}

func TestParseAntigravitySearchResponse(t *testing.T) {
	respData, err := os.ReadFile("/mnt/data/Projects/bifrost/agy-resp-search.json")
	if err != nil {
		t.Fatalf("failed to read agy-resp-search.json: %v", err)
	}

	genResp, err := parseAntigravityChunk(respData)
	if err != nil {
		t.Fatalf("parseAntigravityChunk failed: %v", err)
	}
	if genResp == nil {
		t.Fatal("expected non-nil GenerateContentResponse")
	}

	chatResp := genResp.ToBifrostChatResponse()
	if chatResp == nil {
		t.Fatal("expected non-nil BifrostChatResponse")
	}

	if len(chatResp.Choices) == 0 {
		t.Fatal("expected at least 1 choice")
	}
	choice := chatResp.Choices[0]
	if choice.Message == nil || choice.Message.Content == nil || choice.Message.Content.ContentStr == nil {
		t.Fatal("expected text content in choice message")
	}
	text := *choice.Message.Content.ContentStr
	if !strings.Contains(text, "Atria Dawn") {
		t.Errorf("expected text to mention Atria Dawn, got %s", text)
	}

	if choice.Message.ChatAssistantMessage == nil || len(choice.Message.ChatAssistantMessage.Annotations) == 0 {
		t.Fatal("expected URL annotations from groundingMetadata")
	}
	foundOrCa := false
	for _, ann := range choice.Message.ChatAssistantMessage.Annotations {
		if ann.URLCitation.Title == "orcarouter.ai" {
			foundOrCa = true
			break
		}
	}
	if !foundOrCa {
		t.Errorf("expected annotation with title orcarouter.ai, got %+v", choice.Message.ChatAssistantMessage.Annotations)
	}
}

func TestToAntigravityResponsesRequest_WebSearchTool(t *testing.T) {
	bifrostReq := &schemas.BifrostResponsesRequest{
		Model: "gemini-3.1-flash-lite",
		Input: []schemas.ResponsesMessage{
			{
				Role: schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
				Content: &schemas.ResponsesMessageContent{
					ContentStr: strPtr("what is atria dawn?"),
				},
			},
		},
		Params: &schemas.ResponsesParameters{
			Tools: []schemas.ResponsesTool{
				{Type: schemas.ResponsesToolTypeWebSearch},
			},
		},
	}

	ctx := schemas.NewBifrostContext(nil, schemas.NoDeadline)
	envelope, jsonBytes, err := ToAntigravityResponsesRequest(ctx, bifrostReq, "aicode-consumers")
	if err != nil {
		t.Fatalf("ToAntigravityResponsesRequest failed: %v", err)
	}

	if envelope.RequestType != "web_search" {
		t.Errorf("expected requestType web_search, got %q", envelope.RequestType)
	}
	if len(envelope.Request.Tools) == 0 || envelope.Request.Tools[0].GoogleSearch == nil {
		t.Fatalf("expected googleSearch tool, got %+v", envelope.Request.Tools)
	}
	gs := envelope.Request.Tools[0].GoogleSearch
	if gs.EnhancedContent == nil || gs.EnhancedContent.ImageSearch == nil || gs.EnhancedContent.ImageSearch.MaxResultCount != 5 {
		t.Errorf("expected enhancedContent.imageSearch.maxResultCount = 5, got %+v", gs.EnhancedContent)
	}
	if envelope.Request.GenerationConfig == nil || envelope.Request.GenerationConfig.CandidateCount != 1 {
		t.Errorf("expected candidateCount 1, got %+v", envelope.Request.GenerationConfig)
	}

	// Verify wire JSON
	var wireMap map[string]interface{}
	if err := json.Unmarshal(jsonBytes, &wireMap); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, exists := wireMap["requestId"]; exists {
		t.Errorf("wire JSON must not have requestId for web_search: %s", string(jsonBytes))
	}
	reqObj, _ := wireMap["request"].(map[string]interface{})
	if _, exists := reqObj["sessionId"]; exists {
		t.Errorf("wire JSON request must not have sessionId for web_search: %s", string(jsonBytes))
	}
}

func TestToAntigravityChatRequest_WebSearchOptions(t *testing.T) {
	bifrostReq := &schemas.BifrostChatRequest{
		Model: "gemini-3.1-flash-lite",
		Input: []schemas.ChatMessage{
			{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: strPtr("apa itu atria dawn ?")}},
		},
		Params: &schemas.ChatParameters{
			WebSearchOptions: &schemas.ChatWebSearchOptions{},
		},
	}

	ctx := schemas.NewBifrostContext(nil, schemas.NoDeadline)
	envelope, _, err := ToAntigravityChatRequest(ctx, bifrostReq, "aicode-consumers")
	if err != nil {
		t.Fatalf("ToAntigravityChatRequest failed: %v", err)
	}

	if envelope.RequestType != "web_search" {
		t.Errorf("expected requestType web_search, got %q", envelope.RequestType)
	}
	if len(envelope.Request.Tools) == 0 || envelope.Request.Tools[0].GoogleSearch == nil {
		t.Fatalf("expected googleSearch tool, got %+v", envelope.Request.Tools)
	}
	gs := envelope.Request.Tools[0].GoogleSearch
	if gs.EnhancedContent == nil || gs.EnhancedContent.ImageSearch == nil || gs.EnhancedContent.ImageSearch.MaxResultCount != 5 {
		t.Errorf("expected enhancedContent.imageSearch.maxResultCount = 5, got %+v", gs.EnhancedContent)
	}
}
