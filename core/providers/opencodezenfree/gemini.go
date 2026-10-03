package opencodezenfree

import (
	"context"
	"fmt"
	"time"

	"github.com/maximhq/bifrost/core/providers/gemini"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

// geminiChatStream streams one chat-shaped request over the Google
// Generative Language path.
func (p *opencodeZenFreeProvider) geminiChatStream(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, postHookSpanFinalizer func(context.Context), request *schemas.BifrostChatRequest, extraHeaders map[string]string) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	genReq, err := gemini.ToGeminiChatCompletionRequest(ctx, request)
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderRequestMarshal, err)
	}
	if genReq == nil {
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderRequestMarshal, fmt.Errorf("chat completion request could not be converted to gemini format"))
	}
	rawBody, bifrostErr := marshalGeminiBody(genReq)
	if bifrostErr != nil {
		return nil, bifrostErr
	}
	return gemini.HandleGeminiChatCompletionStream(
		ctx,
		p.streamingClient,
		p.opencodeGeminiStreamURL(ctx, request.Model),
		rawBody,
		geminiStreamHeaders(),
		extraHeaders,
		p.networkConfig.StreamIdleTimeoutInSeconds,
		providerUtils.ShouldSendBackRawRequest(ctx, p.sendBackRawRequest),
		providerUtils.ShouldSendBackRawResponse(ctx, p.sendBackRawResponse),
		p.providerKey,
		request.Model,
		postHookRunner,
		nil,
		p.logger,
		postHookSpanFinalizer,
	)
}

// geminiResponsesStream streams one responses-shaped request over the Google
// Generative Language path.
func (p *opencodeZenFreeProvider) geminiResponsesStream(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, postHookSpanFinalizer func(context.Context), request *schemas.BifrostResponsesRequest, extraHeaders map[string]string) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	genReq, err := gemini.ToGeminiResponsesRequest(ctx, request)
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderRequestMarshal, err)
	}
	if genReq == nil {
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderRequestMarshal, fmt.Errorf("responses request could not be converted to gemini format"))
	}
	rawBody, bifrostErr := marshalGeminiBody(genReq)
	if bifrostErr != nil {
		return nil, bifrostErr
	}
	return gemini.HandleGeminiResponsesStream(
		ctx,
		p.streamingClient,
		p.opencodeGeminiStreamURL(ctx, request.Model),
		rawBody,
		geminiStreamHeaders(),
		extraHeaders,
		p.networkConfig.StreamIdleTimeoutInSeconds,
		providerUtils.ShouldSendBackRawRequest(ctx, p.sendBackRawRequest),
		providerUtils.ShouldSendBackRawResponse(ctx, p.sendBackRawResponse),
		p.providerKey,
		request.Model,
		postHookRunner,
		nil,
		p.logger,
		postHookSpanFinalizer,
	)
}

// geminiStreamHeaders carries the SSE accept headers for the Gemini streaming
// path. Auth rides in extraHeaders (the shared opencode headers).
func geminiStreamHeaders() map[string]string {
	return map[string]string{
		"Accept":        "text/event-stream",
		"Cache-Control": "no-cache",
	}
}

// marshalGeminiBody converts a Gemini generation request to wire bytes.
func marshalGeminiBody(body providerUtils.RequestBodyWithExtraParams) ([]byte, *schemas.BifrostError) {
	raw, err := providerUtils.MarshalProviderRequest(body)
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderRequestMarshal, err)
	}
	return raw, nil
}

// doGeminiChatUnary sends one unary Gemini attempt for a chat-shaped request
// via stream fan-in (same stream:true forcing as the other endpoints).
func (p *opencodeZenFreeProvider) doGeminiChatUnary(ctx *schemas.BifrostContext, request *schemas.BifrostChatRequest, extraHeaders map[string]string) (*schemas.BifrostChatResponse, *schemas.BifrostError) {
	return p.geminiStreamUnary(ctx, request, extraHeaders)
}

// doGeminiResponsesUnary sends one unary Gemini attempt for a
// responses-shaped request via stream fan-in.
func (p *opencodeZenFreeProvider) doGeminiResponsesUnary(ctx *schemas.BifrostContext, request *schemas.BifrostResponsesRequest, extraHeaders map[string]string) (*schemas.BifrostResponsesResponse, *schemas.BifrostError) {
	return p.geminiStreamResponsesUnary(ctx, request, extraHeaders)
}

// geminiStreamUnary drains one Gemini streaming attempt into a unary chat
// response for stream:true forcing on the Gemini path.
func (p *opencodeZenFreeProvider) geminiStreamUnary(ctx *schemas.BifrostContext, request *schemas.BifrostChatRequest, extraHeaders map[string]string) (*schemas.BifrostChatResponse, *schemas.BifrostError) {
	stream, bifrostErr := p.geminiChatStream(ctx, identityPostHookRunner, nil, request, extraHeaders)
	if bifrostErr != nil {
		return nil, bifrostErr
	}
	return accumulateChatStream(stream, request.Model)
}

// geminiStreamResponsesUnary drains one Gemini streaming attempt into a unary
// responses response for stream:true forcing on the Gemini path.
func (p *opencodeZenFreeProvider) geminiStreamResponsesUnary(ctx *schemas.BifrostContext, request *schemas.BifrostResponsesRequest, extraHeaders map[string]string) (*schemas.BifrostResponsesResponse, *schemas.BifrostError) {
	stream, bifrostErr := p.geminiResponsesStream(ctx, identityPostHookRunner, nil, request, extraHeaders)
	if bifrostErr != nil {
		return nil, bifrostErr
	}
	return accumulateResponsesStream(stream, request.Model)
}

var (
	_ = fasthttp.AcquireRequest
	_ = time.Now
)
