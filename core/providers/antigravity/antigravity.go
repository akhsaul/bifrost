// Package antigravity implements the Antigravity (Google Cloud Code / Gemini Code Assist) provider for Bifrost.
package antigravity

import (
	"context"
	"strings"
	"time"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

// AntigravityProvider implements the Provider interface for Google Antigravity / Cloud Code.
type AntigravityProvider struct {
	logger               schemas.Logger                // Logger for provider operations
	client               *fasthttp.Client              // HTTP client for unary API requests
	streamingClient      *fasthttp.Client              // HTTP client for streaming API requests
	networkConfig        schemas.NetworkConfig         // Network configuration including extra headers
	sendBackRawRequest   bool                          // Whether to include raw request in BifrostResponse
	sendBackRawResponse  bool                          // Whether to include raw response in BifrostResponse
	customProviderConfig *schemas.CustomProviderConfig // Custom provider config
}

// NewAntigravityProvider creates a new Antigravity provider instance.
func NewAntigravityProvider(config *schemas.ProviderConfig, logger schemas.Logger) (*AntigravityProvider, error) {
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
		config.NetworkConfig.BaseURL = DefaultRuntimeBaseURL
	}
	config.NetworkConfig.BaseURL = strings.TrimRight(config.NetworkConfig.BaseURL, "/")

	return &AntigravityProvider{
		logger:               logger,
		client:               client,
		streamingClient:      streamingClient,
		networkConfig:        config.NetworkConfig,
		sendBackRawRequest:   config.SendBackRawRequest,
		sendBackRawResponse:  config.SendBackRawResponse,
		customProviderConfig: config.CustomProviderConfig,
	}, nil
}

// GetProviderKey returns the provider identifier for Antigravity.
func (provider *AntigravityProvider) GetProviderKey() schemas.ModelProvider {
	return providerUtils.GetProviderName(schemas.Antigravity, provider.customProviderConfig)
}

// ListModels performs a list models request to Antigravity.
func (provider *AntigravityProvider) ListModels(
	ctx *schemas.BifrostContext,
	keys []schemas.Key,
	request *schemas.BifrostListModelsRequest,
) (*schemas.BifrostListModelsResponse, *schemas.BifrostError) {
	return HandleListModels(ctx, provider.client, keys, provider.networkConfig.BaseURL, provider.networkConfig.ExtraHeaders, provider.logger)
}

// GetKeyQuotaSummary fetches aggregated quota and limits per bucket from /v1internal:retrieveUserQuotaSummary.
func (provider *AntigravityProvider) GetKeyQuotaSummary(
	ctx *schemas.BifrostContext,
	key schemas.Key,
) (*schemas.KeyQuotaSummary, *schemas.BifrostError) {
	accessToken, projectID, authErr := GetAccessTokenAndProject(ctx, provider.client, key, provider.networkConfig.BaseURL, provider.logger)
	if authErr != nil {
		return nil, authErr
	}

	if projectID == "" {
		return nil, newAuthenticationError("missing Google Cloud project ID for Antigravity account", nil)
	}

	summaryResp, err := RetrieveUserQuotaSummaryFromAPI(ctx, provider.client, accessToken, projectID, provider.networkConfig.BaseURL, provider.networkConfig.ExtraHeaders, provider.logger)
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderDoRequest, err)
	}

	groups := make([]schemas.QuotaGroup, 0, len(summaryResp.Groups))
	for _, g := range summaryResp.Groups {
		buckets := make([]schemas.QuotaBucket, 0, len(g.Buckets))
		for _, b := range g.Buckets {
			var resetTime time.Time
			if b.ResetTime != "" {
				resetTime, _ = time.Parse(time.RFC3339, b.ResetTime)
			}
			buckets = append(buckets, schemas.QuotaBucket{
				BucketID:          b.BucketID,
				DisplayName:       b.DisplayName,
				Window:            b.Window,
				ResetTime:         resetTime,
				Description:       b.Description,
				RemainingFraction: b.RemainingFraction,
			})
		}
		groups = append(groups, schemas.QuotaGroup{
			DisplayName: g.DisplayName,
			Description: g.Description,
			Buckets:     buckets,
		})
	}

	return &schemas.KeyQuotaSummary{
		KeyID:       key.ID,
		Provider:    provider.GetProviderKey(),
		Groups:      groups,
		FetchedAt:   time.Now(),
		Description: summaryResp.Description,
	}, nil
}

