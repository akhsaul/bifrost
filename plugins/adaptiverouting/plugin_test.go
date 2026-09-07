package adaptiverouting

import (
	"context"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/modelcatalog"
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
	plugin, err := New(config, nil, nil)
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
		if k.ID == "key-fast" {
			weightFast = k.Weight
		}
		if k.ID == "key-slow" {
			weightSlow = k.Weight
		}
	}

	assert.Greater(t, weightFast, weightSlow)
	assert.Greater(t, weightFast, 0.70)
}

func TestAdaptiveRouting_SelectOptimalTarget(t *testing.T) {
	config := DefaultConfig()
	plugin, err := New(config, nil, nil)
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
	plugin, err := New(config, nil, nil)
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

func TestAdaptiveRouting_Level1ModelCatalogResolution(t *testing.T) {
	catalog := modelcatalog.NewTestCatalog(map[string]string{
		"gpt-4o": "gpt-4o",
	})
	catalog.UpsertLive(schemas.OpenAI, "key-openai", false, []string{"gpt-4o"})
	catalog.UpsertLive(schemas.Azure, "key-azure", false, []string{"gpt-4o"})
	catalog.SetKeyConfigForProvider(schemas.OpenAI, []schemas.Key{{ID: "key-openai", Enabled: schemas.Ptr(true), Models: schemas.WhiteList{"*"}}})
	catalog.SetKeyConfigForProvider(schemas.Azure, []schemas.Key{{ID: "key-azure", Enabled: schemas.Ptr(true), Models: schemas.WhiteList{"*"}}})

	config := DefaultConfig()
	plugin, err := New(config, nil, catalog)
	require.NoError(t, err)
	defer func() {
		_ = plugin.Cleanup()
	}()

	targetOpenAI := TargetID{Provider: schemas.OpenAI, Model: "gpt-4o"}
	targetAzure := TargetID{Provider: schemas.Azure, Model: "gpt-4o"}

	// Seed OpenAI as super fast (30ms) and Azure as slow (1500ms) with errors
	for i := 0; i < 10; i++ {
		plugin.GetStore().RecordMetric(context.Background(), targetOpenAI, 30*time.Millisecond, 15*time.Millisecond, 200, false)
		plugin.GetStore().RecordMetric(context.Background(), targetAzure, 1500*time.Millisecond, 1000*time.Millisecond, 500, true)
	}

	// Request with model="gpt-4o" and provider=""
	req := &schemas.BifrostRequest{
		RequestType: schemas.ChatCompletionRequest,
		ChatRequest: &schemas.BifrostChatRequest{
			Model: "gpt-4o",
		},
	}
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)

	err = plugin.PreRequestHook(ctx, req)
	require.NoError(t, err)

	prov, model, fallbacks := req.GetRequestFields()
	assert.Equal(t, schemas.OpenAI, prov)
	assert.Equal(t, "gpt-4o", model)
	require.Len(t, fallbacks, 1)
	assert.Equal(t, schemas.Azure, fallbacks[0].Provider)

	// Check routing engine logs and engines used
	enginesUsed, ok := ctx.Value(schemas.BifrostContextKeyRoutingEnginesUsed).([]string)
	require.True(t, ok)
	assert.Contains(t, enginesUsed, schemas.RoutingEngineAdaptive)
}

func TestAdaptiveRouting_Level1GovernanceAllowlist(t *testing.T) {
	catalog := modelcatalog.NewTestCatalog(map[string]string{
		"gpt-4o": "gpt-4o",
	})
	catalog.UpsertLive(schemas.OpenAI, "key-openai", false, []string{"gpt-4o"})
	catalog.UpsertLive(schemas.Azure, "key-azure", false, []string{"gpt-4o"})
	catalog.SetKeyConfigForProvider(schemas.OpenAI, []schemas.Key{{ID: "key-openai", Enabled: schemas.Ptr(true), Models: schemas.WhiteList{"*"}}})
	catalog.SetKeyConfigForProvider(schemas.Azure, []schemas.Key{{ID: "key-azure", Enabled: schemas.Ptr(true), Models: schemas.WhiteList{"*"}}})

	config := DefaultConfig()
	plugin, err := New(config, nil, catalog)
	require.NoError(t, err)
	defer func() {
		_ = plugin.Cleanup()
	}()

	targetOpenAI := TargetID{Provider: schemas.OpenAI, Model: "gpt-4o"}
	targetAzure := TargetID{Provider: schemas.Azure, Model: "gpt-4o"}

	// Even though OpenAI is faster, restrict allowlist to Azure only
	plugin.GetStore().RecordMetric(context.Background(), targetOpenAI, 20*time.Millisecond, 10*time.Millisecond, 200, false)
	plugin.GetStore().RecordMetric(context.Background(), targetAzure, 800*time.Millisecond, 400*time.Millisecond, 200, false)

	req := &schemas.BifrostRequest{
		RequestType: schemas.ChatCompletionRequest,
		ChatRequest: &schemas.BifrostChatRequest{
			Model: "gpt-4o",
		},
	}
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyRoutingAllowedProviders, []schemas.ModelProvider{schemas.Azure})

	err = plugin.PreRequestHook(ctx, req)
	require.NoError(t, err)

	prov, model, fallbacks := req.GetRequestFields()
	assert.Equal(t, schemas.Azure, prov)
	assert.Equal(t, "gpt-4o", model)
	assert.Empty(t, fallbacks) // OpenAI was excluded by allowlist, so no fallback
}

