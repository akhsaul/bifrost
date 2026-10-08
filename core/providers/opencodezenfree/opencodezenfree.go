// Package opencodezenfree implements the isolated Opencode Zen Free AI gateway provider.
// It forwards to https://opencode.ai/zen/v1 with the opencode CLI headers
// (authorization, x-opencode-client, b3/traceparent tracing, session affinity)
// and fills Responses API body defaults matching the working opencode capture.
package opencodezenfree

import (
	"context"
	"strings"
	"time"

	"github.com/maximhq/bifrost/core/providers/anthropic"
	"github.com/maximhq/bifrost/core/providers/openai"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

// opencodeZenFreeProvider implements the Provider interface for the anonymous Opencode Zen Free tier.
type opencodeZenFreeProvider struct {
	providerKey         schemas.ModelProvider
	logger              schemas.Logger
	client              *fasthttp.Client
	streamingClient     *fasthttp.Client
	networkConfig       schemas.NetworkConfig
	sendBackRawRequest  bool
	sendBackRawResponse bool
}

// NewOpencodeZenFreeProvider creates a new Opencode Zen Free provider instance.
func NewOpencodeZenFreeProvider(config *schemas.ProviderConfig, logger schemas.Logger) (*opencodeZenFreeProvider, error) {
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

	return &opencodeZenFreeProvider{
		providerKey:         schemas.OpencodeZenFree,
		logger:              logger,
		client:              client,
		streamingClient:     streamingClient,
		networkConfig:       config.NetworkConfig,
		sendBackRawRequest:  config.SendBackRawRequest,
		sendBackRawResponse: config.SendBackRawResponse,
	}, nil
}

// GetProviderKey returns the provider identifier.
func (p *opencodeZenFreeProvider) GetProviderKey() schemas.ModelProvider {
	return p.providerKey
}

// dummyFunctionTool builds one of the placeholder function tools from the
// working opencode capture (edit/read/shell). These deprecated dummies exist
// only to satisfy the upstream shape when the client sends no tools.
func dummyFunctionTool(name string) schemas.ResponsesTool {
	toolType := schemas.ResponsesToolTypeFunction
	return schemas.ResponsesTool{
		Type:        toolType,
		Name:        schemas.Ptr(name),
		Description: schemas.Ptr("deprecated function, don't use it!"),
		ResponsesToolFunction: &schemas.ResponsesToolFunction{
			Parameters: &schemas.ToolFunctionParameters{
				Type: "object",
				Properties: schemas.NewOrderedMapFromPairs(
					schemas.KV("deprecated", map[string]any{"type": "string"}),
				),
				Required:             []string{"deprecated"},
				AdditionalProperties: &schemas.AdditionalPropertiesStruct{AdditionalPropertiesBool: schemas.Ptr(false)},
			},
			Strict: schemas.Ptr(false),
		},
	}
}

// requiredOpencodeZenFreeToolNames are the placeholder tools the upstream expects.
// Merge is by name: a client tool with the same name counts as provided and is
// never overwritten.
var requiredOpencodeZenFreeToolNames = []string{"edit", "read", "shell"}

// ensureOpencodeZenFreeTools appends a dummy placeholder for each of
// edit/read/shell the client did not send. Client tools always win; only the
// missing names are filled in.
func ensureOpencodeZenFreeTools(params *schemas.ResponsesParameters) {
	if params == nil {
		return
	}
	present := make(map[string]bool, len(params.Tools))
	for _, tool := range params.Tools {
		if tool.Name != nil && *tool.Name != "" {
			present[*tool.Name] = true
		}
	}
	for _, name := range requiredOpencodeZenFreeToolNames {
		if !present[name] {
			params.Tools = append(params.Tools, dummyFunctionTool(name))
		}
	}
}

// isSystemPromptMessage reports whether a Responses input item is a plain
// system-prompt message (not a tool call, output, or reasoning item).
func isSystemPromptMessage(msg schemas.ResponsesMessage) bool {
	if msg.Role == nil || *msg.Role != schemas.ResponsesInputMessageRoleSystem {
		return false
	}
	if msg.Type != nil && *msg.Type != schemas.ResponsesMessageTypeMessage {
		return false
	}
	if msg.ResponsesToolMessage != nil || msg.ResponsesReasoning != nil {
		return false
	}
	return msg.Content != nil
}

// systemPromptText extracts the text of a system-prompt message, handling both
// plain string content and input/output text blocks.
func systemPromptText(msg schemas.ResponsesMessage) string {
	if msg.Content == nil {
		return ""
	}
	if msg.Content.ContentStr != nil {
		return strings.TrimSpace(*msg.Content.ContentStr)
	}
	var parts []string
	for _, block := range msg.Content.ContentBlocks {
		if block.Text == nil {
			continue
		}
		switch block.Type {
		case schemas.ResponsesInputMessageContentBlockTypeText,
			schemas.ResponsesOutputMessageContentTypeText:
			if text := strings.TrimSpace(*block.Text); text != "" {
				parts = append(parts, text)
			}
		}
	}
	return strings.Join(parts, "\n")
}

// isTitleGenerationResponsesRequest reports whether the responses request
// carries a session-title generation system prompt. Checks the top-level
// instructions (post-hoist) and any system-role input items not yet hoisted
// (when instructions were explicitly set, extractSystemPromptToInstructions
// leaves input items in place).
func isTitleGenerationResponsesRequest(request *schemas.BifrostResponsesRequest) bool {
	if request == nil {
		return false
	}
	if request.Params != nil && request.Params.Instructions != nil {
		if isTitleGenerationPrompt(*request.Params.Instructions) {
			return true
		}
	}
	for _, msg := range request.Input {
		if msg.Role == nil || (*msg.Role != schemas.ResponsesInputMessageRoleSystem && *msg.Role != schemas.ResponsesInputMessageRoleDeveloper) {
			continue
		}
		if isTitleGenerationPrompt(responsesMessageText(msg)) {
			return true
		}
	}
	return false
}

// responsesMessageText extracts the readable text of a Responses input
// message, handling both plain string content and text content blocks.
func responsesMessageText(msg schemas.ResponsesMessage) string {
	if msg.Content == nil {
		return ""
	}
	if msg.Content.ContentStr != nil {
		return *msg.Content.ContentStr
	}
	var parts []string
	for _, block := range msg.Content.ContentBlocks {
		if block.Text == nil {
			continue
		}
		switch block.Type {
		case schemas.ResponsesInputMessageContentBlockTypeText,
			schemas.ResponsesOutputMessageContentTypeText:
			if text := strings.TrimSpace(*block.Text); text != "" {
				parts = append(parts, text)
			}
		}
	}
	return strings.Join(parts, "\n")
}

// extractSystemPromptToInstructions moves system-prompt messages from input
// into the top-level instructions field the upstream expects. It runs only
// when the client left instructions unset, so explicit instructions are never
// overwritten. Idempotent: a second pass finds no system messages left.
func extractSystemPromptToInstructions(request *schemas.BifrostResponsesRequest) {
	if request == nil || request.Params == nil {
		return
	}
	if request.Params.Instructions != nil && *request.Params.Instructions != "" {
		return
	}
	if len(request.Input) == 0 {
		return
	}
	var texts []string
	kept := make([]schemas.ResponsesMessage, 0, len(request.Input))
	for _, msg := range request.Input {
		if !isSystemPromptMessage(msg) {
			kept = append(kept, msg)
			continue
		}
		if text := systemPromptText(msg); text != "" {
			texts = append(texts, text)
		}
	}
	if len(texts) == 0 {
		return
	}
	request.Params.Instructions = schemas.Ptr(strings.Join(texts, "\n"))
	request.Input = kept
}

// ensureOpencodeZenFreeDefaults fills Responses API body fields to match the working
// opencode capture. Client-supplied values always win; defaults apply only to
// fields the client left unset.
func ensureOpencodeZenFreeDefaults(request *schemas.BifrostResponsesRequest, sessionID string) {
	if request == nil {
		return
	}
	if request.Params == nil {
		request.Params = &schemas.ResponsesParameters{}
	}
	if request.Params.Reasoning == nil {
		request.Params.Reasoning = &schemas.ResponsesParametersReasoning{
			Effort:  schemas.Ptr(DefaultReasoningEffort),
			Summary: schemas.Ptr(DefaultReasoningSummary),
		}
	} else {
		if request.Params.Reasoning.Effort == nil || *request.Params.Reasoning.Effort == "" {
			request.Params.Reasoning.Effort = schemas.Ptr(DefaultReasoningEffort)
		}
		if request.Params.Reasoning.Summary == nil || *request.Params.Reasoning.Summary == "" {
			request.Params.Reasoning.Summary = schemas.Ptr(DefaultReasoningSummary)
		}
	}
	if request.Params.Store == nil {
		request.Params.Store = schemas.Ptr(false)
	}
	// System prompts arrive as system-role input messages on the chat path
	// (ToResponsesRequest has no instructions equivalent); hoist them into
	// instructions before the default below would bury them under a generic one.
	extractSystemPromptToInstructions(request)
	if request.Params.Instructions == nil || *request.Params.Instructions == "" {
		request.Params.Instructions = schemas.Ptr(DefaultInstructions)
	}
	if request.Params.PromptCacheKey == nil || *request.Params.PromptCacheKey == "" {
		if sessionID != "" {
			request.Params.PromptCacheKey = schemas.Ptr(sessionID)
		} else {
			request.Params.PromptCacheKey = schemas.Ptr(DefaultPromptCacheKeyFallback)
		}
	}
	// Ask the upstream to return encrypted_content on reasoning items (as the
	// working direct capture does), so later turns can replay genuine
	// server-issued state instead of dangling references. Client-supplied
	// include values are preserved; the value is only appended when missing.
	hasReasoningInclude := false
	for _, inc := range request.Params.Include {
		if inc == opencodeIncludeReasoningEncryptedContent {
			hasReasoningInclude = true
			break
		}
	}
	if !hasReasoningInclude {
		request.Params.Include = append(request.Params.Include, opencodeIncludeReasoningEncryptedContent)
	}
	// Session-title generation prompts are plain single prompts with no
	// tools; attaching the dummy edit/read/shell placeholders breaks them.
	if !isTitleGenerationResponsesRequest(request) {
		ensureOpencodeZenFreeTools(request.Params)
	}
}

// ListModels performs a list models request to the Opencode Zen Free API.
func (p *opencodeZenFreeProvider) ListModels(ctx *schemas.BifrostContext, keys []schemas.Key, request *schemas.BifrostListModelsRequest) (*schemas.BifrostListModelsResponse, *schemas.BifrostError) {
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

// TextCompletion is not supported by OpencodeZenFree.
func (p *opencodeZenFreeProvider) TextCompletion(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostTextCompletionRequest) (*schemas.BifrostTextCompletionResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.TextCompletionRequest, p.GetProviderKey())
}

// TextCompletionStream is not supported by OpencodeZenFree.
func (p *opencodeZenFreeProvider) TextCompletionStream(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, postHookSpanFinalizer func(context.Context), key schemas.Key, request *schemas.BifrostTextCompletionRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
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

// ChatCompletion performs a chat completion request, routing each model to
// its required upstream endpoint: Gemini-kind models to the Google
// Generative Language path, qwen-kind models to /v1/messages (Anthropic
// wire format), space-bunny-kind models to /v1/chat/completions, and
// everything else to /v1/responses. Unary requests are served via stream
// fan-in (the free tier requires stream:true); on unsupported-format
// failures the chain chat -> responses -> messages is walked.
func (p *opencodeZenFreeProvider) ChatCompletion(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostChatRequest) (*schemas.BifrostChatResponse, *schemas.BifrostError) {
	if request == nil {
		return nil, &schemas.BifrostError{
			IsBifrostError: true,
			Error: &schemas.ErrorField{
				Message: "chat completion request is nil",
			},
		}
	}
	// Resolve once: the generated session ID feeds both prompt_cache_key and
	// the session headers. A second resolution would generate a DIFFERENT
	// random session for the headers (counter advanced), breaking the invariant.
	resolved := ResolveOpencodeHeaders(ctx, p.networkConfig.ExtraHeaders)
	ensureOpencodeZenFreeChatDefaults(request)
	extraHeaders := headersFromResolved(ctx, p.networkConfig.ExtraHeaders, resolved, false)

	primary := classifyOpencodeZenFreeModel(ctx, request.Model)
	if primary == opencodeZenFreeEndpointGemini {
		return p.doGeminiChatUnary(ctx, request, geminiHeaders(extraHeaders))
	}
	var lastErr *schemas.BifrostError
	for _, endpoint := range opencodeZenFreeFallbackChain(primary) {
		var (
			chatResp *schemas.BifrostChatResponse
			bErr     *schemas.BifrostError
		)
		switch endpoint {
		case opencodeZenFreeEndpointChat:
			chatResp, bErr = p.doChatUnary(ctx, request, extraHeaders)
		case opencodeZenFreeEndpointMessages:
			chatResp, bErr = p.doMessagesChatUnary(ctx, request, extraHeaders)
		default:
			responsesResp, rErr := p.doResponsesUnaryViaChat(ctx, request, resolved.SessionID, extraHeaders)
			bErr = rErr
			if rErr == nil && responsesResp != nil {
				chatResp = responsesResp.ToBifrostChatResponse()
				if chatResp == nil {
					bErr = &schemas.BifrostError{
						IsBifrostError: true,
						Error: &schemas.ErrorField{
							Message: "failed to convert responses response to chat response",
						},
					}
				}
			}
		}
		if bErr == nil {
			if chatResp != nil {
				chatResp.BackfillParams(request)
			}
			return chatResp, nil
		}
		if !isUnsupportedFormatError(bErr) {
			return nil, bErr
		}
		lastErr = bErr
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, &schemas.BifrostError{
		IsBifrostError: true,
		Error: &schemas.ErrorField{
			Message: "chat completion request failed on all endpoints",
		},
	}
}

// doResponsesUnaryViaChat serves the responses endpoint for a chat-shaped
// request: convert to Responses shape, apply Responses defaults, and run the
// unary responses attempt.
func (p *opencodeZenFreeProvider) doResponsesUnaryViaChat(ctx *schemas.BifrostContext, request *schemas.BifrostChatRequest, sessionID string, extraHeaders map[string]string) (*schemas.BifrostResponsesResponse, *schemas.BifrostError) {
	responsesReq := request.ToResponsesRequest()
	if responsesReq == nil {
		return nil, &schemas.BifrostError{
			IsBifrostError: true,
			Error: &schemas.ErrorField{
				Message: "failed to convert chat request to responses request",
			},
		}
	}
	ensureOpencodeZenFreeDefaults(responsesReq, sessionID)
	return p.doResponsesUnary(ctx, responsesReq, extraHeaders)
}

// ChatCompletionStream performs a streaming chat completion request,
// routing each model to its required upstream endpoint. Streaming paths
// pass through untouched (no stream forcing, no accumulation); endpoint
// fallback applies only to pre-first-chunk errors.
func (p *opencodeZenFreeProvider) ChatCompletionStream(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, postHookSpanFinalizer func(context.Context), key schemas.Key, request *schemas.BifrostChatRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	if request == nil {
		return nil, &schemas.BifrostError{
			IsBifrostError: true,
			Error: &schemas.ErrorField{
				Message: "chat completion stream request is nil",
			},
		}
	}
	resolved := ResolveOpencodeHeaders(ctx, p.networkConfig.ExtraHeaders)
	ensureOpencodeZenFreeChatDefaults(request)
	extraHeaders := headersFromResolved(ctx, p.networkConfig.ExtraHeaders, resolved, true)

	primary := classifyOpencodeZenFreeModel(ctx, request.Model)
	if primary == opencodeZenFreeEndpointGemini {
		return p.geminiChatStream(ctx, postHookRunner, postHookSpanFinalizer, request, geminiHeaders(extraHeaders))
	}
	chatPostHookRunner := wrapResponsesToChatStreamPostHookRunner(postHookRunner)
	var lastErr *schemas.BifrostError
	for _, endpoint := range opencodeZenFreeFallbackChain(primary) {
		switch endpoint {
		case opencodeZenFreeEndpointChat:
			stream, bErr := openai.HandleOpenAIChatCompletionStreaming(
				ctx,
				p.streamingClient,
				p.opencodeChatURL(ctx),
				request,
				nil,
				extraHeaders,
				p.networkConfig.StreamIdleTimeoutInSeconds,
				providerUtils.ShouldSendBackRawRequest(ctx, p.sendBackRawRequest),
				providerUtils.ShouldSendBackRawResponse(ctx, p.sendBackRawResponse),
				p.providerKey,
				postHookRunner,
				nil,
				nil,
				nil,
				nil,
				nil,
				nil,
				p.logger,
				postHookSpanFinalizer,
			)
			if bErr == nil {
				return stream, nil
			}
			if !isUnsupportedFormatError(bErr) {
				return nil, bErr
			}
			lastErr = bErr
		case opencodeZenFreeEndpointMessages:
			jsonBody, bErr := anthropic.BuildAnthropicChatRequestBody(ctx, request, anthropic.AnthropicRequestBuildConfig{
				Provider:                  schemas.OpencodeZenFree,
				IsStreaming:               true,
				ShouldSendBackRawRequest:  providerUtils.ShouldSendBackRawRequest(ctx, p.sendBackRawRequest),
				ShouldSendBackRawResponse: providerUtils.ShouldSendBackRawResponse(ctx, p.sendBackRawResponse),
			})
			if bErr != nil {
				return nil, bErr
			}
			stream, bErr := anthropic.HandleAnthropicChatCompletionStreaming(
				ctx,
				p.streamingClient,
				p.opencodeMessagesURL(ctx),
				jsonBody,
				anthropicHeaders(nil),
				extraHeaders,
				p.networkConfig.StreamIdleTimeoutInSeconds,
				p.networkConfig.BetaHeaderOverrides,
				providerUtils.ShouldSendBackRawRequest(ctx, p.sendBackRawRequest),
				providerUtils.ShouldSendBackRawResponse(ctx, p.sendBackRawResponse),
				p.providerKey,
				postHookRunner,
				nil,
				nil,
				p.logger,
				postHookSpanFinalizer,
			)
			if bErr == nil {
				return stream, nil
			}
			if !isUnsupportedFormatError(bErr) {
				return nil, bErr
			}
			lastErr = bErr
		default:
			responsesReq := request.ToResponsesRequest()
			if responsesReq == nil {
				return nil, &schemas.BifrostError{
					IsBifrostError: true,
					Error: &schemas.ErrorField{
						Message: "failed to convert chat request to responses request",
					},
				}
			}
			ensureOpencodeZenFreeDefaults(responsesReq, resolved.SessionID)
			stream, bErr := p.openResponsesStream(ctx, chatPostHookRunner, postHookSpanFinalizer, responsesReq, extraHeaders)
			if bErr == nil {
				return stream, nil
			}
			if !isUnsupportedFormatError(bErr) {
				return nil, bErr
			}
			lastErr = bErr
		}
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, &schemas.BifrostError{
		IsBifrostError: true,
		Error: &schemas.ErrorField{
			Message: "chat completion stream request failed on all endpoints",
		},
	}
}

// preserveOpencodeReasoning restores client-supplied effort/summary verbatim on
// the wire request. The shared openai converter normalizes effort through
// datasheet caps that know nothing about opencode-native models, downgrading
// "xhigh"→"high" and rewriting "auto"; opencode accepts both, so they must
// pass through untouched.
// opencodeIncludeReasoningEncryptedContent is the include value the working
// direct capture sends so responses carry encrypted_content for later replay.
const opencodeIncludeReasoningEncryptedContent = "reasoning.encrypted_content"

// ensureOpencodeReasoningItemSummaries converts reasoning_text content on
// reasoning input items into summary_text entries, which is what the upstream
// requires. Items decoded without summary/encrypted_content keys (e.g. harness
// replays carrying only reasoning_text content, like opencode-req-bug.json
// input[2]) would otherwise fail with
// "`input[N]` missing required field `summary`".
//
// The reasoning_text blocks (and plain string content) are removed after
// conversion, so the text lives only in summary. Blocks of any other type are
// kept; when nothing remains, content is dropped entirely. Existing non-empty
// summaries are left untouched. Operates on the wire copy only, never the
// caller's request.
func ensureOpencodeReasoningItemSummaries(wireReq *openai.OpenAIResponsesRequest) *openai.OpenAIResponsesRequest {
	if wireReq == nil {
		return wireReq
	}
	items := wireReq.Input.OpenAIResponsesRequestInputArray
	for i := range items {
		msg := &items[i]
		if msg.Type == nil || *msg.Type != schemas.ResponsesMessageTypeReasoning {
			continue
		}
		var encrypted *string
		if msg.ResponsesReasoning != nil {
			if len(msg.ResponsesReasoning.Summary) > 0 {
				continue
			}
			encrypted = msg.ResponsesReasoning.EncryptedContent
		}
		summary := make([]schemas.ResponsesReasoningSummary, 0, 1)
		var kept []schemas.ResponsesMessageContentBlock
		if msg.Content != nil {
			if msg.Content.ContentStr != nil {
				if text := strings.TrimSpace(*msg.Content.ContentStr); text != "" {
					summary = append(summary, schemas.ResponsesReasoningSummary{
						Type: schemas.ResponsesReasoningContentBlockTypeSummaryText,
						Text: text,
					})
				}
			}
			for _, block := range msg.Content.ContentBlocks {
				if block.Type == schemas.ResponsesOutputMessageContentTypeReasoning {
					if block.Text != nil {
						if text := strings.TrimSpace(*block.Text); text != "" {
							summary = append(summary, schemas.ResponsesReasoningSummary{
								Type: schemas.ResponsesReasoningContentBlockTypeSummaryText,
								Text: text,
							})
						}
					}
					continue
				}
				kept = append(kept, block)
			}
		}
		msg.ResponsesReasoning = &schemas.ResponsesReasoning{
			Summary:          summary,
			EncryptedContent: encrypted,
		}
		if len(kept) == 0 {
			msg.Content = nil
		} else {
			msg.Content = &schemas.ResponsesMessageContent{ContentBlocks: kept}
		}
	}
	return wireReq
}

// normalizeOpencodeReasoningItems repairs replayed reasoning items on the wire
// copy so the upstream accepts them:
//
//  1. The shared openai converter strips encrypted_content for models the
//     datasheet doesn't know (like muse-spark), but the upstream needs the blob
//     to resume reasoning. Restore it from the caller's original input,
//     matched by item id.
//  2. An id without encrypted_content is a dangling server-state reference:
//     Bifrost-minted ids (rs_<50hex> from chat→responses conversion) or stale
//     foreign ids make the upstream fail with "Referenced reasoning item ...
//     was not found or has expired". Strip the id and keep the summary text as
//     plain context instead.
//
// Items carrying encrypted_content keep their id untouched. Operates on the
// wire copy only, never the caller's request.
func normalizeOpencodeReasoningItems(wireReq *openai.OpenAIResponsesRequest, request *schemas.BifrostResponsesRequest) *openai.OpenAIResponsesRequest {
	if wireReq == nil {
		return wireReq
	}
	var encryptedByID map[string]*string
	if request != nil {
		for _, msg := range request.Input {
			if msg.ID == nil || *msg.ID == "" || msg.ResponsesReasoning == nil || msg.ResponsesReasoning.EncryptedContent == nil {
				continue
			}
			if encryptedByID == nil {
				encryptedByID = make(map[string]*string)
			}
			encryptedByID[*msg.ID] = msg.ResponsesReasoning.EncryptedContent
		}
	}
	items := wireReq.Input.OpenAIResponsesRequestInputArray
	for i := range items {
		msg := &items[i]
		if msg.Type == nil || *msg.Type != schemas.ResponsesMessageTypeReasoning {
			continue
		}
		if msg.ResponsesReasoning == nil {
			msg.ResponsesReasoning = &schemas.ResponsesReasoning{
				Summary: []schemas.ResponsesReasoningSummary{},
			}
		}
		if msg.ResponsesReasoning.EncryptedContent == nil && msg.ID != nil && *msg.ID != "" {
			if enc, ok := encryptedByID[*msg.ID]; ok && enc != nil {
				msg.ResponsesReasoning.EncryptedContent = enc
			}
		}
		if msg.ResponsesReasoning.EncryptedContent == nil {
			msg.ID = nil
		}
	}
	return wireReq
}

func preserveOpencodeReasoning(wireReq *openai.OpenAIResponsesRequest, request *schemas.BifrostResponsesRequest) *openai.OpenAIResponsesRequest {
	if wireReq == nil || request == nil || request.Params == nil || request.Params.Reasoning == nil {
		return wireReq
	}
	if wireReq.Reasoning == nil {
		wireReq.Reasoning = &schemas.ResponsesParametersReasoning{}
	}
	wireReq.Reasoning.Effort = request.Params.Reasoning.Effort
	wireReq.Reasoning.Summary = request.Params.Reasoning.Summary
	return wireReq
}

// Responses performs a responses request, routing each model to its
// required upstream endpoint with the same classification as the chat path.
// Non-responses endpoints are served by converting to the target shape;
// unary requests use stream fan-in where the wire supports it.
func (p *opencodeZenFreeProvider) Responses(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostResponsesRequest) (*schemas.BifrostResponsesResponse, *schemas.BifrostError) {
	// Resolve once: the generated session ID feeds both prompt_cache_key and
	// the session headers. A second resolution would generate a DIFFERENT
	// random session for the headers (counter advanced), breaking the invariant.
	resolved := ResolveOpencodeHeaders(ctx, p.networkConfig.ExtraHeaders)
	ensureOpencodeZenFreeDefaults(request, resolved.SessionID)
	extraHeaders := headersFromResolved(ctx, p.networkConfig.ExtraHeaders, resolved, false)

	primary := classifyOpencodeZenFreeModel(ctx, request.Model)
	if primary == opencodeZenFreeEndpointGemini {
		return p.doGeminiResponsesUnary(ctx, request, geminiHeaders(extraHeaders))
	}
	var lastErr *schemas.BifrostError
	for _, endpoint := range opencodeZenFreeFallbackChain(primary) {
		var (
			responsesResp *schemas.BifrostResponsesResponse
			bErr          *schemas.BifrostError
		)
		switch endpoint {
		case opencodeZenFreeEndpointChat:
			chatReq := request.ToChatRequest()
			ensureOpencodeZenFreeChatDefaults(chatReq)
			var chatResp *schemas.BifrostChatResponse
			chatResp, bErr = p.doChatUnary(ctx, chatReq, extraHeaders)
			if bErr == nil {
				if chatResp == nil {
					bErr = &schemas.BifrostError{
						IsBifrostError: true,
						Error: &schemas.ErrorField{
							Message: "failed to convert chat response to responses response",
						},
					}
				} else {
					responsesResp = chatResp.ToBifrostResponsesResponse()
				}
			}
		case opencodeZenFreeEndpointMessages:
			responsesResp, bErr = p.doMessagesResponsesUnary(ctx, request, extraHeaders)
		default:
			responsesResp, bErr = p.doResponsesUnary(ctx, request, extraHeaders)
		}
		if bErr == nil {
			return responsesResp, nil
		}
		if !isUnsupportedFormatError(bErr) {
			return nil, bErr
		}
		lastErr = bErr
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, &schemas.BifrostError{
		IsBifrostError: true,
		Error: &schemas.ErrorField{
			Message: "responses request failed on all endpoints",
		},
	}
}

// openResponsesStream opens one streaming /v1/responses attempt with the
// opencode converter. Streaming paths pass through untouched.
func (p *opencodeZenFreeProvider) openResponsesStream(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, postHookSpanFinalizer func(context.Context), request *schemas.BifrostResponsesRequest, extraHeaders map[string]string) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	postRequestConverter := func(wireReq *openai.OpenAIResponsesRequest) *openai.OpenAIResponsesRequest {
		wireReq = preserveOpencodeReasoning(wireReq, request)
		wireReq = ensureOpencodeReasoningItemSummaries(wireReq)
		return normalizeOpencodeReasoningItems(wireReq, request)
	}
	return openai.HandleOpenAIResponsesStreaming(
		ctx,
		p.streamingClient,
		p.opencodeResponsesURL(ctx),
		request,
		nil,
		extraHeaders,
		p.networkConfig.StreamIdleTimeoutInSeconds,
		providerUtils.ShouldSendBackRawRequest(ctx, p.sendBackRawRequest),
		providerUtils.ShouldSendBackRawResponse(ctx, p.sendBackRawResponse),
		p.providerKey,
		postHookRunner,
		nil,
		parseOpencodeZenFreeError,
		postRequestConverter,
		nil,
		nil,
		p.logger,
		postHookSpanFinalizer,
	)
}

// ResponsesStream performs a streaming responses request, routing each model
// to its required upstream endpoint. Streaming paths pass through untouched;
// endpoint fallback applies only to pre-first-chunk errors.
func (p *opencodeZenFreeProvider) ResponsesStream(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, postHookSpanFinalizer func(context.Context), key schemas.Key, request *schemas.BifrostResponsesRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	// Same single-resolution invariant as Responses: headers must carry the
	// exact session ID used for prompt_cache_key.
	resolved := ResolveOpencodeHeaders(ctx, p.networkConfig.ExtraHeaders)
	ensureOpencodeZenFreeDefaults(request, resolved.SessionID)
	extraHeaders := headersFromResolved(ctx, p.networkConfig.ExtraHeaders, resolved, true)

	primary := classifyOpencodeZenFreeModel(ctx, request.Model)
	if primary == opencodeZenFreeEndpointGemini {
		return p.geminiResponsesStream(ctx, postHookRunner, postHookSpanFinalizer, request, geminiHeaders(extraHeaders))
	}
	var lastErr *schemas.BifrostError
	for _, endpoint := range opencodeZenFreeFallbackChain(primary) {
		switch endpoint {
		case opencodeZenFreeEndpointChat:
			chatReq := request.ToChatRequest()
			ensureOpencodeZenFreeChatDefaults(chatReq)
			stream, bErr := openai.HandleOpenAIChatCompletionStreaming(
				ctx,
				p.streamingClient,
				p.opencodeChatURL(ctx),
				chatReq,
				nil,
				extraHeaders,
				p.networkConfig.StreamIdleTimeoutInSeconds,
				providerUtils.ShouldSendBackRawRequest(ctx, p.sendBackRawRequest),
				providerUtils.ShouldSendBackRawResponse(ctx, p.sendBackRawResponse),
				p.providerKey,
				postHookRunner,
				nil,
				nil,
				nil,
				nil,
				nil,
				nil,
				p.logger,
				postHookSpanFinalizer,
			)
			if bErr == nil {
				return stream, nil
			}
			if !isUnsupportedFormatError(bErr) {
				return nil, bErr
			}
			lastErr = bErr
		case opencodeZenFreeEndpointMessages:
			jsonBody, bErr := anthropic.BuildAnthropicResponsesRequestBody(ctx, request, anthropic.AnthropicRequestBuildConfig{
				Provider:                  schemas.OpencodeZenFree,
				IsStreaming:               true,
				ShouldSendBackRawRequest:  providerUtils.ShouldSendBackRawRequest(ctx, p.sendBackRawRequest),
				ShouldSendBackRawResponse: providerUtils.ShouldSendBackRawResponse(ctx, p.sendBackRawResponse),
			})
			if bErr != nil {
				return nil, bErr
			}
			stream, bErr := anthropic.HandleAnthropicResponsesStream(
				ctx,
				p.streamingClient,
				p.opencodeMessagesURL(ctx),
				jsonBody,
				anthropicHeaders(nil),
				extraHeaders,
				p.networkConfig.StreamIdleTimeoutInSeconds,
				p.networkConfig.BetaHeaderOverrides,
				providerUtils.ShouldSendBackRawRequest(ctx, p.sendBackRawRequest),
				providerUtils.ShouldSendBackRawResponse(ctx, p.sendBackRawResponse),
				p.providerKey,
				postHookRunner,
				nil,
				nil,
				p.logger,
				postHookSpanFinalizer,
			)
			if bErr == nil {
				return stream, nil
			}
			if !isUnsupportedFormatError(bErr) {
				return nil, bErr
			}
			lastErr = bErr
		default:
			stream, bErr := p.openResponsesStream(ctx, postHookRunner, postHookSpanFinalizer, request, extraHeaders)
			if bErr == nil {
				return stream, nil
			}
			if !isUnsupportedFormatError(bErr) {
				return nil, bErr
			}
			lastErr = bErr
		}
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, &schemas.BifrostError{
		IsBifrostError: true,
		Error: &schemas.ErrorField{
			Message: "responses stream request failed on all endpoints",
		},
	}
}

// Embedding is not supported by OpencodeZenFree.
func (p *opencodeZenFreeProvider) Embedding(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostEmbeddingRequest) (*schemas.BifrostEmbeddingResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.EmbeddingRequest, p.GetProviderKey())
}

// Rerank is not supported by OpencodeZenFree.
func (p *opencodeZenFreeProvider) Rerank(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostRerankRequest) (*schemas.BifrostRerankResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.RerankRequest, p.GetProviderKey())
}

// Decision is not supported by OpencodeZenFree.
func (p *opencodeZenFreeProvider) Decision(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostDecisionRequest) (*schemas.BifrostDecisionResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.DecisionRequest, p.GetProviderKey())
}

// OCR is not supported by OpencodeZenFree.
func (p *opencodeZenFreeProvider) OCR(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostOCRRequest) (*schemas.BifrostOCRResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.OCRRequest, p.GetProviderKey())
}

// Speech is not supported by OpencodeZenFree.
func (p *opencodeZenFreeProvider) Speech(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostSpeechRequest) (*schemas.BifrostSpeechResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.SpeechRequest, p.GetProviderKey())
}

