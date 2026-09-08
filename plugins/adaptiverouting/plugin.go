package adaptiverouting

import (
	"context"
	"fmt"
	"math/rand/v2"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/modelcatalog"
)

const (
	adaptiveStartTimeKey  schemas.BifrostContextKey = "bf-adaptive-start-time"
	adaptiveStreamTTFTKey schemas.BifrostContextKey = "bf-adaptive-stream-ttft"
)

// Plugin implements LLMPlugin, providing telemetry metrics collection and adaptive routing / key selection.
type Plugin struct {
	config      Config
	store       Store
	catalog     *modelcatalog.ModelCatalog
	configStore configstore.ConfigStore
	snapshot    atomic.Pointer[AdaptiveRoutingSnapshot]

	ctx        context.Context
	cancelFunc context.CancelFunc
	wg         sync.WaitGroup
	mu         sync.RWMutex

	// pools caches candidate groups from adaptive routing rules so dynamic weights
	// are computed across targets competing within the same rule (e.g. 4 distinct models)
	// rather than isolating each model into an artificial 1-target group.
	poolsMu sync.RWMutex
	pools   map[string][]CandidateTarget
}

// New creates a new Adaptive Routing plugin.
func New(config Config, store Store, catalog *modelcatalog.ModelCatalog, configStore ...configstore.ConfigStore) (*Plugin, error) {
	if store == nil {
		store = NewMemoryStore(config.Alpha)
	}

	var cs configstore.ConfigStore
	if len(configStore) > 0 {
		cs = configStore[0]
	}

	ctx, cancel := context.WithCancel(context.Background())
	p := &Plugin{
		config:      config,
		store:       store,
		catalog:     catalog,
		configStore: cs,
		ctx:         ctx,
		cancelFunc:  cancel,
		pools:       make(map[string][]CandidateTarget),
	}

	initialSnap := &AdaptiveRoutingSnapshot{
		Weights:   make(map[string][]TargetWeight),
		UpdatedAt: time.Now(),
	}
	p.snapshot.Store(initialSnap)

	p.refreshPoolsFromConfigStore()

	if config.Enabled {
		p.startTuningWorker()
	}

	return p, nil
}

// GetName returns the plugin name.
func (p *Plugin) GetName() string {
	return PluginName
}

// Cleanup stops background workers and clears resources.
func (p *Plugin) Cleanup() error {
	p.cancelFunc()
	p.wg.Wait()
	p.store.ResetAll()
	return nil
}

// GetStore returns the underlying Store.
func (p *Plugin) GetStore() Store {
	return p.store
}

// GetSnapshot returns the current active routing snapshot.
func (p *Plugin) GetSnapshot() *AdaptiveRoutingSnapshot {
	return p.snapshot.Load()
}

// startTuningWorker runs the background periodic recomputation loop.
func (p *Plugin) startTuningWorker() {
	interval := p.config.TuningInterval.D()
	if interval <= 0 {
		interval = 3 * time.Second
	}

	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		for {
			select {
			case <-p.ctx.Done():
				return
			case <-ticker.C:
				p.recomputeActiveWeights()
			}
		}
	}()
}

// RegisterTargetPool registers or updates a group of candidate targets from an adaptive routing rule.
func (p *Plugin) RegisterTargetPool(poolKey string, candidates []CandidateTarget) {
	if len(candidates) <= 1 {
		return
	}
	p.poolsMu.Lock()
	defer p.poolsMu.Unlock()
	p.pools[poolKey] = candidates
}