func TestAdaptiveRouting_Level1ThreeProvidersFallbackRanking(t *testing.T) {
	catalog := modelcatalog.NewTestCatalog(map[string]string{
		"llama-3.3-70b": "llama-3.3-70b",
	})
	catalog.UpsertLive(schemas.Groq, "key-groq", false, []string{"llama-3.3-70b"})
	catalog.UpsertLive(schemas.Cerebras, "key-cerebras", false, []string{"llama-3.3-70b"})
	catalog.UpsertLive(schemas.Fireworks, "key-fireworks", false, []string{"llama-3.3-70b"})

	catalog.SetKeyConfigForProvider(schemas.Groq, []schemas.Key{{ID: "key-groq", Enabled: schemas.Ptr(true), Models: schemas.WhiteList{"*"}}})
	catalog.SetKeyConfigForProvider(schemas.Cerebras, []schemas.Key{{ID: "key-cerebras", Enabled: schemas.Ptr(true), Models: schemas.WhiteList{"*"}}})
	catalog.SetKeyConfigForProvider(schemas.Fireworks, []schemas.Key{{ID: "key-fireworks", Enabled: schemas.Ptr(true), Models: schemas.WhiteList{"*"}}})

	config := DefaultConfig()
	plugin, err := New(config, nil, catalog)
	require.NoError(t, err)
	defer func() {
		_ = plugin.Cleanup()
	}()

	targetCerebras := TargetID{Provider: schemas.Cerebras, Model: "llama-3.3-70b"}
	targetGroq := TargetID{Provider: schemas.Groq, Model: "llama-3.3-70b"}
	targetFireworks := TargetID{Provider: schemas.Fireworks, Model: "llama-3.3-70b"}

	// Cerebras is fastest (20ms), Groq is medium (80ms), Fireworks is slowest (500ms)
	for i := 0; i < 10; i++ {
		plugin.GetStore().RecordMetric(context.Background(), targetCerebras, 20*time.Millisecond, 10*time.Millisecond, 200, false)
		plugin.GetStore().RecordMetric(context.Background(), targetGroq, 80*time.Millisecond, 40*time.Millisecond, 200, false)
		plugin.GetStore().RecordMetric(context.Background(), targetFireworks, 500*time.Millisecond, 250*time.Millisecond, 200, false)
	}

	cerebrasPrimaryCount := 0
	for iter := 0; iter < 100; iter++ {
		req := &schemas.BifrostRequest{
			RequestType: schemas.ChatCompletionRequest,
			ChatRequest: &schemas.BifrostChatRequest{
				Model: "llama-3.3-70b",
			},
		}
		ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)

		err = plugin.PreRequestHook(ctx, req)
		require.NoError(t, err)

		prov, model, fallbacks := req.GetRequestFields()
		assert.Equal(t, "llama-3.3-70b", model)
		require.Len(t, fallbacks, 2)

		if prov == schemas.Cerebras {
			cerebrasPrimaryCount++
			// When Cerebras is primary, fallbacks should be [Groq, Fireworks]
			assert.Equal(t, schemas.Groq, fallbacks[0].Provider)
			assert.Equal(t, schemas.Fireworks, fallbacks[1].Provider)
		}

		// Fireworks (slowest) should never be the first fallback if Cerebras or Groq is available
		if prov != schemas.Fireworks {
			assert.NotEqual(t, schemas.Fireworks, fallbacks[0].Provider)
		}
	}

	// Cerebras is 4x faster than Groq and 25x faster than Fireworks, should be primary > 70% of the time
	assert.Greater(t, cerebrasPrimaryCount, 70)
}

