package opencodezenfree

import (
	"github.com/maximhq/bifrost/core/providers/anthropic"
	"github.com/maximhq/bifrost/core/providers/openai"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	schemas "github.com/maximhq/bifrost/core/schemas"
)

// identityPostHookRunner passes streaming chunks through untouched. Unary
// fan-in (stream:true forcing) drains the provider's own streaming path
// internally, so there is no caller post-hook to run per chunk; the core
// runs the plugin pipeline once on the final unary response.
func identityPostHookRunner(_ *schemas.BifrostContext, result *schemas.BifrostResponse, bifrostErr *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError) {
	return result, bifrostErr
}

// opencodeChatURL resolves the chat-completions URL for the request.
func (p *opencodeZenFreeProvider) opencodeChatURL(ctx *schemas.BifrostContext) string {
	return p.networkConfig.BaseURL + providerUtils.GetPathFromContext(ctx, opencodeZenFreeChatPath)
}

// opencodeResponsesURL resolves the responses URL for the request.
func (p *opencodeZenFreeProvider) opencodeResponsesURL(ctx *schemas.BifrostContext) string {
	return p.networkConfig.BaseURL + providerUtils.GetPathFromContext(ctx, opencodeZenFreeResponsesPath)
}

// opencodeMessagesURL resolves the Anthropic messages URL for the request.
func (p *opencodeZenFreeProvider) opencodeMessagesURL(ctx *schemas.BifrostContext) string {
	return p.networkConfig.BaseURL + providerUtils.GetPathFromContext(ctx, opencodeZenFreeMessagesPath)
}

// opencodeGeminiStreamURL resolves the per-model Google Generative Language
// streaming URL (/v1/models/<model>:streamGenerateContent?alt=sse).
func (p *opencodeZenFreeProvider) opencodeGeminiStreamURL(ctx *schemas.BifrostContext, model string) string {
	return p.networkConfig.BaseURL + providerUtils.GetPathFromContext(ctx, opencodeZenFreeGeminiModelsPrefix+bareOpencodeModelName(model)+":streamGenerateContent?alt=sse")
}

// anthropicHeaders builds the headers for the /v1/messages path: the shared
// opencode headers plus the Anthropic version header. The gateway gates this
// endpoint like an Anthropic surface — it answered without any version
// header live, but sending it keeps the request well-formed per the wire
// format.
func anthropicHeaders(extraHeaders map[string]string) map[string]string {
	headers := map[string]string{
		"anthropic-version": "2023-06-01",
	}
	for k, v := range extraHeaders {
		headers[k] = v
	}
	return headers
}

// geminiHeaders builds the headers for the Google Generative Language path:
// the shared opencode headers, without any provider API key (the free tier
// is keyless).
func geminiHeaders(extraHeaders map[string]string) map[string]string {
	headers := map[string]string{}
	for k, v := range extraHeaders {
		headers[k] = v
	}
	return headers
}

// doResponsesUnary sends one unary /v1/responses attempt, forcing
// stream:true on the wire (a unary stream:false call fails with a free-tier
// error live) and accumulating the SSE stream into one responses response.
func (p *opencodeZenFreeProvider) doResponsesUnary(ctx *schemas.BifrostContext, request *schemas.BifrostResponsesRequest, extraHeaders map[string]string) (*schemas.BifrostResponsesResponse, *schemas.BifrostError) {
	postRequestConverter := func(wireReq *openai.OpenAIResponsesRequest) *openai.OpenAIResponsesRequest {
		wireReq = preserveOpencodeReasoning(wireReq, request)
		wireReq = ensureOpencodeReasoningItemSummaries(wireReq)
		return normalizeOpencodeReasoningItems(wireReq, request)
	}
	forced, release := forceStreamTrueResponses(request)
	defer release()
	stream, bifrostErr := openai.HandleOpenAIResponsesStreaming(
		ctx,
		p.streamingClient,
		p.opencodeResponsesURL(ctx),
		forced,
		nil,
		extraHeaders,
		p.networkConfig.StreamIdleTimeoutInSeconds,
		providerUtils.ShouldSendBackRawRequest(ctx, p.sendBackRawRequest),
		providerUtils.ShouldSendBackRawResponse(ctx, p.sendBackRawResponse),
		p.providerKey,
		identityPostHookRunner,
		nil,
		parseOpencodeZenFreeError,
		postRequestConverter,
		nil,
		nil,
		p.logger,
		nil,
	)
	if bifrostErr != nil {
		return nil, bifrostErr
	}
	return accumulateResponsesStream(stream, request.Model)
}

