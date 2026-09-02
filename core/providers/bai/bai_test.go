package bai_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/providers/bai"
	"github.com/maximhq/bifrost/core/schemas"
)

// Compile-time check that baiProvider satisfies the full Provider interface.
// The struct is unexported (opencode-style), so the check lives inside the
// package instead of the external test package.
var _ = bai.InterfaceCheck

func TestBAIProviderDefaults(t *testing.T) {
	config := &schemas.ProviderConfig{}
	config.CheckAndSetDefaults()
	provider, err := bai.NewBAIProvider(config, nil)
	if err != nil {
		t.Fatalf("NewBAIProvider failed: %v", err)
	}
	if provider.GetProviderKey() != schemas.Bai {
		t.Errorf("expected provider key %s, got %s", schemas.Bai, provider.GetProviderKey())
	}
}

func TestChatCompletionMockServer(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("path = %q", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer bai-token" {
			t.Errorf("authorization = %q", r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"bai-1","object":"chat.completion","created":1,"model":"test-model","choices":[{"index":0,"message":{"role":"assistant","content":"ok"}}]}`))
	}))
	defer server.Close()

	provider, err := bai.NewBAIProvider(&schemas.ProviderConfig{NetworkConfig: schemas.NetworkConfig{BaseURL: server.URL}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx := schemas.NewBifrostContext(context.Background(), time.Time{})
	response, bifrostErr := provider.ChatCompletion(ctx, schemas.Key{Value: *schemas.NewSecretVar("bai-token")}, &schemas.BifrostChatRequest{Model: "test-model", Input: []schemas.ChatMessage{{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("hello")}}}})
	if bifrostErr != nil {
		t.Fatal(bifrostErr)
	}
	if response == nil || len(response.Choices) != 1 {
		t.Fatalf("unexpected response: %#v", response)
	}
}
