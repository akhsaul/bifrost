package antigravity

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/bytedance/sonic"
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
	if creds2.ClientID != GetAntigravityClientID() {
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
		// Real server model IDs (from /v1internal:fetchAvailableModels) must
		// pass through UNCHANGED — rewriting them breaks the server request.
		{"gemini-3.6-flash-low", "gemini-3.6-flash-low"},
		{"gemini-3.6-flash-high", "gemini-3.6-flash-high"},
		{"gemini-3.7-flash-medium", "gemini-3.7-flash-medium"},
		{"gemini-3.6-flash-tiered", "gemini-3.6-flash-tiered"},
		{"gemini-3-flash-agent", "gemini-3-flash-agent"},
		{"gemini-pro-agent", "gemini-pro-agent"},
		// Routing prefix is stripped, nothing else.
		{"antigravity/gemini-3.6-flash-low", "gemini-3.6-flash-low"},
		// Convenience aliases for names the server does not know.
		{"gemini-claude-sonnet-4-5", "claude-sonnet-4-6"},
		{"gemini-claude-opus-4-5-thinking", "claude-opus-4-6-thinking"},
		{"", ""},
	}

	for _, tt := range tests {
		got := ResolveModel(tt.input)
		if got != tt.expected {
			t.Errorf("ResolveModel(%q) = %q, want %q", tt.input, got, tt.expected)
		}
	}
}

// TestAntigravityAliasesNeverShadowServerModels pins the bug class: an alias
// key must never be a public model ID (rewriting a valid server model breaks
// the request), and every alias target must be a real public model ID the
// server accepts.
func TestAntigravityAliasesNeverShadowServerModels(t *testing.T) {
	public := make(map[string]bool, len(AntigravityPublicModels))
	for _, m := range AntigravityPublicModels {
		public[m.ID] = true
	}

	for key, target := range AntigravityModelAliases {
		if public[key] {
			t.Errorf("alias key %q is itself a public model ID — rewriting it corrupts a valid server model", key)
		}
		if !public[target] {
			t.Errorf("alias target %q (from key %q) is not a known public model — the server would reject it", target, key)
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

	origURL := tokenEndpointURL
	tokenEndpointURL = server.URL
	defer func() { tokenEndpointURL = origURL }()

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	client := &fasthttp.Client{}
	tok, exp, err := RefreshToken(ctx, client, creds, nil)
	if err != nil {
		t.Fatalf("RefreshToken failed: %v", err)
	}
	if tok != "mock-new-access-token" {
		t.Errorf("expected mock-new-access-token, got %s", tok)
	}
	if exp.IsZero() {
		t.Error("expected non-zero expiration time")
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

func TestFetchGoogleUserInfo(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer mock-access-token" {
			t.Errorf("unexpected authorization header: %s", r.Header.Get("Authorization"))
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			"id": "106104910333037428195",
			"email": "ikhsanmaulana1289@gmail.com",
			"verified_email": true,
			"name": "Ikhsan Maulana"
		}`))
	}))
	defer server.Close()

	// Temporarily override or test directly
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	req.Header.SetMethod(http.MethodGet)
	req.SetRequestURI(server.URL)
	req.Header.Set("Authorization", "Bearer mock-access-token")

	client := &fasthttp.Client{}
	if err := client.Do(req, resp); err != nil {
		t.Fatalf("client.Do failed: %v", err)
	}

	var userInfo GoogleUserInfo
	if err := sonic.Unmarshal(resp.Body(), &userInfo); err != nil {
		t.Fatalf("unmarshal userinfo failed: %v", err)
	}

	if userInfo.Email != "ikhsanmaulana1289@gmail.com" {
		t.Errorf("expected email ikhsanmaulana1289@gmail.com, got %s", userInfo.Email)
	}
	if userInfo.Name != "Ikhsan Maulana" {
		t.Errorf("expected name Ikhsan Maulana, got %s", userInfo.Name)
	}
}

func TestFetchAvailableModelsFromAPI(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != FetchModelsPath {
			t.Errorf("unexpected path: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.Header.Get("Authorization") != "Bearer mock-token" {
			t.Errorf("unexpected auth header: %s", r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			"models": {
				"gemini-3.5-flash-low": {
					"displayName": "Gemini 3.5 Flash (Medium)",
					"maxTokens": 1048576,
					"maxOutputTokens": 65536,
					"isInternal": false
				},
				"internal-test-model": {
					"displayName": "Internal Model",
					"isInternal": true
				}
			},
			"webSearchModelIds": ["gemini-3.5-flash-low"]
		}`))
	}))
	defer server.Close()

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	client := &fasthttp.Client{}

	models, err := FetchAvailableModelsFromAPI(ctx, client, "mock-token", "proj-xyz", server.URL, nil, nil)
	if err != nil {
		t.Fatalf("FetchAvailableModelsFromAPI failed: %v", err)
	}

	if len(models) != 1 {
		t.Fatalf("expected 1 non-internal model, got %d", len(models))
	}
	if models[0].ID != "gemini-3.5-flash-low" {
		t.Errorf("expected gemini-3.5-flash-low, got %s", models[0].ID)
	}
	if models[0].Name == nil || *models[0].Name != "Gemini 3.5 Flash (Medium)" {
		t.Errorf("expected displayName Gemini 3.5 Flash (Medium), got %v", models[0].Name)
	}
	if models[0].ContextLength == nil || *models[0].ContextLength != 1048576 {
		t.Errorf("expected maxTokens 1048576, got %v", models[0].ContextLength)
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
	if resp != nil {
		t.Fatalf("expected nil response when no keys configured, got %v", resp)
	}
	if bErr == nil {
		t.Fatal("expected authentication error when no keys configured, got nil")
	}
	if bErr.StatusCode == nil || *bErr.StatusCode != 401 {
		t.Errorf("expected 401 status code, got %v", bErr.StatusCode)
	}
	if bErr.Error == nil || !strings.Contains(bErr.Error.Message, "no keys configured") {
		t.Errorf("expected 'no keys configured' message, got %v", bErr.Error)
	}
}

