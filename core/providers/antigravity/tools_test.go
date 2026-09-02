package antigravity

import (
	"encoding/json"
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