// SpeechStream is not supported by OpencodeZenFree.
func (p *opencodeZenFreeProvider) SpeechStream(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, postHookSpanFinalizer func(context.Context), key schemas.Key, request *schemas.BifrostSpeechRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.SpeechStreamRequest, p.GetProviderKey())
}

// Transcription is not supported by OpencodeZenFree.
func (p *opencodeZenFreeProvider) Transcription(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostTranscriptionRequest) (*schemas.BifrostTranscriptionResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.TranscriptionRequest, p.GetProviderKey())
}

// TranscriptionStream is not supported by OpencodeZenFree.
func (p *opencodeZenFreeProvider) TranscriptionStream(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, postHookSpanFinalizer func(context.Context), key schemas.Key, request *schemas.BifrostTranscriptionRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.TranscriptionStreamRequest, p.GetProviderKey())
}

// ImageGeneration is not supported by OpencodeZenFree.
func (p *opencodeZenFreeProvider) ImageGeneration(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostImageGenerationRequest) (*schemas.BifrostImageGenerationResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ImageGenerationRequest, p.GetProviderKey())
}

// ImageGenerationStream is not supported by OpencodeZenFree.
func (p *opencodeZenFreeProvider) ImageGenerationStream(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, postHookSpanFinalizer func(context.Context), key schemas.Key, request *schemas.BifrostImageGenerationRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ImageGenerationStreamRequest, p.GetProviderKey())
}

