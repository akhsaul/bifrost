package opencodezenfree

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

// Compile-time check that opencodeZenFreeProvider satisfies the full Provider interface.
var _ schemas.Provider = (*opencodeZenFreeProvider)(nil)

func TestOpencodeZenFreeProviderConstructor(t *testing.T) {
	cfg := &schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{},
	}
	provider, err := NewOpencodeZenFreeProvider(cfg, nil)
	if err != nil {
		t.Fatalf("NewOpencodeZenFreeProvider failed: %v", err)
	}

	if provider.GetProviderKey() != schemas.OpencodeZenFree {
		t.Errorf("expected provider key %s, got %s", schemas.OpencodeZenFree, provider.GetProviderKey())
	}
	if provider.networkConfig.BaseURL != DefaultBaseURL {
		t.Errorf("expected base URL %s, got %s", DefaultBaseURL, provider.networkConfig.BaseURL)
	}
}

func TestOpencodeZenFreeMandatoryHeadersAndReasoning(t *testing.T) {
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
	provider, err := NewOpencodeZenFreeProvider(cfg, nil)
	if err != nil {
		t.Fatalf("NewOpencodeZenFreeProvider failed: %v", err)
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

	// Verify headers match the working opencode capture
	expectedHeaders := map[string]string{
		"Authorization":     "Bearer public",
		"User-Agent":        DefaultUserAgent,
		"X-Opencode-Client": "cli",
	}

	for k, expectedVal := range expectedHeaders {
		gotVal, ok := capReq.headers[k]
		if !ok || gotVal != expectedVal {
			t.Errorf("header %q = %q (present=%v), want %q", k, gotVal, ok, expectedVal)
		}
	}
	for _, removed := range []string{
		"Http-Referer", "X-Title", "X-Stainless-Arch", "X-Stainless-Lang",
		"X-Stainless-Os", "X-Stainless-Runtime",
	} {
		if _, ok := capReq.headers[removed]; ok {
			t.Errorf("header %q must not be sent", removed)
		}
	}

	// Dynamic x-opencode-session header: ses_<12hex><14base62>; the trio must agree.
	sessionHdr, hasSession := capReq.headers["X-Opencode-Session"]
	if !hasSession {
		t.Errorf("missing x-opencode-session header")
	} else {
		re := regexp.MustCompile(`^ses_[0-9a-f]{12}[0-9A-Za-z]{14}$`)
		if !re.MatchString(sessionHdr) {
			t.Errorf("x-opencode-session %q does not match regex %s", sessionHdr, re.String())
		}
		if capReq.headers["X-Session-Affinity"] != sessionHdr || capReq.headers["X-Session-Id"] != sessionHdr {
			t.Errorf("session trio diverged: session=%q affinity=%q id=%q",
				sessionHdr, capReq.headers["X-Session-Affinity"], capReq.headers["X-Session-Id"])
		}
	}
	if _, ok := capReq.headers["X-Opencode-Project"]; !ok {
		t.Errorf("missing x-opencode-project header")
	}
	// traceparent/b3 must share trace-id and span-id.
	traceparent, hasTP := capReq.headers["Traceparent"]
	b3, hasB3 := capReq.headers["B3"]
	if !hasTP || !hasB3 {
		t.Errorf("missing trace headers (traceparent=%v b3=%v)", hasTP, hasB3)
	} else {
		parts := strings.Split(traceparent, "-")
		if len(parts) != 4 || !strings.HasPrefix(b3, parts[1]+"-"+parts[2]+"-1-") {
			t.Errorf("trace headers diverged: traceparent=%q b3=%q", traceparent, b3)
		}
	}

	// Verify reasoning and store injected into request body
	reasoningObj, hasReasoning := capReq.body["reasoning"].(map[string]any)
	if !hasReasoning {
		t.Fatalf("request body missing 'reasoning' object: %+v", capReq.body)
	}
	if reasoningObj["effort"] != "auto" {
		t.Errorf("expected reasoning.effort 'auto', got %v", reasoningObj["effort"])
	}
	if reasoningObj["summary"] != "auto" {
		t.Errorf("expected reasoning.summary 'auto', got %v", reasoningObj["summary"])
	}

	storeVal, hasStore := capReq.body["store"].(bool)
	if !hasStore || storeVal != false {
		t.Errorf("expected store: false, got %v (present=%v)", storeVal, hasStore)
	}

	// Verify instructions, prompt_cache_key (== session), and dummy tools
	if capReq.body["instructions"] != DefaultInstructions {
		t.Errorf("expected instructions %q, got %v", DefaultInstructions, capReq.body["instructions"])
	}
	if capReq.body["prompt_cache_key"] != sessionHdr {
		t.Errorf("expected prompt_cache_key == session %q, got %v", sessionHdr, capReq.body["prompt_cache_key"])
	}
	tools, hasTools := capReq.body["tools"].([]any)
	if !hasTools || len(tools) != 3 {
		t.Fatalf("expected 3 default tools, got %v (present=%v)", capReq.body["tools"], hasTools)
	}
	for i, want := range []string{"edit", "read", "shell"} {
		tool, ok := tools[i].(map[string]any)
		if !ok || tool["name"] != want || tool["type"] != "function" {
			t.Errorf("tool[%d] = %v, want function tool %q", i, tools[i], want)
		}
	}
}

func TestOpencodeZenFreeStreamingHeaders(t *testing.T) {
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
	provider, err := NewOpencodeZenFreeProvider(cfg, nil)
	if err != nil {
		t.Fatalf("NewOpencodeZenFreeProvider failed: %v", err)
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

func TestOpencodeZenFreeChatCompletionAndReasoning(t *testing.T) {
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
	provider, err := NewOpencodeZenFreeProvider(cfg, nil)
	if err != nil {
		t.Fatalf("NewOpencodeZenFreeProvider failed: %v", err)
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
	if reasoningObj["effort"] != "auto" {
		t.Errorf("expected reasoning.effort 'auto', got %v", reasoningObj["effort"])
	}
	if reasoningObj["summary"] != "auto" {
		t.Errorf("expected reasoning.summary 'auto', got %v", reasoningObj["summary"])
	}
	if storeVal, hasStore := body["store"].(bool); !hasStore || storeVal != false {
		t.Errorf("expected store: false, got %v (present=%v)", storeVal, hasStore)
	}
	if body["instructions"] != DefaultInstructions {
		t.Errorf("expected instructions %q, got %v", DefaultInstructions, body["instructions"])
	}
	if tools, ok := body["tools"].([]any); !ok || len(tools) != 3 {
		t.Errorf("expected 3 default tools on chat path, got %v", body["tools"])
	}
}

func TestOpencodeZenFreeChatCompletionStreaming(t *testing.T) {
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
	provider, err := NewOpencodeZenFreeProvider(cfg, nil)
	if err != nil {
		t.Fatalf("NewOpencodeZenFreeProvider failed: %v", err)
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

func TestOpencodeZenFreeListModels(t *testing.T) {
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
	provider, err := NewOpencodeZenFreeProvider(cfg, nil)
	if err != nil {
		t.Fatalf("NewOpencodeZenFreeProvider failed: %v", err)
	}

	ctx := schemas.NewBifrostContext(nil, time.Time{})
	keys := []schemas.Key{
		{
			ID:     "opencode-zen-free-default",
			Name:   "default",
			Weight: 1.0,
			Models: schemas.WhiteList{"*"},
		},
	}
	resp, bErr := provider.ListModels(ctx, keys, &schemas.BifrostListModelsRequest{})
	if bErr != nil {
		t.Fatalf("ListModels failed: %v", bErr.Error)
	}
	if resp == nil || len(resp.Data) != 1 || resp.Data[0].ID != "opencode-zen-free/muse-spark-1.3-contributor-free" {
		t.Fatalf("unexpected ListModels response: %+v", resp)
	}

	mu.Lock()
	defer mu.Unlock()
	if calledPath != "/v1/models" {
		t.Errorf("expected path /v1/models, got %s", calledPath)
	}
	// Verify headers match the working opencode capture
	if auth, exists := headers["Authorization"]; !exists || auth != DefaultAuth {
		t.Errorf("expected Authorization header %q, got %q (exists=%v)", DefaultAuth, auth, exists)
	}
	if headers["User-Agent"] != DefaultUserAgent {
		t.Errorf("expected User-Agent %q, got %q", DefaultUserAgent, headers["User-Agent"])
	}
	if headers["X-Opencode-Client"] != "cli" {
		t.Errorf("expected X-Opencode-Client cli, got %q", headers["X-Opencode-Client"])
	}
	if _, hasSession := headers["X-Opencode-Session"]; !hasSession {
		t.Errorf("expected X-Opencode-Session header to be present")
	}
	if _, ok := headers["X-Opencode-Project"]; !ok {
		t.Errorf("expected X-Opencode-Project header to be present")
	}
	if _, ok := headers["Traceparent"]; !ok {
		t.Errorf("expected Traceparent header to be present")
	}
	if _, ok := headers["B3"]; !ok {
		t.Errorf("expected B3 header to be present")
	}
}

func TestOpencodeZenFreeListModelsKeyless(t *testing.T) {
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
	provider, err := NewOpencodeZenFreeProvider(cfg, nil)
	if err != nil {
		t.Fatalf("NewOpencodeZenFreeProvider failed: %v", err)
	}

	ctx := schemas.NewBifrostContext(nil, time.Time{})
	// Zero keys — the keyless path must still hit /v1/models and surface a
	// provider-level KeyStatus with the empty sentinel KeyID.
	resp, bErr := provider.ListModels(ctx, nil, &schemas.BifrostListModelsRequest{})
	if bErr != nil {
		t.Fatalf("ListModels (keyless) failed: %v", bErr.Error)
	}
	if resp == nil || len(resp.Data) != 1 || resp.Data[0].ID != "opencode-zen-free/muse-spark-1.3-contributor-free" {
		t.Fatalf("unexpected keyless ListModels response: %+v", resp)
	}
	if len(resp.KeyStatuses) != 1 || resp.KeyStatuses[0].KeyID != "" || resp.KeyStatuses[0].Provider != schemas.OpencodeZenFree {
		t.Fatalf("expected one provider-level KeyStatus with empty KeyID, got %+v", resp.KeyStatuses)
	}

	mu.Lock()
	defer mu.Unlock()
	if calledPath != "/v1/models" {
		t.Errorf("expected path /v1/models, got %s", calledPath)
	}
}

func TestOpencodeZenFreeClientValuesWinOverDefaults(t *testing.T) {
	var body map[string]any

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bodyBytes, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(bodyBytes, &body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"id":"resp_123","object":"response","model":"muse-spark-1.3-contributor-free","output":[]}`)
	}))
	defer server.Close()

	cfg := &schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{
			BaseURL: server.URL,
		},
	}
	provider, err := NewOpencodeZenFreeProvider(cfg, nil)
	if err != nil {
		t.Fatalf("NewOpencodeZenFreeProvider failed: %v", err)
	}

	customName := "my_tool"
	ctx := schemas.NewBifrostContext(nil, time.Time{})
	req := &schemas.BifrostResponsesRequest{
		Model: "muse-spark-1.3-contributor-free",
		Input: []schemas.ResponsesMessage{
			{
				Role: schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
				Content: &schemas.ResponsesMessageContent{
					ContentStr: schemas.Ptr("hi"),
				},
			},
		},
		Params: &schemas.ResponsesParameters{
			Instructions:   schemas.Ptr("custom instructions"),
			PromptCacheKey: schemas.Ptr("custom-cache-key"),
			Reasoning: &schemas.ResponsesParametersReasoning{
				Effort:  schemas.Ptr("xhigh"),
				Summary: schemas.Ptr("detailed"),
			},
			Tools: []schemas.ResponsesTool{
				{
					Type: schemas.ResponsesToolTypeFunction,
					Name: &customName,
					ResponsesToolFunction: &schemas.ResponsesToolFunction{
						Strict: schemas.Ptr(false),
					},
				},
			},
		},
	}

	if _, bErr := provider.Responses(ctx, schemas.Key{}, req); bErr != nil {
		t.Fatalf("Responses request failed: %v", bErr.Error)
	}

	reasoningObj, ok := body["reasoning"].(map[string]any)
	if !ok {
		t.Fatalf("request body missing 'reasoning' object: %+v", body)
	}
	if reasoningObj["effort"] != "xhigh" {
		t.Errorf("client reasoning.effort must win, got %v", reasoningObj["effort"])
	}
	if reasoningObj["summary"] != "detailed" {
		t.Errorf("client reasoning.summary must win, got %v", reasoningObj["summary"])
	}
	if body["instructions"] != "custom instructions" {
		t.Errorf("client instructions must win, got %v", body["instructions"])
	}
	if body["prompt_cache_key"] != "custom-cache-key" {
		t.Errorf("client prompt_cache_key must win, got %v", body["prompt_cache_key"])
	}
	tools, ok := body["tools"].([]any)
	if !ok || len(tools) != 4 {
		t.Fatalf("client tools must win plus missing dummies filled (4 tools), got %v", body["tools"])
	}
	if tool, ok := tools[0].(map[string]any); !ok || tool["name"] != customName {
		t.Errorf("client tool must win, got %v", tools[0])
	}
	names := map[string]bool{}
	for _, item := range tools {
		if tool, ok := item.(map[string]any); ok {
			if name, ok := tool["name"].(string); ok {
				names[name] = true
			}
		}
	}
	for _, want := range []string{"edit", "read", "shell"} {
		if !names[want] {
			t.Errorf("missing dummy tool %q must have been filled in, got names %v", want, names)
		}
	}
}

func TestOpencodeZenFreePartialToolsMerge(t *testing.T) {
	var body map[string]any

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bodyBytes, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(bodyBytes, &body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"id":"resp_123","object":"response","model":"muse-spark-1.3-contributor-free","output":[]}`)
	}))
	defer server.Close()

	cfg := &schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{
			BaseURL: server.URL,
		},
	}
	provider, err := NewOpencodeZenFreeProvider(cfg, nil)
	if err != nil {
		t.Fatalf("NewOpencodeZenFreeProvider failed: %v", err)
	}

	// Client sends a REAL "read" tool plus an unrelated custom tool: only the
	// missing edit/shell dummies may be added, and the client's read must stay.
	clientReadDesc := "real read implementation"
	ctx := schemas.NewBifrostContext(nil, time.Time{})
	req := &schemas.BifrostResponsesRequest{
		Model: "muse-spark-1.3-contributor-free",
		Input: []schemas.ResponsesMessage{
			{
				Role: schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
				Content: &schemas.ResponsesMessageContent{
					ContentStr: schemas.Ptr("hi"),
				},
			},
		},
		Params: &schemas.ResponsesParameters{
			Tools: []schemas.ResponsesTool{
				{
					Type:        schemas.ResponsesToolTypeFunction,
					Name:        schemas.Ptr("read"),
					Description: schemas.Ptr(clientReadDesc),
					ResponsesToolFunction: &schemas.ResponsesToolFunction{
						Strict: schemas.Ptr(false),
					},
				},
				{
					Type: schemas.ResponsesToolTypeFunction,
					Name: schemas.Ptr("custom"),
					ResponsesToolFunction: &schemas.ResponsesToolFunction{
						Strict: schemas.Ptr(false),
					},
				},
			},
		},
	}

	if _, bErr := provider.Responses(ctx, schemas.Key{}, req); bErr != nil {
		t.Fatalf("Responses request failed: %v", bErr.Error)
	}

	tools, ok := body["tools"].([]any)
	if !ok || len(tools) != 4 {
		t.Fatalf("expected 4 tools (2 client + 2 missing dummies), got %v", body["tools"])
	}
	byName := map[string]map[string]any{}
	for _, item := range tools {
		tool, ok := item.(map[string]any)
		if !ok {
			t.Fatalf("tool entry is not an object: %v", item)
		}
		name, _ := tool["name"].(string)
		byName[name] = tool
	}
	if byName["read"]["description"] != clientReadDesc {
		t.Errorf("client 'read' tool must not be overwritten, got %v", byName["read"])
	}
	if byName["custom"] == nil {
		t.Errorf("client 'custom' tool must be preserved, got names %v", byName)
	}
	for _, want := range []string{"edit", "shell"} {
		if byName[want] == nil {
			t.Errorf("missing dummy %q must have been filled in", want)
		}
	}
}

func TestOpencodeZenFreeSystemPromptExtractedToInstructions(t *testing.T) {
	var body map[string]any

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bodyBytes, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(bodyBytes, &body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"id":"resp_123","object":"response","model":"muse-spark-1.3-contributor-free","output":[]}`)
	}))
	defer server.Close()

	cfg := &schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{
			BaseURL: server.URL,
		},
	}
	provider, err := NewOpencodeZenFreeProvider(cfg, nil)
	if err != nil {
		t.Fatalf("NewOpencodeZenFreeProvider failed: %v", err)
	}

	ctx := schemas.NewBifrostContext(nil, time.Time{})
	req := &schemas.BifrostResponsesRequest{
		Model: "muse-spark-1.3-contributor-free",
		Input: []schemas.ResponsesMessage{
			{
				Role: schemas.Ptr(schemas.ResponsesInputMessageRoleSystem),
				Content: &schemas.ResponsesMessageContent{
					ContentStr: schemas.Ptr("Be concise."),
				},
			},
			{
				Role: schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
				Content: &schemas.ResponsesMessageContent{
					ContentStr: schemas.Ptr("hi"),
				},
			},
		},
	}

	if _, bErr := provider.Responses(ctx, schemas.Key{}, req); bErr != nil {
		t.Fatalf("Responses request failed: %v", bErr.Error)
	}

	if body["instructions"] != "Be concise." {
		t.Errorf("system message must move to instructions, got %v", body["instructions"])
	}
	input, ok := body["input"].([]any)
	if !ok {
		t.Fatalf("expected input array, got %v", body["input"])
	}
	for _, item := range input {
		if msg, ok := item.(map[string]any); ok && msg["role"] == "system" {
			t.Errorf("system message must be removed from input, got %v", body["input"])
		}
	}
	if len(input) != 1 {
		t.Errorf("expected 1 remaining input message, got %v", body["input"])
	}
}

func TestOpencodeZenFreeExplicitInstructionsUntouched(t *testing.T) {
	var body map[string]any

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bodyBytes, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(bodyBytes, &body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"id":"resp_123","object":"response","model":"muse-spark-1.3-contributor-free","output":[]}`)
	}))
	defer server.Close()

	cfg := &schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{
			BaseURL: server.URL,
		},
	}
	provider, err := NewOpencodeZenFreeProvider(cfg, nil)
	if err != nil {
		t.Fatalf("NewOpencodeZenFreeProvider failed: %v", err)
	}

	ctx := schemas.NewBifrostContext(nil, time.Time{})
	req := &schemas.BifrostResponsesRequest{
		Model: "muse-spark-1.3-contributor-free",
		Input: []schemas.ResponsesMessage{
			{
				Role: schemas.Ptr(schemas.ResponsesInputMessageRoleSystem),
				Content: &schemas.ResponsesMessageContent{
					ContentStr: schemas.Ptr("system stays"),
				},
			},
			{
				Role: schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
				Content: &schemas.ResponsesMessageContent{
					ContentStr: schemas.Ptr("hi"),
				},
			},
		},
		Params: &schemas.ResponsesParameters{
			Instructions: schemas.Ptr("explicit"),
		},
	}

	if _, bErr := provider.Responses(ctx, schemas.Key{}, req); bErr != nil {
		t.Fatalf("Responses request failed: %v", bErr.Error)
	}

	if body["instructions"] != "explicit" {
		t.Errorf("explicit instructions must win, got %v", body["instructions"])
	}
	input, ok := body["input"].([]any)
	if !ok || len(input) != 2 {
		t.Errorf("input must be untouched when instructions are explicit, got %v", body["input"])
	}
}

