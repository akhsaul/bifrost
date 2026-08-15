package handlers

import (
	"encoding/json"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	"github.com/valyala/fasthttp"
)

func TestExportConfig_StructureAndDefaults(t *testing.T) {
	inMemoryStore := &lib.Config{
		ClientConfig: &configstore.ClientConfig{
			InitialPoolSize:  500,
			LogRetentionDays: 90,
			Compat: configstore.CompatConfig{
				ConvertTextToChat: true,
			},
		},
		Providers: map[schemas.ModelProvider]configstore.ProviderConfig{
			schemas.OpenAI: {
				Keys: []schemas.Key{
					{
						ID:     "key-1",
						Name:   "openai-primary",
						Value:  *schemas.NewSecretVar("env.OPENAI_API_KEY"),
						Weight: 1,
					},
				},
			},
		},
	}

	handler := NewConfigHandler(nil, inMemoryStore)

	ctx := &fasthttp.RequestCtx{}
	ctx.Request.Header.SetMethod("GET")
	ctx.Request.SetRequestURI("/api/config/export")

	handler.exportConfig(ctx)

	if ctx.Response.StatusCode() != fasthttp.StatusOK {
		t.Fatalf("expected status 200, got %d", ctx.Response.StatusCode())
	}

	var result map[string]any
	if err := json.Unmarshal(ctx.Response.Body(), &result); err != nil {
		t.Fatalf("failed to unmarshal exported config JSON: %v", err)
	}

	// Verify top-level required fields
	if result["$schema"] != "https://www.getbifrost.ai/schema" {
		t.Errorf("expected $schema https://www.getbifrost.ai/schema, got %v", result["$schema"])
	}
	if result["version"] != float64(2) {
		t.Errorf("expected version 2, got %v", result["version"])
	}
	if result["source_of_truth"] != "split" {
		t.Errorf("expected source_of_truth split, got %v", result["source_of_truth"])
	}

	// Verify server defaults
	server, ok := result["server"].(map[string]any)
	if !ok {
		t.Fatalf("expected server object, got %T", result["server"])
	}
	if server["read_buffer_size"] != float64(65536) {
		t.Errorf("expected read_buffer_size 65536, got %v", server["read_buffer_size"])
	}

	// Verify client defaults and overridden values
	client, ok := result["client"].(map[string]any)
	if !ok {
		t.Fatalf("expected client object, got %T", result["client"])
	}
	if client["initial_pool_size"] != float64(500) {
		t.Errorf("expected initial_pool_size 500, got %v", client["initial_pool_size"])
	}
	if client["log_retention_days"] != float64(90) {
		t.Errorf("expected log_retention_days 90, got %v", client["log_retention_days"])
	}
	if client["enable_logging"] != true {
		t.Errorf("expected enable_logging true, got %v", client["enable_logging"])
	}
	if client["routing_chain_max_depth"] != float64(10) {
		t.Errorf("expected routing_chain_max_depth 10, got %v", client["routing_chain_max_depth"])
	}

	// Verify providers structure
	providers, ok := result["providers"].(map[string]any)
	if !ok {
		t.Fatalf("expected providers object, got %T", result["providers"])
	}
	openai, ok := providers["openai"].(map[string]any)
	if !ok {
		t.Fatalf("expected openai provider object, got %T", providers["openai"])
	}
	keys, ok := openai["keys"].([]any)
	if !ok || len(keys) != 1 {
		t.Fatalf("expected 1 key for openai, got %v", openai["keys"])
	}
	key := keys[0].(map[string]any)
	if key["value"] != "env.OPENAI_API_KEY" {
		t.Errorf("expected value env.OPENAI_API_KEY, got %v", key["value"])
	}
}

func TestExportConfig_CustomProviderAllowedRequestsDefaults(t *testing.T) {
	inMemoryStore := &lib.Config{
		ClientConfig: &configstore.ClientConfig{
			InitialPoolSize:  1000,
			LogRetentionDays: 365,
		},
		Providers: map[schemas.ModelProvider]configstore.ProviderConfig{
			schemas.TokenFaucet: {
				Keys: []schemas.Key{
					{
						ID:     "key-tf-1",
						Name:   "tokenfaucet-primary",
						Value:  *schemas.NewSecretVar("env.TOKENFAUCET_API_KEY"),
						Weight: 1,
					},
				},
				CustomProviderConfig: &schemas.CustomProviderConfig{
					BaseProviderType: schemas.OpenAI,
					IsKeyLess:        false,
					// AllowedRequests is nil -> should be exported with all defaults
				},
			},
		},
	}

	handler := NewConfigHandler(nil, inMemoryStore)

	ctx := &fasthttp.RequestCtx{}
	ctx.Request.Header.SetMethod("GET")
	ctx.Request.SetRequestURI("/api/config/export")

	handler.exportConfig(ctx)

	if ctx.Response.StatusCode() != fasthttp.StatusOK {
		t.Fatalf("expected status 200, got %d", ctx.Response.StatusCode())
	}

	var result map[string]any
	if err := json.Unmarshal(ctx.Response.Body(), &result); err != nil {
		t.Fatalf("failed to unmarshal exported config JSON: %v", err)
	}

	providers, ok := result["providers"].(map[string]any)
	if !ok {
		t.Fatalf("expected providers object, got %T", result["providers"])
	}
	tokenfaucet, ok := providers["tokenfaucet"].(map[string]any)
	if !ok {
		t.Fatalf("expected tokenfaucet provider object, got %T", providers["tokenfaucet"])
	}
	cpc, ok := tokenfaucet["custom_provider_config"].(map[string]any)
	if !ok {
		t.Fatalf("expected custom_provider_config object, got %T", tokenfaucet["custom_provider_config"])
	}
	if cpc["base_provider_type"] != "openai" {
		t.Errorf("expected base_provider_type openai, got %v", cpc["base_provider_type"])
	}
	ar, ok := cpc["allowed_requests"].(map[string]any)
	if !ok {
		t.Fatalf("expected allowed_requests object, got %T", cpc["allowed_requests"])
	}
	if ar["chat_completion"] != true {
		t.Errorf("expected chat_completion true, got %v", ar["chat_completion"])
	}
	if ar["chat_completion_stream"] != true {
		t.Errorf("expected chat_completion_stream true, got %v", ar["chat_completion_stream"])
	}
	if ar["list_models"] != true {
		t.Errorf("expected list_models true, got %v", ar["list_models"])
	}
	if ar["realtime"] != false {
		t.Errorf("expected realtime false, got %v", ar["realtime"])
	}
}