// GetModelsQuota fetches per-model quota information from /v1internal:fetchAvailableModels.
func (provider *AntigravityProvider) GetModelsQuota(
	ctx *schemas.BifrostContext,
	key schemas.Key,
) (map[string]schemas.ModelQuotaInfo, *schemas.BifrostError) {
	accessToken, projectID, authErr := GetAccessTokenAndProject(ctx, provider.client, key, provider.networkConfig.BaseURL, provider.logger)
	if authErr != nil {
		return nil, authErr
	}

	if projectID == "" {
		return nil, newAuthenticationError("missing Google Cloud project ID for Antigravity account", nil)
	}

	fetchResp, err := FetchRawAvailableModelsFromAPI(ctx, provider.client, accessToken, projectID, provider.networkConfig.BaseURL, provider.networkConfig.ExtraHeaders, provider.logger)
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderDoRequest, err)
	}

	now := time.Now()
	res := make(map[string]schemas.ModelQuotaInfo)
	for modelID, details := range fetchResp.Models {
		if details.IsInternal {
			continue
		}
		info := schemas.ModelQuotaInfo{
			Model:             modelID,
			DisplayName:       details.DisplayName,
			RemainingFraction: 1.0,
		}
		if details.QuotaInfo != nil {
			info.RemainingFraction = details.QuotaInfo.RemainingFraction
			if details.QuotaInfo.ResetTime != "" {
				if t, parseErr := time.Parse(time.RFC3339, details.QuotaInfo.ResetTime); parseErr == nil {
					info.ResetTime = t
					if diff := t.Sub(now); diff > 0 {
						info.ResetAfter = diff
					}
				}
			}
			// If remaining fraction is 0 or less, mark as limited
			if info.RemainingFraction <= 0 {
				info.IsLimited = true
			}
		}
		res[modelID] = info
	}

	return res, nil
}

// ChatCompletion performs a chat completion request to the Antigravity API.
func (provider *AntigravityProvider) ChatCompletion(
	ctx *schemas.BifrostContext,
	key schemas.Key,
	request *schemas.BifrostChatRequest,
) (*schemas.BifrostChatResponse, *schemas.BifrostError) {
	accessToken, projectID, authErr := GetAccessTokenAndProject(ctx, provider.client, key, provider.networkConfig.BaseURL, provider.logger)
	if authErr != nil {
		return nil, authErr
	}

	if projectID == "" {
		return nil, newAuthenticationError("missing Google Cloud project ID for Antigravity account (ensure Gemini Code Assist onboarding is completed)", nil)
	}

	creds := GetCredentials(key)
	_, jsonBytes, err := ToAntigravityChatRequest(ctx, request, projectID)
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrRequestBodyConversion, err)
	}

	targetURL := provider.networkConfig.BaseURL + providerUtils.GetPathFromContext(ctx, GenerateContentPath)
	headers := map[string]string{
		"Authorization": "Bearer " + accessToken,
		"User-Agent":    GetUserAgent(creds.ClientProfile),
	}

	return HandleAntigravityChatCompletion(
		ctx,
		provider.client,
		targetURL,
		jsonBytes,
		headers,
		provider.networkConfig.ExtraHeaders,
		request.Model,
		providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest),
		providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse),
		provider.logger,
	)
}

// ChatCompletionStream performs a streaming chat completion request to the Antigravity API.
func (provider *AntigravityProvider) ChatCompletionStream(
	ctx *schemas.BifrostContext,
	postHookRunner schemas.PostHookRunner,
	postHookSpanFinalizer func(context.Context),
	key schemas.Key,
	request *schemas.BifrostChatRequest,
) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	accessToken, projectID, authErr := GetAccessTokenAndProject(ctx, provider.client, key, provider.networkConfig.BaseURL, provider.logger)
	if authErr != nil {
		return nil, authErr
	}

	if projectID == "" {
		return nil, newAuthenticationError("missing Google Cloud project ID for Antigravity account (ensure Gemini Code Assist onboarding is completed)", nil)
	}

	creds := GetCredentials(key)
	_, jsonBytes, err := ToAntigravityChatRequest(ctx, request, projectID)
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrRequestBodyConversion, err)
	}

	targetURL := provider.networkConfig.BaseURL + providerUtils.GetPathFromContext(ctx, StreamGeneratePath)
	headers := map[string]string{
		"Authorization": "Bearer " + accessToken,
		"User-Agent":    GetUserAgent(creds.ClientProfile),
	}

	return HandleAntigravityChatCompletionStream(
		ctx,
		provider.streamingClient,
		targetURL,
		jsonBytes,
		headers,
		provider.networkConfig.ExtraHeaders,
		request.Model,
		provider.networkConfig.StreamIdleTimeoutInSeconds,
		providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest),
		providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse),
		postHookRunner,
		postHookSpanFinalizer,
		provider.logger,
	)
}