// ImageEdit is not supported by OpencodeZenFree.
func (p *opencodeZenFreeProvider) ImageEdit(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostImageEditRequest) (*schemas.BifrostImageGenerationResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ImageEditRequest, p.GetProviderKey())
}

// ImageEditStream is not supported by OpencodeZenFree.
func (p *opencodeZenFreeProvider) ImageEditStream(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, postHookSpanFinalizer func(context.Context), key schemas.Key, request *schemas.BifrostImageEditRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ImageEditStreamRequest, p.GetProviderKey())
}

// ImageVariation is not supported by OpencodeZenFree.
func (p *opencodeZenFreeProvider) ImageVariation(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostImageVariationRequest) (*schemas.BifrostImageGenerationResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ImageVariationRequest, p.GetProviderKey())
}

// VideoGeneration is not supported by OpencodeZenFree.
func (p *opencodeZenFreeProvider) VideoGeneration(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostVideoGenerationRequest) (*schemas.BifrostVideoGenerationResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.VideoGenerationRequest, p.GetProviderKey())
}

// VideoRetrieve is not supported by OpencodeZenFree.
func (p *opencodeZenFreeProvider) VideoRetrieve(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostVideoRetrieveRequest) (*schemas.BifrostVideoGenerationResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.VideoRetrieveRequest, p.GetProviderKey())
}

