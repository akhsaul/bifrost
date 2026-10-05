package zed

import (
	"context"
	"net/http"
	"time"

	"github.com/bytedance/sonic"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

// openResponsesStream performs one POST /completions for a Responses request
// on an inner open_ai model and forwards Zed's inner Responses events
// directly — no chat conversion in either direction.
func (provider *ZedProvider) openResponsesStream(
	ctx *schemas.BifrostContext,
	postHookRunner schemas.PostHookRunner,
	postHookSpanFinalizer func(context.Context),
	authValue string,
	envelope *ZedCompletionsEnvelope,
	request *schemas.BifrostResponsesRequest,
	inner interface{},
) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	_ = inner
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

		lastChunkTime := startTime
		sawCompleted := false
		streamErr := readZedStream(ctx, resp, func(line []byte) *schemas.BifrostError {
			var parsed ZedStreamLine
			if err := sonic.Unmarshal(line, &parsed); err != nil {
				return nil
			}
			if parsed.Status != nil || parsed.Event == nil {
				return nil
			}
			eventBytes, err := sonic.Marshal(parsed.Event.Raw)
			if err != nil || len(eventBytes) == 0 {
				return nil
			}
			var streamResp schemas.BifrostResponsesStreamResponse
			if err := sonic.Unmarshal(eventBytes, &streamResp); err != nil {
				return nil
			}
			if streamResp.Type == schemas.ResponsesStreamResponseTypeError {
				return responsesStreamError(&streamResp)
			}
			if streamResp.Type == schemas.ResponsesStreamResponseTypeCompleted ||
				streamResp.Type == schemas.ResponsesStreamResponseTypeIncomplete {
				if sendBackRawRequest {
					providerUtils.ParseAndSetRawRequest(&streamResp.ExtraFields, jsonBody)
				}
				streamResp.ExtraFields.Latency = time.Since(startTime).Milliseconds()
				ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
				providerUtils.ProcessAndSendResponse(ctx, postHookRunner,
					providerUtils.GetBifrostResponseForStreamResponse(nil, nil, &streamResp, nil, nil, nil), responseChan, postHookSpanFinalizer)
				sawCompleted = true
				return nil
			}
			streamResp.ExtraFields.ChunkIndex = streamResp.SequenceNumber
			streamResp.ExtraFields.Latency = time.Since(lastChunkTime).Milliseconds()
			lastChunkTime = now()
			if sendBackRawResponse {
				streamResp.ExtraFields.RawResponse = string(line)
			}
			providerUtils.ProcessAndSendResponse(ctx, postHookRunner,
				providerUtils.GetBifrostResponseForStreamResponse(nil, nil, &streamResp, nil, nil, nil), responseChan, postHookSpanFinalizer)
			return nil
		})
		if streamErr != nil {
			ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
			providerUtils.ProcessAndSendBifrostError(ctx, postHookRunner, providerUtils.EnrichError(ctx, streamErr, jsonBody, nil, sendBackRawRequest, sendBackRawResponse, latency), responseChan, provider.logger, postHookSpanFinalizer)
			return
		}
		if !sawCompleted {
			providerUtils.SendStreamTruncatedError(ctx, postHookRunner, responseChan, provider.logger, postHookSpanFinalizer, jsonBody)
		}
	}()

	return responseChan, nil
}
