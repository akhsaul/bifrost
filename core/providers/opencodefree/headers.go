package opencodefree

import (
	"strings"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
)

const (
	DefaultBaseURL          = "https://opencode.ai/zen"
	DefaultUserAgent        = "HermesAgent/0.21.2"
	DefaultHTTPReferer      = "https://hermes-agent.nousresearch.com"
	DefaultTitle            = "Hermes Agent"
	StainlessArch           = "x64"
	StainlessAsync          = "false"
	StainlessLang           = "python"
	StainlessOS             = "Linux"
	StainlessPackageVersion = "2.24.0"
	StainlessReadTimeout    = "1800.0"
	StainlessRetryCount     = "0"
	StainlessRuntime        = "CPython"
	StainlessRuntimeVersion = "3.11.16"
)

// GenerateSessionID produces an OpenCode session identifier matching the format:
// YYYYMMDD_HHMMSS_<6 hex chars> (e.g. 20260912_111111_d98c49).
func GenerateSessionID() string {
	return time.Now().Format("20060102_150405") + "_" + schemas.GetRandomString(6)
}

// ResolveSessionID resolves the session ID with the following priority:
// 1. Context extra headers (REST API client headers, e.g. x-bf-eh-x-opencode-session or x-opencode-session)
// 2. Config extra headers (config.json / Web UI network_config.extra_headers)
// 3. Bifrost context session ID (session stickiness)
// 4. Dynamic session ID generation (YYYYMMDD_HHMMSS_<6 hex chars>)
func ResolveSessionID(ctx *schemas.BifrostContext, configExtraHeaders map[string]string) string {
	if ctx != nil {
		if ctxHeaders, ok := ctx.Value(schemas.BifrostContextKeyExtraHeaders).(map[string][]string); ok {
			for k, vs := range ctxHeaders {
				if strings.EqualFold(k, "x-opencode-session") && len(vs) > 0 && vs[0] != "" {
					return vs[0]
				}
			}
		}
	}
	for k, v := range configExtraHeaders {
		if strings.EqualFold(k, "x-opencode-session") && v != "" {
			return v
		}
	}
	if ctx != nil {
		if sid, ok := ctx.Value(schemas.BifrostContextKeySessionID).(string); ok && sid != "" {
			return sid
		}
	}
	return GenerateSessionID()
}

// BuildHeaders constructs the complete set of required headers for opencode-free.
// User-provided extra headers (from config.json, Web UI, or context) take priority and override defaults.
//
// NOTE: accept, accept-encoding, connection, content-type and host are
// intentionally NOT sent. accept-encoding: gzip made the upstream return a
// gzip body that the unary list-models path could not decode (it reads
// resp.Body() directly), causing "failed to unmarshal response". Verified
// live: without accept-encoding the server returns plain JSON 200.
// content-type is still set by the openai helpers themselves, and
// host/connection are managed by fasthttp.
func BuildHeaders(ctx *schemas.BifrostContext, configExtraHeaders map[string]string, isStreaming bool) map[string]string {
	_ = isStreaming
	sessionID := ResolveSessionID(ctx, configExtraHeaders)

	headers := map[string]string{
		"authorization":               "",
		"http-referer":                DefaultHTTPReferer,
		"user-agent":                  DefaultUserAgent,
		"x-opencode-session":          sessionID,
		"x-stainless-arch":            StainlessArch,
		"x-stainless-async":           StainlessAsync,
		"x-stainless-lang":            StainlessLang,
		"x-stainless-os":              StainlessOS,
		"x-stainless-package-version": StainlessPackageVersion,
		"x-stainless-read-timeout":    StainlessReadTimeout,
		"x-stainless-retry-count":     StainlessRetryCount,
		"x-stainless-runtime":         StainlessRuntime,
		"x-stainless-runtime-version": StainlessRuntimeVersion,
		"x-title":                     DefaultTitle,
	}

	// Config-level extra headers override defaults
	for k, v := range configExtraHeaders {
		// Case-insensitive key match: remove existing default key if casing differs
		for existingKey := range headers {
			if strings.EqualFold(existingKey, k) {
				delete(headers, existingKey)
				break
			}
		}
		headers[k] = v
	}

	// Per-request context extra headers have highest priority
	if ctx != nil {
		if ctxHeaders, ok := ctx.Value(schemas.BifrostContextKeyExtraHeaders).(map[string][]string); ok {
			for k, vs := range ctxHeaders {
				if len(vs) > 0 {
					for existingKey := range headers {
						if strings.EqualFold(existingKey, k) {
							delete(headers, existingKey)
							break
						}
					}
					headers[k] = vs[0]
				}
			}
		}
	}

	return headers
}
