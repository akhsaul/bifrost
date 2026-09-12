package logging

import (
	"context"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// matrixChatRequest builds a minimal chat completion request for matrix tests.
func matrixChatRequest(userMsg string) *schemas.BifrostRequest {
	return &schemas.BifrostRequest{
		RequestType: schemas.ChatCompletionRequest,
		ChatRequest: &schemas.BifrostChatRequest{
			Provider: schemas.OpenAI,
			Model:    "gpt-4o-mini",
			Input: []schemas.ChatMessage{
				{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: &userMsg}},
			},
			Params: &schemas.ChatParameters{},
		},
	}
}

// matrixSuccessResponse builds a minimal non-streaming chat success response,
// carrying raw provider payloads the way a provider attaches them when raw
// capture is active in core (applyRawCaptureSignals).
func matrixSuccessResponse(assistantText string) *schemas.BifrostResponse {
	return &schemas.BifrostResponse{
		ChatResponse: &schemas.BifrostChatResponse{
			ID:     "chatcmpl-matrix",
			Object: string(schemas.ChatCompletionRequest),
			Choices: []schemas.BifrostResponseChoice{
				{
					Index: 0,
					ChatNonStreamResponseChoice: &schemas.ChatNonStreamResponseChoice{
						Message: &schemas.ChatMessage{Role: schemas.ChatMessageRoleAssistant, Content: &schemas.ChatMessageContent{ContentStr: &assistantText}},
					},
				},
			},
			ExtraFields: schemas.BifrostResponseExtraFields{
				RequestType:            schemas.ChatCompletionRequest,
				OriginalModelRequested: "gpt-4o-mini",
				ResolvedModelUsed:      "gpt-4o-mini",
				RawRequest:             map[string]interface{}{"model": "gpt-4o-mini", "messages": "matrix"},
				RawResponse:            map[string]interface{}{"id": "chatcmpl-matrix", "choices": "matrix"},
			},
		},
	}
}

// matrixError builds a provider error carrying raw request/response payloads.
func matrixError(userMsg string) *schemas.BifrostError {
	statusCode := 500
	return &schemas.BifrostError{
		IsBifrostError: true,
		StatusCode:     &statusCode,
		Error:          &schemas.ErrorField{Message: "provider failed"},
		ExtraFields: schemas.BifrostErrorExtraFields{
			RequestType: schemas.ChatCompletionRequest,
			Provider:    schemas.OpenAI,
			RawRequest:  map[string]interface{}{"model": "gpt-4o-mini", "messages": []interface{}{map[string]interface{}{"role": "user", "content": userMsg}}},
			RawResponse: map[string]interface{}{"error": map[string]interface{}{"message": "internal server error"}},
		},
	}
}

// matrixCtx builds a Bifrost context with request id, on-error signal, and the
// provider store_raw_request_response signal.
func matrixCtx(requestID string, onErr, storeRaw bool) *schemas.BifrostContext {
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyRequestID, requestID)
	ctx.SetValue(schemas.BifrostContextKeyContentLoggingOnError, onErr)
	ctx.SetValue(schemas.BifrostContextKeyShouldStoreRawInLogs, storeRaw)
	return ctx
}

func runMatrixPreHook(t *testing.T, plugin *LoggerPlugin, ctx *schemas.BifrostContext, userMsg string) {
	t.Helper()
	_, _, err := plugin.PreLLMHook(ctx, matrixChatRequest(userMsg))
	require.NoError(t, err)
}

// TestMatrix_False_True_True: disable=false, store_raw=true, on_error=true.
// Success and error both keep parsed content and raw payloads.
func TestMatrix_False_True_True(t *testing.T) {
	for _, tc := range []struct {
		name    string
		isError bool
	}{
		{"success", false},
		{"error", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := newTestStore(t)
			defer store.Close(context.Background())
			plugin, err := Init(context.Background(), &Config{
				DisableContentLogging: boolPtr(false),
				ContentLoggingOnError: boolPtr(true),
			}, testLogger{}, store, nil, nil, nil)
			require.NoError(t, err)
			t.Cleanup(func() { _ = plugin.Cleanup() })

			requestID := "matrix-ftt-" + tc.name
			ctx := matrixCtx(requestID, true, true)
			userMsg := "matrix ftt " + tc.name
			runMatrixPreHook(t, plugin, ctx, userMsg)
			if tc.isError {
				_, _, err = plugin.PostLLMHook(ctx, nil, matrixError(userMsg))
			} else {
				_, _, err = plugin.PostLLMHook(ctx, matrixSuccessResponse("answer ftt"), nil)
			}
			require.NoError(t, err)
			require.NoError(t, plugin.Cleanup())

			entry, err := store.FindByID(context.Background(), requestID)
			require.NoError(t, err)
			assert.NotEmpty(t, entry.InputHistoryParsed, "parsed content must be stored")
			assert.NotEmpty(t, entry.RawRequest, "raw request must be stored")
			assert.NotEmpty(t, entry.RawResponse, "raw response must be stored")
			assert.False(t, entry.ContentHidden)
		})
	}
}

