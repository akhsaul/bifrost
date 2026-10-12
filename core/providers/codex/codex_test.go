package codex_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/providers/codex"
	"github.com/maximhq/bifrost/core/schemas"
)

func testContext() *schemas.BifrostContext {
	return schemas.NewBifrostContext(context.Background(), time.Time{})
}

func testResponsesRequest() *schemas.BifrostResponsesRequest {
	return &schemas.BifrostResponsesRequest{
		Model: "gpt-5.5",
		Input: []schemas.ResponsesMessage{
			{
				Role:    schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
				Content: &schemas.ResponsesMessageContent{ContentStr: schemas.Ptr("hello")},
			},
		},
	}
}

func testChatRequest() *schemas.BifrostChatRequest {
	return &schemas.BifrostChatRequest{
		Model: "gpt-5.5",
		Input: []schemas.ChatMessage{
			{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("hello")}},
		},
	}
}

// newTestProvider builds a provider whose BaseURL points at the mock server.
// Token/account URLs are constants (auth.openai.com, chatgpt.com), so the
// mock can only serve inference paths; OAuth paths are covered by the token
// logic tests below via static keys.
func newTestProvider(t *testing.T, baseURL string) *codex.CodexProvider {
	t.Helper()
	provider, err := codex.NewCodexProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{BaseURL: baseURL},
	}, bifrost.NewDefaultLogger(schemas.LogLevelError))
	if err != nil {
		t.Fatal(err)
	}
	return provider
}

func TestBuildHeadersDefaults(t *testing.T) {
	headers := codex.BuildHeaders(testContext(), nil)
	for _, k := range []string{"originator", "user-agent", "version"} {
		if headers[k] == "" {
			t.Errorf("missing default header %q", k)
		}
	}
	if headers["originator"] != "codex-tui" {
		t.Errorf("originator = %q, want codex-tui", headers["originator"])
	}
	for _, k := range []string{"authorization", "chatgpt-account-id"} {
		for hk := range headers {
			if strings.EqualFold(hk, k) {
				t.Errorf("credential header %q must not be set by BuildHeaders", k)
			}
		}
	}
}

func TestBuildHeadersOverrides(t *testing.T) {
	configExtra := map[string]string{"version": "9.9.9", "authorization": "Bearer foreign"}
	ctx := testContext()
	ctx.SetValue(schemas.BifrostContextKeyExtraHeaders, map[string][]string{
		"version": {"1.2.3"},
	})
	headers := codex.BuildHeaders(ctx, configExtra)
	if headers["version"] != "1.2.3" {
		t.Errorf("ctx extra header should win, got %q", headers["version"])
	}
	if _, ok := headers["authorization"]; ok {
		t.Error("authorization from config must be dropped, it is credential-owned")
	}
}

func TestResolveCredentialsRequiresSomething(t *testing.T) {
	provider := newTestProvider(t, "http://localhost")
	_, bifrostErr := provider.Responses(testContext(), schemas.Key{}, testResponsesRequest())
	if bifrostErr == nil {
		t.Fatal("expected configuration error for key without value or oauth config")
	}
	if !strings.Contains(strings.ToLower(bifrostErr.Error.Message), "refresh_token") {
		t.Errorf("error should mention refresh_token, got %q", bifrostErr.Error.Message)
	}
	if bifrostErr.AllowFallbacks == nil || *bifrostErr.AllowFallbacks {
		t.Error("configuration errors must block fallbacks")
	}
}

func TestResponsesStaticKeyMockServer(t *testing.T) {
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			t.Errorf("path = %q", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer static-token" {
			t.Errorf("authorization = %q", got)
		}
		if got := r.Header.Get("Chatgpt-Account-Id"); got != "" {
			t.Errorf("chatgpt-account-id should be absent without an account, got %q", got)
		}
		if got := r.Header.Get("User-Agent"); !strings.HasPrefix(got, "codex-tui/") {
			t.Errorf("user-agent = %q, want codex-tui prefix", got)
		}
		if got := r.Header.Get("Originator"); got != "codex-tui" {
			t.Errorf("originator = %q, want codex-tui", got)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp-1\",\"object\":\"response\",\"created_at\":1,\"model\":\"gpt-5.5\",\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"hi\"}]}]}}\n\n"))
	}))
	defer server.Close()

	provider := newTestProvider(t, server.URL)
	response, bifrostErr := provider.Responses(
		testContext(),
		schemas.Key{Value: *schemas.NewSecretVar("static-token")},
		testResponsesRequest(),
	)
	if bifrostErr != nil {
		t.Fatal(bifrostErr)
	}
	if response == nil || response.Status == nil || *response.Status != "completed" {
		t.Fatalf("unexpected response: %#v", response)
	}
	if len(response.Output) != 1 {
		t.Fatalf("want 1 output message, got %d", len(response.Output))
	}
}

