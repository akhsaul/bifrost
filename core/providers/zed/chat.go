package zed

import (
	"context"
	"net/http"
	"time"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

// ChatCompletion performs a chat completion request to Zed's /completions.
// Zed only streams, so the NDJSON stream is accumulated into one response.
func (provider *ZedProvider) ChatCompletion(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostChatRequest) (*schemas.BifrostChatResponse, *schemas.BifrostError) {
	if request == nil {
		return nil, providerUtils.NewBifrostOperationError("chat completion request is nil", nil)
	}
	// Resolve the inner family from the live /models catalog.
	innerProvider, bErr := provider.resolveInnerProvider(ctx, key, request.Model)
	if bErr != nil {
		return nil, bErr
	}
	envelope, bErr := provider.buildChatEnvelope(ctx, key, request, innerProvider)
	if bErr != nil {
		return nil, bErr
	}
	return provider.doCompletionsUnary(ctx, key, envelope, request.Model, innerProvider)
}

// buildChatEnvelope converts a Bifrost chat request to the /completions
// envelope for the resolved inner family.
func (provider *ZedProvider) buildChatEnvelope(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostChatRequest, innerProvider string) (*ZedCompletionsEnvelope, *schemas.BifrostError) {
	switch innerProvider {
	case ZedInnerAnthropic:
		inner, bErr := toInnerAnthropicRequest(ctx, request)
		if bErr != nil {
			return nil, bErr
		}
		return buildCompletionsEnvelope(ctx, provider.networkConfig.ExtraHeaders, innerProvider, request.Model, inner), nil
	case ZedInnerGoogle:
		inner, bErr := toInnerGeminiRequest(ctx, request)
		if bErr != nil {
			return nil, bErr
		}
		return buildCompletionsEnvelope(ctx, provider.networkConfig.ExtraHeaders, innerProvider, request.Model, inner), nil
	default:
		// OpenAI inner family speaks Responses (input[]), so route the chat
		// request through the Responses converter first.
		responsesReq := request.ToResponsesRequest()
		if responsesReq == nil {
			return nil, providerUtils.NewBifrostOperationError(schemas.ErrRequestBodyConversion, errNilOpenAIRequest)
		}
		threadID := resolveThreadID(ctx, provider.networkConfig.ExtraHeaders)
		inner, bErr := toInnerOpenAIRequest(ctx, responsesReq, threadID)
		if bErr != nil {
			return nil, bErr
		}
		envelope := buildCompletionsEnvelope(ctx, provider.networkConfig.ExtraHeaders, ZedInnerOpenAI, request.Model, inner)
		// The open_ai branch needs prompt_cache_key=thread_id; reuse the
		// envelope's own thread so the two never diverge.
		inner.PromptCacheKey = &envelope.ThreadID
		_ = key
		return envelope, nil
	}
}

// ChatCompletionStream performs a streaming chat completion request to Zed's
// /completions. Each NDJSON event converts through the inner family's
// streaming converter and is forwarded as a Bifrost chat chunk.
func (provider *ZedProvider) ChatCompletionStream(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, postHookSpanFinalizer func(context.Context), key schemas.Key, request *schemas.BifrostChatRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	if request == nil {
		return nil, providerUtils.NewBifrostOperationError("chat completion stream request is nil", nil)
	}
	innerProvider, bErr := provider.resolveInnerProvider(ctx, key, request.Model)
	if bErr != nil {
		return nil, bErr
	}
	envelope, bErr := provider.buildChatEnvelope(ctx, key, request, innerProvider)
	if bErr != nil {
		return nil, bErr
	}
	authValue, isOAuth, bErr := provider.resolveCredentials(ctx, key)
	if bErr != nil {
		return nil, bErr
	}
	stream, err := provider.openCompletionsStream(ctx, postHookRunner, postHookSpanFinalizer, authValue, envelope, request.Model, innerProvider)
	if err != nil && isOAuth && isUnauthorized(err) {
		invalidateCredentials(&key)
		if authValue, _, bErr = provider.resolveCredentials(ctx, key); bErr != nil {
			return nil, bErr
		}
		return provider.openCompletionsStream(ctx, postHookRunner, postHookSpanFinalizer, authValue, envelope, request.Model, innerProvider)
	}
	return stream, err
}

// openCompletionsStream performs one POST /completions and forwards converted
// NDJSON events as chat chunks.
func (provider *ZedProvider) openCompletionsStream(
	ctx *schemas.BifrostContext,
	postHookRunner schemas.PostHookRunner,
	postHookSpanFinalizer func(context.Context),
	authValue string,
	envelope *ZedCompletionsEnvelope,
	requestModel string,
	innerProvider string,
) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	jsonBody, bErr := marshalEnvelope(envelope)
	if bErr != nil {
		return nil, bErr
	}
	url := provider.networkConfig.BaseURL + providerUtils.GetPathFromContext(ctx, provider.chatPath)

	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	resp.StreamBody = true
	defer fasthttp.ReleaseRequest(req)

	req.Header.SetMethod(http.MethodPost)
	req.SetRequestURI(url)
	req.Header.SetContentType("application/json")
	for k, v := range BuildHeaders(ctx, provider.networkConfig.ExtraHeaders, true) {
		req.Header.Set(k, v)
	}
	req.Header.Set("Authorization", authValue)
	providerUtils.SetExtraHeaders(ctx, req, provider.networkConfig.ExtraHeaders, nil)
	req.SetBody(jsonBody)

	sendBackRawRequest := providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest)
	sendBackRawResponse := providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse)

	startTime := time.Now()
	doErr := providerUtils.DoStreamingRequest(ctx, provider.streamingClient, req, resp)
	latency := time.Since(startTime)
	if doErr != nil {
		defer providerUtils.ReleaseStreamingResponse(ctx, resp)
		return nil, providerUtils.EnrichError(ctx, streamDoError(doErr), jsonBody, nil, sendBackRawRequest, sendBackRawResponse, latency)
	}
	ctx.SetValue(schemas.BifrostContextKeyProviderResponseHeaders, providerUtils.ExtractProviderResponseHeaders(resp))
	if resp.StatusCode() != fasthttp.StatusOK {
		defer providerUtils.ReleaseStreamingResponse(ctx, resp)
		body := append([]byte(nil), resp.Body()...)
		return nil, providerUtils.EnrichError(ctx, providerUtils.SetErrorLatency(parseZedError(resp), latency),
			jsonBody, body, sendBackRawRequest, sendBackRawResponse, latency)
	}
	if providerUtils.SetupStreamingPassthrough(ctx, resp) {
		responseChan := make(chan *schemas.BifrostStreamChunk)
		providerUtils.CloseStream(ctx, responseChan)
		return responseChan, nil
	}

	responseChan := make(chan *schemas.BifrostStreamChunk, schemas.DefaultStreamBufferSize)

	go func() {
		defer providerUtils.EnsureStreamFinalizerCalled(ctx, postHookSpanFinalizer)
		defer func() {
			if ctx.Err() == context.Canceled {
				providerUtils.HandleStreamCancellation(ctx, postHookRunner, responseChan, provider.logger, postHookSpanFinalizer, jsonBody)
			} else if ctx.Err() == context.DeadlineExceeded {
				providerUtils.HandleStreamTimeout(ctx, postHookRunner, responseChan, provider.logger, postHookSpanFinalizer, jsonBody)
			}
			providerUtils.CloseStream(ctx, responseChan)
		}()
		defer providerUtils.ReleaseStreamingResponse(ctx, resp)

		if resp.BodyStream() == nil {
			bErr := providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseEmpty, errEmptyStreamBody)
			ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
			providerUtils.ProcessAndSendBifrostError(ctx, postHookRunner, providerUtils.EnrichError(ctx, bErr, jsonBody, nil, sendBackRawRequest, sendBackRawResponse, latency), responseChan, provider.logger, postHookSpanFinalizer)
			return
		}

		forwarder := newChatForwarder(ctx, postHookRunner, responseChan, requestModel, innerProvider, jsonBody, startTime)
		streamErr := readZedStream(ctx, resp, func(line []byte) *schemas.BifrostError {
			return forwarder.feedLine(ctx, line, sendBackRawResponse)
		})
		if streamErr != nil {
			ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
			providerUtils.ProcessAndSendBifrostError(ctx, postHookRunner, providerUtils.EnrichError(ctx, streamErr, jsonBody, nil, sendBackRawRequest, sendBackRawResponse, latency), responseChan, provider.logger, postHookSpanFinalizer)
			return
		}
		forwarder.finish(ctx, startTime, sendBackRawRequest)
	}()

	return responseChan, nil
}
