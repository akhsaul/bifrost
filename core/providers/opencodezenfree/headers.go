package opencodezenfree

import (
	"crypto/rand"
	"crypto/sha1"
	"encoding/hex"
	"math/big"
	"regexp"
	"strings"
	"sync/atomic"
	"time"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
)

const (
	DefaultBaseURL   = "https://opencode.ai/inference"
	DefaultAuth      = "Bearer public"
	DefaultClient    = "cli"
	DefaultUserAgent = "opencode/latest/2.2.5/cli"

	// OpencodeEnvTag is required by OpenCode edge for free tier requests.
	OpencodeEnvTag = "<env>Working directory: ?</env>"
	// DefaultInstructions mirrors the working opencode capture with the mandatory environment tag.
	DefaultInstructions = "You are helpfull assistant\n" + OpencodeEnvTag
	// DefaultReasoningEffort / DefaultReasoningSummary are fallbacks only: client-supplied values always win.
	DefaultReasoningEffort  = "auto"
	DefaultReasoningSummary = "auto"
	// DefaultPromptCacheKeyFallback is used only when no session ID is available.
	DefaultPromptCacheKeyFallback = "global"
)

const (
	base62Chars      = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"
	crockford32Chars = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"
)

var (
	sessionCounter uint64
	isSHA1Hex      = regexp.MustCompile(`^[0-9a-fA-F]{40}$`)
	workingDirRe   = regexp.MustCompile(`(?i)(?:^|\n|<env>|\s)working\s+directory:\s*([^\r\n<]+)`)
)

// GenerateOpencodeOrgID produces an OpenCode organization identifier matching
// ULID format: org_01<24 Crockford Base32 characters>.
func GenerateOpencodeOrgID() string {
	return "org_01" + randomCrockford32(24)
}

func randomCrockford32(length int) string {
	raw := make([]byte, length)
	if _, err := rand.Read(raw); err != nil {
		for i := range raw {
			raw[i] = byte((time.Now().UnixNano() >> (8 * (i % 8))) & 0xff)
		}
	}
	out := make([]byte, length)
	for i := 0; i < length; i++ {
		out[i] = crockford32Chars[raw[i]%32]
	}
	return string(out)
}

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

// SHA1String returns the lowercase 40-hex SHA-1 digest of the given string.
func SHA1String(s string) string {
	sum := sha1.Sum([]byte(s))
	return hex.EncodeToString(sum[:])
}

// ExtractWorkingDirectory searches for "working directory: <path>\n" or
// "<env>Working directory: <path></env>" in prompt text, returning the path up
// to newline or tag closing. Returns empty if not found or if the value is "?".
func ExtractWorkingDirectory(text string) string {
	matches := workingDirRe.FindStringSubmatch(text)
	if len(matches) > 1 {
		dir := strings.TrimSpace(matches[1])
		if dir != "" && dir != "?" {
			return dir
		}
	}
	return ""
}

// GenerateProjectID derives the x-opencode-project value from a git remote,
// matching packages/core/src/project.ts resolve() + parts():
// sha1("git-remote:<lower-host>/<path without leading/trailing slashes and .git suffix>").
// With no remote info it falls back to sha1("global").
func GenerateProjectID(host, path string) string {
	host = strings.TrimSpace(host)
	path = strings.TrimSpace(path)
	if host == "" || path == "" {
		return SHA1String("global")
	}
	p := strings.TrimPrefix(path, "/")
	p = strings.TrimSuffix(p, ".git")
	p = strings.TrimSuffix(p, "/")
	p = strings.TrimPrefix(p, "/")
	norm := strings.ToLower(host) + "/" + p
	return SHA1String("git-remote:" + norm)
}

func lookupHeader(ctx *schemas.BifrostContext, configExtraHeaders map[string]schemas.SecretVar, names ...string) string {
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
			val := v.GetValue()
			if strings.EqualFold(k, name) && val != "" {
				return val
			}
		}
	}
	return ""
}

// ResolveOrgID resolves x-opencode-org-id with the following priority:
// 1. Context extra headers (x-opencode-org-id, x-org-id, x-opencode-org)
// 2. Config extra headers (same names)
// 3. Dynamic generated org ID (org_01 + 24 Crockford Base32 characters)
func ResolveOrgID(ctx *schemas.BifrostContext, configExtraHeaders map[string]schemas.SecretVar) string {
	if v := lookupHeader(ctx, configExtraHeaders, "x-opencode-org-id", "x-org-id", "x-opencode-org"); v != "" {
		return v
	}
	return GenerateOpencodeOrgID()
}

