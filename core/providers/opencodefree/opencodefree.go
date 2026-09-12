// Package opencodefree implements the isolated Opencode Free AI gateway provider.
// It connects anonymously to https://opencode.ai/zen/v1 without an API key,
// mandates attribution and stainless headers (with dynamic session affinity),
// and injects reasoning into the Responses API request body.
package opencodefree

import (
	"context"
	"strings"
	"time"

	"github.com/maximhq/bifrost/core/providers/openai"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

// opencodeFreeProvider implements the Provider interface for the anonymous Opencode Free tier.
type opencodeFreeProvider struct {
	providerKey         schemas.ModelProvider
	logger              schemas.Logger
	client              *fasthttp.Client
	streamingClient     *fasthttp.Client
	networkConfig       schemas.NetworkConfig
	sendBackRawRequest  bool
	sendBackRawResponse bool
}

// NewOpencodeFreeProvider creates a new Opencode Free provider instance.
func NewOpencodeFreeProvider(config *schemas.ProviderConfig, logger schemas.Logger) (*opencodeFreeProvider, error) {
	config.CheckAndSetDefaults()

	requestTimeout := time.Second * time.Duration(config.NetworkConfig.DefaultRequestTimeoutInSeconds)
	client := &fasthttp.Client{
		ReadTimeout:         requestTimeout,
		WriteTimeout:        requestTimeout,
		MaxConnsPerHost:     config.NetworkConfig.MaxConnsPerHost,
		MaxIdleConnDuration: time.Second * time.Duration(config.NetworkConfig.KeepAliveTimeoutInSeconds),
		MaxConnWaitTimeout:  requestTimeout,
		MaxConnDuration:     time.Second * time.Duration(schemas.DefaultMaxConnDurationInSeconds),
		ConnPoolStrategy:    fasthttp.FIFO,
	}

	client = providerUtils.ConfigureProxy(client, config.ProxyConfig, logger)
	client = providerUtils.ConfigureDialer(client, config.NetworkConfig.AllowPrivateNetwork)
	client = providerUtils.ConfigureTLS(client, config.NetworkConfig, logger)
	streamingClient := providerUtils.BuildStreamingClient(client)

	if config.NetworkConfig.BaseURL == "" {
		config.NetworkConfig.BaseURL = DefaultBaseURL
	}
	config.NetworkConfig.BaseURL = strings.TrimRight(config.NetworkConfig.BaseURL, "/")

	return &opencodeFreeProvider{
		providerKey:         schemas.OpencodeFree,
		logger:              logger,
		client:              client,
		streamingClient:     streamingClient,
		networkConfig:       config.NetworkConfig,
		sendBackRawRequest:  config.SendBackRawRequest,
		sendBackRawResponse: config.SendBackRawResponse,
	}, nil
}

// GetProviderKey returns the provider identifier.
func (p *opencodeFreeProvider) GetProviderKey() schemas.ModelProvider {
	return p.providerKey
}

// ensureResponsesReasoning guarantees that reasoning and store fields are populated on Responses API requests.
func ensureResponsesReasoning(request *schemas.BifrostResponsesRequest) {
	if request == nil {
		return
	}
	if request.Params == nil {
		request.Params = &schemas.ResponsesParameters{}
	}
	if request.Params.Reasoning == nil {
		request.Params.Reasoning = &schemas.ResponsesParametersReasoning{
			Effort:  schemas.Ptr("high"),
			Summary: schemas.Ptr("auto"),
		}
	} else {
		if request.Params.Reasoning.Effort == nil || *request.Params.Reasoning.Effort == "" {
			request.Params.Reasoning.Effort = schemas.Ptr("high")
		}
		if request.Params.Reasoning.Summary == nil || *request.Params.Reasoning.Summary == "" {
			request.Params.Reasoning.Summary = schemas.Ptr("auto")
		}
	}
	if request.Params.Store == nil {
		request.Params.Store = schemas.Ptr(false)
	}
}

// ListModels performs a list models request to the Opencode Free API.
func (p *opencodeFreeProvider) ListModels(ctx *schemas.BifrostContext, keys []schemas.Key, request *schemas.BifrostListModelsRequest) (*schemas.BifrostListModelsResponse, *schemas.BifrostError) {
	if len(keys) == 0 {
		return providerUtils.HandleKeylessListModelsRequest(p.providerKey, func() (*schemas.BifrostListModelsResponse, *schemas.BifrostError) {
			unfiltered := false
			if request != nil {
				unfiltered = request.Unfiltered
			}
			return openai.ListModelsByKey(
				ctx,
				p.client,
				p.networkConfig.BaseURL+providerUtils.GetPathFromContext(ctx, "/v1/models"),
				schemas.Key{Models: schemas.WhiteList{"*"}},
				unfiltered,
				BuildHeaders(ctx, p.networkConfig.ExtraHeaders, false),
				p.providerKey,
				providerUtils.ShouldSendBackRawRequest(ctx, p.sendBackRawRequest),
				providerUtils.ShouldSendBackRawResponse(ctx, p.sendBackRawResponse),
			)
		})
	}

	extraHeaders := BuildHeaders(ctx, p.networkConfig.ExtraHeaders, false)
	return openai.HandleOpenAIListModelsRequest(
		ctx,
		p.client,
		request,
		p.networkConfig.BaseURL+providerUtils.GetPathFromContext(ctx, "/v1/models"),
		keys,
		extraHeaders,
		p.providerKey,
		providerUtils.ShouldSendBackRawRequest(ctx, p.sendBackRawRequest),
		providerUtils.ShouldSendBackRawResponse(ctx, p.sendBackRawResponse),
	)
}

// TextCompletion is not supported by OpencodeFree.
func (p *opencodeFreeProvider) TextCompletion(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostTextCompletionRequest) (*schemas.BifrostTextCompletionResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.TextCompletionRequest, p.GetProviderKey())
}