func TestAntigravityProvider_ListModels_KeyStatuses(t *testing.T) {
	ClearTokenCache()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "fetchAvailableModels") {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{
				"models": {
					"gemini-3.6-flash-high": {
						"displayName": "Gemini 3.6 Flash (High)",
						"maxTokens": 1048576
					}
				},
				"webSearchModelIds": ["gemini-3.6-flash-high"],
				"tieredModelIds": {
					"flashLite": ["gemini-3.5-flash-lite"],
					"flash": ["gemini-3.8-flash-tiered"],
					"pro": ["gemini-3.1-pro-low"]
				},
				"deprecatedModelIds": {
					"gemini-3.1-pro-high": {
						"newModelId": "gemini-pro-agent"
					}
				}
			}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	provider, err := NewAntigravityProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{
			BaseURL: server.URL,
		},
	}, nil)
	if err != nil {
		t.Fatalf("failed to create provider: %v", err)
	}

	goodKey := schemas.Key{
		ID:     "good-key-1",
		Name:   "good-antigravity",
		Models: schemas.WhiteList{"*"},
		AntigravityKeyConfig: &schemas.AntigravityKeyConfig{
			ProjectID:   schemas.NewSecretVar("test-project"),
			AccessToken: schemas.NewSecretVar("ya29.test-access-token"),
		},
	}

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	resp, bErr := provider.ListModels(ctx, []schemas.Key{goodKey}, &schemas.BifrostListModelsRequest{Provider: schemas.Antigravity})
	if bErr != nil {
		t.Fatalf("ListModels failed: %v", bErr)
	}
	if len(resp.KeyStatuses) != 1 {
		t.Fatalf("expected 1 KeyStatus, got %d", len(resp.KeyStatuses))
	}
	if resp.KeyStatuses[0].KeyID != "good-key-1" || resp.KeyStatuses[0].Status != schemas.KeyStatusSuccess {
		t.Errorf("expected KeyStatus success for good-key-1, got %+v", resp.KeyStatuses[0])
	}

	// Failure case: invalid credentials
	badKey := schemas.Key{
		ID:                   "bad-key-1",
		Name:                 "bad-antigravity",
		Models:               schemas.WhiteList{"*"},
		AntigravityKeyConfig: &schemas.AntigravityKeyConfig{
			// No refresh token and no access token
		},
	}
	_, bErr = provider.ListModels(ctx, []schemas.Key{badKey}, &schemas.BifrostListModelsRequest{Provider: schemas.Antigravity})
	if bErr == nil {
		t.Fatal("expected error for bad key")
	}
	if len(bErr.ExtraFields.KeyStatuses) != 1 {
		t.Fatalf("expected 1 KeyStatus on error, got %d", len(bErr.ExtraFields.KeyStatuses))
	}
	if bErr.ExtraFields.KeyStatuses[0].KeyID != "bad-key-1" || bErr.ExtraFields.KeyStatuses[0].Status != schemas.KeyStatusListModelsFailed {
		t.Errorf("expected KeyStatus list_models_failed for bad-key-1, got %+v", bErr.ExtraFields.KeyStatuses[0])
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

func TestAntigravityProvider_QuotaMethods(t *testing.T) {
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "retrieveUserQuotaSummary") {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{
				"groups": [
					{
						"displayName": "Gemini Models",
						"buckets": [
							{
								"bucketId": "gemini-5h",
								"displayName": "Five Hour Limit Remaining",
								"window": "5h",
								"resetTime": "2026-08-23T15:30:00Z",
								"remainingFraction": 0.75
							}
						]
					}
				],
				"description": "Quota limits"
			}`))
			return
		}

		if strings.Contains(r.URL.Path, "fetchAvailableModels") {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{
				"models": {
					"gemini-3.5-flash-low": {
						"displayName": "Gemini 3.5 Flash (Medium)",
						"quotaInfo": {
							"remainingFraction": 0.85,
							"resetTime": "2026-08-23T15:30:00Z"
						}
					}
				},
				"webSearchModelIds": ["gemini-3.5-flash-low"]
			}`))
			return
		}

		w.WriteHeader(http.StatusOK)
	}))
	defer mockServer.Close()

	config := &schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{
			BaseURL: mockServer.URL,
		},
	}
	provider, err := NewAntigravityProvider(config, nil)
	if err != nil {
		t.Fatalf("failed to create provider: %v", err)
	}

	key := schemas.Key{
		ID: "test-quota-key",
		AntigravityKeyConfig: &schemas.AntigravityKeyConfig{
			ProjectID:   schemas.NewSecretVar("my-test-proj"),
			AccessToken: schemas.NewSecretVar("valid-access-token"),
		},
	}

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)

	// 1. Test GetKeyQuotaSummary
	summary, bErr := provider.GetKeyQuotaSummary(ctx, key)
	if bErr != nil {
		t.Fatalf("GetKeyQuotaSummary failed: %v", bErr.Error.Message)
	}
	if summary == nil || len(summary.Groups) != 1 {
		t.Fatalf("expected 1 group, got %v", summary)
	}
	if summary.Groups[0].Buckets[0].RemainingFraction != 0.75 {
		t.Errorf("expected remaining fraction 0.75, got %f", summary.Groups[0].Buckets[0].RemainingFraction)
	}

	// 2. Test GetModelsQuota
	modelsQuota, bErr := provider.GetModelsQuota(ctx, key)
	if bErr != nil {
		t.Fatalf("GetModelsQuota failed: %v", bErr.Error.Message)
	}
	if len(modelsQuota) != 1 {
		t.Fatalf("expected 1 model quota, got %d", len(modelsQuota))
	}
	info, ok := modelsQuota["gemini-3.5-flash-low"]
	if !ok {
		t.Fatalf("expected gemini-3.5-flash-low in quota map")
	}
	if info.RemainingFraction != 0.85 {
		t.Errorf("expected 0.85, got %f", info.RemainingFraction)
	}
}

