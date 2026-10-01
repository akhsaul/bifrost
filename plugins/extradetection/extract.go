package extradetection

import (
	"strings"

	"github.com/maximhq/bifrost/core/schemas"
)

// collectUserTexts returns user-role message texts, most recent first. Only
// user-role messages are included: assistant turns, tool calls, tool results,
// function outputs, and system/developer prompts are skipped and never
// counted toward MaxMessagesToScan. Empty texts are dropped.
func collectUserTexts(req *schemas.BifrostRequest) []string {
	if req == nil {
		return nil
	}
	switch {
	case req.ChatRequest != nil:
		return collectChatUserTexts(req.ChatRequest.Input)
	case req.ResponsesRequest != nil:
		return collectResponsesUserTexts(req.ResponsesRequest.Input, responsesInstructions(req))
	case req.TextCompletionRequest != nil:
		if req.TextCompletionRequest.Input != nil && req.TextCompletionRequest.Input.PromptStr != nil {
			if text := strings.TrimSpace(*req.TextCompletionRequest.Input.PromptStr); text != "" {
				return []string{text}
			}
		}
	}
	return nil
}

// collectFullInputText returns all human-authored request text joined for the
// "full_input" scope: system/developer instructions plus every user message in
// conversation order. Assistant and tool content is excluded.
func collectFullInputText(req *schemas.BifrostRequest) string {
	if req == nil {
		return ""
	}
	var parts []string
	switch {
	case req.ChatRequest != nil:
		for _, msg := range req.ChatRequest.Input {
			switch msg.Role {
			case schemas.ChatMessageRoleSystem, schemas.ChatMessageRoleDeveloper:
				if text := chatMessageText(msg.Content); strings.TrimSpace(text) != "" {
					parts = append(parts, text)
				}
			case schemas.ChatMessageRoleUser:
				if text := chatMessageText(msg.Content); strings.TrimSpace(text) != "" {
					parts = append(parts, text)
				}
			}
		}
	case req.ResponsesRequest != nil:
		if instructions := responsesInstructions(req); strings.TrimSpace(instructions) != "" {
			parts = append(parts, instructions)
		}
		for _, text := range collectResponsesUserTextsOldestFirst(req.ResponsesRequest.Input) {
			parts = append(parts, text)
		}
	case req.TextCompletionRequest != nil:
		if req.TextCompletionRequest.Input != nil && req.TextCompletionRequest.Input.PromptStr != nil {
			parts = append(parts, *req.TextCompletionRequest.Input.PromptStr)
		}
	}
	return strings.Join(parts, "\n")
}

// collectTokenTexts returns every text fragment that counts toward the token
// estimate: system/developer instructions, all user messages, and assistant
// text (assistant output shapes the provider's next-turn context window usage).
// Tool payloads are excluded — they are machine-generated bulk that would
// inflate the estimate without reflecting billable prompt shape.
func collectTokenTexts(req *schemas.BifrostRequest) []string {
	if req == nil {
		return nil
	}
	var parts []string
	switch {
	case req.ChatRequest != nil:
		for _, msg := range req.ChatRequest.Input {
			switch msg.Role {
			case schemas.ChatMessageRoleSystem, schemas.ChatMessageRoleDeveloper,
				schemas.ChatMessageRoleUser, schemas.ChatMessageRoleAssistant:
				if text := chatMessageText(msg.Content); text != "" {
					parts = append(parts, text)
				}
			}
		}
	case req.ResponsesRequest != nil:
		if instructions := responsesInstructions(req); instructions != "" {
			parts = append(parts, instructions)
		}
		for _, msg := range req.ResponsesRequest.Input {
			if msg.Role == nil {
				continue
			}
			switch *msg.Role {
			case schemas.ResponsesInputMessageRoleSystem, schemas.ResponsesInputMessageRoleDeveloper,
				schemas.ResponsesInputMessageRoleUser, schemas.ResponsesInputMessageRoleAssistant:
				if text := responsesMessageText(msg.Content); text != "" {
					parts = append(parts, text)
				}
			}
		}
	case req.TextCompletionRequest != nil:
		if req.TextCompletionRequest.Input != nil && req.TextCompletionRequest.Input.PromptStr != nil {
			parts = append(parts, *req.TextCompletionRequest.Input.PromptStr)
		}
	}
	return parts
}

func collectChatUserTexts(messages []schemas.ChatMessage) []string {
	var out []string
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role != schemas.ChatMessageRoleUser {
			continue
		}
		if text := chatMessageText(messages[i].Content); strings.TrimSpace(text) != "" {
			out = append(out, text)
		}
	}
	return out
}

func chatMessageText(content *schemas.ChatMessageContent) string {
	if content == nil {
		return ""
	}
	if content.ContentStr != nil {
		return *content.ContentStr
	}
	var parts []string
	for _, block := range content.ContentBlocks {
		switch block.Type {
		case schemas.ChatContentBlockTypeText:
			if block.Text != nil && *block.Text != "" {
				parts = append(parts, *block.Text)
			}
		case schemas.ChatContentBlockTypeRefusal:
			if block.Refusal != nil && *block.Refusal != "" {
				parts = append(parts, *block.Refusal)
			}
		}
	}
	return strings.Join(parts, "\n")
}

func collectResponsesUserTexts(messages []schemas.ResponsesMessage, _ string) []string {
	var out []string
	for i := len(messages) - 1; i >= 0; i-- {
		msg := messages[i]
		if msg.Role == nil || *msg.Role != schemas.ResponsesInputMessageRoleUser {
			continue
		}
		if text := responsesMessageText(msg.Content); strings.TrimSpace(text) != "" {
			out = append(out, text)
		}
	}
	return out
}

func collectResponsesUserTextsOldestFirst(messages []schemas.ResponsesMessage) []string {
	var out []string
	for _, msg := range messages {
		if msg.Role == nil || *msg.Role != schemas.ResponsesInputMessageRoleUser {
			continue
		}
		if text := responsesMessageText(msg.Content); strings.TrimSpace(text) != "" {
			out = append(out, text)
		}
	}
	return out
}

func responsesMessageText(content *schemas.ResponsesMessageContent) string {
	if content == nil {
		return ""
	}
	if content.ContentStr != nil {
		return *content.ContentStr
	}
	var parts []string
	for _, block := range content.ContentBlocks {
		switch block.Type {
		case schemas.ResponsesInputMessageContentBlockTypeText,
			schemas.ResponsesOutputMessageContentTypeText,
			schemas.ResponsesOutputMessageContentTypeReasoning,
			schemas.ResponsesOutputMessageContentTypeSummaryText:
			if block.Text != nil && *block.Text != "" {
				parts = append(parts, *block.Text)
			}
		case schemas.ResponsesOutputMessageContentTypeRefusal:
			if block.ResponsesOutputMessageContentRefusal != nil &&
				block.ResponsesOutputMessageContentRefusal.Refusal != "" {
				parts = append(parts, block.ResponsesOutputMessageContentRefusal.Refusal)
			}
		}
	}
	return strings.Join(parts, "\n")
}

func responsesInstructions(req *schemas.BifrostRequest) string {
	if req == nil || req.ResponsesRequest == nil || req.ResponsesRequest.Params == nil ||
		req.ResponsesRequest.Params.Instructions == nil {
		return ""
	}
	return *req.ResponsesRequest.Params.Instructions
}
