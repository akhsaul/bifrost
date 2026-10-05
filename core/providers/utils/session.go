package utils

import (
	"strings"

	"github.com/maximhq/bifrost/core/schemas"
)

// ResolveSessionID resolves the session ID for one request with the following priority:
//
//  1. Provider-specific header names (headerNames...) looked up
//     case-insensitively in per-request context extra headers
//     (BifrostContextKeyExtraHeaders) first, then in the provider config
//     extra headers (configExtraHeaders). An explicit per-request header
//     always wins.
//  2. BifrostContextKeySessionID from ctx. The transport fills this from
//     x-bf-session-id or any of the harness session headers
//     (schemas.HarnessSessionHeaders), so every coding harness gets session
//     stickiness without a code change per provider.
//  3. nil when no session ID is present anywhere.
//
// The return is *string (not string): callers generate their own session ID
// in the upstream's native format when this returns nil (zed needs a UUID v4,
// opencode needs ses_<hex><base62>, modal sends nothing at all).
func ResolveSessionID(ctx *schemas.BifrostContext, configExtraHeaders map[string]string, headerNames ...string) *string {
	if v := lookupSessionHeader(ctx, configExtraHeaders, headerNames); v != "" {
		return &v
	}
	if ctx != nil {
		if sid, ok := ctx.Value(schemas.BifrostContextKeySessionID).(string); ok && strings.TrimSpace(sid) != "" {
			trimmed := strings.TrimSpace(sid)
			return &trimmed
		}
	}
	return nil
}

// lookupSessionHeader searches headerNames in context extra headers first,
// then in config extra headers. Matching is case-insensitive. Returns the
// first non-blank value, or "" when nothing matches.
func lookupSessionHeader(ctx *schemas.BifrostContext, configExtraHeaders map[string]string, headerNames []string) string {
	if len(headerNames) == 0 {
		return ""
	}
	if ctx != nil {
		if ctxHeaders, ok := ctx.Value(schemas.BifrostContextKeyExtraHeaders).(map[string][]string); ok {
			for k, vs := range ctxHeaders {
				for _, name := range headerNames {
					if strings.EqualFold(k, name) && len(vs) > 0 && strings.TrimSpace(vs[0]) != "" {
						return strings.TrimSpace(vs[0])
					}
				}
			}
		}
	}
	for k, v := range configExtraHeaders {
		for _, name := range headerNames {
			if strings.EqualFold(k, name) && strings.TrimSpace(v) != "" {
				return strings.TrimSpace(v)
			}
		}
	}
	return ""
}
