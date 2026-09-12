package logging

import (
	"context"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
)

// contentLoggingOnErrorTestPlugin builds a plugin with the given global toggles for
// content-logging-on-error tests.
func contentLoggingOnErrorTestPlugin(disableContentLogging, contentLoggingOnError *bool) *LoggerPlugin {
	return &LoggerPlugin{
		disableContentLogging:  disableContentLogging,
		contentLoggingOnError: contentLoggingOnError,
		logger:                 testLogger{},
	}
}

// onErrCtx builds a context carrying the request id and the content-logging-on-error
// signal the HTTP transport stamps from live client config.
func onErrCtx(requestID string, onErr bool) *schemas.BifrostContext {
	ctx := schemas.NewBifrostContext(context.Background(), time.Time{})
	ctx.SetValue(schemas.BifrostContextKeyRequestID, requestID)
	ctx.SetValue(schemas.BifrostContextKeyContentLoggingOnError, onErr)
	return ctx
}

// TestIsContentLoggingOnErrorEnabled_GlobalConfig verifies the global config pointer
// enables the mode without any context signal.
func TestIsContentLoggingOnErrorEnabled_GlobalConfig(t *testing.T) {
	p := contentLoggingOnErrorTestPlugin(nil, boolPtr(true))
	assert.True(t, p.isContentLoggingOnErrorEnabled(nil))

	p = contentLoggingOnErrorTestPlugin(nil, boolPtr(false))
	assert.False(t, p.isContentLoggingOnErrorEnabled(nil))

	p = contentLoggingOnErrorTestPlugin(nil, nil)
	assert.False(t, p.isContentLoggingOnErrorEnabled(nil))
}

// TestIsContentLoggingOnErrorEnabled_ContextSignal verifies the per-request context
// signal (set by the transport from live config) enables the mode even when the
// plugin-level pointer is nil.
func TestIsContentLoggingOnErrorEnabled_ContextSignal(t *testing.T) {
	p := contentLoggingOnErrorTestPlugin(nil, nil)

	ctx := onErrCtx("req-1", true)
	assert.True(t, p.isContentLoggingOnErrorEnabled(ctx))

	ctx = onErrCtx("req-2", false)
	assert.False(t, p.isContentLoggingOnErrorEnabled(ctx))
}

// TestPreLLMHookBuffersContentWhenOnlyOnErrorEnabled verifies the ingress phase still
// extracts input history when content logging is globally disabled but
// content_logging_on_error is enabled — without buffering there is nothing to persist
// later when the provider fails.
func TestPreLLMHookBuffersContentWhenOnlyOnErrorEnabled(t *testing.T) {
	store := newTestStore(t)
	defer store.Close(context.Background())
	plugin, err := Init(context.Background(), &Config{}, testLogger{}, store, nil, nil, nil)
	if err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	t.Cleanup(func() {
		if cleanupErr := plugin.Cleanup(); cleanupErr != nil {
			t.Errorf("Cleanup() error = %v", cleanupErr)
		}
	})

	ctx := onErrCtx("req-buffer-on-error", true)
	userMsg := "hello this prompt caused an error"
	_, _, err = plugin.PreLLMHook(ctx, &schemas.BifrostRequest{
		RequestType: schemas.ChatCompletionRequest,
		ChatRequest: &schemas.BifrostChatRequest{
			Provider: schemas.OpenAI,
			Model:    "gpt-4o-mini",
			Input: []schemas.ChatMessage{
				{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: &userMsg}},
			},
			Params: &schemas.ChatParameters{},
		},
	})
	if err != nil {
		t.Fatalf("PreLLMHook() error = %v", err)
	}

	pendingVal, ok := plugin.pendingLogsEntries.Load("req-buffer-on-error")
	if !ok {
		t.Fatal("expected pending log entry to be buffered")
	}
	pending := pendingVal.(*PendingLogData)
	if len(pending.InitialData.InputHistory) == 0 {
		t.Fatal("expected input history to be buffered for content-logging-on-error")
	}
	assert.Equal(t, userMsg, *pending.InitialData.InputHistory[0].Content.ContentStr)
}

