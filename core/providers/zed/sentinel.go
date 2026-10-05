package zed

import "errors"

var (
	errNilAnthropicRequest = errors.New("zed: anthropic conversion returned nil")
	errNilGeminiRequest    = errors.New("zed: gemini conversion returned nil")
	errNilOpenAIRequest    = errors.New("zed: openai conversion returned nil")
	errStreamTruncated     = errors.New("zed: provider closed the stream before sending stream_ended (upstream connection ended mid-stream)")
	errEmptyStreamBody     = errors.New("zed: provider returned an empty response")
)
