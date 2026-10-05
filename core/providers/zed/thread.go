package zed

import (
	"github.com/google/uuid"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
)

// resolveThreadID returns the Zed thread_id for one request: the caller's
// session id when the harness or client supplied one, or a fresh UUID v4.
// A stable thread_id across turns keeps Zed's server-side conversation and
// prompt-cache affinity; a fresh one isolates the request.
func resolveThreadID(ctx *schemas.BifrostContext, configExtraHeaders map[string]string) string {
	if sid := providerUtils.ResolveSessionID(ctx, configExtraHeaders); sid != nil {
		return *sid
	}
	return uuid.NewString()
}

// resolvePromptID mints a fresh prompt id per request. Zed treats prompt_id
// as the per-turn identifier (like a request id), so unlike thread_id it is
// never reused from the session.
func resolvePromptID() string {
	return uuid.NewString()
}

// buildCompletionsEnvelope wraps a converted inner-family request in Zed's
// /completions envelope for the given model.
func buildCompletionsEnvelope(ctx *schemas.BifrostContext, configExtraHeaders map[string]string, innerProvider, model string, providerRequest interface{}) *ZedCompletionsEnvelope {
	return &ZedCompletionsEnvelope{
		ThreadID:        resolveThreadID(ctx, configExtraHeaders),
		PromptID:        resolvePromptID(),
		Provider:        innerProvider,
		Model:           model,
		ProviderRequest: providerRequest,
	}
}