// VideoDownload is not supported by OpencodeZenFree.
func (p *opencodeZenFreeProvider) VideoDownload(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostVideoDownloadRequest) (*schemas.BifrostVideoDownloadResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.VideoDownloadRequest, p.GetProviderKey())
}

// VideoDelete is not supported by OpencodeZenFree.
func (p *opencodeZenFreeProvider) VideoDelete(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostVideoDeleteRequest) (*schemas.BifrostVideoDeleteResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.VideoDeleteRequest, p.GetProviderKey())
}

// VideoList is not supported by OpencodeZenFree.
func (p *opencodeZenFreeProvider) VideoList(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostVideoListRequest) (*schemas.BifrostVideoListResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.VideoListRequest, p.GetProviderKey())
}

// VideoEdit is not supported by OpencodeZenFree.
func (p *opencodeZenFreeProvider) VideoEdit(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostVideoEditRequest) (*schemas.BifrostVideoEditResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.VideoEditRequest, p.GetProviderKey())
}

// VideoRemix is not supported by OpencodeZenFree.
func (p *opencodeZenFreeProvider) VideoRemix(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostVideoRemixRequest) (*schemas.BifrostVideoGenerationResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.VideoRemixRequest, p.GetProviderKey())
}

// BatchCreate is not supported by OpencodeZenFree.
func (p *opencodeZenFreeProvider) BatchCreate(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostBatchCreateRequest) (*schemas.BifrostBatchCreateResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.BatchCreateRequest, p.GetProviderKey())
}