func TestAdaptiveRouting_ExistingFallbacksPreserved(t *testing.T) {
	catalog := modelcatalog.NewTestCatalog(map[string]string{
		"gpt-4o": "gpt-4o",
	})
	catalog.UpsertLive(schemas.OpenAI, "key-openai", false, []string{"gpt-4o"})
	catalog.UpsertLive(schemas.Azure, "key-azure", false, []string{"gpt-4o"})
	catalog.SetKeyConfigForProvider(schemas.OpenAI, []schemas.Key{{ID: "key-openai", Enabled: schemas.Ptr(true), Models: schemas.WhiteList{"*"}}})
	catalog.SetKeyConfigForProvider(schemas.Azure, []schemas.Key{{ID: "key-azure", Enabled: schemas.Ptr(true), Models: schemas.WhiteList{"*"}}})

	config := DefaultConfig()
	plugin, err := New(config, nil, catalog)
	require.NoError(t, err)
	defer func() {
		_ = plugin.Cleanup()
	}()

	existingFallback := schemas.Fallback{Provider: schemas.Bedrock, Model: "anthropic.claude-v2"}
	req := &schemas.BifrostRequest{
		RequestType: schemas.ChatCompletionRequest,
		ChatRequest: &schemas.BifrostChatRequest{
			Model:     "gpt-4o",
			Fallbacks: []schemas.Fallback{existingFallback},
		},
	}
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)

	err = plugin.PreRequestHook(ctx, req)
	require.NoError(t, err)

	prov, _, fallbacks := req.GetRequestFields()
	assert.NotEmpty(t, prov)
	require.Len(t, fallbacks, 1)
	assert.Equal(t, schemas.Bedrock, fallbacks[0].Provider)
}

// TestAdaptiveRouting_SelectorInjectedInPreRequestHook verifies that the AdaptiveTargetSelector
// is available on the BifrostContext immediately after PreRequestHook — not deferred to
// PreLLMHook. This is required so the routing plugin (which evaluates rules in its own
// PreRequestHook, running after this plugin) can consult the selector for strategy="adaptive" rules.
func TestAdaptiveRouting_SelectorInjectedInPreRequestHook(t *testing.T) {
	config := DefaultConfig()
	plugin, err := New(config, nil, nil)
	require.NoError(t, err)
	defer func() { _ = plugin.Cleanup() }()

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	req := &schemas.BifrostRequest{
		RequestType: schemas.ChatCompletionRequest,
		ChatRequest: &schemas.BifrostChatRequest{Model: "gpt-4o"},
	}

	// Before PreRequestHook the selector must be absent
	assert.Nil(t, ctx.Value(schemas.BifrostContextKeyAdaptiveTargetSelector))

	err = plugin.PreRequestHook(ctx, req)
	require.NoError(t, err)

	// After PreRequestHook the selector must be present and callable
	selectorVal := ctx.Value(schemas.BifrostContextKeyAdaptiveTargetSelector)
	require.NotNil(t, selectorVal, "AdaptiveTargetSelector must be set on context after PreRequestHook")

	selector, ok := selectorVal.(func(targets []configstoreTables.TableRoutingTarget) (configstoreTables.TableRoutingTarget, bool))
	require.True(t, ok, "selector must have the correct function signature")

	// Calling it with a single target should return that target
	prov := "openai"
	model := "gpt-4o"
	targets := []configstoreTables.TableRoutingTarget{{Provider: &prov, Model: &model, Weight: 1.0}}
	picked, ok := selector(targets)
	require.True(t, ok)
	assert.Equal(t, "openai", *picked.Provider)
}

