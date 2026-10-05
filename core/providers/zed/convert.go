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