// BatchList is not supported by OpencodeZenFree.
func (p *opencodeZenFreeProvider) BatchList(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostBatchListRequest) (*schemas.BifrostBatchListResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.BatchListRequest, p.GetProviderKey())
}

// BatchRetrieve is not supported by OpencodeZenFree.
func (p *opencodeZenFreeProvider) BatchRetrieve(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostBatchRetrieveRequest) (*schemas.BifrostBatchRetrieveResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.BatchRetrieveRequest, p.GetProviderKey())
}

// BatchCancel is not supported by OpencodeZenFree.
func (p *opencodeZenFreeProvider) BatchCancel(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostBatchCancelRequest) (*schemas.BifrostBatchCancelResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.BatchCancelRequest, p.GetProviderKey())
}

// BatchDelete is not supported by OpencodeZenFree.
func (p *opencodeZenFreeProvider) BatchDelete(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostBatchDeleteRequest) (*schemas.BifrostBatchDeleteResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.BatchDeleteRequest, p.GetProviderKey())
}

// BatchResults is not supported by OpencodeZenFree.
func (p *opencodeZenFreeProvider) BatchResults(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostBatchResultsRequest) (*schemas.BifrostBatchResultsResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.BatchResultsRequest, p.GetProviderKey())
}

