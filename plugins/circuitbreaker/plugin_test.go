package circuitbreaker

import (
	"context"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCircuitBreaker_BasicTripAndFilter(t *testing.T) {
	config := Config{
		Enabled:         true,
		DefaultCooldown: schemas.Duration(5 * time.Hour),
	}
	plugin, err := New(config, nil)
	require.NoError(t, err)

	key1 := schemas.Key{ID: "key-1", Name: "Key 1"}
	key2 := schemas.Key{ID: "key-2", Name: "Key 2"}
	keys := []schemas.Key{key1, key2}

	filter := plugin.KeyPoolFilter()

	// Initial check: both keys eligible
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	filtered, err := filter(ctx, schemas.OpenAI, "gpt-4o", keys)
	require.NoError(t, err)
	assert.Len(t, filtered, 2)

	// Simulate Rate Limit Error on key-1
	ctx.SetValue(schemas.BifrostContextKeySelectedKeyID, "key-1")
	status429 := 429
	bifrostErr := &schemas.BifrostError{
		StatusCode: &status429,
		Error: &schemas.ErrorField{
			Message: "Rate limit reached for gpt-4o: quota exceeded",
		},
	}
	bifrostErr.PopulateExtraFields(schemas.ChatCompletionRequest, schemas.OpenAI, "gpt-4o", "gpt-4o")

	_, outErr, err := plugin.PostLLMHook(ctx, nil, bifrostErr)
	require.NoError(t, err)
	assert.Equal(t, bifrostErr, outErr)

	// Check filter again for gpt-4o: key-1 should be excluded, key-2 remains
	filtered, err = filter(ctx, schemas.OpenAI, "gpt-4o", keys)
	require.NoError(t, err)
	require.Len(t, filtered, 1)
	assert.Equal(t, "key-2", filtered[0].ID)

	// Check filter for a different model e.g. "o1-preview": key-1 should STILL be eligible! (Granularity per Key+Model)
	filteredDiffModel, err := filter(ctx, schemas.OpenAI, "o1-preview", keys)
	require.NoError(t, err)
	assert.Len(t, filteredDiffModel, 2)
}

func TestCircuitBreaker_DynamicCooldownParsing(t *testing.T) {
	config := Config{
		Enabled:         true,
		DefaultCooldown: schemas.Duration(5 * time.Hour),
	}
	plugin, err := New(config, nil)
	require.NoError(t, err)

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeySelectedKeyID, "key-sub")

	status429 := 429
	bifrostErr := &schemas.BifrostError{
		StatusCode: &status429,
		Error: &schemas.ErrorField{
			Message: "Usage limit reached. Try again in 200ms",
		},
	}
	bifrostErr.PopulateExtraFields(schemas.ChatCompletionRequest, schemas.Anthropic, "claude-3-5-sonnet-20241022", "claude-3-5-sonnet-20241022")

	_, _, err = plugin.PostLLMHook(ctx, nil, bifrostErr)
	require.NoError(t, err)

	keys := []schemas.Key{{ID: "key-sub"}}
	filter := plugin.KeyPoolFilter()

	// Immediately: filtered
	filtered, err := filter(ctx, schemas.Anthropic, "claude-3-5-sonnet-20241022", keys)
	require.NoError(t, err)
	assert.Empty(t, filtered)

	// Wait for cooldown to expire
	time.Sleep(250 * time.Millisecond)

	filtered, err = filter(ctx, schemas.Anthropic, "claude-3-5-sonnet-20241022", keys)
	require.NoError(t, err)
	assert.Len(t, filtered, 1)
}

func TestCircuitBreaker_StaticKeyModelConfig(t *testing.T) {
	config := Config{
		Enabled:         true,
		DefaultCooldown: schemas.Duration(5 * time.Hour),
		KeyConfigs: []KeyModelConfig{
			{
				KeyID:    "key-weekly",
				Cooldown: schemas.Duration(7 * 24 * time.Hour),
			},
			{
				KeyID:    "key-fast-reset",
				Cooldown: schemas.Duration(100 * time.Millisecond),
			},
		},
	}
	plugin, err := New(config, nil)
	require.NoError(t, err)

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeySelectedKeyID, "key-fast-reset")

	status429 := 429
	bifrostErr := &schemas.BifrostError{
		StatusCode: &status429,
		Error: &schemas.ErrorField{
			Message: "Rate limit exceeded",
		},
	}
	bifrostErr.PopulateExtraFields(schemas.ChatCompletionRequest, schemas.OpenAI, "gpt-4o", "gpt-4o")

	_, _, err = plugin.PostLLMHook(ctx, nil, bifrostErr)
	require.NoError(t, err)

	keys := []schemas.Key{{ID: "key-fast-reset"}}
	filter := plugin.KeyPoolFilter()

	filtered, err := filter(ctx, schemas.OpenAI, "gpt-4o", keys)
	require.NoError(t, err)
	assert.Empty(t, filtered)

	// After 120ms it should reset
	time.Sleep(120 * time.Millisecond)
	filtered, err = filter(ctx, schemas.OpenAI, "gpt-4o", keys)
	require.NoError(t, err)
	assert.Len(t, filtered, 1)
}