// TextCompletionStream is not supported by OpencodeFree.
func (p *opencodeFreeProvider) TextCompletionStream(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, postHookSpanFinalizer func(context.Context), key schemas.Key, request *schemas.BifrostTextCompletionRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.TextCompletionStreamRequest, p.GetProviderKey())
}

// wrapResponsesToChatStreamPostHookRunner adapts responses stream events to chat responses for post-hooks and client chunks.
func wrapResponsesToChatStreamPostHookRunner(postHookRunner schemas.PostHookRunner) schemas.PostHookRunner {
	return func(ctx *schemas.BifrostContext, result *schemas.BifrostResponse, bifrostErr *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError) {
		if result != nil && result.ResponsesStreamResponse != nil {
			if converted := result.ResponsesStreamResponse.ToBifrostChatResponse(); converted != nil {
				result = &schemas.BifrostResponse{ChatResponse: converted}
			}
		}
		if postHookRunner != nil {
			return postHookRunner(ctx, result, bifrostErr)
		}
		return result, bifrostErr
	}
}

// ChatCompletion performs a chat completion request by converting to Responses API format
// and routing to the Opencode Free /v1/responses endpoint.
func (p *opencodeFreeProvider) ChatCompletion(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostChatRequest) (*schemas.BifrostChatResponse, *schemas.BifrostError) {
	if request == nil {
		return nil, &schemas.BifrostError{
			IsBifrostError: true,
			Error: &schemas.ErrorField{
				Message: "chat completion request is nil",
			},
		}
	}
	responsesReq := request.ToResponsesRequest()
	if responsesReq == nil {
		return nil, &schemas.BifrostError{
			IsBifrostError: true,
			Error: &schemas.ErrorField{
				Message: "failed to convert chat request to responses request",
			},
		}
	}
	responsesResp, bifrostErr := p.Responses(ctx, key, responsesReq)
	if bifrostErr != nil {
		return nil, bifrostErr
	}
	chatResp := responsesResp.ToBifrostChatResponse()
	if chatResp == nil {
		return nil, &schemas.BifrostError{
			IsBifrostError: true,
			Error: &schemas.ErrorField{
				Message: "failed to convert responses response to chat response",
			},
		}
	}
	chatResp.BackfillParams(request)
	return chatResp, nil
}

// ChatCompletionStream performs a streaming chat completion request by converting to Responses API
// format and routing to the Opencode Free /v1/responses endpoint.
func (p *opencodeFreeProvider) ChatCompletionStream(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, postHookSpanFinalizer func(context.Context), key schemas.Key, request *schemas.BifrostChatRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	if request == nil {
		return nil, &schemas.BifrostError{
			IsBifrostError: true,
			Error: &schemas.ErrorField{
				Message: "chat completion stream request is nil",
			},
		}
	}
	responsesReq := request.ToResponsesRequest()
	if responsesReq == nil {
		return nil, &schemas.BifrostError{
			IsBifrostError: true,
			Error: &schemas.ErrorField{
				Message: "failed to convert chat request to responses request",
			},
		}
	}
	return p.ResponsesStream(
		ctx,
		wrapResponsesToChatStreamPostHookRunner(postHookRunner),
		postHookSpanFinalizer,
		key,
		responsesReq,
	)
}

