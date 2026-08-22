package antigravity

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

func TestGetCredentials(t *testing.T) {
	// 1. From AntigravityKeyConfig
	key1 := schemas.Key{
		ID: "key-1",
		AntigravityKeyConfig: &schemas.AntigravityKeyConfig{
			ProjectID:     schemas.NewSecretVar("test-project"),
			RefreshToken:  schemas.NewSecretVar("test-refresh-token"),
			AccessToken:   schemas.NewSecretVar("test-access-token"),
			ClientID:      schemas.NewSecretVar("custom-client-id"),
			ClientSecret:  schemas.NewSecretVar("custom-client-secret"),
			ClientProfile: schemas.Ptr("cli"),
		},
	}
	creds1 := GetCredentials(key1)
	if creds1.ProjectID != "test-project" {
		t.Errorf("expected project_id test-project, got %s", creds1.ProjectID)
	}
	if creds1.RefreshToken != "test-refresh-token" {
		t.Errorf("expected refresh_token test-refresh-token, got %s", creds1.RefreshToken)
	}
	if creds1.ClientID != "custom-client-id" {
		t.Errorf("expected client_id custom-client-id, got %s", creds1.ClientID)
	}
	if creds1.ClientProfile != "cli" {
		t.Errorf("expected client_profile cli, got %s", creds1.ClientProfile)
	}

	// 2. From Key.Value JSON
	jsonPayload := `{"project_id":"json-project","refresh_token":"json-refresh","client_profile":"ide"}`
	key2 := schemas.Key{
		ID:    "key-2",
		Value: *schemas.NewSecretVar(jsonPayload),
	}
	creds2 := GetCredentials(key2)
	if creds2.ProjectID != "json-project" {
		t.Errorf("expected project_id json-project, got %s", creds2.ProjectID)
	}
	if creds2.RefreshToken != "json-refresh" {
		t.Errorf("expected refresh_token json-refresh, got %s", creds2.RefreshToken)
	}
	if creds2.ClientID != DefaultAntigravityClientID {
		t.Errorf("expected default client_id, got %s", creds2.ClientID)
	}

	// 3. From Key.Value string refresh token
	key3 := schemas.Key{
		ID:    "key-3",
		Value: *schemas.NewSecretVar("1//0abc123refresh"),
	}
	creds3 := GetCredentials(key3)
	if creds3.RefreshToken != "1//0abc123refresh" {
		t.Errorf("expected refresh_token 1//0abc123refresh, got %s", creds3.RefreshToken)
	}

	// 4. From Key.Value string access token
	key4 := schemas.Key{
		ID:    "key-4",
		Value: *schemas.NewSecretVar("ya29.a0AfH6SM..."),
	}
	creds4 := GetCredentials(key4)
	if creds4.AccessToken != "ya29.a0AfH6SM..." {
		t.Errorf("expected access_token ya29.a0AfH6SM..., got %s", creds4.AccessToken)
	}
}

func TestResolveModel(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"gemini-claude-sonnet-4-5", "claude-sonnet-4-6"},
		{"gemini-claude-opus-4-5-thinking", "claude-opus-4-6-thinking"},
		{"antigravity/gemini-3.6-flash-high", "gemini-3.6-flash-tiered(high)"},
		{"gemini-3-pro-image-preview", "gemini-3-pro-image"},
		{"gemini-3.6-flash-high", "gemini-3.6-flash-tiered(high)"},
		{"gemini-3.5-flash-low", "gemini-3.5-flash-low"},
	}

	for _, tt := range tests {
		got := ResolveModel(tt.input)
		if got != tt.expected {
			t.Errorf("ResolveModel(%q) = %q, want %q", tt.input, got, tt.expected)
		}
	}
}

