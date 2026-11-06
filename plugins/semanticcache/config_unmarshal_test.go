package semanticcache

import (
	"encoding/json"
	"testing"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
)

func TestUnmarshalJSON_DefaultCacheKey(t *testing.T) {
	tests := []struct {
		name     string
		json     string
		expected string
	}{
		{
			name:     "set",
			json:     `{"dimension": 1536, "default_cache_key": "my-cache-key"}`,
			expected: "my-cache-key",
		},
		{
			name:     "omitted",
			json:     `{"dimension": 1536}`,
			expected: "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var config Config
			if err := json.Unmarshal([]byte(tc.json), &config); err != nil {
				t.Fatalf("Failed to unmarshal: %v", err)
			}
			if config.DefaultCacheKey != tc.expected {
				t.Errorf("Expected DefaultCacheKey %q, got %q", tc.expected, config.DefaultCacheKey)
			}
		})
	}
}

func TestUnmarshalJSON_AllFields(t *testing.T) {
	input := `{
		"provider": "openai",
		"embedding_model": "text-embedding-3-small",
		"dimension": 1536,
		"ttl": "10m",
		"threshold": 0.9,
		"vector_store_namespace": "my-ns",
		"default_cache_key": "global-key",
		"conversation_history_threshold": 5,
		"cache_by_model": false,
		"cache_by_provider": false,
		"exclude_system_prompt": true,
		"cache_tool_call_responses": true,
		"allowed_request_types": ["embedding"],
		"cache_type": "direct"
	}`

	var config Config
	if err := json.Unmarshal([]byte(input), &config); err != nil {
		t.Fatalf("Failed to unmarshal: %v", err)
	}

	if config.Provider != "openai" {
		t.Errorf("Provider: expected %q, got %q", "openai", config.Provider)
	}
	if config.EmbeddingModel != "text-embedding-3-small" {
		t.Errorf("EmbeddingModel: expected %q, got %q", "text-embedding-3-small", config.EmbeddingModel)
	}
	if config.Dimension != 1536 {
		t.Errorf("Dimension: expected 1536, got %d", config.Dimension)
	}
	if config.TTL != 10*time.Minute {
		t.Errorf("TTL: expected 10m, got %v", config.TTL)
	}
	if config.Threshold != 0.9 {
		t.Errorf("Threshold: expected 0.9, got %f", config.Threshold)
	}
	if config.VectorStoreNamespace != "my-ns" {
		t.Errorf("VectorStoreNamespace: expected %q, got %q", "my-ns", config.VectorStoreNamespace)
	}
	if config.DefaultCacheKey != "global-key" {
		t.Errorf("DefaultCacheKey: expected %q, got %q", "global-key", config.DefaultCacheKey)
	}
	if config.ConversationHistoryThreshold != 5 {
		t.Errorf("ConversationHistoryThreshold: expected 5, got %d", config.ConversationHistoryThreshold)
	}
	if config.CacheByModel == nil || *config.CacheByModel != false {
		t.Errorf("CacheByModel: expected false, got %v", config.CacheByModel)
	}
	if config.CacheByProvider == nil || *config.CacheByProvider != false {
		t.Errorf("CacheByProvider: expected false, got %v", config.CacheByProvider)
	}
	if config.ExcludeSystemPrompt == nil || *config.ExcludeSystemPrompt != true {
		t.Errorf("ExcludeSystemPrompt: expected true, got %v", config.ExcludeSystemPrompt)
	}
	if !config.CacheToolCallResponses {
		t.Errorf("CacheToolCallResponses: expected true, got %v", config.CacheToolCallResponses)
	}
	if len(config.AllowedRequestTypes) != 1 || config.AllowedRequestTypes[0] != "embedding" {
		t.Errorf("AllowedRequestTypes: expected ['embedding'], got %v", config.AllowedRequestTypes)
	}
	if config.CacheType != "direct" {
		t.Errorf("CacheType: expected 'direct', got %q", config.CacheType)
	}
}

func TestIsRequestTypeAllowed(t *testing.T) {
	// When AllowedRequestTypes is empty, all types are allowed
	pluginEmpty := &Plugin{config: &Config{}}
	if !pluginEmpty.isRequestTypeAllowed("embedding") {
		t.Error("expected embedding to be allowed when list is empty")
	}
	if !pluginEmpty.isRequestTypeAllowed("chat_completion") {
		t.Error("expected chat_completion to be allowed when list is empty")
	}

	// When AllowedRequestTypes is set to ["embedding"]
	pluginEmbeddingOnly := &Plugin{config: &Config{AllowedRequestTypes: []string{"embedding"}}}
	if !pluginEmbeddingOnly.isRequestTypeAllowed("embedding") {
		t.Error("expected embedding to be allowed")
	}
	if pluginEmbeddingOnly.isRequestTypeAllowed("chat_completion") {
		t.Error("expected chat_completion to NOT be allowed")
	}
	if pluginEmbeddingOnly.isRequestTypeAllowed("text_completion") {
		t.Error("expected text_completion to NOT be allowed")
	}

	// Test plural alias ["embeddings"]
	pluginPlural := &Plugin{config: &Config{AllowedRequestTypes: []string{"embeddings"}}}
	if !pluginPlural.isRequestTypeAllowed("embedding") {
		t.Error("expected embedding to be allowed with 'embeddings' alias")
	}
	if pluginPlural.isRequestTypeAllowed("chat_completion") {
		t.Error("expected chat_completion to NOT be allowed")
	}
	}
}

