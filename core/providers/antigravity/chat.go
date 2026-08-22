package antigravity

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/providers/gemini"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

const (
	MaxAntigravityClaudeOutputTokens = 16384
)

// ToAntigravityChatRequest converts a BifrostChatRequest into an AntigravityRequestEnvelope.
func ToAntigravityChatRequest(
	ctx *schemas.BifrostContext,
	bifrostReq *schemas.BifrostChatRequest,
	projectID string,
) (*AntigravityRequestEnvelope, []byte, error) {
	if bifrostReq == nil {
		return nil, nil, nil
	}

	resolvedModel := ResolveModel(bifrostReq.Model)
	origModel := bifrostReq.Model
	bifrostReq.Model = resolvedModel
	defer func() {
		bifrostReq.Model = origModel
	}()

	geminiReq, err := gemini.ToGeminiChatCompletionRequest(ctx, bifrostReq)
	if err != nil {
		return nil, nil, err
	}

	sanitizeAntigravityRequest(geminiReq, resolvedModel)

	var sysInstruction *gemini.Content
	if geminiReq.SystemInstruction != nil && len(geminiReq.SystemInstruction.Parts) > 0 {
		sysInstruction = geminiReq.SystemInstruction
	}

	var genConfig *gemini.GenerationConfig
	if geminiReq.GenerationConfig.MaxOutputTokens > 0 ||
		geminiReq.GenerationConfig.Temperature != nil ||
		geminiReq.GenerationConfig.TopP != nil ||
		geminiReq.GenerationConfig.TopK != nil ||
		len(geminiReq.GenerationConfig.StopSequences) > 0 ||
		geminiReq.GenerationConfig.ThinkingConfig != nil {
		cfgCopy := geminiReq.GenerationConfig
		genConfig = &cfgCopy
	}

	sessionID := GenerateAntigravitySessionID()
	reqID := GenerateAntigravityRequestID(sessionID, resolvedModel, "agent", len(geminiReq.Contents))

	innerReq := &AntigravityInnerRequest{
		SessionID:         sessionID,
		Contents:          geminiReq.Contents,
		SystemInstruction: sysInstruction,
		GenerationConfig:  genConfig,
		SafetySettings:    geminiReq.SafetySettings,
		Tools:             geminiReq.Tools,
		ToolConfig:        geminiReq.ToolConfig,
	}

	envelope := &AntigravityRequestEnvelope{
		Project:     projectID,
		Model:       resolvedModel,
		UserAgent:   "antigravity",
		RequestID:   reqID,
		RequestType: "agent",
		Request:     innerReq,
	}

	jsonBytes, err := sonic.Marshal(envelope)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to marshal Antigravity request envelope: %w", err)
	}

	return envelope, jsonBytes, nil
}

// ToAntigravityResponsesRequest converts a BifrostResponsesRequest into an AntigravityRequestEnvelope.
func ToAntigravityResponsesRequest(
	ctx *schemas.BifrostContext,
	bifrostReq *schemas.BifrostResponsesRequest,
	projectID string,
) (*AntigravityRequestEnvelope, []byte, error) {
	if bifrostReq == nil {
		return nil, nil, nil
	}

	resolvedModel := ResolveModel(bifrostReq.Model)
	origModel := bifrostReq.Model
	bifrostReq.Model = resolvedModel
	defer func() {
		bifrostReq.Model = origModel
	}()

	geminiReq, err := gemini.ToGeminiResponsesRequest(ctx, bifrostReq)
	if err != nil {
		return nil, nil, err
	}

	sanitizeAntigravityRequest(geminiReq, resolvedModel)

	var sysInstruction *gemini.Content
	if geminiReq.SystemInstruction != nil && len(geminiReq.SystemInstruction.Parts) > 0 {
		sysInstruction = geminiReq.SystemInstruction
	}

	var genConfig *gemini.GenerationConfig
	if geminiReq.GenerationConfig.MaxOutputTokens > 0 ||
		geminiReq.GenerationConfig.Temperature != nil ||
		geminiReq.GenerationConfig.TopP != nil ||
		geminiReq.GenerationConfig.TopK != nil ||
		len(geminiReq.GenerationConfig.StopSequences) > 0 ||
		geminiReq.GenerationConfig.ThinkingConfig != nil {
		cfgCopy := geminiReq.GenerationConfig
		genConfig = &cfgCopy
	}

	sessionID := GenerateAntigravitySessionID()
	reqID := GenerateAntigravityRequestID(sessionID, resolvedModel, "agent", len(geminiReq.Contents))

	innerReq := &AntigravityInnerRequest{
		SessionID:         sessionID,
		Contents:          geminiReq.Contents,
		SystemInstruction: sysInstruction,
		GenerationConfig:  genConfig,
		SafetySettings:    geminiReq.SafetySettings,
		Tools:             geminiReq.Tools,
		ToolConfig:        geminiReq.ToolConfig,
	}

	envelope := &AntigravityRequestEnvelope{
		Project:     projectID,
		Model:       resolvedModel,
		UserAgent:   "antigravity",
		RequestID:   reqID,
		RequestType: "agent",
		Request:     innerReq,
	}

	jsonBytes, err := sonic.Marshal(envelope)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to marshal Antigravity request envelope: %w", err)
	}

	return envelope, jsonBytes, nil
}

