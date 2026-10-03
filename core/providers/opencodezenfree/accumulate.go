package opencodezenfree

import (
	"strings"

	schemas "github.com/maximhq/bifrost/core/schemas"
)

// forcedStreamMarker keys the unary stream:true forcing flag. Only the unary
// (non-stream) entry points apply it; streaming paths pass through untouched.
//
// The marker travels on ExtraParams (never serialized by the converters —
// the streaming handlers set the wire stream field themselves), so the
// forcing is observable in tests without changing wire bytes.
const forcedStreamMarker = "opencode_zen_free_forced_stream"

// forceStreamTrueChat returns a copy of the chat request carrying an explicit
// stream:true marker for the unary fan-in path, plus a release function.
func forceStreamTrueChat(request *schemas.BifrostChatRequest) (*schemas.BifrostChatRequest, func()) {
	if request == nil {
		return nil, func() {}
	}
	forced := *request
	extra := make(map[string]interface{}, 1)
	if forced.Params != nil {
		for k, v := range forced.Params.ExtraParams {
			extra[k] = v
		}
	}
	extra[forcedStreamMarker] = true
	if forced.Params == nil {
		forced.Params = &schemas.ChatParameters{}
	} else {
		paramsCopy := *forced.Params
		forced.Params = &paramsCopy
	}
	forced.Params.ExtraParams = extra
	return &forced, func() {}
}

// forceStreamTrueResponses is the responses-shape counterpart of
// forceStreamTrueChat.
func forceStreamTrueResponses(request *schemas.BifrostResponsesRequest) (*schemas.BifrostResponsesRequest, func()) {
	if request == nil {
		return nil, func() {}
	}
	forced := *request
	extra := make(map[string]interface{}, 1)
	if forced.Params != nil {
		for k, v := range forced.Params.ExtraParams {
			extra[k] = v
		}
	}
	extra[forcedStreamMarker] = true
	if forced.Params == nil {
		forced.Params = &schemas.ResponsesParameters{}
	} else {
		paramsCopy := *forced.Params
		forced.Params = &paramsCopy
	}
	forced.Params.ExtraParams = extra
	return &forced, func() {}
}

// forcedStreamTrue reports whether a request carries the unary fan-in marker.
func forcedStreamTrue(extra map[string]interface{}) bool {
	if len(extra) == 0 {
		return false
	}
	v, ok := extra[forcedStreamMarker]
	if !ok {
		return false
	}
	b, ok := v.(bool)
	return ok && b
}

// accumulateChatStream drains a chat stream channel (the provider's own
// streaming path, already forced to stream:true) into one unary chat
// response. Text deltas concatenate in arrival order; tool-call argument
// fragments concatenate per tool index; reasoning, refusal, and annotations
// merge the same way. Usage takes the last (authoritative) frame; id, model,
// and created pin to the first frame that carries them.
//
// A stream that yields no content chunk but ends cleanly still returns a
// response (empty choice, finish reason when observed) rather than nil, so
// unary callers never face an empty success. The first error chunk aborts
// the drain and surfaces as the unary error — on unsupported-format signals
// the caller advances the endpoint fallback chain.
func accumulateChatStream(stream chan *schemas.BifrostStreamChunk, model string) (*schemas.BifrostChatResponse, *schemas.BifrostError) {
	merged := &schemas.BifrostChatResponse{
		Model:  model,
		Object: "chat.completion",
	}
	type toolAcc struct {
		id        *string
		toolType  *string
		name      *string
		arguments strings.Builder
		index     uint16
		seen      bool
	}
	var (
		content     strings.Builder
		reasoning   strings.Builder
		refusal     strings.Builder
		tools       []toolAcc
		finish      *string
		created     int
		id          string
		respModel   string
		usage       *schemas.BifrostLLMUsage
		extraFields schemas.BifrostResponseExtraFields
		sawChunk    bool
	)
	ensureTool := func(index uint16) *toolAcc {
		for i := range tools {
			if tools[i].index == index {
				return &tools[i]
			}
		}
		tools = append(tools, toolAcc{index: index})
		return &tools[len(tools)-1]
	}
	for chunk := range stream {
		if chunk == nil {
			continue
		}
		if chunk.BifrostError != nil && chunk.BifrostError.Error != nil {
			return nil, chunk.BifrostError
		}
		chatResp := chunk.BifrostChatResponse
		if chatResp == nil && chunk.BifrostResponsesStreamResponse != nil {
			chatResp = chunk.BifrostResponsesStreamResponse.ToBifrostChatResponse()
		}
		if chatResp == nil {
			continue
		}
		sawChunk = true
		if id == "" && chatResp.ID != "" {
			id = chatResp.ID
		}
		if respModel == "" && chatResp.Model != "" {
			respModel = chatResp.Model
		}
		if created == 0 && chatResp.Created != 0 {
			created = chatResp.Created
		}
		if chatResp.Usage != nil {
			usage = chatResp.Usage
		}
		extraFields.Latency = chatResp.ExtraFields.Latency
		if chatResp.ExtraFields.ProviderResponseHeaders != nil {
			extraFields.ProviderResponseHeaders = chatResp.ExtraFields.ProviderResponseHeaders
		}
		for _, choice := range chatResp.Choices {
			if choice.FinishReason != nil && *choice.FinishReason != "" {
				finish = choice.FinishReason
			}
			var delta *schemas.ChatStreamResponseChoiceDelta
			if choice.ChatStreamResponseChoice != nil {
				delta = choice.ChatStreamResponseChoice.Delta
			}
			if delta == nil {
				continue
			}
			if delta.Content != nil {
				content.WriteString(*delta.Content)
			}
			if delta.Reasoning != nil {
				reasoning.WriteString(*delta.Reasoning)
			}
			if delta.Refusal != nil {
				refusal.WriteString(*delta.Refusal)
			}
			for _, tc := range delta.ToolCalls {
				acc := ensureTool(tc.Index)
				acc.seen = true
				if tc.ID != nil {
					acc.id = tc.ID
				}
				if tc.Type != nil {
					acc.toolType = tc.Type
				}
				if tc.Function.Name != nil {
					name := *tc.Function.Name
					acc.name = &name
				}
				acc.arguments.WriteString(tc.Function.Arguments)
			}
		}
	}
	if !sawChunk && usage == nil {
		return merged, nil
	}
	if id != "" {
		merged.ID = id
	}
	if respModel != "" {
		merged.Model = respModel
	}
	merged.Created = created
	merged.Usage = usage
	merged.ExtraFields = extraFields
	message := schemas.ChatMessage{
		Role:    schemas.ChatMessageRoleAssistant,
		Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr(content.String())},
	}
	if reasoning.Len() > 0 {
		text := reasoning.String()
		message.ChatAssistantMessage = &schemas.ChatAssistantMessage{
			Reasoning: &text,
		}
	}
	if refusal.Len() > 0 {
		text := refusal.String()
		if message.ChatAssistantMessage == nil {
			message.ChatAssistantMessage = &schemas.ChatAssistantMessage{}
		}
		message.ChatAssistantMessage.Refusal = &text
	}
	if len(tools) > 0 {
		calls := make([]schemas.ChatAssistantMessageToolCall, 0, len(tools))
		for _, t := range tools {
			if !t.seen {
				continue
			}
			call := schemas.ChatAssistantMessageToolCall{
				Index: t.index,
				ID:    t.id,
				Type:  t.toolType,
				Function: schemas.ChatAssistantMessageToolCallFunction{
					Arguments: t.arguments.String(),
				},
			}
			if t.name != nil {
				name := *t.name
				call.Function.Name = &name
			}
			calls = append(calls, call)
		}
		if message.ChatAssistantMessage == nil {
			message.ChatAssistantMessage = &schemas.ChatAssistantMessage{}
		}
		message.ChatAssistantMessage.ToolCalls = calls
	}
	merged.Choices = []schemas.BifrostResponseChoice{
		{
			Index:        0,
			FinishReason: finish,
			ChatNonStreamResponseChoice: &schemas.ChatNonStreamResponseChoice{
				Message: &message,
			},
		},
	}
	return merged, nil
}

