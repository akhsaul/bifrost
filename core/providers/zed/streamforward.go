package zed

import (
	"time"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/providers/anthropic"
	"github.com/maximhq/bifrost/core/providers/gemini"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
)

// chatForwarder converts one /completions NDJSON stream into Bifrost chat
// chunks and forwards them. One forwarder serves one streaming request; it
// is not safe for concurrent use. It mirrors the accumulator's converters but
// emits per-event deltas instead of folding them.
type chatForwarder struct {
	ch             chan *schemas.BifrostStreamChunk
	model          string
	innerProvider  string
	anthropicState *anthropic.AnthropicStreamState
	geminiState    *gemini.GeminiStreamState
	jsonBody       []byte
	startTime      time.Time
	chunkIndex     int
	lastChunkTime  time.Time
	messageID      string
	usage          *schemas.BifrostLLMUsage
	postHookRunner schemas.PostHookRunner
}

// newChatForwarder creates a forwarder for one streaming request.
func newChatForwarder(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, ch chan *schemas.BifrostStreamChunk, model, innerProvider string, jsonBody []byte, startTime time.Time) *chatForwarder {
	streamUsage := &schemas.BifrostLLMUsage{}
	ctx.SetValue(schemas.BifrostContextKeyStreamAccumulatedUsage, streamUsage)
	return &chatForwarder{
		model:          model,
		innerProvider:  innerProvider,
		anthropicState: anthropic.NewAnthropicStreamState(),
		geminiState:    gemini.NewGeminiStreamState(),
		jsonBody:       jsonBody,
		startTime:      startTime,
		lastChunkTime:  startTime,
		usage:          streamUsage,
		postHookRunner: postHookRunner,
		ch:             ch,
	}
}

// feedLine parses one NDJSON line, converts its event, and forwards the
// resulting chunks. The terminal marker is a no-op here: finish() emits the
// final chunk after the read loop drains.
func (f *chatForwarder) feedLine(ctx *schemas.BifrostContext, line []byte, sendBackRawResponse bool) *schemas.BifrostError {
	var parsed ZedStreamLine
	if err := sonic.Unmarshal(line, &parsed); err != nil {
		return nil
	}
	if parsed.Status != nil || parsed.Event == nil {
		return nil
	}
	foldForwarderUsage(f.usage, f.innerProvider, parsed.Event.Raw)
	eventBytes, err := sonic.Marshal(parsed.Event.Raw)
	if err != nil || len(eventBytes) == 0 {
		return nil
	}
	raw := ""
	if sendBackRawResponse {
		raw = string(line)
	}
	switch f.innerProvider {
	case ZedInnerAnthropic:
		return f.feedAnthropic(ctx, eventBytes, raw)
	case ZedInnerGoogle:
		return f.feedGemini(ctx, eventBytes, raw)
	default:
		return f.feedOpenAI(ctx, eventBytes, raw)
	}
}

// feedAnthropic converts one inner Anthropic event and forwards it.
func (f *chatForwarder) feedAnthropic(ctx *schemas.BifrostContext, eventBytes []byte, raw string) *schemas.BifrostError {
	var event anthropic.AnthropicStreamEvent
	if err := sonic.Unmarshal(eventBytes, &event); err != nil {
		return nil
	}
	delta, bErr, _ := event.ToBifrostChatCompletionStream(ctx, "", f.anthropicState)
	if bErr != nil {
		return bErr
	}
	if delta != nil {
		f.emit(ctx, delta, raw)
	}
	return nil
}

// feedGemini converts one inner Gemini event and forwards resulting deltas.
func (f *chatForwarder) feedGemini(ctx *schemas.BifrostContext, eventBytes []byte, raw string) *schemas.BifrostError {
	var parsed gemini.GenerateContentResponse
	if err := sonic.Unmarshal(eventBytes, &parsed); err != nil {
		return nil
	}
	deltas, bErr, _ := parsed.ToBifrostChatCompletionStream(f.geminiState)
	if bErr != nil {
		return bErr
	}
	for _, delta := range deltas {
		if delta != nil {
			f.emit(ctx, delta, raw)
		}
	}
	return nil
}

// feedOpenAI converts one inner OpenAI Responses event and forwards it as a
// chat chunk via ToBifrostChatResponse.
func (f *chatForwarder) feedOpenAI(ctx *schemas.BifrostContext, eventBytes []byte, raw string) *schemas.BifrostError {
	var streamResp schemas.BifrostResponsesStreamResponse
	if err := sonic.Unmarshal(eventBytes, &streamResp); err != nil {
		return nil
	}
	if streamResp.Type == schemas.ResponsesStreamResponseTypeError {
		return responsesStreamError(&streamResp)
	}
	delta := streamResp.ToBifrostChatResponse()
	if delta == nil {
		return nil
	}
	f.emit(ctx, delta, raw)
	return nil
}

// emit stamps one converted delta and forwards it through post-hooks.
func (f *chatForwarder) emit(ctx *schemas.BifrostContext, delta *schemas.BifrostChatResponse, raw string) {
	if delta.ID == "" {
		delta.ID = f.messageID
	} else if f.messageID == "" {
		f.messageID = delta.ID
	}
	if delta.Model == "" {
		delta.Model = f.model
	}
	if delta.Usage != nil {
		mergeUsage(f.usage, delta.Usage)
	}
	delta.ExtraFields.ChunkIndex = f.chunkIndex
	delta.ExtraFields.Latency = time.Since(f.lastChunkTime).Milliseconds()
	delta.ExtraFields.Provider = schemas.Zed
	delta.ExtraFields.RequestType = schemas.ChatCompletionStreamRequest
	f.lastChunkTime = now()
	f.chunkIndex++
	if raw != "" {
		delta.ExtraFields.RawResponse = raw
	}
	providerUtils.ProcessAndSendResponse(ctx, f.postHookRunner,
		providerUtils.GetBifrostResponseForStreamResponse(nil, delta, nil, nil, nil, nil), f.responseChan(), nil)
}

// responseChan is set per stream; stashed here so emit stays small.
func (f *chatForwarder) responseChan() chan *schemas.BifrostStreamChunk {
	return f.ch
}

// finish emits the terminal chunk carrying the final usage.
func (f *chatForwarder) finish(ctx *schemas.BifrostContext, startTime time.Time, sendBackRawRequest bool) {
	final := providerUtils.CreateBifrostChatCompletionChunkResponse(f.messageID, f.usage, schemas.Ptr("stop"), f.chunkIndex, f.model, 0)
	final.ExtraFields.Provider = schemas.Zed
	final.ExtraFields.RequestType = schemas.ChatCompletionStreamRequest
	if sendBackRawRequest {
		providerUtils.ParseAndSetRawRequest(&final.ExtraFields, f.jsonBody)
	}
	final.ExtraFields.Latency = time.Since(startTime).Milliseconds()
	ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
	providerUtils.ProcessAndSendResponse(ctx, f.postHookRunner,
		providerUtils.GetBifrostResponseForStreamResponse(nil, final, nil, nil, nil, nil), f.responseChan(), nil)
}

// foldForwarderUsage extracts usage from one raw wire event, mirroring the
// accumulator's foldEventUsage for the streaming path.
func foldForwarderUsage(usage *schemas.BifrostLLMUsage, innerProvider string, raw map[string]interface{}) {
	acc := &chatAccumulator{usage: usage, innerProvider: innerProvider}
	acc.foldEventUsage(raw)
}