// TestMatrix_False_False_False: disable=false, store_raw=false, on_error=false.
// Parsed content stored, raw empty, on both success and error.
func TestMatrix_False_False_False(t *testing.T) {
	for _, tc := range []struct {
		name    string
		isError bool
	}{
		{"success", false},
		{"error", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := newTestStore(t)
			defer store.Close(context.Background())
			plugin, err := Init(context.Background(), &Config{
				DisableContentLogging: boolPtr(false),
				ContentLoggingOnError: boolPtr(false),
			}, testLogger{}, store, nil, nil, nil)
			require.NoError(t, err)
			t.Cleanup(func() { _ = plugin.Cleanup() })

			requestID := "matrix-fff-" + tc.name
			ctx := matrixCtx(requestID, false, false)
			userMsg := "matrix fff " + tc.name
			runMatrixPreHook(t, plugin, ctx, userMsg)
			if tc.isError {
				_, _, err = plugin.PostLLMHook(ctx, nil, matrixError(userMsg))
			} else {
				_, _, err = plugin.PostLLMHook(ctx, matrixSuccessResponse("answer fff"), nil)
			}
			require.NoError(t, err)
			require.NoError(t, plugin.Cleanup())

			entry, err := store.FindByID(context.Background(), requestID)
			require.NoError(t, err)
			assert.NotEmpty(t, entry.InputHistoryParsed, "parsed content must be stored")
			assert.Empty(t, entry.RawRequest, "raw request must be empty")
			assert.Empty(t, entry.RawResponse, "raw response must be empty")
			assert.False(t, entry.ContentHidden)
		})
	}
}

// TestMatrix_False_False_True: disable=false, store_raw=false, on_error=true.
// Parsed content stored on both paths; raw stored on error only.
func TestMatrix_False_False_True(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		store := newTestStore(t)
		defer store.Close(context.Background())
		plugin, err := Init(context.Background(), &Config{
			DisableContentLogging: boolPtr(false),
			ContentLoggingOnError: boolPtr(true),
		}, testLogger{}, store, nil, nil, nil)
		require.NoError(t, err)
		t.Cleanup(func() { _ = plugin.Cleanup() })

		requestID := "matrix-fft-success"
		ctx := matrixCtx(requestID, true, false)
		userMsg := "matrix fft success"
		runMatrixPreHook(t, plugin, ctx, userMsg)
		_, _, err = plugin.PostLLMHook(ctx, matrixSuccessResponse("answer fft"), nil)
		require.NoError(t, err)
		require.NoError(t, plugin.Cleanup())

		entry, err := store.FindByID(context.Background(), requestID)
		require.NoError(t, err)
		assert.NotEmpty(t, entry.InputHistoryParsed, "parsed content must be stored on success")
		assert.Empty(t, entry.RawRequest, "raw request must be empty on success")
		assert.Empty(t, entry.RawResponse, "raw response must be empty on success")
		assert.False(t, entry.ContentHidden)
	})

	t.Run("error", func(t *testing.T) {
		store := newTestStore(t)
		defer store.Close(context.Background())
		plugin, err := Init(context.Background(), &Config{
			DisableContentLogging: boolPtr(false),
			ContentLoggingOnError: boolPtr(true),
		}, testLogger{}, store, nil, nil, nil)
		require.NoError(t, err)
		t.Cleanup(func() { _ = plugin.Cleanup() })

		requestID := "matrix-fft-error"
		ctx := matrixCtx(requestID, true, false)
		userMsg := "matrix fft error"
		runMatrixPreHook(t, plugin, ctx, userMsg)
		_, _, err = plugin.PostLLMHook(ctx, nil, matrixError(userMsg))
		require.NoError(t, err)
		require.NoError(t, plugin.Cleanup())

		entry, err := store.FindByID(context.Background(), requestID)
		require.NoError(t, err)
		assert.NotEmpty(t, entry.InputHistoryParsed, "parsed content must be stored on error")
		assert.NotEmpty(t, entry.RawRequest, "raw request must be stored on error")
		assert.NotEmpty(t, entry.RawResponse, "raw response must be stored on error")
		assert.False(t, entry.ContentHidden)
	})
}

