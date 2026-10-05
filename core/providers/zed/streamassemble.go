package zed

import (
	"strconv"
	"strings"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/schemas"
)

// itoa formats a small int without importing strconv at the call site.
func itoa(i int) string { return strconv.Itoa(i) }

// feedOpenAIEvent decodes one inner OpenAI Responses stream event and folds
// it into the accumulator. OpenAI events map to Responses stream responses,
// which convert losslessly to chat deltas via ToBifrostChatResponse.
func (a *chatAccumulator) feedOpenAIEvent(eventBytes []byte) *schemas.BifrostError {
	var streamResp schemas.BifrostResponsesStreamResponse
	if err := sonic.Unmarshal(eventBytes, &streamResp); err != nil {
		return nil // skip undecodable events; the terminal marker still ends the stream
	}
	if streamResp.Type == schemas.ResponsesStreamResponseTypeError {
		return responsesStreamError(&streamResp)
	}
	delta := streamResp.ToBifrostChatResponse()
	if delta == nil {
		return nil
	}
	a.observeChatDelta(delta)
	if streamResp.Type == schemas.ResponsesStreamResponseTypeCompleted ||
		streamResp.Type == schemas.ResponsesStreamResponseTypeIncomplete {
		a.sawFamilyTerminal = true
	}
	return nil
}

// observeChatDelta records one converted chat delta: text/tool fragments are
// kept for assembly, usage accumulates, finish reason and message id stick.
func (a *chatAccumulator) observeChatDelta(delta *schemas.BifrostChatResponse) {
	if delta == nil {
		return
	}
	if delta.ID != "" && a.messageID == "" {
		a.messageID = delta.ID
	}
	if delta.Usage != nil {
		mergeUsage(a.usage, delta.Usage)
	}
	for i := range delta.Choices {
		if fr := delta.Choices[i].FinishReason; fr != nil && *fr != "" {
			a.finishReason = fr
		}
	}
	a.parts = append(a.parts, delta)
}

// mergeUsage folds one delta's usage into the running total, keeping the max
// of each counter (families report cumulative usage per event).
func mergeUsage(dst, src *schemas.BifrostLLMUsage) {
	if dst == nil || src == nil {
		return
	}
	if src.PromptTokens > dst.PromptTokens {
		dst.PromptTokens = src.PromptTokens
	}
	if src.CompletionTokens > dst.CompletionTokens {
		dst.CompletionTokens = src.CompletionTokens
	}
	if src.TotalTokens > dst.TotalTokens {
		dst.TotalTokens = src.TotalTokens
	}
	if calculated := dst.PromptTokens + dst.CompletionTokens; calculated > dst.TotalTokens {
		dst.TotalTokens = calculated
	}
	if src.PromptTokensDetails != nil {
		dst.PromptTokensDetails = src.PromptTokensDetails
	}
	if src.CompletionTokensDetails != nil {
		dst.CompletionTokensDetails = src.CompletionTokensDetails
	}
	if src.Cost != nil {
		dst.Cost = src.Cost
	}
}