func TestToAntigravityChatRequest_ClaudeSanitization(t *testing.T) {
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)

	bifrostReq := &schemas.BifrostChatRequest{
		Model: "claude-sonnet-4-6",
		Input: []schemas.ChatMessage{
			{
				Role: schemas.ChatMessageRoleUser,
				Content: &schemas.ChatMessageContent{
					ContentStr: schemas.Ptr("Hello"),
				},
			},
			{
				Role: schemas.ChatMessageRoleAssistant,
				Content: &schemas.ChatMessageContent{
					ContentStr: schemas.Ptr("Hi there!"),
				},
			},
		},
		Params: &schemas.ChatParameters{
			MaxCompletionTokens: schemas.Ptr(32000), // Exceeds Claude 16384 limit
		},
	}

	envelope, jsonBytes, err := ToAntigravityChatRequest(ctx, bifrostReq, "project-123")
	if err != nil {
		t.Fatalf("ToAntigravityChatRequest failed: %v", err)
	}

	if envelope.Project != "project-123" {
		t.Errorf("expected project project-123, got %s", envelope.Project)
	}
	if envelope.UserAgent != "antigravity" {
		t.Errorf("expected userAgent antigravity, got %s", envelope.UserAgent)
	}
	if envelope.RequestType != "agent" {
		t.Errorf("expected requestType agent, got %s", envelope.RequestType)
	}

	// Verify max output tokens was clamped
	if envelope.Request.GenerationConfig.MaxOutputTokens != MaxAntigravityClaudeOutputTokens {
		t.Errorf("expected max output tokens to be clamped to %d, got %v", MaxAntigravityClaudeOutputTokens, envelope.Request.GenerationConfig.MaxOutputTokens)
	}

	// Verify trailing assistant turn was stripped
	if len(envelope.Request.Contents) != 1 {
		t.Errorf("expected 1 content turn after stripping trailing assistant turn, got %d", len(envelope.Request.Contents))
	}

	if len(jsonBytes) == 0 {
		t.Error("expected non-empty jsonBytes")
	}
}

func TestEnsureProjectID(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != LoadCodeAssistPath {
			t.Errorf("unexpected path: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.Header.Get("Authorization") != "Bearer test-access-token" {
			t.Errorf("unexpected auth header: %s", r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"cloudaicompanionProject": "discovered-project-xyz"}`))
	}))
	defer server.Close()

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	client := &fasthttp.Client{}

	projectID, err := EnsureProjectID(ctx, client, "test-access-token", server.URL, "ide", nil)
	if err != nil {
		t.Fatalf("EnsureProjectID failed: %v", err)
	}
	if projectID != "discovered-project-xyz" {
		t.Errorf("expected discovered-project-xyz, got %s", projectID)
	}
}

func TestRefreshToken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.FormValue("grant_type") != "refresh_token" {
			t.Errorf("expected grant_type refresh_token, got %s", r.FormValue("grant_type"))
		}
		if r.FormValue("refresh_token") != "mock-rt" {
			t.Errorf("expected refresh_token mock-rt, got %s", r.FormValue("refresh_token"))
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"access_token": "mock-new-access-token", "expires_in": 3600}`))
	}))
	defer server.Close()

	ClearTokenCache()

	key := schemas.Key{
		ID: "test-refresh-key",
		AntigravityKeyConfig: &schemas.AntigravityKeyConfig{
			ProjectID:    schemas.NewSecretVar("my-proj"),
			RefreshToken: schemas.NewSecretVar("mock-rt"),
		},
	}

	creds := GetCredentials(key)
	if creds.RefreshToken != "mock-rt" {
		t.Fatalf("expected refresh token mock-rt, got %s", creds.RefreshToken)
	}
}