// FileUpload is not supported by OpencodeZenFree.
func (p *opencodeZenFreeProvider) FileUpload(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostFileUploadRequest) (*schemas.BifrostFileUploadResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.FileUploadRequest, p.GetProviderKey())
}

// FileList is not supported by OpencodeZenFree.
func (p *opencodeZenFreeProvider) FileList(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostFileListRequest) (*schemas.BifrostFileListResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.FileListRequest, p.GetProviderKey())
}

// FileRetrieve is not supported by OpencodeZenFree.
func (p *opencodeZenFreeProvider) FileRetrieve(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostFileRetrieveRequest) (*schemas.BifrostFileRetrieveResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.FileRetrieveRequest, p.GetProviderKey())
}

// FileDelete is not supported by OpencodeZenFree.
func (p *opencodeZenFreeProvider) FileDelete(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostFileDeleteRequest) (*schemas.BifrostFileDeleteResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.FileDeleteRequest, p.GetProviderKey())
}

// FileContent is not supported by OpencodeZenFree.
func (p *opencodeZenFreeProvider) FileContent(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostFileContentRequest) (*schemas.BifrostFileContentResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.FileContentRequest, p.GetProviderKey())
}

// CountTokens is not supported by OpencodeZenFree.
func (p *opencodeZenFreeProvider) CountTokens(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostResponsesRequest) (*schemas.BifrostCountTokensResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.CountTokensRequest, p.GetProviderKey())
}

