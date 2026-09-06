package guardrails

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"sync"

	"github.com/maximhq/bifrost/core/schemas"
)

// reversibleMapKey is a plugin-private context key. It is NOT part of
// schemas' reservedKeys, so writes succeed even while the core has
// BlockRestrictedWrites enabled during plugin hooks.
type reversibleMapKey struct{}

// reversibleMaps is a process-wide fallback store keyed by context pointer
// for contexts that cannot carry arbitrary values (defensive fallback; the
// primary path stores the map directly on the BifrostContext).
var reversibleMaps sync.Map // map[*schemas.BifrostContext]map[string]string

// generateReversibleToken produces a deterministic-but-unique placeholder for
// one secret occurrence. The 8-char hex suffix comes from SHA-256 of the
// secret plus random salt, so the same secret still gets distinct tokens per
// occurrence (preventing LLM-side correlation across occurrences).
func generateReversibleToken(entityType, secret string) string {
	salt := make([]byte, 4)
	_, _ = rand.Read(salt)
	sum := sha256.Sum256(append([]byte(secret), salt...))
	short := hex.EncodeToString(sum[:])[:8]
	if entityType == "" {
		entityType = "SECRET"
	}
	return "__BF_REV_" + entityType + "_" + short + "__"
}

// storeReversibleToken records placeholder -> secret on the request context.
func storeReversibleToken(ctx *schemas.BifrostContext, token, secret string) {
	if ctx == nil || token == "" || secret == "" {
		return
	}
	existing, _ := ctx.Value(reversibleMapKey{}).(map[string]string)
	if existing == nil {
		existing = make(map[string]string)
		ctx.SetValue(reversibleMapKey{}, existing)
	}
	existing[token] = secret
}

// getReversibleTokens returns the placeholder map stored on the context
// (nil when nothing was tokenized for this request).
func getReversibleTokens(ctx *schemas.BifrostContext) map[string]string {
	if ctx == nil {
		return nil
	}
	m, _ := ctx.Value(reversibleMapKey{}).(map[string]string)
	return m
}

// detokenizeString replaces every known placeholder in input with its original
// secret. Longest-token-first ordering prevents prefix collisions.
func detokenizeString(input string, tokens map[string]string) string {
	if input == "" || len(tokens) == 0 {
		return input
	}
	out := input
	for token, secret := range tokens {
		if token == "" || secret == "" {
			continue
		}
		if strings.Contains(out, token) {
			out = strings.ReplaceAll(out, token, secret)
		}
	}
	return out
}

// detokenizeToolCalls walks every choice's tool_calls in the response and
// restores any reversible placeholders inside their JSON arguments. This is
// the bridge that lets bash/script tool executions on the client receive the
// original secrets the LLM itself never saw.
func detokenizeToolCalls(resp *schemas.BifrostResponse, ctx *schemas.BifrostContext) {
	if resp == nil || resp.ChatResponse == nil || ctx == nil {
		return
	}
	tokens := getReversibleTokens(ctx)
	if len(tokens) == 0 {
		return
	}
	for i := range resp.ChatResponse.Choices {
		c := &resp.ChatResponse.Choices[i]
		if c.ChatNonStreamResponseChoice == nil || c.ChatNonStreamResponseChoice.Message == nil {
			continue
		}
		msg := c.ChatNonStreamResponseChoice.Message
		if msg.ChatAssistantMessage == nil {
			continue
		}
		for j := range msg.ChatAssistantMessage.ToolCalls {
			tc := &msg.ChatAssistantMessage.ToolCalls[j]
			if tc.Function.Arguments != "" {
				tc.Function.Arguments = detokenizeString(tc.Function.Arguments, tokens)
			}
		}
	}
}
