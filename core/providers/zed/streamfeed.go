package zed

import (
	"time"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/providers/anthropic"
	"github.com/maximhq/bifrost/core/providers/gemini"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

// now returns the current time. Split out for the stream-phase timers.
func now() time.Time { return time.Now() }

// since returns the elapsed duration. Split out for the stream-phase timers.
func since(t time.Time) time.Duration { return time.Since(t) }

// feedAnthropicEvent decodes one inner Anthropic stream event and folds it
// into the accumulator via the shared Anthropic streaming converter.
func (a *chatAccumulator) feedAnthropicEvent(ctx *schemas.BifrostContext, eventBytes []byte) *schemas.BifrostError {
	var event anthropic.AnthropicStreamEvent
	if err := sonic.Unmarshal(eventBytes, &event); err != nil {
		return nil // skip undecodable events; the terminal marker still ends the stream
	}
	convStart := now()
	delta, bErr, isLast := event.ToBifrostChatCompletionStream(ctx, "", a.anthropicState)
	schemas.AddStreamConvert(ctx, since(convStart))
	if bErr != nil {
		return bErr
	}
	if delta != nil {
		a.observeChatDelta(delta)
	}
	if isLast {
		a.sawFamilyTerminal = true
	}
	return nil
}

// feedGeminiEvent decodes one inner Gemini stream event and folds it into
// the accumulator via the shared Gemini streaming converter.
func (a *chatAccumulator) feedGeminiEvent(eventBytes []byte) *schemas.BifrostError {
	var parsed gemini.GenerateContentResponse
	if err := sonic.Unmarshal(eventBytes, &parsed); err != nil {
		return nil // skip undecodable chunks; the terminal marker still ends the stream
	}
	deltas, bErr, isLast := parsed.ToBifrostChatCompletionStream(a.geminiState)
	if bErr != nil {
		return bErr
	}
	for _, delta := range deltas {
		if delta != nil {
			a.observeChatDelta(delta)
		}
	}
	if isLast {
		a.sawFamilyTerminal = true
	}
	return nil
}

// readZedStream drains a /completions NDJSON body line by line. Lines arrive
// as {"event":...} or {"status":"stream_ended"}; the shared SSE data reader
// passes raw JSON lines through untouched, so no custom framing is needed.
func readZedStream(ctx *schemas.BifrostContext, resp *fasthttp.Response, feed func([]byte) *schemas.BifrostError) *schemas.BifrostError {
	reader, releaseGzip := providerUtils.DecompressStreamBody(resp)
	defer releaseGzip()
	reader, stopIdleTimeout := providerUtils.NewIdleTimeoutReader(reader, resp.BodyStream(), providerUtils.GetStreamIdleTimeout(ctx), ctx)
	defer stopIdleTimeout()
	stopCancellation := providerUtils.SetupStreamCancellation(ctx, resp.BodyStream(), nil)
	defer stopCancellation()

	sseReader := providerUtils.GetSSEDataReader(ctx, reader)
	for {
		if ctx.Err() != nil {
			return nil
		}
		line, readErr := sseReader.ReadDataLine()
		if readErr != nil {
			// EOF without the terminal marker means truncation; anything
			// else is a transport error. Cancellation is handled by the
			// caller's defer chain.
			if ctx.Err() != nil {
				return nil
			}
			return providerUtils.NewBifrostOperationError(schemas.ErrProviderStreamTruncated, errStreamTruncated)
		}
		if len(line) == 0 {
			continue
		}
		if bErr := feed(line); bErr != nil {
			return bErr
		}
		// The accumulator records the terminal marker; stop promptly instead
		// of waiting for EOF.
		if feedSawTerminal(line) {
			return nil
		}
	}
}

// feedSawTerminal reports whether the line is the NDJSON terminal marker.
func feedSawTerminal(line []byte) bool {
	var parsed ZedStreamLine
	if err := sonic.Unmarshal(line, &parsed); err != nil {
		return false
	}
	return parsed.Status != nil && *parsed.Status == ZedStreamEnded
}