func TestResponsesStaticKeySendsAccountID(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Chatgpt-Account-Id"); got != "acct-123" {
			t.Errorf("chatgpt-account-id = %q, want acct-123", got)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp-1\",\"object\":\"response\",\"created_at\":1,\"model\":\"gpt-5.5\",\"status\":\"completed\",\"output\":[]}}\n\n"))
	}))
	defer server.Close()

	provider := newTestProvider(t, server.URL)
	key := schemas.Key{
		Value:          *schemas.NewSecretVar("static-token"),
		CodexKeyConfig: &schemas.CodexKeyConfig{AccountID: *schemas.NewSecretVar("acct-123")},
	}
	if _, bifrostErr := provider.Responses(testContext(), key, testResponsesRequest()); bifrostErr != nil {
		t.Fatal(bifrostErr)
	}
}

func TestResponsesStripsUnsupportedParams(t *testing.T) {
	var bodies atomic.Value
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(buf)
		bodies.Store(string(buf))
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp-1\",\"object\":\"response\",\"created_at\":1,\"model\":\"gpt-5.5\",\"status\":\"completed\",\"output\":[]}}\n\n"))
	}))
	defer server.Close()

	provider := newTestProvider(t, server.URL)
	req := testResponsesRequest()
	req.Params = &schemas.ResponsesParameters{
		MaxOutputTokens:      schemas.Ptr(100),
		Truncation:           schemas.Ptr("auto"),
		Background:           schemas.Ptr(true),
		User:                 schemas.Ptr("u1"),
		PromptCacheRetention: schemas.Ptr("30m"),
		SafetyIdentifier:     schemas.Ptr("s1"),
	}
	if _, bifrostErr := provider.Responses(testContext(), schemas.Key{Value: *schemas.NewSecretVar("static-token")}, req); bifrostErr != nil {
		t.Fatal(bifrostErr)
	}
	body, _ := bodies.Load().(string)
	for _, banned := range []string{"max_output_tokens", "truncation", "background", "prompt_cache_retention", "safety_identifier"} {
		if strings.Contains(body, banned) {
			t.Errorf("wire body must not contain %q: %s", banned, body)
		}
	}
	if !strings.Contains(body, `"store":false`) {
		t.Errorf("wire body must default store to false: %s", body)
	}
}