// sanitizeAntigravityRequest applies provider-specific safety and model adjustments.
func sanitizeAntigravityRequest(req *gemini.GeminiGenerationRequest, model string) {
	if req == nil {
		return
	}

	// Sanitize competitive system prompt strings that Google Cloud Code flags
	if req.SystemInstruction != nil {
		blockedPhrase := "You are a Claude agent, built on Anthropic's Claude Agent SDK."
		for i := range req.SystemInstruction.Parts {
			if strings.Contains(req.SystemInstruction.Parts[i].Text, blockedPhrase) {
				req.SystemInstruction.Parts[i].Text = strings.ReplaceAll(req.SystemInstruction.Parts[i].Text, blockedPhrase, "")
			}
		}
	}

	isClaude := strings.Contains(strings.ToLower(model), "claude")

	if isClaude {
		// Strip trailing assistant turns for Claude models
		if len(req.Contents) > 1 {
			for len(req.Contents) > 1 && req.Contents[len(req.Contents)-1].Role == "model" {
				req.Contents = req.Contents[:len(req.Contents)-1]
			}
		}

		// Clamp max output tokens to 16384 for Claude
		if req.GenerationConfig.MaxOutputTokens > MaxAntigravityClaudeOutputTokens {
			req.GenerationConfig.MaxOutputTokens = MaxAntigravityClaudeOutputTokens
		}
	}

	// Filter unsupported safety categories
	if len(req.SafetySettings) > 0 {
		var filtered []gemini.SafetySetting
		for _, s := range req.SafetySettings {
			if s.Category != "HARM_CATEGORY_CIVIC_INTEGRITY" {
				filtered = append(filtered, s)
			}
		}
		req.SafetySettings = filtered
	}
}

