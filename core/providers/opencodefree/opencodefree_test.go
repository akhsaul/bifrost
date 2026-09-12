package opencodefree

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"sync"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

// Compile-time check that opencodeFreeProvider satisfies the full Provider interface.
var _ schemas.Provider = (*opencodeFreeProvider)(nil)

func TestOpencodeFreeProviderConstructor(t *testing.T) {
	cfg := &schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{},
	}
	provider, err := NewOpencodeFreeProvider(cfg, nil)
	if err != nil {
		t.Fatalf("NewOpencodeFreeProvider failed: %v", err)
	}

	if provider.GetProviderKey() != schemas.OpencodeFree {
		t.Errorf("expected provider key %s, got %s", schemas.OpencodeFree, provider.GetProviderKey())
	}
	if provider.networkConfig.BaseURL != DefaultBaseURL {
		t.Errorf("expected base URL %s, got %s", DefaultBaseURL, provider.networkConfig.BaseURL)
	}
}

func TestOpencodeFreeMandatoryHeadersAndReasoning(t *testing.T) {
	type capturedRequest struct {
		method  string
		path    string
		headers map[string]string
		body    map[string]any
	}

	var (
		mu       sync.Mutex
		captures []capturedRequest
	)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bodyBytes, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		var payload map[string]any
		_ = json.Unmarshal(bodyBytes, &payload)

		capturedHeaders := make(map[string]string)
		for k, v := range r.Header {
			if len(v) > 0 {
				capturedHeaders[k] = v[0]
			}
		}

		mu.Lock()
		captures = append(captures, capturedRequest{
			method:  r.Method,
			path:    r.URL.Path,
			headers: capturedHeaders,
			body:    payload,
		})
		mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"id":"resp_123","object":"response","model":"muse-spark-1.3-contributor-free","output":[]}`)
	}))
	defer server.Close()

	cfg := &schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{
			BaseURL: server.URL,
		},
	}
	provider, err := NewOpencodeFreeProvider(cfg, nil)
	if err != nil {
		t.Fatalf("NewOpencodeFreeProvider failed: %v", err)
	}

	ctx := schemas.NewBifrostContext(nil, time.Time{})
	req := &schemas.BifrostResponsesRequest{
		Model: "muse-spark-1.3-contributor-free",
		Input: []schemas.ResponsesMessage{
			{
				Role: schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
				Content: &schemas.ResponsesMessageContent{
					ContentStr: schemas.Ptr("who are you?"),
				},
			},
		},
	}

	resp, bErr := provider.Responses(ctx, schemas.Key{}, req)
	if bErr != nil {
		t.Fatalf("Responses request failed: %v", bErr.Error)
	}
	if resp == nil || resp.ID == nil || *resp.ID != "resp_123" {
		t.Fatalf("unexpected response: %+v", resp)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(captures) != 1 {
		t.Fatalf("expected 1 captured request, got %d", len(captures))
	}

	capReq := captures[0]
	if capReq.path != "/v1/responses" {
		t.Errorf("expected path /v1/responses, got %s", capReq.path)
	}

	// Verify mandatory headers
	expectedHeaders := map[string]string{
		"Http-Referer":                "https://hermes-agent.nousresearch.com",
		"User-Agent":                  "HermesAgent/0.21.2",
		"X-Stainless-Arch":            "x64",
		"X-Stainless-Async":           "false",
		"X-Stainless-Lang":            "python",
		"X-Stainless-Os":              "Linux",
		"X-Stainless-Package-Version": "2.24.0",
		"X-Stainless-Read-Timeout":    "1800.0",
		"X-Stainless-Retry-Count":     "0",
		"X-Stainless-Runtime":         "CPython",
		"X-Stainless-Runtime-Version": "3.11.16",
		"X-Title":                     "Hermes Agent",
	}

	for k, expectedVal := range expectedHeaders {
		gotVal, ok := capReq.headers[k]
		if !ok || gotVal != expectedVal {
			t.Errorf("header %q = %q (present=%v), want %q", k, gotVal, ok, expectedVal)
		}
	}

	// Dynamic x-opencode-session header regex check: YYYYMMDD_HHMMSS_<6 hex chars>
	sessionHdr, hasSession := capReq.headers["X-Opencode-Session"]
	if !hasSession {
		t.Errorf("missing x-opencode-session header")
	} else {
		re := regexp.MustCompile(`^\d{8}_\d{6}_[a-f0-9]{6}$`)
		if !re.MatchString(sessionHdr) {
			t.Errorf("x-opencode-session %q does not match regex %s", sessionHdr, re.String())
		}
	}

	// Verify reasoning and store injected into request body
	reasoningObj, hasReasoning := capReq.body["reasoning"].(map[string]any)
	if !hasReasoning {
		t.Fatalf("request body missing 'reasoning' object: %+v", capReq.body)
	}
	if reasoningObj["effort"] != "high" {
		t.Errorf("expected reasoning.effort 'high', got %v", reasoningObj["effort"])
	}
	if reasoningObj["summary"] != "auto" {
		t.Errorf("expected reasoning.summary 'auto', got %v", reasoningObj["summary"])
	}

	storeVal, hasStore := capReq.body["store"].(bool)
	if !hasStore || storeVal != false {
		t.Errorf("expected store: false, got %v (present=%v)", storeVal, hasStore)
	}
}

func TestOpencodeFreeStreamingHeaders(t *testing.T) {
	var capturedAccept string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedAccept = r.Header.Get("Accept")
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"sequence_number\":1,\"response\":{\"id\":\"resp_stream\",\"object\":\"response\",\"model\":\"muse-spark-1.3-contributor-free\",\"output\":[]}}\n\n")
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
	}))
	defer server.Close()

	cfg := &schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{
			BaseURL: server.URL,
		},
	}
	provider, err := NewOpencodeFreeProvider(cfg, nil)
	if err != nil {
		t.Fatalf("NewOpencodeFreeProvider failed: %v", err)
	}

	ctx := schemas.NewBifrostContext(nil, time.Time{})
	req := &schemas.BifrostResponsesRequest{
		Model: "muse-spark-1.3-contributor-free",
		Input: []schemas.ResponsesMessage{
			{
				Role: schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
				Content: &schemas.ResponsesMessageContent{
					ContentStr: schemas.Ptr("streaming test"),
				},
			},
		},
	}

	postHookRunner := func(_ *schemas.BifrostContext, result *schemas.BifrostResponse, _ *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError) {
		return result, nil
	}
	stream, bErr := provider.ResponsesStream(ctx, postHookRunner, nil, schemas.Key{}, req)
	if bErr != nil {
		t.Fatalf("ResponsesStream failed: %v", bErr.Error)
	}
	if stream != nil {
		for range stream {
		}
	}

	if capturedAccept != "text/event-stream" {
		t.Errorf("streaming accept header = %q, want text/event-stream", capturedAccept)
	}
}

func TestOpencodeFreeChatCompletionAndReasoning(t *testing.T) {
	var (
		mu         sync.Mutex
		calledPath string
		body       map[string]any
	)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bodyBytes, _ := io.ReadAll(r.Body)
		var payload map[string]any
		_ = json.Unmarshal(bodyBytes, &payload)

		mu.Lock()
		calledPath = r.URL.Path
		body = payload
		mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"id":"resp_123","object":"response","status":"completed","model":"muse-spark-1.3-contributor-free","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"hello"}]}]}`)
	}))
	defer server.Close()

	cfg := &schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{
			BaseURL: server.URL,
		},
	}
	provider, err := NewOpencodeFreeProvider(cfg, nil)
	if err != nil {
		t.Fatalf("NewOpencodeFreeProvider failed: %v", err)
	}

	ctx := schemas.NewBifrostContext(nil, time.Time{})
	chatReq := &schemas.BifrostChatRequest{
		Model: "muse-spark-1.3-contributor-free",
		Input: []schemas.ChatMessage{
			{
				Role: schemas.ChatMessageRoleUser,
				Content: &schemas.ChatMessageContent{
					ContentStr: schemas.Ptr("hello"),
				},
			},
		},
	}

	resp, bErr := provider.ChatCompletion(ctx, schemas.Key{}, chatReq)
	if bErr != nil {
		t.Fatalf("ChatCompletion failed: %v", bErr.Error)
	}
	if resp == nil || len(resp.Choices) == 0 {
		t.Fatalf("unexpected ChatCompletion response: %+v", resp)
	}
	choice := resp.Choices[0]
	if choice.ChatNonStreamResponseChoice == nil || choice.ChatNonStreamResponseChoice.Message == nil || choice.ChatNonStreamResponseChoice.Message.Content == nil || choice.ChatNonStreamResponseChoice.Message.Content.ContentStr == nil || *choice.ChatNonStreamResponseChoice.Message.Content.ContentStr != "hello" {
		t.Fatalf("unexpected ChatCompletion response content: %+v", resp)
	}

	mu.Lock()
	defer mu.Unlock()
	if calledPath != "/v1/responses" {
		t.Errorf("expected path /v1/responses, got %s", calledPath)
	}
	// Verify input array exists (Responses API format)
	if _, hasInput := body["input"]; !hasInput {
		t.Errorf("expected 'input' field in request body, got %v", body)
	}
	// Verify reasoning effort and summary
	reasoningObj, hasReasoning := body["reasoning"].(map[string]any)
	if !hasReasoning {
		t.Fatalf("request body missing 'reasoning' object: %+v", body)
	}
	if reasoningObj["effort"] != "high" {
		t.Errorf("expected reasoning.effort 'high', got %v", reasoningObj["effort"])
	}
	if reasoningObj["summary"] != "auto" {
		t.Errorf("expected reasoning.summary 'auto', got %v", reasoningObj["summary"])
	}
	if storeVal, hasStore := body["store"].(bool); !hasStore || storeVal != false {
		t.Errorf("expected store: false, got %v (present=%v)", storeVal, hasStore)
	}
}

