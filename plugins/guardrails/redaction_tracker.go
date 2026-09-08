package guardrails

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"

	"github.com/maximhq/bifrost/core/schemas"
)

// redactionTrackerKey is a plugin-private context key.
type redactionTrackerKey struct{}

// RequestRedactionTracker tracks redactions across input and output phases of a single request.
type RequestRedactionTracker struct {
	sync.Mutex
	entityCounters   map[string]int
	secretToToken    map[string]string
	tokenToSecret    map[string]string
	tokenToMasked    map[string]string
	tokenToRevealKey map[string]string
	redactionData    schemas.RedactionData
}

// NewRequestRedactionTracker initializes an empty tracker.
func NewRequestRedactionTracker() *RequestRedactionTracker {
	return &RequestRedactionTracker{
		entityCounters:   make(map[string]int),
		secretToToken:    make(map[string]string),
		tokenToSecret:    make(map[string]string),
		tokenToMasked:    make(map[string]string),
		tokenToRevealKey: make(map[string]string),
		redactionData: schemas.RedactionData{
			LiteralReplacements: schemas.RedactionMapsByPhase{
				Input:  make(map[string]string),
				Output: make(map[string]string),
			},
			ReversibleMappings: schemas.RedactionMapsByPhase{
				Input:  make(map[string]string),
				Output: make(map[string]string),
			},
		},
	}
}

// GetOrCreateTracker retrieves the tracker from ctx, creating one if needed.
func GetOrCreateTracker(ctx *schemas.BifrostContext) *RequestRedactionTracker {
	if ctx == nil {
		return NewRequestRedactionTracker()
	}
	if v, ok := ctx.Value(redactionTrackerKey{}).(*RequestRedactionTracker); ok && v != nil {
		return v
	}
	tracker := NewRequestRedactionTracker()
	ctx.SetValue(redactionTrackerKey{}, tracker)
	return tracker
}

// Redact generates or retrieves a replacement placeholder for secret according to strategy,
// updates internal mappings, and registers reveal mapping on ctx if mode is runtime_reversible.
func (t *RequestRedactionTracker) Redact(ctx *schemas.BifrostContext, entityType, secret string, strategy RedactionStrategy, mode RedactionMode, phase schemas.RedactionPhase) string {
	if secret == "" {
		return ""
	}
	if entityType == "" {
		entityType = "SECRET"
	}

	t.Lock()
	defer t.Unlock()

	// If secret was already assigned a placeholder in this request, reuse it.
	if token, ok := t.secretToToken[secret]; ok {
		if mode == RedactionModeRuntimeReversible {
			revealKey := t.tokenToRevealKey[token]
			t.redactionData.ReversibleMappings.MergePhase(phase, map[string]string{revealKey: secret})
			t.redactionData.LiteralReplacements.MergePhase(phase, map[string]string{secret: token})
			schemas.SetRedactionDataOnContext(ctx, t.redactionData)
		}
		return token
	}

	var placeholder, revealKey string
	switch strategy {
	case RedactionHash:
		sum := sha256.Sum256([]byte(secret))
		shortHash := hex.EncodeToString(sum[:])[:16]
		placeholder = fmt.Sprintf("[%s:%s]", entityType, shortHash)
		revealKey = fmt.Sprintf("%s:%s", entityType, shortHash)
	case RedactionMask:
		return strings.Repeat("*", len([]rune(secret)))
	default: // RedactionReplace
		t.entityCounters[entityType]++
		count := t.entityCounters[entityType]
		placeholder = fmt.Sprintf("[%s-%d]", entityType, count)
		revealKey = fmt.Sprintf("%s-%d", entityType, count)
	}

	t.secretToToken[secret] = placeholder
	t.tokenToSecret[placeholder] = secret
	t.tokenToMasked[placeholder] = maskPartially(secret)
	t.tokenToRevealKey[placeholder] = revealKey

	if mode == RedactionModeRuntimeReversible {
		t.redactionData.ReversibleMappings.MergePhase(phase, map[string]string{revealKey: secret})
		t.redactionData.LiteralReplacements.MergePhase(phase, map[string]string{secret: placeholder})
		schemas.SetRedactionDataOnContext(ctx, t.redactionData)
	}

	return placeholder
}

// TokenToSecret returns the original secret for placeholder (e.g. for tool_calls arguments).
func (t *RequestRedactionTracker) TokenToSecret(token string) string {
	t.Lock()
	defer t.Unlock()
	return t.tokenToSecret[token]
}

// TokenToMasked returns the partially masked value for placeholder (e.g. for client egress text).
func (t *RequestRedactionTracker) TokenToMasked(token string) string {
	t.Lock()
	defer t.Unlock()
	return t.tokenToMasked[token]
}

// GetAllTokensToSecret returns a copy of placeholder -> secret map.
func (t *RequestRedactionTracker) GetAllTokensToSecret() map[string]string {
	t.Lock()
	defer t.Unlock()
	res := make(map[string]string, len(t.tokenToSecret))
	for k, v := range t.tokenToSecret {
		res[k] = v
	}
	return res
}

// GetAllTokensToMasked returns a copy of placeholder -> partially masked value map.
func (t *RequestRedactionTracker) GetAllTokensToMasked() map[string]string {
	t.Lock()
	defer t.Unlock()
	res := make(map[string]string, len(t.tokenToMasked))
	for k, v := range t.tokenToMasked {
		res[k] = v
	}
	return res
}

// HasTokens reports whether any placeholder was generated for this request.
func (t *RequestRedactionTracker) HasTokens() bool {
	t.Lock()
	defer t.Unlock()
	return len(t.tokenToSecret) > 0
}

// maskPartially produces a partially masked version of secret:
// 4 characters front and back are shown with "***" in the middle
// (e.g. "github_pat_key" -> "gith***_key", "github_pat_key123" -> "gith***y123").
// For shorter strings:
// - <= 4 runes: replaced entirely with '*'
// - 5 to 8 runes: 1 character front and back with "***" in the middle
func maskPartially(secret string) string {
	if secret == "" {
		return ""
	}
	runes := []rune(secret)
	n := len(runes)
	if n <= 4 {
		return strings.Repeat("*", n)
	}
	if n <= 8 {
		return string(runes[:1]) + "***" + string(runes[n-1:])
	}
	return string(runes[:4]) + "***" + string(runes[n-4:])
}