// TestMatrix_False_True_False: disable=false, store_raw=true, on_error=false.
// Parsed content and raw stored on both success and error.
func TestMatrix_False_True_False(t *testing.T) {
	for _, tc := range []struct {
		name    string
		isError bool
	}{
		{"success", false},
		{"error", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := newTestStore(t)
			defer store.Close(context.Background())
			plugin, err := Init(context.Background(), &Config{
				DisableContentLogging: boolPtr(false),
				ContentLoggingOnError: boolPtr(false),
			}, testLogger{}, store, nil, nil, nil)
			require.NoError(t, err)
			t.Cleanup(func() { _ = plugin.Cleanup() })

			requestID := "matrix-ftf-" + tc.name
			ctx := matrixCtx(requestID, false, true)
			userMsg := "matrix ftf " + tc.name
			runMatrixPreHook(t, plugin, ctx, userMsg)
			if tc.isError {
				_, _, err = plugin.PostLLMHook(ctx, nil, matrixError(userMsg))
			} else {
				_, _, err = plugin.PostLLMHook(ctx, matrixSuccessResponse("answer ftf"), nil)
			}
			require.NoError(t, err)
			require.NoError(t, plugin.Cleanup())

			entry, err := store.FindByID(context.Background(), requestID)
			require.NoError(t, err)
			assert.NotEmpty(t, entry.InputHistoryParsed, "parsed content must be stored")
			assert.NotEmpty(t, entry.RawRequest, "raw request must be stored")
			assert.NotEmpty(t, entry.RawResponse, "raw response must be stored")
			assert.False(t, entry.ContentHidden)
		})
	}
}

// TestMatrix_True_MasterSwitch: disable=true is an absolute master switch.
// Both parsed content and raw are empty on success and error, regardless of
// store_raw_request_response or content_logging_on_error.
func TestMatrix_True_MasterSwitch(t *testing.T) {
	cases := []struct {
		name     string
		storeRaw bool
		onErr    bool
		isError  bool
	}{
		{"success_storeRaw_onErr", true, true, false},
		{"success_noRaw_noOnErr", false, false, false},
		{"success_storeRaw_noOnErr", true, false, false},
		{"success_noRaw_onErr", false, true, false},
		{"error_storeRaw_onErr", true, true, true},
		{"error_noRaw_noOnErr", false, false, true},
		{"error_storeRaw_noOnErr", true, false, true},
		{"error_noRaw_onErr", false, true, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := newTestStore(t)
			defer store.Close(context.Background())
			plugin, err := Init(context.Background(), &Config{
				DisableContentLogging: boolPtr(true),
				ContentLoggingOnError: boolPtr(tc.onErr),
			}, testLogger{}, store, nil, nil, nil)
			require.NoError(t, err)
			t.Cleanup(func() { _ = plugin.Cleanup() })

			requestID := "matrix-true-" + tc.name
			ctx := matrixCtx(requestID, tc.onErr, tc.storeRaw)
			userMsg := "matrix true " + tc.name
			runMatrixPreHook(t, plugin, ctx, userMsg)
			if tc.isError {
				_, _, err = plugin.PostLLMHook(ctx, nil, matrixError(userMsg))
			} else {
				_, _, err = plugin.PostLLMHook(ctx, matrixSuccessResponse("answer true"), nil)
			}
			require.NoError(t, err)
			require.NoError(t, plugin.Cleanup())

			entry, err := store.FindByID(context.Background(), requestID)
			require.NoError(t, err)
			assert.Empty(t, entry.InputHistoryParsed, "master switch must drop parsed content")
			assert.Empty(t, entry.RawRequest, "master switch must drop raw request")
			assert.Empty(t, entry.RawResponse, "master switch must drop raw response")
			assert.True(t, entry.ContentHidden, "master switch must mark entry hidden")
		})
	}
}