// Responses performs a responses request to the Opencode Free API.
func (p *opencodeFreeProvider) Responses(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostResponsesRequest) (*schemas.BifrostResponsesResponse, *schemas.BifrostError) {
	ensureResponsesReasoning(request)
	authHeader := map[string]string{"Authorization": ""}
	extraHeaders := BuildHeaders(ctx, p.networkConfig.ExtraHeaders, false)
	return openai.HandleOpenAIResponsesRequest(
		ctx,
		p.client,
		p.networkConfig.BaseURL+providerUtils.GetPathFromContext(ctx, "/v1/responses"),
		request,
		authHeader,
		extraHeaders,
		providerUtils.ShouldSendBackRawRequest(ctx, p.sendBackRawRequest),
		providerUtils.ShouldSendBackRawResponse(ctx, p.sendBackRawResponse),
		p.providerKey,
		nil,
		parseOpencodeFreeError,
		nil,
		p.logger,
	)
}

// ResponsesStream performs a streaming responses request to the Opencode Free API.
func (p *opencodeFreeProvider) ResponsesStream(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, postHookSpanFinalizer func(context.Context), key schemas.Key, request *schemas.BifrostResponsesRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	ensureResponsesReasoning(request)
	authHeader := map[string]string{"Authorization": ""}
	extraHeaders := BuildHeaders(ctx, p.networkConfig.ExtraHeaders, true)
	return openai.HandleOpenAIResponsesStreaming(
		ctx,
		p.streamingClient,
		p.networkConfig.BaseURL+providerUtils.GetPathFromContext(ctx, "/v1/responses"),
		request,
		authHeader,
		extraHeaders,
		p.networkConfig.StreamIdleTimeoutInSeconds,
		providerUtils.ShouldSendBackRawRequest(ctx, p.sendBackRawRequest),
		providerUtils.ShouldSendBackRawResponse(ctx, p.sendBackRawResponse),
		p.providerKey,
		postHookRunner,
		nil,
		parseOpencodeFreeError,
		nil,
		nil,
		nil,
		p.logger,
		postHookSpanFinalizer,
	)
}

// Embedding is not supported by OpencodeFree.
func (p *opencodeFreeProvider) Embedding(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostEmbeddingRequest) (*schemas.BifrostEmbeddingResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.EmbeddingRequest, p.GetProviderKey())
}

// Rerank is not supported by OpencodeFree.
func (p *opencodeFreeProvider) Rerank(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostRerankRequest) (*schemas.BifrostRerankResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.RerankRequest, p.GetProviderKey())
}

// OCR is not supported by OpencodeFree.
func (p *opencodeFreeProvider) OCR(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostOCRRequest) (*schemas.BifrostOCRResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.OCRRequest, p.GetProviderKey())
}

// Speech is not supported by OpencodeFree.
func (p *opencodeFreeProvider) Speech(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostSpeechRequest) (*schemas.BifrostSpeechResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.SpeechRequest, p.GetProviderKey())
}

// SpeechStream is not supported by OpencodeFree.
func (p *opencodeFreeProvider) SpeechStream(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, postHookSpanFinalizer func(context.Context), key schemas.Key, request *schemas.BifrostSpeechRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.SpeechStreamRequest, p.GetProviderKey())
}

// Transcription is not supported by OpencodeFree.
func (p *opencodeFreeProvider) Transcription(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostTranscriptionRequest) (*schemas.BifrostTranscriptionResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.TranscriptionRequest, p.GetProviderKey())
}

// TranscriptionStream is not supported by OpencodeFree.
func (p *opencodeFreeProvider) TranscriptionStream(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, postHookSpanFinalizer func(context.Context), key schemas.Key, request *schemas.BifrostTranscriptionRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.TranscriptionStreamRequest, p.GetProviderKey())
}

// ImageGeneration is not supported by OpencodeFree.
func (p *opencodeFreeProvider) ImageGeneration(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostImageGenerationRequest) (*schemas.BifrostImageGenerationResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ImageGenerationRequest, p.GetProviderKey())
}