// Responses performs a responses request using the chat completion path internally.
func (provider *AntigravityProvider) Responses(
	ctx *schemas.BifrostContext,
	key schemas.Key,
	request *schemas.BifrostResponsesRequest,
) (*schemas.BifrostResponsesResponse, *schemas.BifrostError) {
	chatResponse, err := provider.ChatCompletion(ctx, key, request.ToChatRequest())
	if err != nil {
		return nil, err
	}
	return chatResponse.ToBifrostResponsesResponse(), nil
}

// ResponsesStream performs a streaming responses request using the chat completion stream internally.
func (provider *AntigravityProvider) ResponsesStream(
	ctx *schemas.BifrostContext,
	postHookRunner schemas.PostHookRunner,
	postHookSpanFinalizer func(context.Context),
	key schemas.Key,
	request *schemas.BifrostResponsesRequest,
) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	ctx.SetValue(schemas.BifrostContextKeyIsResponsesToChatCompletionFallback, true)
	return provider.ChatCompletionStream(
		ctx,
		postHookRunner,
		postHookSpanFinalizer,
		key,
		request.ToChatRequest(),
	)
}

// CountTokens is not supported directly by the Antigravity provider.
func (provider *AntigravityProvider) CountTokens(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostResponsesRequest) (*schemas.BifrostCountTokensResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.CountTokensRequest, provider.GetProviderKey())
}

// Compaction is not supported by the Antigravity provider.
func (provider *AntigravityProvider) Compaction(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostCompactionRequest) (*schemas.BifrostCompactionResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.CompactionRequest, provider.GetProviderKey())
}

// TextCompletion is not supported by the Antigravity provider.
func (provider *AntigravityProvider) TextCompletion(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostTextCompletionRequest) (*schemas.BifrostTextCompletionResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.TextCompletionRequest, provider.GetProviderKey())
}

// TextCompletionStream is not supported by the Antigravity provider.
func (provider *AntigravityProvider) TextCompletionStream(_ *schemas.BifrostContext, _ schemas.PostHookRunner, _ func(context.Context), _ schemas.Key, _ *schemas.BifrostTextCompletionRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.TextCompletionStreamRequest, provider.GetProviderKey())
}

// Embedding is not supported by the Antigravity provider.
func (provider *AntigravityProvider) Embedding(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostEmbeddingRequest) (*schemas.BifrostEmbeddingResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.EmbeddingRequest, provider.GetProviderKey())
}

// Rerank is not supported by the Antigravity provider.
func (provider *AntigravityProvider) Rerank(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostRerankRequest) (*schemas.BifrostRerankResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.RerankRequest, provider.GetProviderKey())
}

// OCR is not supported by the Antigravity provider.
func (provider *AntigravityProvider) OCR(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostOCRRequest) (*schemas.BifrostOCRResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.OCRRequest, provider.GetProviderKey())
}

// Speech is not supported by the Antigravity provider.
func (provider *AntigravityProvider) Speech(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostSpeechRequest) (*schemas.BifrostSpeechResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.SpeechRequest, provider.GetProviderKey())
}

// SpeechStream is not supported by the Antigravity provider.
func (provider *AntigravityProvider) SpeechStream(_ *schemas.BifrostContext, _ schemas.PostHookRunner, _ func(context.Context), _ schemas.Key, _ *schemas.BifrostSpeechRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.SpeechStreamRequest, provider.GetProviderKey())
}

// Transcription is not supported by the Antigravity provider.
func (provider *AntigravityProvider) Transcription(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostTranscriptionRequest) (*schemas.BifrostTranscriptionResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.TranscriptionRequest, provider.GetProviderKey())
}

// TranscriptionStream is not supported by the Antigravity provider.
func (provider *AntigravityProvider) TranscriptionStream(_ *schemas.BifrostContext, _ schemas.PostHookRunner, _ func(context.Context), _ schemas.Key, _ *schemas.BifrostTranscriptionRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.TranscriptionStreamRequest, provider.GetProviderKey())
}

// ImageGeneration is not supported by the Antigravity provider.
func (provider *AntigravityProvider) ImageGeneration(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostImageGenerationRequest) (*schemas.BifrostImageGenerationResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ImageGenerationRequest, provider.GetProviderKey())
}

// ImageGenerationStream is not supported by the Antigravity provider.
func (provider *AntigravityProvider) ImageGenerationStream(_ *schemas.BifrostContext, _ schemas.PostHookRunner, _ func(context.Context), _ schemas.Key, _ *schemas.BifrostImageGenerationRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ImageGenerationStreamRequest, provider.GetProviderKey())
}

