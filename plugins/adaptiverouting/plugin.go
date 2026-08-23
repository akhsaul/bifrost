package adaptiverouting

import (
	"context"
	"math/rand/v2"
	"sync"
	"sync/atomic"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
)

const (
	adaptiveStartTimeKey schemas.BifrostContextKey = "bf-adaptive-start-time"
)

// Plugin implements LLMPlugin, providing telemetry metrics collection and adaptive routing / key selection.
type Plugin struct {
	config   Config
	store    Store
	snapshot atomic.Pointer[AdaptiveRoutingSnapshot]

	ctx        context.Context
	cancelFunc context.CancelFunc
	wg         sync.WaitGroup
	mu         sync.RWMutex
}

// New creates a new Adaptive Routing plugin.
func New(config Config, store Store) (*Plugin, error) {
	if store == nil {
		store = NewMemoryStore(config.Alpha)
	}

	ctx, cancel := context.WithCancel(context.Background())
	p := &Plugin{
		config:     config,
		store:      store,
		ctx:        ctx,
		cancelFunc: cancel,
	}

	initialSnap := &AdaptiveRoutingSnapshot{
		Weights:   make(map[string][]TargetWeight),
		UpdatedAt: time.Now(),
	}
	p.snapshot.Store(initialSnap)

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

// recomputeActiveWeights calculates updated weights across all known targets and updates the atomic snapshot.
func (p *Plugin) recomputeActiveWeights() {
	window := p.config.WindowSize.D()
	if window <= 0 {
		window = 5 * time.Minute
	}

	allStats := p.store.GetAllStats(p.ctx, window)
	if len(allStats) == 0 {
		return
	}

	// Group targets by model/pool
	modelGroups := make(map[string][]TargetID)
	for target := range allStats {
		groupKey := target.Model
		if groupKey == "" {
			groupKey = string(target.Provider)
		}
		modelGroups[groupKey] = append(modelGroups[groupKey], target)
	}

	newWeights := make(map[string][]TargetWeight, len(modelGroups))
	for groupKey, candidates := range modelGroups {
		newWeights[groupKey] = ComputeDynamicWeights(candidates, allStats, p.config)
	}

	newSnap := &AdaptiveRoutingSnapshot{
		Weights:   newWeights,
		UpdatedAt: time.Now(),
	}
	p.snapshot.Store(newSnap)
}

// PreRequestHook records the request start time into the context.
func (p *Plugin) PreRequestHook(ctx *schemas.BifrostContext, _ *schemas.BifrostRequest) error {
	if ctx != nil {
		ctx.SetValue(adaptiveStartTimeKey, time.Now())
	}
	return nil
}

// PreLLMHook records the request start time if not already present and injects the adaptive target selector.
func (p *Plugin) PreLLMHook(ctx *schemas.BifrostContext, req *schemas.BifrostRequest) (*schemas.BifrostRequest, *schemas.LLMPluginShortCircuit, error) {
	if ctx != nil {
		if ctx.Value(adaptiveStartTimeKey) == nil {
			ctx.SetValue(adaptiveStartTimeKey, time.Now())
		}
		// Inject adaptive target selector function for governance routing rules
		ctx.SetValue(schemas.BifrostContextKeyAdaptiveTargetSelector, p.AdaptiveTargetSelector())
	}
	return req, nil, nil
}

// PostLLMHook records performance metrics (duration, TTFT, status, errors) after execution.
func (p *Plugin) PostLLMHook(ctx *schemas.BifrostContext, resp *schemas.BifrostResponse, bifrostErr *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError, error) {
	if !p.config.Enabled || ctx == nil {
		return resp, bifrostErr, nil
	}

	_, provider, originalModel, resolvedModel := bifrost.GetResponseFields(resp, bifrostErr)
	model := resolvedModel
	if model == "" {
		model = originalModel
	}
	if provider == "" && model == "" {
		return resp, bifrostErr, nil
	}

	keyID := bifrost.GetStringFromContext(ctx, schemas.BifrostContextKeySelectedKeyID)
	target := TargetID{
		Provider: provider,
		Model:    model,
		KeyID:    keyID,
	}

	var duration time.Duration
	if start, ok := ctx.Value(adaptiveStartTimeKey).(time.Time); ok {
		duration = time.Since(start)
	}

	var ttft time.Duration
	statusCode := 200
	isError := false

	if resp != nil {
		extraFields := resp.GetExtraFields()
		if extraFields.Latency > 0 && extraFields.ChunkIndex == 0 {
			ttft = time.Duration(extraFields.Latency) * time.Millisecond
		}
	}

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

// AdaptiveTargetSelector returns a selector closure for selecting between TableRoutingTarget options dynamically.
func (p *Plugin) AdaptiveTargetSelector() func(targets []configstoreTables.TableRoutingTarget) (configstoreTables.TableRoutingTarget, bool) {
	return func(targets []configstoreTables.TableRoutingTarget) (configstoreTables.TableRoutingTarget, bool) {
		if !p.config.Enabled || len(targets) == 0 {
			return configstoreTables.TableRoutingTarget{}, false
		}
		if len(targets) == 1 {
			return targets[0], true
		}

		candidates := make([]TargetID, 0, len(targets))
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

			candidates = append(candidates, TargetID{
				Provider: schemas.ModelProvider(prov),
				Model:    model,
				KeyID:    keyID,
			})
		}

		pickedTarget, ok := p.SelectOptimalTarget(candidates)
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
				clonedKey.Weight = schemas.Ptr(w)
			}
			result[i] = clonedKey
		}

		return result, nil
	}
}