// doChatUnary sends one unary /v1/chat/completions attempt, forcing
// stream:true on the wire (the free tier requires it) and accumulating the
// SSE stream into one chat response. The stream is drained and closed here,
// so no goroutine or connection outlives the call.
func (p *opencodeZenFreeProvider) doChatUnary(ctx *schemas.BifrostContext, request *schemas.BifrostChatRequest, extraHeaders map[string]string) (*schemas.BifrostChatResponse, *schemas.BifrostError) {
	forced, release := forceStreamTrueChat(request)
	defer release()
	stream, bifrostErr := openai.HandleOpenAIChatCompletionStreaming(
		ctx,
		p.streamingClient,
		p.opencodeChatURL(ctx),
		forced,
		nil,
		extraHeaders,
		p.networkConfig.StreamIdleTimeoutInSeconds,
		providerUtils.ShouldSendBackRawRequest(ctx, p.sendBackRawRequest),
		providerUtils.ShouldSendBackRawResponse(ctx, p.sendBackRawResponse),
		p.providerKey,
		identityPostHookRunner,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		p.logger,
		nil,
	)
	if bifrostErr != nil {
		return nil, bifrostErr
	}
	return accumulateChatStream(stream, request.Model)
}

// doMessagesChatUnary sends one unary /v1/messages attempt for a chat-shaped
// request, forcing stream:true on the wire and accumulating the SSE stream.
func (p *opencodeZenFreeProvider) doMessagesChatUnary(ctx *schemas.BifrostContext, request *schemas.BifrostChatRequest, extraHeaders map[string]string) (*schemas.BifrostChatResponse, *schemas.BifrostError) {
	forced, release := forceStreamTrueChat(request)
	defer release()
	jsonBody, bifrostErr := anthropic.BuildAnthropicChatRequestBody(ctx, forced, anthropic.AnthropicRequestBuildConfig{
		Provider:                  schemas.OpencodeZenFree,
		IsStreaming:               true,
		ShouldSendBackRawRequest:  providerUtils.ShouldSendBackRawRequest(ctx, p.sendBackRawRequest),
		ShouldSendBackRawResponse: providerUtils.ShouldSendBackRawResponse(ctx, p.sendBackRawResponse),
	})
	if bifrostErr != nil {
		return nil, bifrostErr
	}
	stream, bifrostErr := anthropic.HandleAnthropicChatCompletionStreaming(
		ctx,
		p.streamingClient,
		p.opencodeMessagesURL(ctx),
		jsonBody,
		anthropicHeaders(nil),
		extraHeaders,
		p.networkConfig.StreamIdleTimeoutInSeconds,
		p.networkConfig.BetaHeaderOverrides,
		providerUtils.ShouldSendBackRawRequest(ctx, p.sendBackRawRequest),
		providerUtils.ShouldSendBackRawResponse(ctx, p.sendBackRawResponse),
		p.providerKey,
		identityPostHookRunner,
		nil,
		nil,
		p.logger,
		nil,
	)
	if bifrostErr != nil {
		return nil, bifrostErr
	}
	return accumulateChatStream(stream, request.Model)
}

// doMessagesResponsesUnary sends one unary /v1/messages attempt for a
// responses-shaped request, forcing stream:true on the wire and accumulating
// the SSE stream.
func (p *opencodeZenFreeProvider) doMessagesResponsesUnary(ctx *schemas.BifrostContext, request *schemas.BifrostResponsesRequest, extraHeaders map[string]string) (*schemas.BifrostResponsesResponse, *schemas.BifrostError) {
	forced, release := forceStreamTrueResponses(request)
	defer release()
	jsonBody, bifrostErr := anthropic.BuildAnthropicResponsesRequestBody(ctx, forced, anthropic.AnthropicRequestBuildConfig{
		Provider:                  schemas.OpencodeZenFree,
		IsStreaming:               true,
		ShouldSendBackRawRequest:  providerUtils.ShouldSendBackRawRequest(ctx, p.sendBackRawRequest),
		ShouldSendBackRawResponse: providerUtils.ShouldSendBackRawResponse(ctx, p.sendBackRawResponse),
	})
	if bifrostErr != nil {
		return nil, bifrostErr
	}
	stream, bifrostErr := anthropic.HandleAnthropicResponsesStream(
		ctx,
		p.streamingClient,
		p.opencodeMessagesURL(ctx),
		jsonBody,
		anthropicHeaders(nil),
		extraHeaders,
		p.networkConfig.StreamIdleTimeoutInSeconds,
		p.networkConfig.BetaHeaderOverrides,
		providerUtils.ShouldSendBackRawRequest(ctx, p.sendBackRawRequest),
		providerUtils.ShouldSendBackRawResponse(ctx, p.sendBackRawResponse),
		p.providerKey,
		identityPostHookRunner,
		nil,
		nil,
		p.logger,
		nil,
	)
	if bifrostErr != nil {
		return nil, bifrostErr
	}
	return accumulateResponsesStream(stream, request.Model)
}
