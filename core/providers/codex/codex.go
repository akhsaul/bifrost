package codex

import (
	"context"
	"strings"
	"time"

	"github.com/maximhq/bifrost/core/providers/openai"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

// CodexProvider implements the Provider interface for OpenAI Codex.
type CodexProvider struct {
	logger               schemas.Logger        // Logger for provider operations
	client               *fasthttp.Client      // HTTP client for unary API requests (ReadTimeout bounds overall response)
	streamingClient      *fasthttp.Client      // HTTP client for streaming API requests (no ReadTimeout; idle governed by NewIdleTimeoutReader)
	exchangeClient       *fasthttp.Client      // HTTP client for OAuth/account exchanges (capped body; governed like inference)
	networkConfig        schemas.NetworkConfig // Network configuration including extra headers
	sendBackRawRequest   bool                  // Whether to include raw request in BifrostResponse
	sendBackRawResponse  bool                  // Whether to include raw response in BifrostResponse
	customProviderConfig *schemas.CustomProviderConfig
	responsesPath        string
	modelsPath           string
}

// NewCodexProvider creates a new Codex provider instance.
func NewCodexProvider(config *schemas.ProviderConfig, logger schemas.Logger) (*CodexProvider, error) {
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

	// Order is load-bearing. ConfigureDialer wraps whatever Dial the proxy installed, and
	// BuildStreamingClient clones the fully configured client, so it must come last.
	client = providerUtils.ConfigureProxy(client, config.ProxyConfig, logger)
	client = providerUtils.ConfigureDialer(client, config.NetworkConfig.AllowPrivateNetwork)
	client = providerUtils.ConfigureTLS(client, config.NetworkConfig, logger)
	streamingClient := providerUtils.BuildStreamingClient(client)

	// Credential exchanges talk to auth.openai.com and chatgpt.com, not to the
	// inference host. Cloning the configured client inherits its dial policy,
	// proxy and TLS config without re-running any of them, so the exchange is
	// governed by exactly the same rules as inference. The body cap is
	// enforced at read time, so a runaway response is refused before it is
	// allocated rather than truncated afterwards.
	exchangeClient := providerUtils.CloneFastHTTPClientConfig(client)
	exchangeClient.MaxResponseBodySize = maxExchangeBodyBytes
	exchangeClient.StreamResponseBody = false

	if config.NetworkConfig.BaseURL == "" {
		config.NetworkConfig.BaseURL = DefaultBaseURL
	}
	config.NetworkConfig.BaseURL = strings.TrimRight(config.NetworkConfig.BaseURL, "/")

	return &CodexProvider{
		logger:               logger,
		client:               client,
		streamingClient:      streamingClient,
		exchangeClient:       exchangeClient,
		networkConfig:        config.NetworkConfig,
		sendBackRawRequest:   config.SendBackRawRequest,
		sendBackRawResponse:  config.SendBackRawResponse,
		customProviderConfig: config.CustomProviderConfig,
		responsesPath:        "/responses",
		modelsPath:           "/models",
	}, nil
}

// GetProviderKey returns the provider identifier for Codex.
func (provider *CodexProvider) GetProviderKey() schemas.ModelProvider {
	return schemas.Codex
}

// resolveAuth resolves the credentials for one request and reports whether
// OAuth mode was used (true) or a static token (false).
func (provider *CodexProvider) resolveAuth(ctx *schemas.BifrostContext, key schemas.Key) (*codexCredentials, bool, *schemas.BifrostError) {
	return resolveCredentials(ctx, key, provider.exchangeClient)
}

// doResponses runs one unary Responses attempt with the given credentials.
// Unary responses are served via stream fan-in: Codex's /responses endpoint
// is SSE-first, so stream:true is forced and the stream is accumulated into
// one response. The wire stream flag is owned here — the streaming handler
// sets it itself — so callers pass their request through untouched.
func (provider *CodexProvider) doResponses(ctx *schemas.BifrostContext, creds *codexCredentials, request *schemas.BifrostResponsesRequest) (*schemas.BifrostResponsesResponse, *schemas.BifrostError) {
	stream, bErr := openai.HandleOpenAIResponsesStreaming(
		ctx,
		provider.streamingClient,
		provider.networkConfig.BaseURL+providerUtils.GetPathFromContext(ctx, provider.responsesPath),
		request,
		authHeadersFor(creds),
		BuildHeaders(ctx, provider.networkConfig.ExtraHeaders),
		provider.networkConfig.StreamIdleTimeoutInSeconds,
		providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest),
		providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse),
		provider.GetProviderKey(),
		silentPostHookRunner,
		nil,
		parseCodexError,
		withCodexRequestDefaults,
		nil,
		nil,
		provider.logger,
		nil,
	)
	if bErr != nil {
		return nil, bErr
	}
	return accumulateResponsesStream(stream, request.Model)
}

// Responses performs a responses request against Codex's /responses endpoint.
func (provider *CodexProvider) Responses(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostResponsesRequest) (*schemas.BifrostResponsesResponse, *schemas.BifrostError) {
	if err := checkOperationAllowed(provider, schemas.ResponsesRequest); err != nil {
		return nil, err
	}
	if request == nil {
		return nil, providerUtils.NewBifrostOperationError("responses request is nil", nil)
	}
	creds, isOAuth, bErr := provider.resolveAuth(ctx, key)
	if bErr != nil {
		return nil, bErr
	}
	resp, err := provider.doResponses(ctx, creds, request)
	if err != nil && isOAuth && isUnauthorized(err) {
		// The cached access token died (or was never valid). Drop it and retry
		// once with a freshly minted one before surfacing the 401.
		invalidateCredentials(&key)
		if creds, _, bErr = provider.resolveAuth(ctx, key); bErr != nil {
			return nil, bErr
		}
		return provider.doResponses(ctx, creds, request)
	}
	if err == nil && resp != nil {
		resp.BackfillParams(request)
	}
	return resp, err
}

