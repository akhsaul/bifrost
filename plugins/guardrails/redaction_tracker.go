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

	// Streaming carry buffers to handle placeholders split across chunks.
	// Bounded strictly to ~30 bytes max per active stream/tool.
	streamToolArgsCarry  map[uint16]string // keyed by ToolCall Index
	streamContentCarry   map[int]string    // keyed by Choice Index
	streamReasoningCarry map[int]string    // keyed by Choice Index
}

// NewRequestRedactionTracker initializes an empty tracker.
func NewRequestRedactionTracker() *RequestRedactionTracker {
	return &RequestRedactionTracker{
		entityCounters:       make(map[string]int),
		secretToToken:        make(map[string]string),
		tokenToSecret:        make(map[string]string),
		tokenToMasked:        make(map[string]string),
		tokenToRevealKey:     make(map[string]string),
		streamToolArgsCarry:  make(map[uint16]string),
		streamContentCarry:   make(map[int]string),
		streamReasoningCarry: make(map[int]string),
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

// streamReplace processes an incoming text fragment for a stream using replacements map.
// If isLast is true, all text is flushed and carry is emptied.
// Otherwise, if the text ends with a suffix that is a proper prefix of any placeholder in replacements,
// that suffix is held back in carry for the next chunk.
func (t *RequestRedactionTracker) streamReplace(incoming string, carry *string, replacements map[string]string, isLast bool) string {
	combined := *carry + incoming
	if combined == "" {
		*carry = ""
		return ""
	}

	// 1. Replace all full placeholders present in combined
	for token, repl := range replacements {
		if token != "" && repl != "" && strings.Contains(combined, token) {
			combined = strings.ReplaceAll(combined, token, repl)
		}
	}

	// If this is the last chunk, don't hold back anything
	if isLast {
		*carry = ""
		return combined
	}

	// 2. Find the longest suffix of combined that is a proper prefix of any placeholder
	maxHold := 0
	for token := range replacements {
		if len(token) > maxHold {
			maxHold = len(token)
		}
	}
	if maxHold > 0 {
		maxHold-- // only proper prefixes (length < len(token))
	}

	maxCheck := len(combined)
	if maxCheck > maxHold {
		maxCheck = maxHold
	}

	holdBackLen := 0
	for l := maxCheck; l >= 1; l-- {
		suffix := combined[len(combined)-l:]
		for token := range replacements {
			if strings.HasPrefix(token, suffix) {
				holdBackLen = l
				break
			}
		}
		if holdBackLen > 0 {
			break
		}
	}

	if holdBackLen > 0 {
		emit := combined[:len(combined)-holdBackLen]
		*carry = combined[len(combined)-holdBackLen:]
		return emit
	}

	*carry = ""
	return combined
}

// StreamReplaceToolArgs processes streaming tool call arguments for a given tool call index.
// Placeholders are replaced with the original full secrets.
func (t *RequestRedactionTracker) StreamReplaceToolArgs(toolIndex uint16, args string, isLast bool) string {
	t.Lock()
	defer t.Unlock()
	if t.streamToolArgsCarry == nil {
		t.streamToolArgsCarry = make(map[uint16]string)
	}
	carry := t.streamToolArgsCarry[toolIndex]
	out := t.streamReplace(args, &carry, t.tokenToSecret, isLast)
	if carry == "" {
		delete(t.streamToolArgsCarry, toolIndex)
	} else {
		t.streamToolArgsCarry[toolIndex] = carry
	}
	return out
}

// FlushToolArgsCarry flushes any remaining carry for tool calls.
func (t *RequestRedactionTracker) FlushToolArgsCarry() map[uint16]string {
	t.Lock()
	defer t.Unlock()
	if len(t.streamToolArgsCarry) == 0 {
		return nil
	}
	res := make(map[uint16]string, len(t.streamToolArgsCarry))
	for k, v := range t.streamToolArgsCarry {
		if v != "" {
			res[k] = v
		}
	}
	t.streamToolArgsCarry = make(map[uint16]string)
	return res
}

// StreamReplaceContent processes streaming content for a given choice index.
// Placeholders are replaced with partially masked values.
func (t *RequestRedactionTracker) StreamReplaceContent(choiceIndex int, content string, isLast bool) string {
	t.Lock()
	defer t.Unlock()
	if t.streamContentCarry == nil {
		t.streamContentCarry = make(map[int]string)
	}
	carry := t.streamContentCarry[choiceIndex]
	out := t.streamReplace(content, &carry, t.tokenToMasked, isLast)
	if carry == "" {
		delete(t.streamContentCarry, choiceIndex)
	} else {
		t.streamContentCarry[choiceIndex] = carry
	}
	return out
}

// FlushContentCarry flushes any remaining carry for choiceIndex.
func (t *RequestRedactionTracker) FlushContentCarry(choiceIndex int) string {
	t.Lock()
	defer t.Unlock()
	if t.streamContentCarry == nil {
		return ""
	}
	res := t.streamContentCarry[choiceIndex]
	delete(t.streamContentCarry, choiceIndex)
	return res
}

// StreamReplaceReasoning processes streaming reasoning for a given choice index.
// Placeholders are replaced with partially masked values.
func (t *RequestRedactionTracker) StreamReplaceReasoning(choiceIndex int, reasoning string, isLast bool) string {
	t.Lock()
	defer t.Unlock()
	if t.streamReasoningCarry == nil {
		t.streamReasoningCarry = make(map[int]string)
	}
	carry := t.streamReasoningCarry[choiceIndex]
	out := t.streamReplace(reasoning, &carry, t.tokenToMasked, isLast)
	if carry == "" {
		delete(t.streamReasoningCarry, choiceIndex)
	} else {
		t.streamReasoningCarry[choiceIndex] = carry
	}
	return out
}

// FlushReasoningCarry flushes any remaining reasoning carry for choiceIndex.
func (t *RequestRedactionTracker) FlushReasoningCarry(choiceIndex int) string {
	t.Lock()
	defer t.Unlock()
	if t.streamReasoningCarry == nil {
		return ""
	}
	res := t.streamReasoningCarry[choiceIndex]
	delete(t.streamReasoningCarry, choiceIndex)
	return res
}

