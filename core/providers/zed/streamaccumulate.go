package zed

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/providers/anthropic"
	"github.com/maximhq/bifrost/core/providers/gemini"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

// doCompletionsUnary sends one POST /completions with the envelope and
// accumulates the NDJSON stream into a single BifrostChatResponse. Zed has no
// non-streaming mode, so unary Bifrost calls always take this path.
func (provider *ZedProvider) doCompletionsUnary(
	ctx *schemas.BifrostContext,
	key schemas.Key,
	envelope *ZedCompletionsEnvelope,
	requestModel string,
	innerProvider string,
) (*schemas.BifrostChatResponse, *schemas.BifrostError) {
	authValue, isOAuth, bErr := provider.resolveCredentials(ctx, key)
	if bErr != nil {
		return nil, bErr
	}
	resp, err := provider.postCompletionsAccumulate(ctx, authValue, envelope, requestModel, innerProvider)
	if err != nil && isOAuth && isUnauthorized(err) {
		invalidateCredentials(&key)
		if authValue, _, bErr = provider.resolveCredentials(ctx, key); bErr != nil {
			return nil, bErr
		}
		return provider.postCompletionsAccumulate(ctx, authValue, envelope, requestModel, innerProvider)
	}
	return resp, err
}

// postCompletionsAccumulate performs one POST /completions and folds every
// NDJSON event into a single chat response via the inner family's streaming
// converters.
func (provider *ZedProvider) postCompletionsAccumulate(
	ctx *schemas.BifrostContext,
	authValue string,
	envelope *ZedCompletionsEnvelope,
	requestModel string,
	innerProvider string,
) (*schemas.BifrostChatResponse, *schemas.BifrostError) {
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

	startTime := time.Now()
	doErr := providerUtils.DoStreamingRequest(ctx, provider.streamingClient, req, resp)
	latency := time.Since(startTime)
	if doErr != nil {
		defer providerUtils.ReleaseStreamingResponse(ctx, resp)
		return nil, streamDoError(doErr)
	}
	ctx.SetValue(schemas.BifrostContextKeyProviderResponseHeaders, providerUtils.ExtractProviderResponseHeaders(resp))
	if resp.StatusCode() != fasthttp.StatusOK {
		defer providerUtils.ReleaseStreamingResponse(ctx, resp)
		body := append([]byte(nil), resp.Body()...)
		bErr := parseZedError(resp)
		return nil, providerUtils.EnrichError(ctx, providerUtils.SetErrorLatency(bErr, latency),
			jsonBody, body,
			providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest),
			providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse),
			latency)
	}

	acc := newChatAccumulator(requestModel, innerProvider)
	streamErr := readZedStream(ctx, resp, func(line []byte) *schemas.BifrostError {
		return acc.feedLine(ctx, line)
	})
	providerUtils.ReleaseStreamingResponse(ctx, resp)
	if streamErr != nil {
		return nil, providerUtils.EnrichError(ctx, streamErr, jsonBody, nil,
			providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest),
			providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse),
			latency)
	}
	chatResp, bErr := acc.finalize(ctx)
	if bErr != nil {
		return nil, providerUtils.EnrichError(ctx, bErr, jsonBody, nil,
			providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest),
			providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse),
			latency)
	}
	chatResp.BackfillParams(&schemas.BifrostChatRequest{Model: requestModel})
	chatResp.ExtraFields.Latency = latency.Milliseconds()
	chatResp.ExtraFields.Provider = schemas.Zed
	chatResp.ExtraFields.RequestType = schemas.ChatCompletionRequest
	chatResp.ExtraFields.OriginalModelRequested = requestModel
	chatResp.ExtraFields.ResolvedModelUsed = requestModel
	if providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest) {
		providerUtils.ParseAndSetRawRequest(&chatResp.ExtraFields, jsonBody)
	}
	return chatResp, nil
}

// marshalEnvelope encodes the /completions envelope with sorted keys for
// prompt-cache-stable bytes.
func marshalEnvelope(envelope *ZedCompletionsEnvelope) ([]byte, *schemas.BifrostError) {
	body, err := providerUtils.MarshalSorted(envelope)
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderRequestMarshal, err)
	}
	return body, nil
}

