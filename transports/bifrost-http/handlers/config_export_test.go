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