func TestOpencodeZenFreeReasoningItemWithoutSummary(t *testing.T) {
	var body map[string]any

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bodyBytes, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(bodyBytes, &body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"id":"resp_123","object":"response","model":"muse-spark-1.3-contributor-free","output":[]}`)
	}))
	defer server.Close()

	cfg := &schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{
			BaseURL: server.URL,
		},
	}
	provider, err := NewOpencodeZenFreeProvider(cfg, nil)
	if err != nil {
		t.Fatalf("NewOpencodeZenFreeProvider failed: %v", err)
	}

	// Shape of opencode-req-bug.json: a reasoning item carrying only
	// reasoning_text content (no summary key) at input[2]. The upstream
	// rejects it with "`input[2]` missing required field `summary`".
	reasoningType := schemas.ResponsesMessageTypeReasoning
	reasoningText := "Let me investigate the codebase first."
	ctx := schemas.NewBifrostContext(nil, time.Time{})
	req := &schemas.BifrostResponsesRequest{
		Model: "muse-spark-1.3-contributor-free",
		Input: []schemas.ResponsesMessage{
			{
				Role: schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
				Content: &schemas.ResponsesMessageContent{
					ContentStr: schemas.Ptr("plan reminder"),
				},
			},
			{
				Role: schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
				Content: &schemas.ResponsesMessageContent{
					ContentStr: schemas.Ptr("help me fix embedding"),
				},
			},
			{
				ID:   schemas.Ptr("rs_3fc246866df43e3b9ac9e7f97f38c4c34ae1801703c759dee6"),
				Type: &reasoningType,
				Content: &schemas.ResponsesMessageContent{
					ContentBlocks: []schemas.ResponsesMessageContentBlock{
						{
							Type: schemas.ResponsesOutputMessageContentTypeReasoning,
							Text: &reasoningText,
						},
					},
				},
			},
		},
	}

	if _, bErr := provider.Responses(ctx, schemas.Key{}, req); bErr != nil {
		t.Fatalf("Responses request failed: %v", bErr.Error)
	}

	input, ok := body["input"].([]any)
	if !ok || len(input) != 3 {
		t.Fatalf("expected 3 input items on the wire, got %v", body["input"])
	}
	item, ok := input[2].(map[string]any)
	if !ok {
		t.Fatalf("input[2] is not an object: %v", input[2])
	}
	summary, ok := item["summary"].([]any)
	if !ok || len(summary) == 0 {
		t.Fatalf("input[2] must carry a non-empty summary array, got %v", item["summary"])
	}
	entry, ok := summary[0].(map[string]any)
	if !ok || entry["type"] != "summary_text" || entry["text"] != reasoningText {
		t.Errorf("summary must be converted from reasoning_text content, got %v", summary[0])
	}
	if content, hasContent := item["content"]; hasContent {
		t.Errorf("reasoning_text content must be removed after conversion, got %v", content)
	}
}

func TestOpencodeZenFreeMintedReasoningIDStripped(t *testing.T) {
	var body map[string]any

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bodyBytes, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(bodyBytes, &body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"id":"resp_123","object":"response","model":"muse-spark-1.3-contributor-free","output":[]}`)
	}))
	defer server.Close()

	cfg := &schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{
			BaseURL: server.URL,
		},
	}
	provider, err := NewOpencodeZenFreeProvider(cfg, nil)
	if err != nil {
		t.Fatalf("NewOpencodeZenFreeProvider failed: %v", err)
	}

	// Bifrost-minted id (rs_<50hex>, as produced by chat→responses conversion)
	// with summary but no encrypted_content: a dangling server reference the
	// upstream rejects with "was not found or has expired". The id must go,
	// the summary text must stay as context.
	reasoningType := schemas.ResponsesMessageTypeReasoning
	summaryText := "thinking out loud"
	ctx := schemas.NewBifrostContext(nil, time.Time{})
	req := &schemas.BifrostResponsesRequest{
		Model: "muse-spark-1.3-contributor-free",
		Input: []schemas.ResponsesMessage{
			{
				Role: schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
				Content: &schemas.ResponsesMessageContent{
					ContentStr: schemas.Ptr("hi"),
				},
			},
			{
				ID:   schemas.Ptr("rs_8cba6e71bc32a717b4bb24f25a58741111e23baf71bf30a897"),
				Type: &reasoningType,
				ResponsesReasoning: &schemas.ResponsesReasoning{
					Summary: []schemas.ResponsesReasoningSummary{
						{Type: schemas.ResponsesReasoningContentBlockTypeSummaryText, Text: summaryText},
					},
				},
			},
		},
	}

	if _, bErr := provider.Responses(ctx, schemas.Key{}, req); bErr != nil {
		t.Fatalf("Responses request failed: %v", bErr.Error)
	}

	input, ok := body["input"].([]any)
	if !ok || len(input) != 2 {
		t.Fatalf("expected 2 input items on the wire, got %v", body["input"])
	}
	item, ok := input[1].(map[string]any)
	if !ok {
		t.Fatalf("input[1] is not an object: %v", input[1])
	}
	if id, hasID := item["id"]; hasID {
		t.Errorf("dangling reasoning id must be stripped, got %v", id)
	}
	summary, ok := item["summary"].([]any)
	if !ok || len(summary) != 1 {
		t.Fatalf("summary must be preserved as context, got %v", item["summary"])
	}
	if entry, ok := summary[0].(map[string]any); !ok || entry["text"] != summaryText {
		t.Errorf("summary text must be preserved, got %v", summary[0])
	}
}

