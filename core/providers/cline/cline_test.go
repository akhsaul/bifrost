package cline_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/internal/llmtests"
	"github.com/maximhq/bifrost/core/providers/cline"
	"github.com/maximhq/bifrost/core/schemas"
)

func testContext() *schemas.BifrostContext {
	return schemas.NewBifrostContext(context.Background(), time.Time{})
}

func testChatRequest() *schemas.BifrostChatRequest {
	return &schemas.BifrostChatRequest{
		Model: "cline-free/deepseek-v4.1-flash",
		Input: []schemas.ChatMessage{
			{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("hello")}},
		},
	}
}

func newTestProvider(t *testing.T, baseURL string) *cline.ClineProvider {
	t.Helper()
	provider, err := cline.NewClineProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{BaseURL: baseURL},
	}, bifrost.NewDefaultLogger(schemas.LogLevelError))
	if err != nil {
		t.Fatal(err)
	}
	return provider
}

func TestBuildHeadersDefaults(t *testing.T) {
	headers := cline.BuildHeaders(testContext(), nil, testChatRequest())
	for _, k := range []string{
		"http-referer", "user-agent", "sec-fetch-mode",
		"x-client-type", "x-client-version", "x-core-version", "x-is-multiroot",
		"x-platform", "x-platform-version", "x-task-id", "x-title",
	} {
		if headers[k] == "" {
			t.Errorf("missing default header %q", k)
		}
	}
	if _, ok := headers["authorization"]; ok {
		t.Error("BuildHeaders must never set authorization")
	}
	// Transport-level headers stay out.
	for _, k := range []string{"accept", "accept-encoding", "connection", "content-type", "host"} {
		for hk := range headers {
			if strings.EqualFold(hk, k) {
				t.Errorf("transport header %q must not be set by BuildHeaders", k)
			}
		}
	}
}

func TestBuildHeadersOverrides(t *testing.T) {
	configExtra := map[string]string{"x-title": "Custom", "x-task-id": "config-task"}
	ctx := testContext()
	ctx.SetValue(schemas.BifrostContextKeyExtraHeaders, map[string][]string{
		"x-title": {"CtxTitle"},
	})
	headers := cline.BuildHeaders(ctx, configExtra, testChatRequest())
	if headers["x-title"] != "CtxTitle" {
		t.Errorf("ctx extra header should win, got %q", headers["x-title"])
	}
	// x-task-id resolves from ctx/body/config before generation; config value
	// is the only source here.
	if headers["x-task-id"] != "config-task" {
		t.Errorf("x-task-id should come from config, got %q", headers["x-task-id"])
	}
}

func TestResolveTaskIDPriority(t *testing.T) {
	configExtra := map[string]string{"x-task-id": "config-task"}
	req := testChatRequest()
	req.Params = &schemas.ChatParameters{ExtraParams: map[string]interface{}{"task_id": "body-task"}}

	// Body beats config.
	if got := cline.ResolveTaskID(testContext(), configExtra, req); got != "body-task" {
		t.Errorf("body should beat config, got %q", got)
	}
	// Ctx beats body.
	ctx := testContext()
	ctx.SetValue(schemas.BifrostContextKeyExtraHeaders, map[string][]string{"x-task-id": {"ctx-task"}})
	if got := cline.ResolveTaskID(ctx, configExtra, req); got != "ctx-task" {
		t.Errorf("ctx should beat body, got %q", got)
	}
	// Generated shape: <millis>_<5 chars>.
	got := cline.ResolveTaskID(testContext(), nil, testChatRequest())
	parts := strings.Split(got, "_")
	if len(parts) != 2 || len(parts[1]) != 5 {
		t.Errorf("generated task id has wrong shape: %q", got)
	}
}