// Compaction is not supported by OpencodeZenFree.
func (p *opencodeZenFreeProvider) Compaction(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostCompactionRequest) (*schemas.BifrostCompactionResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.CompactionRequest, p.GetProviderKey())
}

// ContainerCreate is not supported by OpencodeZenFree.
func (p *opencodeZenFreeProvider) ContainerCreate(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostContainerCreateRequest) (*schemas.BifrostContainerCreateResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerCreateRequest, p.GetProviderKey())
}

// ContainerList is not supported by OpencodeZenFree.
func (p *opencodeZenFreeProvider) ContainerList(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostContainerListRequest) (*schemas.BifrostContainerListResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerListRequest, p.GetProviderKey())
}

// ContainerRetrieve is not supported by OpencodeZenFree.
func (p *opencodeZenFreeProvider) ContainerRetrieve(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostContainerRetrieveRequest) (*schemas.BifrostContainerRetrieveResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerRetrieveRequest, p.GetProviderKey())
}

// ContainerDelete is not supported by OpencodeZenFree.
func (p *opencodeZenFreeProvider) ContainerDelete(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostContainerDeleteRequest) (*schemas.BifrostContainerDeleteResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerDeleteRequest, p.GetProviderKey())
}

// ContainerFileCreate is not supported by OpencodeZenFree.
func (p *opencodeZenFreeProvider) ContainerFileCreate(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostContainerFileCreateRequest) (*schemas.BifrostContainerFileCreateResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerFileCreateRequest, p.GetProviderKey())
}

