package guardrails

import (
	"context"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMaskPartially(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"github_pat_key", "gith***_key"},
		{"github_pat_123", "gith***_123"},
		{"github_pat_key123", "gith***y123"},
		{"abcdefghij", "abcd***ghij"},
		{"12345678", "1***8"},
		{"secret", "s***t"},
		{"hello", "h***o"},
		{"four", "****"},
		{"abc", "***"},
		{"a", "*"},
		{"", ""},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			assert.Equal(t, tt.expected, maskPartially(tt.input))
		})
	}
}

func TestRedactionTracker_ReplaceStrategy(t *testing.T) {
	ctx := schemas.NewBifrostContext(context.Background(), time.Time{})
	tracker := GetOrCreateTracker(ctx)

	// First email
	p1 := tracker.Redact(ctx, "EMAIL", "alex@example.com", RedactionReplace, RedactionModeRuntimeReversible, schemas.RedactionPhaseInput)
	assert.Equal(t, "[EMAIL-1]", p1)

	// Second different email -> [EMAIL-2]
	p2 := tracker.Redact(ctx, "EMAIL", "bob@example.com", RedactionReplace, RedactionModeRuntimeReversible, schemas.RedactionPhaseInput)
	assert.Equal(t, "[EMAIL-2]", p2)

	// Same email repeated in same request -> still [EMAIL-1]
	p1Again := tracker.Redact(ctx, "EMAIL", "alex@example.com", RedactionReplace, RedactionModeRuntimeReversible, schemas.RedactionPhaseInput)
	assert.Equal(t, "[EMAIL-1]", p1Again)

	// Different entity type -> [SECRET-1]
	s1 := tracker.Redact(ctx, "SECRET", "github_pat_key", RedactionReplace, RedactionModeRuntimeReversible, schemas.RedactionPhaseInput)
	assert.Equal(t, "[SECRET-1]", s1)

	// Check context has RedactionData
	rData, ok := schemas.RedactionDataFromContext(ctx)
	require.True(t, ok)
	assert.Equal(t, "alex@example.com", rData.ReversibleMappings.Input["EMAIL-1"])
	assert.Equal(t, "bob@example.com", rData.ReversibleMappings.Input["EMAIL-2"])
	assert.Equal(t, "github_pat_key", rData.ReversibleMappings.Input["SECRET-1"])

	// Check token mappings
	assert.Equal(t, "github_pat_key", tracker.TokenToSecret("[SECRET-1]"))
	assert.Equal(t, "gith***_key", tracker.TokenToMasked("[SECRET-1]"))
}

func TestRedactionTracker_HashStrategy(t *testing.T) {
	ctx := schemas.NewBifrostContext(context.Background(), time.Time{})
	tracker := GetOrCreateTracker(ctx)

	h1 := tracker.Redact(ctx, "EMAIL", "alex@example.com", RedactionHash, RedactionModeRuntimeReversible, schemas.RedactionPhaseInput)
	// Must be [EMAIL:<16-hex>]
	assert.True(t, len(h1) == len("[EMAIL:]")+16)
	assert.Contains(t, h1, "[EMAIL:")

	// Same email gives same hash
	h1Again := tracker.Redact(ctx, "EMAIL", "alex@example.com", RedactionHash, RedactionModeRuntimeReversible, schemas.RedactionPhaseInput)
	assert.Equal(t, h1, h1Again)

	// Check context has RedactionData
	rData, ok := schemas.RedactionDataFromContext(ctx)
	require.True(t, ok)
	key := h1[1 : len(h1)-1] // without brackets: EMAIL:<hex>
	assert.Equal(t, "alex@example.com", rData.ReversibleMappings.Input[key])
}

func TestRedactionTracker_PermanentRuntimeModeDoesNotStoreReveal(t *testing.T) {
	ctx := schemas.NewBifrostContext(context.Background(), time.Time{})
	tracker := GetOrCreateTracker(ctx)

	p := tracker.Redact(ctx, "SECRET", "github_pat_key", RedactionReplace, RedactionModeRuntime, schemas.RedactionPhaseInput)
	assert.Equal(t, "[SECRET-1]", p)

	// Context should NOT have reversible data
	_, ok := schemas.RedactionDataFromContext(ctx)
	assert.False(t, ok)

	// But in-memory egress restoration should still work!
	assert.Equal(t, "github_pat_key", tracker.TokenToSecret("[SECRET-1]"))
	assert.Equal(t, "gith***_key", tracker.TokenToMasked("[SECRET-1]"))
}
