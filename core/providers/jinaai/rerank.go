package jinaai

import (
	"strings"

	"github.com/bytedance/sonic"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
)

// ToJinaRerankRequest converts a Bifrost rerank request to Jina AI format.
func ToJinaRerankRequest(bifrostReq *schemas.BifrostRerankRequest) *JinaRerankRequest {
	if bifrostReq == nil {
		return nil
	}

	model := strings.TrimPrefix(bifrostReq.Model, "jina-ai/")

	jinaReq := &JinaRerankRequest{
		Model:       model,
		Query:       bifrostReq.Query,
		ExtraParams: make(map[string]interface{}),
	}

	documents := make([]interface{}, len(bifrostReq.Documents))
	for i, doc := range bifrostReq.Documents {
		if doc.Text != "" || len(doc.Data) == 0 {
			documents[i] = doc.Text
		} else {
			// For structured or multimodal document items
			if encoded, err := sonic.Marshal(doc.Data); err == nil {
				documents[i] = string(encoded)
			} else {
				documents[i] = doc.Text
			}
		}
	}
	jinaReq.Documents = documents

	if bifrostReq.Params != nil {
		jinaReq.TopN = bifrostReq.Params.TopN
		jinaReq.ReturnDocuments = bifrostReq.Params.ReturnDocuments
		if bifrostReq.Params.ExtraParams != nil {
			for k, v := range bifrostReq.Params.ExtraParams {
				jinaReq.ExtraParams[k] = v
			}
		}
	}

	return jinaReq
}

// ToBifrostRerankResponse converts a Jina AI rerank response to Bifrost format.
func (response *JinaRerankResponse) ToBifrostRerankResponse(origDocs []schemas.RerankDocument, returnDocs bool, requestModel string) *schemas.BifrostRerankResponse {
	if response == nil {
		return nil
	}

	model := response.Model
	if model == "" {
		model = requestModel
	}

	bifrostResp := &schemas.BifrostRerankResponse{
		Model:   model,
		Results: make([]schemas.RerankResult, 0, len(response.Results)),
	}

	for _, res := range response.Results {
		result := schemas.RerankResult{
			Index:          res.Index,
			RelevanceScore: res.RelevanceScore,
		}
		if res.Index >= 0 && res.Index < len(origDocs) {
			result.ID = origDocs[res.Index].ID
		}
		if returnDocs {
			if res.Document != nil && res.Document.Text != "" {
				result.Document = &schemas.RerankDocument{
					Text: res.Document.Text,
				}
				if res.Index >= 0 && res.Index < len(origDocs) {
					result.Document.ID = origDocs[res.Index].ID
					result.Document.Meta = origDocs[res.Index].Meta
				}
			} else if res.Index >= 0 && res.Index < len(origDocs) {
				result.Document = schemas.Ptr(origDocs[res.Index])
			}
		}
		bifrostResp.Results = append(bifrostResp.Results, result)
	}

	if response.Usage.TotalTokens > 0 || response.Usage.PromptTokens > 0 {
		bifrostResp.Usage = &schemas.BifrostLLMUsage{
			TotalTokens:  response.Usage.TotalTokens,
			PromptTokens: response.Usage.PromptTokens,
		}
	}

	return bifrostResp
}

// Rerank performs a rerank request to Jina AI's /v1/rerank endpoint.
func (provider *Provider) Rerank(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostRerankRequest) (*schemas.BifrostRerankResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.JinaAI, provider.customProviderConfig, schemas.RerankRequest); err != nil {
		return nil, err
	}

	jsonBody, bifrostErr := providerUtils.CheckContextAndGetRequestBody(
		ctx,
		request,
		func() (providerUtils.RequestBodyWithExtraParams, error) {
			return ToJinaRerankRequest(request), nil
		})
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	url := provider.networkConfig.BaseURL + providerUtils.GetPathFromContext(ctx, provider.rerankPath)
	responseBody, latency, providerResponseHeaders, err := provider.completeRequest(ctx, jsonBody, url, key.Value.GetValue())
	if providerResponseHeaders != nil {
		ctx.SetValue(schemas.BifrostContextKeyProviderResponseHeaders, providerResponseHeaders)
	}
	if err != nil {
		return nil, providerUtils.EnrichError(ctx, err, jsonBody, nil, provider.sendBackRawRequest, provider.sendBackRawResponse, latency)
	}

	if isLargeResp, _ := ctx.Value(schemas.BifrostContextKeyLargeResponseMode).(bool); isLargeResp {
		return &schemas.BifrostRerankResponse{
			Model: request.Model,
			ExtraFields: schemas.BifrostResponseExtraFields{
				Latency:                 latency.Milliseconds(),
				ProviderResponseHeaders: providerResponseHeaders,
			},
		}, nil
	}

	var jinaResponse JinaRerankResponse
	rawRequest, rawResponse, bifrostErr := providerUtils.HandleProviderResponseCtx(ctx, responseBody, &jinaResponse, jsonBody, providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest), providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse))
	if bifrostErr != nil {
		return nil, providerUtils.EnrichError(ctx, bifrostErr, jsonBody, responseBody, provider.sendBackRawRequest, provider.sendBackRawResponse, latency)
	}

	returnDocs := request.Params != nil && request.Params.ReturnDocuments != nil && *request.Params.ReturnDocuments
	bifrostResponse := jinaResponse.ToBifrostRerankResponse(request.Documents, returnDocs, request.Model)

	bifrostResponse.ExtraFields.Latency = latency.Milliseconds()
	bifrostResponse.ExtraFields.ProviderResponseHeaders = providerResponseHeaders
	if providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest) {
		bifrostResponse.ExtraFields.RawRequest = rawRequest
	}
	if providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse) {
		bifrostResponse.ExtraFields.RawResponse = rawResponse
	}

	return bifrostResponse, nil
}