func TestResponsesStaticKey401NoRetry(t *testing.T) {
	var hits atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"bad token","type":"invalid_request_error"}}`))
	}))
	defer server.Close()

	provider := newTestProvider(t, server.URL)
	_, bifrostErr := provider.Responses(
		testContext(),
		schemas.Key{Value: *schemas.NewSecretVar("static-token")},
		testResponsesRequest(),
	)
	if bifrostErr == nil {
		t.Fatal("expected error")
	}
	if bifrostErr.StatusCode == nil || *bifrostErr.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %v, want 401", bifrostErr.StatusCode)
	}
	if hits.Load() != 1 {
		t.Errorf("static-key 401 must not retry, hits = %d", hits.Load())
	}
	if bifrostErr.AllowFallbacks == nil || *bifrostErr.AllowFallbacks {
		t.Error("auth errors must block fallbacks")
	}
}

func TestChatCompletionViaResponsesMockServer(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			t.Errorf("path = %q, chat must go through /responses", r.URL.Path)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp-1\",\"object\":\"response\",\"created_at\":1,\"model\":\"gpt-5.5\",\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"hello back\"}]}]}}\n\n"))
	}))
	defer server.Close()

	provider := newTestProvider(t, server.URL)
	response, bifrostErr := provider.ChatCompletion(
		testContext(),
		schemas.Key{Value: *schemas.NewSecretVar("static-token")},
		testChatRequest(),
	)
	if bifrostErr != nil {
		t.Fatal(bifrostErr)
	}
	if response == nil || len(response.Choices) != 1 {
		t.Fatalf("unexpected response: %#v", response)
	}
	content := response.Choices[0].Message.Content
	if content == nil || content.ContentStr == nil || !strings.Contains(*content.ContentStr, "hello back") {
		t.Errorf("unexpected content: %#v", content)
	}
}

func TestResponsesRawCaptureFlowsThroughAccumulator(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp-1\",\"object\":\"response\",\"created_at\":1,\"model\":\"gpt-5.5\",\"status\":\"completed\",\"output\":[]}}\n\n"))
	}))
	defer server.Close()

	provider := newTestProvider(t, server.URL)
	ctx := testContext()
	// store_raw_request_response drives capture via the context flag, exactly
	// like bifrost.go's requestWorker does — no provider config flag needed.
	ctx.SetValue(schemas.BifrostContextKeyCaptureRawRequest, true)
	ctx.SetValue(schemas.BifrostContextKeyCaptureRawResponse, true)
	response, bifrostErr := provider.Responses(
		ctx,
		schemas.Key{Value: *schemas.NewSecretVar("static-token")},
		testResponsesRequest(),
	)
	if bifrostErr != nil {
		t.Fatal(bifrostErr)
	}
	if response.ExtraFields.RawRequest == nil {
		t.Error("unary fan-in must carry ExtraFields.RawRequest when capture is on")
	} else if raw, err := json.Marshal(response.ExtraFields.RawRequest); err != nil || !strings.Contains(string(raw), `"model":"gpt-5.5"`) {
		t.Errorf("raw request should be the wire body, got %s (err %v)", raw, err)
	}
	if response.ExtraFields.RawResponse == nil {
		t.Error("unary fan-in must carry ExtraFields.RawResponse when capture is on")
	} else if raw, err := json.Marshal(response.ExtraFields.RawResponse); err != nil || !strings.Contains(string(raw), "response.completed") {
		t.Errorf("raw response should hold the SSE frames, got %s (err %v)", raw, err)
	}

	// Capture off: both must stay nil.
	plain, bifrostErr := provider.Responses(
		testContext(),
		schemas.Key{Value: *schemas.NewSecretVar("static-token")},
		testResponsesRequest(),
	)
	if bifrostErr != nil {
		t.Fatal(bifrostErr)
	}
	if plain.ExtraFields.RawRequest != nil || plain.ExtraFields.RawResponse != nil {
		t.Error("raw fields must be nil when capture is off")
	}
}

func TestListModelsFiltersByPlan(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/models") {
			t.Errorf("path = %q", r.URL.Path)
		}
		if got := r.URL.Query().Get("client_version"); got == "" {
			t.Error("client_version query must be sent")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"models":[
			{"slug":"gpt-5.5","display_name":"GPT-5.5","description":"d","available_in_plans":["free","plus"],"context_window":100,"max_context_window":200},
			{"slug":"gpt-pro-only","display_name":"Pro","available_in_plans":["plus"],"context_window":100},
			{"slug":"codex-auto-review","display_name":"Review","available_in_plans":["business"],"context_window":100}
		]}`))
	}))
	defer server.Close()

	provider := newTestProvider(t, server.URL)
	// Static keys carry no plan, so nothing passes the strict plan filter.
	resp, bifrostErr := provider.ListModels(
		testContext(),
		[]schemas.Key{{ID: "codex-key-1", Value: *schemas.NewSecretVar("static-token"), Models: schemas.WhiteList{"*"}}},
		&schemas.BifrostListModelsRequest{Provider: schemas.Codex},
	)
	if bifrostErr != nil {
		t.Fatal(bifrostErr)
	}
	if len(resp.Data) != 0 {
		t.Errorf("static key has no plan, want 0 models, got %v", resp.Data)
	}
	if len(resp.KeyStatuses) != 1 || resp.KeyStatuses[0].Status != schemas.KeyStatusSuccess {
		t.Errorf("expected KeyStatus success, got %+v", resp.KeyStatuses)
	}
}

func TestListModelsNoKeys(t *testing.T) {
	provider := newTestProvider(t, "http://localhost")
	if _, bifrostErr := provider.ListModels(testContext(), nil, &schemas.BifrostListModelsRequest{}); bifrostErr == nil {
		t.Error("expected error for missing keys")
	}
}

func TestUnsupportedOperations(t *testing.T) {
	provider := newTestProvider(t, "http://localhost")
	ctx := testContext()
	key := schemas.Key{Value: *schemas.NewSecretVar("k")}
	if _, err := provider.Embedding(ctx, key, &schemas.BifrostEmbeddingRequest{}); err == nil {
		t.Error("Embedding should be unsupported")
	}
	if _, err := provider.TextCompletion(ctx, key, &schemas.BifrostTextCompletionRequest{}); err == nil {
		t.Error("TextCompletion should be unsupported")
	}
	if _, err := provider.CountTokens(ctx, key, &schemas.BifrostResponsesRequest{}); err == nil {
		t.Error("CountTokens should be unsupported")
	}
}
