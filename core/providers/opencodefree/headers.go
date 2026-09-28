package opencodefree

import (
	"crypto/sha1"
	"encoding/hex"
	"strings"
	"sync/atomic"
	"time"

	"crypto/rand"
	"math/big"

	"github.com/maximhq/bifrost/core/schemas"
)

const (
	DefaultBaseURL   = "https://opencode.ai/zen"
	DefaultAuth      = "Bearer public"
	DefaultClient    = "cli"
	DefaultUserAgent = "opencode/latest/2.0.18/cli"

	// DefaultInstructions mirrors the working opencode-req.json capture (typo included).
	DefaultInstructions = "You are helpfull assistant"
	// DefaultReasoningEffort / DefaultReasoningSummary are fallbacks only: client-supplied values always win.
	DefaultReasoningEffort  = "auto"
	DefaultReasoningSummary = "auto"
	// DefaultPromptCacheKeyFallback is used only when no session ID is available.
	DefaultPromptCacheKeyFallback = "global"
)

const base62Chars = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

var sessionCounter uint64

// GenerateOpencodeSessionID produces an OpenCode session identifier matching
// packages/opencode/src/id/id.ts create("ses", "descending"):
// ses_<12 hex><14 base62>, where the hex part is ~(now_ms*0x1000 + counter) masked to 48 bits.
func GenerateOpencodeSessionID() string {
	counter := atomic.AddUint64(&sessionCounter, 1)
	const mask = (uint64(1) << 48) - 1
	now0 := (uint64(time.Now().UnixMilli())*0x1000 + counter) & mask
	inv := (^now0) & mask
	random := randomBase62(14)
	return "ses_" + hex48(inv) + random
}

func hex48(v uint64) string {
	b := make([]byte, 12)
	hex.Encode(b, []byte{
		byte(v >> 40), byte(v >> 32), byte(v >> 24),
		byte(v >> 16), byte(v >> 8), byte(v),
	})
	return string(b)
}

func randomBase62(length int) string {
	out := make([]byte, length)
	for i := range out {
		n, err := rand.Int(rand.Reader, big.NewInt(62))
		if err != nil {
			out[i] = base62Chars[uint64(time.Now().UnixNano()+int64(i))%62]
			continue
		}
		out[i] = base62Chars[n.Int64()]
	}
	return string(out)
}

func randomHex(bytes int) string {
	b := make([]byte, bytes)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand must not fail; fall back to time-seeded bytes to stay total.
		for i := range b {
			b[i] = byte((time.Now().UnixNano() >> (8 * (i % 8))) & 0xff)
		}
	}
	// Guard against the all-zero IDs the trace specs forbid.
	allZero := true
	for _, v := range b {
		if v != 0 {
			allZero = false
			break
		}
	}
	if allZero {
		b[len(b)-1] = 1
	}
	return hex.EncodeToString(b)
}

// GenerateTraceHeaders returns a paired (traceparent, b3) sharing the same
// trace-id and span-id, per opencode-generate-headers.md.
func GenerateTraceHeaders() (traceparent, b3 string) {
	traceID := randomHex(16)
	spanID := randomHex(8)
	parentID := randomHex(8)
	return "00-" + traceID + "-" + spanID + "-01", traceID + "-" + spanID + "-1-" + parentID
}

// GenerateProjectID derives the x-opencode-project value from a git remote,
// matching packages/core/src/project.ts resolve() + parts():
// sha1("git-remote:<lower-host>/<path without leading/trailing slashes and .git suffix>").
// With no remote info it falls back to sha1("global").
func GenerateProjectID(host, path string) string {
	host = strings.TrimSpace(host)
	path = strings.TrimSpace(path)
	if host == "" || path == "" {
		sum := sha1.Sum([]byte("global"))
		return hex.EncodeToString(sum[:])
	}
	p := strings.TrimPrefix(path, "/")
	p = strings.TrimSuffix(p, ".git")
	p = strings.TrimSuffix(p, "/")
	p = strings.TrimPrefix(p, "/")
	norm := strings.ToLower(host) + "/" + p
	sum := sha1.Sum([]byte("git-remote:" + norm))
	return hex.EncodeToString(sum[:])
}

func lookupHeader(ctx *schemas.BifrostContext, configExtraHeaders map[string]string, names ...string) string {
	if ctx != nil {
		if ctxHeaders, ok := ctx.Value(schemas.BifrostContextKeyExtraHeaders).(map[string][]string); ok {
			for k, vs := range ctxHeaders {
				for _, name := range names {
					if strings.EqualFold(k, name) && len(vs) > 0 && vs[0] != "" {
						return vs[0]
					}
				}
			}
		}
	}
	for k, v := range configExtraHeaders {
		for _, name := range names {
			if strings.EqualFold(k, name) && v != "" {
				return v
			}
		}
	}
	return ""
}

