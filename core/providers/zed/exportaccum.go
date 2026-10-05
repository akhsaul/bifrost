package zed

import (
	"github.com/maximhq/bifrost/core/schemas"
)

// NewChatAccumulatorForTest exposes the unary stream accumulator for tests.
func NewChatAccumulatorForTest(model, innerProvider string) interface {
	FeedLine(ctx *schemas.BifrostContext, line []byte) *schemas.BifrostError
	Finalize(ctx *schemas.BifrostContext) (*schemas.BifrostChatResponse, *schemas.BifrostError)
} {
	acc := newChatAccumulator(model, innerProvider)
	return &chatAccumulatorAdapter{acc: acc}
}

// chatAccumulatorAdapter presents the unexported accumulator through a
// test-visible interface.
type chatAccumulatorAdapter struct {
	acc *chatAccumulator
}

// FeedLine folds one NDJSON line into the accumulator.
func (a *chatAccumulatorAdapter) FeedLine(ctx *schemas.BifrostContext, line []byte) *schemas.BifrostError {
	return a.acc.feedLine(ctx, line)
}

// Finalize assembles the accumulated deltas into one chat response.
func (a *chatAccumulatorAdapter) Finalize(ctx *schemas.BifrostContext) (*schemas.BifrostChatResponse, *schemas.BifrostError) {
	return a.acc.finalize(ctx)
}