func TestOpencodeFreeChatCompletionStreaming(t *testing.T) {
	var (
		mu         sync.Mutex
		calledPath string
	)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calledPath = r.URL.Path
		mu.Unlock()

		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "data: {\"type\":\"response.output_item.added\",\"sequence_number\":1,\"item\":{\"type\":\"message\",\"role\":\"assistant\"}}\n\n")
		_, _ = fmt.Fprint(w, "data: {\"type\":\"response.output_text.delta\",\"sequence_number\":2,\"delta\":\"hello stream\"}\n\n")
		_, _ = fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"sequence_number\":3,\"response\":{\"id\":\"resp_stream\",\"object\":\"response\",\"status\":\"completed\",\"model\":\"muse-spark-1.3-contributor-free\",\"output\":[]}}\n\n")
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
	}))
	defer server.Close()

	cfg := &schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{
			BaseURL: server.URL,
		},
	}
	provider, err := NewOpencodeFreeProvider(cfg, nil)
	if err != nil {
		t.Fatalf("NewOpencodeFreeProvider failed: %v", err)
	}

	ctx := schemas.NewBifrostContext(nil, time.Time{})
	chatReq := &schemas.BifrostChatRequest{
		Model: "muse-spark-1.3-contributor-free",
		Input: []schemas.ChatMessage{
			{
				Role: schemas.ChatMessageRoleUser,
				Content: &schemas.ChatMessageContent{
					ContentStr: schemas.Ptr("streaming test"),
				},
			},
		},
	}

	postHookRunner := func(_ *schemas.BifrostContext, result *schemas.BifrostResponse, _ *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError) {
		return result, nil
	}
	stream, bErr := provider.ChatCompletionStream(ctx, postHookRunner, nil, schemas.Key{}, chatReq)
	if bErr != nil {
		t.Fatalf("ChatCompletionStream failed: %v", bErr.Error)
	}
	if stream == nil {
		t.Fatal("expected non-nil stream channel")
	}

	var chunks []*schemas.BifrostStreamChunk
	for chunk := range stream {
		chunks = append(chunks, chunk)
	}

	mu.Lock()
	defer mu.Unlock()
	if calledPath != "/v1/responses" {
		t.Errorf("expected path /v1/responses, got %s", calledPath)
	}

	foundDelta := false
	for _, ch := range chunks {
		if ch.BifrostChatResponse != nil && len(ch.BifrostChatResponse.Choices) > 0 {
			choice := ch.BifrostChatResponse.Choices[0]
			if choice.ChatStreamResponseChoice != nil && choice.ChatStreamResponseChoice.Delta != nil && choice.ChatStreamResponseChoice.Delta.Content != nil {
				if *choice.ChatStreamResponseChoice.Delta.Content == "hello stream" {
					foundDelta = true
				}
			}
		}
	}
	if !foundDelta {
		t.Errorf("did not find converted delta content 'hello stream' in stream chunks: %+v", chunks)
	}
}

