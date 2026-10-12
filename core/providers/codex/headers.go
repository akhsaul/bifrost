package codex

import (
	"strconv"
	"strings"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
)

// BuildHeaders constructs the Codex client-identity headers for one request.
// Operator-provided headers take priority over defaults in this order:
// config-level extra_headers, then per-request context extra headers.
//
// NOTE: authorization and chatgpt-account-id are intentionally NOT set here.
// They are resolved from the key's credentials (static token vs OAuth) and
// applied last by the caller, so a stale or foreign value can never leak onto
// the wire. Transport-level headers (accept, accept-encoding, connection,
// content-type, host) are also left out: the openai helpers and fasthttp own
// those.
func BuildHeaders(ctx *schemas.BifrostContext, configExtraHeaders map[string]string) map[string]string {
	headers := map[string]string{
		"originator": DefaultOriginator,
		"user-agent": DefaultUserAgent,
		"version":    DefaultVersion,
	}

	// Config-level extra headers override defaults (except the credential-owned
	// ones, which the caller applies last).
	for k, v := range configExtraHeaders {
		if isCredentialHeader(k) {
			continue
		}
		setHeaderCaseInsensitive(headers, k, v)
	}

	// Per-request context extra headers have the highest priority.
	if ctx != nil {
		if ctxHeaders, ok := ctx.Value(schemas.BifrostContextKeyExtraHeaders).(map[string][]string); ok {
			for k, vs := range ctxHeaders {
				if len(vs) == 0 {
					continue
				}
				if isCredentialHeader(k) {
					continue
				}
				setHeaderCaseInsensitive(headers, k, vs[0])
			}
		}
	}

	return headers
}

// authHeadersFor builds the authHeader map (applied after extra headers, so
// it always wins) for one request's resolved credentials: the bearer token
// always, and chatgpt-account-id only when an account is bound.
func authHeadersFor(creds *codexCredentials) map[string]string {
	headers := map[string]string{
		"Authorization": "Bearer " + creds.AccessToken,
	}
	if strings.TrimSpace(creds.AccountID) != "" {
		headers["chatgpt-account-id"] = creds.AccountID
	}
	return headers
}

// isCredentialHeader reports whether a header name is credential-owned and
// therefore must only be set from resolved credentials, never from operator
// or per-request overrides.
func isCredentialHeader(name string) bool {
	return strings.EqualFold(name, "authorization") ||
		strings.EqualFold(name, "chatgpt-account-id")
}

// setHeaderCaseInsensitive sets a header, replacing any existing key that
// matches case-insensitively so defaults cannot linger under another casing.
func setHeaderCaseInsensitive(headers map[string]string, key, value string) {
	for existingKey := range headers {
		if strings.EqualFold(existingKey, key) {
			delete(headers, existingKey)
			break
		}
	}
	headers[key] = value
}

// itoa formats a small int without importing strconv at the call site.
func itoa(i int) string { return strconv.Itoa(i) }

// warn logs through the provider logger when one is configured. Providers are
// constructed with a nil logger in unit tests, so this must stay nil-safe.
func warn(logger schemas.Logger, format string, args ...interface{}) {
	if logger != nil {
		logger.Warn(format, args...)
	}
}

// silentPostHookRunner passes streaming chunks through untouched. Unary
// responses are served via stream fan-in (forcing stream:true and
// accumulating the SSE stream into one response), so there is no caller
// post-hook to run per chunk; the core runs the plugin pipeline once on the
// final unary response.
func silentPostHookRunner(_ *schemas.BifrostContext, result *schemas.BifrostResponse, bifrostErr *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError) {
	return result, bifrostErr
}

// wrapResponsesToChatStreamPostHookRunner adapts responses stream events to
// chat responses for post-hooks and client chunks.
func wrapResponsesToChatStreamPostHookRunner(postHookRunner schemas.PostHookRunner) schemas.PostHookRunner {
	return func(ctx *schemas.BifrostContext, result *schemas.BifrostResponse, bifrostErr *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError) {
		if result != nil && result.ResponsesStreamResponse != nil {
			if converted := result.ResponsesStreamResponse.ToBifrostChatResponse(); converted != nil {
				result = &schemas.BifrostResponse{ChatResponse: converted}
			}
		}
		if postHookRunner != nil {
			return postHookRunner(ctx, result, bifrostErr)
		}
		return result, bifrostErr
	}
}

// checkOperationAllowed gates a request type on the provider's
// custom_provider_config, mirroring what the openai provider does per method.
func checkOperationAllowed(provider *CodexProvider, op schemas.RequestType) *schemas.BifrostError {
	return providerUtils.CheckOperationAllowed(schemas.Codex, provider.customProviderConfig, op)
}