func TestParseAntigravityError(t *testing.T) {
	resp := &fasthttp.Response{}
	resp.SetStatusCode(429)
	resp.SetBody([]byte(`{"error": {"code": 429, "message": "Resource has been exhausted (e.g. check quota).", "status": "RESOURCE_EXHAUSTED"}}`))

	err := parseAntigravityError(resp, "gemini-3.6-flash-high")
	if err == nil {
		t.Fatal("expected non-nil error")
	}
	if *err.StatusCode != 429 {
		t.Errorf("expected status code 429, got %d", *err.StatusCode)
	}
	if err.Error.Message != "Resource has been exhausted (e.g. check quota)." {
		t.Errorf("unexpected message: %s", err.Error.Message)
	}
	if *err.Error.Type != "RESOURCE_EXHAUSTED" {
		t.Errorf("unexpected type: %v", err.Error.Type)
	}
	if err.ExtraFields.Provider != schemas.Antigravity {
		t.Errorf("expected provider antigravity, got %v", err.ExtraFields.Provider)
	}
}

func TestAntigravityProvider_ChatCompletion(t *testing.T) {
	ClearTokenCache()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "generateContent") {
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			chunk := `{"response":{"candidates":[{"content":{"parts":[{"text":"Hello from Antigravity!"}],"role":"model"},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":5,"candidatesTokenCount":6,"totalTokenCount":11}}}`
			_, _ = fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", chunk)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	config := &schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{
			BaseURL: server.URL,
		},
	}

	provider, err := NewAntigravityProvider(config, nil)
	if err != nil {
		t.Fatalf("failed to create provider: %v", err)
	}

	key := schemas.Key{
		ID: "test-direct-key",
		AntigravityKeyConfig: &schemas.AntigravityKeyConfig{
			ProjectID:   schemas.NewSecretVar("test-proj"),
			AccessToken: schemas.NewSecretVar("mock-access-token"),
		},
	}

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	req := &schemas.BifrostChatRequest{
		Model: "gemini-3.6-flash-high",
		Input: []schemas.ChatMessage{
			{
				Role: schemas.ChatMessageRoleUser,
				Content: &schemas.ChatMessageContent{
					ContentStr: schemas.Ptr("Hello"),
				},
			},
		},
	}

	resp, bErr := provider.ChatCompletion(ctx, key, req)
	if bErr != nil {
		t.Fatalf("ChatCompletion failed: %v", bErr.Error.Message)
	}

	if resp == nil {
		t.Fatal("expected non-nil response")
	}

	if len(resp.Choices) == 0 {
		t.Fatal("expected at least 1 choice")
	}

	choice := resp.Choices[0]
	if choice.Message.Content == nil || choice.Message.Content.ContentStr == nil || *choice.Message.Content.ContentStr != "Hello from Antigravity!" {
		t.Errorf("unexpected content: %v", choice.Message.Content)
	}

	if resp.Usage == nil || resp.Usage.TotalTokens != 11 {
		t.Errorf("unexpected usage: %v", resp.Usage)
	}
}

func TestAntigravityProvider_ListModels(t *testing.T) {
	config := &schemas.ProviderConfig{}
	provider, err := NewAntigravityProvider(config, nil)
	if err != nil {
		t.Fatalf("failed to create provider: %v", err)
	}

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	resp, bErr := provider.ListModels(ctx, nil, nil)
	if bErr != nil {
		t.Fatalf("ListModels failed: %v", bErr)
	}

	if resp == nil || len(resp.Data) == 0 {
		t.Fatal("expected non-empty model list")
	}

	found := false
	for _, m := range resp.Data {
		if m.ID == "gemini-3.6-flash-high" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected gemini-3.6-flash-high in model list")
	}
}