// refreshPoolsFromConfigStore reads active routing rules with strategy="adaptive" and caches their candidate pools.
func (p *Plugin) refreshPoolsFromConfigStore() {
	if p.configStore == nil {
		return
	}
	rules, err := p.configStore.GetRoutingRules(p.ctx)
	if err != nil {
		return
	}

	p.poolsMu.Lock()
	defer p.poolsMu.Unlock()

	for _, rule := range rules {
		if rule.Strategy != "adaptive" || !rule.EnabledValue() || len(rule.Targets) <= 1 {
			continue
		}

		candidates := make([]CandidateTarget, 0, len(rule.Targets))
		for _, t := range rule.Targets {
			prov := ""
			if t.Provider != nil {
				prov = *t.Provider
			}
			model := ""
			if t.Model != nil {
				model = *t.Model
			}
			keyID := ""
			if t.KeyID != nil {
				keyID = *t.KeyID
			}
			baseW := t.Weight
			if baseW <= 0 {
				baseW = 1.0
			}
			candidates = append(candidates, CandidateTarget{
				TargetID: TargetID{
					Provider: schemas.ModelProvider(prov),
					Model:    model,
					KeyID:    keyID,
				},
				BaseWeight: baseW,
			})
		}
		p.pools[rule.ID] = candidates
	}
}

// recomputeActiveWeights calculates updated weights across all known targets and updates the atomic snapshot.
func (p *Plugin) recomputeActiveWeights() {
	window := p.config.WindowSize.D()
	if window <= 0 {
		window = 5 * time.Minute
	}

	allStats := p.store.GetAllStats(p.ctx, window)
	p.refreshPoolsFromConfigStore()

	newWeights := make(map[string][]TargetWeight)

	// 1. Recompute dynamic weights for each registered adaptive rule pool
	p.poolsMu.RLock()
	for poolKey, poolCandidates := range p.pools {
		if len(poolCandidates) > 1 {
			newWeights[poolKey] = ComputeDynamicWeightsWithBaseWeights(poolCandidates, allStats, p.config)
		}
	}
	p.poolsMu.RUnlock()

	// 2. Also group remaining targets by Model (for fallback Level 1 direction selection)
	modelGroups := make(map[string][]CandidateTarget)
	for target := range allStats {
		if target.Provider == "" || target.Model == "" || target.KeyID != "" {
			continue
		}
		modelGroups[target.Model] = append(modelGroups[target.Model], CandidateTarget{
			TargetID:   target,
			BaseWeight: 1.0,
		})
	}
	for groupKey, candidates := range modelGroups {
		if _, exists := newWeights[groupKey]; !exists && len(candidates) > 1 {
			newWeights[groupKey] = ComputeDynamicWeightsWithBaseWeights(candidates, allStats, p.config)
		}
	}

	newSnap := &AdaptiveRoutingSnapshot{
		Weights:   newWeights,
		UpdatedAt: time.Now(),
	}
	p.snapshot.Store(newSnap)
}