// ImageGenerationStream is not supported by OpencodeFree.
func (p *opencodeFreeProvider) ImageGenerationStream(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, postHookSpanFinalizer func(context.Context), key schemas.Key, request *schemas.BifrostImageGenerationRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ImageGenerationStreamRequest, p.GetProviderKey())
}

// ImageEdit is not supported by OpencodeFree.
func (p *opencodeFreeProvider) ImageEdit(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostImageEditRequest) (*schemas.BifrostImageGenerationResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ImageEditRequest, p.GetProviderKey())
}

// ImageEditStream is not supported by OpencodeFree.
func (p *opencodeFreeProvider) ImageEditStream(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, postHookSpanFinalizer func(context.Context), key schemas.Key, request *schemas.BifrostImageEditRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ImageEditStreamRequest, p.GetProviderKey())
}

// ImageVariation is not supported by OpencodeFree.
func (p *opencodeFreeProvider) ImageVariation(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostImageVariationRequest) (*schemas.BifrostImageGenerationResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ImageVariationRequest, p.GetProviderKey())
}

// VideoGeneration is not supported by OpencodeFree.
func (p *opencodeFreeProvider) VideoGeneration(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostVideoGenerationRequest) (*schemas.BifrostVideoGenerationResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.VideoGenerationRequest, p.GetProviderKey())
}

// VideoRetrieve is not supported by OpencodeFree.
func (p *opencodeFreeProvider) VideoRetrieve(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostVideoRetrieveRequest) (*schemas.BifrostVideoGenerationResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.VideoRetrieveRequest, p.GetProviderKey())
}

// VideoDownload is not supported by OpencodeFree.
func (p *opencodeFreeProvider) VideoDownload(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostVideoDownloadRequest) (*schemas.BifrostVideoDownloadResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.VideoDownloadRequest, p.GetProviderKey())
}

// VideoDelete is not supported by OpencodeFree.
func (p *opencodeFreeProvider) VideoDelete(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostVideoDeleteRequest) (*schemas.BifrostVideoDeleteResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.VideoDeleteRequest, p.GetProviderKey())
}

// VideoList is not supported by OpencodeFree.
func (p *opencodeFreeProvider) VideoList(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostVideoListRequest) (*schemas.BifrostVideoListResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.VideoListRequest, p.GetProviderKey())
}

// VideoEdit is not supported by OpencodeFree.
func (p *opencodeFreeProvider) VideoEdit(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostVideoEditRequest) (*schemas.BifrostVideoEditResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.VideoEditRequest, p.GetProviderKey())
}

// VideoRemix is not supported by OpencodeFree.
func (p *opencodeFreeProvider) VideoRemix(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostVideoRemixRequest) (*schemas.BifrostVideoGenerationResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.VideoRemixRequest, p.GetProviderKey())
}

// BatchCreate is not supported by OpencodeFree.
func (p *opencodeFreeProvider) BatchCreate(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostBatchCreateRequest) (*schemas.BifrostBatchCreateResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.BatchCreateRequest, p.GetProviderKey())
}

// BatchList is not supported by OpencodeFree.
func (p *opencodeFreeProvider) BatchList(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostBatchListRequest) (*schemas.BifrostBatchListResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.BatchListRequest, p.GetProviderKey())
}

// BatchRetrieve is not supported by OpencodeFree.
func (p *opencodeFreeProvider) BatchRetrieve(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostBatchRetrieveRequest) (*schemas.BifrostBatchRetrieveResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.BatchRetrieveRequest, p.GetProviderKey())
}

// BatchCancel is not supported by OpencodeFree.
func (p *opencodeFreeProvider) BatchCancel(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostBatchCancelRequest) (*schemas.BifrostBatchCancelResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.BatchCancelRequest, p.GetProviderKey())
}

// BatchDelete is not supported by OpencodeFree.
func (p *opencodeFreeProvider) BatchDelete(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostBatchDeleteRequest) (*schemas.BifrostBatchDeleteResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.BatchDeleteRequest, p.GetProviderKey())
}

// BatchResults is not supported by OpencodeFree.
func (p *opencodeFreeProvider) BatchResults(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostBatchResultsRequest) (*schemas.BifrostBatchResultsResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.BatchResultsRequest, p.GetProviderKey())
}

// FileUpload is not supported by OpencodeFree.
func (p *opencodeFreeProvider) FileUpload(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostFileUploadRequest) (*schemas.BifrostFileUploadResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.FileUploadRequest, p.GetProviderKey())
}

