package bitdeer_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/providers/bitdeer"
	"github.com/maximhq/bifrost/core/schemas"
)

// newTestProvider returns a provider pointed at the given mock server base URL.
func newTestProvider(t *testing.T, baseURL string) *bitdeer.Provider {
	t.Helper()
	provider, err := bitdeer.NewBitdeerProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{BaseURL: baseURL},
	}, nil)
	if err != nil {
		t.Fatalf("NewBitdeerProvider failed: %v", err)
	}
	if provider.GetProviderKey() != schemas.Bitdeer {
		t.Fatalf("provider key = %q, want %q", provider.GetProviderKey(), schemas.Bitdeer)
	}
	return provider
}

func TestDefaultBaseURL(t *testing.T) {
	// No NetworkConfig.BaseURL set: the constructor must fall back to Bitdeer's
	// public endpoint. The default is asserted via the exported key only, since
	// networkConfig is unexported; the path behaviour below pins the /v1 suffix.
	config := &schemas.ProviderConfig{}
	config.CheckAndSetDefaults()
	if config.NetworkConfig.BaseURL != "" {
		t.Fatalf("precondition: expected empty BaseURL, got %q", config.NetworkConfig.BaseURL)
	}
	if _, err := bitdeer.NewBitdeerProvider(config, nil); err != nil {
		t.Fatalf("NewBitdeerProvider with default base URL failed: %v", err)
	}
}

func TestChatCompletionMockServer(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("path = %q, want %q", r.URL.Path, "/v1/chat/completions")
		}
		if r.Header.Get("Authorization") != "Bearer bitdeer-token" {
			t.Errorf("authorization = %q", r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"bitdeer-1","object":"chat.completion","created":1,"model":"test-model","choices":[{"index":0,"message":{"role":"assistant","content":"ok"}}]}`))
	}))
	defer server.Close()

	// The base URL includes /v1, so the provider must append only /chat/completions.
	provider := newTestProvider(t, server.URL+"/v1")
	ctx := schemas.NewBifrostContext(context.Background(), time.Time{})
	response, bifrostErr := provider.ChatCompletion(ctx, schemas.Key{Value: *schemas.NewSecretVar("bitdeer-token")}, &schemas.BifrostChatRequest{
		Model: "test-model",
		Input: []schemas.ChatMessage{{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("hello")}}},
	})
	if bifrostErr != nil {
		t.Fatal(bifrostErr)
	}
	if response == nil || len(response.Choices) != 1 {
		t.Fatalf("unexpected response: %#v", response)
	}
}

// TestResponsesHitsNativeEndpoint pins that the Responses API is served by
// Bitdeer's own /v1/responses endpoint. If Responses is ever regressed to the
// Responses-to-Chat fallback (request.ToChatRequest()), this fails because the
// mock server would see /v1/chat/completions instead.
func TestResponsesHitsNativeEndpoint(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/responses" {
			t.Errorf("path = %q, want %q", r.URL.Path, "/v1/responses")
		}
		if r.Header.Get("Authorization") != "Bearer bitdeer-token" {
			t.Errorf("authorization = %q", r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp-1","object":"response","model":"test-model","status":"completed","output":[]}`))
	}))
	defer server.Close()

	provider := newTestProvider(t, server.URL+"/v1")
	ctx := schemas.NewBifrostContext(context.Background(), time.Time{})
	response, bifrostErr := provider.Responses(ctx, schemas.Key{Value: *schemas.NewSecretVar("bitdeer-token")}, &schemas.BifrostResponsesRequest{
		Model: "test-model",
		Input: []schemas.ResponsesMessage{{
			Role:    schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
			Content: &schemas.ResponsesMessageContent{ContentStr: schemas.Ptr("hello")},
		}},
	})
	if bifrostErr != nil {
		t.Fatal(bifrostErr)
	}
	if response == nil {
		t.Fatalf("unexpected nil response")
	}
	// A Responses-to-Chat fallback would set this flag; native Responses must not.
	if isFallback, ok := ctx.Value(schemas.BifrostContextKeyIsResponsesToChatCompletionFallback).(bool); ok && isFallback {
		t.Errorf("Responses fell back to chat completions instead of using the native endpoint")
	}
}

func TestListModelsMockServer(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			t.Errorf("path = %q, want %q", r.URL.Path, "/v1/models")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"test-model","object":"model","owned_by":"bitdeer"}]}`))
	}))
	defer server.Close()

	provider := newTestProvider(t, server.URL+"/v1")
	ctx := schemas.NewBifrostContext(context.Background(), time.Time{})
	response, bifrostErr := provider.ListModels(ctx, []schemas.Key{{Value: *schemas.NewSecretVar("bitdeer-token")}}, &schemas.BifrostListModelsRequest{})
	if bifrostErr != nil {
		t.Fatal(bifrostErr)
	}
	if response == nil {
		t.Fatalf("unexpected nil response")
	}
}

// TestUnsupportedOperations pins that the operations Bitdeer does not expose
// return the standard "unsupported operation" error rather than nil,nil.
func TestUnsupportedOperations(t *testing.T) {
	provider := newTestProvider(t, "http://127.0.0.1:1/v1")
	ctx := schemas.NewBifrostContext(context.Background(), time.Time{})
	key := schemas.Key{Value: *schemas.NewSecretVar("bitdeer-token")}

	if resp, err := provider.Embedding(ctx, key, &schemas.BifrostEmbeddingRequest{}); err == nil || resp != nil {
		t.Errorf("Embedding: got (%#v, %#v), want (nil, unsupported error)", resp, err)
	}
	if resp, err := provider.TextCompletion(ctx, key, &schemas.BifrostTextCompletionRequest{}); err == nil || resp != nil {
		t.Errorf("TextCompletion: got (%#v, %#v), want (nil, unsupported error)", resp, err)
	}
	if chunks, err := provider.PassthroughStream(ctx, nil, nil, key, &schemas.BifrostPassthroughRequest{}); err == nil || chunks != nil {
		t.Errorf("PassthroughStream: got (%#v, %#v), want (nil, unsupported error)", chunks, err)
	}
}