// streamDoError classifies a failed streaming Do.
func streamDoError(doErr error) *schemas.BifrostError {
	if doErr == context.Canceled {
		return &schemas.BifrostError{
			IsBifrostError: false,
			Error: &schemas.ErrorField{
				Type:    schemas.Ptr(schemas.RequestCancelled),
				Message: schemas.ErrRequestCancelled,
				Error:   doErr,
			},
		}
	}
	if doErr == fasthttp.ErrTimeout || doErr == context.DeadlineExceeded {
		return providerUtils.NewBifrostTimeoutError(schemas.ErrProviderRequestTimedOut, doErr)
	}
	return providerUtils.NewBifrostOperationError(schemas.ErrProviderDoRequest, doErr)
}

// chatAccumulator folds a /completions NDJSON stream into one chat response
// using the inner family's streaming converters. One accumulator serves one
// unary request; it is not safe for concurrent use.
type chatAccumulator struct {
	model             string
	innerProvider     string
	anthropicState    *anthropic.AnthropicStreamState
	geminiState       *gemini.GeminiStreamState
	responsesState    *schemas.ChatToResponsesStreamState
	parts             []*schemas.BifrostChatResponse
	usage             *schemas.BifrostLLMUsage
	finishReason      *string
	messageID         string
	sawTerminal       bool
	sawFamilyTerminal bool
}

// newChatAccumulator creates an accumulator with per-family stream state.
func newChatAccumulator(model, innerProvider string) *chatAccumulator {
	return &chatAccumulator{
		model:          model,
		innerProvider:  innerProvider,
		anthropicState: anthropic.NewAnthropicStreamState(),
		geminiState:    gemini.NewGeminiStreamState(),
		responsesState: schemas.AcquireChatToResponsesStreamState(),
		usage:          &schemas.BifrostLLMUsage{},
	}
}

// feedLine parses one NDJSON line and folds its event into the accumulator.
// The terminal {"status":"stream_ended"} line only marks completion.
//
// Usage is folded here, not in the per-event converters: the shared family
// converters drop usage on most events (Anthropic's only surfaces on
// message_start/message_delta), so the accumulator reads the usage object
// straight off the wire event for every family.
func (a *chatAccumulator) feedLine(ctx *schemas.BifrostContext, line []byte) *schemas.BifrostError {
	var parsed ZedStreamLine
	if err := sonic.Unmarshal(line, &parsed); err != nil {
		return nil // skip undecodable lines; the terminal marker still ends the stream
	}
	if parsed.Status != nil {
		if *parsed.Status == ZedStreamEnded {
			a.sawTerminal = true
		}
		return nil
	}
	if parsed.Event == nil {
		return nil
	}
	a.foldEventUsage(parsed.Event.Raw)
	eventBytes, err := sonic.Marshal(parsed.Event.Raw)
	if err != nil || len(eventBytes) == 0 {
		return nil
	}
	switch a.innerProvider {
	case ZedInnerAnthropic:
		return a.feedAnthropicEvent(ctx, eventBytes)
	case ZedInnerGoogle:
		return a.feedGeminiEvent(eventBytes)
	default:
		return a.feedOpenAIEvent(eventBytes)
	}
}

// foldEventUsage extracts usage from one raw wire event for every family:
//   - anthropic: event.message.usage (message_start) or event.usage (message_delta)
//   - google: event.usageMetadata (GenerateContentResponse field)
//   - open_ai: event.response.usage (response.completed)
//
// All three report cumulative per-request totals, so counters keep the max.
func (a *chatAccumulator) foldEventUsage(raw map[string]interface{}) {
	if raw == nil {
		return
	}
	switch a.innerProvider {
	case ZedInnerAnthropic:
		if msg, ok := raw["message"].(map[string]interface{}); ok {
			a.foldAnthropicUsage(msg["usage"])
		}
		a.foldAnthropicUsage(raw["usage"])
	case ZedInnerGoogle:
		a.foldGeminiUsage(raw["usageMetadata"])
	default:
		if resp, ok := raw["response"].(map[string]interface{}); ok {
			a.foldOpenAIUsage(resp["usage"])
		}
	}
}