// ImageEdit is not supported by the Antigravity provider.
func (provider *AntigravityProvider) ImageEdit(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostImageEditRequest) (*schemas.BifrostImageGenerationResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ImageEditRequest, provider.GetProviderKey())
}

// ImageEditStream is not supported by the Antigravity provider.
func (provider *AntigravityProvider) ImageEditStream(_ *schemas.BifrostContext, _ schemas.PostHookRunner, _ func(context.Context), _ schemas.Key, _ *schemas.BifrostImageEditRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ImageEditStreamRequest, provider.GetProviderKey())
}

// ImageVariation is not supported by the Antigravity provider.
func (provider *AntigravityProvider) ImageVariation(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostImageVariationRequest) (*schemas.BifrostImageGenerationResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ImageVariationRequest, provider.GetProviderKey())
}

// VideoGeneration is not supported by the Antigravity provider.
func (provider *AntigravityProvider) VideoGeneration(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostVideoGenerationRequest) (*schemas.BifrostVideoGenerationResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.VideoGenerationRequest, provider.GetProviderKey())
}

// VideoRetrieve is not supported by the Antigravity provider.
func (provider *AntigravityProvider) VideoRetrieve(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostVideoRetrieveRequest) (*schemas.BifrostVideoGenerationResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.VideoRetrieveRequest, provider.GetProviderKey())
}

// VideoDownload is not supported by the Antigravity provider.
func (provider *AntigravityProvider) VideoDownload(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostVideoDownloadRequest) (*schemas.BifrostVideoDownloadResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.VideoDownloadRequest, provider.GetProviderKey())
}

// VideoDelete is not supported by the Antigravity provider.
func (provider *AntigravityProvider) VideoDelete(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostVideoDeleteRequest) (*schemas.BifrostVideoDeleteResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.VideoDeleteRequest, provider.GetProviderKey())
}

// VideoList is not supported by the Antigravity provider.
func (provider *AntigravityProvider) VideoList(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostVideoListRequest) (*schemas.BifrostVideoListResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.VideoListRequest, provider.GetProviderKey())
}

// VideoRemix is not supported by the Antigravity provider.
func (provider *AntigravityProvider) VideoRemix(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostVideoRemixRequest) (*schemas.BifrostVideoGenerationResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.VideoRemixRequest, provider.GetProviderKey())
}

// BatchCreate is not supported by the Antigravity provider.
func (provider *AntigravityProvider) BatchCreate(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostBatchCreateRequest) (*schemas.BifrostBatchCreateResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.BatchCreateRequest, provider.GetProviderKey())
}

// BatchList is not supported by the Antigravity provider.
func (provider *AntigravityProvider) BatchList(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostBatchListRequest) (*schemas.BifrostBatchListResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.BatchListRequest, provider.GetProviderKey())
}

// BatchRetrieve is not supported by the Antigravity provider.
func (provider *AntigravityProvider) BatchRetrieve(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostBatchRetrieveRequest) (*schemas.BifrostBatchRetrieveResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.BatchRetrieveRequest, provider.GetProviderKey())
}

// BatchCancel is not supported by the Antigravity provider.
func (provider *AntigravityProvider) BatchCancel(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostBatchCancelRequest) (*schemas.BifrostBatchCancelResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.BatchCancelRequest, provider.GetProviderKey())
}

// BatchDelete is not supported by the Antigravity provider.
func (provider *AntigravityProvider) BatchDelete(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostBatchDeleteRequest) (*schemas.BifrostBatchDeleteResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.BatchDeleteRequest, provider.GetProviderKey())
}

// BatchResults is not supported by the Antigravity provider.
func (provider *AntigravityProvider) BatchResults(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostBatchResultsRequest) (*schemas.BifrostBatchResultsResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.BatchResultsRequest, provider.GetProviderKey())
}

// FileUpload is not supported by the Antigravity provider.
func (provider *AntigravityProvider) FileUpload(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostFileUploadRequest) (*schemas.BifrostFileUploadResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.FileUploadRequest, provider.GetProviderKey())
}

// FileList is not supported by the Antigravity provider.
func (provider *AntigravityProvider) FileList(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostFileListRequest) (*schemas.BifrostFileListResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.FileListRequest, provider.GetProviderKey())
}

// FileRetrieve is not supported by the Antigravity provider.
func (provider *AntigravityProvider) FileRetrieve(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostFileRetrieveRequest) (*schemas.BifrostFileRetrieveResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.FileRetrieveRequest, provider.GetProviderKey())
}

// FileDelete is not supported by the Antigravity provider.
func (provider *AntigravityProvider) FileDelete(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostFileDeleteRequest) (*schemas.BifrostFileDeleteResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.FileDeleteRequest, provider.GetProviderKey())
}