func TestCircuitBreaker_ISOAndRawSecondsParsing(t *testing.T) {
	now := time.Now()

	// 1. Test ISO timestamp format
	future := now.Add(2 * time.Hour).UTC().Format(time.RFC3339)
	errISO := &schemas.BifrostError{
		Error: &schemas.ErrorField{
			Message: "Usage quota exceeded. Resets at " + future,
		},
	}
	dISO, okISO := ParseCooldownDuration(errISO, now)
	assert.True(t, okISO)
	assert.InDelta(t, (2 * time.Hour).Seconds(), dISO.Seconds(), 2)

	// 2. Test raw seconds
	errSec := &schemas.BifrostError{
		Error: &schemas.ErrorField{
			Message: "Too many requests. Please retry after 3600 seconds",
		},
	}
	dSec, okSec := ParseCooldownDuration(errSec, now)
	assert.True(t, okSec)
	assert.Equal(t, 3600*time.Second, dSec)
}

func TestCircuitBreaker_CustomPatternAndManualReset(t *testing.T) {
	config := Config{
		Enabled:         true,
		DefaultCooldown: schemas.Duration(5 * time.Hour),
		ErrorPatterns:   []string{`custom_subscription_exhausted`},
	}
	plugin, err := New(config, nil)
	require.NoError(t, err)

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeySelectedKeyID, "key-custom")

	// Simulate custom error message with 200 OK or 400
	status400 := 400
	bifrostErr := &schemas.BifrostError{
		StatusCode: &status400,
		Error: &schemas.ErrorField{
			Message: "Request failed with error: custom_subscription_exhausted",
		},
	}
	bifrostErr.PopulateExtraFields(schemas.ChatCompletionRequest, schemas.OpenAI, "gpt-4o", "gpt-4o")

	_, _, err = plugin.PostLLMHook(ctx, nil, bifrostErr)
	require.NoError(t, err)

	keys := []schemas.Key{{ID: "key-custom"}}
	filter := plugin.KeyPoolFilter()

	filtered, err := filter(ctx, schemas.OpenAI, "gpt-4o", keys)
	require.NoError(t, err)
	assert.Empty(t, filtered)

	// Manual Reset
	plugin.GetStore().Reset("key-custom", schemas.OpenAI, "gpt-4o")
	filtered, err = filter(ctx, schemas.OpenAI, "gpt-4o", keys)
	require.NoError(t, err)
	assert.Len(t, filtered, 1)
}

type mockQuotaProvider struct {
	schemas.Provider
	keySummary  *schemas.KeyQuotaSummary
	modelsQuota map[string]schemas.ModelQuotaInfo
}

func (m *mockQuotaProvider) GetProviderKey() schemas.ModelProvider {
	return schemas.Antigravity
}

func (m *mockQuotaProvider) GetKeyQuotaSummary(_ *schemas.BifrostContext, key schemas.Key) (*schemas.KeyQuotaSummary, *schemas.BifrostError) {
	return m.keySummary, nil
}

func (m *mockQuotaProvider) GetModelsQuota(_ *schemas.BifrostContext, key schemas.Key) (map[string]schemas.ModelQuotaInfo, *schemas.BifrostError) {
	return m.modelsQuota, nil
}

func TestCircuitBreaker_SyncQuota(t *testing.T) {
	config := Config{
		Enabled:         true,
		DefaultCooldown: schemas.Duration(5 * time.Hour),
	}
	plugin, err := New(config, nil)
	require.NoError(t, err)

	key := schemas.Key{ID: "key-antigravity"}
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)

	mockProv := &mockQuotaProvider{
		keySummary: &schemas.KeyQuotaSummary{
			KeyID:    "key-antigravity",
			Provider: schemas.Antigravity,
			Groups: []schemas.QuotaGroup{
				{
					DisplayName: "Gemini Models",
					Buckets: []schemas.QuotaBucket{
						{
							BucketID:          "gemini-5h",
							DisplayName:       "Five Hour Limit Remaining",
							RemainingFraction: 0.0,
							ResetTime:         time.Now().Add(3 * time.Hour),
						},
					},
				},
			},
		},
		modelsQuota: map[string]schemas.ModelQuotaInfo{
			"gemini-3.5-flash-low": {
				Model:             "gemini-3.5-flash-low",
				RemainingFraction: 0.0, // Limited!
				ResetTime:         time.Now().Add(3 * time.Hour),
				IsLimited:         true,
			},
			"claude-opus-4-6-thinking": {
				Model:             "claude-opus-4-6-thinking",
				RemainingFraction: 1.0, // Available
				IsLimited:         false,
			},
		},
	}

	// 1. Sync per-key summary
	summary, err := plugin.SyncKeyQuota(ctx, mockProv, key)
	require.NoError(t, err)
	assert.Equal(t, "key-antigravity", summary.KeyID)
	assert.Len(t, summary.Groups, 1)

	// 2. Sync per-model quota (should trip gemini-3.5-flash-low and keep claude-opus-4-6-thinking eligible)
	modelsQuota, err := plugin.SyncModelQuota(ctx, mockProv, key)
	require.NoError(t, err)
	assert.Len(t, modelsQuota, 2)

	filter := plugin.KeyPoolFilter()
	keys := []schemas.Key{key}

	// gemini-3.5-flash-low should be excluded
	filteredGemini, err := filter(ctx, schemas.Antigravity, "gemini-3.5-flash-low", keys)
	require.NoError(t, err)
	assert.Empty(t, filteredGemini)

	// claude-opus-4-6-thinking should be available
	filteredClaude, err := filter(ctx, schemas.Antigravity, "claude-opus-4-6-thinking", keys)
	require.NoError(t, err)
	assert.Len(t, filteredClaude, 1)
}