var serverUA string

func TestGetUserAgent_CLI(t *testing.T) {
	ua := GetUserAgent("cli")
	if !strings.Contains(ua, "antigravity/cli/1.2.12") {
		t.Errorf("GetUserAgent(cli) = %q, want version 1.2.12", ua)
	}
	if !strings.Contains(ua, "cl=989022288") {
		t.Errorf("GetUserAgent(cli) = %q, want cl=989022288", ua)
	}
}

func TestAntigravityProvider_ChatCompletion_UserAgentPrecedence(t *testing.T) {
	ClearTokenCache()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		serverUA = r.Header.Get("User-Agent")
		if strings.Contains(r.URL.Path, "generateContent") {
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			chunk := `{"response":{"candidates":[{"content":{"parts":[{"text":"hi"}],"role":"model"},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1,"totalTokenCount":2}}}`
			_, _ = fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", chunk)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	config := &schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{
			BaseURL:      server.URL,
			ExtraHeaders: map[string]string{"User-Agent": "my-custom-ua/2.0"},
		},
	}
	provider, err := NewAntigravityProvider(config, nil)
	if err != nil {
		t.Fatalf("failed to create provider: %v", err)
	}

	key := schemas.Key{
		ID: "test-ua-key",
		AntigravityKeyConfig: &schemas.AntigravityKeyConfig{
			ProjectID:   schemas.NewSecretVar("test-proj"),
			AccessToken: schemas.NewSecretVar("mock-access-token"),
		},
	}
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	req := &schemas.BifrostChatRequest{
		Model: "gemini-3.6-flash-high",
		Input: []schemas.ChatMessage{{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("Hello")}}},
	}
	if _, bErr := provider.ChatCompletion(ctx, key, req); bErr != nil {
		t.Fatalf("ChatCompletion failed: %v", bErr.Error.Message)
	}
	if got := serverUA; got != "my-custom-ua/2.0" {
		t.Errorf("User-Agent = %q, want user override %q (default is %q)", got, "my-custom-ua/2.0", GetUserAgent("cli"))
	}
}

