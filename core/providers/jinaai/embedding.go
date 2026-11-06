package jinaai

import (
	"strings"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
)

// ToJinaEmbeddingRequest converts a Bifrost embedding request to Jina AI format.
func ToJinaEmbeddingRequest(bifrostReq *schemas.BifrostEmbeddingRequest) *JinaEmbeddingRequest {
	if bifrostReq == nil {
		return nil
	}

	model := strings.TrimPrefix(bifrostReq.Model, "jina-ai/")

	jinaReq := &JinaEmbeddingRequest{
		Model:       model,
		ExtraParams: make(map[string]interface{}),
	}

	// Set input
	if len(bifrostReq.Input) > 0 {
		var hasNonText bool
		for _, item := range bifrostReq.Input {
			for _, part := range item.Content {
				if part.Type != schemas.EmbeddingContentPartTypeText {
					hasNonText = true
					break
				}
			}
			if hasNonText {
				break
			}
		}

		if hasNonText {
			objects := make([]map[string]interface{}, 0, len(bifrostReq.Input))
			for _, item := range bifrostReq.Input {
				for _, part := range item.Content {
					obj := make(map[string]interface{})
					if part.Text != nil {
						obj["text"] = *part.Text
					}
					if part.Image != nil {
						if part.Image.URL != nil {
							obj["image"] = *part.Image.URL
						} else if part.Image.Data != nil {
							obj["image"] = *part.Image.Data
						}
					}
					if len(obj) > 0 {
						objects = append(objects, obj)
					}
				}
			}
			jinaReq.Input = objects
		} else {
			texts := schemas.EmbeddingInput(bifrostReq.Input).Texts()
			if len(texts) == 1 {
				jinaReq.Input = texts[0]
			} else {
				jinaReq.Input = texts
			}
		}
	}

	// Set parameters
	if bifrostReq.Params != nil {
		jinaReq.Dimensions = bifrostReq.Params.Dimensions
		jinaReq.EmbeddingType = bifrostReq.Params.EncodingFormat

		// Extract extra params known to Jina
		if bifrostReq.Params.ExtraParams != nil {
			for k, v := range bifrostReq.Params.ExtraParams {
				jinaReq.ExtraParams[k] = v
			}

			if task, ok := schemas.SafeExtractString(bifrostReq.Params.ExtraParams["task"]); ok {
				delete(jinaReq.ExtraParams, "task")
				jinaReq.Task = &task
			}
			if normalized, ok := bifrostReq.Params.ExtraParams["normalized"].(bool); ok {
				delete(jinaReq.ExtraParams, "normalized")
				jinaReq.Normalized = &normalized
			}
			if lateChunking, ok := bifrostReq.Params.ExtraParams["late_chunking"].(bool); ok {
				delete(jinaReq.ExtraParams, "late_chunking")
				jinaReq.LateChunking = &lateChunking
			}
			if embeddingType, ok := schemas.SafeExtractString(bifrostReq.Params.ExtraParams["embedding_type"]); ok {
				delete(jinaReq.ExtraParams, "embedding_type")
				jinaReq.EmbeddingType = &embeddingType
			}
		}
	}

	return jinaReq
}

// ToBifrostEmbeddingResponse converts Jina AI embedding response to Bifrost format.
func (response *JinaEmbeddingResponse) ToBifrostEmbeddingResponse(requestModel string) *schemas.BifrostEmbeddingResponse {
	if response == nil {
		return nil
	}

	model := response.Model
	if model == "" {
		model = requestModel
	}

	bifrostResp := &schemas.BifrostEmbeddingResponse{
		Model:  model,
		Object: response.Object,
		Data:   make([]schemas.EmbeddingData, 0, len(response.Data)),
	}
	if bifrostResp.Object == "" {
		bifrostResp.Object = "list"
	}

	for _, item := range response.Data {
		var embStruct schemas.EmbeddingStruct
		if len(item.Embedding) > 0 {
			_ = embStruct.UnmarshalJSON(item.Embedding)
		}
		bifrostResp.Data = append(bifrostResp.Data, schemas.EmbeddingData{
			Index:     item.Index,
			Object:    item.Object,
			Embedding: embStruct,
		})
	}

	// Backfill prompt_tokens if missing but total_tokens is present (embeddings specific requirement)
	promptTokens := response.Usage.PromptTokens
	totalTokens := response.Usage.TotalTokens
	if promptTokens == 0 && totalTokens > 0 {
		promptTokens = totalTokens
	}

	bifrostResp.Usage = &schemas.BifrostLLMUsage{
		PromptTokens: promptTokens,
		TotalTokens:  totalTokens,
	}

	return bifrostResp
}

// Embedding performs an embedding request to Jina AI's /v1/embeddings endpoint.
func (provider *Provider) Embedding(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostEmbeddingRequest) (*schemas.BifrostEmbeddingResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.JinaAI, provider.customProviderConfig, schemas.EmbeddingRequest); err != nil {
		return nil, err
	}

	jsonBody, bifrostErr := providerUtils.CheckContextAndGetRequestBody(
		ctx,
		request,
		func() (providerUtils.RequestBodyWithExtraParams, error) {
			return ToJinaEmbeddingRequest(request), nil
		})
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	url := provider.networkConfig.BaseURL + providerUtils.GetPathFromContext(ctx, provider.embeddingsPath)
	responseBody, latency, providerResponseHeaders, err := provider.completeRequest(ctx, jsonBody, url, key.Value.GetValue())
	if providerResponseHeaders != nil {
		ctx.SetValue(schemas.BifrostContextKeyProviderResponseHeaders, providerResponseHeaders)
	}
	if err != nil {
		return nil, providerUtils.EnrichError(ctx, err, jsonBody, nil, provider.sendBackRawRequest, provider.sendBackRawResponse, latency)
	}

	if isLargeResp, _ := ctx.Value(schemas.BifrostContextKeyLargeResponseMode).(bool); isLargeResp {
		return &schemas.BifrostEmbeddingResponse{
			Model: request.Model,
			ExtraFields: schemas.BifrostResponseExtraFields{
				Latency:                 latency.Milliseconds(),
				ProviderResponseHeaders: providerResponseHeaders,
			},
		}, nil
	}

	var jinaResponse JinaEmbeddingResponse
	rawRequest, rawResponse, bifrostErr := providerUtils.HandleProviderResponseCtx(ctx, responseBody, &jinaResponse, jsonBody, providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest), providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse))
	if bifrostErr != nil {
		return nil, providerUtils.EnrichError(ctx, bifrostErr, jsonBody, responseBody, provider.sendBackRawRequest, provider.sendBackRawResponse, latency)
	}

	bifrostResponse := jinaResponse.ToBifrostEmbeddingResponse(request.Model)

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