func TestUnmarshalJSON_TTLFormats(t *testing.T) {
	tests := []struct {
		name     string
		json     string
		expected time.Duration
	}{
		{
			name:     "duration string",
			json:     `{"dimension": 1536, "ttl": "5m"}`,
			expected: 5 * time.Minute,
		},
		{
			name:     "numeric seconds",
			json:     `{"dimension": 1536, "ttl": 300}`,
			expected: 300 * time.Second,
		},
		{
			name:     "omitted",
			json:     `{"dimension": 1536}`,
			expected: 0,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var config Config
			if err := json.Unmarshal([]byte(tc.json), &config); err != nil {
				t.Fatalf("Failed to unmarshal: %v", err)
			}
			if config.TTL != tc.expected {
				t.Errorf("Expected TTL %v, got %v", tc.expected, config.TTL)
			}
		})
	}
}

func TestUnmarshalJSON_BoolPointerFields(t *testing.T) {
	tests := []struct {
		name               string
		json               string
		expectCacheByModel *bool
		expectCacheByProv  *bool
		expectExcludeSys   *bool
	}{
		{
			name:               "all set to true",
			json:               `{"dimension": 1536, "cache_by_model": true, "cache_by_provider": true, "exclude_system_prompt": true}`,
			expectCacheByModel: bifrost.Ptr(true),
			expectCacheByProv:  bifrost.Ptr(true),
			expectExcludeSys:   bifrost.Ptr(true),
		},
		{
			name:               "all set to false",
			json:               `{"dimension": 1536, "cache_by_model": false, "cache_by_provider": false, "exclude_system_prompt": false}`,
			expectCacheByModel: bifrost.Ptr(false),
			expectCacheByProv:  bifrost.Ptr(false),
			expectExcludeSys:   bifrost.Ptr(false),
		},
		{
			name:               "all omitted",
			json:               `{"dimension": 1536}`,
			expectCacheByModel: nil,
			expectCacheByProv:  nil,
			expectExcludeSys:   nil,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var config Config
			if err := json.Unmarshal([]byte(tc.json), &config); err != nil {
				t.Fatalf("Failed to unmarshal: %v", err)
			}
			assertBoolPtr(t, "CacheByModel", config.CacheByModel, tc.expectCacheByModel)
			assertBoolPtr(t, "CacheByProvider", config.CacheByProvider, tc.expectCacheByProv)
			assertBoolPtr(t, "ExcludeSystemPrompt", config.ExcludeSystemPrompt, tc.expectExcludeSys)
		})
	}
}

func assertBoolPtr(t *testing.T, field string, got, want *bool) {
	t.Helper()
	if got == nil && want == nil {
		return
	}
	if got == nil || want == nil {
		t.Errorf("%s: expected %v, got %v", field, want, got)
		return
	}
	if *got != *want {
		t.Errorf("%s: expected %v, got %v", field, *want, *got)
	}
}

func TestPreLLMHookAllowedRequestTypes(t *testing.T) {
	cfg := getDefaultTestConfig()
	cfg.AllowedRequestTypes = []string{"embedding"}

	plugin := &Plugin{
		config: cfg,
		logger: bifrost.NewDefaultLogger(schemas.LogLevelDebug),
		store:  newDirectFastPathStore(),
	}

	// 1. ChatCompletionRequest should be bypassed
	chatReq := &schemas.BifrostRequest{
		RequestType: schemas.ChatCompletionRequest,
		ChatRequest: CreateBasicChatRequest("hello", 0, 100),
	}
	ctxChat := CreateContextWithCacheKey(t, "chat-test")
	_, shortCircuitChat, errChat := plugin.PreLLMHook(ctxChat, chatReq)
	if errChat != nil || shortCircuitChat != nil {
		t.Fatalf("PreLLMHook for chat unexpected: shortCircuit=%v err=%v", shortCircuitChat, errChat)
	}
	reqIDChat, _ := ctxChat.Value(schemas.BifrostContextKeyRequestID).(string)
	if state := plugin.getCacheState(reqIDChat); state != nil {
		t.Fatalf("expected state for chat request to be nil because it is not in AllowedRequestTypes")
	}

	// 2. EmbeddingRequest should be allowed and processed
	embReq := &schemas.BifrostRequest{
		RequestType:      schemas.EmbeddingRequest,
		EmbeddingRequest: CreateEmbeddingRequest([]string{"hello world"}),
	}
	ctxEmb := CreateContextWithCacheKey(t, "emb-test")
	_, shortCircuitEmb, errEmb := plugin.PreLLMHook(ctxEmb, embReq)
	if errEmb != nil {
		t.Fatalf("PreLLMHook for embedding error: %v", errEmb)
	}
	if shortCircuitEmb != nil {
		t.Fatalf("unexpected short circuit on first request")
	}
	reqIDEmb, _ := ctxEmb.Value(schemas.BifrostContextKeyRequestID).(string)
	state := plugin.getCacheState(reqIDEmb)
	if state == nil {
		t.Fatalf("expected state for embedding request to be populated")
	}
	if state.DirectCacheID == "" {
		t.Fatalf("expected DirectCacheID to be set for embedding request")
	}
}
