package opencodezenfree

import (
	"strings"

	"github.com/maximhq/bifrost/core/schemas"
)

// Upstream endpoint paths served by the Opencode Zen gateway. Each model
// family speaks exactly one of these wire formats:
//   - /v1/responses and /v1/chat/completions: OpenAI formats
//   - /v1/messages: Anthropic wire format
//   - /v1/models/<model>: Google Generative Language format (@ai-sdk/google)
const (
	opencodeZenFreeChatPath      = "/v1/chat/completions"
	opencodeZenFreeResponsesPath = "/v1/responses"
	opencodeZenFreeMessagesPath  = "/v1/messages"
	// opencodeZenFreeGeminiModelsPrefix prefixes the per-model Google
	// Generative Language path (/v1/models/<model>).
	opencodeZenFreeGeminiModelsPrefix = "/v1/models/"
)

// opencodeZenFreeEndpoint is one upstream route a model can take.
type opencodeZenFreeEndpoint int

const (
	opencodeZenFreeEndpointResponses opencodeZenFreeEndpoint = iota
	opencodeZenFreeEndpointChat
	opencodeZenFreeEndpointMessages
	opencodeZenFreeEndpointGemini
)

// path returns the URL path suffix for the endpoint. Gemini resolves
// per-model (/v1/models/<model>), so it reports only its prefix.
func (e opencodeZenFreeEndpoint) path() string {
	switch e {
	case opencodeZenFreeEndpointChat:
		return opencodeZenFreeChatPath
	case opencodeZenFreeEndpointMessages:
		return opencodeZenFreeMessagesPath
	case opencodeZenFreeEndpointGemini:
		return opencodeZenFreeGeminiModelsPrefix
	default:
		return opencodeZenFreeResponsesPath
	}
}

// isGeminiModelName reports whether the bare model name belongs to the Gemini
// family, which the gateway serves on the Google Generative Language path.
// Terminal: Gemini models never enter the OpenAI/Anthropic fallback chain.
func isGeminiModelName(bareModel string) bool {
	lower := strings.ToLower(strings.TrimSpace(bareModel))
	return strings.HasPrefix(lower, "gemini")
}

// bareOpencodeModelName strips a provider prefix that just repeats the
// gateway ("opencode-zen/space-bunny-free" -> "space-bunny-free") so the
// classifier sees the name the datasheet and the prefix rules match on.
// Namespaced models from other families ("meta-llama/...") survive untouched.
func bareOpencodeModelName(model string) string {
	if p, bare := schemas.ParseModelString(model, ""); p != "" && bare != "" {
		if p == schemas.OpencodeZenFree || p == schemas.OpencodeZen || p == schemas.OpencodeGo {
			return bare
		}
	}
	return model
}

// classifyOpencodeZenFreeModel resolves the upstream endpoint for a model.
// Gemini-kind models are terminal. Everything else reads the datasheet's
// supported_endpoints via ResolveModelCaps (the opencode-zen-free rows fold
// onto opencode-zen); when the row says nothing it answers responses, the
// endpoint this provider historically served.
func classifyOpencodeZenFreeModel(ctx *schemas.BifrostContext, model string) opencodeZenFreeEndpoint {
	bare := bareOpencodeModelName(model)
	if isGeminiModelName(bare) {
		return opencodeZenFreeEndpointGemini
	}
	caps := schemas.ResolveModelCaps(schemas.OpencodeZenFree, schemas.ResolveCanonicalModel(ctx, model))
	if caps.SupportsEndpoint(opencodeZenFreeMessagesPath, false) {
		return opencodeZenFreeEndpointMessages
	}
	if caps.SupportsEndpoint(opencodeZenFreeChatPath, false) {
		return opencodeZenFreeEndpointChat
	}
	return opencodeZenFreeEndpointResponses
}

// opencodeZenFreeFallbackChain is the pre-response-error fallback order for
// non-Gemini models: chat first, then responses, then messages. The caller
// rotates it so the classified primary leads and the rest follow without
// repeating.
func opencodeZenFreeFallbackChain(primary opencodeZenFreeEndpoint) []opencodeZenFreeEndpoint {
	order := []opencodeZenFreeEndpoint{
		opencodeZenFreeEndpointChat,
		opencodeZenFreeEndpointResponses,
		opencodeZenFreeEndpointMessages,
	}
	chain := make([]opencodeZenFreeEndpoint, 0, len(order))
	chain = append(chain, primary)
	for _, e := range order {
		if e != primary {
			chain = append(chain, e)
		}
	}
	return chain
}

// isUnsupportedFormatError reports whether an upstream error signals the
// model does not speak the attempted wire format (e.g. "Model
// space-bunny-free is not supported for format openai"). Only these errors
// may advance the endpoint fallback chain; auth, rate-limit, quota, and
// content errors must surface untouched.
func isUnsupportedFormatError(bifrostErr *schemas.BifrostError) bool {
	if bifrostErr == nil || bifrostErr.Error == nil {
		return false
	}
	msg := strings.ToLower(bifrostErr.Error.Message)
	if msg == "" {
		return false
	}
	if strings.Contains(msg, "not supported for format") {
		return true
	}
	if strings.Contains(msg, "unsupported format") || strings.Contains(msg, "unsupported media") {
		return true
	}
	if strings.Contains(msg, "does not support") && strings.Contains(msg, "format") {
		return true
	}
	return false
}