// TestPostLLMHookErrorKeepsContentWithContentLoggingDisabled is the core wire-visible
// behaviour of content_logging_on_error: an error entry stores the input history and
// error details even when disable_content_logging is true, while success entries stay
// content-free.
func TestPostLLMHookErrorKeepsContentWithContentLoggingDisabled(t *testing.T) {
	t.Run("error_path_retains_content_and_raw", func(t *testing.T) {
		store := newTestStore(t)
		defer store.Close(context.Background())
		plugin, err := Init(context.Background(), &Config{
			DisableContentLogging:  boolPtr(true),
			ContentLoggingOnError: boolPtr(true),
		}, testLogger{}, store, nil, nil, nil)
		if err != nil {
			t.Fatalf("Init() error = %v", err)
		}
		t.Cleanup(func() {
			_ = plugin.Cleanup()
		})

		ctx := onErrCtx("req-onerr-error", true)
		userMsg := "prompt that triggered provider failure"
		_, _, err = plugin.PreLLMHook(ctx, &schemas.BifrostRequest{
			RequestType: schemas.ChatCompletionRequest,
			ChatRequest: &schemas.BifrostChatRequest{
				Provider: schemas.OpenAI,
				Model:    "gpt-4o-mini",
				Input: []schemas.ChatMessage{
					{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: &userMsg}},
				},
				Params: &schemas.ChatParameters{},
			},
		})
		if err != nil {
			t.Fatalf("PreLLMHook() error = %v", err)
		}

		statusCode := 500
		rawReqBody := map[string]interface{}{"model": "gpt-4o-mini", "messages": []interface{}{map[string]interface{}{"role": "user", "content": userMsg}}}
		rawRespBody := map[string]interface{}{"error": map[string]interface{}{"message": "internal server error"}}
		bifrostErr := &schemas.BifrostError{
			IsBifrostError: true,
			StatusCode:     &statusCode,
			Error:          &schemas.ErrorField{Message: "provider failed"},
			ExtraFields: schemas.BifrostErrorExtraFields{
				RequestType:  schemas.ChatCompletionRequest,
				Provider:     schemas.OpenAI,
				RawRequest:  rawReqBody,
				RawResponse: rawRespBody,
			},
		}

		_, _, err = plugin.PostLLMHook(ctx, nil, bifrostErr)
		if err != nil {
			t.Fatalf("PostLLMHook() error = %v", err)
		}
		if err := plugin.Cleanup(); err != nil {
			t.Fatalf("Cleanup() error = %v", err)
		}

		entry, err := store.FindByID(context.Background(), "req-onerr-error")
		if err != nil {
			t.Fatalf("FindByID() error = %v", err)
		}
		if entry.Status != "error" {
			t.Fatalf("expected error status, got %q", entry.Status)
		}
		if entry.ContentHidden {
			t.Fatal("error entry under content_logging_on_error must not be content-hidden")
		}
		if len(entry.InputHistoryParsed) == 0 {
			t.Fatal("error entry under content_logging_on_error must retain input history")
		}
		assert.Equal(t, userMsg, *entry.InputHistoryParsed[0].Content.ContentStr)
		if entry.ErrorDetailsParsed == nil {
			t.Fatal("error entry must carry error details")
		}
		if entry.RawRequest == "" {
			t.Fatal("error entry under content_logging_on_error must retain raw request bytes regardless of provider store_raw_request_response")
		}
		if entry.RawResponse == "" {
			t.Fatal("error entry under content_logging_on_error must retain raw response bytes regardless of provider store_raw_request_response")
		}
	})

	t.Run("success_path_strips_content", func(t *testing.T) {
		store := newTestStore(t)
		defer store.Close(context.Background())
		plugin, err := Init(context.Background(), &Config{
			DisableContentLogging:  boolPtr(true),
			ContentLoggingOnError: boolPtr(true),
		}, testLogger{}, store, nil, nil, nil)
		if err != nil {
			t.Fatalf("Init() error = %v", err)
		}
		t.Cleanup(func() {
			_ = plugin.Cleanup()
		})

		ctx2 := onErrCtx("req-onerr-success", true)
		successMsg := "successful prompt that should not be logged"
		_, _, err = plugin.PreLLMHook(ctx2, &schemas.BifrostRequest{
			RequestType: schemas.ChatCompletionRequest,
			ChatRequest: &schemas.BifrostChatRequest{
				Provider: schemas.OpenAI,
				Model:    "gpt-4o-mini",
				Input: []schemas.ChatMessage{
					{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: &successMsg}},
				},
				Params: &schemas.ChatParameters{},
			},
		})
		if err != nil {
			t.Fatalf("PreLLMHook() error = %v", err)
		}

		assistantText := "successful answer"
		chatResp := &schemas.BifrostResponse{
			ChatResponse: &schemas.BifrostChatResponse{
				ID:     "chatcmpl-1",
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
				},
			},
		}
		_, _, err = plugin.PostLLMHook(ctx2, chatResp, nil)
		if err != nil {
			t.Fatalf("PostLLMHook() error = %v", err)
		}
		if err := plugin.Cleanup(); err != nil {
			t.Fatalf("Cleanup() error = %v", err)
		}

		entry2, err := store.FindByID(context.Background(), "req-onerr-success")
		if err != nil {
			t.Fatalf("FindByID() error = %v", err)
		}
		if entry2.Status != "success" {
			t.Fatalf("expected success status, got %q", entry2.Status)
		}
		if len(entry2.InputHistoryParsed) != 0 {
			t.Fatalf("success entry under content_logging_on_error must strip input history, got %d messages", len(entry2.InputHistoryParsed))
		}
		if entry2.OutputMessage != "" {
			t.Fatal("success entry under content_logging_on_error must strip output message")
		}
	})
}