// PreRequestHook records request start time, injects the adaptive target selector for routing
// rules (must happen here — before the routing plugin's own PreRequestHook evaluates rules —
// rather than in PreLLMHook, which runs too late), and automatically resolves the optimal
// provider for unprefixed models via the model catalog (Level 1 Direction Selection).
func (p *Plugin) PreRequestHook(ctx *schemas.BifrostContext, req *schemas.BifrostRequest) error {
	if ctx != nil {
		ctx.SetValue(adaptiveStartTimeKey, time.Now())
		// Inject adaptive target selector so routing rules with strategy="adaptive" can pick
		// targets dynamically. Runs in this hook because the routing plugin evaluates rules
		// during its own PreRequestHook and reads the selector from the context.
		ctx.SetValue(schemas.BifrostContextKeyAdaptiveTargetSelector, p.AdaptiveTargetSelector())
	}
	if !p.config.Enabled || req == nil || p.catalog == nil {
		return nil
	}
	if req.RequestType == schemas.PassthroughRequest || req.RequestType == schemas.PassthroughStreamRequest {
		return nil
	}

	provider, model, existingFallbacks := req.GetRequestFields()
	if provider != "" || model == "" {
		return nil
	}

	providers := p.catalog.GetProvidersForModel(model)
	if len(providers) == 0 {
		return nil
	}

	// Filter by governance allowlist if set
	var allowed []schemas.ModelProvider
	allowlistSet := false
	if ctx != nil {
		allowed, allowlistSet = ctx.Value(schemas.BifrostContextKeyRoutingAllowedProviders).([]schemas.ModelProvider)
	}

	candidates := make([]schemas.ModelProvider, 0, len(providers))
	for _, prov := range providers {
		if allowlistSet && !slices.Contains(allowed, prov) {
			continue
		}
		// Ensure provider has active keys if configured in keyconfig
		if keys := p.catalog.KeysAllowingModel(prov, model); len(keys) == 0 {
			if entries := p.catalog.KeyConfigEntries(prov); len(entries) > 0 {
				continue
			}
		}
		candidates = append(candidates, prov)
	}

	if len(candidates) == 0 {
		return nil
	}

	if len(candidates) == 1 {
		selected := candidates[0]
		req.SetProvider(selected)
		if ctx != nil {
			ctx.AppendRoutingEngineLog(schemas.RoutingEngineAdaptive, schemas.LogLevelInfo, fmt.Sprintf(
				"Single candidate provider %s for model %s selected via adaptive routing", selected, model,
			))
			schemas.AppendToContextList(ctx, schemas.BifrostContextKeyRoutingEnginesUsed, schemas.RoutingEngineAdaptive)
		}
		return nil
	}

	// Multiple candidates: Calculate dynamic weights for Level 1
	targetCandidates := make([]TargetID, len(candidates))
	for i, c := range candidates {
		targetCandidates[i] = TargetID{
			Provider: c,
			Model:    model,
		}
	}

	window := p.config.WindowSize.D()
	if window <= 0 {
		window = 5 * time.Minute
	}
	statsMap := make(map[TargetID]TargetStats, len(targetCandidates))
	for _, tc := range targetCandidates {
		statsMap[tc] = p.store.GetStats(p.ctx, tc, window)
	}
	weights := ComputeDynamicWeights(targetCandidates, statsMap, p.config)

	// Pick target according to computed dynamic weights
	r := rand.Float64()
	var pickedTarget TargetID
	for _, tw := range weights {
		if r <= tw.CumWeight {
			pickedTarget = tw.TargetID
			break
		}
	}
	if pickedTarget.Provider == "" {
		pickedTarget = weights[len(weights)-1].TargetID
	}

	selected := pickedTarget.Provider
	req.SetProvider(selected)

	// Sort candidate weights descending by Score to build ranked fallbacks
	sortedWeights := make([]TargetWeight, len(weights))
	copy(sortedWeights, weights)
	slices.SortFunc(sortedWeights, func(a, b TargetWeight) int {
		if a.Score > b.Score {
			return -1
		} else if a.Score < b.Score {
			return 1
		}
		return 0
	})

	if len(existingFallbacks) == 0 && len(sortedWeights) > 1 {
		fallbacks := make([]schemas.Fallback, 0, len(sortedWeights)-1)
		for _, tw := range sortedWeights {
			if tw.TargetID.Provider == selected {
				continue
			}
			fallbacks = append(fallbacks, schemas.Fallback{
				Provider: tw.TargetID.Provider,
				Model:    model,
			})
		}
		if len(fallbacks) > 0 {
			req.SetFallbacks(fallbacks)
		}
	}

	if ctx != nil {
		candidateStrs := make([]string, len(weights))
		for i, tw := range weights {
			candidateStrs[i] = fmt.Sprintf("%s (weight=%.2f, score=%.1f, latency=%.1fms)", tw.TargetID.Provider, tw.Weight, tw.Score, tw.P90Ms)
		}
		ctx.AppendRoutingEngineLog(schemas.RoutingEngineAdaptive, schemas.LogLevelInfo, fmt.Sprintf(
			"Adaptive routing evaluated %d providers for model %s: [%s]; selected %s",
			len(weights), model, strings.Join(candidateStrs, ", "), selected,
		))
		schemas.AppendToContextList(ctx, schemas.BifrostContextKeyRoutingEnginesUsed, schemas.RoutingEngineAdaptive)
	}

	return nil
}