func TestAntigravityProvider_ChatCompletion_401InvalidateAndRetry(t *testing.T) {
	ClearTokenCache()

	var attempts int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Token refresh endpoint
		if r.URL.Path == "/token" || r.FormValue("grant_type") == "refresh_token" {
			_ = r.ParseForm()
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"access_token": "fresh-token-after-401", "expires_in": 3600}`))
			return
		}

		if strings.Contains(r.URL.Path, "generateContent") {
			attempts++
			authHeader := r.Header.Get("Authorization")
			if authHeader == "Bearer dead-cached-token" {
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"error": {"code": 401, "message": "Request had invalid authentication credentials.", "status": "UNAUTHENTICATED"}}`))
				return
			}
			if authHeader == "Bearer fresh-token-after-401" {
				w.Header().Set("Content-Type", "text/event-stream")
				w.WriteHeader(http.StatusOK)
				chunk := `{"response":{"candidates":[{"content":{"parts":[{"text":"Success after 401 retry!"}],"role":"model"},"finishReason":"STOP"}]}}`
				_, _ = fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", chunk)
				return
			}
			w.WriteHeader(http.StatusForbidden)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	origURL := tokenEndpointURL
	tokenEndpointURL = server.URL + "/token"
	defer func() { tokenEndpointURL = origURL }()

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
		ID: "test-401-key",
		AntigravityKeyConfig: &schemas.AntigravityKeyConfig{
			ProjectID:    schemas.NewSecretVar("test-proj"),
			RefreshToken: schemas.NewSecretVar("my-refresh-token"),
			AccessToken:  schemas.NewSecretVar("dead-cached-token"),
		},
	}

	// Pre-seed tokenCache with the dead token
	creds := GetCredentials(key)
	cacheKey := computeCacheKey(key.ID, creds)
	tokenCacheMu.Lock()
	tokenCache[cacheKey] = &CachedToken{
		AccessToken: "dead-cached-token",
		ExpiresAt:   time.Now().Add(10 * time.Minute),
		ProjectID:   "test-proj",
	}
	tokenCacheMu.Unlock()

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	req := &schemas.BifrostChatRequest{
		Model: "gemini-3.6-flash-high",
		Input: []schemas.ChatMessage{
			{
				Role: schemas.ChatMessageRoleUser,
				Content: &schemas.ChatMessageContent{
					ContentStr: schemas.Ptr("Test prompt"),
				},
			},
		},
	}

	resp, bErr := provider.ChatCompletion(ctx, key, req)
	if bErr != nil {
		t.Fatalf("expected successful retry after 401, but got error: %v", bErr.Error.Message)
	}
	if resp == nil || len(resp.Choices) == 0 {
		t.Fatal("expected non-empty response after 401 retry")
	}
	choice := resp.Choices[0]
	if choice.Message.Content == nil || *choice.Message.Content.ContentStr != "Success after 401 retry!" {
		t.Errorf("unexpected content: %v", choice.Message.Content)
	}
	if attempts != 2 {
		t.Errorf("expected 2 attempts (initial 401 + 1 retry), got %d", attempts)
	}

	// Verify tokenCache was updated with fresh token
	tokenCacheMu.RLock()
	cachedToken := tokenCache[cacheKey]
	tokenCacheMu.RUnlock()
	if cachedToken == nil || cachedToken.AccessToken != "fresh-token-after-401" {
		t.Errorf("expected cached token to be fresh-token-after-401, got %+v", cachedToken)
	}
}