// accumulateResponsesStream drains a responses stream channel into one unary
// responses response. The terminal response.completed event carries the full
// response, so it is preferred verbatim; when the stream ends without one
// (deltas only), the text deltas concatenate into a single output message.
// The first error chunk aborts the drain and surfaces as the unary error.
func accumulateResponsesStream(stream chan *schemas.BifrostStreamChunk, model string) (*schemas.BifrostResponsesResponse, *schemas.BifrostError) {
	var (
		completed *schemas.BifrostResponsesResponse
		text      strings.Builder
		id        *string
		created   int
		respModel string
		usage     *schemas.ResponsesResponseUsage
		extra     schemas.BifrostResponseExtraFields
		sawChunk  bool
	)
	for chunk := range stream {
		if chunk == nil {
			continue
		}
		if chunk.BifrostError != nil && chunk.BifrostError.Error != nil {
			return nil, chunk.BifrostError
		}
		ev := chunk.BifrostResponsesStreamResponse
		if ev == nil {
			continue
		}
		sawChunk = true
		switch ev.Type {
		case schemas.ResponsesStreamResponseTypeCompleted, schemas.ResponsesStreamResponseTypeIncomplete:
			if ev.Response != nil {
				completed = ev.Response
			}
		case schemas.ResponsesStreamResponseTypeOutputTextDelta:
			if ev.Delta != nil {
				text.WriteString(*ev.Delta)
			}
			if ev.Response != nil {
				if id == nil && ev.Response.ID != nil {
					id = ev.Response.ID
				}
				if ev.Response.Model != "" {
					respModel = ev.Response.Model
				}
				if ev.Response.Usage != nil {
					usage = ev.Response.Usage
				}
			}
		case schemas.ResponsesStreamResponseTypeOutputItemDone:
			if ev.Item != nil && ev.Item.Content != nil && ev.Item.Content.ContentStr != nil {
				text.WriteString(*ev.Item.Content.ContentStr)
			}
		}
		if ev.Response != nil && ev.Response.Usage != nil {
			usage = ev.Response.Usage
		}
		extra.Latency = ev.ExtraFields.Latency
		if ev.ExtraFields.ProviderResponseHeaders != nil {
			extra.ProviderResponseHeaders = ev.ExtraFields.ProviderResponseHeaders
		}
	}
	if completed != nil {
		return completed, nil
	}
	if !sawChunk {
		return &schemas.BifrostResponsesResponse{Model: model, ExtraFields: extra}, nil
	}
	out := &schemas.BifrostResponsesResponse{
		Model:       model,
		Usage:       usage,
		ExtraFields: extra,
	}
	if id != nil {
		out.ID = id
	}
	if respModel != "" {
		out.Model = respModel
	}
	out.CreatedAt = created
	content := text.String()
	out.Output = []schemas.ResponsesMessage{
		{
			Role:    schemas.Ptr(schemas.ResponsesInputMessageRoleAssistant),
			Content: &schemas.ResponsesMessageContent{ContentStr: &content},
		},
	}
	return out, nil
}
