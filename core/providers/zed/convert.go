package zed

import (
	"strings"

	"github.com/maximhq/bifrost/core/providers/anthropic"
	"github.com/maximhq/bifrost/core/providers/gemini"
	"github.com/maximhq/bifrost/core/providers/openai"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
)

// toInnerAnthropicRequest converts a Bifrost chat request to the Anthropic
// Messages shape Zed expects inside provider_request. The Anthropic converter
// gates on BifrostContextKeySupportsAssistantPrefill only when explicitly set
// to false; Zed replays full conversations like the native API, so the
// default (prefill allowed) applies.
func toInnerAnthropicRequest(ctx *schemas.BifrostContext, request *schemas.BifrostChatRequest) (*anthropic.AnthropicMessageRequest, *schemas.BifrostError) {
	ensureZedChatSystemPrompt(request)
	inner, err := anthropic.ToAnthropicChatRequest(ctx, request)
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrRequestBodyConversion, err)
	}
	if inner == nil {
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrRequestBodyConversion, errNilAnthropicRequest)
	}
	// Zed drives the stream, not the inner request: the envelope has no
	// per-family stream flag for Anthropic, and the NDJSON stream always flows.
	inner.Stream = nil
	// The wire model is the Zed model id (e.g. "claude-haiku-4-5"), not the
	// versioned upstream id — Zed resolves it server-side.
	inner.Model = request.Model
	return inner, nil
}

// toInnerGeminiRequest converts a Bifrost chat request to the Gemini
// generateContent shape Zed expects inside provider_request. The model is
// sent as a "models/..." resource name, matching the live Zed capture.
func toInnerGeminiRequest(ctx *schemas.BifrostContext, request *schemas.BifrostChatRequest) (*gemini.GeminiGenerationRequest, *schemas.BifrostError) {
	ensureZedChatSystemPrompt(request)
	inner, err := gemini.ToGeminiChatCompletionRequest(ctx, request)
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrRequestBodyConversion, err)
	}
	if inner == nil {
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrRequestBodyConversion, errNilGeminiRequest)
	}
	inner.Model = toGeminiResourceName(request.Model)
	inner.Stream = false
	return inner, nil
}

// toInnerOpenAIRequest converts a Bifrost responses request to the OpenAI
// Responses shape Zed expects inside provider_request (input[], stream:true,
// prompt_cache_key=thread_id, store:false). It mirrors the live Zed capture:
// only these envelope-level fields are set here; sampling/model params ride
// inside the converted request itself.
func toInnerOpenAIRequest(ctx *schemas.BifrostContext, request *schemas.BifrostResponsesRequest, threadID string) (*openai.OpenAIResponsesRequest, *schemas.BifrostError) {
	stripEmptyZedResponsesSystemMessages(request)
	ensureZedResponsesSystemPrompt(request)
	normalizeZedResponsesInput(request)
	inner := openai.ToOpenAIResponsesRequest(ctx, request)
	if inner == nil {
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrRequestBodyConversion, errNilOpenAIRequest)
	}
	inner.Model = request.Model
	inner.Stream = schemas.Ptr(true)
	if threadID != "" {
		inner.PromptCacheKey = schemas.Ptr(threadID)
	}
	inner.Store = schemas.Ptr(false)
	return inner, nil
}

// zedDefaultSystemPrompt is the fallback system instruction injected into
// every Zed inner request (anthropic, google, open_ai families) when the
// caller supplied none. Zed's cloud API rejects Google (and, live-verified,
// Anthropic/OpenAI-family) requests that carry no system content at all, so
// the default keeps bare user-only transcripts sendable. Matched against the
// Bifrost-level shapes (schemas.ChatMessage / schemas.ResponsesMessage),
// never the serialized wire JSON.
const zedDefaultSystemPrompt = "You are helpfull assistant"