// PreLLMHook records the request start time if not already present. The adaptive target
// selector is injected in PreRequestHook (see above) — running it here as well would be
// redundant for routing rules and too late anyway.
func (p *Plugin) PreLLMHook(ctx *schemas.BifrostContext, req *schemas.BifrostRequest) (*schemas.BifrostRequest, *schemas.LLMPluginShortCircuit, error) {
	if ctx != nil {
		if ctx.Value(adaptiveStartTimeKey) == nil {
			ctx.SetValue(adaptiveStartTimeKey, time.Now())
		}
	}
	return req, nil, nil
}

// PostLLMHook records performance metrics (duration, TTFT, status, errors) after execution.
func (p *Plugin) PostLLMHook(ctx *schemas.BifrostContext, resp *schemas.BifrostResponse, bifrostErr *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError, error) {
	if !p.config.Enabled || ctx == nil {
		return resp, bifrostErr, nil
	}

	requestType, provider, _, resolvedModel := bifrost.GetResponseFields(resp, bifrostErr)
	// Prefer RoutingInfo — it carries the provider/model pair that THIS attempt actually
	// executed, including fallback attempts. The deprecated ExtraFields triplet mixes the
	// failed primary's model with the fallback's provider on fallbacks (OriginalModelRequested
	// collapses to the caller-sent model), which would fabricate phantom targets like
	// "antigravity/muse-spark-1.3-contributor-free" for a request whose muse target failed
	// over to antigravity. RoutingInfo.Model is always the wire model of this attempt.
	routingInfo := bifrost.GetResponseRoutingInfo(resp, bifrostErr)
	if routingInfo.Provider != "" {
		provider = routingInfo.Provider
	}
	if routingInfo.Model != "" {
		resolvedModel = routingInfo.Model
	}
	model := resolvedModel
	// Only record metrics for attempts with BOTH a provider and a model: targets without
	// a model (e.g. provider-level probes) cannot be attributed to any routing rule and
	// would render as "provider/" rows on the dashboard.
	if provider == "" || model == "" {
		return resp, bifrostErr, nil
	}

	keyID := bifrost.GetStringFromContext(ctx, schemas.BifrostContextKeySelectedKeyID)
	target := TargetID{
		Provider: provider,
		Model:    model,
		KeyID:    keyID,
	}

	isStreaming := bifrost.IsStreamRequestType(requestType)

	// In streaming mode, capture TTFT on chunk 0
	if isStreaming && resp != nil {
		extraFields := resp.GetExtraFields()
		if extraFields != nil && extraFields.ChunkIndex == 0 && extraFields.Latency > 0 {
			ttft := time.Duration(extraFields.Latency) * time.Millisecond
			ctx.SetValue(adaptiveStreamTTFTKey, ttft)
		}
	}

	// For streams, only record metric on error or when the stream has ended (final chunk)
	if isStreaming && bifrostErr == nil {
		streamEndVal := ctx.Value(schemas.BifrostContextKeyStreamEndIndicator)
		isFinalChunk, isFinalChunkBool := streamEndVal.(bool)
		if !isFinalChunkBool || !isFinalChunk {
			// Intermediate chunk: do not record sample to prevent inflating request counts
			return resp, bifrostErr, nil
		}
	}

	var duration time.Duration
	if start, ok := ctx.Value(adaptiveStartTimeKey).(time.Time); ok {
		duration = time.Since(start)
	}

	var ttft time.Duration
	if isStreaming {
		if cachedTTFT, ok := ctx.Value(adaptiveStreamTTFTKey).(time.Duration); ok {
			ttft = cachedTTFT
		}
	} else if resp != nil {
		extraFields := resp.GetExtraFields()
		if extraFields != nil && extraFields.Latency > 0 && extraFields.ChunkIndex == 0 {
			ttft = time.Duration(extraFields.Latency) * time.Millisecond
		}
	}

	statusCode := 200
	isError := false

	if bifrostErr != nil {
		isError = true
		if bifrostErr.StatusCode != nil {
			statusCode = *bifrostErr.StatusCode
		} else {
			statusCode = 500
		}
	}

	p.store.RecordMetric(p.ctx, target, duration, ttft, statusCode, isError)

	// Also record at the Provider+Model level (without keyID) for provider-level cross routing
	if keyID != "" {
		targetNoKey := TargetID{
			Provider: provider,
			Model:    model,
		}
		p.store.RecordMetric(p.ctx, targetNoKey, duration, ttft, statusCode, isError)
	}

	return resp, bifrostErr, nil
}