// openResponsesStream opens one streaming /responses attempt with the given
// credentials.
func (provider *CodexProvider) openResponsesStream(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, postHookSpanFinalizer func(context.Context), creds *codexCredentials, request *schemas.BifrostResponsesRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	return openai.HandleOpenAIResponsesStreaming(
		ctx,
		provider.streamingClient,
		provider.networkConfig.BaseURL+providerUtils.GetPathFromContext(ctx, provider.responsesPath),
		request,
		authHeadersFor(creds),
		BuildHeaders(ctx, provider.networkConfig.ExtraHeaders),
		provider.networkConfig.StreamIdleTimeoutInSeconds,
		providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest),
		providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse),
		provider.GetProviderKey(),
		postHookRunner,
		nil,
		parseCodexError,
		withCodexRequestDefaults,
		nil,
		nil,
		provider.logger,
		postHookSpanFinalizer,
	)
}

// ResponsesStream performs a streaming responses request against Codex's
// /responses endpoint.
func (provider *CodexProvider) ResponsesStream(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, postHookSpanFinalizer func(context.Context), key schemas.Key, request *schemas.BifrostResponsesRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	if err := checkOperationAllowed(provider, schemas.ResponsesStreamRequest); err != nil {
		return nil, err
	}
	if request == nil {
		return nil, providerUtils.NewBifrostOperationError("responses stream request is nil", nil)
	}
	creds, isOAuth, bErr := provider.resolveAuth(ctx, key)
	if bErr != nil {
		return nil, bErr
	}
	stream, err := provider.openResponsesStream(ctx, postHookRunner, postHookSpanFinalizer, creds, request)
	if err != nil && isOAuth && isUnauthorized(err) {
		invalidateCredentials(&key)
		if creds, _, bErr = provider.resolveAuth(ctx, key); bErr != nil {
			return nil, bErr
		}
		return provider.openResponsesStream(ctx, postHookRunner, postHookSpanFinalizer, creds, request)
	}
	return stream, err
}

// doChatCompletion runs one chat completion attempt via the Responses
// endpoint: convert to Responses shape, serve, and convert back.
func (provider *CodexProvider) doChatCompletion(ctx *schemas.BifrostContext, creds *codexCredentials, request *schemas.BifrostChatRequest) (*schemas.BifrostChatResponse, *schemas.BifrostError) {
	responsesReq := request.ToResponsesRequest()
	if responsesReq == nil {
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrRequestBodyConversion, nil)
	}
	responsesResp, bErr := provider.doResponses(ctx, creds, responsesReq)
	if bErr != nil {
		return nil, bErr
	}
	chatResp := responsesResp.ToBifrostChatResponse()
	if chatResp == nil {
		return nil, providerUtils.NewBifrostOperationError("failed to convert responses response to chat response", nil)
	}
	chatResp.BackfillParams(request)
	return chatResp, nil
}

// ChatCompletion performs a chat completion request via Codex's /responses
// endpoint, converting through the Responses shape.
func (provider *CodexProvider) ChatCompletion(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostChatRequest) (*schemas.BifrostChatResponse, *schemas.BifrostError) {
	if err := checkOperationAllowed(provider, schemas.ChatCompletionRequest); err != nil {
		return nil, err
	}
	if request == nil {
		return nil, providerUtils.NewBifrostOperationError("chat completion request is nil", nil)
	}
	creds, isOAuth, bErr := provider.resolveAuth(ctx, key)
	if bErr != nil {
		return nil, bErr
	}
	resp, err := provider.doChatCompletion(ctx, creds, request)
	if err != nil && isOAuth && isUnauthorized(err) {
		invalidateCredentials(&key)
		if creds, _, bErr = provider.resolveAuth(ctx, key); bErr != nil {
			return nil, bErr
		}
		return provider.doChatCompletion(ctx, creds, request)
	}
	return resp, err
}

// ChatCompletionStream performs a streaming chat completion request via
// Codex's /responses endpoint. The Responses SSE stream is converted to chat
// chunks per event, so the caller still gets a chat stream.
func (provider *CodexProvider) ChatCompletionStream(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, postHookSpanFinalizer func(context.Context), key schemas.Key, request *schemas.BifrostChatRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	if err := checkOperationAllowed(provider, schemas.ChatCompletionStreamRequest); err != nil {
		return nil, err
	}
	if request == nil {
		return nil, providerUtils.NewBifrostOperationError("chat completion stream request is nil", nil)
	}
	responsesReq := request.ToResponsesRequest()
	if responsesReq == nil {
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrRequestBodyConversion, nil)
	}
	creds, isOAuth, bErr := provider.resolveAuth(ctx, key)
	if bErr != nil {
		return nil, bErr
	}
	stream, err := provider.openResponsesStream(ctx, wrapResponsesToChatStreamPostHookRunner(postHookRunner), postHookSpanFinalizer, creds, responsesReq)
	if err != nil && isOAuth && isUnauthorized(err) {
		invalidateCredentials(&key)
		if creds, _, bErr = provider.resolveAuth(ctx, key); bErr != nil {
			return nil, bErr
		}
		return provider.openResponsesStream(ctx, wrapResponsesToChatStreamPostHookRunner(postHookRunner), postHookSpanFinalizer, creds, responsesReq)
	}
	return stream, err
}
