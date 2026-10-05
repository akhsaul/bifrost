package zed

import (
	"strings"

	"github.com/maximhq/bifrost/core/schemas"
)

// BuildHeaders constructs the Zed client-identity headers for one request.
// Operator-provided headers take priority over defaults in this order:
// config-level extra_headers, then per-request context extra headers.
//
// NOTE: authorization is intentionally NOT set here. It is resolved from the
// key's credentials (user login vs LLM bearer token) and applied last by the
// caller, so a stale or foreign Authorization value can never leak onto the
// wire. Transport-level headers (accept, accept-encoding, connection,
// content-type, host) are also left out: the request helpers and fasthttp
// own those.
func BuildHeaders(ctx *schemas.BifrostContext, configExtraHeaders map[string]string, forCompletions bool) map[string]string {
	headers := map[string]string{
		"user-agent": DefaultUserAgent,
	}
	if forCompletions {
		headers["x-zed-version"] = DefaultVersion
		headers["x-zed-client-supports-status-messages"] = "true"
		headers["x-zed-client-supports-stream-ended-request-completion-status"] = "true"
	} else {
		headers["x-zed-system-id"] = resolveSystemID(nil)
	}

	// Config-level extra headers override defaults.
	for k, v := range configExtraHeaders {
		if strings.EqualFold(k, "authorization") {
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
				if strings.EqualFold(k, "authorization") {
					continue
				}
				setHeaderCaseInsensitive(headers, k, vs[0])
			}
		}
	}

	return headers
}

// modelsHeaders builds the headers for GET /models: identity headers plus
// the x-ai capability flag observed in the live capture.
func modelsHeaders(ctx *schemas.BifrostContext, configExtraHeaders map[string]string, bearerToken string) map[string]string {
	headers := BuildHeaders(ctx, configExtraHeaders, true)
	headers["x-zed-client-supports-x-ai"] = "true"
	headers["Authorization"] = "Bearer " + bearerToken
	return headers
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
