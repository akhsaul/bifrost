package modal_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/providers/modal"
	"github.com/maximhq/bifrost/core/schemas"
)

func modalKey(name, model, endpointModel, username, region string) schemas.Key {
	key := schemas.Key{
		Name:   name,
		Value:  *schemas.NewSecretVar("modal-token"),
		Models: schemas.WhiteList{model},
		ModalKeyConfig: &schemas.ModalKeyConfig{
			Model:         model,
			EndpointModel: endpointModel,
			Username:      *schemas.NewSecretVar(username),
		},
	}
	if region != "" {
		key.ModalKeyConfig.Region = schemas.NewSecretVar(region)
	}
	return key
}

func chatRequest(model string) *schemas.BifrostChatRequest {
	return &schemas.BifrostChatRequest{
		Model: model,
		Input: []schemas.ChatMessage{{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("hello")}}},
	}
}

func TestChatCompletionRejectsMismatchedModel(t *testing.T) {
	provider, err := modal.NewModalProvider(&schemas.ProviderConfig{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx := schemas.NewBifrostContext(context.Background(), time.Time{})

	key := modalKey("modal-deepseek", "deepseek-ai/DeepSeek-V4.1-Flash", "deepseek-v4-1-flash", "sansatu29", "us-west")
	_, bifrostErr := provider.ChatCompletion(ctx, key, chatRequest("other-model"))
	if bifrostErr == nil {
		t.Fatal("expected model mismatch error, got nil")
	}
	want := "can't use model `other-model` for key `modal-deepseek`"
	if bifrostErr.Error.Message != want {
		t.Fatalf("error message = %q, want %q", bifrostErr.Error.Message, want)
	}
}

func TestChatCompletionMockServer(t *testing.T) {
	var gotPath, gotAuth, gotAffinity string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotAffinity = r.Header.Get("Modal-Routing-Affinity-Key")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"modal-1","object":"chat.completion","created":1,"model":"deepseek-ai/DeepSeek-V4.1-Flash","choices":[{"index":0,"message":{"role":"assistant","content":"ok"}}]}`))
	}))
	defer server.Close()

	// The deployment host is key-derived, so a mock-server round trip is
	// exercised through the BifrostContext URL-path override: setting the full
	// mock URL there makes chatRequestURL return it verbatim.
	provider, err := modal.NewModalProvider(&schemas.ProviderConfig{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx := schemas.NewBifrostContext(context.Background(), time.Time{})
	ctx.SetValue(schemas.BifrostContextKeyURLPath, server.URL+"/v1/chat/completions")
	// Session id flows through as Modal-Routing-Affinity-Key.
	ctx.SetValue(schemas.BifrostContextKeySessionID, "conv-123")
	key := modalKey("modal-deepseek", "deepseek-ai/DeepSeek-V4.1-Flash", "deepseek-v4-1-flash", "sansatu29", "us-west")
	resp, bifrostErr := provider.ChatCompletion(ctx, key, chatRequest("deepseek-ai/DeepSeek-V4.1-Flash"))
	if bifrostErr != nil {
		t.Fatal(bifrostErr)
	}
	if resp == nil || len(resp.Choices) != 1 {
		t.Fatalf("unexpected response: %#v", resp)
	}
	if gotPath != "/v1/chat/completions" {
		t.Errorf("path = %q", gotPath)
	}
	if gotAuth != "Bearer modal-token" {
		t.Errorf("authorization = %q", gotAuth)
	}
	if gotAffinity != "conv-123" {
		t.Errorf("affinity = %q, want conv-123", gotAffinity)
	}
}

func TestChatCompletionNoAffinityWithoutSession(t *testing.T) {
	var gotAffinity string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAffinity = r.Header.Get("Modal-Routing-Affinity-Key")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"modal-1","object":"chat.completion","created":1,"model":"deepseek-ai/DeepSeek-V4.1-Flash","choices":[{"index":0,"message":{"role":"assistant","content":"ok"}}]}`))
	}))
	defer server.Close()

	provider, err := modal.NewModalProvider(&schemas.ProviderConfig{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx := schemas.NewBifrostContext(context.Background(), time.Time{})
	ctx.SetValue(schemas.BifrostContextKeyURLPath, server.URL+"/v1/chat/completions")
	key := modalKey("modal-deepseek", "deepseek-ai/DeepSeek-V4.1-Flash", "deepseek-v4-1-flash", "sansatu29", "us-west")
	if _, bifrostErr := provider.ChatCompletion(ctx, key, chatRequest("deepseek-ai/DeepSeek-V4.1-Flash")); bifrostErr != nil {
		t.Fatal(bifrostErr)
	}
	if gotAffinity != "" {
		t.Errorf("affinity = %q, want empty", gotAffinity)
	}
}

func TestChatCompletionExplicitAffinityHeaderWins(t *testing.T) {
	var gotAffinity string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAffinity = r.Header.Get("Modal-Routing-Affinity-Key")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"modal-1","object":"chat.completion","created":1,"model":"deepseek-ai/DeepSeek-V4.1-Flash","choices":[{"index":0,"message":{"role":"assistant","content":"ok"}}]}`))
	}))
	defer server.Close()

	provider, err := modal.NewModalProvider(&schemas.ProviderConfig{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx := schemas.NewBifrostContext(context.Background(), time.Time{})
	ctx.SetValue(schemas.BifrostContextKeyURLPath, server.URL+"/v1/chat/completions")
	ctx.SetValue(schemas.BifrostContextKeySessionID, "conv-session")
	ctx.SetValue(schemas.BifrostContextKeyExtraHeaders, map[string][]string{
		"Modal-Routing-Affinity-Key": {"conv-explicit"},
	})
	key := modalKey("modal-deepseek", "deepseek-ai/DeepSeek-V4.1-Flash", "deepseek-v4-1-flash", "sansatu29", "us-west")
	if _, bifrostErr := provider.ChatCompletion(ctx, key, chatRequest("deepseek-ai/DeepSeek-V4.1-Flash")); bifrostErr != nil {
		t.Fatal(bifrostErr)
	}
	if gotAffinity != "conv-explicit" {
		t.Errorf("affinity = %q, want conv-explicit", gotAffinity)
	}
}

func TestDeploymentBaseURL(t *testing.T) {
	provider, err := modal.NewModalProvider(&schemas.ProviderConfig{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx := schemas.NewBifrostContext(context.Background(), time.Time{})

	key := modalKey("modal-deepseek", "deepseek-ai/DeepSeek-V4.1-Flash", "deepseek-v4-1-flash", "sansatu29", "")
	url, bifrostErr := provider.ChatCompletionURLForTest(ctx, key, "deepseek-ai/DeepSeek-V4.1-Flash")
	if bifrostErr != nil {
		t.Fatal(bifrostErr)
	}
	want := "https://sansatu29--ep-deepseek-v4-1-flash-server.us-west.modal.direct/v1/chat/completions"
	if url != want {
		t.Fatalf("url = %q, want %q", url, want)
	}

	// Explicit region overrides the us-west default.
	key2 := modalKey("modal-eu", "deepseek-ai/DeepSeek-V4.1-Flash", "deepseek-v4-1-flash", "sansatu29", "eu-west")
	url2, bifrostErr := provider.ChatCompletionURLForTest(ctx, key2, "deepseek-ai/DeepSeek-V4.1-Flash")
	if bifrostErr != nil {
		t.Fatal(bifrostErr)
	}
	want2 := "https://sansatu29--ep-deepseek-v4-1-flash-server.eu-west.modal.direct/v1/chat/completions"
	if url2 != want2 {
		t.Fatalf("url = %q, want %q", url2, want2)
	}
}

func TestListModelsAggregatesKeyModels(t *testing.T) {
	provider, err := modal.NewModalProvider(&schemas.ProviderConfig{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx := schemas.NewBifrostContext(context.Background(), time.Time{})

	keys := []schemas.Key{
		modalKey("modal-a", "deepseek-ai/DeepSeek-V4.1-Flash", "deepseek-v4-1-flash", "sansatu29", "us-west"),
		modalKey("modal-b", "meta-llama/Llama-3.1-8B-Instruct", "llama-3-1-8b-instruct", "sansatu29", "us-west"),
		{Name: "modal-incomplete"},
	}
	resp, bifrostErr := provider.ListModels(ctx, keys, &schemas.BifrostListModelsRequest{Provider: schemas.Modal})
	if bifrostErr != nil {
		t.Fatal(bifrostErr)
	}
	if len(resp.Data) != 2 {
		t.Fatalf("expected 2 models, got %d", len(resp.Data))
	}
	if resp.Data[0].ID != "deepseek-ai/DeepSeek-V4.1-Flash" || resp.Data[1].ID != "meta-llama/Llama-3.1-8B-Instruct" {
		t.Fatalf("unexpected models: %#v", resp.Data)
	}
	if len(resp.KeyStatuses) != 2 {
		t.Fatalf("expected 2 key statuses, got %d", len(resp.KeyStatuses))
	}
}