// assembleChatParts merges converted deltas into one chat response: text
// concatenates, tool calls union by id, reasoning details append.
func assembleChatParts(parts []*schemas.BifrostChatResponse, model string) *schemas.BifrostChatResponse {
	out := &schemas.BifrostChatResponse{
		Model:  model,
		Object: "chat.completion",
		Choices: []schemas.BifrostResponseChoice{
			{
				Index: 0,
				ChatNonStreamResponseChoice: &schemas.ChatNonStreamResponseChoice{
					Message: &schemas.ChatMessage{
						Role:    schemas.ChatMessageRoleAssistant,
						Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("")},
					},
				},
			},
		},
	}
	if len(parts) == 0 {
		return out
	}
	msg := out.Choices[0].ChatNonStreamResponseChoice.Message
	var text strings.Builder
	toolCallsByID := make(map[string]*schemas.ChatAssistantMessageToolCall)
	var toolCallOrder []string
	var reasoningDetails []schemas.ChatReasoningDetails
	for _, part := range parts {
		for _, choice := range part.Choices {
			var deltaText string
			var calls []schemas.ChatAssistantMessageToolCall
			var details []schemas.ChatReasoningDetails
			if choice.ChatStreamResponseChoice != nil && choice.ChatStreamResponseChoice.Delta != nil {
				d := choice.ChatStreamResponseChoice.Delta
				if d.Content != nil {
					deltaText = *d.Content
				}
				calls = d.ToolCalls
				details = d.ReasoningDetails
			} else if choice.ChatNonStreamResponseChoice != nil && choice.ChatNonStreamResponseChoice.Message != nil {
				m := choice.ChatNonStreamResponseChoice.Message
				if m.Content != nil {
					if m.Content.ContentStr != nil {
						deltaText = *m.Content.ContentStr
					} else {
						for _, b := range m.Content.ContentBlocks {
							if b.Text != nil {
								deltaText += *b.Text
							}
						}
					}
				}
				if m.ChatAssistantMessage != nil {
					calls = m.ChatAssistantMessage.ToolCalls
					details = m.ChatAssistantMessage.ReasoningDetails
				}
			}
			if deltaText != "" {
				text.WriteString(deltaText)
			}
			for _, tc := range calls {
				id := ""
				if tc.ID != nil {
					id = *tc.ID
				}
				if id == "" {
					id = "\x00" + itoa(len(toolCallOrder))
				}
				if existing, ok := toolCallsByID[id]; ok {
					mergeToolCall(existing, &tc)
				} else {
					cp := tc
					toolCallsByID[id] = &cp
					toolCallOrder = append(toolCallOrder, id)
				}
			}
			reasoningDetails = append(reasoningDetails, details...)
		}
	}
	combined := text.String()
	msg.Content = &schemas.ChatMessageContent{ContentStr: &combined}
	if len(toolCallOrder) > 0 {
		msg.ChatAssistantMessage = &schemas.ChatAssistantMessage{}
		for _, id := range toolCallOrder {
			tc := *toolCallsByID[id]
			if strings.HasPrefix(id, "\x00") {
				tc.ID = nil
			}
			msg.ChatAssistantMessage.ToolCalls = append(msg.ChatAssistantMessage.ToolCalls, tc)
		}
	}
	if len(reasoningDetails) > 0 {
		if msg.ChatAssistantMessage == nil {
			msg.ChatAssistantMessage = &schemas.ChatAssistantMessage{}
		}
		msg.ChatAssistantMessage.ReasoningDetails = reasoningDetails
	}
	return out
}

// mergeToolCall accumulates one streaming tool-call fragment into its target:
// argument JSON concatenates, other fields fill in when empty.
func mergeToolCall(dst *schemas.ChatAssistantMessageToolCall, src *schemas.ChatAssistantMessageToolCall) {
	if dst.Function.Arguments != "" && src.Function.Arguments != "" {
		dst.Function.Arguments += src.Function.Arguments
	} else if dst.Function.Arguments == "" {
		dst.Function.Arguments = src.Function.Arguments
	}
	if dst.Function.Name == nil {
		dst.Function.Name = src.Function.Name
	}
	if dst.ID == nil {
		dst.ID = src.ID
	}
	if dst.Type == nil {
		dst.Type = src.Type
	}
}

// responsesStreamError converts an inner Responses error event to a BifrostError.
func responsesStreamError(response *schemas.BifrostResponsesStreamResponse) *schemas.BifrostError {
	bErr := &schemas.BifrostError{
		Type:           schemas.Ptr(string(schemas.ResponsesStreamResponseTypeError)),
		IsBifrostError: false,
		Error:          &schemas.ErrorField{},
	}
	if response.Message != nil {
		bErr.Error.Message = *response.Message
	}
	if response.Param != nil {
		bErr.Error.Param = response.Param
	}
	if response.Code != nil {
		bErr.Error.Code = response.Code
	}
	if bErr.Error.Message == "" {
		bErr.Error.Message = "zed: inner open_ai stream error"
	}
	return bErr
}
