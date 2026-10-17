package jinaai

import (
	"net/http"
	"strings"
	"time"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

// Provider implements the Provider interface for Jina AI.
type Provider struct {
	logger               schemas.Logger
	client               *fasthttp.Client
	streamingClient      *fasthttp.Client
	networkConfig        schemas.NetworkConfig
	sendBackRawRequest   bool
	sendBackRawResponse  bool
	customProviderConfig *schemas.CustomProviderConfig

	embeddingsPath string
	rerankPath     string
	modelsPath     string
}

// NewJinaAIProvider creates a new Jina AI provider instance.
func NewJinaAIProvider(config *schemas.ProviderConfig, logger schemas.Logger) (*Provider, error) {
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
		config.NetworkConfig.BaseURL = "https://api.jina.ai/v1"
	}
	config.NetworkConfig.BaseURL = strings.TrimRight(config.NetworkConfig.BaseURL, "/")

	return &Provider{
		logger:               logger,
		client:               client,
		streamingClient:      streamingClient,
		networkConfig:        config.NetworkConfig,
		sendBackRawRequest:   config.SendBackRawRequest,
		sendBackRawResponse:  config.SendBackRawResponse,
		customProviderConfig: config.CustomProviderConfig,
		embeddingsPath:       "/embeddings",
		rerankPath:           "/rerank",
		modelsPath:           "/models",
	}, nil
}

// GetProviderKey returns the provider identifier for Jina AI.
func (provider *Provider) GetProviderKey() schemas.ModelProvider {
	return providerUtils.GetProviderName(schemas.JinaAI, provider.customProviderConfig)
}

// completeRequest executes an HTTP POST request to the Jina AI API.
func (provider *Provider) completeRequest(ctx *schemas.BifrostContext, jsonData []byte, url string, key string) ([]byte, time.Duration, map[string]string, *schemas.BifrostError) {
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	respOwned := true
	defer func() {
		if respOwned {
			fasthttp.ReleaseResponse(resp)
		}
	}()

	providerUtils.SetExtraHeaders(ctx, req, provider.networkConfig.ExtraHeaders, nil)

	req.SetRequestURI(url)
	req.Header.SetMethod(http.MethodPost)
	req.Header.SetContentType("application/json")
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}

	usedLargePayloadBody := providerUtils.ApplyLargePayloadRequestBodyWithModelNormalization(ctx, req, schemas.JinaAI)
	if !usedLargePayloadBody {
		req.SetBody(jsonData)
	}

	activeClient := providerUtils.PrepareResponseStreaming(ctx, provider.client, resp)
	latency, bifrostErr, wait := providerUtils.MakeRequestWithContext(ctx, activeClient, req, resp)
	defer wait()
	if usedLargePayloadBody {
		providerUtils.DrainLargePayloadRemainder(ctx)
	}
	if bifrostErr != nil {
		return nil, latency, nil, bifrostErr
	}

	providerResponseHeaders := providerUtils.ExtractProviderResponseHeaders(resp)

	if resp.StatusCode() != fasthttp.StatusOK {
		providerUtils.MaterializeStreamErrorBody(ctx, resp)
		return nil, latency, providerResponseHeaders, providerUtils.SetErrorLatency(parseJinaError(resp), latency)
	}

	body, isLargeResp, decodeErr := providerUtils.FinalizeResponseWithLargeDetection(ctx, resp, provider.logger)
	if decodeErr != nil {
		return nil, latency, providerResponseHeaders, decodeErr
	}
	if isLargeResp {
		respOwned = false
		return nil, latency, providerResponseHeaders, nil
	}

	return body, latency, providerResponseHeaders, nil
}