func TestAntigravityProvider_ChatCompletion_Non401DoesNotInvalidate(t *testing.T) {
	ClearTokenCache()

	var attempts int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "generateContent") {
			attempts++
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error": {"code": 429, "message": "Rate limit exceeded", "status": "RESOURCE_EXHAUSTED"}}`))
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
		ID: "test-non401-key",
		AntigravityKeyConfig: &schemas.AntigravityKeyConfig{
			ProjectID:    schemas.NewSecretVar("test-proj"),
			RefreshToken: schemas.NewSecretVar("my-refresh-token"),
			AccessToken:  schemas.NewSecretVar("good-token"),
		},
	}

	// Pre-seed tokenCache with good token
	creds := GetCredentials(key)
	cacheKey := computeCacheKey(key.ID, creds)
	tokenCacheMu.Lock()
	tokenCache[cacheKey] = &CachedToken{
		AccessToken: "good-token",
		ExpiresAt:   time.Now().Add(10 * time.Minute),
		ProjectID:   "test-proj",
	}
	tokenCacheMu.Unlock()

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	req := &schemas.BifrostChatRequest{
		Model: "gemini-3.6-flash-high",
		Input: []schemas.ChatMessage{
			{
				Role: schemas.ChatMessageRoleUser,
				Content: &schemas.ChatMessageContent{
					ContentStr: schemas.Ptr("Test prompt"),
				},
			},
		},
	}

	resp, bErr := provider.ChatCompletion(ctx, key, req)
	if resp != nil {
		t.Fatalf("expected error, got response: %v", resp)
	}
	if bErr == nil || bErr.StatusCode == nil || *bErr.StatusCode != 429 {
		t.Fatalf("expected 429 error, got: %v", bErr)
	}
	if attempts != 1 {
		t.Errorf("expected exactly 1 attempt (no retry on 429), got %d", attempts)
	}

	// Verify tokenCache was NOT invalidated
	tokenCacheMu.RLock()
	cachedToken := tokenCache[cacheKey]
	tokenCacheMu.RUnlock()
	if cachedToken == nil || cachedToken.AccessToken != "good-token" {
		t.Errorf("expected cached token to remain 'good-token', got %+v", cachedToken)
	}
}

