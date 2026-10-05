package zed

import (
	"github.com/maximhq/bifrost/core/schemas"
)

// BuildEnvelopeForTest exposes buildChatEnvelope for golden tests.
func (provider *ZedProvider) BuildEnvelopeForTest(ctx *schemas.BifrostContext, request *schemas.BifrostChatRequest, innerProvider string) *ZedCompletionsEnvelope {
	envelope, bErr := provider.buildChatEnvelope(ctx, schemas.Key{}, request, innerProvider)
	if bErr != nil {
		return nil
	}
	return envelope
}

// BuildResponsesEnvelopeForTest exposes the Responses envelope path for
// golden tests: convert + wrap with prompt_cache_key=thread_id.
func (provider *ZedProvider) BuildResponsesEnvelopeForTest(ctx *schemas.BifrostContext, request *schemas.BifrostResponsesRequest) *ZedCompletionsEnvelope {
	threadID := resolveThreadID(ctx, provider.networkConfig.ExtraHeaders)
	inner, bErr := toInnerOpenAIRequest(ctx, request, threadID)
	if bErr != nil {
		return nil
	}
	envelope := buildCompletionsEnvelope(ctx, provider.networkConfig.ExtraHeaders, ZedInnerOpenAI, request.Model, inner)
	inner.PromptCacheKey = &envelope.ThreadID
	return envelope
}
