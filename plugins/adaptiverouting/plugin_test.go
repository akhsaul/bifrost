package adaptiverouting

import (
	"context"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAdaptiveRouting_ScoreCalculation(t *testing.T) {
	config := DefaultConfig()

	// Fast target
	fastStats := TargetStats{
		EWMALatencyMs: 50.0,
		TotalRequests: 100,
		SuccessCount:  100,
	}
	fastEff, fastScore := CalculateCompositeScore(fastStats, config)
	assert.Equal(t, 50.0, fastEff)
	assert.Equal(t, 20.0, fastScore)

	// Slow target with 429 rate limit errors
	slowStats := TargetStats{
		EWMALatencyMs:     500.0,
		TotalRequests:     100,
		SuccessCount:      70,
		RateLimit429Count: 30, // 30% 429 errors -> 1.0 + (5.0 * 0.3) = 2.5 multiplier
	}
	slowEff, slowScore := CalculateCompositeScore(slowStats, config)
	assert.Equal(t, 1250.0, slowEff)
	assert.Equal(t, 0.8, slowScore)
}

func TestAdaptiveRouting_DynamicWeightComputation(t *testing.T) {
	config := DefaultConfig()
	config.ExplorationFloor = 0.05 // 5% minimum floor

	targetA := TargetID{Provider: schemas.OpenAI, Model: "gpt-4o"}
	targetB := TargetID{Provider: schemas.Azure, Model: "gpt-4o"}
	candidates := []TargetID{targetA, targetB}

	statsMap := map[TargetID]TargetStats{
		targetA: {
			EWMALatencyMs: 50.0,
			TotalRequests: 100,
			SuccessCount:  100,
		},
		targetB: {
			EWMALatencyMs: 500.0,
			TotalRequests: 100,
			SuccessCount:  100,
		},
	}

	weights := ComputeDynamicWeights(candidates, statsMap, config)
	require.Len(t, weights, 2)

	// Target A (fast) should receive substantially higher weight than Target B (slow)
	assert.Greater(t, weights[0].Weight, 0.80)
	assert.GreaterOrEqual(t, weights[1].Weight, config.ExplorationFloor) // B still gets exploration floor
	assert.InDelta(t, 1.0, weights[0].Weight+weights[1].Weight, 0.001)
	assert.Equal(t, 1.0, weights[1].CumWeight)
}

func TestAdaptiveRouting_PluginLifecycleAndKeyFilter(t *testing.T) {
	config := DefaultConfig()
	plugin, err := New(config, nil)
	require.NoError(t, err)
	defer func() {
		_ = plugin.Cleanup()
	}()

	key1 := schemas.Key{ID: "key-fast", Name: "Fast Key"}
	key2 := schemas.Key{ID: "key-slow", Name: "Slow Key"}
	keys := []schemas.Key{key1, key2}

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)

	// Record fast metrics for key1
	for i := 0; i < 5; i++ {
		ctxKey1 := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
		ctxKey1.SetValue(adaptiveStartTimeKey, time.Now().Add(-40*time.Millisecond))
		ctxKey1.SetValue(schemas.BifrostContextKeySelectedKeyID, "key-fast")

		resp := &schemas.BifrostResponse{
			ChatResponse: &schemas.BifrostChatResponse{
				ExtraFields: schemas.BifrostResponseExtraFields{
					Provider:          schemas.OpenAI,
					ResolvedModelUsed: "gpt-4o",
				},
			},
		}
		_, _, err = plugin.PostLLMHook(ctxKey1, resp, nil)
		require.NoError(t, err)
	}

	// Record slow metrics + 429 for key2
	for i := 0; i < 5; i++ {
		ctxKey2 := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
		ctxKey2.SetValue(adaptiveStartTimeKey, time.Now().Add(-800*time.Millisecond))
		ctxKey2.SetValue(schemas.BifrostContextKeySelectedKeyID, "key-slow")

		status429 := 429
		bifrostErr := &schemas.BifrostError{
			StatusCode: &status429,
			Error: &schemas.ErrorField{
				Message: "Rate limit reached",
			},
		}
		bifrostErr.PopulateExtraFields(schemas.ChatCompletionRequest, schemas.OpenAI, "gpt-4o", "gpt-4o")
		_, _, err = plugin.PostLLMHook(ctxKey2, nil, bifrostErr)
		require.NoError(t, err)
	}

	filter := plugin.KeyPoolFilter()
	filteredKeys, err := filter(ctx, schemas.OpenAI, "gpt-4o", keys)
	require.NoError(t, err)
	require.Len(t, filteredKeys, 2)

	// key-fast weight should be much greater than key-slow weight
	var weightFast, weightSlow float64
	for _, k := range filteredKeys {
		if k.ID == "key-fast" && k.Weight != nil {
			weightFast = *k.Weight
		}
		if k.ID == "key-slow" && k.Weight != nil {
			weightSlow = *k.Weight
		}
	}

	assert.Greater(t, weightFast, weightSlow)
	assert.Greater(t, weightFast, 0.70)
}