func TestAntigravityProvider_ChatCompletionStream(t *testing.T) {
	ClearTokenCache()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "streamGenerateContent") {
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			chunk1 := `{"response":{"candidates":[{"content":{"parts":[{"text":"Chunk 1 "}],"role":"model"}}]}}`
			chunk2 := `{"response":{"candidates":[{"content":{"parts":[{"text":"Chunk 2"}],"role":"model"},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":5,"candidatesTokenCount":6,"totalTokenCount":11}}}`
			_, _ = fmt.Fprintf(w, "data: %s\n\ndata: %s\n\ndata: [DONE]\n\n", chunk1, chunk2)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	config := &schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{
			BaseURL: server.URL,
		},
	}

	provider, err := NewAntigravityProvider(config, nil)
	if err != nil {
		t.Fatalf("failed to create provider: %v", err)
	}

	key := schemas.Key{
		ID: "test-direct-key",
		AntigravityKeyConfig: &schemas.AntigravityKeyConfig{
			ProjectID:   schemas.NewSecretVar("test-proj"),
			AccessToken: schemas.NewSecretVar("mock-access-token"),
		},
	}

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	req := &schemas.BifrostChatRequest{
		Model: "gemini-3.6-flash-high",
		Input: []schemas.ChatMessage{
			{
				Role: schemas.ChatMessageRoleUser,
				Content: &schemas.ChatMessageContent{
					ContentStr: schemas.Ptr("Hello"),
				},
			},
		},
	}

	postHookRunner := func(ctx *schemas.BifrostContext, resp *schemas.BifrostResponse, err *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError) {
		return resp, err
	}

	streamChan, bErr := provider.ChatCompletionStream(ctx, postHookRunner, nil, key, req)
	if bErr != nil {
		t.Fatalf("ChatCompletionStream failed: %v", bErr.Error.Message)
	}

	var chunks []*schemas.BifrostChatResponse
	for chunk := range streamChan {
		if chunk.BifrostChatResponse != nil {
			chunks = append(chunks, chunk.BifrostChatResponse)
		}
	}

	if len(chunks) == 0 {
		t.Fatal("expected at least 1 stream chunk")
	}
}

func TestAntigravityProvider_Responses(t *testing.T) {
	ClearTokenCache()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "generateContent") {
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			chunk := `{"response":{"candidates":[{"content":{"parts":[{"text":"Response output"}],"role":"model"},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":3,"candidatesTokenCount":4,"totalTokenCount":7}}}`
			_, _ = fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", chunk)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	config := &schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{
			BaseURL: server.URL,
		},
	}

	provider, err := NewAntigravityProvider(config, nil)
	if err != nil {
		t.Fatalf("failed to create provider: %v", err)
	}

	key := schemas.Key{
		ID: "test-direct-key",
		AntigravityKeyConfig: &schemas.AntigravityKeyConfig{
			ProjectID:   schemas.NewSecretVar("test-proj"),
			AccessToken: schemas.NewSecretVar("mock-access-token"),
		},
	}

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	req := &schemas.BifrostResponsesRequest{
		Model: "gemini-3.6-flash-high",
		Input: []schemas.ResponsesMessage{
			{
				Role: schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
				Content: &schemas.ResponsesMessageContent{
					ContentStr: schemas.Ptr("Hello"),
				},
			},
		},
	}

	resp, bErr := provider.Responses(ctx, key, req)
	if bErr != nil {
		t.Fatalf("Responses failed: %v", bErr.Error.Message)
	}

	if resp == nil {
		t.Fatal("expected non-nil response")
	}
}

func TestAntigravityProvider_UnsupportedMethods(t *testing.T) {
	config := &schemas.ProviderConfig{}
	provider, err := NewAntigravityProvider(config, nil)
	if err != nil {
		t.Fatalf("failed to create provider: %v", err)
	}

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	key := schemas.Key{ID: "k1"}

	if _, err := provider.Embedding(ctx, key, nil); err == nil {
		t.Error("expected Embedding to return unsupported error")
	}
	if _, err := provider.Speech(ctx, key, nil); err == nil {
		t.Error("expected Speech to return unsupported error")
	}
	if _, err := provider.ImageGeneration(ctx, key, nil); err == nil {
		t.Error("expected ImageGeneration to return unsupported error")
	}
	if _, err := provider.CountTokens(ctx, key, nil); err == nil {
		t.Error("expected CountTokens to return unsupported error")
	}
}