func TestChatCompletionEnvelopeMockServer(t *testing.T) {
	envelope, err := os.ReadFile(filepath.Join("testdata", "cline-resp-no-stream.json"))
	if err != nil {
		t.Fatal(err)
	}
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("path = %q", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer static-key" {
			t.Errorf("authorization = %q", got)
		}
		if r.Header.Get("X-Task-Id") == "" {
			t.Error("x-task-id must always be sent")
		}
		if r.Header.Get("User-Agent") == "" {
			t.Error("user-agent must always be sent")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(envelope)
	}))
	defer server.Close()

	provider := newTestProvider(t, server.URL)
	response, bifrostErr := provider.ChatCompletion(
		testContext(),
		schemas.Key{Value: *schemas.NewSecretVar("static-key")},
		testChatRequest(),
	)
	if bifrostErr != nil {
		t.Fatal(bifrostErr)
	}
	if response == nil || len(response.Choices) != 1 {
		t.Fatalf("unexpected response: %#v", response)
	}
	content := response.Choices[0].Message.Content
	if content == nil || content.ContentStr == nil || !strings.Contains(*content.ContentStr, "AI assistant") {
		t.Errorf("unexpected content: %#v", content)
	}
	if response.Usage == nil || response.Usage.PromptTokens != 44 {
		t.Errorf("usage not parsed, got %#v", response.Usage)
	}
}

func TestChatCompletionOAuthHeaderAndCache(t *testing.T) {
	expiry := time.Now().Add(2 * time.Hour).UTC().Format(time.RFC3339)
	var refreshHits atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/auth/refresh":
			refreshHits.Add(1)
			_, _ = w.Write([]byte(`{"data":{"accessToken":"oauth-token-1","tokenType":"Bearer","expiresAt":"` + expiry + `","refreshToken":"refresh-1"},"success":true}`))
		case "/v1/chat/completions":
			if got := r.Header.Get("Authorization"); got != "Bearer workos:oauth-token-1" {
				t.Errorf("authorization = %q, want workos-prefixed bearer", got)
			}
			_, _ = w.Write([]byte(`{"id":"c1","object":"chat.completion","created":1,"model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"ok"}}]}`))
		default:
			t.Errorf("unexpected path %q", r.URL.Path)
		}
	}))
	defer server.Close()

	provider := newTestProvider(t, server.URL)
	key := schemas.Key{
		ID:             "oauth-key",
		ClineKeyConfig: &schemas.ClineKeyConfig{RefreshToken: *schemas.NewSecretVar("refresh-1")},
	}
	for i := 0; i < 2; i++ {
		if _, bifrostErr := provider.ChatCompletion(testContext(), key, testChatRequest()); bifrostErr != nil {
			t.Fatal(bifrostErr)
		}
	}
	if refreshHits.Load() != 1 {
		t.Errorf("refresh hits = %d, want 1 (second call must use the cache)", refreshHits.Load())
	}
}

func TestChatCompletionOAuth401RetriesOnce(t *testing.T) {
	expiry := time.Now().Add(2 * time.Hour).UTC().Format(time.RFC3339)
	var refreshHits atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/auth/refresh":
			// First mint is stale; the retry after 401 gets the good one.
			token := "stale-token"
			if refreshHits.Add(1) > 1 {
				token = "fresh-token"
			}
			_, _ = w.Write([]byte(`{"data":{"accessToken":"` + token + `","tokenType":"Bearer","expiresAt":"` + expiry + `","refreshToken":"refresh-1"},"success":true}`))
		case "/v1/chat/completions":
			if r.Header.Get("Authorization") != "Bearer workos:fresh-token" {
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"error":"unauthorized","success":false}`))
				return
			}
			_, _ = w.Write([]byte(`{"id":"c1","object":"chat.completion","created":1,"model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"ok"}}]}`))
		}
	}))
	defer server.Close()

	provider := newTestProvider(t, server.URL)
	key := schemas.Key{
		ID:             "oauth-stale-key",
		ClineKeyConfig: &schemas.ClineKeyConfig{RefreshToken: *schemas.NewSecretVar("refresh-1")},
	}
	response, bifrostErr := provider.ChatCompletion(testContext(), key, testChatRequest())
	if bifrostErr != nil {
		t.Fatal(bifrostErr)
	}
	if response == nil || len(response.Choices) != 1 {
		t.Fatalf("unexpected response after retry: %#v", response)
	}
	if refreshHits.Load() != 2 {
		t.Errorf("refresh hits = %d, want 2 (initial mint + post-401 retry)", refreshHits.Load())
	}
}

