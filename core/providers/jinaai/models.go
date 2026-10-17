package jinaai

import (
	"net/http"
	"strings"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

// ToBifrostListModelsResponse converts Jina AI's models response to Bifrost format,
// filtering allowed/blacklisted models, resolving aliases, and annotating supported methods.
func (response *JinaModelsResponse) ToBifrostListModelsResponse(providerKey schemas.ModelProvider, allowedModels schemas.WhiteList, blacklistedModels schemas.BlackList, aliases schemas.KeyAliases, unfiltered bool) *schemas.BifrostListModelsResponse {
	if response == nil {
		return nil
	}

	bifrostResponse := &schemas.BifrostListModelsResponse{
		Data: make([]schemas.Model, 0, len(response.Data)),
	}

	pipeline := &providerUtils.ListModelsPipeline{
		AllowedModels:     allowedModels,
		BlacklistedModels: blacklistedModels,
		Aliases:           aliases,
		Unfiltered:        unfiltered,
		ProviderKey:       providerKey,
		MatchFns:          providerUtils.DefaultMatchFns(),
	}
	if pipeline.ShouldEarlyExit() {
		return bifrostResponse
	}

	included := make(map[string]bool)

	for _, model := range response.Data {
		cleanID := strings.TrimPrefix(model.ID, "jina-ai/")

		var supportedMethods []string
		isEmbedding := false
		for _, mod := range model.OutputModalities {
			if strings.EqualFold(mod, "embeddings") {
				isEmbedding = true
				break
			}
		}
		cleanLower := strings.ToLower(cleanID)
		if isEmbedding || strings.Contains(cleanLower, "embedding") || strings.Contains(cleanLower, "clip") || strings.Contains(cleanLower, "colbert") {
			supportedMethods = append(supportedMethods, string(schemas.EmbeddingRequest))
		}
		if strings.Contains(cleanLower, "rerank") || strings.Contains(strings.ToLower(model.Name), "rerank") {
			supportedMethods = append(supportedMethods, string(schemas.RerankRequest))
		}

		for _, result := range pipeline.FilterModel(cleanID) {
			resolvedKey := strings.ToLower(result.ResolvedID)
			if included[resolvedKey] {
				continue
			}
			entry := schemas.Model{
				ID:               string(providerKey) + "/" + result.ResolvedID,
				Name:             schemas.Ptr(model.Name),
				ContextLength:    schemas.Ptr(model.ContextLength),
				SupportedMethods: supportedMethods,
			}
			if result.AliasValue != "" {
				entry.Alias = schemas.Ptr(result.AliasValue)
			}
			bifrostResponse.Data = append(bifrostResponse.Data, entry)
			included[resolvedKey] = true
		}
	}

	bifrostResponse.Data = append(bifrostResponse.Data, pipeline.BackfillModels(included)...)

	return bifrostResponse
}

// listModelsByKey performs a list models request for a single key against Jina AI's /v1/models.
func (provider *Provider) listModelsByKey(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostListModelsRequest) (*schemas.BifrostListModelsResponse, *schemas.BifrostError) {
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	providerUtils.SetExtraHeaders(ctx, req, provider.networkConfig.ExtraHeaders, nil)

	req.SetRequestURI(provider.networkConfig.BaseURL + providerUtils.GetPathFromContext(ctx, provider.modelsPath))
	req.Header.SetMethod(http.MethodGet)
	req.Header.SetContentType("application/json")

	if key.Value.GetValue() != "" {
		req.Header.Set("Authorization", "Bearer "+key.Value.GetValue())
	}

	latency, bifrostErr, wait := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
	defer wait()
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	ctx.SetValue(schemas.BifrostContextKeyProviderResponseHeaders, providerUtils.ExtractProviderResponseHeaders(resp))
	if resp.StatusCode() != fasthttp.StatusOK {
		return nil, providerUtils.SetErrorLatency(parseJinaError(resp), latency)
	}

	var jinaResponse JinaModelsResponse
	rawRequest, rawResponse, bifrostErr := providerUtils.HandleProviderResponseCtx(
		ctx,
		resp.Body(),
		&jinaResponse,
		nil,
		providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest),
		providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse),
	)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	response := jinaResponse.ToBifrostListModelsResponse(provider.GetProviderKey(), key.Models, key.BlacklistedModels, key.Aliases, request.Unfiltered)

	response.ExtraFields.Latency = latency.Milliseconds()
	response.ExtraFields.ProviderResponseHeaders = providerUtils.ExtractProviderResponseHeaders(resp)

	if providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest) {
		response.ExtraFields.RawRequest = rawRequest
	}
	if providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse) {
		response.ExtraFields.RawResponse = rawResponse
	}

	return response, nil
}

// ListModels performs a list models request to Jina AI's API.
func (provider *Provider) ListModels(ctx *schemas.BifrostContext, keys []schemas.Key, request *schemas.BifrostListModelsRequest) (*schemas.BifrostListModelsResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.JinaAI, provider.customProviderConfig, schemas.ListModelsRequest); err != nil {
		return nil, err
	}
	return providerUtils.HandleMultipleListModelsRequests(
		ctx,
		keys,
		request,
		provider.listModelsByKey,
	)
}