// hasZedChatSystemPrompt reports whether a chat transcript already carries a
// system/developer turn with renderable content. Turns with an empty string
// or no text blocks count as absent: clients routinely send
// {"role":"system","content":""}, which must not satisfy Zed's system
// requirement nor survive as an empty system block on the wire.
func hasZedChatSystemPrompt(request *schemas.BifrostChatRequest) bool {
	if request == nil {
		return false
	}
	for _, msg := range request.Input {
		if msg.Role != schemas.ChatMessageRoleSystem && msg.Role != schemas.ChatMessageRoleDeveloper {
			continue
		}
		if chatMessageHasRenderableContent(msg.Content) {
			return true
		}
	}
	return false
}

// chatMessageHasRenderableContent reports whether chat content carries
// anything worth sending: non-empty text, or a media/file payload.
func chatMessageHasRenderableContent(content *schemas.ChatMessageContent) bool {
	if content == nil {
		return false
	}
	if content.ContentStr != nil && *content.ContentStr != "" {
		return true
	}
	for _, block := range content.ContentBlocks {
		if block.Text != nil && *block.Text != "" {
			return true
		}
		if block.ImageURLStruct != nil || block.File != nil || block.InputAudio != nil || block.Refusal != nil {
			return true
		}
	}
	return false
}

// stripEmptyZedChatSystemMessages drops system/developer turns that carry no
// renderable content, so an empty client system prompt neither blocks the
// default injection nor leaks into the inner request as an empty system
// block (which Zed rejects the same way it rejects a missing one).
func stripEmptyZedChatSystemMessages(request *schemas.BifrostChatRequest) {
	kept := make([]schemas.ChatMessage, 0, len(request.Input))
	for _, msg := range request.Input {
		if (msg.Role == schemas.ChatMessageRoleSystem || msg.Role == schemas.ChatMessageRoleDeveloper) &&
			!chatMessageHasRenderableContent(msg.Content) {
			continue
		}
		kept = append(kept, msg)
	}
	request.Input = kept
}

// ensureZedChatSystemPrompt prepends the default system turn to a chat
// request that carries none, mirroring the Zed editor's own capture shape
// (system prompt first, then the user transcript). Mutating the caller's
// request keeps the retry/fallback path on the same transcript: the second
// converter call sees a system turn present and leaves it untouched.
func ensureZedChatSystemPrompt(request *schemas.BifrostChatRequest) {
	if request == nil {
		return
	}
	stripEmptyZedChatSystemMessages(request)
	if hasZedChatSystemPrompt(request) {
		return
	}
	request.Input = append([]schemas.ChatMessage{{
		Role:    schemas.ChatMessageRoleSystem,
		Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr(zedDefaultSystemPrompt)},
	}}, request.Input...)
}

// hasZedResponsesSystemPrompt reports whether a Responses request already
// carries a system instruction: top-level instructions, or a system/developer
// input message with renderable content. Messages with no role at all are
// skipped: the OpenAI converter drops role-less non-message items, so they
// can never satisfy Zed's system requirement. Empty-content system turns
// (clients routinely send {"role":"system","content":""}) count as absent.
func hasZedResponsesSystemPrompt(request *schemas.BifrostResponsesRequest) bool {
	if request == nil {
		return false
	}
	if request.Params != nil && request.Params.Instructions != nil && *request.Params.Instructions != "" {
		return true
	}
	for _, msg := range request.Input {
		if msg.Role == nil || (*msg.Role != schemas.ResponsesInputMessageRoleSystem && *msg.Role != schemas.ResponsesInputMessageRoleDeveloper) {
			continue
		}
		if responsesMessageHasRenderableContent(msg.Content) {
			return true
		}
	}
	return false
}

// responsesMessageHasRenderableContent reports whether Responses content
// carries anything worth sending: non-empty text, or a media/file payload.
func responsesMessageHasRenderableContent(content *schemas.ResponsesMessageContent) bool {
	if content == nil {
		return false
	}
	if content.ContentStr != nil && *content.ContentStr != "" {
		return true
	}
	for _, block := range content.ContentBlocks {
		if block.Text != nil && *block.Text != "" {
			return true
		}
		if block.FileID != nil || block.ResponsesInputMessageContentBlockImage != nil ||
			block.ResponsesInputMessageContentBlockFile != nil || block.Audio != nil ||
			block.ResponsesOutputMessageContentText != nil || block.ResponsesOutputMessageContentRefusal != nil {
			return true
		}
	}
	return false
}

