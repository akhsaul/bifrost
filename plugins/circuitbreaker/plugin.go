package circuitbreaker

import (
	"fmt"
	"regexp"
	"sync"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
)

// Plugin implements LLMPlugin and provides KeyPoolFilter functionality.
type Plugin struct {
	config         Config
	store          Store
	customPatterns []*regexp.Regexp
	mu             sync.RWMutex
}

// New creates a new Circuit Breaker plugin with the provided config and optional store.
func New(config Config, store Store) (*Plugin, error) {
	if store == nil {
		store = NewMemoryStore()
	}

	// Default fallback cooldown to 5 hours if not set
	if config.DefaultCooldown.D() <= 0 {
		config.DefaultCooldown = schemas.Duration(5 * time.Hour)
	}

	var compiledPatterns []*regexp.Regexp
	for _, pattern := range config.ErrorPatterns {
		if pattern != "" {
			re, err := regexp.Compile(pattern)
			if err != nil {
				return nil, err
			}
			compiledPatterns = append(compiledPatterns, re)
		}
	}

	return &Plugin{
		config:         config,
		store:          store,
		customPatterns: compiledPatterns,
	}, nil
}

// GetName returns the plugin name.
func (p *Plugin) GetName() string {
	return PluginName
}

// Cleanup frees resources.
func (p *Plugin) Cleanup() error {
	p.store.ResetAll()
	return nil
}

// GetStore returns the underlying Store.
func (p *Plugin) GetStore() Store {
	return p.store
}

// PreRequestHook is a no-op for this plugin.
func (p *Plugin) PreRequestHook(_ *schemas.BifrostContext, _ *schemas.BifrostRequest) error {
	return nil
}

// PreLLMHook is a no-op for this plugin (filtering happens in KeyPoolFilter).
func (p *Plugin) PreLLMHook(_ *schemas.BifrostContext, req *schemas.BifrostRequest) (*schemas.BifrostRequest, *schemas.LLMPluginShortCircuit, error) {
	return req, nil, nil
}

// PostLLMHook inspects errors after LLM execution and trips the circuit breaker on quota/rate-limits.
func (p *Plugin) PostLLMHook(ctx *schemas.BifrostContext, resp *schemas.BifrostResponse, bifrostErr *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError, error) {
	if !p.config.Enabled || bifrostErr == nil {
		return resp, bifrostErr, nil
	}

	// Check if this error is a rate limit or quota exceeded error
	if !IsRateLimitOrQuotaExceeded(bifrostErr, p.customPatterns) {
		return resp, bifrostErr, nil
	}

	// Extract key ID from context
	keyID := bifrost.GetStringFromContext(ctx, schemas.BifrostContextKeySelectedKeyID)
	if keyID == "" {
		return resp, bifrostErr, nil
	}

	provider := bifrostErr.ExtraFields.Provider
	model := bifrostErr.ExtraFields.ResolvedModelUsed
	if model == "" {
		model = bifrostErr.ExtraFields.OriginalModelRequested
	}

	now := time.Now()
	cooldown := p.resolveCooldown(keyID, provider, model, bifrostErr, now)
	resetTime := now.Add(cooldown)

	statusCode := 0
	if bifrostErr.StatusCode != nil {
		statusCode = *bifrostErr.StatusCode
	}

	p.store.Trip(keyID, provider, model, resetTime, bifrostErr.GetErrorString(), statusCode)

	return resp, bifrostErr, nil
}

// resolveCooldown determines the duration using the hierarchy:
// 1. Dynamic retry-after/reset time from error
// 2. Per-key/model static config
// 3. Global default cooldown (5h)
func (p *Plugin) resolveCooldown(keyID string, provider schemas.ModelProvider, model string, bifrostErr *schemas.BifrostError, now time.Time) time.Duration {
	// 1. Dynamic check from error
	if dyn, ok := ParseCooldownDuration(bifrostErr, now); ok && dyn > 0 {
		return dyn
	}

	// 2. Per-key static config check
	for _, kc := range p.config.KeyConfigs {
		if (kc.KeyID == "" || kc.KeyID == keyID) &&
			(kc.Provider == "" || kc.Provider == provider) &&
			(kc.Model == "" || kc.Model == model) {
			if kc.Cooldown.D() > 0 {
				return kc.Cooldown.D()
			}
		}
	}

	// 3. Fallback to default config
	return p.config.DefaultCooldown.D()
}

// SyncKeyQuota actively probes the provider for the given key and updates circuit breaker state.
func (p *Plugin) SyncKeyQuota(ctx *schemas.BifrostContext, prov schemas.Provider, key schemas.Key) (*schemas.KeyQuotaSummary, error) {
	quotaProvider, ok := prov.(schemas.QuotaInfoProvider)
	if !ok {
		return nil, fmt.Errorf("provider %s does not support active quota info probing", prov.GetProviderKey())
	}

	summary, bifrostErr := quotaProvider.GetKeyQuotaSummary(ctx, key)
	if bifrostErr != nil {
		return nil, fmt.Errorf("failed to fetch quota summary from provider: %s", bifrostErr.GetErrorString())
	}

	return summary, nil
}

// SyncModelQuota actively probes per-model quota and automatically trips/un-trips the breaker store.
func (p *Plugin) SyncModelQuota(ctx *schemas.BifrostContext, prov schemas.Provider, key schemas.Key) (map[string]schemas.ModelQuotaInfo, error) {
	quotaProvider, ok := prov.(schemas.QuotaInfoProvider)
	if !ok {
		return nil, fmt.Errorf("provider %s does not support active quota info probing", prov.GetProviderKey())
	}

	modelsQuota, bifrostErr := quotaProvider.GetModelsQuota(ctx, key)
	if bifrostErr != nil {
		return nil, fmt.Errorf("failed to fetch models quota from provider: %s", bifrostErr.GetErrorString())
	}

	providerKey := prov.GetProviderKey()
	now := time.Now()

	for modelID, info := range modelsQuota {
		if info.IsLimited || info.RemainingFraction <= 0 {
			resetTime := now.Add(p.config.DefaultCooldown.D())
			if !info.ResetTime.IsZero() {
				resetTime = info.ResetTime
			} else if info.ResetAfter > 0 {
				resetTime = now.Add(info.ResetAfter)
			}
			p.store.Trip(key.ID, providerKey, modelID, resetTime, "Active quota probe: limit reached", 429)
		} else if info.RemainingFraction > 0 {
			// If quota is healthy, clear any active trip on this model
			p.store.Reset(key.ID, providerKey, modelID)
		}
	}

	return modelsQuota, nil
}

// KeyPoolFilter returns a KeyPoolFilter function that filters out tripped keys for the requested model.
func (p *Plugin) KeyPoolFilter() schemas.KeyPoolFilter {
	return func(ctx *schemas.BifrostContext, provider schemas.ModelProvider, model string, keys []schemas.Key) ([]schemas.Key, error) {
		if !p.config.Enabled || len(keys) == 0 {
			return keys, nil
		}

		now := time.Now()
		eligible := make([]schemas.Key, 0, len(keys))
		for _, k := range keys {
			if k.Enabled != nil && !*k.Enabled {
				continue
			}
			if !p.store.IsTripped(k.ID, provider, model, now) {
				eligible = append(eligible, k)
			}
		}

		return eligible, nil
	}
}