// TestPostLLMHookDisabledModeKeepsLegacyBehaviour verifies that without
// content_logging_on_error, a globally disabled content logging still drops content
// from error entries (the pre-existing behaviour).
func TestPostLLMHookDisabledModeKeepsLegacyBehaviour(t *testing.T) {
	store := newTestStore(t)
	defer store.Close(context.Background())
	plugin, err := Init(context.Background(), &Config{
		DisableContentLogging: boolPtr(true),
	}, testLogger{}, store, nil, nil, nil)
	if err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	t.Cleanup(func() {
		if cleanupErr := plugin.Cleanup(); cleanupErr != nil {
			t.Errorf("Cleanup() error = %v", cleanupErr)
		}
	})

	ctx := onErrCtx("req-legacy-error", false)
	userMsg := "prompt under legacy behaviour"
	_, _, err = plugin.PreLLMHook(ctx, &schemas.BifrostRequest{
		RequestType: schemas.ChatCompletionRequest,
		ChatRequest: &schemas.BifrostChatRequest{
			Provider: schemas.OpenAI,
			Model:    "gpt-4o-mini",
			Input: []schemas.ChatMessage{
				{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: &userMsg}},
			},
			Params: &schemas.ChatParameters{},
		},
	})
	if err != nil {
		t.Fatalf("PreLLMHook() error = %v", err)
	}

	statusCode := 500
	bifrostErr := &schemas.BifrostError{
		IsBifrostError: true,
		StatusCode:     &statusCode,
		Error:          &schemas.ErrorField{Message: "provider failed"},
		ExtraFields: schemas.BifrostErrorExtraFields{
			RequestType: schemas.ChatCompletionRequest,
			Provider:    schemas.OpenAI,
		},
	}
	_, _, err = plugin.PostLLMHook(ctx, nil, bifrostErr)
	if err != nil {
		t.Fatalf("PostLLMHook() error = %v", err)
	}
	if err := plugin.Cleanup(); err != nil {
		t.Fatalf("Cleanup() error = %v", err)
	}

	entry, err := store.FindByID(context.Background(), "req-legacy-error")
	if err != nil {
		t.Fatalf("FindByID() error = %v", err)
	}
	if entry.Status != "error" {
		t.Fatalf("expected error status, got %q", entry.Status)
	}
	if len(entry.InputHistoryParsed) != 0 {
		t.Fatalf("legacy mode must keep dropping content on error entries, got %d messages", len(entry.InputHistoryParsed))
	}
}

// TestPostLLMHookSuccessStripsContentEvenIfGlobalDisableIsFalse verifies that when
// content_logging_on_error is true, even if disable_content_logging is false (the
// default), successful requests strip their content so that content is logged ONLY on
// error.
func TestPostLLMHookSuccessStripsContentEvenIfGlobalDisableIsFalse(t *testing.T) {
	store := newTestStore(t)
	defer store.Close(context.Background())
	plugin, err := Init(context.Background(), &Config{
		DisableContentLogging:  boolPtr(false),
		ContentLoggingOnError: boolPtr(true),
	}, testLogger{}, store, nil, nil, nil)
	if err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	t.Cleanup(func() {
		_ = plugin.Cleanup()
	})

	ctx := onErrCtx("req-global-false-success", true)
	successMsg := "should be stripped because on_error mode only logs errors"
	_, _, err = plugin.PreLLMHook(ctx, &schemas.BifrostRequest{
		RequestType: schemas.ChatCompletionRequest,
		ChatRequest: &schemas.BifrostChatRequest{
			Provider: schemas.OpenAI,
			Model:    "gpt-4o-mini",
			Input: []schemas.ChatMessage{
				{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: &successMsg}},
			},
			Params: &schemas.ChatParameters{},
		},
	})
	if err != nil {
		t.Fatalf("PreLLMHook() error = %v", err)
	}

	assistantText := "answer"
	chatResp := &schemas.BifrostResponse{
		ChatResponse: &schemas.BifrostChatResponse{
			ID:     "chatcmpl-2",
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
			},
		},
	}
	_, _, err = plugin.PostLLMHook(ctx, chatResp, nil)
	if err != nil {
		t.Fatalf("PostLLMHook() error = %v", err)
	}
	if err := plugin.Cleanup(); err != nil {
		t.Fatalf("Cleanup() error = %v", err)
	}

	entry, err := store.FindByID(context.Background(), "req-global-false-success")
	if err != nil {
		t.Fatalf("FindByID() error = %v", err)
	}
	if entry.Status != "success" {
		t.Fatalf("expected success status, got %q", entry.Status)
	}
	if len(entry.InputHistoryParsed) != 0 {
		t.Fatalf("success entry under content_logging_on_error must strip input history even when disable_content_logging=false, got %d messages", len(entry.InputHistoryParsed))
	}
	if entry.OutputMessage != "" {
		t.Fatal("success entry under content_logging_on_error must strip output message")
	}
}