// SelectOptimalTarget chooses the best TargetID among candidates based on pre-computed active weights.
func (p *Plugin) SelectOptimalTarget(candidates []TargetID) (TargetID, bool) {
	if len(candidates) == 0 {
		return TargetID{}, false
	}
	if len(candidates) == 1 {
		return candidates[0], true
	}

	window := p.config.WindowSize.D()
	if window <= 0 {
		window = 5 * time.Minute
	}

	statsMap := make(map[TargetID]TargetStats, len(candidates))
	for _, c := range candidates {
		statsMap[c] = p.store.GetStats(p.ctx, c, window)
	}

	weights := ComputeDynamicWeights(candidates, statsMap, p.config)
	if len(weights) == 0 {
		return candidates[0], true
	}

	r := rand.Float64()
	for _, tw := range weights {
		if r <= tw.CumWeight {
			return tw.TargetID, true
		}
	}
	return weights[len(weights)-1].TargetID, true
}

// SelectOptimalTargetWithBaseWeights chooses the best TargetID among candidates with base weights.
func (p *Plugin) SelectOptimalTargetWithBaseWeights(candidates []CandidateTarget) (TargetID, bool) {
	if len(candidates) == 0 {
		return TargetID{}, false
	}
	if len(candidates) == 1 {
		return candidates[0].TargetID, true
	}

	window := p.config.WindowSize.D()
	if window <= 0 {
		window = 5 * time.Minute
	}

	statsMap := make(map[TargetID]TargetStats, len(candidates))
	for _, c := range candidates {
		statsMap[c.TargetID] = p.store.GetStats(p.ctx, c.TargetID, window)
	}

	weights := ComputeDynamicWeightsWithBaseWeights(candidates, statsMap, p.config)
	if len(weights) == 0 {
		return candidates[0].TargetID, true
	}

	r := rand.Float64()
	for _, tw := range weights {
		if r <= tw.CumWeight {
			return tw.TargetID, true
		}
	}
	return weights[len(weights)-1].TargetID, true
}

// AdaptiveTargetSelector returns a selector closure for selecting between TableRoutingTarget options dynamically.
func (p *Plugin) AdaptiveTargetSelector() func(targets []configstoreTables.TableRoutingTarget) (configstoreTables.TableRoutingTarget, bool) {
	return func(targets []configstoreTables.TableRoutingTarget) (configstoreTables.TableRoutingTarget, bool) {
		if !p.config.Enabled || len(targets) == 0 {
			return configstoreTables.TableRoutingTarget{}, false
		}
		if len(targets) == 1 {
			return targets[0], true
		}

		candidates := make([]CandidateTarget, 0, len(targets))
		for _, t := range targets {
			prov := ""
			if t.Provider != nil {
				prov = *t.Provider
			}
			model := ""
			if t.Model != nil {
				model = *t.Model
			}
			keyID := ""
			if t.KeyID != nil {
				keyID = *t.KeyID
			}

			baseW := t.Weight
			if baseW <= 0 {
				baseW = 1.0
			}

			candidates = append(candidates, CandidateTarget{
				TargetID: TargetID{
					Provider: schemas.ModelProvider(prov),
					Model:    model,
					KeyID:    keyID,
				},
				BaseWeight: baseW,
			})
		}

		// Register the pool of candidates for this rule so background weight tuning
		// computes real dynamic weights across targets competing in this rule.
		ruleID := targets[0].RuleID
		if ruleID == "" {
			sigs := make([]string, len(candidates))
			for i, c := range candidates {
				sigs[i] = c.TargetID.String()
			}
			ruleID = strings.Join(sigs, ";")
		}
		p.RegisterTargetPool(ruleID, candidates)

		pickedTarget, ok := p.SelectOptimalTargetWithBaseWeights(candidates)
		if !ok {
			return targets[0], true
		}

		// Find the matching TableRoutingTarget
		for _, t := range targets {
			tProv := ""
			if t.Provider != nil {
				tProv = *t.Provider
			}
			tModel := ""
			if t.Model != nil {
				tModel = *t.Model
			}
			tKeyID := ""
			if t.KeyID != nil {
				tKeyID = *t.KeyID
			}

			if tProv == string(pickedTarget.Provider) && tModel == pickedTarget.Model && tKeyID == pickedTarget.KeyID {
				return t, true
			}
		}

		return targets[0], true
	}
}