// normalizeZedResponsesInput rewrites every Responses input message to the
// wire shape Zed's Responses parser accepts: content as an ARRAY of blocks,
// never a bare string. The shared OpenAI converter passes ContentStr
// through verbatim, which serializes to "content":"..." — every Zed capture
// in debug/zed-dev carries content as [{"type":"input_text","text":...}]
// instead, and Zed rejects the string form outright
// ("invalid type: string ..., expected a sequence").
// Messages without renderable content are left untouched: the empty-system
// strip runs first and drops them if they are system/developer turns.
func normalizeZedResponsesInput(request *schemas.BifrostResponsesRequest) {
	for i := range request.Input {
		msg := &request.Input[i]
		if msg.Content == nil || msg.Content.ContentStr == nil || *msg.Content.ContentStr == "" {
			continue
		}
		if len(msg.Content.ContentBlocks) > 0 {
			continue
		}
		text := *msg.Content.ContentStr
		blockType := schemas.ResponsesInputMessageContentBlockTypeText
		if msg.Role != nil && *msg.Role == schemas.ResponsesInputMessageRoleAssistant {
			blockType = schemas.ResponsesOutputMessageContentTypeText
		}
		msg.Content = &schemas.ResponsesMessageContent{
			ContentBlocks: []schemas.ResponsesMessageContentBlock{{
				Type: blockType,
				Text: &text,
			}},
		}
	}
}

// stripEmptyZedResponsesSystemMessages drops system/developer input messages
// that carry no renderable content, so an empty client system prompt neither
// blocks the default injection nor leaks into the inner request as an empty
// system block (which Zed rejects the same way it rejects a missing one).
func stripEmptyZedResponsesSystemMessages(request *schemas.BifrostResponsesRequest) {
	kept := make([]schemas.ResponsesMessage, 0, len(request.Input))
	for _, msg := range request.Input {
		if msg.Role != nil && (*msg.Role == schemas.ResponsesInputMessageRoleSystem || *msg.Role == schemas.ResponsesInputMessageRoleDeveloper) &&
			!responsesMessageHasRenderableContent(msg.Content) {
			continue
		}
		kept = append(kept, msg)
	}
	request.Input = kept
}

// ensureZedResponsesSystemPrompt prepends a default system input message to a
// Responses request that carries none (neither top-level instructions nor a
// system/developer turn). Only the inner open_ai family funnels through
// ToResponsesRequest conversion, so input message placement — not
// instructions — matches the live Zed capture for that family (system first in
// input[]). The content is an input_text block array, not a bare string:
// Zed's parser rejects string content on the Responses wire shape
// ("invalid type: string ..., expected a sequence"). Idempotent like the chat
// counterpart.
func ensureZedResponsesSystemPrompt(request *schemas.BifrostResponsesRequest) {
	if request == nil {
		return
	}
	if hasZedResponsesSystemPrompt(request) {
		return
	}
	systemType := schemas.ResponsesMessageTypeMessage
	request.Input = append([]schemas.ResponsesMessage{{
		Type: &systemType,
		Role: schemas.Ptr(schemas.ResponsesInputMessageRoleSystem),
		Content: &schemas.ResponsesMessageContent{
			ContentBlocks: []schemas.ResponsesMessageContentBlock{{
				Type: schemas.ResponsesInputMessageContentBlockTypeText,
				Text: schemas.Ptr(zedDefaultSystemPrompt),
			}},
		},
	}}, request.Input...)
}

// toGeminiResourceName maps a Bifrost model id to the Gemini resource name
// Zed sends ("models/<id>").
func toGeminiResourceName(model string) string {
	model = strings.TrimSpace(model)
	if strings.HasPrefix(model, "models/") {
		return model
	}
	if idx := strings.Index(model, "/"); idx >= 0 && idx+1 < len(model) {
		return "models/" + model[idx+1:]
	}
	return "models/" + model
}