// ResolveSessionID resolves the session ID with the following priority:
// 1. Context extra headers (x-opencode-session, x-opencode-session-id, x-session-affinity, x-session-id)
// 2. Config extra headers (same names)
// 3. Bifrost context session ID (session stickiness)
// 4. Dynamic session ID generation (ses_<12hex><14base62>)
func ResolveSessionID(ctx *schemas.BifrostContext, configExtraHeaders map[string]schemas.SecretVar) string {
	if v := lookupHeader(ctx, configExtraHeaders, "x-opencode-session", "x-opencode-session-id", "x-session-affinity", "x-session-id"); v != "" {
		return v
	}
	if sid := providerUtils.ResolveSessionID(ctx, nil); sid != nil {
		return *sid
	}
	return GenerateOpencodeSessionID()
}

// ResolveProjectID resolves x-opencode-project:
// 1. Context/config extra headers (x-opencode-project and common client variants):
//    - Used directly if provided by the client.
// 2. Working directory from system prompt:
//    - Parses "working directory: <dir>\n" or "<env>Working directory: <dir></env>".
//    - If found and not "?", hashed via SHA-1.
// 3. Fallback:
//    - SHA-1 of "global": "9dc20b006142d921ea0474629eab5930bc3cea00".
//
// NOTE: Bifrost operates at the gateway layer and never invokes external executables (such as git).
func ResolveProjectID(ctx *schemas.BifrostContext, configExtraHeaders map[string]schemas.SecretVar, systemPrompts ...string) string {
	if v := lookupHeader(ctx, configExtraHeaders,
		"x-opencode-project", "x-opencode-project-id", "x-project-id", "x-project",
	); v != "" {
		return strings.TrimSpace(v)
	}

	for _, prompt := range systemPrompts {
		if dir := ExtractWorkingDirectory(prompt); dir != "" {
			return SHA1String(dir)
		}
	}

	return SHA1String("global")
}

// ResolvedHeaders holds the session/project/org/trace values shared between the
// HTTP headers and the request body defaults (prompt_cache_key).
type ResolvedHeaders struct {
	SessionID   string
	ProjectID   string
	OrgID       string
	Traceparent string
	B3          string
}

// ResolveOpencodeHeaders resolves (or generates) the session, project, org, and
// trace values once per request so headers and body defaults stay consistent.
func ResolveOpencodeHeaders(ctx *schemas.BifrostContext, configExtraHeaders map[string]schemas.SecretVar, systemPrompts ...string) ResolvedHeaders {
	sessionID := ResolveSessionID(ctx, configExtraHeaders)
	projectID := ResolveProjectID(ctx, configExtraHeaders, systemPrompts...)
	orgID := ResolveOrgID(ctx, configExtraHeaders)

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
		OrgID:       orgID,
		Traceparent: traceparent,
		B3:          b3,
	}
}

// headersFromResolved builds headers from an already-resolved session/project/
// trace/org set, so callers that also need the session ID (prompt_cache_key)
// don't resolve twice and generate divergent random sessions.
func headersFromResolved(ctx *schemas.BifrostContext, configExtraHeaders map[string]schemas.SecretVar, resolved ResolvedHeaders, isStreaming bool) map[string]schemas.SecretVar {
	_ = isStreaming
	headers := map[string]schemas.SecretVar{
		"authorization":         *schemas.NewSecretVar(DefaultAuth),
		"x-opencode-client":     *schemas.NewSecretVar(DefaultClient),
		"user-agent":            *schemas.NewSecretVar(DefaultUserAgent),
		"b3":                    *schemas.NewSecretVar(resolved.B3),
		"traceparent":           *schemas.NewSecretVar(resolved.Traceparent),
		"x-opencode-org-id":     *schemas.NewSecretVar(resolved.OrgID),
		"x-opencode-project":    *schemas.NewSecretVar(resolved.ProjectID),
		"x-opencode-session":    *schemas.NewSecretVar(resolved.SessionID),
		"x-opencode-session-id": *schemas.NewSecretVar(resolved.SessionID),
		"x-session-affinity":    *schemas.NewSecretVar(resolved.SessionID),
		"x-session-id":          *schemas.NewSecretVar(resolved.SessionID),
	}
	applyHeaderOverrides(headers, configExtraHeaders)
	applyContextHeaderOverrides(ctx, headers)
	return headers
}

func applyHeaderOverrides(headers map[string]schemas.SecretVar, configExtraHeaders map[string]schemas.SecretVar) {
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

func applyContextHeaderOverrides(ctx *schemas.BifrostContext, headers map[string]schemas.SecretVar) {
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
					headers[k] = *schemas.NewSecretVar(vs[0])
				}
			}
		}
	}
}

// BuildHeaders constructs the complete set of headers for opencode-zen-free,
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
func BuildHeaders(ctx *schemas.BifrostContext, configExtraHeaders map[string]schemas.SecretVar, isStreaming bool) map[string]schemas.SecretVar {
	return headersFromResolved(ctx, configExtraHeaders, ResolveOpencodeHeaders(ctx, configExtraHeaders), isStreaming)
}