// HandleAntigravityChatCompletion executes a unary chat completion request.
func HandleAntigravityChatCompletion(
	ctx *schemas.BifrostContext,
	client *fasthttp.Client,
	url string,
	jsonBody []byte,
	headers map[string]string,
	extraHeaders map[string]string,
	rawModel string,
	sendBackRawRequest bool,
	sendBackRawResponse bool,
	logger schemas.Logger,
) (*schemas.BifrostChatResponse, *schemas.BifrostError) {
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	req.Header.SetMethod(http.MethodPost)
	req.SetRequestURI(url)
	req.Header.SetContentType("application/json")
	req.Header.Set("Accept", "text/event-stream")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	providerUtils.SetExtraHeaders(ctx, req, extraHeaders, nil)
	req.SetBody(jsonBody)

	latency, bifrostErr, wait := providerUtils.MakeRequestWithContext(ctx, client, req, resp)
	defer wait()

	if bifrostErr != nil {
		return nil, providerUtils.EnrichError(ctx, bifrostErr, jsonBody, nil, sendBackRawRequest, sendBackRawResponse, latency)
	}

	if resp.StatusCode() != fasthttp.StatusOK {
		return nil, providerUtils.EnrichError(ctx, parseAntigravityError(resp, rawModel), jsonBody, resp.Body(), sendBackRawRequest, sendBackRawResponse, latency)
	}

	body := resp.Body()
	accumulated := &gemini.GenerateContentResponse{}
	streamState := gemini.NewGeminiStreamState()

	// Parse SSE lines or standard JSON body
	if bytes.Contains(body, []byte("data:")) {
		scanner := bufio.NewScanner(bytes.NewReader(body))
		for scanner.Scan() {
			line := bytes.TrimSpace(scanner.Bytes())
			if len(line) == 0 || bytes.Equal(line, []byte("data: [DONE]")) {
				continue
			}
			if bytes.HasPrefix(line, []byte("data:")) {
				data := bytes.TrimSpace(bytes.TrimPrefix(line, []byte("data:")))
				if len(data) == 0 {
					continue
				}
				chunkResp, err := parseAntigravityChunk(data)
				if err == nil && chunkResp != nil {
					mergeGeminiResponses(accumulated, chunkResp)
				}
			}
		}
	} else {
		chunkResp, err := parseAntigravityChunk(body)
		if err != nil {
			return nil, providerUtils.EnrichError(ctx, providerUtils.NewBifrostOperationError("failed to decode Antigravity response", err), jsonBody, body, sendBackRawRequest, sendBackRawResponse, latency)
		}
		if chunkResp != nil {
			accumulated = chunkResp
		}
	}

	chatResp := accumulated.ToBifrostChatResponse()
	if chatResp != nil {
		chatResp.Model = rawModel
		chatResp.ExtraFields = schemas.BifrostResponseExtraFields{
			Provider: schemas.Antigravity,
			Latency:  latency.Milliseconds(),
		}
		if sendBackRawRequest {
			providerUtils.ParseAndSetRawRequest(&chatResp.ExtraFields, jsonBody)
		}
		if sendBackRawResponse {
			var rawResp interface{}
			if err := sonic.Unmarshal(body, &rawResp); err == nil {
				chatResp.ExtraFields.RawResponse = rawResp
			} else {
				chatResp.ExtraFields.RawResponse = string(body)
			}
		}
	}
	_ = streamState

	return chatResp, nil
}