func TestResolveCredentialsRequiresSomething(t *testing.T) {
	provider := newTestProvider(t, "http://localhost")
	_, bifrostErr := provider.ChatCompletion(testContext(), schemas.Key{}, testChatRequest())
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

func TestListModelsMergesRecommended(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer static-key" {
			t.Errorf("authorization = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/models":
			_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"stealth/space-bunny-alpha","object":"model","created":1,"owned_by":"stealth"},{"id":"openai/gpt-x","object":"model","created":1,"owned_by":"openai"}]}`))
		case "/v1/ai/cline/recommended-models":
			_, _ = w.Write([]byte(`{"recommended":[{"id":"spacexai/grok-4.7","name":"grok-4.7","description":"","tags":["NEW"]}],"free":[{"id":"stealth/space-bunny-alpha","name":"dup","description":"","tags":[]},{"id":"cline-free/mimo-v2.6-flash","name":"Mimo","description":"","tags":[]}],"clinePass":[],"clineCloud":[]}`))
		default:
			t.Errorf("unexpected path %q", r.URL.Path)
		}
	}))
	defer server.Close()

	provider := newTestProvider(t, server.URL)
	resp, bifrostErr := provider.ListModels(
		testContext(),
		[]schemas.Key{{Value: *schemas.NewSecretVar("static-key"), Models: schemas.WhiteList{"*"}}},
		&schemas.BifrostListModelsRequest{Provider: schemas.Cline},
	)
	if bifrostErr != nil {
		t.Fatal(bifrostErr)
	}
	ids := map[string]bool{}
	for _, m := range resp.Data {
		ids[m.ID] = true
	}
	for _, want := range []string{"cline/openai/gpt-x", "cline/spacexai/grok-4.7", "cline/cline-free/mimo-v2.6-flash", "cline/stealth/space-bunny-alpha"} {
		if !ids[want] {
			t.Errorf("missing merged model %q (got %v)", want, ids)
		}
	}
	if len(resp.Data) != 4 {
		t.Errorf("want 4 models after dedupe, got %d: %v", len(resp.Data), ids)
	}
}

func TestListModelsRecommendedFailsSoft(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/v1/ai/cline/recommended-models" {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":"boom","success":false}`))
			return
		}
		_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"openai/gpt-x","object":"model","created":1,"owned_by":"openai"}]}`))
	}))
	defer server.Close()

	provider := newTestProvider(t, server.URL)
	resp, bifrostErr := provider.ListModels(
		testContext(),
		[]schemas.Key{{Value: *schemas.NewSecretVar("static-key"), Models: schemas.WhiteList{"*"}}},
		&schemas.BifrostListModelsRequest{Provider: schemas.Cline},
	)
	if bifrostErr != nil {
		t.Fatal(bifrostErr)
	}
	if len(resp.Data) != 1 || resp.Data[0].ID != "cline/openai/gpt-x" {
		t.Errorf("base catalog should survive recommended failure, got %#v", resp.Data)
	}
}

func TestListModelsNoKeys(t *testing.T) {
	provider := newTestProvider(t, "http://localhost")
	if _, bifrostErr := provider.ListModels(testContext(), nil, &schemas.BifrostListModelsRequest{}); bifrostErr == nil {
		t.Error("expected error for missing keys")
	}
}

func TestParseClineErrorShapes(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"unauthorized","success":false}`))
	}))
	defer server.Close()

	provider := newTestProvider(t, server.URL)
	_, bifrostErr := provider.ChatCompletion(
		testContext(),
		schemas.Key{Value: *schemas.NewSecretVar("bad-key")},
		testChatRequest(),
	)
	if bifrostErr == nil {
		t.Fatal("expected error")
	}
	if bifrostErr.StatusCode == nil || *bifrostErr.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %v, want 401", bifrostErr.StatusCode)
	}
	if !strings.Contains(bifrostErr.Error.Message, "unauthorized") {
		t.Errorf("message = %q, want unauthorized", bifrostErr.Error.Message)
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

// TestCline is the entrypoint for `make test-core PROVIDER=cline`.
//
// It lives in the external test package because llmtests imports core, and
// core imports this provider. An internal test would close that cycle.
//
// Without credentials the test skips, matching how every other live provider
// suite in this repo is gated. Either a static key (CLINE_API_KEY) or the
// OAuth refresh token from the WorkOS device flow (CLINE_REFRESH_TOKEN) works.
func TestCline(t *testing.T) {
	t.Parallel()

	if !hasClineCredentials(os.Getenv) {
		t.Skip("Skipping Cline tests: set CLINE_API_KEY for static-key auth, or " +
			"CLINE_REFRESH_TOKEN (with optional CLINE_CLIENT_ID) for OAuth")
	}

	client, ctx, cancel, err := llmtests.SetupTest()
	if err != nil {
		t.Fatalf("Error initializing test setup: %v", err)
	}
	defer cancel()
	defer client.Shutdown()

	testConfig := llmtests.ComprehensiveTestConfig{
		Provider:  schemas.Cline,
		ChatModel: "cline-free/deepseek-v4.1-flash",
		Fallbacks: []schemas.Fallback{
			{Provider: schemas.Cline, Model: "cline-free/deepseek-v4.1-flash"},
		},
		TextModel:      "cline-free/deepseek-v4.1-flash",
		ReasoningModel: "cline-free/deepseek-v4.1-flash",
		Scenarios: llmtests.TestScenarios{
			// Cline has no text completions endpoint and no Responses API;
			// everything goes through chat.
			TextCompletion:        false,
			TextCompletionStream:  false,
			SimpleChat:            true,
			CompletionStream:      true,
			MultiTurnConversation: true,
			ToolCalls:             true,
			ToolCallsStreaming:    true,
			ListModels:            true,
		},
	}

	t.Run("ClineTests", func(t *testing.T) {
		llmtests.RunAllComprehensiveTests(t, client, ctx, testConfig)
	})
}

// hasClineCredentials reports whether enough credentials are present to run
// the live suite: either a static API key or an OAuth refresh token.
func hasClineCredentials(getenv func(string) string) bool {
	if strings.TrimSpace(getenv("CLINE_API_KEY")) != "" {
		return true
	}
	return strings.TrimSpace(getenv("CLINE_REFRESH_TOKEN")) != ""
}

func TestOAuthAdoptsRotatedRefreshToken(t *testing.T) {
	var refreshBodies []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/v1/auth/refresh" {
			body, _ := io.ReadAll(r.Body)
			refreshBodies = append(refreshBodies, string(body))
			// Short-lived access token: already inside the refresh margin, so
			// every call mints again. Rotation must be adopted for mint #2.
			expiry := time.Now().Add(30 * time.Second).UTC().Format(time.RFC3339)
			_, _ = w.Write([]byte(`{"data":{"accessToken":"tok","tokenType":"Bearer","expiresAt":"` + expiry + `","refreshToken":"rotated-2"},"success":true}`))
			return
		}
		_, _ = w.Write([]byte(`{"id":"c1","object":"chat.completion","created":1,"model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"ok"}}]}`))
	}))
	defer server.Close()

	provider := newTestProvider(t, server.URL)
	key := schemas.Key{
		ID:             "oauth-rotate-key",
		ClineKeyConfig: &schemas.ClineKeyConfig{RefreshToken: *schemas.NewSecretVar("original-1")},
	}
	for i := 0; i < 2; i++ {
		if _, bifrostErr := provider.ChatCompletion(testContext(), key, testChatRequest()); bifrostErr != nil {
			t.Fatal(bifrostErr)
		}
	}
	if len(refreshBodies) != 2 {
		t.Fatalf("want 2 refresh calls, got %d", len(refreshBodies))
	}
	if !strings.Contains(refreshBodies[0], `"refreshToken":"original-1"`) {
		t.Errorf("first refresh must use the configured token: %s", refreshBodies[0])
	}
	if !strings.Contains(refreshBodies[1], `"refreshToken":"rotated-2"`) {
		t.Errorf("second refresh must use the adopted rotated token: %s", refreshBodies[1])
	}
}