func TestBuildAntigravityAuthURL_Legacy(t *testing.T) {
	authURL := BuildAntigravityAuthURL("", "test-state")
	u, err := url.Parse(authURL)
	if err != nil {
		t.Fatalf("failed to parse auth URL: %v", err)
	}
	q := u.Query()
	if q.Get("response_type") != "code" {
		t.Errorf("expected response_type=code, got %s", q.Get("response_type"))
	}
	if q.Get("client_id") != GetAntigravityClientID() {
		t.Errorf("expected client_id=%s, got %s", GetAntigravityClientID(), q.Get("client_id"))
	}
	if q.Get("redirect_uri") != DefaultAntigravityRedirectURI {
		t.Errorf("expected redirect_uri=%s, got %s", DefaultAntigravityRedirectURI, q.Get("redirect_uri"))
	}
	if q.Get("code_challenge") != "" {
		t.Errorf("legacy auth URL should not have code_challenge, got %s", q.Get("code_challenge"))
	}
	if q.Get("state") != "test-state" {
		t.Errorf("expected state=test-state, got %s", q.Get("state"))
	}
}

func TestBuildAntigravityAuthURL_PKCE(t *testing.T) {
	authURL, verifier, state := BuildAntigravityAuthURLPKCE("", "")
	if verifier == "" {
		t.Fatal("expected non-empty code_verifier")
	}
	if len(verifier) < 43 {
		t.Errorf("expected code_verifier length >= 43, got %d", len(verifier))
	}
	if state == "" {
		t.Fatal("expected non-empty auto-generated state")
	}

	u, err := url.Parse(authURL)
	if err != nil {
		t.Fatalf("failed to parse auth URL: %v", err)
	}
	q := u.Query()
	if q.Get("response_type") != "code" {
		t.Errorf("expected response_type=code, got %s", q.Get("response_type"))
	}
	if q.Get("client_id") != GetAntigravityClientID() {
		t.Errorf("expected client_id=%s, got %s", GetAntigravityClientID(), q.Get("client_id"))
	}
	if q.Get("redirect_uri") != NewAntigravityRedirectURI {
		t.Errorf("expected redirect_uri=%s, got %s", NewAntigravityRedirectURI, q.Get("redirect_uri"))
	}
	if q.Get("code_challenge_method") != "S256" {
		t.Errorf("expected code_challenge_method=S256, got %s", q.Get("code_challenge_method"))
	}
	if q.Get("code_challenge") == "" {
		t.Error("expected non-empty code_challenge")
	}
	if q.Get("state") != state {
		t.Errorf("expected state=%s, got %s", state, q.Get("state"))
	}
	if !strings.Contains(q.Get("scope"), "https://www.googleapis.com/auth/aicode") {
		t.Errorf("expected scope to contain aicode, got %s", q.Get("scope"))
	}
	if !strings.Contains(q.Get("scope"), "openid") {
		t.Errorf("expected scope to contain openid, got %s", q.Get("scope"))
	}
}

func TestPKCEVerifierCache(t *testing.T) {
	StorePendingCodeVerifier("test-state-1", "test-verifier-1")

	if got := GetLatestCodeVerifier(); got != "test-verifier-1" {
		t.Errorf("expected latest verifier 'test-verifier-1', got %s", got)
	}

	if got := GetPendingCodeVerifier("test-state-1"); got != "test-verifier-1" {
		t.Errorf("expected pending verifier 'test-verifier-1', got %s", got)
	}

	// Should be consumed
	if got := GetPendingCodeVerifier("test-state-1"); got != "" {
		t.Errorf("expected empty verifier after consumption, got %s", got)
	}
}