func TestOpencodeFreeListModels(t *testing.T) {
	var (
		mu         sync.Mutex
		calledPath string
		headers    map[string]string
	)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calledPath = r.URL.Path
		headers = make(map[string]string)
		for k, vs := range r.Header {
			if len(vs) > 0 {
				headers[k] = vs[0]
			}
		}
		mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"object":"list","data":[{"id":"muse-spark-1.3-contributor-free","object":"model","created":1700000000,"owned_by":"opencode"}]}`)
	}))
	defer server.Close()

	cfg := &schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{
			BaseURL: server.URL,
		},
	}
	provider, err := NewOpencodeFreeProvider(cfg, nil)
	if err != nil {
		t.Fatalf("NewOpencodeFreeProvider failed: %v", err)
	}

	ctx := schemas.NewBifrostContext(nil, time.Time{})
	keys := []schemas.Key{
		{
			ID:     "opencode-free-default",
			Name:   "default",
			Weight: 1.0,
			Models: schemas.WhiteList{"*"},
		},
	}
	resp, bErr := provider.ListModels(ctx, keys, &schemas.BifrostListModelsRequest{})
	if bErr != nil {
		t.Fatalf("ListModels failed: %v", bErr.Error)
	}
	if resp == nil || len(resp.Data) != 1 || resp.Data[0].ID != "opencode-free/muse-spark-1.3-contributor-free" {
		t.Fatalf("unexpected ListModels response: %+v", resp)
	}

	mu.Lock()
	defer mu.Unlock()
	if calledPath != "/v1/models" {
		t.Errorf("expected path /v1/models, got %s", calledPath)
	}
	// Verify mandatory authorization header presence (empty string)
	if auth, exists := headers["Authorization"]; !exists || auth != "" {
		t.Errorf("expected Authorization header to be empty string, got %q (exists=%v)", auth, exists)
	}
	// Verify mandatory telemetry / identity headers
	if headers["User-Agent"] != DefaultUserAgent {
		t.Errorf("expected User-Agent %q, got %q", DefaultUserAgent, headers["User-Agent"])
	}
	if _, hasSession := headers["X-Opencode-Session"]; !hasSession {
		t.Errorf("expected X-Opencode-Session header to be present")
	}
}

func TestOpencodeFreeListModelsKeyless(t *testing.T) {
	var (
		mu         sync.Mutex
		calledPath string
	)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calledPath = r.URL.Path
		mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"object":"list","data":[{"id":"muse-spark-1.3-contributor-free","object":"model","created":1700000000,"owned_by":"opencode"}]}`)
	}))
	defer server.Close()

	cfg := &schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{
			BaseURL: server.URL,
		},
	}
	provider, err := NewOpencodeFreeProvider(cfg, nil)
	if err != nil {
		t.Fatalf("NewOpencodeFreeProvider failed: %v", err)
	}

	ctx := schemas.NewBifrostContext(nil, time.Time{})
	// Zero keys — the keyless path must still hit /v1/models and surface a
	// provider-level KeyStatus with the empty sentinel KeyID.
	resp, bErr := provider.ListModels(ctx, nil, &schemas.BifrostListModelsRequest{})
	if bErr != nil {
		t.Fatalf("ListModels (keyless) failed: %v", bErr.Error)
	}
	if resp == nil || len(resp.Data) != 1 || resp.Data[0].ID != "opencode-free/muse-spark-1.3-contributor-free" {
		t.Fatalf("unexpected keyless ListModels response: %+v", resp)
	}
	if len(resp.KeyStatuses) != 1 || resp.KeyStatuses[0].KeyID != "" || resp.KeyStatuses[0].Provider != schemas.OpencodeFree {
		t.Fatalf("expected one provider-level KeyStatus with empty KeyID, got %+v", resp.KeyStatuses)
	}

	mu.Lock()
	defer mu.Unlock()
	if calledPath != "/v1/models" {
		t.Errorf("expected path /v1/models, got %s", calledPath)
	}
}