// KeyPoolFilter returns a KeyPoolFilter function that dynamically sorts/filters candidate keys.
func (p *Plugin) KeyPoolFilter() schemas.KeyPoolFilter {
	return func(ctx *schemas.BifrostContext, provider schemas.ModelProvider, model string, keys []schemas.Key) ([]schemas.Key, error) {
		if !p.config.Enabled || len(keys) <= 1 {
			return keys, nil
		}

		candidates := make([]TargetID, 0, len(keys))
		for _, k := range keys {
			if k.Enabled != nil && !*k.Enabled {
				continue
			}
			candidates = append(candidates, TargetID{
				Provider: provider,
				Model:    model,
				KeyID:    k.ID,
			})
		}

		if len(candidates) <= 1 {
			return keys, nil
		}

		// Reorder keys based on dynamic weights
		window := p.config.WindowSize.D()
		if window <= 0 {
			window = 5 * time.Minute
		}
		statsMap := make(map[TargetID]TargetStats, len(candidates))
		for _, c := range candidates {
			statsMap[c] = p.store.GetStats(p.ctx, c, window)
		}
		weights := ComputeDynamicWeights(candidates, statsMap, p.config)

		// Map key ID to computed weight
		weightMap := make(map[string]float64, len(weights))
		for _, tw := range weights {
			weightMap[tw.TargetID.KeyID] = tw.Weight
		}

		// Clone and assign dynamic weight to Key objects
		result := make([]schemas.Key, len(keys))
		for i, k := range keys {
			clonedKey := k
			if w, ok := weightMap[k.ID]; ok {
				clonedKey.Weight = w
			}
			result[i] = clonedKey
		}

		return result, nil
	}
}