func TestExchangeAuthCode_Legacy(t *testing.T) {
	var capturedCodeVerifier string
	var capturedGrantType string
	var capturedRedirectURI string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "token") {
			_ = r.ParseForm()
			capturedCodeVerifier = r.Form.Get("code_verifier")
			capturedGrantType = r.Form.Get("grant_type")
			capturedRedirectURI = r.Form.Get("redirect_uri")

			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{
				"access_token": "mock-access-token",
				"refresh_token": "mock-refresh-token",
				"expires_in": 3600,
				"token_type": "Bearer"
			}`))
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	origTokenURL := tokenEndpointURL
	tokenEndpointURL = server.URL + "/token"
	defer func() { tokenEndpointURL = origTokenURL }()

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	client := &fasthttp.Client{}

	creds, err := ExchangeAuthCode(ctx, client, "legacy-code", "", "legacy-client-id", "legacy-client-secret", nil)
	if err != nil {
		t.Fatalf("legacy exchange failed: %v", err)
	}
	if creds.RefreshToken != "mock-refresh-token" {
		t.Errorf("expected refresh token 'mock-refresh-token', got %s", creds.RefreshToken)
	}
	if capturedCodeVerifier != "" {
		t.Errorf("expected no code_verifier for legacy exchange, got '%s'", capturedCodeVerifier)
	}
	if capturedGrantType != "authorization_code" {
		t.Errorf("expected grant_type 'authorization_code', got '%s'", capturedGrantType)
	}
	if capturedRedirectURI != DefaultAntigravityRedirectURI {
		t.Errorf("expected default redirect URI '%s', got '%s'", DefaultAntigravityRedirectURI, capturedRedirectURI)
	}
}

func TestExchangeAuthCode_PKCE(t *testing.T) {
	var capturedCodeVerifier string
	var capturedGrantType string
	var capturedRedirectURI string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "token") {
			_ = r.ParseForm()
			capturedCodeVerifier = r.Form.Get("code_verifier")
			capturedGrantType = r.Form.Get("grant_type")
			capturedRedirectURI = r.Form.Get("redirect_uri")

			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{
				"access_token": "mock-access-token",
				"refresh_token": "mock-refresh-token",
				"expires_in": 3600,
				"token_type": "Bearer"
			}`))
			return
		}
		if strings.Contains(r.URL.Path, "userinfo") {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{
				"id": "12345",
				"email": "test@example.com",
				"verified_email": true,
				"name": "Test User"
			}`))
			return
		}
		if strings.Contains(r.URL.Path, "loadCodeAssist") {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{
				"cloudAICompanionProject": "test-project-pkce"
			}`))
			return
		}
		if strings.Contains(r.URL.Path, "retrieveUserQuotaSummary") {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{
				"groups": []
			}`))
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	origTokenURL := tokenEndpointURL
	tokenEndpointURL = server.URL + "/token"
	defer func() { tokenEndpointURL = origTokenURL }()

	// Test with explicit code_verifier
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", "test-auth-code")
	form.Set("code_verifier", "my-pkce-verifier-12345")
	form.Set("redirect_uri", NewAntigravityRedirectURI)

	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	req.Header.SetMethod(http.MethodPost)
	req.SetRequestURI(tokenEndpointURL)
	req.Header.SetContentType("application/x-www-form-urlencoded")
	form.Set("client_id", "my-client-id")
	req.SetBodyString(form.Encode())

	client := &fasthttp.Client{}
	err := client.Do(req, resp)
	if err != nil {
		t.Fatalf("token exchange test failed: %v", err)
	}
	if capturedCodeVerifier != "my-pkce-verifier-12345" {
		t.Errorf("expected captured code_verifier 'my-pkce-verifier-12345', got '%s'", capturedCodeVerifier)
	}
	if capturedGrantType != "authorization_code" {
		t.Errorf("expected captured grant_type 'authorization_code', got '%s'", capturedGrantType)
	}
	if capturedRedirectURI != NewAntigravityRedirectURI {
		t.Errorf("expected redirect URI '%s', got '%s'", NewAntigravityRedirectURI, capturedRedirectURI)
	}
}

func TestExchangeAuthCodePKCE_MissingVerifierError(t *testing.T) {
	// Clear any cached verifier
	pendingVerifiersMu.Lock()
	pendingVerifiers = make(map[string]pendingVerifierEntry)
	latestVerifier = ""
	latestVerifierAt = time.Time{}
	pendingVerifiersMu.Unlock()

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	_, err := ExchangeAuthCodePKCE(ctx, nil, "code-without-verifier", NewAntigravityRedirectURI, "client-id", "", "", nil)
	if err == nil {
		t.Fatal("expected error when code_verifier is missing in ExchangeAuthCodePKCE, got nil")
	}
	if !strings.Contains(err.Error(), "PKCE code_verifier is required") {
		t.Errorf("expected 'PKCE code_verifier is required' error, got %v", err)
	}
}