// FileContent is not supported by the Antigravity provider.
func (provider *AntigravityProvider) FileContent(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostFileContentRequest) (*schemas.BifrostFileContentResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.FileContentRequest, provider.GetProviderKey())
}

// CachedContentCreate is not supported by the Antigravity provider.
func (provider *AntigravityProvider) CachedContentCreate(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostCachedContentCreateRequest) (*schemas.BifrostCachedContentCreateResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.CachedContentCreateRequest, provider.GetProviderKey())
}

// CachedContentList is not supported by the Antigravity provider.
func (provider *AntigravityProvider) CachedContentList(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostCachedContentListRequest) (*schemas.BifrostCachedContentListResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.CachedContentListRequest, provider.GetProviderKey())
}

// CachedContentRetrieve is not supported by the Antigravity provider.
func (provider *AntigravityProvider) CachedContentRetrieve(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostCachedContentRetrieveRequest) (*schemas.BifrostCachedContentRetrieveResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.CachedContentRetrieveRequest, provider.GetProviderKey())
}

// CachedContentUpdate is not supported by the Antigravity provider.
func (provider *AntigravityProvider) CachedContentUpdate(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostCachedContentUpdateRequest) (*schemas.BifrostCachedContentUpdateResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.CachedContentUpdateRequest, provider.GetProviderKey())
}

// CachedContentDelete is not supported by the Antigravity provider.
func (provider *AntigravityProvider) CachedContentDelete(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostCachedContentDeleteRequest) (*schemas.BifrostCachedContentDeleteResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.CachedContentDeleteRequest, provider.GetProviderKey())
}

// ContainerCreate is not supported by the Antigravity provider.
func (provider *AntigravityProvider) ContainerCreate(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostContainerCreateRequest) (*schemas.BifrostContainerCreateResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerCreateRequest, provider.GetProviderKey())
}

// ContainerList is not supported by the Antigravity provider.
func (provider *AntigravityProvider) ContainerList(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostContainerListRequest) (*schemas.BifrostContainerListResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerListRequest, provider.GetProviderKey())
}

// ContainerRetrieve is not supported by the Antigravity provider.
func (provider *AntigravityProvider) ContainerRetrieve(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostContainerRetrieveRequest) (*schemas.BifrostContainerRetrieveResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerRetrieveRequest, provider.GetProviderKey())
}

// ContainerDelete is not supported by the Antigravity provider.
func (provider *AntigravityProvider) ContainerDelete(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostContainerDeleteRequest) (*schemas.BifrostContainerDeleteResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerDeleteRequest, provider.GetProviderKey())
}

// ContainerFileCreate is not supported by the Antigravity provider.
func (provider *AntigravityProvider) ContainerFileCreate(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostContainerFileCreateRequest) (*schemas.BifrostContainerFileCreateResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerFileCreateRequest, provider.GetProviderKey())
}

// ContainerFileList is not supported by the Antigravity provider.
func (provider *AntigravityProvider) ContainerFileList(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostContainerFileListRequest) (*schemas.BifrostContainerFileListResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerFileListRequest, provider.GetProviderKey())
}

// ContainerFileRetrieve is not supported by the Antigravity provider.
func (provider *AntigravityProvider) ContainerFileRetrieve(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostContainerFileRetrieveRequest) (*schemas.BifrostContainerFileRetrieveResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerFileRetrieveRequest, provider.GetProviderKey())
}

// ContainerFileContent is not supported by the Antigravity provider.
func (provider *AntigravityProvider) ContainerFileContent(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostContainerFileContentRequest) (*schemas.BifrostContainerFileContentResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerFileContentRequest, provider.GetProviderKey())
}

// ContainerFileDelete is not supported by the Antigravity provider.
func (provider *AntigravityProvider) ContainerFileDelete(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostContainerFileDeleteRequest) (*schemas.BifrostContainerFileDeleteResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerFileDeleteRequest, provider.GetProviderKey())
}

// Passthrough is not supported by the Antigravity provider.
func (provider *AntigravityProvider) Passthrough(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostPassthroughRequest) (*schemas.BifrostPassthroughResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.PassthroughRequest, provider.GetProviderKey())
}

// PassthroughStream is not supported by the Antigravity provider.
func (provider *AntigravityProvider) PassthroughStream(_ *schemas.BifrostContext, _ schemas.PostHookRunner, _ func(context.Context), _ schemas.Key, _ *schemas.BifrostPassthroughRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.PassthroughStreamRequest, provider.GetProviderKey())
}