// GetMetricsSummary generates a complete telemetry snapshot and summary statistics for real-time monitoring.
func (p *Plugin) GetMetricsSummary() AdaptiveMetricsSummary {
	window := p.config.WindowSize.D()
	if window <= 0 {
		window = 5 * time.Minute
	}

	allStats := p.store.GetAllStats(p.ctx, window)
	snapshot := p.GetSnapshot()

	// Build lookup for active dynamic weights from the latest snapshot.
	// Precedence (deterministic): multi-target rule pools first, then keyless
	// provider/model groups. Keyed targets (#keyID) never overwrite the keyless
	// entry they mirror — map iteration order is randomized in Go, so letting
	// both write the same map key made weights flap between rule-pool values
	// (e.g. 8.9%) and 50/50 model-group values on every dashboard refresh.
	dynamicWeightMap := make(map[string]float64)
	if snapshot != nil && snapshot.Weights != nil {
		poolKeys := make([]string, 0, len(snapshot.Weights))
		for poolKey := range snapshot.Weights {
			poolKeys = append(poolKeys, poolKey)
		}
		slices.Sort(poolKeys)
		// Pass 1: pools with more than one target (adaptive rule pools) win.
		for _, poolKey := range poolKeys {
			twSlice := snapshot.Weights[poolKey]
			if len(twSlice) <= 1 {
				continue
			}
			for _, tw := range twSlice {
				dynamicWeightMap[tw.TargetID.String()] = tw.Weight
			}
		}
		// Pass 2: fill any remaining targets from model groups (keyless entries only,
		// so a keyed duplicate cannot clobber its keyless sibling's rule weight).
		for _, poolKey := range poolKeys {
			twSlice := snapshot.Weights[poolKey]
			if len(twSlice) != 1 {
				continue
			}
			tw := twSlice[0]
			if _, exists := dynamicWeightMap[tw.TargetID.String()]; !exists {
				dynamicWeightMap[tw.TargetID.String()] = tw.Weight
			}
		}
	}

	metricsList := make([]TargetMetricView, 0, len(allStats))
	var totalEWMA float64
	var totalTTFT float64
	var ttftCount int
	var total429s int64

	for target, stats := range allStats {
		// Only display targets that have both a provider and a model — model-less
		// entries cannot be attributed to any routing rule target.
		if target.Provider == "" || target.Model == "" {
			continue
		}

		var successRate float64 = 100.0
		if stats.TotalRequests > 0 {
			successRate = (float64(stats.SuccessCount) / float64(stats.TotalRequests)) * 100.0
		}

		status := "healthy"
		if stats.RateLimit429Count > 0 || stats.Error5xxCount > 0 || successRate < 95.0 {
			status = "degraded"
		} else if stats.EWMALatencyMs > 0 && stats.EWMALatencyMs <= 100.0 {
			status = "optimal"
		}

		dynW, hasDynW := dynamicWeightMap[target.String()]
		if !hasDynW {
			// Fallback to provider/model level without keyID
			targetNoKey := TargetID{Provider: target.Provider, Model: target.Model}
			if w, ok := dynamicWeightMap[targetNoKey.String()]; ok {
				dynW = w
			}
		}

		total429s += stats.RateLimit429Count
		totalEWMA += stats.EWMALatencyMs
		if stats.TTFTMs > 0 {
			totalTTFT += stats.TTFTMs
			ttftCount++
		}

		metricsList = append(metricsList, TargetMetricView{
			Target:            target.String(),
			Provider:          string(target.Provider),
			Model:             target.Model,
			KeyID:             target.KeyID,
			EWMALatencyMs:     stats.EWMALatencyMs,
			TTFTMs:            stats.TTFTMs,
			P90LatencyMs:      stats.P90LatencyMs,
			SuccessRate:       successRate,
			RateLimit429Count: stats.RateLimit429Count,
			ErrorCount:        stats.Error5xxCount,
			TotalRequests:     stats.TotalRequests,
			DynamicWeight:     dynW,
			Status:            status,
		})
	}

	// Sort metrics by Target string for stable ordering
	slices.SortFunc(metricsList, func(a, b TargetMetricView) int {
		return strings.Compare(a.Target, b.Target)
	})

	var avgEWMA float64
	if len(metricsList) > 0 {
		avgEWMA = totalEWMA / float64(len(metricsList))
	}
	var avgTTFT float64
	if ttftCount > 0 {
		avgTTFT = totalTTFT / float64(ttftCount)
	}

	var res AdaptiveMetricsSummary
	res.Metrics = metricsList
	res.Summary.AvgEWMALatencyMs = avgEWMA
	res.Summary.AvgTTFTMs = avgTTFT
	res.Summary.TotalTargets = len(metricsList)
	res.Summary.Total429s = total429s

	return res
}

// GetConfig returns the current plugin configuration.
func (p *Plugin) GetConfig() Config {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.config
}

// UpdateConfig safely updates the configuration and triggers an immediate weight recalculation.
func (p *Plugin) UpdateConfig(cfg Config) {
	p.mu.Lock()
	p.config = cfg
	p.mu.Unlock()

	p.recomputeActiveWeights()
}