func TestAdaptiveRouting_SelectOptimalTarget(t *testing.T) {
	config := DefaultConfig()
	plugin, err := New(config, nil)
	require.NoError(t, err)
	defer func() {
		_ = plugin.Cleanup()
	}()

	targetA := TargetID{Provider: schemas.OpenAI, Model: "gpt-4o"}
	targetB := TargetID{Provider: schemas.Azure, Model: "gpt-4o"}

	// Record 100 fast calls for targetA (10ms)
	for i := 0; i < 10; i++ {
		plugin.GetStore().RecordMetric(context.Background(), targetA, 10*time.Millisecond, 5*time.Millisecond, 200, false)
	}

	// Record 100 slow calls for targetB (2000ms)
	for i := 0; i < 10; i++ {
		plugin.GetStore().RecordMetric(context.Background(), targetB, 2000*time.Millisecond, 1500*time.Millisecond, 200, false)
	}

	// Over 100 picks, targetA should be selected significantly more often
	pickCountA := 0
	for i := 0; i < 100; i++ {
		picked, ok := plugin.SelectOptimalTarget([]TargetID{targetA, targetB})
		require.True(t, ok)
		if picked.Provider == schemas.OpenAI {
			pickCountA++
		}
	}

	assert.Greater(t, pickCountA, 80)
}

func TestAdaptiveRouting_AdaptiveTargetSelector(t *testing.T) {
	config := DefaultConfig()
	plugin, err := New(config, nil)
	require.NoError(t, err)
	defer func() {
		_ = plugin.Cleanup()
	}()

	provOpenAI := "openai"
	provAzure := "azure"
	model := "gpt-4o"

	targets := []configstoreTables.TableRoutingTarget{
		{Provider: &provOpenAI, Model: &model, Weight: 0.5},
		{Provider: &provAzure, Model: &model, Weight: 0.5},
	}

	targetOpenAI := TargetID{Provider: schemas.OpenAI, Model: "gpt-4o"}
	targetAzure := TargetID{Provider: schemas.Azure, Model: "gpt-4o"}

	// Seed openai as fast (20ms) and azure as slow (1500ms)
	for i := 0; i < 10; i++ {
		plugin.GetStore().RecordMetric(context.Background(), targetOpenAI, 20*time.Millisecond, 10*time.Millisecond, 200, false)
		plugin.GetStore().RecordMetric(context.Background(), targetAzure, 1500*time.Millisecond, 1000*time.Millisecond, 200, false)
	}

	selector := plugin.AdaptiveTargetSelector()
	openaiSelected := 0
	for i := 0; i < 50; i++ {
		selected, ok := selector(targets)
		require.True(t, ok)
		if selected.Provider != nil && *selected.Provider == "openai" {
			openaiSelected++
		}
	}

	// openai should win the majority of adaptive distributions
	assert.Greater(t, openaiSelected, 40)
}

