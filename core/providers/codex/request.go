package codex

import (
	"strings"

	"github.com/maximhq/bifrost/core/providers/openai"
	"github.com/maximhq/bifrost/core/schemas"
)

// withCodexRequestDefaults applies the Codex wire rules to one Responses
// request before it is sent:
//
//   - store defaults to false. Codex rejects store=true with "Store must be
//     set to false"; an explicit true is left for the upstream to report.
//   - max_output_tokens, truncation, background, user, prompt_cache_retention
//     and safety_identifier are stripped. Codex answers each with 400
//     "Unsupported parameter" (see open-sse/executors/codex.ts).
//   - system messages convert to developer in place. The Responses cache key
//     covers input but not instructions, so converting keeps system prompts in
//     the cacheable prefix instead of moving them out of it.
func withCodexRequestDefaults(wireReq *openai.OpenAIResponsesRequest) *openai.OpenAIResponsesRequest {
	if wireReq == nil {
		return wireReq
	}

	if wireReq.Store == nil {
		wireReq.Store = schemas.Ptr(false)
	}

	wireReq.MaxOutputTokens = nil
	wireReq.Truncation = nil
	wireReq.Background = nil
	wireReq.User = nil
	wireReq.PromptCacheRetention = nil
	wireReq.SafetyIdentifier = nil

	for i := range wireReq.Input.OpenAIResponsesRequestInputArray {
		msg := &wireReq.Input.OpenAIResponsesRequestInputArray[i]
		if msg.Role == nil || *msg.Role != schemas.ResponsesInputMessageRoleSystem {
			continue
		}
		if msg.Type != nil && *msg.Type != schemas.ResponsesMessageTypeMessage {
			continue
		}
		role := schemas.ResponsesInputMessageRoleDeveloper
		msg.Role = &role
	}

	return wireReq
}

// normalizePlan lowercases and trims a plan_type for comparison.
func normalizePlan(plan string) string {
	return strings.ToLower(strings.TrimSpace(plan))
}

// planAllowed reports whether plan may see a model advertising plans.
func planAllowed(plan string, plans []string) bool {
	plan = normalizePlan(plan)
	if plan == "" {
		return false
	}
	for _, p := range plans {
		if normalizePlan(p) == plan {
			return true
		}
	}
	return false
}
