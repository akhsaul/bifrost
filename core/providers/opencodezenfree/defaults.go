package opencodezenfree

import (
	"strings"

	"github.com/maximhq/bifrost/core/schemas"
)

// dummyChatFunctionTool builds one of the placeholder chat function tools
// (edit/read/shell) mirroring dummyFunctionTool in the Responses shape.
// Client tools always win; only missing names are filled in.
func dummyChatFunctionTool(name string) schemas.ChatTool {
	return schemas.ChatTool{
		Type: schemas.ChatToolTypeFunction,
		Function: &schemas.ChatToolFunction{
			Name:        name,
			Description: schemas.Ptr("deprecated function, don't use it!"),
			Parameters: &schemas.ToolFunctionParameters{
				Type: "object",
				Properties: schemas.NewOrderedMapFromPairs(
					schemas.KV("deprecated", map[string]any{"type": "string"}),
				),
				Required:             []string{"deprecated"},
				AdditionalProperties: &schemas.AdditionalPropertiesStruct{AdditionalPropertiesBool: schemas.Ptr(false)},
			},
			Strict: schemas.Ptr(false),
		},
	}
}

// chatToolName reports the callable name of a chat tool, or "" when the tool
// carries no name (server-tool variants name themselves via Type).
func chatToolName(tool schemas.ChatTool) string {
	if tool.Function != nil && strings.TrimSpace(tool.Function.Name) != "" {
		return tool.Function.Name
	}
	if strings.TrimSpace(tool.Name) != "" {
		return tool.Name
	}
	return ""
}

// hasChatSystemPrompt reports whether the chat input already carries a
// system-role (or developer-role) message, i.e. the client supplied its own
// system prompt.
func hasChatSystemPrompt(input []schemas.ChatMessage) bool {
	for _, msg := range input {
		if msg.Role == schemas.ChatMessageRoleSystem || msg.Role == schemas.ChatMessageRoleDeveloper {
			return true
		}
	}
	return false
}

// ensureOpencodeZenFreeChatDefaults fills chat-shaped body fields to match the
// working opencode capture. Client-supplied values always win; defaults apply
// only to fields the client left unset:
//   - a leading system message (DefaultInstructions) when no system prompt exists
//   - a dummy placeholder for each of edit/read/shell the client did not send
//   - store=false when unset
func ensureOpencodeZenFreeChatDefaults(request *schemas.BifrostChatRequest) {
	if request == nil {
		return
	}
	if !hasChatSystemPrompt(request.Input) {
		request.Input = append([]schemas.ChatMessage{{
			Role:    schemas.ChatMessageRoleSystem,
			Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr(DefaultInstructions)},
		}}, request.Input...)
	}
	if request.Params == nil {
		request.Params = &schemas.ChatParameters{}
	}
	if request.Params.Store == nil {
		request.Params.Store = schemas.Ptr(false)
	}
	present := make(map[string]bool, len(request.Params.Tools))
	for _, tool := range request.Params.Tools {
		if name := chatToolName(tool); name != "" {
			present[name] = true
		}
	}
	for _, name := range requiredOpencodeZenFreeToolNames {
		if !present[name] {
			request.Params.Tools = append(request.Params.Tools, dummyChatFunctionTool(name))
		}
	}
}
