package cline

import (
	"fmt"
	"strings"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
)

// GenerateTaskID produces a Cline task identifier matching the native shape:
// <unixmillis>_<5 alphanumerics> (e.g. 1790481333310_3iq00).
func GenerateTaskID() string {
	return fmt.Sprintf("%d_%s", time.Now().UnixMilli(), schemas.GetRandomString(5))
}

// ResolveTaskID resolves the x-task-id for one request with the following priority:
// 1. Per-request context extra headers (x-bf-eh-x-task-id from the caller)
// 2. Request body extra params (task_id or x-task-id)
// 3. Config-level extra headers (network_config.extra_headers)
// 4. Generated task ID in the native shape
func ResolveTaskID(ctx *schemas.BifrostContext, configExtraHeaders map[string]schemas.SecretVar, request *schemas.BifrostChatRequest) string {
	if ctx != nil {
		if ctxHeaders, ok := ctx.Value(schemas.BifrostContextKeyExtraHeaders).(map[string][]string); ok {
			for k, vs := range ctxHeaders {
				if strings.EqualFold(k, "x-task-id") && len(vs) > 0 && strings.TrimSpace(vs[0]) != "" {
					return vs[0]
				}
			}
		}
	}
	if request != nil {
		for _, k := range []string{"x-task-id", "task_id"} {
			if v, ok := request.GetExtraParams()[k]; ok {
				if s, ok := v.(string); ok && strings.TrimSpace(s) != "" {
					return s
				}
			}
		}
	}
	for k, v := range configExtraHeaders {
		if strings.EqualFold(k, "x-task-id") && strings.TrimSpace(v.GetValue()) != "" {
			return v.GetValue()
		}
	}
	return GenerateTaskID()
}

// setHeaderCaseInsensitive sets a header, replacing any existing key that
// matches case-insensitively so defaults cannot linger under another casing.
func setHeaderCaseInsensitive(headers map[string]schemas.SecretVar, key string, value schemas.SecretVar) {
	for existingKey := range headers {
		if strings.EqualFold(existingKey, key) {
			delete(headers, existingKey)
			break
		}
	}
	headers[key] = value
}

// BuildHeaders constructs the Cline client-identity headers for one request.
// Operator-provided headers take priority over defaults in this order:
// config-level extra_headers, then per-request context extra headers.
//
// NOTE: authorization is intentionally NOT set here. It is resolved from the
// key's credentials (static key vs OAuth) and applied last by the caller, so a
// stale or foreign Authorization value can never leak onto the wire.
// Transport-level headers (accept, accept-encoding, connection, content-type,
// host) are also left out: the openai helpers and fasthttp own those.
func BuildHeaders(ctx *schemas.BifrostContext, configExtraHeaders map[string]schemas.SecretVar, request *schemas.BifrostChatRequest) map[string]schemas.SecretVar {
	headers := map[string]schemas.SecretVar{
		"http-referer":       *schemas.NewSecretVar(DefaultHTTPReferer),
		"user-agent":         *schemas.NewSecretVar(DefaultUserAgent),
		"sec-fetch-mode":     *schemas.NewSecretVar("cors"),
		"x-client-type":      *schemas.NewSecretVar(DefaultClientType),
		"x-client-version":   *schemas.NewSecretVar(DefaultClientVers),
		"x-core-version":     *schemas.NewSecretVar(DefaultCoreVers),
		"x-is-multiroot":     *schemas.NewSecretVar(DefaultIsMultiroot),
		"x-platform":         *schemas.NewSecretVar(DefaultPlatform),
		"x-platform-version": *schemas.NewSecretVar(DefaultPlatVers),
		"x-task-id":          *schemas.NewSecretVar(ResolveTaskID(ctx, configExtraHeaders, request)),
		"x-title":            *schemas.NewSecretVar(DefaultTitle),
	}

	// Config-level extra headers override defaults (except authorization,
	// which is credential-owned, and x-task-id, already resolved above with
	// config as one of its sources).
	for k, v := range configExtraHeaders {
		if strings.EqualFold(k, "authorization") || strings.EqualFold(k, "x-task-id") {
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
				if strings.EqualFold(k, "authorization") || strings.EqualFold(k, "x-task-id") {
					continue
				}
				setHeaderCaseInsensitive(headers, k, *schemas.NewSecretVar(vs[0]))
			}
		}
	}

	return headers
}
