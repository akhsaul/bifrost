package zed

import (
	"context"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
)

// Responses performs a responses request via Zed's /completions.
//
// Only the inner open_ai family speaks Responses natively. For anthropic and
// google models the request converts to chat (ResponsesRequest -> ChatRequest,
// the same fallback cline/modal use) and the chat result converts back.
func (provider *ZedProvider) Responses(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostResponsesRequest) (*schemas.BifrostResponsesResponse, *schemas.BifrostError) {
	if request == nil {
		return nil, providerUtils.NewBifrostOperationError("responses request is nil", nil)
	}
	innerProvider, bErr := provider.resolveInnerProvider(ctx, key, request.Model)
	if bErr != nil {
		return nil, bErr
	}
	if innerProvider != ZedInnerOpenAI {
		chatResponse, err := provider.ChatCompletion(ctx, key, request.ToChatRequest())
		if err != nil {
			return nil, err
		}
		return chatResponse.ToBifrostResponsesResponse(), nil
	}
	threadID := resolveThreadID(ctx, provider.networkConfig.ExtraHeaders)
	inner, bErr := toInnerOpenAIRequest(ctx, request, threadID)
	if bErr != nil {
		return nil, bErr
	}
	envelope := buildCompletionsEnvelope(ctx, provider.networkConfig.ExtraHeaders, ZedInnerOpenAI, request.Model, inner)
	// Keep prompt_cache_key and thread_id identical: reuse the envelope's own
	// thread so the two never diverge.
	inner.PromptCacheKey = &envelope.ThreadID
	chatResp, err := provider.doCompletionsUnary(ctx, key, envelope, request.Model, ZedInnerOpenAI)
	if err != nil {
		return nil, err
	}
	return chatResp.ToBifrostResponsesResponse(), nil
}

// ResponsesStream performs a streaming responses request via Zed's
// /completions. Non-OpenAI models stream through the chat path with the
// Responses-to-Chat fallback flag set, matching cline/modal behavior.
func (provider *ZedProvider) ResponsesStream(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, postHookSpanFinalizer func(context.Context), key schemas.Key, request *schemas.BifrostResponsesRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	if request == nil {
		return nil, providerUtils.NewBifrostOperationError("responses stream request is nil", nil)
	}
	innerProvider, bErr := provider.resolveInnerProvider(ctx, key, request.Model)
	if bErr != nil {
		return nil, bErr
	}
	if innerProvider != ZedInnerOpenAI {
		ctx.SetValue(schemas.BifrostContextKeyIsResponsesToChatCompletionFallback, true)
		return provider.ChatCompletionStream(ctx, postHookRunner, postHookSpanFinalizer, key, request.ToChatRequest())
	}
	// Native path: Zed's inner open_ai events already ARE Responses stream
	// events, so forward them directly instead of converting chat deltas.
	threadID := resolveThreadID(ctx, provider.networkConfig.ExtraHeaders)
	inner, bErr := toInnerOpenAIRequest(ctx, request, threadID)
	if bErr != nil {
		return nil, bErr
	}
	envelope, bErr := func() (*ZedCompletionsEnvelope, *schemas.BifrostError) {
		e := buildCompletionsEnvelope(ctx, provider.networkConfig.ExtraHeaders, ZedInnerOpenAI, request.Model, inner)
		inner.PromptCacheKey = &e.ThreadID
		return e, nil
	}()
	if bErr != nil {
		return nil, bErr
	}
	authValue, isOAuth, bErr := provider.resolveCredentials(ctx, key)
	if bErr != nil {
		return nil, bErr
	}
	stream, err := provider.openResponsesStream(ctx, postHookRunner, postHookSpanFinalizer, authValue, envelope, request, inner)
	if err != nil && isOAuth && isUnauthorized(err) {
		invalidateCredentials(&key)
		if authValue, _, bErr = provider.resolveCredentials(ctx, key); bErr != nil {
			return nil, bErr
		}
		return provider.openResponsesStream(ctx, postHookRunner, postHookSpanFinalizer, authValue, envelope, request, inner)
	}
	return stream, err
}
