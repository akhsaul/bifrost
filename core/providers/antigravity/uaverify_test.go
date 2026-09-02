package antigravity

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	schemas "github.com/maximhq/bifrost/core/schemas"
)

// TestUAVerifyCapturedPayload runs the REAL captured Hermes request payload
// (data-bifrost-agy.json, root) through ToAntigravityChatRequest and verifies
// the wire body carries zero parametersJsonSchema and proto UPPERCASE params.
func TestUAVerifyCapturedPayload(t *testing.T) {
	raw, err := os.ReadFile("../../../data-bifrost-agy.json")
	if err != nil {
		t.Skipf("captured payload not present: %v", err)
	}

	var captured struct {
		Request struct {
			Contents []interface{}     `json:"contents"`
			Tools    []json.RawMessage `json:"tools"`
		} `json:"request"`
		Model string `json:"model"`
	}
	if err := json.Unmarshal(raw, &captured); err != nil {
		t.Fatalf("unmarshal captured: %v", err)
	}

	// Rebuild a Bifrost request from the captured Gemini-shaped pieces:
	// contents (user text) + tools (already Gemini functionDeclarations form).
	var contents []schemas.ChatMessage
	for _, c := range captured.Request.Contents {
		b, _ := json.Marshal(c)
		var cm struct {
			Role  string `json:"role"`
			Parts []struct {
				Text string `json:"text"`
			} `json:"parts"`
		}
		if err := json.Unmarshal(b, &cm); err != nil || len(cm.Parts) == 0 {
			continue
		}
		role := schemas.ChatMessageRoleUser
		if cm.Role == "model" {
			role = schemas.ChatMessageRoleAssistant
		}
		contents = append(contents, schemas.ChatMessage{
			Role:    role,
			Content: &schemas.ChatMessageContent{ContentStr: &cm.Parts[0].Text},
		})
	}

	var tools []schemas.ChatTool
	for _, tr := range captured.Request.Tools {
		var gt struct {
			FunctionDeclarations []struct {
				Name             string          `json:"name"`
				Description      string          `json:"description"`
				RawParameters    json.RawMessage `json:"parameters"`
				RawParamsJSONSch json.RawMessage `json:"parametersJsonSchema"`
			} `json:"functionDeclarations"`
		}
		if err := json.Unmarshal(tr, &gt); err != nil {
			t.Fatalf("unmarshal captured tool: %v", err)
		}
		for _, fd := range gt.FunctionDeclarations {
			paramsJSON := fd.RawParamsJSONSch
			if len(paramsJSON) == 0 {
				paramsJSON = fd.RawParameters
			}
			// Convert the Gemini-schema JSON into a JSON-Schema-shaped
			// ToolFunctionParameters via round trip (types will be UPPERCASE
			// from the capture; ToGemini converter tolerates that via passthrough).
			var tfp schemas.ToolFunctionParameters
			if len(paramsJSON) > 0 {
				if err := json.Unmarshal(paramsJSON, &tfp); err != nil {
					t.Fatalf("unmarshal params for %s: %v", fd.Name, err)
				}
			}
			desc := fd.Description
			tools = append(tools, schemas.ChatTool{
				Type: schemas.ChatToolTypeFunction,
				Function: &schemas.ChatToolFunction{
					Name:        fd.Name,
					Description: &desc,
					Parameters:  &tfp,
				},
			})
		}
	}

	if len(tools) == 0 {
		t.Fatal("no tools extracted from captured payload")
	}

	ctx := schemas.NewBifrostContext(nil, schemas.NoDeadline)
	_, jsonBytes, err := ToAntigravityChatRequest(ctx, &schemas.BifrostChatRequest{
		Model:  captured.Model,
		Input:  contents,
		Params: &schemas.ChatParameters{Tools: tools},
	}, "aicode-consumers")
	if err != nil {
		t.Fatalf("ToAntigravityChatRequest failed: %v", err)
	}

	body := string(jsonBytes)
	if strings.Contains(body, "parametersJsonSchema") {
		t.Fatalf("wire body still contains parametersJsonSchema")
	}
	if !strings.Contains(body, `"type":"OBJECT"`) {
		t.Fatalf("wire body missing proto OBJECT type; sample: %s", body[:min(400, len(body))])
	}

	// Count declarations converted
	var wire struct {
		Request struct {
			Tools []struct {
				FDs []struct {
					Name       string          `json:"name"`
					Parameters json.RawMessage `json:"parameters"`
				} `json:"functionDeclarations"`
			} `json:"tools"`
		} `json:"request"`
	}
	if err := json.Unmarshal(jsonBytes, &wire); err != nil {
		t.Fatalf("unmarshal wire: %v", err)
	}
	converted := 0
	for _, tool := range wire.Request.Tools {
		for _, fd := range tool.FDs {
			if len(fd.Parameters) == 0 {
				t.Errorf("tool %s has empty parameters", fd.Name)
				continue
			}
			if !strings.Contains(string(fd.Parameters), `"type":"OBJECT"`) {
				t.Errorf("tool %s parameters not OBJECT proto form: %s", fd.Name, string(fd.Parameters)[:min(200, len(fd.Parameters))])
			}
			converted++
		}
	}
	t.Logf("converted %d/%d tool declarations to proto Schema form", converted, len(tools))
	if converted != len(tools) {
		t.Errorf("expected all %d tools converted, got %d", len(tools), converted)
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