func TestAdaptiveRouting_StreamingRecordsOnlyOnFinalChunk(t *testing.T) {
	config := DefaultConfig()
	plugin, err := New(config, nil, nil)
	require.NoError(t, err)
	defer func() { _ = plugin.Cleanup() }()

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(adaptiveStartTimeKey, time.Now().Add(-100*time.Millisecond))

	targetID := TargetID{Provider: schemas.OpenAI, Model: "gpt-4o"}

	// Simulate intermediate chunks (indices 0 to 3) with isFinalChunk = false
	for i := 0; i < 4; i++ {
		resp := &schemas.BifrostResponse{
			ChatResponse: &schemas.BifrostChatResponse{
				ExtraFields: schemas.BifrostResponseExtraFields{
					RequestType:       schemas.ChatCompletionStreamRequest,
					Provider:          schemas.OpenAI,
					ResolvedModelUsed: "gpt-4o",
					ChunkIndex:        i,
					Latency:           25, // TTFT on chunk 0
				},
			},
		}
		_, _, err := plugin.PostLLMHook(ctx, resp, nil)
		require.NoError(t, err)

		// After intermediate chunks, stats must still be 0 (no record yet)
		stats := plugin.store.GetStats(context.Background(), targetID, 5*time.Minute)
		assert.Equal(t, int64(0), stats.TotalRequests)
	}

	// Final chunk: stream end indicator is true
	ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
	finalResp := &schemas.BifrostResponse{
		ChatResponse: &schemas.BifrostChatResponse{
			ExtraFields: schemas.BifrostResponseExtraFields{
				RequestType:       schemas.ChatCompletionStreamRequest,
				Provider:          schemas.OpenAI,
				ResolvedModelUsed: "gpt-4o",
				ChunkIndex:        4,
			},
		},
	}
	_, _, err = plugin.PostLLMHook(ctx, finalResp, nil)
	require.NoError(t, err)

	// Now exactly 1 request sample should be recorded
	stats := plugin.store.GetStats(context.Background(), targetID, 5*time.Minute)
	assert.Equal(t, int64(1), stats.TotalRequests)
	assert.Equal(t, int64(1), stats.SuccessCount)
	assert.InDelta(t, 25.0, stats.TTFTMs, 0.1, "TTFT from chunk 0 must be preserved")
}

func TestAdaptiveRouting_BaseWeightRespectedWhenLatencyEqual(t *testing.T) {
	config := DefaultConfig()
	config.ExplorationFloor = 0.0

	targetA := TargetID{Provider: schemas.OpenAI, Model: "model-a"}
	targetB := TargetID{Provider: schemas.Anthropic, Model: "model-b"}

	candidates := []CandidateTarget{
		{TargetID: targetA, BaseWeight: 0.8},
		{TargetID: targetB, BaseWeight: 0.2},
	}

	// Equal latencies
	statsMap := map[TargetID]TargetStats{
		targetA: {EWMALatencyMs: 100.0, TotalRequests: 10, SuccessCount: 10},
		targetB: {EWMALatencyMs: 100.0, TotalRequests: 10, SuccessCount: 10},
	}

	weights := ComputeDynamicWeightsWithBaseWeights(candidates, statsMap, config)
	require.Len(t, weights, 2)

	// With equal latencies, weights should reflect the 0.8 / 0.2 ratio (80% vs 20%)
	assert.InDelta(t, 0.8, weights[0].Weight, 0.01)
	assert.InDelta(t, 0.2, weights[1].Weight, 0.01)
}

func TestAdaptiveRouting_GetMetricsSummary(t *testing.T) {
	config := DefaultConfig()
	plugin, err := New(config, nil, nil)
	require.NoError(t, err)
	defer func() { _ = plugin.Cleanup() }()

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(adaptiveStartTimeKey, time.Now().Add(-50*time.Millisecond))

	resp := &schemas.BifrostResponse{
		ChatResponse: &schemas.BifrostChatResponse{
			ExtraFields: schemas.BifrostResponseExtraFields{
				RequestType:       schemas.ChatCompletionRequest,
				Provider:          schemas.OpenAI,
				ResolvedModelUsed: "gpt-4o",
			},
		},
	}
	_, _, err = plugin.PostLLMHook(ctx, resp, nil)
	require.NoError(t, err)

	summary := plugin.GetMetricsSummary()
	assert.GreaterOrEqual(t, summary.Summary.TotalTargets, 1)
	assert.Equal(t, int64(0), summary.Summary.Total429s)

	found := false
	for _, m := range summary.Metrics {
		if m.Provider == "openai" && m.Model == "gpt-4o" {
			found = true
			assert.Equal(t, int64(1), m.TotalRequests)
			assert.Equal(t, 100.0, m.SuccessRate)
			assert.Equal(t, "optimal", m.Status)
		}
	}
	assert.True(t, found, "openai/gpt-4o target metric must be present in summary")
}
