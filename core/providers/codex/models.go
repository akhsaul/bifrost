package codex

import (
	"net/http"
	"strings"

	"github.com/bytedance/sonic"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

// ListModels performs a list models request to the Codex API.
//
// The catalog is custom-shaped (slug + available_in_plans), so it is fetched
// directly rather than through the shared OpenAI list-models path. Every
// model is filtered by the resolved account's plan_type against its
// available_in_plans — only models the plan can serve are returned. The plan
// filter runs on every ListModels call, including unfiltered ones.
func (provider *CodexProvider) ListModels(ctx *schemas.BifrostContext, keys []schemas.Key, request *schemas.BifrostListModelsRequest) (*schemas.BifrostListModelsResponse, *schemas.BifrostError) {
	if len(keys) == 0 {
		return nil, configurationError("codex: no keys configured")
	}

	return providerUtils.HandleMultipleListModelsRequests(
		ctx,
		keys,
		request,
		provider.listModelsByKey,
	)
}

func (provider *CodexProvider) listModelsByKey(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostListModelsRequest) (*schemas.BifrostListModelsResponse, *schemas.BifrostError) {
	unfiltered := request != nil && request.Unfiltered

	creds, isOAuth, bErr := provider.resolveAuth(ctx, key)
	if bErr != nil {
		return nil, bErr
	}

	resp, err := provider.fetchModels(ctx, creds, key, unfiltered)
	if err != nil && isOAuth && isUnauthorized(err) {
		// The cached access token died (or was never valid). Drop it and retry
		// once with a freshly minted one before surfacing the 401.
		invalidateCredentials(&key)
		if creds, _, bErr = provider.resolveAuth(ctx, key); bErr != nil {
			return nil, bErr
		}
		resp, err = provider.fetchModels(ctx, creds, key, unfiltered)
	}
	return resp, err
}

// fetchModels performs one GET /models and converts the catalog, applying
// the account's plan filter first and the key's allowlist/blacklist/alias
// pipeline second (only when not unfiltered).
func (provider *CodexProvider) fetchModels(ctx *schemas.BifrostContext, creds *codexCredentials, key schemas.Key, unfiltered bool) (*schemas.BifrostListModelsResponse, *schemas.BifrostError) {
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	providerUtils.SetExtraHeaders(ctx, req, BuildHeaders(ctx, provider.networkConfig.ExtraHeaders), nil)
	for k, v := range authHeadersFor(creds) {
		req.Header.Set(k, v)
	}
	req.SetRequestURI(provider.networkConfig.BaseURL + providerUtils.GetPathFromContext(ctx, provider.modelsPath) + "?client_version=" + DefaultClientVersion)
	req.Header.SetMethod(http.MethodGet)
	req.Header.SetContentType("application/json")

	latency, bifrostErr, wait := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
	defer wait()
	if bifrostErr != nil {
		return nil, bifrostErr
	}
	ctx.SetValue(schemas.BifrostContextKeyProviderResponseHeaders, providerUtils.ExtractProviderResponseHeaders(resp))

	if resp.StatusCode() != fasthttp.StatusOK {
		return nil, providerUtils.SetErrorLatency(parseCodexError(resp), latency)
	}

	responseBody := append([]byte(nil), resp.Body()...)
	var catalog CodexModelsResponse
	if err := sonic.Unmarshal(responseBody, &catalog); err != nil {
		return nil, providerUtils.SetErrorLatency(
			providerUtils.NewProviderAPIError("codex: could not parse the models response", err, resp.StatusCode(), nil, nil), latency)
	}

	response := &schemas.BifrostListModelsResponse{
		Data: make([]schemas.Model, 0, len(catalog.Models)),
	}

	pipeline := &providerUtils.ListModelsPipeline{
		AllowedModels:     key.Models,
		BlacklistedModels: key.BlacklistedModels,
		Aliases:           key.Aliases,
		Unfiltered:        unfiltered,
		ProviderKey:       schemas.Codex,
		MatchFns:          providerUtils.DefaultMatchFns(),
	}
	if !unfiltered && pipeline.ShouldEarlyExit() {
		return response, nil
	}

	included := make(map[string]bool)
	for _, model := range catalog.Models {
		id := strings.TrimSpace(model.Slug)
		if id == "" {
			continue
		}
		// The plan filter runs on every call: strict — only models whose
		// available_in_plans contains the resolved account's plan_type pass.
		if !planAllowed(creds.PlanType, model.AvailableInPlans) {
			continue
		}
		for _, result := range pipeline.FilterModel(id) {
			entry := schemas.Model{
				ID:   string(schemas.Codex) + "/" + result.ResolvedID,
				Name: schemas.Ptr(displayName(model, id)),
			}
			if model.Description != "" {
				entry.Description = schemas.Ptr(model.Description)
			}
			if contextLength := contextWindow(model); contextLength > 0 {
				entry.ContextLength = schemas.Ptr(contextLength)
			}
			entry.OwnedBy = schemas.Ptr("codex")
			if result.AliasValue != "" {
				entry.Alias = schemas.Ptr(result.AliasValue)
			}
			response.Data = append(response.Data, entry)
			included[strings.ToLower(result.ResolvedID)] = true
		}
	}

	// No backfill: it could re-add a model the plan filter removed but the
	// key's allowlist names, which the strict plan rule forbids.
	_ = included

	response.ExtraFields.Latency = latency.Milliseconds()
	if providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse) {
		response.ExtraFields.RawResponse = string(responseBody)
	}

	return response, nil
}

// displayName prefers the catalog's display name, falling back to the slug.
func displayName(model CodexModel, slug string) string {
	if strings.TrimSpace(model.DisplayName) != "" {
		return model.DisplayName
	}
	return slug
}

// contextWindow prefers the max window (the real usable window) over the
// pricing-tier window, mirroring OmniRoute's discovery merge.
func contextWindow(model CodexModel) int {
	if model.MaxContextWindow > 0 {
		return model.MaxContextWindow
	}
	return model.ContextWindow
}
