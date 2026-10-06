package zed

import (
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/bytedance/sonic"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

const (
	// modelsCacheTTL is how long one GET /models result is reused. The
	// catalog is also the routing table (model id -> inner family), so a
	// miss on a model unknown to the cache triggers one immediate refresh
	// before erroring.
	modelsCacheTTL = 5 * time.Minute
)

// zedModelsCache is one cached /models catalog for a key.
type zedModelsCache struct {
	entries   []ZedModelEntry
	byID      map[string]ZedModelEntry
	fetchedAt time.Time
}

// zedModelsEntry is one cache slot behind a single refresh lock.
type zedModelsEntry struct {
	mu    sync.Mutex
	cache *zedModelsCache
}

// zedModelsPool maps key cache key to *zedModelsEntry. Keyed by the same
// credential hash as the token pool: one login sees one catalog.
var zedModelsPool sync.Map

func loadOrCreateModelsEntry(cacheKey string) *zedModelsEntry {
	if existing, ok := zedModelsPool.Load(cacheKey); ok {
		return existing.(*zedModelsEntry)
	}
	actual, _ := zedModelsPool.LoadOrStore(cacheKey, &zedModelsEntry{})
	return actual.(*zedModelsEntry)
}

// resolveInnerProvider maps a Bifrost model id to Zed's envelope provider
// discriminator using the live /models catalog. Unknown models trigger one
// refresh before erroring so newly released Zed models work without waiting
// for the TTL.
func (provider *ZedProvider) resolveInnerProvider(ctx *schemas.BifrostContext, key schemas.Key, model string) (string, *schemas.BifrostError) {
	entry, bErr := provider.getModelsCatalog(ctx, key, false)
	if bErr != nil {
		return "", bErr
	}
	if found, ok := entry.cache.byID[strings.ToLower(strings.TrimSpace(model))]; ok {
		return found.Provider, nil
	}
	// One refresh for models the cache does not know yet.
	entry, bErr = provider.getModelsCatalog(ctx, key, true)
	if bErr != nil {
		return "", bErr
	}
	if found, ok := entry.cache.byID[strings.ToLower(strings.TrimSpace(model))]; ok {
		return found.Provider, nil
	}
	return "", providerUtils.NewProviderAPIError(
		"zed: model \""+model+"\" is not in Zed's /models catalog for this key", nil, 0, nil, nil)
}

// getModelsCatalog returns the cached catalog, refreshing on expiry or when
// forceRefresh is set.
func (provider *ZedProvider) getModelsCatalog(ctx *schemas.BifrostContext, key schemas.Key, forceRefresh bool) (*zedModelsEntry, *schemas.BifrostError) {
	entry := loadOrCreateModelsEntry(zedCacheKey(key))
	if !forceRefresh {
		if cache := entry.cache; cache != nil && time.Since(cache.fetchedAt) < modelsCacheTTL {
			return entry, nil
		}
	}
	entry.mu.Lock()
	defer entry.mu.Unlock()
	if !forceRefresh {
		if cache := entry.cache; cache != nil && time.Since(cache.fetchedAt) < modelsCacheTTL {
			return entry, nil
		}
	}
	catalog, bErr := provider.fetchModelsCatalog(ctx, key)
	if bErr != nil {
		// Serve stale on transient failures so one bad /models call does not
		// take down inference for every model on the key.
		if entry.cache != nil && !isPermanentError(bErr) {
			return entry, nil
		}
		return nil, bErr
	}
	entry.cache = catalog
	return entry, nil
}

// fetchModelsCatalog performs one GET /models and indexes it by model id.
func (provider *ZedProvider) fetchModelsCatalog(ctx *schemas.BifrostContext, key schemas.Key) (*zedModelsCache, *schemas.BifrostError) {
	authValue, _, bErr := provider.resolveCredentials(ctx, key)
	if bErr != nil {
		return nil, bErr
	}

	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	req.SetRequestURI(provider.networkConfig.BaseURL + provider.modelsPath)
	req.Header.SetMethod(http.MethodGet)
	req.Header.Set("Accept", "application/json")
	for k, v := range modelsHeaders(ctx, provider.networkConfig.ExtraHeaders, strings.TrimPrefix(authValue, "Bearer ")) {
		req.Header.Set(k, v)
	}
	providerUtils.SetExtraHeaders(ctx, req, provider.networkConfig.ExtraHeaders, nil)

	latency, bifrostErr, wait := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
	defer wait()
	if bifrostErr != nil {
		return nil, bifrostErr
	}
	providerResponseHeaders := providerUtils.ExtractProviderResponseHeaders(resp)
	if providerResponseHeaders != nil {
		ctx.SetValue(schemas.BifrostContextKeyProviderResponseHeaders, providerResponseHeaders)
	}
	if resp.StatusCode() == http.StatusUnauthorized {
		invalidateCredentials(&key)
		if authValue, _, bErr = provider.resolveCredentials(ctx, key); bErr != nil {
			return nil, bErr
		}
		return provider.fetchModelsCatalogWithAuth(ctx, key, authValue)
	}
	if resp.StatusCode() != http.StatusOK {
		return nil, providerUtils.SetErrorLatency(parseZedError(resp), latency)
	}
	body := append([]byte(nil), resp.Body()...)
	var parsed ZedModelsResponse
	if err := sonic.Unmarshal(body, &parsed); err != nil {
		bErr := providerUtils.NewProviderAPIError("zed: could not parse the /models response", err, resp.StatusCode(), nil, nil)
		return nil, providerUtils.SetErrorLatency(bErr, latency)
	}
	byID := make(map[string]ZedModelEntry, len(parsed.Models))
	for _, m := range parsed.Models {
		id := strings.TrimSpace(m.ID)
		if id == "" {
			continue
		}
		byID[strings.ToLower(id)] = m
	}
	return &zedModelsCache{entries: parsed.Models, byID: byID, fetchedAt: time.Now()}, nil
}

// fetchModelsCatalogWithAuth retries one GET /models with a fresh token.
func (provider *ZedProvider) fetchModelsCatalogWithAuth(ctx *schemas.BifrostContext, key schemas.Key, authValue string) (*zedModelsCache, *schemas.BifrostError) {
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	req.SetRequestURI(provider.networkConfig.BaseURL + provider.modelsPath)
	req.Header.SetMethod(http.MethodGet)
	req.Header.Set("Accept", "application/json")
	for k, v := range modelsHeaders(ctx, provider.networkConfig.ExtraHeaders, strings.TrimPrefix(authValue, "Bearer ")) {
		req.Header.Set(k, v)
	}
	providerUtils.SetExtraHeaders(ctx, req, provider.networkConfig.ExtraHeaders, nil)

	latency, bifrostErr, wait := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
	defer wait()
	if bifrostErr != nil {
		return nil, bifrostErr
	}
	if resp.StatusCode() != http.StatusOK {
		return nil, providerUtils.SetErrorLatency(parseZedError(resp), latency)
	}
	body := append([]byte(nil), resp.Body()...)
	var parsed ZedModelsResponse
	if err := sonic.Unmarshal(body, &parsed); err != nil {
		bErr := providerUtils.NewProviderAPIError("zed: could not parse the /models response", err, resp.StatusCode(), nil, nil)
		return nil, providerUtils.SetErrorLatency(bErr, latency)
	}
	byID := make(map[string]ZedModelEntry, len(parsed.Models))
	for _, m := range parsed.Models {
		id := strings.TrimSpace(m.ID)
		if id == "" {
			continue
		}
		byID[strings.ToLower(id)] = m
	}
	return &zedModelsCache{entries: parsed.Models, byID: byID, fetchedAt: time.Now()}, nil
}

// ListModels lists the models Zed exposes for the given keys.
//
// Zed's /models needs a bearer LLM token, so this goes through the same
// credential chain as inference (mint + 401 invalidate-and-retry per key).
// Responses are merged across keys with dedupe by model ID.
func (provider *ZedProvider) ListModels(ctx *schemas.BifrostContext, keys []schemas.Key, request *schemas.BifrostListModelsRequest) (*schemas.BifrostListModelsResponse, *schemas.BifrostError) {
	if len(keys) == 0 {
		return nil, configurationError("zed: no keys configured")
	}
	return providerUtils.HandleMultipleListModelsRequests(ctx, keys, request, provider.listModelsByKey)
}

func (provider *ZedProvider) listModelsByKey(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostListModelsRequest) (*schemas.BifrostListModelsResponse, *schemas.BifrostError) {
	unfiltered := request != nil && request.Unfiltered

	entry, bErr := provider.getModelsCatalog(ctx, key, false)
	if bErr != nil {
		return nil, bErr
	}
	return modelsToBifrostResponse(entry.cache, key, unfiltered), nil
}

// modelsToBifrostResponse converts a cached Zed catalog through the shared
// allowlist/blacklist/alias pipeline. IDs are advertised with the serving
// provider prefix (e.g. "zed/claude-haiku-4-5"), matching every other
// provider's ListModels contract.
func modelsToBifrostResponse(cache *zedModelsCache, key schemas.Key, unfiltered bool) *schemas.BifrostListModelsResponse {
	pipeline := &providerUtils.ListModelsPipeline{
		AllowedModels:     key.Models,
		BlacklistedModels: key.BlacklistedModels,
		Aliases:           key.Aliases,
		Unfiltered:        unfiltered,
		ProviderKey:       schemas.Zed,
		MatchFns:          providerUtils.DefaultMatchFns(),
	}
	if pipeline.ShouldEarlyExit() {
		return &schemas.BifrostListModelsResponse{}
	}
	seen := make(map[string]bool)
	models := make([]schemas.Model, 0, len(cache.entries))
	for _, entry := range cache.entries {
		for _, result := range pipeline.FilterModel(entry.ID) {
			if seen[strings.ToLower(result.ResolvedID)] {
				continue
			}
			seen[strings.ToLower(result.ResolvedID)] = true
			model := schemas.Model{ID: string(schemas.Zed) + "/" + result.ResolvedID}
			if name := strings.TrimSpace(entry.DisplayName); name != "" {
				model.Name = schemas.Ptr(name)
			}
			if entry.MaxTokens != nil {
				model.ContextLength = entry.MaxTokens
			}
			if entry.MaxOutputToks != nil {
				model.MaxOutputTokens = entry.MaxOutputToks
			}
			if owner := ownerFromInnerProvider(entry.Provider); owner != "" {
				model.OwnedBy = schemas.Ptr(owner)
			}
			if result.AliasValue != "" {
				model.Alias = schemas.Ptr(result.AliasValue)
			}
			models = append(models, model)
		}
	}
	return &schemas.BifrostListModelsResponse{Data: models}
}

// ownerFromInnerProvider maps Zed's envelope discriminator to a display owner.
func ownerFromInnerProvider(inner string) string {
	switch inner {
	case ZedInnerAnthropic:
		return "anthropic"
	case ZedInnerGoogle:
		return "google"
	case ZedInnerOpenAI:
		return "openai"
	default:
		return ""
	}
}