// HandleAntigravityChatCompletionStream executes a streaming chat completion request.
func HandleAntigravityChatCompletionStream(
	ctx *schemas.BifrostContext,
	client *fasthttp.Client,
	url string,
	jsonBody []byte,
	headers map[string]string,
	extraHeaders map[string]string,
	rawModel string,
	streamIdleTimeoutInSeconds int,
	sendBackRawRequest bool,
	sendBackRawResponse bool,
	postHookRunner schemas.PostHookRunner,
	postHookSpanFinalizer func(context.Context),
	logger schemas.Logger,
) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	providerUtils.SetStreamIdleTimeoutIfEmpty(ctx, streamIdleTimeoutInSeconds)
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	resp.StreamBody = true
	defer fasthttp.ReleaseRequest(req)

	req.Header.SetMethod(http.MethodPost)
	req.SetRequestURI(url)
	req.Header.SetContentType("application/json")
	req.Header.Set("Accept", "text/event-stream")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	providerUtils.SetExtraHeaders(ctx, req, extraHeaders, nil)
	req.SetBody(jsonBody)

	startTime := time.Now()
	doErr := providerUtils.DoStreamingRequest(ctx, client, req, resp)
	latency := time.Since(startTime)
	if doErr != nil {
		defer providerUtils.ReleaseStreamingResponse(ctx, resp)
		if errors.Is(doErr, context.Canceled) {
			return nil, providerUtils.EnrichError(ctx, &schemas.BifrostError{
				IsBifrostError: false,
				Error: &schemas.ErrorField{
					Type:    schemas.Ptr(schemas.RequestCancelled),
					Message: schemas.ErrRequestCancelled,
					Error:   doErr,
				},
			}, jsonBody, nil, sendBackRawRequest, sendBackRawResponse, latency)
		}
		if errors.Is(doErr, fasthttp.ErrTimeout) || errors.Is(doErr, context.DeadlineExceeded) {
			return nil, providerUtils.EnrichError(ctx, providerUtils.NewBifrostTimeoutError(schemas.ErrProviderRequestTimedOut, doErr), jsonBody, nil, sendBackRawRequest, sendBackRawResponse, latency)
		}
		return nil, providerUtils.EnrichError(ctx, providerUtils.NewBifrostOperationError(schemas.ErrProviderDoRequest, doErr), jsonBody, nil, sendBackRawRequest, sendBackRawResponse, latency)
	}

	if resp.StatusCode() != fasthttp.StatusOK {
		defer providerUtils.ReleaseStreamingResponse(ctx, resp)
		respBody := append([]byte(nil), resp.Body()...)
		return nil, providerUtils.EnrichError(ctx, parseAntigravityError(resp, rawModel), jsonBody, respBody, sendBackRawRequest, sendBackRawResponse, latency)
	}

	responseChan := make(chan *schemas.BifrostStreamChunk, schemas.DefaultStreamBufferSize)

	go func() {
		defer providerUtils.EnsureStreamFinalizerCalled(ctx, postHookSpanFinalizer)
		defer func() {
			if ctx.Err() == context.Canceled {
				providerUtils.HandleStreamCancellation(ctx, postHookRunner, responseChan, logger, postHookSpanFinalizer, jsonBody)
			} else if ctx.Err() == context.DeadlineExceeded {
				providerUtils.HandleStreamTimeout(ctx, postHookRunner, responseChan, logger, postHookSpanFinalizer, jsonBody)
			}
			providerUtils.CloseStream(ctx, responseChan)
		}()
		defer providerUtils.ReleaseStreamingResponse(ctx, resp)

		if resp.BodyStream() == nil {
			bifrostErr := providerUtils.NewBifrostOperationError(
				"Provider returned an empty response",
				fmt.Errorf("provider returned an empty response"),
			)
			ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
			providerUtils.ProcessAndSendBifrostError(ctx, postHookRunner, providerUtils.EnrichError(ctx, bifrostErr, jsonBody, nil, sendBackRawRequest, sendBackRawResponse, latency), responseChan, logger, postHookSpanFinalizer)
			return
		}

		decompressedReader, releaseGzip := providerUtils.DecompressStreamBody(resp)
		defer releaseGzip()

		decompressedReader, stopIdleTimeout := providerUtils.NewIdleTimeoutReader(decompressedReader, resp.BodyStream(), providerUtils.GetStreamIdleTimeout(ctx), ctx)
		defer stopIdleTimeout()

		stopCancellation := providerUtils.SetupStreamCancellation(ctx, resp.BodyStream(), logger)
		defer stopCancellation()

		sseReader := providerUtils.GetSSEDataReader(ctx, decompressedReader)
		chunkIndex := 0
		lastChunkTime := startTime
		var responseID string
		streamState := gemini.NewGeminiStreamState()
		streamUsage := &schemas.BifrostLLMUsage{}
		ctx.SetValue(schemas.BifrostContextKeyStreamAccumulatedUsage, streamUsage)

		for {
			if ctx.Err() != nil {
				return
			}
			eventData, readErr := sseReader.ReadDataLine()
			if readErr == io.EOF {
				break
			}
			if readErr != nil {
				if ctx.Err() != nil {
					return
				}
				ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
				if logger != nil {
					logger.Warn("Error reading Antigravity stream: %v", readErr)
				}
				providerUtils.ProcessAndSendError(ctx, postHookRunner, readErr, responseChan, logger, postHookSpanFinalizer)
				return
			}

			geminiResponse, err := parseAntigravityChunk(eventData)
			if err != nil {
				if logger != nil {
					logger.Warn("Failed to process Antigravity stream chunk: %v", err)
				}
				continue
			}

			if geminiResponse == nil {
				continue
			}

			if geminiResponse.ResponseID != "" && responseID == "" {
				responseID = geminiResponse.ResponseID
			}

			response, bifrostErr, isLastChunk := geminiResponse.ToBifrostChatCompletionStream(streamState)
			if bifrostErr != nil {
				ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
				providerUtils.ProcessAndSendBifrostError(ctx, postHookRunner, providerUtils.EnrichError(ctx, bifrostErr, jsonBody, nil, sendBackRawRequest, sendBackRawResponse, latency), responseChan, logger, postHookSpanFinalizer)
				return
			}

			if response != nil {
				response.ID = responseID
				response.Model = rawModel
				if response.Usage != nil {
					*streamUsage = *response.Usage
				}
				response.ExtraFields = schemas.BifrostResponseExtraFields{
					ChunkIndex: chunkIndex,
					Latency:    time.Since(lastChunkTime).Milliseconds(),
					Provider:   schemas.Antigravity,
				}
				if isLastChunk {
					ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
				}
				if sendBackRawRequest {
					providerUtils.ParseAndSetRawRequest(&response.ExtraFields, jsonBody)
				}
				if sendBackRawResponse {
					response.ExtraFields.RawResponse = string(eventData)
				}

				lastChunkTime = time.Now()
				chunkIndex++

				if isLastChunk {
					response.ExtraFields.Latency = time.Since(startTime).Milliseconds()
					ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
					providerUtils.ProcessAndSendResponse(ctx, postHookRunner, providerUtils.GetBifrostResponseForStreamResponse(nil, response, nil, nil, nil, nil), responseChan, postHookSpanFinalizer)
					break
				}

				// Process response through post-hooks and send to channel
				providerUtils.ProcessAndSendResponse(ctx, postHookRunner, providerUtils.GetBifrostResponseForStreamResponse(nil, response, nil, nil, nil, nil), responseChan, postHookSpanFinalizer)
			}
		}
	}()

	return responseChan, nil
}