func TestOpencodeZenFreeGenuineReasoningStateRestored(t *testing.T) {
	var body map[string]any

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bodyBytes, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(bodyBytes, &body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"id":"resp_123","object":"response","model":"muse-spark-1.3-contributor-free","output":[]}`)
	}))
	defer server.Close()

	cfg := &schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{
			BaseURL: server.URL,
		},
	}
	provider, err := NewOpencodeZenFreeProvider(cfg, nil)
	if err != nil {
		t.Fatalf("NewOpencodeZenFreeProvider failed: %v", err)
	}

	// Genuine server-issued replay state (compound id + encrypted_content, as
	// in the working direct capture): the shared converter strips the blob
	// for unknown models, so the provider must restore it and keep the id.
	reasoningType := schemas.ResponsesMessageTypeReasoning
	genuineID := "rs_6aba254700e7cd92187d4a10:rs_01a0e721a0577245b00d933b8f5c8b04"
	blob := "gfp_encrypted_blob"
	ctx := schemas.NewBifrostContext(nil, time.Time{})
	req := &schemas.BifrostResponsesRequest{
		Model: "muse-spark-1.3-contributor-free",
		Input: []schemas.ResponsesMessage{
			{
				Role: schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
				Content: &schemas.ResponsesMessageContent{
					ContentStr: schemas.Ptr("hi"),
				},
			},
			{
				ID:   schemas.Ptr(genuineID),
				Type: &reasoningType,
				ResponsesReasoning: &schemas.ResponsesReasoning{
					Summary: []schemas.ResponsesReasoningSummary{
						{Type: schemas.ResponsesReasoningContentBlockTypeSummaryText, Text: "prior thinking"},
					},
					EncryptedContent: &blob,
				},
			},
		},
	}

	if _, bErr := provider.Responses(ctx, schemas.Key{}, req); bErr != nil {
		t.Fatalf("Responses request failed: %v", bErr.Error)
	}

	input, ok := body["input"].([]any)
	if !ok || len(input) != 2 {
		t.Fatalf("expected 2 input items on the wire, got %v", body["input"])
	}
	item, ok := input[1].(map[string]any)
	if !ok {
		t.Fatalf("input[1] is not an object: %v", input[1])
	}
	if item["id"] != genuineID {
		t.Errorf("genuine reasoning id must be kept, got %v", item["id"])
	}
	if item["encrypted_content"] != blob {
		t.Errorf("encrypted_content must be restored on the wire, got %v", item["encrypted_content"])
	}
}

func TestOpencodeZenFreeIncludeReasoningEncryptedDefault(t *testing.T) {
	var body map[string]any

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bodyBytes, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(bodyBytes, &body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"id":"resp_123","object":"response","model":"muse-spark-1.3-contributor-free","output":[]}`)
	}))
	defer server.Close()

	cfg := &schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{
			BaseURL: server.URL,
		},
	}
	provider, err := NewOpencodeZenFreeProvider(cfg, nil)
	if err != nil {
		t.Fatalf("NewOpencodeZenFreeProvider failed: %v", err)
	}

	ctx := schemas.NewBifrostContext(nil, time.Time{})
	req := &schemas.BifrostResponsesRequest{
		Model: "muse-spark-1.3-contributor-free",
		Input: []schemas.ResponsesMessage{
			{
				Role: schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
				Content: &schemas.ResponsesMessageContent{
					ContentStr: schemas.Ptr("hi"),
				},
			},
		},
		Params: &schemas.ResponsesParameters{
			Include: []string{"code_interpreter_call.outputs"},
		},
	}

	if _, bErr := provider.Responses(ctx, schemas.Key{}, req); bErr != nil {
		t.Fatalf("Responses request failed: %v", bErr.Error)
	}

	include, ok := body["include"].([]any)
	if !ok {
		t.Fatalf("expected include array on the wire, got %v", body["include"])
	}
	found := map[string]bool{}
	for _, v := range include {
		if s, ok := v.(string); ok {
			found[s] = true
		}
	}
	if !found["code_interpreter_call.outputs"] {
		t.Errorf("client include value must be preserved, got %v", body["include"])
	}
	if !found["reasoning.encrypted_content"] {
		t.Errorf("reasoning.encrypted_content must be defaulted, got %v", body["include"])
	}
}

func TestOpencodeZenFreeErrorParsing(t *testing.T) {
	resp := &fasthttp.Response{}
	resp.SetStatusCode(fasthttp.StatusBadRequest)
	resp.SetBodyString(`{"type": "error", "error": {"type": "invalid_request", "message": "unknown free model"}}`)

	bErr := parseOpencodeZenFreeError(resp)
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

func TestOpencodeZenFreeUnsupportedOperations(t *testing.T) {
	cfg := &schemas.ProviderConfig{}
	p, err := NewOpencodeZenFreeProvider(cfg, nil)
	if err != nil {
		t.Fatalf("NewOpencodeZenFreeProvider failed: %v", err)
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