// foldAnthropicUsage folds one Anthropic usage object. InputTokens excludes
// the cache counters (they are separate fields), so the billable prompt
// total is input + cache_read + cache_creation.
func (a *chatAccumulator) foldAnthropicUsage(v interface{}) {
	u, ok := v.(map[string]interface{})
	if !ok {
		return
	}
	input := intOf(u["input_tokens"])
	cacheRead := intOf(u["cache_read_input_tokens"])
	cacheCreation := intOf(u["cache_creation_input_tokens"])
	output := intOf(u["output_tokens"])
	if prompt := input + cacheRead + cacheCreation; prompt > a.usage.PromptTokens {
		a.usage.PromptTokens = prompt
	}
	if output > a.usage.CompletionTokens {
		a.usage.CompletionTokens = output
	}
	if total := a.usage.PromptTokens + a.usage.CompletionTokens; total > a.usage.TotalTokens {
		a.usage.TotalTokens = total
	}
	if cacheRead > 0 || cacheCreation > 0 {
		if a.usage.PromptTokensDetails == nil {
			a.usage.PromptTokensDetails = &schemas.ChatPromptTokensDetails{}
		}
		if cacheRead > a.usage.PromptTokensDetails.CachedReadTokens {
			a.usage.PromptTokensDetails.CachedReadTokens = cacheRead
		}
		if cacheCreation > a.usage.PromptTokensDetails.CachedWriteTokens {
			a.usage.PromptTokensDetails.CachedWriteTokens = cacheCreation
		}
	}
}

// foldGeminiUsage folds one Gemini usageMetadata object.
func (a *chatAccumulator) foldGeminiUsage(v interface{}) {
	u, ok := v.(map[string]interface{})
	if !ok {
		return
	}
	if prompt := intOf(u["promptTokenCount"]); prompt > a.usage.PromptTokens {
		a.usage.PromptTokens = prompt
	}
	if completion := intOf(u["candidatesTokenCount"]); completion > a.usage.CompletionTokens {
		a.usage.CompletionTokens = completion
	}
	if total := intOf(u["totalTokenCount"]); total > a.usage.TotalTokens {
		a.usage.TotalTokens = total
	}
	if total := a.usage.PromptTokens + a.usage.CompletionTokens; total > a.usage.TotalTokens {
		a.usage.TotalTokens = total
	}
}

// foldOpenAIUsage folds one OpenAI Responses usage object.
func (a *chatAccumulator) foldOpenAIUsage(v interface{}) {
	u, ok := v.(map[string]interface{})
	if !ok {
		return
	}
	if prompt := intOf(u["input_tokens"]); prompt > a.usage.PromptTokens {
		a.usage.PromptTokens = prompt
	}
	if completion := intOf(u["output_tokens"]); completion > a.usage.CompletionTokens {
		a.usage.CompletionTokens = completion
	}
	if total := intOf(u["total_tokens"]); total > a.usage.TotalTokens {
		a.usage.TotalTokens = total
	}
	if total := a.usage.PromptTokens + a.usage.CompletionTokens; total > a.usage.TotalTokens {
		a.usage.TotalTokens = total
	}
	if details, ok := u["input_tokens_details"].(map[string]interface{}); ok {
		if a.usage.PromptTokensDetails == nil {
			a.usage.PromptTokensDetails = &schemas.ChatPromptTokensDetails{}
		}
		if cached := intOf(details["cached_tokens"]); cached > a.usage.PromptTokensDetails.CachedReadTokens {
			a.usage.PromptTokensDetails.CachedReadTokens = cached
		}
	}
	if details, ok := u["output_tokens_details"].(map[string]interface{}); ok {
		if a.usage.CompletionTokensDetails == nil {
			a.usage.CompletionTokensDetails = &schemas.ChatCompletionTokensDetails{}
		}
		if reasoning := intOf(details["reasoning_tokens"]); reasoning > a.usage.CompletionTokensDetails.ReasoningTokens {
			a.usage.CompletionTokensDetails.ReasoningTokens = reasoning
		}
	}
}

// intOf coerces a decoded JSON number (float64, int, int32, int64,
// json.Number) to int. Anything else is 0.
func intOf(v interface{}) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case float32:
		return int(n)
	case int:
		return n
	case int32:
		return int(n)
	case int64:
		return int(n)
	case json.Number:
		i, _ := n.Int64()
		return int(i)
	default:
		return 0
	}
}

// finalize assembles the accumulated deltas into one chat response.
func (a *chatAccumulator) finalize(ctx *schemas.BifrostContext) (*schemas.BifrostChatResponse, *schemas.BifrostError) {
	defer schemas.ReleaseChatToResponsesStreamState(a.responsesState)
	if !a.sawTerminal {
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderStreamTruncated, errStreamTruncated)
	}
	assembled := assembleChatParts(a.parts, a.model)
	if a.messageID != "" {
		assembled.ID = a.messageID
	}
	assembled.Usage = a.usage
	if len(assembled.Choices) > 0 && a.finishReason != nil {
		assembled.Choices[0].FinishReason = a.finishReason
	}
	_ = ctx
	return assembled, nil
}
