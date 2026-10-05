// Package zed implements the Zed provider: Zed editor's cloud LLM surface at
// https://cloud.zed.dev.
//
// Zed exposes a single inference endpoint, POST /completions, wrapped in an
// envelope:
//
//	{thread_id, prompt_id, provider, model, provider_request}
//
// provider is Zed's own vocabulary ("anthropic" | "google" | "open_ai") and
// provider_request carries the native upstream shape for that family:
// Anthropic Messages API, Gemini generateContent, or the OpenAI Responses API
// (input[], never Chat Completions). The response is always an NDJSON stream
// of {"event": ...} lines ending with {"status":"stream_ended"} — there is no
// non-streaming mode, so unary Bifrost calls accumulate the stream.
//
// Auth is two-layer: the operator's Zed login (user_id + access_token JSON
// blob) mints a short-lived LLM token per organization via
// POST /client/llm_tokens, and inference calls carry it as
// "Bearer <llm_token>". organization_id defaults to default_organization_id
// from GET /client/users/me.
package zed

import (
	"strings"
	"time"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

// ZedProvider implements the Provider interface for Zed's cloud LLM API.
type ZedProvider struct {
	logger              schemas.Logger        // Logger for provider operations
	client              *fasthttp.Client      // HTTP client for unary API requests (ReadTimeout bounds overall response)
	streamingClient     *fasthttp.Client      // HTTP client for streaming API requests (no ReadTimeout; idle governed by NewIdleTimeoutReader)
	networkConfig       schemas.NetworkConfig // Network configuration including extra headers
	sendBackRawRequest  bool                  // Whether to include raw request in BifrostResponse
	sendBackRawResponse bool                  // Whether to include raw response in BifrostResponse
	chatPath            string
	modelsPath          string
	usersMePath         string
	llmTokensPath       string
}

// NewZedProvider creates a new Zed provider instance.
func NewZedProvider(config *schemas.ProviderConfig, logger schemas.Logger) (*ZedProvider, error) {
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

	return &ZedProvider{
		logger:              logger,
		client:              client,
		streamingClient:     streamingClient,
		networkConfig:       config.NetworkConfig,
		sendBackRawRequest:  config.SendBackRawRequest,
		sendBackRawResponse: config.SendBackRawResponse,
		chatPath:            "/completions",
		modelsPath:          "/models",
		usersMePath:         "/client/users/me",
		llmTokensPath:       "/client/llm_tokens",
	}, nil
}

// GetProviderKey returns the provider identifier for Zed.
func (provider *ZedProvider) GetProviderKey() schemas.ModelProvider {
	return schemas.Zed
}