// ResolveSessionID resolves the session ID with the following priority:
// 1. Context extra headers (x-opencode-session, x-session-affinity, x-session-id)
// 2. Config extra headers (same names)
// 3. Bifrost context session ID (session stickiness)
// 4. Dynamic session ID generation (ses_<12hex><14base62>)
func ResolveSessionID(ctx *schemas.BifrostContext, configExtraHeaders map[string]string) string {
	if v := lookupHeader(ctx, configExtraHeaders, "x-opencode-session", "x-session-affinity", "x-session-id"); v != "" {
		return v
	}
	if ctx != nil {
		if sid, ok := ctx.Value(schemas.BifrostContextKeySessionID).(string); ok && sid != "" {
			return sid
		}
	}
	return GenerateOpencodeSessionID()
}

// ResolveProjectID resolves x-opencode-project with the following priority:
// 1. Context/config extra headers (x-opencode-project and common client variants)
// 2. Generated sha1 fallback per opencode-generate-headers.md
func ResolveProjectID(ctx *schemas.BifrostContext, configExtraHeaders map[string]string) string {
	if v := lookupHeader(ctx, configExtraHeaders,
		"x-opencode-project", "x-opencode-project-id", "x-project-id", "x-project",
	); v != "" {
		return v
	}
	return GenerateProjectID("", "")
}

// ResolvedHeaders holds the session/project/trace values shared between the
// HTTP headers and the request body defaults (prompt_cache_key).
type ResolvedHeaders struct {
	SessionID   string
	ProjectID   string
	Traceparent string
	B3          string
}

// ResolveOpencodeHeaders resolves (or generates) the session, project, and
// trace values once per request so headers and body defaults stay consistent.
func ResolveOpencodeHeaders(ctx *schemas.BifrostContext, configExtraHeaders map[string]string) ResolvedHeaders {
	sessionID := ResolveSessionID(ctx, configExtraHeaders)
	projectID := ResolveProjectID(ctx, configExtraHeaders)

	traceparent := lookupHeader(ctx, configExtraHeaders, "traceparent")
	b3 := lookupHeader(ctx, configExtraHeaders, "b3")
	if traceparent == "" || b3 == "" {
		genTraceparent, genB3 := GenerateTraceHeaders()
		if traceparent == "" {
			traceparent = genTraceparent
		}
		if b3 == "" {
			b3 = genB3
		}
	}

	return ResolvedHeaders{
		SessionID:   sessionID,
		ProjectID:   projectID,
		Traceparent: traceparent,
		B3:          b3,
	}
}

// headersFromResolved builds headers from an already-resolved session/project/
// trace triple, so callers that also need the session ID (prompt_cache_key)
// don't resolve twice and generate divergent random sessions.
func headersFromResolved(ctx *schemas.BifrostContext, configExtraHeaders map[string]string, resolved ResolvedHeaders, isStreaming bool) map[string]string {
	_ = isStreaming
	headers := map[string]string{
		"authorization":      DefaultAuth,
		"x-opencode-client":  DefaultClient,
		"user-agent":         DefaultUserAgent,
		"b3":                 resolved.B3,
		"traceparent":        resolved.Traceparent,
		"x-opencode-project": resolved.ProjectID,
		"x-opencode-session": resolved.SessionID,
		"x-session-affinity": resolved.SessionID,
		"x-session-id":       resolved.SessionID,
	}
	applyHeaderOverrides(headers, configExtraHeaders)
	applyContextHeaderOverrides(ctx, headers)
	return headers
}

func applyHeaderOverrides(headers map[string]string, configExtraHeaders map[string]string) {
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
}

func applyContextHeaderOverrides(ctx *schemas.BifrostContext, headers map[string]string) {
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
}

// BuildHeaders constructs the complete set of headers for opencode-free,
// matching the working capture: authorization, x-opencode-client, user-agent,
// b3, traceparent, x-opencode-project, x-opencode-session, x-session-affinity,
// x-session-id. Anything not in that set is user-provided, never hardcoded.
//
// User-provided extra headers (from config.json, Web UI, or context) take
// priority and override defaults.
//
// NOTE: accept, accept-encoding, connection, content-type and host are
// intentionally NOT sent. accept-encoding: gzip made the upstream return a
// gzip body that the unary list-models path could not decode (it reads
// resp.Body() directly), causing "failed to unmarshal response". Verified
// live: without accept-encoding the server returns plain JSON 200.
// content-type is still set by the openai helpers themselves, and
// host/connection are managed by fasthttp.
func BuildHeaders(ctx *schemas.BifrostContext, configExtraHeaders map[string]string, isStreaming bool) map[string]string {
	return headersFromResolved(ctx, configExtraHeaders, ResolveOpencodeHeaders(ctx, configExtraHeaders), isStreaming)
}
