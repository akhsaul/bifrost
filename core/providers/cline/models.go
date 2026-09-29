package cline

import (
	"net/http"
	"strings"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/providers/openai"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

// ListModels performs a list models request to the Cline API.
//
// Cline splits its catalog across two endpoints: /v1/models (OpenAI shape)
// and /v1/ai/cline/recommended-models (custom shape grouped into recommended,
// free, clinePass and clineCloud buckets). Both are fetched with the same
// credentials and merged with dedupe by model ID.
//
// The recommended fetch is fail-soft — if it fails, the /v1/models result is still returned.
func (provider *ClineProvider) ListModels(ctx *schemas.BifrostContext, keys []schemas.Key, request *schemas.BifrostListModelsRequest) (*schemas.BifrostListModelsResponse, *schemas.BifrostError) {
	if len(keys) == 0 {
		return nil, configurationError("cline: no keys configured")
	}

	return providerUtils.HandleMultipleListModelsRequests(
		ctx,
		keys,
		request,
		provider.listModelsByKey,
	)
}

func (provider *ClineProvider) listModelsByKey(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostListModelsRequest) (*schemas.BifrostListModelsResponse, *schemas.BifrostError) {
	unfiltered := request != nil && request.Unfiltered

	authValue, isOAuth, bErr := provider.resolveAuth(ctx, key)
	if bErr != nil {
		return nil, bErr
	}

	baseResp, bErr := provider.listBaseModels(ctx, key, authValue, unfiltered)
	if bErr != nil && isOAuth && isUnauthorized(bErr) {
		invalidateCredentials(&key)
		if authValue, _, bErr = provider.resolveAuth(ctx, key); bErr != nil {
			return nil, bErr
		}
		baseResp, bErr = provider.listBaseModels(ctx, key, authValue, unfiltered)
	}
	if bErr != nil {
		return nil, bErr
	}

	recommended := provider.fetchRecommended(ctx, authValue)
	if recommended == nil {
		return baseResp, nil
	}
	return mergeRecommendedModels(baseResp, recommended, key, unfiltered), nil
}

// listBaseModels fetches /v1/models through the shared OpenAI list-models
// path. The auth suffix is carried in a synthetic key because ListModelsByKey
// builds its own "Bearer "+value header.
func (provider *ClineProvider) listBaseModels(ctx *schemas.BifrostContext, key schemas.Key, authValue string, unfiltered bool) (*schemas.BifrostListModelsResponse, *schemas.BifrostError) {
	authKey := key
	authKey.Value = *schemas.NewSecretVar(strings.TrimPrefix(authValue, "Bearer "))
	return openai.ListModelsByKey(
		ctx,
		provider.client,
		provider.networkConfig.BaseURL+providerUtils.GetPathFromContext(ctx, provider.modelsPath),
		authKey,
		unfiltered,
		BuildHeaders(ctx, provider.networkConfig.ExtraHeaders, nil),
		provider.GetProviderKey(),
		providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest),
		providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse),
	)
}

// fetchRecommended fetches the recommended-models catalog. It returns nil on
// any failure so the caller can fall back to the base catalog alone. The
// response carries no tokens, but the Authorization header that fetched it
// does — so only the status is logged, never the body.
func (provider *ClineProvider) fetchRecommended(ctx *schemas.BifrostContext, authValue string) *ClineRecommendedModelsResponse {
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	providerUtils.SetExtraHeaders(ctx, req, BuildHeaders(ctx, provider.networkConfig.ExtraHeaders, nil), nil)
	req.SetRequestURI(provider.networkConfig.BaseURL + providerUtils.GetPathFromContext(ctx, provider.recommendedPath))
	req.Header.SetMethod(http.MethodGet)
	req.Header.SetContentType("application/json")
	req.Header.Set("Authorization", authValue)

	_, bifrostErr, wait := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
	defer wait()
	if bifrostErr != nil {
		provider.warn("cline: recommended-models fetch failed, continuing with base catalog: %s", bifrostErr.GetErrorString())
		return nil
	}
	if resp.StatusCode() != fasthttp.StatusOK {
		provider.warn("cline: recommended-models returned status %d, continuing with base catalog", resp.StatusCode())
		return nil
	}

	responseBody := append([]byte(nil), resp.Body()...)
	var recommended ClineRecommendedModelsResponse
	if err := sonic.Unmarshal(responseBody, &recommended); err != nil {
		provider.warn("cline: could not parse recommended-models, continuing with base catalog: %v", err)
		return nil
	}
	return &recommended
}

// warn logs through the provider logger when one is configured. Providers are
// constructed with a nil logger in unit tests, so this must stay nil-safe.
func (provider *ClineProvider) warn(format string, args ...interface{}) {
	if provider.logger != nil {
		provider.logger.Warn(format, args...)
	}
}

// mergeRecommendedModels unions recommended entries into the base response,
// deduping by full model ID. Entries run through the same allowlist /
// blacklist / alias pipeline as the base catalog; no second backfill runs,
// the base conversion already did it.
func mergeRecommendedModels(base *schemas.BifrostListModelsResponse, recommended *ClineRecommendedModelsResponse, key schemas.Key, unfiltered bool) *schemas.BifrostListModelsResponse {
	if base == nil {
		base = &schemas.BifrostListModelsResponse{}
	}

	pipeline := &providerUtils.ListModelsPipeline{
		AllowedModels:     key.Models,
		BlacklistedModels: key.BlacklistedModels,
		Aliases:           key.Aliases,
		Unfiltered:        unfiltered,
		ProviderKey:       schemas.Cline,
		MatchFns:          providerUtils.DefaultMatchFns(),
	}
	if pipeline.ShouldEarlyExit() {
		return base
	}

	seen := make(map[string]bool, len(base.Data))
	for _, model := range base.Data {
		seen[strings.ToLower(model.ID)] = true
	}
	seenRaw := make(map[string]bool)

	for _, entry := range recommended.All() {
		id := strings.TrimSpace(entry.ID)
		if id == "" || seenRaw[strings.ToLower(id)] {
			continue
		}
		seenRaw[strings.ToLower(id)] = true
		for _, result := range pipeline.FilterModel(id) {
			fullID := string(schemas.Cline) + "/" + result.ResolvedID
			if seen[strings.ToLower(fullID)] {
				continue
			}
			seen[strings.ToLower(fullID)] = true
			model := schemas.Model{ID: fullID}
			if owner := ownerFromID(id); owner != "" {
				model.OwnedBy = schemas.Ptr(owner)
			}
			if result.AliasValue != "" {
				model.Alias = schemas.Ptr(result.AliasValue)
			}
			base.Data = append(base.Data, model)
		}
	}
	return base
}

// ownerFromID derives the owner from a slash-namespaced model ID
// (e.g. "stealth/pixel-canary" → "stealth").
func ownerFromID(id string) string {
	if i := strings.Index(id, "/"); i > 0 {
		return id[:i]
	}
	return ""
}