// ContainerFileList is not supported by OpencodeZenFree.
func (p *opencodeZenFreeProvider) ContainerFileList(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostContainerFileListRequest) (*schemas.BifrostContainerFileListResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerFileListRequest, p.GetProviderKey())
}

// ContainerFileRetrieve is not supported by OpencodeZenFree.
func (p *opencodeZenFreeProvider) ContainerFileRetrieve(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostContainerFileRetrieveRequest) (*schemas.BifrostContainerFileRetrieveResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerFileRetrieveRequest, p.GetProviderKey())
}

// ContainerFileContent is not supported by OpencodeZenFree.
func (p *opencodeZenFreeProvider) ContainerFileContent(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostContainerFileContentRequest) (*schemas.BifrostContainerFileContentResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerFileContentRequest, p.GetProviderKey())
}

// ContainerFileDelete is not supported by OpencodeZenFree.
func (p *opencodeZenFreeProvider) ContainerFileDelete(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostContainerFileDeleteRequest) (*schemas.BifrostContainerFileDeleteResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerFileDeleteRequest, p.GetProviderKey())
}

// Passthrough is not supported by OpencodeZenFree.
func (p *opencodeZenFreeProvider) Passthrough(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostPassthroughRequest) (*schemas.BifrostPassthroughResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.PassthroughRequest, p.GetProviderKey())
}

// PassthroughStream is not supported by OpencodeZenFree.
func (p *opencodeZenFreeProvider) PassthroughStream(_ *schemas.BifrostContext, _ schemas.PostHookRunner, _ func(context.Context), _ schemas.Key, _ *schemas.BifrostPassthroughRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.PassthroughStreamRequest, p.GetProviderKey())
}