// FileList is not supported by OpencodeFree.
func (p *opencodeFreeProvider) FileList(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostFileListRequest) (*schemas.BifrostFileListResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.FileListRequest, p.GetProviderKey())
}

// FileRetrieve is not supported by OpencodeFree.
func (p *opencodeFreeProvider) FileRetrieve(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostFileRetrieveRequest) (*schemas.BifrostFileRetrieveResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.FileRetrieveRequest, p.GetProviderKey())
}

// FileDelete is not supported by OpencodeFree.
func (p *opencodeFreeProvider) FileDelete(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostFileDeleteRequest) (*schemas.BifrostFileDeleteResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.FileDeleteRequest, p.GetProviderKey())
}

// FileContent is not supported by OpencodeFree.
func (p *opencodeFreeProvider) FileContent(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostFileContentRequest) (*schemas.BifrostFileContentResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.FileContentRequest, p.GetProviderKey())
}

// CountTokens is not supported by OpencodeFree.
func (p *opencodeFreeProvider) CountTokens(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostResponsesRequest) (*schemas.BifrostCountTokensResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.CountTokensRequest, p.GetProviderKey())
}

// Compaction is not supported by OpencodeFree.
func (p *opencodeFreeProvider) Compaction(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostCompactionRequest) (*schemas.BifrostCompactionResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.CompactionRequest, p.GetProviderKey())
}

// ContainerCreate is not supported by OpencodeFree.
func (p *opencodeFreeProvider) ContainerCreate(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostContainerCreateRequest) (*schemas.BifrostContainerCreateResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerCreateRequest, p.GetProviderKey())
}

// ContainerList is not supported by OpencodeFree.
func (p *opencodeFreeProvider) ContainerList(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostContainerListRequest) (*schemas.BifrostContainerListResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerListRequest, p.GetProviderKey())
}

// ContainerRetrieve is not supported by OpencodeFree.
func (p *opencodeFreeProvider) ContainerRetrieve(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostContainerRetrieveRequest) (*schemas.BifrostContainerRetrieveResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerRetrieveRequest, p.GetProviderKey())
}

// ContainerDelete is not supported by OpencodeFree.
func (p *opencodeFreeProvider) ContainerDelete(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostContainerDeleteRequest) (*schemas.BifrostContainerDeleteResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerDeleteRequest, p.GetProviderKey())
}

// ContainerFileCreate is not supported by OpencodeFree.
func (p *opencodeFreeProvider) ContainerFileCreate(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostContainerFileCreateRequest) (*schemas.BifrostContainerFileCreateResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerFileCreateRequest, p.GetProviderKey())
}

// ContainerFileList is not supported by OpencodeFree.
func (p *opencodeFreeProvider) ContainerFileList(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostContainerFileListRequest) (*schemas.BifrostContainerFileListResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerFileListRequest, p.GetProviderKey())
}

// ContainerFileRetrieve is not supported by OpencodeFree.
func (p *opencodeFreeProvider) ContainerFileRetrieve(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostContainerFileRetrieveRequest) (*schemas.BifrostContainerFileRetrieveResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerFileRetrieveRequest, p.GetProviderKey())
}

// ContainerFileContent is not supported by OpencodeFree.
func (p *opencodeFreeProvider) ContainerFileContent(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostContainerFileContentRequest) (*schemas.BifrostContainerFileContentResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerFileContentRequest, p.GetProviderKey())
}

// ContainerFileDelete is not supported by OpencodeFree.
func (p *opencodeFreeProvider) ContainerFileDelete(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostContainerFileDeleteRequest) (*schemas.BifrostContainerFileDeleteResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerFileDeleteRequest, p.GetProviderKey())
}

// Passthrough is not supported by OpencodeFree.
func (p *opencodeFreeProvider) Passthrough(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostPassthroughRequest) (*schemas.BifrostPassthroughResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.PassthroughRequest, p.GetProviderKey())
}

// PassthroughStream is not supported by OpencodeFree.
func (p *opencodeFreeProvider) PassthroughStream(_ *schemas.BifrostContext, _ schemas.PostHookRunner, _ func(context.Context), _ schemas.Key, _ *schemas.BifrostPassthroughRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.PassthroughStreamRequest, p.GetProviderKey())
}