func TestOpencodeFreeErrorParsing(t *testing.T) {
	resp := &fasthttp.Response{}
	resp.SetStatusCode(fasthttp.StatusBadRequest)
	resp.SetBodyString(`{"type": "error", "error": {"type": "invalid_request", "message": "unknown free model"}}`)

	bErr := parseOpencodeFreeError(resp)
	if bErr == nil {
		t.Fatal("expected non-nil BifrostError")
	}
	if bErr.Error.Message != "unknown free model" {
		t.Errorf("expected error message 'unknown free model', got %q", bErr.Error.Message)
	}
	if bErr.Error.Type == nil || *bErr.Error.Type != "invalid_request" {
		t.Errorf("expected error type 'invalid_request', got %v", bErr.Error.Type)
	}
}

func TestOpencodeFreeUnsupportedOperations(t *testing.T) {
	cfg := &schemas.ProviderConfig{}
	p, err := NewOpencodeFreeProvider(cfg, nil)
	if err != nil {
		t.Fatalf("NewOpencodeFreeProvider failed: %v", err)
	}

	ctx := schemas.NewBifrostContext(nil, time.Time{})
	if _, bErr := p.TextCompletion(ctx, schemas.Key{}, nil); bErr == nil {
		t.Error("expected error for unsupported TextCompletion")
	}
	if _, bErr := p.Embedding(ctx, schemas.Key{}, nil); bErr == nil {
		t.Error("expected error for unsupported Embedding")
	}
	if _, bErr := p.Speech(ctx, schemas.Key{}, nil); bErr == nil {
		t.Error("expected error for unsupported Speech")
	}
	if _, bErr := p.ImageGeneration(ctx, schemas.Key{}, nil); bErr == nil {
		t.Error("expected error for unsupported ImageGeneration")
	}
	if _, bErr := p.CountTokens(ctx, schemas.Key{}, nil); bErr == nil {
		t.Error("expected error for unsupported CountTokens")
	}
}