// parseAntigravityChunk unmarshals an Antigravity SSE data payload into GenerateContentResponse.
func parseAntigravityChunk(data []byte) (*gemini.GenerateContentResponse, error) {
	if len(data) == 0 || bytes.Equal(data, []byte("[DONE]")) {
		return nil, nil
	}

	var chunk AntigravitySSEChunk
	if err := sonic.Unmarshal(data, &chunk); err == nil {
		if chunk.Response != nil {
			return chunk.Response, nil
		}
		if len(chunk.Candidates) > 0 {
			return &gemini.GenerateContentResponse{
				Candidates:    chunk.Candidates,
				UsageMetadata: chunk.UsageMetadata,
			}, nil
		}
		if chunk.Markdown != "" {
			return &gemini.GenerateContentResponse{
				Candidates: []*gemini.Candidate{
					{
						Content: &gemini.Content{
							Role: "model",
							Parts: []*gemini.Part{
								{Text: chunk.Markdown},
							},
						},
					},
				},
				UsageMetadata: chunk.UsageMetadata,
			}, nil
		}
	}

	// Try direct GenerateContentResponse unmarshal
	var direct gemini.GenerateContentResponse
	if err := sonic.Unmarshal(data, &direct); err == nil && len(direct.Candidates) > 0 {
		return &direct, nil
	}

	return nil, fmt.Errorf("unknown Antigravity stream chunk format: %s", string(data))
}

// mergeGeminiResponses accumulates streaming chunks into a single response.
func mergeGeminiResponses(target *gemini.GenerateContentResponse, source *gemini.GenerateContentResponse) {
	if source == nil {
		return
	}
	if target.ResponseID == "" && source.ResponseID != "" {
		target.ResponseID = source.ResponseID
	}
	if target.ModelVersion == "" && source.ModelVersion != "" {
		target.ModelVersion = source.ModelVersion
	}
	if source.UsageMetadata != nil {
		target.UsageMetadata = source.UsageMetadata
	}
	if len(source.Candidates) > 0 {
		if len(target.Candidates) == 0 {
			target.Candidates = append(target.Candidates, source.Candidates[0])
		} else {
			targetCandidate := target.Candidates[0]
			sourceCandidate := source.Candidates[0]
			if sourceCandidate.FinishReason != "" {
				targetCandidate.FinishReason = sourceCandidate.FinishReason
			}
			if sourceCandidate.Content != nil {
				if targetCandidate.Content == nil {
					targetCandidate.Content = sourceCandidate.Content
				} else {
					targetCandidate.Content.Parts = append(targetCandidate.Content.Parts, sourceCandidate.Content.Parts...)
				}
			}
		}
	}
}
